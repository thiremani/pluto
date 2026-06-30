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

// compileGate lowers a value-position condition expression to its i1 gate: the
// conjunction of the comparisons it contains. A condition is just a value-position
// expression we ask "did it yield?" of — comparisons yield their LHS and chain, so
// `i > 2 < 8` gates on `i > 2 AND i < 8`. The retained LHS values drive the gate
// but are not a result, so they are freed here (a heap LHS computed only to test a
// gate must not leak). Returns a nil value when expr contributes no comparison
// (e.g. a bare range driver, whose admitted domain is handled by the loop).
func (c *Compiler) compileGate(expr ast.Expression) llvm.Value {
	// No || in the tree: the gate is the conjunction of the comparisons, which
	// extractCondExprs computes directly while freeing the retained LHS temps
	// (a heap LHS tested only for the gate must not leak).
	if !c.hasFallbackOrInTree(expr) {
		c.pushCondLHSFrame()
		defer c.popCondLHSFrame()
		cond, temps := c.extractCondExprs(expr, llvm.Value{}, nil)
		c.cleanupCondExprElse(temps)
		return cond
	}

	// A value-position || is a left-biased fallback, so its gate is "did the
	// expression yield?" — a > 2 || b > 3 gates on a>2 OR b>3, not AND. Reuse the
	// value lowering: record the gate on the yield path and free the yielded value
	// (a gate discards it; freeTemporary skips borrowed operands, so variables are
	// safe and heap temporaries are released).
	i1 := Int{Width: 1}
	i1Ty := c.mapToLLVMType(i1)
	gatePtr := c.createEntryBlockAlloca(i1Ty, "gate.mem")
	c.createStore(llvm.ConstInt(i1Ty, 0, false), gatePtr, i1)
	c.compileCondExprValue(expr, llvm.Value{}, func() {
		vals := c.compileExpression(expr, nil)
		c.freeTemporary(expr, vals)
		c.createStore(llvm.ConstInt(i1Ty, 1, false), gatePtr, i1)
	})
	return c.createLoad(gatePtr, i1, "gate")
}

// andGates ANDs the i1 gates of a list of condition expressions ("did every
// condition yield?"). Callers pass a non-empty list of comparisons, so each
// compileGate returns a non-nil gate and the result is non-nil. Bounds-guard
// folding is the caller's concern (only the statement path needs it).
func (c *Compiler) andGates(exprs []ast.Expression) llvm.Value {
	var cond llvm.Value
	for _, expr := range exprs {
		gate := c.compileGate(expr)
		if cond.IsNil() {
			cond = gate
		} else {
			cond = c.builder.CreateAnd(cond, gate, "and_cond")
		}
	}
	return cond
}

