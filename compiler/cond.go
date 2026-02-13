package compiler

import (
	"fmt"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

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
	switch e := expr.(type) {
	case *ast.CallExpression:
		for _, arg := range e.Arguments {
			if ident, ok := arg.(*ast.Identifier); ok {
				out[ident.Value] = struct{}{}
			}
			collectCallArgIdentifiers(arg, out)
		}
	case *ast.InfixExpression:
		collectCallArgIdentifiers(e.Left, out)
		collectCallArgIdentifiers(e.Right, out)
	case *ast.PrefixExpression:
		collectCallArgIdentifiers(e.Right, out)
	case *ast.ArrayLiteral:
		for _, row := range e.Rows {
			for _, cell := range row {
				collectCallArgIdentifiers(cell, out)
			}
		}
	case *ast.ArrayRangeExpression:
		collectCallArgIdentifiers(e.Array, out)
		collectCallArgIdentifiers(e.Range, out)
	case *ast.RangeLiteral:
		collectCallArgIdentifiers(e.Start, out)
		collectCallArgIdentifiers(e.Stop, out)
		if e.Step != nil {
			collectCallArgIdentifiers(e.Step, out)
		}
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

func (c *Compiler) collectConditionalOutTypes(stmt *ast.LetStatement) ([]Type, bool) {
	outTypes := []Type{}
	for _, expr := range stmt.Value {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		if info == nil {
			c.Errors = append(c.Errors, &token.CompileError{
				Token: stmt.Token,
				Msg:   fmt.Sprintf("missing type info for conditional expression %T", expr),
			})
			return nil, false
		}
		outTypes = append(outTypes, info.OutTypes...)
	}
	return outTypes, true
}

func (c *Compiler) createConditionalTempOutputs(stmt *ast.LetStatement) ([]*ast.Identifier, []Type, bool) {
	outTypes, ok := c.collectConditionalOutTypes(stmt)
	if !ok {
		return nil, nil, false
	}
	if len(outTypes) != len(stmt.Name) {
		c.Errors = append(c.Errors, &token.CompileError{
			Token: stmt.Token,
			Msg:   fmt.Sprintf("conditional outputs mismatch: got %d values for %d targets", len(outTypes), len(stmt.Name)),
		})
		return nil, nil, false
	}

	tempNames := make([]*ast.Identifier, len(stmt.Name))
	for i, ident := range stmt.Name {
		if outTypes[i].Kind() == UnresolvedKind {
			c.Errors = append(c.Errors, &token.CompileError{
				Token: ident.Token,
				Msg:   fmt.Sprintf("conditional rhs output type for %q is unresolved", ident.Value),
			})
			return nil, nil, false
		}

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
	return tempNames, outTypes, true
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

	tempNames, outTypes, ok := c.createConditionalTempOutputs(stmt)
	if !ok {
		return
	}

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

// condExprChildren returns the immediate child expressions of an AST node.
func condExprChildren(expr ast.Expression) []ast.Expression {
	switch e := expr.(type) {
	case *ast.InfixExpression:
		return []ast.Expression{e.Left, e.Right}
	case *ast.PrefixExpression:
		return []ast.Expression{e.Right}
	case *ast.CallExpression:
		return e.Arguments
	case *ast.ArrayLiteral:
		if len(e.Rows) == 1 {
			return e.Rows[0]
		}
	case *ast.ArrayRangeExpression:
		return []ast.Expression{e.Array, e.Range}
	}
	return nil
}

// hasCondExprInTree returns true if any node in the expression tree has
// IsCondExpr set (a comparison in value position).
func (c *Compiler) hasCondExprInTree(expr ast.Expression) bool {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if info != nil && info.IsCondExpr {
		return true
	}
	for _, child := range condExprChildren(expr) {
		if c.hasCondExprInTree(child) {
			return true
		}
	}
	return false
}

// cascadeCondExprs walks the expression tree and emits cascading conditional
// branches for each IsCondExpr comparison. For each comparison:
//  1. Recurse into operands first (bottom-up for nested cases)
//  2. Compile both operands
//  3. Emit comparison → i1
//  4. Branch: true → new cond_pass block, false → skipBlock
//  5. Store LHS value in condExprValues for later substitution
func (c *Compiler) cascadeCondExprs(expr ast.Expression, skipBlock llvm.BasicBlock) {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if info == nil {
		return
	}

	// Handle conditional expression (comparison in value position)
	if infix, ok := expr.(*ast.InfixExpression); ok && info.IsCondExpr {
		// Bottom-up: extract conditions from operands first
		c.cascadeCondExprs(infix.Left, skipBlock)
		c.cascadeCondExprs(infix.Right, skipBlock)

		// Compile both operands (may return pre-extracted values)
		left := c.compileExpression(infix.Left, nil)
		right := c.compileExpression(infix.Right, nil)

		// Emit comparison via operator table → i1
		lSym := c.derefIfPointer(left[0], "")
		rSym := c.derefIfPointer(right[0], "")
		cmpResult := defaultOps[opKey{
			Operator:  infix.Operator,
			LeftType:  lSym.Type.Key(),
			RightType: rSym.Type.Key(),
		}](c, lSym, rSym, true)

		// Branch: true → continue cascade, false → skip
		fn := c.builder.GetInsertBlock().Parent()
		passBlock := c.Context.AddBasicBlock(fn, "cond_pass")
		c.builder.CreateCondBr(cmpResult.Val, passBlock, skipBlock)
		c.builder.SetInsertPointAtEnd(passBlock)

		// Store LHS for substitution during value compilation
		c.condExprValues[infix] = lSym
		return
	}

	// Not a conditional expression — recurse into children
	for _, child := range condExprChildren(expr) {
		c.cascadeCondExprs(child, skipBlock)
	}
}

// compileCondExprStatement handles let statements that have conditional
// expressions (comparisons) embedded in their value expressions.
// It cascades through conditions with short-circuit evaluation, then compiles
// the value expressions in the true path with comparisons replaced by their
// pre-extracted LHS values.
func (c *Compiler) compileCondExprStatement(stmt *ast.LetStatement, stmtCond llvm.Value, hasStmtCond bool) {
	c.prePromoteConditionalCallArgs(stmt.Value)

	tempNames, outTypes, ok := c.createConditionalTempOutputs(stmt)
	if !ok {
		return
	}

	fn := c.builder.GetInsertBlock().Parent()
	contBlock := c.Context.AddBasicBlock(fn, "continue")

	// Branch on statement-level condition first (if any)
	if hasStmtCond {
		stmtPassBlock := c.Context.AddBasicBlock(fn, "stmt_cond_pass")
		c.builder.CreateCondBr(stmtCond, stmtPassBlock, contBlock)
		c.builder.SetInsertPointAtEnd(stmtPassBlock)
	}

	// Cascade through embedded conditional expressions
	c.condExprValues = make(map[ast.Expression]*Symbol)
	for _, expr := range stmt.Value {
		c.cascadeCondExprs(expr, contBlock)
	}

	// All conditions passed — compile assignments into temp slots
	c.compileCondAssignments(tempNames, stmt.Name, stmt.Value)
	c.builder.CreateBr(contBlock)

	// Continue block: commit temp values to real destinations
	c.builder.SetInsertPointAtEnd(contBlock)
	c.commitConditionalOutputs(stmt.Name, tempNames, outTypes)
	DeleteBulk(c.Scopes, tempNamesToStrings(tempNames))

	c.condExprValues = nil
}
