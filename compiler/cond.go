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

func (c *Compiler) addCompileError(tok token.Token, msg string) {
	c.Errors = append(c.Errors, &token.CompileError{
		Token: tok,
		Msg:   msg,
	})
}

// collectCallArgIdentifiers walks an expression and records identifiers used as
// direct call arguments. These identifiers may be promoted to memory by
// compileArgs, so conditional lowering pre-promotes them in the pre-branch block.
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
			c.addCompileError(stmt.Token, fmt.Sprintf("missing type info for conditional expression %T", expr))
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
		c.addCompileError(
			stmt.Token,
			fmt.Sprintf("conditional outputs mismatch: got %d values for %d targets", len(outTypes), len(stmt.Name)),
		)
		return nil, nil, false
	}

	tempNames := make([]*ast.Identifier, len(stmt.Name))
	for i, ident := range stmt.Name {
		if outTypes[i].Kind() == UnresolvedKind {
			c.addCompileError(
				ident.Token,
				fmt.Sprintf("conditional rhs output type for %q is unresolved", ident.Value),
			)
			return nil, nil, false
		}

		tempName := fmt.Sprintf("__cond_tmp_%d_%s", c.tmpCounter, ident.Value)
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
		tempSym, ok := Get(c.Scopes, tempNames[i].Value)
		if !ok {
			c.addCompileError(ident.Token, "missing conditional temp output: "+tempNames[i].Value)
			return
		}

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

func (c *Compiler) removeConditionalTemps(tempNames []*ast.Identifier) {
	current := c.Scopes[len(c.Scopes)-1].Elems
	for _, ident := range tempNames {
		delete(current, ident.Value)
	}
}

// compileCondStatement lowers:
//
//	name = cond value
//
// to:
//
// 1. Allocate per-output temp slots seeded with current destination value (or zero for new vars)
// 2. IF branch: compile assignment into temp slots using normal assignment/free rules
// 3. ELSE branch: no-op (seed values already represent the else result)
// 4. Merge: commit temp slot values to real destinations once
func (c *Compiler) compileCondStatement(stmt *ast.LetStatement, cond llvm.Value) {
	// compileArgs may promote identifier call args to memory. Pre-promote before branching
	// so both branches observe the same storage model.
	c.prePromoteConditionalCallArgs(stmt.Value)

	tempNames, outTypes, ok := c.createConditionalTempOutputs(stmt)
	if !ok {
		return
	}

	fn := c.builder.GetInsertBlock().Parent()
	ifBlock := c.Context.AddBasicBlock(fn, "if")
	elseBlock := c.Context.AddBasicBlock(fn, "else")
	contBlock := c.Context.AddBasicBlock(fn, "continue")
	c.builder.CreateCondBr(cond, ifBlock, elseBlock)

	c.builder.SetInsertPointAtEnd(ifBlock)
	c.compileAssignments(tempNames, stmt.Name, stmt.Value)
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(elseBlock)
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
	c.commitConditionalOutputs(stmt.Name, tempNames, outTypes)
	c.removeConditionalTemps(tempNames)
}