// evalConditions ANDs the i1 gates of a list of condition expressions and folds
// in the bounds-guard check. The caller must pushBoundsGuard before and
// popBoundsGuard after.
func (c *Compiler) evalConditions(exprs []ast.Expression, guardPtr llvm.Value) llvm.Value {
	// Every condition reaching evalConditions is a comparison: validation rejects
	// non-comparison gates, and splitCondRanges routes bare range drivers into the
	// range list rather than here. So compileGate returns a non-nil gate for each,
	// and since callers pass a non-empty list, cond is non-nil after the loop.
	cond := c.andGates(exprs)

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

// collectPromotableCallArgIdentifiers walks an expression and records bare
// identifier call arguments that lower indirectly. Those identifiers may be
// promoted to memory by lowerCallArgs, so conditional lowering pre-promotes
// only that subset before branching.
func (c *Compiler) collectPromotableCallArgIdentifiers(expr ast.Expression, out map[string]struct{}) {
	if ce, ok := expr.(*ast.CallExpression); ok {
		c.addPromotableArgs(ce, out)
	}
	for _, child := range ast.ExprChildren(expr) {
		c.collectPromotableCallArgIdentifiers(child, out)
	}
}

func (c *Compiler) addPromotableArgs(ce *ast.CallExpression, out map[string]struct{}) {
	info := c.ExprCache[key(c.FuncNameMangled, ce)]
	if info == nil {
		return
	}

	paramTypes := c.inferCallParamTypes(info)
	mangled := Mangle(c.MangledPath, ce.Function.Value, paramTypes)
	fnInfo := c.FuncCache[mangled]
	if fnInfo == nil {
		return
	}

	abi := classifyFuncABI(paramTypes, fnInfo.OutTypes)
	for i, arg := range ce.Arguments {
		if abi.Params[i].Mode != ABIParamIndirect {
			continue
		}
		ident, ok := arg.(*ast.Identifier)
		if !ok {
			continue
		}
		out[ident.Value] = struct{}{}
	}
}

// prePromoteConditionalCallArgs promotes local identifiers that are used as
// indirect call arguments so branch codegen does not introduce path-dependent
// promotions.
func (c *Compiler) prePromoteConditionalCallArgs(exprs []ast.Expression) {
	argNames := make(map[string]struct{})
	for _, expr := range exprs {
		c.collectPromotableCallArgIdentifiers(expr, argNames)
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

// resolveDestSeed returns the current destination value when it already exists
// in scope, or the type zero value for a fresh destination.
func (c *Compiler) resolveDestSeed(ident *ast.Identifier, outType Type) *Symbol {
	existing, ok := Get(c.Scopes, ident.Value)
	if !ok {
		return c.makeZeroValue(outType)
	}
	return c.valueSymbol(ident.Value, existing, ident.Value+"_cond_seed")
}

func (c *Compiler) createConditionalTempOutputs(stmt *ast.LetStatement) ([]*ast.Identifier, []Type) {
	outTypes := c.resolvedDestTypes(stmt.Name, c.collectOutTypes(stmt))
	return c.createConditionalTempOutputsFor(stmt.Name, outTypes), outTypes
}

func (c *Compiler) createConditionalTempOutputsFor(dest []*ast.Identifier, outTypes []Type) []*ast.Identifier {
	tempNames := make([]*ast.Identifier, len(dest))
	for i, ident := range dest {
		tempName := fmt.Sprintf("condtmp_%s_%d", ident.Value, c.tmpCounter)
		c.tmpCounter++
		tempIdent := &ast.Identifier{Value: tempName}

		ptr := c.createEntryBlockAlloca(c.mapToLLVMType(outTypes[i]), tempName+".mem")
		tempSym := &Symbol{
			Val:      ptr,
			Type:     Ptr{Elem: outTypes[i]},
			Borrowed: true,
		}
		seed := c.resolveDestSeed(ident, outTypes[i])
		c.storeSymbolToSlot(tempSym, seed, outTypes[i], tempName+"_seed")

		// Temporary conditional outputs are borrowed so scope cleanup does not free
		// values that are transferred to real destinations in the merge block.
		Put(c.Scopes, tempName, tempSym)
		tempNames[i] = tempIdent
	}
	return tempNames
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
			c.storeSymbolToSlot(oldSym, finalSym, oldSym.Type.(Ptr).Elem, ident.Value+"_cond_commit")

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

func (c *Compiler) compileCondAssignmentValues(
	tempNames []*ast.Identifier,
	dest []*ast.Identifier,
	exprs []ast.Expression,
) ([]*Symbol, []*Symbol, []string, []int) {
	aliases := c.aliasCondDests(dest, tempNames)
	oldValues := c.captureOldValues(tempNames)
	syms, rhsNames, resCounts := c.compileAssignmentValues(tempNames, exprs)
	c.restoreCondDests(aliases)
	return oldValues, syms, rhsNames, resCounts
}

func (c *Compiler) compileCondAssignmentsWithGuard(tempNames []*ast.Identifier, dest []*ast.Identifier, exprs []ast.Expression, guardPtr llvm.Value) {
	oldValues, syms, rhsNames, resCounts := c.compileCondAssignmentValues(tempNames, dest, exprs)
	c.finishAssignmentsWithGuard(tempNames, dest, exprs, oldValues, syms, rhsNames, resCounts, guardPtr)
}

func (c *Compiler) createStageTempOutputsFor(dest []*ast.Identifier) []*ast.Identifier {
	tempNames := make([]*ast.Identifier, len(dest))
	for i, ident := range dest {
		commitTempSym, _ := Get(c.Scopes, ident.Value)
		ptrType := commitTempSym.Type.(Ptr)
		outType := ptrType.Elem

		tempName := fmt.Sprintf("condstage_%s_%d", ident.Value, c.tmpCounter)
		c.tmpCounter++
		tempIdent := &ast.Identifier{Value: tempName}

		ptr := c.createEntryBlockAlloca(c.mapToLLVMType(outType), tempName+".mem")
		stageTempSym := &Symbol{
			Val:      ptr,
			Type:     Ptr{Elem: outType},
			Borrowed: true,
		}
		seed := c.resolveDestSeed(ident, outType)
		seed = c.deepCopyIfNeeded(seed)
		c.storeSymbolToSlot(stageTempSym, seed, outType, tempName+"_seed")
		Put(c.Scopes, tempName, stageTempSym)
		tempNames[i] = tempIdent
	}
	return tempNames
}

func (c *Compiler) commitStageTempOutputs(dest []*ast.Identifier, stageTempNames []*ast.Identifier) {
	for i, ident := range dest {
		stageSym, _ := Get(c.Scopes, stageTempNames[i].Value)
		destSym, _ := Get(c.Scopes, ident.Value)

		oldValue := c.valueSymbol(ident.Value, destSym, ident.Value+"_stage_old")
		ptrType := destSym.Type.(Ptr)

		stagedValue := c.valueSymbol(stageTempNames[i].Value, stageSym, stageTempNames[i].Value+"_stage_final")
		c.storeSymbolToSlot(destSym, stagedValue, ptrType.Elem, ident.Value+"_stage_commit")

		if c.skipBorrowedOldValueFree(oldValue) {
			continue
		}
		c.freeSymbolValue(oldValue, ident.Value+"_stage_old")
	}
}

type condStageGroup struct {
	commitTempNames []*ast.Identifier
	stageTempNames  []*ast.Identifier
}

func (c *Compiler) stageCondRangedExpr(expr ast.Expression, dest []*ast.Identifier, stageTempNames []*ast.Identifier) {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	stageAliases := c.aliasCondDests(dest, stageTempNames)
	defer c.restoreCondDests(stageAliases)

	compileStageAssign := func() {
		guardPtr := c.pushBoundsGuard("cond_value_guard")
		defer c.popBoundsGuard()

		c.compileCondAssignmentsWithGuard(stageTempNames, dest, []ast.Expression{expr}, guardPtr)
	}

	c.withLoopNestVersioned(info.Ranges, []ast.Expression{expr}, func() {
		if c.hasCondExprInTree(expr) {
			c.compileCondExprValue(expr, llvm.Value{}, compileStageAssign)
			return
		}

		compileStageAssign()
	})
}

func (c *Compiler) stageCondRangedAssignments(
	assignExprs []ast.Expression,
	assignDests []*ast.Identifier,
	commitTempNames []*ast.Identifier,
) []condStageGroup {
	assignTargetIdx := 0
	groups := make([]condStageGroup, 0, len(assignExprs))

	// Two alias layers are active here. The outer alias makes destination reads
	// resolve to commit temps while stage temps are seeded. stageCondRangedExpr
	// then temporarily aliases the same destinations to private stage temps so
	// self-referential RHS expressions read/write only that staged result.
	allAliases := c.aliasCondDests(assignDests, commitTempNames)
	defer c.restoreCondDests(allAliases)

	for _, expr := range assignExprs {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		numOutputs := len(info.OutTypes)
		exprCommitTempNames := commitTempNames[assignTargetIdx : assignTargetIdx+numOutputs]
		exprDestNames := assignDests[assignTargetIdx : assignTargetIdx+numOutputs]
		stageTempNames := c.createStageTempOutputsFor(exprCommitTempNames)

		c.stageCondRangedExpr(expr, exprDestNames, stageTempNames)
		groups = append(groups, condStageGroup{
			commitTempNames: exprCommitTempNames,
			stageTempNames:  stageTempNames,
		})
		assignTargetIdx += numOutputs
	}

	return groups
}

func (c *Compiler) commitCondRangedStages(groups []condStageGroup) {
	for _, group := range groups {
		c.commitStageTempOutputs(group.commitTempNames, group.stageTempNames)
		DeleteBulk(c.Scopes, tempNamesToStrings(group.stageTempNames))
	}
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
// conditional value lowering.
func (c *Compiler) hasCondExprInTree(expr ast.Expression) bool {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if info != nil && info.HasCondExpr() {
		return true
	}
	for _, child := range ast.ExprChildren(expr) {
		if c.hasCondExprInTree(child) {
			return true
		}
	}
	return false
}

func (c *Compiler) fallbackOrExpr(expr ast.Expression) (*ast.InfixExpression, bool) {
	infix, ok := ast.IsLogicalOr(expr)
	if !ok {
		return nil, false
	}

	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	return infix, info.HasFallbackOr()
}

func (c *Compiler) hasFallbackOrInTree(expr ast.Expression) bool {
	if _, ok := c.fallbackOrExpr(expr); ok {
		return true
	}
	// Array cells and (cond value) nodes resolve any || at their own level (see
	// extractCondExprs), so they are boundaries for statement-level fallback
	// detection — don't descend into them.
	switch expr.(type) {
	case *ast.ArrayLiteral, *ast.CondValueExpr:
		return false
	}
	for _, child := range ast.ExprChildren(expr) {
		if c.hasFallbackOrInTree(child) {
			return true
		}
	}
	return false
}

// handleComparisons processes each slot of a multi-return comparison based on
// its CondMode. CondScalar slots are compared and ANDed into cond. CondArray
// slots are compiled as element-wise array masks (source freed, marked borrowed).
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
			// compileArrayMask handles deref internally
			lhsSyms[i] = c.compileArrayMask(op, left[i], right[i], info.OutTypes[i])
			c.freeSymbolValue(left[i], "")
			left[i].Borrowed = true
		}
	}
	return lhsSyms, cond
}

// extractCondExprs evaluates value-position comparisons and stores their LHS
// values for substitution during later value compilation.
func (c *Compiler) extractCondExprs(expr ast.Expression, cond llvm.Value, temps []condTemp) (llvm.Value, []condTemp) {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]

	// Array-literal cells and (cond value) nodes are local-resolution boundaries:
	// each resolves its own condition (zero-fill / fallback, or the cond-value's
	// branch) at its own level. Statement-level extraction must stop here —
	// descending in would lift their inner comparison into a gate over the whole
	// assignment, which keeps the old value on failure (breaking local resolution)
	// and double-evaluates the node, leaking its heap LHS on the pass-through path.
	switch expr.(type) {
	case *ast.ArrayLiteral, *ast.CondValueExpr:
		return cond, temps
	}

	// Comparisons with ranges can be extracted only when all required iterators
	// are already bound by an outer loop (no pending ranges).
	if infix, ok := expr.(*ast.InfixExpression); ok && info.HasCondScalar() && len(c.pendingLoopRanges(info.Ranges)) == 0 {
		cond, temps = c.extractCondExprs(infix.Left, cond, temps)
		cond, temps = c.extractCondExprs(infix.Right, cond, temps)
		return c.extractCondComparison(infix, info, cond, temps)
	}

	for _, child := range ast.ExprChildren(expr) {
		cond, temps = c.extractCondExprs(child, cond, temps)
	}
	return cond, temps
}

func (c *Compiler) extractCondComparison(infix *ast.InfixExpression, info *ExprInfo, cond llvm.Value, temps []condTemp) (llvm.Value, []condTemp) {
	left := c.compileExpression(infix.Left, nil)
	right := c.compileExpression(infix.Right, nil)

	var lhsSyms []*Symbol
	lhsSyms, cond = c.handleComparisons(infix.Operator, left, right, info, cond)

	frame := c.requireCondLHSFrame()
	frame[key(c.FuncNameMangled, infix)] = lhsSyms

	// Track `left` as a temporary to free in cleanup — but not when it is a chained
	// inner comparison (a > b < c): its value came from substituting that already-
	// retained comparison, so it is the SAME symbol already tracked. Re-tracking it
	// would free it twice (double-free / crash) for a heap LHS.
	if _, chained := frame[key(c.FuncNameMangled, infix.Left)]; !chained {
		temps = append(temps, condTemp{infix.Left, left})
	}

	// Right is comparison-only; left is retained for condLHS substitution.
	c.freeTemporary(infix.Right, right)
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

// compileCondExprValue gates value-position cond expressions. Comparisons
// compose as AND; fallback OR tries the right side only after the left fails.
func (c *Compiler) compileCondExprValue(expr ast.Expression, baseCond llvm.Value, onTrue func()) {
	c.pushCondLHSFrame()
	defer c.popCondLHSFrame()

	if c.hasFallbackOrInTree(expr) {
		c.compileYield(expr, baseCond, onTrue, func() {})
		return
	}

	cond, temps := c.extractCondExprs(expr, baseCond, nil)
	c.branchCond(cond, temps, onTrue, func() {})
}

// compileCondOperands leaves expr itself to the caller.
func (c *Compiler) compileCondOperands(expr ast.Expression, baseCond llvm.Value, onTrue func()) {
	c.pushCondLHSFrame()
	defer c.popCondLHSFrame()

	if c.hasFallbackOrInTree(expr) {
		c.compileChildYields(ast.ExprChildren(expr), baseCond, onTrue, func() {})
		return
	}

	cond := baseCond
	var temps []condTemp
	for _, child := range ast.ExprChildren(expr) {
		cond, temps = c.extractCondExprs(child, cond, temps)
	}
	c.branchCond(cond, temps, onTrue, func() {})
}

func (c *Compiler) branchCond(cond llvm.Value, temps []condTemp, onTrue func(), onFalse func()) {
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
	onFalse()
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
}

func (c *Compiler) compileChildYields(children []ast.Expression, baseCond llvm.Value, onTrue func(), onFalse func()) {
	if len(children) == 0 {
		c.branchCond(baseCond, nil, onTrue, onFalse)
		return
	}

	child := children[0]
	rest := children[1:]
	if c.hasCondExprInTree(child) {
		c.compileYield(child, baseCond, func() {
			c.compileChildYields(rest, llvm.Value{}, onTrue, onFalse)
		}, onFalse)
		return
	}

	c.compileChildYields(rest, baseCond, onTrue, onFalse)
}

func (c *Compiler) compileYield(expr ast.Expression, baseCond llvm.Value, onTrue func(), onFalse func()) {
	if logicalOr, ok := c.fallbackOrExpr(expr); ok {
		c.compileFallbackOr(logicalOr, baseCond, onTrue, onFalse)
		return
	}

	// A (cond value) yields its value only when its condition holds; otherwise it
	// fails to onFalse (so an enclosing || tries the next alternative).
	if cv, ok := expr.(*ast.CondValueExpr); ok {
		cond := c.andGates(cv.Conds)
		if !baseCond.IsNil() {
			cond = c.builder.CreateAnd(baseCond, cond, "cv_and")
		}
		c.branchCond(cond, nil, func() {
			vals := c.compileExpression(cv.Value, nil)
			c.withCondLHS(cv, vals, onTrue)
		}, onFalse)
		return
	}

	if !c.hasFallbackOrInTree(expr) {
		cond, temps := c.extractCondExprs(expr, baseCond, nil)
		c.branchCond(cond, temps, onTrue, onFalse)
		return
	}

	c.compileChildYields(ast.ExprChildren(expr), baseCond, func() {
		if infix, ok := expr.(*ast.InfixExpression); ok {
			info := c.ExprCache[key(c.FuncNameMangled, infix)]
			if info != nil && info.HasCondScalar() && len(c.pendingLoopRanges(info.Ranges)) == 0 {
				cond, temps := c.extractCondComparison(infix, info, llvm.Value{}, nil)
				c.branchCond(cond, temps, onTrue, onFalse)
				return
			}
		}

		onTrue()
	}, onFalse)
}

func (c *Compiler) withCondLHS(expr ast.Expression, syms []*Symbol, body func()) {
	frame := c.requireCondLHSFrame()
	exprKey := key(c.FuncNameMangled, expr)
	frame[exprKey] = syms
	defer delete(frame, exprKey)
	body()
}

// compileCondValueExpr lowers a parenthesized conditional value (cond value).
// When a surrounding gate (a value-position ||, via compileYield) has already
// branched on Cond and bound the chosen value under this node's key, that
// pre-bound value is returned. With pending ranges (e.g. the root `r = (i>2 i)`)
// it drives a loop; otherwise it compiles the scalar branch/phi, using the
// range-scalarized rewrite when an outer loop already bound the iterators.
func (c *Compiler) compileCondValueExpr(expr *ast.CondValueExpr) []*Symbol {
	if frame := c.currentCondLHSFrame(); frame != nil {
		if syms, ok := frame[key(c.FuncNameMangled, expr)]; ok {
			return syms
		}
	}

	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if len(c.pendingLoopRanges(info.Ranges)) > 0 {
		return c.compileCondValueExprRanges(expr, info)
	}
	if rew, ok := info.Rewrite.(*ast.CondValueExpr); ok && rew != expr {
		expr = rew
		info = c.ExprCache[key(c.FuncNameMangled, expr)]
	}
	return c.compileCondValueExprBasic(expr, info)
}

// compileCondValueExprBasic is the scalar lowering: yield Value when every
// condition holds, otherwise the zero of each output slot's type. Only the taken
// arm is evaluated, so a heap value is never allocated on the path it isn't used.
func (c *Compiler) compileCondValueExprBasic(expr *ast.CondValueExpr, info *ExprInfo) []*Symbol {
	outTypes := info.OutTypes

	cond := c.andGates(expr.Conds)
	trueBlock, falseBlock, contBlock := c.createIfElseCont(cond, "cv_true", "cv_false", "cv_cont")

	// True path evaluates the value (one symbol per output slot for a
	// multi-return value); false path yields the zero of each slot's type.
	c.builder.SetInsertPointAtEnd(trueBlock)
	vals := c.compileExpression(expr.Value, nil)
	for i := range vals {
		vals[i] = c.derefIfPointer(vals[i], "cv_val")
	}
	c.builder.CreateBr(contBlock)
	trueEnd := c.builder.GetInsertBlock()

	c.builder.SetInsertPointAtEnd(falseBlock)
	zeros := make([]*Symbol, len(outTypes))
	for i, t := range outTypes {
		zeros[i] = c.makeZeroValue(t)
	}
	c.builder.CreateBr(contBlock)
	falseEnd := c.builder.GetInsertBlock()

	c.builder.SetInsertPointAtEnd(contBlock)
	res := make([]*Symbol, len(outTypes))
	for i, t := range outTypes {
		phi := c.builder.CreatePHI(c.mapToLLVMType(t), "cv_phi")
		phi.AddIncoming([]llvm.Value{vals[i].Val, zeros[i].Val}, []llvm.BasicBlock{trueEnd, falseEnd})
		res[i] = &Symbol{Val: phi, Type: t}
	}
	return res
}

// compileCondValueExprRanges lowers a range-bearing (cond value) at the root of
// an assignment: it loops the iteration domain, writing the per-iteration
// value-or-zero into a seeded output slot, and the final iteration's value wins
// (root assignment keeps the last yielded value).
func (c *Compiler) compileCondValueExprRanges(expr *ast.CondValueExpr, info *ExprInfo) []*Symbol {
	PushScope(&c.Scopes, BlockScope)
	defer c.popScope()

	outputs := c.makeOutputs(nil, info.OutTypes, true)
	for i, t := range info.OutTypes {
		c.storeSymbolToSlot(outputs[i], c.makeZeroValue(t), t, "cv_seed")
	}

	rew := info.Rewrite.(*ast.CondValueExpr)
	withCollectorPreparedLoopNest(c, rew, info.Ranges, nil, nil, func(prepared *ast.CondValueExpr) {
		c.pushBoundsGuard("condval_iter_bounds_guard")
		defer c.popBoundsGuard()

		vals := c.compileCondValueExprBasic(prepared, c.ExprCache[key(c.FuncNameMangled, prepared)])

		// Free the previous iteration's value before overwriting, and route the
		// store through the active bounds guard so out-of-bounds iterations skip
		// it (keeping the last in-bounds value) — matching the other range paths.
		store := func() {
			for i := range vals {
				c.freeSymbolValue(outputs[i], "cv_old_output")
				c.storeSymbolToSlot(outputs[i], vals[i], info.OutTypes[i], "cv_iter_store")
			}
		}
		skip := func() {
			for i := range vals {
				c.freeTemporarySymbol(vals[i], "cv_skip_drop")
			}
		}
		if !c.withStmtBoundsGuard("cv_bounds_ok", "cv_bounds_run", "cv_bounds_skip", "cv_bounds_cont", store, skip) {
			store()
		}
	})

	out := make([]*Symbol, len(outputs))
	for i := range outputs {
		elemType := outputs[i].Type.(Ptr).Elem
		out[i] = &Symbol{Val: c.createLoad(outputs[i].Val, elemType, "cv_final"), Type: elemType}
	}
	return out
}

func (c *Compiler) compileFallbackOr(expr *ast.InfixExpression, baseCond llvm.Value, onTrue func(), onFalse func()) {
	if !baseCond.IsNil() {
		c.branchCond(baseCond, nil, func() {
			c.compileFallbackOr(expr, llvm.Value{}, onTrue, onFalse)
		}, onFalse)
		return
	}

	leftTrue := func() {
		left := c.compileExpression(expr.Left, nil)
		c.withCondLHS(expr, left, onTrue)
	}
	rightTrue := func() {
		right := c.compileExpression(expr.Right, nil)
		c.withCondLHS(expr, right, onTrue)
	}

	c.compileYield(expr.Left, llvm.Value{}, leftTrue, func() {
		c.compileYield(expr.Right, llvm.Value{}, rightTrue, onFalse)
	})
}

func (c *Compiler) isRangeDriverCond(expr ast.Expression) bool {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if len(info.OutTypes) != 1 {
		return false
	}

	return isRangeDriverType(info.OutTypes[0])
}

func (c *Compiler) collectDriverRanges(expr ast.Expression) []*RangeInfo {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if len(info.Ranges) > 0 {
		return info.Ranges
	}

	ident, ok := expr.(*ast.Identifier)
	if !ok {
		panic(fmt.Sprintf("internal: bare range driver %T missing cached ranges", expr))
	}
	return []*RangeInfo{{Name: ident.Value}}
}

// splitCondRanges collects merged ranges and boolean guard expressions
// from statement conditions. Bare range/array-range drivers contribute only
// ranges; comparisons contribute both ranges and a per-iteration guard.
// Returns nil, nil if no condition introduces ranges.
func (c *Compiler) splitCondRanges(conditions []ast.Expression) ([]*RangeInfo, []ast.Expression) {
	var ranges []*RangeInfo
	var condExprs []ast.Expression
	for _, expr := range conditions {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		if c.isRangeDriverCond(expr) {
			ranges = mergeUses(ranges, c.collectDriverRanges(expr))
			continue
		}

		if len(info.Ranges) == 0 {
			continue
		}

		ranges = mergeUses(ranges, info.Ranges)
		if info.Rewrite != nil {
			condExprs = append(condExprs, info.Rewrite)
			continue
		}
		condExprs = append(condExprs, expr)
	}
	if len(ranges) == 0 {
		return nil, nil
	}
	return ranges, condExprs
}

// withCondRangeLoop sets up the shared loop+guard+branch scaffold used by
// both accumulation and iteration paths: loop over all ranges, evaluate
// conditions, branch on the combined result, and call body on the true path.
// Probes cover every expression that can issue array accesses inside that loop
// region, letting the affine fast path apply uniformly when it can be proven.
func (c *Compiler) withCondRangeLoop(allRanges []*RangeInfo, condExprs []ast.Expression, probes []ast.Expression, guardName, ifName, contName string, body func()) {
	c.withLoopNestVersioned(allRanges, probes, func() {
		if len(condExprs) == 0 {
			body()
			return
		}

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

// compileCondRangedStatement lowers ranged statement conditions.
// Statement conditions are shared across the whole assignment. They determine
// the outer admitted iteration domain, while each RHS expression keeps any
// extra local drivers to itself inside that shared gate. Top-level 1D array
// literals accumulate across admitted iterations; all other outputs use normal
// conditional iteration (last value wins).
func (c *Compiler) compileCondRangedStatement(stmt *ast.LetStatement, condRanges []*RangeInfo, condExprs []ast.Expression) {
	c.prePromoteConditionalCallArgs(stmt.Value)

	assignExprs := []ast.Expression{}
	assignDests := []*ast.Identifier{}
	assignOutTypes := []Type{}
	assignCollectorTemps := []string{}
	// loopProbes collect every expression in the shared ranged-condition loop
	// that can issue array accesses, so affine versioning can prove the whole
	// region safe up front instead of only individual RHS shapes.
	loopProbes := append([]ast.Expression{}, condExprs...)

	accumLits := []*ast.ArrayLiteral{}
	accumAccs := []*ArrayAccumulator{}
	accumDests := []*ast.Identifier{}
	accumOldValues := []*Symbol{}

	targetIdx := 0
	for _, expr := range stmt.Value {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		numOutputs := len(info.OutTypes)

		if lit, ok := expr.(*ast.ArrayLiteral); ok && len(lit.Headers) == 0 && len(lit.Rows) == 1 {
			accumDest := stmt.Name[targetIdx]
			accumLits = append(accumLits, lit)
			accumAccs = append(accumAccs, c.NewArrayAccumulator(info.OutTypes[0].(Array)))
			accumDests = append(accumDests, accumDest)
			accumOldValues = append(accumOldValues, c.captureOldValues([]*ast.Identifier{accumDest})[0])
			resolvedLit, _ := c.resolveArrayLiteralRewrite(lit)
			loopProbes = append(loopProbes, resolvedLit)
			targetIdx += numOutputs
			continue
		}

		preparedExpr, collectorTemps := c.prepareCollectorTreeFor(expr, condRanges, condExprs)
		assignExprs = append(assignExprs, preparedExpr)
		loopProbes = append(loopProbes, preparedExpr)
		assignCollectorTemps = append(assignCollectorTemps, collectorTemps...)
		for i := 0; i < numOutputs; i++ {
			dest := stmt.Name[targetIdx+i]
			assignDests = append(assignDests, dest)
			assignOutTypes = append(assignOutTypes, c.bindingSlotType(dest.Value, info.OutTypes[i]))
		}
		targetIdx += numOutputs
	}

	hasAssigns := len(assignDests) > 0
	defer c.cleanupMaterializedCollectors(assignCollectorTemps)

	var assignTempNames []*ast.Identifier
	if hasAssigns {
		assignTempNames = c.createConditionalTempOutputsFor(assignDests, assignOutTypes)
	}

	c.withCondRangeLoop(condRanges, condExprs, loopProbes, "cond_iter_guard", "cond_iter_if", "cond_iter_cont", func() {
		c.compileCondRangedIteration(
			assignExprs, assignDests, assignTempNames,
			accumAccs, accumLits,
		)
	})

	if hasAssigns {
		c.commitConditionalOutputs(assignDests, assignTempNames, assignOutTypes)
		DeleteBulk(c.Scopes, tempNamesToStrings(assignTempNames))
	}

	for i, acc := range accumAccs {
		result := c.ArrayAccResult(acc)
		c.storeValue(accumDests[i].Value, result, false)
		if accumOldValues[i] == nil || c.skipBorrowedOldValueFree(accumOldValues[i]) {
			continue
		}
		c.freeSymbolValue(accumOldValues[i], "old_accum")
	}
}

// compileCondRangedIteration runs inside the per-iteration body of
// compileCondRangedStatement. It stages scalar assignments independently
// and appends array literal cells to accumulators.
func (c *Compiler) compileCondRangedIteration(
	assignExprs []ast.Expression,
	assignDests []*ast.Identifier,
	commitTempNames []*ast.Identifier,
	accumAccs []*ArrayAccumulator,
	accumLits []*ast.ArrayLiteral,
) {
	// Accum-only: no assigns, just push cells.
	if len(assignDests) == 0 {
		c.appendArrayLiterals(accumAccs, accumLits)
		return
	}

	// The outer statement condition admits one iteration here, but sibling RHS
	// expressions may still skip later in that same admitted step. Each RHS first
	// writes into a private stage temp under its own bounds guard, so a local skip
	// leaves that stage temp seeded with the prior value without suppressing
	// sibling RHS writes.
	stageGroups := c.stageCondRangedAssignments(assignExprs, assignDests, commitTempNames)

	if len(accumLits) > 0 {
		c.appendArrayLiterals(accumAccs, accumLits)
	}

	c.commitCondRangedStages(stageGroups)
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

		// A value-position multi-return comparison (Pair > Pair, Mix > Mix, ...)
		// commits each slot independently: array slots mask (overwrite), scalar slots
		// gate on their own condition — commit the LHS when it holds, keep the seeded
		// prior value otherwise (the same keep-old-on-false a single comparison gives).
		// A failing slot affects only itself, never the tuple. The cell-wise AND is
		// reserved for gate/condition position and for || fallback; a single-return
		// comparison keeps the gated path below.
		if infix, ok := c.independentTupleComparison(expr, info); ok {
			c.compileTupleComparisonPerSlot(infix, info, exprTempNames, stmtCond)
		} else {
			c.compileCondExprValue(expr, stmtCond, func() {
				c.compileCondAssignments(exprTempNames, exprDestNames, exprValues)
			})
		}

		targetIdx += numOutputs
	}

	c.commitConditionalOutputs(stmt.Name, tempNames, outTypes)
	DeleteBulk(c.Scopes, tempNamesToStrings(tempNames))
}

// independentTupleComparison returns the comparison and true when expr is a
// value-position multi-return comparison whose slots commit independently (array
// masks plus per-slot-gated scalars) rather than as one all-or-nothing AND-gate.
// True for a multi-slot comparison (Pair > Pair, Mix > Mix, ...) that is neither
// ranged nor contains a value-position || anywhere in its tree. A single-return
// comparison is excluded (it keeps the gated path), and the cell-wise AND still
// applies in gate/condition position and for ||. Chained tuple comparisons
// (Pair > Pair < Pair) never reach here — the type solver rejects them
// (rejectChainedTupleComparison).
//
// The || check is the recursive hasFallbackOrInTree, not the flat info.HasFallbackOr:
// a || nested in an operand (e.g. Pair(a > 2 || 7, b) > Pair(1, 1)) leaves the top
// comparison's CompareModes free of CondOr, but the per-slot path evaluates operands
// directly — without the yield/branching context a value-position || needs — so such
// an expression must defer to the gated path.
func (c *Compiler) independentTupleComparison(expr ast.Expression, info *ExprInfo) (*ast.InfixExpression, bool) {
	infix, ok := expr.(*ast.InfixExpression)
	if !ok {
		return nil, false
	}
	if c.hasFallbackOrInTree(expr) || len(c.pendingLoopRanges(info.Ranges)) > 0 {
		return nil, false
	}
	if !info.HasAnyComparison() {
		return nil, false
	}
	if len(info.OutTypes) <= 1 {
		return nil, false
	}
	return infix, true
}

// compileTupleComparisonPerSlot commits a value-position multi-return comparison
// slot by slot into the pre-seeded temps. Array slots mask and overwrite; scalar
// slots commit their LHS only when the per-slot comparison holds, otherwise leaving
// the seeded prior value in place (keep-old-on-false). The whole commit is guarded
// by any statement condition. Operands are evaluated once and freed after.
func (c *Compiler) compileTupleComparisonPerSlot(infix *ast.InfixExpression, info *ExprInfo, tempNames []*ast.Identifier, stmtCond llvm.Value) {
	c.assignUnderStmtCond(stmtCond, func() {
		// Guard operand evaluation so an out-of-bounds access (e.g. arr[oob]) in any
		// operand fails the whole tuple assignment and keeps the prior values, like a
		// normal assignment. Bounds checks are recorded while the operands compile.
		guardPtr := c.pushBoundsGuard("tuple_bounds_guard")
		defer c.popBoundsGuard()

		left := c.compileExpression(infix.Left, nil)
		right := c.compileExpression(infix.Right, nil)

		commitSlots := func() {
			for i := range left {
				elemType := info.OutTypes[i]
				if info.CompareModes[i] == CondArray {
					mask := c.compileArrayMask(infix.Operator, left[i], right[i], elemType)
					c.commitSlotValue(tempNames[i], mask, elemType, false)
					continue
				}

				lhs, cond := c.compareScalars(infix.Operator, left[i], right[i])
				ifBlock, contBlock := c.createIfCont(cond, "tuple_slot_if", "tuple_slot_cont")
				c.builder.SetInsertPointAtEnd(ifBlock)
				c.commitSlotValue(tempNames[i], lhs, elemType, true)
				c.builder.CreateBr(contBlock)
				c.builder.SetInsertPointAtEnd(contBlock)
			}
		}

		if c.stmtBoundsUsed() {
			c.withGuardedBranch(guardPtr, "tuple_bounds_ok", "tuple_bounds_commit", "tuple_bounds_skip", "tuple_bounds_cont", commitSlots, nil)
		} else {
			commitSlots()
		}

		c.freeTemporary(infix.Left, left)
		c.freeTemporary(infix.Right, right)
	})
}

// assignUnderStmtCond runs commit unconditionally when there is no statement
// condition, or guarded by it (keeping the seeded prior values on the false path).
func (c *Compiler) assignUnderStmtCond(stmtCond llvm.Value, commit func()) {
	if stmtCond.IsNil() {
		commit()
		return
	}

	ifBlock, contBlock := c.createIfCont(stmtCond, "stmt_cond_if", "stmt_cond_cont")
	c.builder.SetInsertPointAtEnd(ifBlock)
	commit()
	c.builder.CreateBr(contBlock)
	c.builder.SetInsertPointAtEnd(contBlock)
}

// commitSlotValue stores value into the pre-seeded temp slot, freeing the slot's
// previous value unless it is a borrowed view. copyValue deep-copies the value
// first (for a borrowed scalar LHS that the temp must own); an array mask is
// already owned and passes through. Mirrors the seed-commit protocol used by
// commitStageTempOutputs.
func (c *Compiler) commitSlotValue(tempIdent *ast.Identifier, value *Symbol, elemType Type, copyValue bool) {
	tempSym, _ := Get(c.Scopes, tempIdent.Value)
	oldValue := c.valueSymbol(tempIdent.Value, tempSym, tempIdent.Value+"_slot_old")

	toStore := value
	if copyValue {
		toStore = c.deepCopyIfNeeded(value)
	}
	c.storeSymbolToSlot(tempSym, toStore, elemType, tempIdent.Value+"_slot_store")

	if c.skipBorrowedOldValueFree(oldValue) {
		return
	}
	c.freeSymbolValue(oldValue, tempIdent.Value+"_slot_old")
}
