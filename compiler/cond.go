package compiler

import (
	"fmt"

	"github.com/thiremani/pluto/ast"
	"tinygo.org/x/go-llvm"
)

// condTemp holds a pre-compiled LHS operand and its source expression, used to
// free heap temporaries on the false branch of conditional expression lowering.
type condTemp struct {
	expr ast.Expression
	syms []*Symbol
}

func (c *Compiler) compileConditions(stmt *ast.LetStatement) (cond llvm.Value, hasConditions bool) {
	if len(stmt.Condition) == 0 {
		hasConditions = false
		return
	}

	hasConditions = true
	for i, expr := range stmt.Condition {
		condSyms := c.compileExpression(expr, nil)
		for idx, condSym := range condSyms {
			if i == 0 && idx == 0 {
				cond = condSym.Val
				continue
			}
			cond = c.builder.CreateAnd(cond, condSym.Val, "and_cond")
		}
	}
	return
}

// collectCallArgIdentifiers walks an expression and records identifiers that
// appear inside call argument subexpressions. These identifiers may be promoted
// to memory by compileArgs, so conditional lowering pre-promotes them before
// branching.
func collectCallArgIdentifiers(expr ast.Expression, out map[string]struct{}) {
	if ce, ok := expr.(*ast.CallExpression); ok {
		for _, arg := range ce.Arguments {
			if ident, ok := arg.(*ast.Identifier); ok {
				out[ident.Value] = struct{}{}
			}
		}
	}
	for _, child := range ast.ExprChildren(expr) {
		collectCallArgIdentifiers(child, out)
	}
}

// prePromoteConditionalCallArgs promotes local identifiers that are used as call
// arguments so branch codegen does not introduce path-dependent promotions.
func (c *Compiler) prePromoteConditionalCallArgs(exprs []ast.Expression) {
	argNames := make(map[string]struct{})
	for _, expr := range exprs {
		collectCallArgIdentifiers(expr, argNames)
	}

	for name := range argNames {
		sym, ok := Get(c.Scopes, name)
		if !ok {
			continue
		}
		if sym.Type.Kind() == PtrKind {
			continue
		}
		c.promoteToMemory(name)
	}
}

func (c *Compiler) collectOutTypes(stmt *ast.LetStatement) []Type {
	outTypes := []Type{}
	for _, expr := range stmt.Value {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		outTypes = append(outTypes, info.OutTypes...)
	}
	return outTypes
}

func (c *Compiler) createConditionalTempOutputs(stmt *ast.LetStatement) ([]*ast.Identifier, []Type) {
	outTypes := c.collectOutTypes(stmt)

	tempNames := make([]*ast.Identifier, len(stmt.Name))
	for i, ident := range stmt.Name {
		tempName := fmt.Sprintf("condtmp_%s_%d", ident.Value, c.tmpCounter)
		c.tmpCounter++
		tempIdent := &ast.Identifier{Value: tempName}

		ptr := c.createEntryBlockAlloca(c.mapToLLVMType(outTypes[i]), tempName+".mem")
		seed := c.makeZeroValue(outTypes[i])
		if existing, ok := Get(c.Scopes, ident.Value); ok {
			existingType := existing.Type
			if ptrType, isPtr := existingType.(Ptr); isPtr {
				existingType = ptrType.Elem
			}
			// Never reinterpret an unresolved old value as a concrete type.
			// If metadata is unresolved, keep the zero seed for this temp slot.
			if existingType.Kind() != UnresolvedKind {
				seed = c.derefIfPointer(existing, ident.Value+"_cond_seed")
			}
		}
		c.createStore(seed.Val, ptr, outTypes[i])

		// Temporary conditional outputs are borrowed so scope cleanup does not free
		// values that are transferred to real destinations in the merge block.
		Put(c.Scopes, tempName, &Symbol{
			Val:      ptr,
			Type:     Ptr{Elem: outTypes[i]},
			Borrowed: true,
		})
		tempNames[i] = tempIdent
	}
	return tempNames, outTypes
}

