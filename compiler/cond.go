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

// evalConditions compiles a list of condition expressions, ANDs all resulting
// i1 values together, and incorporates bounds guard checks. The caller must
// call pushBoundsGuard before and popBoundsGuard after.
func (c *Compiler) evalConditions(exprs []ast.Expression, guardPtr llvm.Value) llvm.Value {
	var cond llvm.Value
	for _, expr := range exprs {
		condSyms := c.compileExpression(expr, nil)
		for _, condSym := range condSyms {
			if cond.IsNil() {
				cond = condSym.Val
			} else {
				cond = c.builder.CreateAnd(cond, condSym.Val, "and_cond")
			}
		}
	}

	if c.stmtBoundsUsed() {
		boundsOK := c.createLoad(guardPtr, Int{Width: 1}, "cond_bounds_ok")
		cond = c.builder.CreateAnd(cond, boundsOK, "and_cond_bounds")
	}
	return cond
}

func (c *Compiler) compileConditions(stmt *ast.LetStatement) (cond llvm.Value, hasConditions bool) {
	if len(stmt.Condition) == 0 {
		hasConditions = false
		return
	}

	guardPtr := c.pushBoundsGuard("cond_bounds_guard")
	defer c.popBoundsGuard()

	hasConditions = true
	cond = c.evalConditions(stmt.Condition, guardPtr)
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
		c.promoteExistingSym(name)
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

// resolveConditionalSeed returns the initial value for conditional temp outputs.
// Existing destinations keep their current value on false branches; new
// destinations start from the type zero value.
func (c *Compiler) resolveConditionalSeed(ident *ast.Identifier, outType Type) *Symbol {
	existing, ok := Get(c.Scopes, ident.Value)
	if !ok {
		return c.makeZeroValue(outType)
	}
	return c.derefIfPointer(existing, ident.Value+"_cond_seed")
}

func (c *Compiler) createConditionalTempOutputs(stmt *ast.LetStatement) ([]*ast.Identifier, []Type) {
	outTypes := c.resolvedDestTypes(stmt.Name, c.collectOutTypes(stmt))

	tempNames := make([]*ast.Identifier, len(stmt.Name))
	for i, ident := range stmt.Name {
		tempName := fmt.Sprintf("condtmp_%s_%d", ident.Value, c.tmpCounter)
		c.tmpCounter++
		tempIdent := &ast.Identifier{Value: tempName}

		ptr := c.createEntryBlockAlloca(c.mapToLLVMType(outTypes[i]), tempName+".mem")
		tempSym := &Symbol{
			Val:      ptr,
			Type:     Ptr{Elem: outTypes[i]},
			Borrowed: true,
		}
		seed := c.resolveConditionalSeed(ident, outTypes[i])
		c.storeSymbolToPtrAsType(tempSym, seed, outTypes[i], tempName+"_seed")

		// Temporary conditional outputs are borrowed so scope cleanup does not free
		// values that are transferred to real destinations in the merge block.
		Put(c.Scopes, tempName, tempSym)
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
			c.storeSymbolToPtrAsType(oldSym, finalSym, oldSym.Type.(Ptr).Elem, ident.Value+"_cond_commit")

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

	ifBlock, contBlock := c.createIfCont(cond, "if", "continue")

	c.builder.SetInsertPointAtEnd(ifBlock)
	c.compileCondAssignments(tempNames, stmt.Name, stmt.Value)
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
	c.commitConditionalOutputs(stmt.Name, tempNames, outTypes)
	DeleteBulk(c.Scopes, tempNamesToStrings(tempNames))
}

// valuesHaveCondExpr returns true if any value expression contains an
// embedded cond-expr (scalar comparison in value position).
func (c *Compiler) valuesHaveCondExpr(values []ast.Expression) bool {
	for _, expr := range values {
		if c.hasCondExprInTree(expr) {
			return true
		}
	}
	return false
}

// hasCondExprInTree returns true if any node in the expression tree has
// CondScalar set (a scalar comparison in value position).
func (c *Compiler) hasCondExprInTree(expr ast.Expression) bool {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if info.HasCondScalar() {
		return true
	}
	for _, child := range ast.ExprChildren(expr) {
		if c.hasCondExprInTree(child) {
			return true
		}
	}
	return false
}

// handleComparisons processes each slot of a multi-return comparison based on
// its CondMode. CondScalar slots are compared and ANDed into cond. CondArray
// slots are compiled as array filters (source freed, marked borrowed).
func (c *Compiler) handleComparisons(op string, left, right []*Symbol, info *ExprInfo, cond llvm.Value) ([]*Symbol, llvm.Value) {
	lhsSyms := make([]*Symbol, len(left))
	for i := range left {
		switch info.CompareModes[i] {
		case CondScalar:
			lSym, cmpVal := c.compareScalars(op, left[i], right[i])
			if cond.IsNil() {
				cond = cmpVal
			} else {
				cond = c.builder.CreateAnd(cond, cmpVal, fmt.Sprintf("and_cond_%d", i))
			}
			lhsSyms[i] = lSym
		case CondArray:
			// compileArrayFilter handles deref internally
			lhsSyms[i] = c.compileArrayFilter(op, left[i], right[i], info.OutTypes[i])
			c.freeSymbolValue(left[i], "")
			left[i].Borrowed = true
		}
	}
	return lhsSyms, cond
}

// extractCondExprs walks the expression tree, evaluates each CondScalar
// comparison, ANDs results into cond, and stores LHS values in the
// statement-local condLHS map for substitution during later value compilation.
// LHS temporaries are appended to temps so the caller can free them on the
// false path.
func (c *Compiler) extractCondExprs(expr ast.Expression, cond llvm.Value, temps []condTemp) (llvm.Value, []condTemp) {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]

	// Handle conditional expression (comparison in value position).
	// Comparisons with ranges can be extracted only when all required iterators
	// are already bound by an outer loop (no pending ranges).
	if infix, ok := expr.(*ast.InfixExpression); ok && info.HasCondScalar() && len(c.pendingLoopRanges(info.Ranges)) == 0 {
		// Bottom-up: extract conditions from operands first
		cond, temps = c.extractCondExprs(infix.Left, cond, temps)
		cond, temps = c.extractCondExprs(infix.Right, cond, temps)

		// Compile both operands (may return pre-extracted values)
		left := c.compileExpression(infix.Left, nil)
		right := c.compileExpression(infix.Right, nil)

		var lhsSyms []*Symbol
		lhsSyms, cond = c.handleComparisons(infix.Operator, left, right, info, cond)

		c.requireCondLHSFrame()[key(c.FuncNameMangled, expr)] = lhsSyms
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

// cleanupCondExprElse frees temporaries retained during cond-expr extraction
// that are not consumed when the condition evaluates to false.
func (c *Compiler) cleanupCondExprElse(temps []condTemp) {
	for _, tmp := range temps {
		c.freeTemporary(tmp.expr, tmp.syms)
	}
	for exprKey, lhsSyms := range c.requireCondLHSFrame() {
		exprInfo := c.ExprCache[exprKey]
		for i, mode := range exprInfo.CompareModes {
			if mode == CondArray {
				c.freeSymbolValue(lhsSyms[i], "")
			}
		}
	}
}

// compileCondExprValue extracts cond-expr predicates for expr, branches on the
// combined condition (AND with baseCond when provided), and compiles onTrue on
// the true path only. False path performs standard cond-expr cleanup.
func (c *Compiler) compileCondExprValue(expr ast.Expression, baseCond llvm.Value, onTrue func()) {
	c.pushCondLHSFrame()
	defer c.popCondLHSFrame()

	var temps []condTemp
	cond := baseCond
	cond, temps = c.extractCondExprs(expr, cond, temps)

	if cond.IsNil() {
		onTrue()
		return
	}

	ifBlock, elseBlock, contBlock := c.createIfElseCont(cond, "cond_if", "cond_else", "cond_cont")

	c.builder.SetInsertPointAtEnd(ifBlock)
	onTrue()
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(elseBlock)
	c.cleanupCondExprElse(temps)
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
}

// extractCondRanges collects merged ranges and per-condition compile
// expressions from statement conditions. Returns nil, nil if no condition
// has ranges.
func (c *Compiler) extractCondRanges(conditions []ast.Expression) ([]*RangeInfo, []ast.Expression) {
	var ranges []*RangeInfo
	condExprs := make([]ast.Expression, len(conditions))
	for i, expr := range conditions {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		if len(info.Ranges) > 0 {
			ranges = mergeUses(ranges, info.Ranges)
			if info.Rewrite != nil {
				condExprs[i] = info.Rewrite
			} else {
				condExprs[i] = expr
			}
		} else {
			condExprs[i] = expr
		}
	}
	if len(ranges) == 0 {
		return nil, nil
	}
	return ranges, condExprs
}

// condAccumPattern detects conditional accumulation: ranged conditions with
// 1D array literal values ([...]). Returns merged condition ranges and
// per-condition compile expressions, or nil, nil when the pattern does not match.
func (c *Compiler) condAccumPattern(stmt *ast.LetStatement) ([]*RangeInfo, []ast.Expression) {
	if len(stmt.Condition) == 0 || len(stmt.Value) == 0 {
		return nil, nil
	}
	for _, v := range stmt.Value {
		lit, ok := v.(*ast.ArrayLiteral)
		if !ok || len(lit.Headers) != 0 || len(lit.Rows) != 1 {
			return nil, nil
		}
	}
	return c.extractCondRanges(stmt.Condition)
}

// mergeValueRanges merges value-level ranges from all value expressions
// into a base set of ranges. Returns the combined set.
func (c *Compiler) mergeValueRanges(base []*RangeInfo, values []ast.Expression) []*RangeInfo {
	all := base
	for _, expr := range values {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		if info != nil && len(info.Ranges) > 0 {
			all = mergeUses(all, info.Ranges)
		}
	}
	return all
}

// withCondRangeLoop sets up the shared loop+guard+branch scaffold used by
// both accumulation and iteration paths: loop over all ranges, evaluate
// conditions, branch on the combined result, and call body on the true path.
func (c *Compiler) withCondRangeLoop(allRanges []*RangeInfo, condExprs []ast.Expression, guardName, ifName, contName string, body func()) {
	c.withLoopNest(allRanges, func() {
		guardPtr := c.pushBoundsGuard(guardName)
		combinedCond := c.evalConditions(condExprs, guardPtr)
		c.popBoundsGuard()

		ifBlock, contBlock := c.createIfCont(combinedCond, ifName, contName)

		c.builder.SetInsertPointAtEnd(ifBlock)
		body()
		c.builder.CreateBr(contBlock)

		c.builder.SetInsertPointAtEnd(contBlock)
	})
}

// compileCondAccumStatement lowers ranged conditions with array literal
// values ([...]) into per-iteration accumulation. Result is [] if the
// condition was never true (list-comprehension semantics).
func (c *Compiler) compileCondAccumStatement(stmt *ast.LetStatement, condRanges []*RangeInfo, condExprs []ast.Expression) {
	c.prePromoteConditionalCallArgs(stmt.Value)

	oldValues := c.captureOldValues(stmt.Name)

	accs := make([]*ArrayAccumulator, len(stmt.Value))
	for i, expr := range stmt.Value {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		accs[i] = c.NewArrayAccumulator(info.OutTypes[0].(Array))
	}

	allRanges := c.mergeValueRanges(condRanges, stmt.Value)

	c.withCondRangeLoop(allRanges, condExprs, "accum_cond_guard", "accum_push", "accum_cont", func() {
		c.appendValuesToAccums(accs, stmt.Value)
	})

	for i := range accs {
		result := c.ArrayAccResult(accs[i])
		c.storeValue(stmt.Name[i].Value, result, false)
	}
	for _, old := range oldValues {
		if old == nil || c.skipBorrowedOldValueFree(old) {
			continue
		}
		c.freeSymbolValue(old, "old_accum")
	}
}

// compileCondIterStatement lowers ranged conditions with non-accumulating
// values. Iterates over all ranges (condition + value), evaluates conditions
// per iteration, and assigns values when true (last value wins).
// When values contain embedded cond-exprs, each is wrapped in
// compileCondExprValue to preserve old values when the cond-expr is false.
func (c *Compiler) compileCondIterStatement(stmt *ast.LetStatement, condRanges []*RangeInfo, condExprs []ast.Expression) {
	c.prePromoteConditionalCallArgs(stmt.Value)

	tempNames, outTypes := c.createConditionalTempOutputs(stmt)

	allRanges := c.mergeValueRanges(condRanges, stmt.Value)

	c.withCondRangeLoop(allRanges, condExprs, "cond_iter_guard", "cond_iter_if", "cond_iter_cont", func() {
		if c.valuesHaveCondExpr(stmt.Value) {
			targetIdx := 0
			for _, expr := range stmt.Value {
				info := c.ExprCache[key(c.FuncNameMangled, expr)]
				numOutputs := len(info.OutTypes)
				exprTempNames := tempNames[targetIdx : targetIdx+numOutputs]
				exprDestNames := stmt.Name[targetIdx : targetIdx+numOutputs]
				exprValues := []ast.Expression{expr}
				c.compileCondExprValue(expr, llvm.Value{}, func() {
					c.compileCondAssignments(exprTempNames, exprDestNames, exprValues)
				})
				targetIdx += numOutputs
			}
		} else {
			c.compileCondAssignments(tempNames, stmt.Name, stmt.Value)
		}
	})

	c.commitConditionalOutputs(stmt.Name, tempNames, outTypes)
	DeleteBulk(c.Scopes, tempNamesToStrings(tempNames))
}

// compileCondExprStatement handles let statements that have conditional
// expressions (comparisons) embedded in their value expressions.
// Each value expression is processed independently: its conditions are
// ANDed with statement conditions and branched on separately, so
// p, q = a > 2, d < 10 evaluates each condition independently rather
// than ANDing them all-or-nothing.
func (c *Compiler) compileCondExprStatement(stmt *ast.LetStatement, stmtCond llvm.Value) {
	c.prePromoteConditionalCallArgs(stmt.Value)

	tempNames, outTypes := c.createConditionalTempOutputs(stmt)

	targetIdx := 0
	for _, expr := range stmt.Value {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		numOutputs := len(info.OutTypes)
		exprTempNames := tempNames[targetIdx : targetIdx+numOutputs]
		exprDestNames := stmt.Name[targetIdx : targetIdx+numOutputs]
		exprValues := []ast.Expression{expr}

		c.compileCondExprValue(expr, stmtCond, func() {
			c.compileCondAssignments(exprTempNames, exprDestNames, exprValues)
		})

		targetIdx += numOutputs
	}

	c.commitConditionalOutputs(stmt.Name, tempNames, outTypes)
	DeleteBulk(c.Scopes, tempNamesToStrings(tempNames))
}