func (c *Compiler) commitConditionalOutputs(dest []*ast.Identifier, tempNames []*ast.Identifier, outTypes []Type) {
	for i, ident := range dest {
		tempSym, _ := Get(c.Scopes, tempNames[i].Value)

		finalType := outTypes[i]
		finalVal := c.createLoad(tempSym.Val, finalType, ident.Value+"_cond_final")
		finalSym := &Symbol{
			Val:  finalVal,
			Type: finalType,
		}

		oldSym, exists := Get(c.Scopes, ident.Value)
		if !exists {
			Put(c.Scopes, ident.Value, finalSym)
			continue
		}

		if _, ok := oldSym.Type.(Ptr); ok {
			c.createStore(finalVal, oldSym.Val, finalType)

			// Keep pointer element type in sync (important for string ownership flags).
			updated := GetCopy(oldSym)
			updated.Type = Ptr{Elem: finalType}
			if !SetExisting(c.Scopes, ident.Value, updated) {
				Put(c.Scopes, ident.Value, updated)
			}
			continue
		}

		// Non-pointer symbols are replaced directly. Old value ownership is already
		// handled in the IF branch assignment into temp slots.
		Put(c.Scopes, ident.Value, finalSym)
	}
}

func tempNamesToStrings(tempNames []*ast.Identifier) []string {
	names := make([]string, len(tempNames))
	for i, ident := range tempNames {
		names[i] = ident.Value
	}
	return names
}

// aliasCondDests maps existing destination names to conditional temp slots so
// RHS reads during IF-branch assignment see the latest temp writes.
func (c *Compiler) aliasCondDests(dest []*ast.Identifier, tempNames []*ast.Identifier) map[string]*Symbol {
	aliases := make(map[string]*Symbol, len(dest))

	for i, ident := range dest {
		oldSym, exists := Get(c.Scopes, ident.Value)
		if !exists {
			continue
		}
		tempSym, ok := Get(c.Scopes, tempNames[i].Value)
		if !ok {
			continue
		}
		aliases[ident.Value] = oldSym
		SetExisting(c.Scopes, ident.Value, tempSym)
	}

	return aliases
}

func (c *Compiler) restoreCondDests(aliases map[string]*Symbol) {
	for name, oldSym := range aliases {
		SetExisting(c.Scopes, name, oldSym)
	}
}

// compileCondAssignments wraps compileAssignments for conditional lowering.
// Existing destination names are temporarily aliased to temp slots so
// self-referential RHS expressions read/write the same evolving slot.
func (c *Compiler) compileCondAssignments(tempNames []*ast.Identifier, dest []*ast.Identifier, exprs []ast.Expression) {
	aliases := c.aliasCondDests(dest, tempNames)
	c.compileAssignments(tempNames, dest, exprs)
	c.restoreCondDests(aliases)
}

// compileCondStatement lowers:
//
//	name = cond value
//
// to:
//
// 1. Allocate per-output temp slots seeded with current destination value (or zero for new vars)
// 2. IF branch: alias existing destination names to temp slots, then compile assignment
// 3. ELSE branch: no-op (seed values already represent the else result)
// 4. Merge: commit temp slot values to real destinations once
func (c *Compiler) compileCondStatement(stmt *ast.LetStatement, cond llvm.Value) {
	// compileArgs may promote identifier call args by creating an alloca in entry
	// and storing the current value at the call site. If promotion happens only in
	// the IF block, the false path would skip that store and later loads can read
	// uninitialized memory. Pre-promote here so storage is initialized on all paths.
	c.prePromoteConditionalCallArgs(stmt.Value)

	tempNames, outTypes := c.createConditionalTempOutputs(stmt)

	fn := c.builder.GetInsertBlock().Parent()
	ifBlock := c.Context.AddBasicBlock(fn, "if")
	contBlock := c.Context.AddBasicBlock(fn, "continue")
	c.builder.CreateCondBr(cond, ifBlock, contBlock)

	c.builder.SetInsertPointAtEnd(ifBlock)
	c.compileCondAssignments(tempNames, stmt.Name, stmt.Value)
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
	c.commitConditionalOutputs(stmt.Name, tempNames, outTypes)
	DeleteBulk(c.Scopes, tempNamesToStrings(tempNames))
}

// hasCondExprInTree returns true if any node in the expression tree has
// CondScalar set (a scalar comparison in value position).
func (c *Compiler) hasCondExprInTree(expr ast.Expression) bool {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if info.CompareMode == CondScalar {
		return true
	}
	for _, child := range ast.ExprChildren(expr) {
		if c.hasCondExprInTree(child) {
			return true
		}
	}
	return false
}

// andScalarComparisons compares each element pair of left and right using op,
// ANDs the results into cond, and returns the dereferenced LHS symbols.
// Array-typed pairs are skipped (handled by array filtering).
func (c *Compiler) andScalarComparisons(op string, left, right []*Symbol, cond llvm.Value) ([]*Symbol, llvm.Value) {
	lhsSyms := make([]*Symbol, len(left))
	for i := range left {
		lSym := c.derefIfPointer(left[i], "")
		rSym := c.derefIfPointer(right[i], "")

		if lSym.Type.Kind() == ArrayKind || rSym.Type.Kind() == ArrayKind {
			continue
		}

		cmpResult := defaultOps[opKey{
			Operator:  op,
			LeftType:  opType(lSym.Type.Key()),
			RightType: opType(rSym.Type.Key()),
		}](c, lSym, rSym, true)

		if cond.IsNil() {
			cond = cmpResult.Val
		} else {
			cond = c.builder.CreateAnd(cond, cmpResult.Val, "and_cond")
		}
		lhsSyms[i] = lSym
	}
	return lhsSyms, cond
}

// extractCondExprs walks the expression tree, evaluates each CondScalar
// comparison, ANDs results into cond, and stores LHS values in c.condLHS
// for substitution during later value compilation. LHS temporaries are
// appended to temps so the caller can free them on the false path.
func (c *Compiler) extractCondExprs(expr ast.Expression, cond llvm.Value, temps []condTemp) (llvm.Value, []condTemp) {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]

	// Handle conditional expression (comparison in value position)
	if infix, ok := expr.(*ast.InfixExpression); ok && info.CompareMode == CondScalar {
		// Bottom-up: extract conditions from operands first
		cond, temps = c.extractCondExprs(infix.Left, cond, temps)
		cond, temps = c.extractCondExprs(infix.Right, cond, temps)

		// Compile both operands (may return pre-extracted values)
		left := c.compileExpression(infix.Left, nil)
		right := c.compileExpression(infix.Right, nil)

		var lhsSyms []*Symbol
		lhsSyms, cond = c.andScalarComparisons(infix.Operator, left, right, cond)

		c.condLHS[key(c.FuncNameMangled, expr)] = lhsSyms
		temps = append(temps, condTemp{infix.Left, left})
		// Free right-side temporaries (only used for comparison).
		// Left-side values are retained in condLHS for later substitution.
		c.freeTemporary(infix.Right, right)
		return cond, temps
	}

	// Not a conditional expression — recurse into children
	for _, child := range ast.ExprChildren(expr) {
		cond, temps = c.extractCondExprs(child, cond, temps)
	}
	return cond, temps
}

// compileCondExprStatement handles let statements that have conditional
// expressions (comparisons) embedded in their value expressions.
// It extracts all conditions, ANDs them with statement conditions, then
// branches once. Value expressions are compiled in the true path with
// comparisons replaced by their pre-extracted LHS values via c.condLHS.
func (c *Compiler) compileCondExprStatement(stmt *ast.LetStatement, stmtCond llvm.Value) {
	c.prePromoteConditionalCallArgs(stmt.Value)

	tempNames, outTypes := c.createConditionalTempOutputs(stmt)

	// Save and initialize statement-local condLHS map for extraction.
	// Save/restore handles re-entrant calls (e.g. nested cond-expr in callee).
	savedCondLHS := c.condLHS
	c.condLHS = make(map[ExprKey][]*Symbol)

	// Combine statement conditions with embedded conditional expressions.
	// temps collects LHS operands compiled before the branch for false-path cleanup.
	var temps []condTemp
	cond := stmtCond
	for _, expr := range stmt.Value {
		cond, temps = c.extractCondExprs(expr, cond, temps)
	}

	// Branch: true → compute values, false → free pre-compiled LHS temporaries
	fn := c.builder.GetInsertBlock().Parent()
	ifBlock := c.Context.AddBasicBlock(fn, "if")
	elseBlock := c.Context.AddBasicBlock(fn, "else")
	contBlock := c.Context.AddBasicBlock(fn, "continue")
	c.builder.CreateCondBr(cond, ifBlock, elseBlock)

	c.builder.SetInsertPointAtEnd(ifBlock)
	c.compileCondAssignments(tempNames, stmt.Name, stmt.Value)
	c.builder.CreateBr(contBlock)

	// Else block: free LHS temporaries that won't be consumed.
	// extractCondExprs compiles LHS operands before the branch; on the true
	// path they are consumed by assignment, but on the false path they are
	// orphaned and must be freed to avoid leaking heap types (e.g. strings).
	c.builder.SetInsertPointAtEnd(elseBlock)
	for _, tmp := range temps {
		c.freeTemporary(tmp.expr, tmp.syms)
	}
	c.builder.CreateBr(contBlock)

	// Continue block: commit temp values to real destinations
	c.builder.SetInsertPointAtEnd(contBlock)
	c.commitConditionalOutputs(stmt.Name, tempNames, outTypes)
	DeleteBulk(c.Scopes, tempNamesToStrings(tempNames))

	// Restore previous state (supports re-entrant cond-expr compilation)
	c.condLHS = savedCondLHS
}
