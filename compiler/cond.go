package compiler

import (
	"fmt"

	"github.com/thiremani/pluto/ast"
	"tinygo.org/x/go-llvm"
)

// condTemp holds a pre-compiled operand and its source expression, used to
// free heap temporaries that a skipped or false path leaves unconsumed.
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
	// The gate is the conjunction of the per-slot conditions extraction
	// returns: for comparisons "did every cell hold", for a value-position ||
	// "did every slot yield" — a > 2 || b > 3 gates on a>2 OR b>3, which IS its
	// yield flag. The retained LHS values drive the gate but are not a result,
	// so they are freed here (a heap LHS computed only to test a gate must not
	// leak).
	c.pushCondLHSFrame()
	defer c.popCondLHSFrame()
	conds, temps := c.extractSlotConds(expr, nil)
	c.cleanupCondExprElse(temps)
	return c.foldSlotConds(conds)
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
		stagedValue := c.valueSymbol(stageTempNames[i].Value, stageSym, stageTempNames[i].Value+"_stage_final")
		c.commitSlotValue(ident, stagedValue, false)
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
		// Iterators bound by this loop leave no pending ranges, so a per-slot
		// value expression commits slot-by-slot here too — a ranged statement
		// condition must not change the value's per-slot semantics.
		if c.perSlotCommittable(expr, info) {
			c.compilePerSlotAssign(expr, info, stageTempNames, llvm.Value{})
			return
		}
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

// andConds ANDs two i1 conditions where either may be nil (unconditional).
func (c *Compiler) andConds(a, b llvm.Value, name string) llvm.Value {
	if a.IsNil() {
		return b
	}
	if b.IsNil() {
		return a
	}
	return c.builder.CreateAnd(a, b, name)
}

// foldSlotConds ANDs all per-slot conditions into a single gate. Returns nil
// when every slot is unconditional.
func (c *Compiler) foldSlotConds(conds []llvm.Value) llvm.Value {
	var cond llvm.Value
	for _, sc := range conds {
		cond = c.andConds(cond, sc, "and_slots")
	}
	return cond
}

func slotCondAt(conds []llvm.Value, i int) llvm.Value {
	if i >= len(conds) {
		return llvm.Value{}
	}
	return conds[i]
}

// broadcastConds spreads one condition over n slots (all nil when cond is nil).
func broadcastConds(cond llvm.Value, n int) []llvm.Value {
	conds := make([]llvm.Value, n)
	if cond.IsNil() {
		return conds
	}
	for i := range conds {
		conds[i] = cond
	}
	return conds
}

// extractSlotConds evaluates the value-position comparisons in expr and
// returns per-slot conditions aligned to expr's output slots — a nil entry
// (or an empty slice, for condition-free subtrees whose arity is not cached)
// means the slot always yields. Comparison LHS values and array masks are
// stashed in the condLHS frame for substitution during later value
// compilation; retained operand temporaries are appended to temps.
//
// Conditions merge along dataflow. A slot-aligned infix combines its
// children's conditions slot-wise, so each slot of Pair > Pair < Pair or
// (Pair > Pair) + Pair(0, 0) carries only its own comparisons. Any other node
// (call, index, range, ...) produces its slots together from all of its
// children, so their conditions AND into every output slot — for a
// single-output expression that is exactly the old single gate.
func (c *Compiler) extractSlotConds(expr ast.Expression, temps []condTemp) ([]llvm.Value, []condTemp) {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]

	// Array-literal cells and (cond value) nodes are local-resolution boundaries:
	// each resolves its own condition (zero-fill / fallback, or the cond-value's
	// branch) at its own level. Statement-level extraction must stop here —
	// descending in would lift their inner comparison into a gate over the whole
	// assignment, which keeps the old value on failure (breaking local resolution)
	// and double-evaluates the node, leaking its heap LHS on the pass-through path.
	switch expr.(type) {
	case *ast.ArrayLiteral, *ast.CondValueExpr:
		return nil, temps
	}

	// A value-position || resolves per slot: slot i falls back only when slot i
	// of the left side failed to yield.
	if or, ok := ast.IsLogicalOr(expr); ok && info != nil && info.HasFallbackOr() {
		return c.extractFallbackOrSlots(or, info, temps)
	}

	// Comparisons with ranges can be extracted only when all required iterators
	// are already bound by an outer loop (no pending ranges).
	if infix, ok := expr.(*ast.InfixExpression); ok && info.HasAnyComparison() && len(c.pendingLoopRanges(info.Ranges)) == 0 {
		return c.extractComparisonSlots(infix, info, temps)
	}

	switch e := expr.(type) {
	case *ast.InfixExpression:
		var lConds, rConds []llvm.Value
		lConds, temps = c.extractSlotConds(e.Left, temps)
		rConds, temps = c.extractSlotConds(e.Right, temps)
		if len(lConds) == 0 && len(rConds) == 0 {
			return nil, temps
		}
		// The solver enforces operand arity == node arity for non-comparison
		// infix, so operand conditions are either absent or slot-aligned;
		// anything else would zip lane i's condition onto lane j (a miscompile).
		n := len(info.OutTypes)
		if (len(lConds) != 0 && len(lConds) != n) || (len(rConds) != 0 && len(rConds) != n) {
			panic(fmt.Sprintf("internal: slot conditions misaligned with infix arity (left %d, right %d, slots %d)", len(lConds), len(rConds), n))
		}
		conds := make([]llvm.Value, n)
		for i := range conds {
			conds[i] = c.andConds(slotCondAt(lConds, i), slotCondAt(rConds, i), fmt.Sprintf("slot_and_%d", i))
		}
		return conds, temps
	case *ast.PrefixExpression:
		return c.extractSlotConds(e.Right, temps)
	}

	// Non-aligned nodes (calls, indexing, ranges, ...): every child feeds all
	// output slots, so child conditions AND together and broadcast.
	var all llvm.Value
	for _, child := range ast.ExprChildren(expr) {
		var childConds []llvm.Value
		childConds, temps = c.extractSlotConds(child, temps)
		all = c.andConds(all, c.foldSlotConds(childConds), "child_and")
	}
	if all.IsNil() {
		return nil, temps
	}
	return broadcastConds(all, len(info.OutTypes)), temps
}

// extractComparisonSlots evaluates one value-position comparison. Operands are
// compiled once (an inner comparison substitutes its retained LHS, so chains
// share one evaluation); each scalar slot yields its comparison bit ANDed with
// the operands' own conditions for that slot, and each array slot becomes an
// element-wise mask, which always yields and so contributes no condition. LHS
// values are retained in the condLHS frame; the left operand is tracked as a
// temporary unless it is a chained comparison that is already tracked.
func (c *Compiler) extractComparisonSlots(infix *ast.InfixExpression, info *ExprInfo, temps []condTemp) ([]llvm.Value, []condTemp) {
	var lConds, rConds []llvm.Value
	lConds, temps = c.extractSlotConds(infix.Left, temps)
	rConds, temps = c.extractSlotConds(infix.Right, temps)

	left := c.compileExpression(infix.Left, nil)
	right := c.compileExpression(infix.Right, nil)

	conds := make([]llvm.Value, len(left))
	lhsSyms := make([]*Symbol, len(left))
	for i := range left {
		operandCond := c.andConds(slotCondAt(lConds, i), slotCondAt(rConds, i), fmt.Sprintf("operand_cond_%d", i))
		switch info.CompareModes[i] {
		case CondArray:
			// compileArrayMask handles deref internally. The mask copies the
			// elements it keeps, so an owned left operand (call result, literal,
			// inner mask) is consumed here — but a named variable's loaded value
			// is the variable's live payload and must survive.
			lhsSyms[i] = c.compileArrayMask(infix.Operator, left[i], right[i], info.OutTypes[i])
			if _, isIdent := infix.Left.(*ast.Identifier); !isIdent {
				c.freeSymbolValue(left[i], "")
				left[i].Borrowed = true
			}
			conds[i] = operandCond
		default:
			lSym, cmpVal := c.compareScalars(infix.Operator, left[i], right[i])
			conds[i] = c.andConds(operandCond, cmpVal, fmt.Sprintf("slot_cond_%d", i))
			lhsSyms[i] = lSym
		}
	}

	frame := c.requireCondLHSFrame()
	frame[key(c.FuncNameMangled, infix)] = lhsSyms

	// Track `left` as a temporary to free in cleanup — but not when it is a chained
	// inner comparison (a > b < c): its value came from substituting that already-
	// retained comparison, so it is the SAME symbol already tracked. Re-tracking it
	// would free it twice (double-free / crash) for a heap LHS.
	if _, chained := frame[key(c.FuncNameMangled, infix.Left)]; !chained {
		temps = append(temps, condTemp{expr: infix.Left, syms: left})
	}

	// Right is comparison-only; left is retained for condLHS substitution. A
	// parenthesized comparison on the right (a > (b < c)) substituted the inner
	// comparison's retained LHS — the same symbol already tracked — so freeing
	// it here would double-free on the cleanup path.
	if _, chainedRight := frame[key(c.FuncNameMangled, infix.Right)]; !chainedRight {
		c.freeTemporary(infix.Right, right)
	}
	return conds, temps
}

// extractFallbackOrSlots lowers a value-position || per slot: slot i yields the
// left side's value when its condition holds and falls back to the right side
// only when it failed. The right side is evaluated lazily — once, and only when
// some slot did not yield. Each result slot holds an owned copy (or a freeable
// zero when neither side yielded, which downstream reads never commit because
// the returned slot condition is the yield flag). The resolved values are
// stashed in the condLHS frame under the || node, and the slots are tracked as
// one temporary, so both commit conventions work: the AND-gate world moves
// them into destinations on the true path and frees them on the else path,
// while the per-slot world copies then frees.
func (c *Compiler) extractFallbackOrSlots(or *ast.InfixExpression, info *ExprInfo, temps []condTemp) ([]llvm.Value, []condTemp) {
	i1 := Int{Width: 1}
	i1Ty := c.mapToLLVMType(i1)

	slots := make([]llvm.Value, len(info.OutTypes))
	yields := make([]llvm.Value, len(info.OutTypes))
	for i, outType := range info.OutTypes {
		slots[i] = c.createEntryBlockAlloca(c.mapToLLVMType(outType), fmt.Sprintf("or_slot_%d.mem", i))
		yields[i] = c.createEntryBlockAlloca(i1Ty, fmt.Sprintf("or_yield_%d.mem", i))
		c.createStore(llvm.ConstInt(i1Ty, 0, false), yields[i], i1)
	}

	storeSide := func(side ast.Expression, conds []llvm.Value, needFlags bool) {
		for i, outType := range info.OutTypes {
			store := func() {
				val, owned := c.spineSlotValue(side, i, outType)
				if owned {
					// Ownership moves into the result slot; mark the source so
					// mask cleanup skips it (a fresh combine is simply discarded).
					val.Borrowed = true
				} else {
					// Deref before deciding to copy: a leaf's slot value may be
					// Ptr-wrapped, and the copy must clone the pointee — the
					// source is freed while the result slot lives on.
					val = c.derefIfPointer(val, fmt.Sprintf("or_take_val_%d", i))
					if !IsStrG(val.Type) {
						val = c.deepCopyIfNeeded(val)
					}
				}
				coerced := c.coerceSymbolForType(val, outType, fmt.Sprintf("or_val_%d", i))
				c.createStore(coerced.Val, slots[i], coerced.Type)
				c.createStore(llvm.ConstInt(i1Ty, 1, false), yields[i], i1)
			}

			cond := slotCondAt(conds, i)
			if needFlags {
				need := c.builder.CreateNot(c.createLoad(yields[i], i1, fmt.Sprintf("or_need_%d", i)), fmt.Sprintf("or_miss_%d", i))
				cond = c.andConds(need, cond, fmt.Sprintf("or_take_%d", i))
			}
			if cond.IsNil() {
				store()
				continue
			}

			ifBlock, elseBlock, contBlock := c.createIfElseCont(cond, fmt.Sprintf("or_store_%d", i), fmt.Sprintf("or_skip_%d", i), fmt.Sprintf("or_store_cont_%d", i))

			c.builder.SetInsertPointAtEnd(ifBlock)
			store()
			c.builder.CreateBr(contBlock)

			// A directly stored mask skipped at runtime was never moved; free it.
			c.builder.SetInsertPointAtEnd(elseBlock)
			c.freeSkippedSlotMask(side, i)
			c.builder.CreateBr(contBlock)

			c.builder.SetInsertPointAtEnd(contBlock)
		}
	}

	beforeLeft := c.frameMaskKeys()
	lConds, lTemps := c.prepareFallbackOperand(or.Left, nil)
	storeSide(or.Left, lConds, false)
	c.freeCondTemps(lTemps)
	c.freeUnmovedMasksSince(beforeLeft)

	// Lazy right: only when some slot failed to yield. An unconditional left
	// (every slot always yields) makes the fallback dead.
	leftAll := c.foldSlotConds(lConds)
	if !leftAll.IsNil() {
		someMissed := c.builder.CreateNot(leftAll, "or_some_missed")
		c.underCond(someMissed, "or_rhs", "or_rhs_cont", func() {
			beforeRight := c.frameMaskKeys()
			rConds, rTemps := c.prepareFallbackOperand(or.Right, nil)
			storeSide(or.Right, rConds, true)
			c.freeCondTemps(rTemps)
			c.freeUnmovedMasksSince(beforeRight)
		})
	}

	// Slots that never yielded hold zero (freeable: an StrH zero is an owned
	// heap copy, an array zero is null), so cleanup stays unconditional.
	conds := make([]llvm.Value, len(info.OutTypes))
	loaded := make([]*Symbol, len(info.OutTypes))
	for i, outType := range info.OutTypes {
		yield := c.createLoad(yields[i], i1, fmt.Sprintf("or_yield_%d", i))
		c.underCond(c.builder.CreateNot(yield, fmt.Sprintf("or_zero_%d", i)), fmt.Sprintf("or_seed_%d", i), fmt.Sprintf("or_seed_cont_%d", i), func() {
			zero := c.makeZeroValue(outType)
			c.createStore(zero.Val, slots[i], outType)
		})
		conds[i] = yield
		loaded[i] = &Symbol{Val: c.createLoad(slots[i], outType, fmt.Sprintf("or_slot_%d", i)), Type: outType}
	}

	frame := c.requireCondLHSFrame()
	frame[key(c.FuncNameMangled, or)] = loaded
	temps = append(temps, condTemp{expr: or, syms: loaded})
	return conds, temps
}

// prepareFallbackOperand prepares one side of a value-position || for per-slot
// reads. A (cond value) operand is failable here — it yields only when its
// condition holds — unlike everywhere else, where it self-resolves; its value
// arm is compiled behind its own gate. Everything else prepares like a spine.
func (c *Compiler) prepareFallbackOperand(expr ast.Expression, temps []condTemp) ([]llvm.Value, []condTemp) {
	cv, ok := expr.(*ast.CondValueExpr)
	if !ok {
		return c.prepareSpine(expr, temps)
	}

	info := c.ExprCache[key(c.FuncNameMangled, cv)]
	gate := c.andGates(cv.Conds)

	slots := make([]llvm.Value, len(info.OutTypes))
	for i, outType := range info.OutTypes {
		slots[i] = c.createEntryBlockAlloca(c.mapToLLVMType(outType), fmt.Sprintf("or_cv_%d.mem", i))
	}

	ifBlock, elseBlock, contBlock := c.createIfElseCont(gate, "or_cv_if", "or_cv_else", "or_cv_cont")

	c.builder.SetInsertPointAtEnd(ifBlock)
	syms := c.compileExpression(cv.Value, nil)
	for i, outType := range info.OutTypes {
		coerced := c.coerceSymbolForType(syms[i], outType, fmt.Sprintf("or_cv_val_%d", i))
		c.createStore(coerced.Val, slots[i], coerced.Type)
	}
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(elseBlock)
	for i, outType := range info.OutTypes {
		zero := c.makeZeroValue(outType)
		c.createStore(zero.Val, slots[i], outType)
	}
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
	loaded := make([]*Symbol, len(info.OutTypes))
	for i, outType := range info.OutTypes {
		loaded[i] = &Symbol{Val: c.createLoad(slots[i], outType, fmt.Sprintf("or_cv_%d", i)), Type: outType}
	}
	frame := c.requireCondLHSFrame()
	frame[key(c.FuncNameMangled, cv)] = loaded
	temps = append(temps, condTemp{expr: cv, syms: loaded})
	return broadcastConds(gate, len(info.OutTypes)), temps
}

// cleanupCondExprElse frees temporaries retained during cond-expr extraction
// that are not consumed when the condition evaluates to false, plus the frame
// masks a nested resolution has not already released.
func (c *Compiler) cleanupCondExprElse(temps []condTemp) {
	for _, tmp := range temps {
		c.freeTemporary(tmp.expr, tmp.syms)
	}
	c.freeFrameMasks()
}

// compileCondExprValue gates value-position cond expressions on the ANDed
// per-slot conditions: comparisons compose as AND, and a value-position ||
// contributes its yield flags (fallback resolved during extraction).
func (c *Compiler) compileCondExprValue(expr ast.Expression, baseCond llvm.Value, onTrue func()) {
	c.pushCondLHSFrame()
	defer c.popCondLHSFrame()

	conds, temps := c.extractSlotConds(expr, nil)
	cond := c.andConds(baseCond, c.foldSlotConds(conds), "base_and")
	c.branchCond(cond, temps, onTrue, func() {})
}

// compileCondOperands leaves expr itself to the caller.
func (c *Compiler) compileCondOperands(expr ast.Expression, baseCond llvm.Value, onTrue func()) {
	c.pushCondLHSFrame()
	defer c.popCondLHSFrame()

	cond := baseCond
	var temps []condTemp
	for _, child := range ast.ExprChildren(expr) {
		var childConds []llvm.Value
		childConds, temps = c.extractSlotConds(child, temps)
		cond = c.andConds(cond, c.foldSlotConds(childConds), "child_gate")
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

// compileCondValueExpr lowers a parenthesized conditional value (cond value).
// When a surrounding || (via prepareFallbackOperand) has already branched on
// Cond and bound the chosen value under this node's key, that pre-bound value
// is returned. With pending ranges (e.g. the root `r = (i>2 i)`) it drives a
// loop; otherwise it compiles the scalar branch/phi, using the
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

		// A multi-return value expression whose slots carry independent
		// conditions (Pair > Pair, (Pair > Pair) + Pair(1, 1), Mix > Mix, ...)
		// commits each slot under its own condition: array slots mask
		// (overwrite), scalar slots keep the seeded prior value when their
		// condition fails — the same keep-old a single comparison gives. A
		// failing slot affects only itself. The cell-wise AND is reserved for
		// gate/condition position and || fallback; everything else keeps the
		// single ANDed gate below.
		if c.perSlotCommittable(expr, info) {
			c.compilePerSlotAssign(expr, info, exprTempNames, stmtCond)
		} else {
			c.compileCondExprValue(expr, stmtCond, func() {
				c.compileCondAssignments(exprTempNames, exprDestNames, []ast.Expression{expr})
			})
		}

		targetIdx += numOutputs
	}

	c.commitConditionalOutputs(stmt.Name, tempNames, outTypes)
	DeleteBulk(c.Scopes, tempNamesToStrings(tempNames))
}

// perSlotCommittable reports whether expr commits slot-by-slot in value
// position: a multi-return expression whose root is a comparison, a
// value-position ||, or a slot-aligned arithmetic spine over one, so each
// output slot carries its own condition. Excluded (they keep the single ANDed
// gate): single-return expressions, ranged expressions, and roots whose slots
// merge — a call's outputs are produced together, so every argument condition
// gates them all.
func (c *Compiler) perSlotCommittable(expr ast.Expression, info *ExprInfo) bool {
	if len(info.OutTypes) <= 1 {
		return false
	}
	if len(c.pendingLoopRanges(info.Ranges)) > 0 {
		return false
	}
	if !c.hasCondExprInTree(expr) {
		return false
	}
	return c.isSlotAlignedSpine(expr)
}

// isSlotAlignedSpine reports whether expr's root is a comparison, a
// value-position ||, or an arithmetic infix tree over one — the shapes whose
// output slots stay aligned with their operands' slots, so per-slot conditions
// are well-defined all the way to the root.
func (c *Compiler) isSlotAlignedSpine(expr ast.Expression) bool {
	infix, ok := expr.(*ast.InfixExpression)
	if !ok {
		return false
	}
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if info != nil && len(c.pendingLoopRanges(info.Ranges)) > 0 {
		return false
	}
	if info != nil && (info.HasAnyComparison() || info.HasFallbackOr()) {
		return true
	}
	if infix.Token.IsComparison() || infix.IsLogicalOr() {
		return false
	}
	return c.isSlotAlignedSpine(infix.Left) || c.isSlotAlignedSpine(infix.Right)
}

// compilePerSlotAssign lowers a per-slot-committable value expression into its
// pre-seeded temp slots. Extraction evaluates the comparisons and spine leaves
// once; each slot then commits under its own condition — array slots store
// their mask, scalar slots store their value when the slot condition holds and
// keep the seeded prior value otherwise. Spine arithmetic for a slot compiles
// inside that slot's branch, so a combine (e.g. a division) runs only when its
// slot commits. The whole lowering sits behind any statement condition, and a
// failed bounds check in an operand skips every commit, keeping prior values
// like a normal assignment.
func (c *Compiler) compilePerSlotAssign(expr ast.Expression, info *ExprInfo, tempNames []*ast.Identifier, stmtCond llvm.Value) {
	c.underCond(stmtCond, "stmt_cond_if", "stmt_cond_cont", func() {
		c.pushBoundsGuard("slot_bounds_guard")
		defer c.popBoundsGuard()

		c.pushCondLHSFrame()
		defer c.popCondLHSFrame()

		conds, temps := c.prepareSpine(expr, nil)

		// Masks still unresolved after extraction (a || operand settles its own
		// while extracting): once commits run, each is either moved into its
		// slot or freed by that slot's else arm, and the post-commit sweep
		// releases the ones arithmetic consumed. The bounds-skip path runs no
		// commits, so it frees them all.
		spineMasks := c.unresolvedFrameMasks()

		commitAndSweep := func() {
			for i, outType := range info.OutTypes {
				c.commitSpineSlot(expr, i, slotCondAt(conds, i), tempNames[i], outType)
			}
			c.freeFrameMasks()
		}
		skipAll := func() {
			for _, mask := range spineMasks {
				c.freeSymbolValue(mask, "bounds_skip_mask")
			}
		}
		if !c.withStmtBoundsGuard("slot_bounds_ok", "slot_bounds_commit", "slot_bounds_skip", "slot_bounds_cont", commitAndSweep, skipAll) {
			commitAndSweep()
		}

		c.freeCondTemps(temps)
	})
}

// prepareSpine extracts per-slot conditions for a spine and pre-evaluates its
// leaves. Comparisons are evaluated by extraction (retaining LHS values and
// masks in the condLHS frame); any other leaf (call, literal, identifier, ...)
// is evaluated once here and its slot values stashed in the frame, gated on
// its own condition when it has one — a call whose argument comparison fails
// must not run, and the slots reading it are gated by that same condition.
func (c *Compiler) prepareSpine(expr ast.Expression, temps []condTemp) ([]llvm.Value, []condTemp) {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if _, ok := ast.IsLogicalOr(expr); ok && info != nil && info.HasFallbackOr() {
		return c.extractSlotConds(expr, temps)
	}
	if infix, ok := expr.(*ast.InfixExpression); ok && info.HasAnyComparison() && len(c.pendingLoopRanges(info.Ranges)) == 0 {
		return c.extractComparisonSlots(infix, info, temps)
	}

	if infix, ok := expr.(*ast.InfixExpression); ok && c.isSlotAlignedSpine(expr) {
		var lConds, rConds []llvm.Value
		lConds, temps = c.prepareSpine(infix.Left, temps)
		rConds, temps = c.prepareSpine(infix.Right, temps)
		conds := make([]llvm.Value, len(info.OutTypes))
		for i := range conds {
			conds[i] = c.andConds(slotCondAt(lConds, i), slotCondAt(rConds, i), fmt.Sprintf("spine_and_%d", i))
		}
		return conds, temps
	}

	return c.prepareSpineLeaf(expr, info, temps)
}

// prepareSpineLeaf evaluates one non-comparison spine operand and stashes its
// slot values in the condLHS frame. A leaf with nested comparisons (e.g. a
// call with a conditional argument) is evaluated behind its own gate — the
// false arm stores zero values instead, so the loaded slots always hold
// freeable values (an StrH zero is an owned heap copy, an array zero is null)
// and cleanup stays unconditional. Slots reading a gated leaf carry its
// condition, so a zero seed is never committed.
func (c *Compiler) prepareSpineLeaf(expr ast.Expression, info *ExprInfo, temps []condTemp) ([]llvm.Value, []condTemp) {
	var leafConds []llvm.Value
	leafConds, temps = c.extractSlotConds(expr, temps)

	frame := c.requireCondLHSFrame()
	// A node that resolved itself during extraction (a value-position ||) has
	// its slot values stashed and tracked already; reuse them and its per-slot
	// conditions as-is.
	if _, ok := frame[key(c.FuncNameMangled, expr)]; ok {
		return leafConds, temps
	}

	leafCond := c.foldSlotConds(leafConds)
	if leafCond.IsNil() {
		syms := c.compileExpression(expr, nil)
		frame[key(c.FuncNameMangled, expr)] = syms
		temps = append(temps, condTemp{expr: expr, syms: syms})
		return broadcastConds(leafCond, len(info.OutTypes)), temps
	}

	slots := make([]llvm.Value, len(info.OutTypes))
	for i, outType := range info.OutTypes {
		slots[i] = c.createEntryBlockAlloca(c.mapToLLVMType(outType), fmt.Sprintf("spine_leaf_%d.mem", i))
	}

	ifBlock, elseBlock, contBlock := c.createIfElseCont(leafCond, "spine_leaf_if", "spine_leaf_else", "spine_leaf_cont")

	c.builder.SetInsertPointAtEnd(ifBlock)
	syms := c.compileExpression(expr, nil)
	for i, outType := range info.OutTypes {
		coerced := c.coerceSymbolForType(syms[i], outType, fmt.Sprintf("spine_leaf_val_%d", i))
		c.createStore(coerced.Val, slots[i], coerced.Type)
	}
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(elseBlock)
	for i, outType := range info.OutTypes {
		zero := c.makeZeroValue(outType)
		c.createStore(zero.Val, slots[i], outType)
	}
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
	loaded := make([]*Symbol, len(info.OutTypes))
	for i, outType := range info.OutTypes {
		loaded[i] = &Symbol{Val: c.createLoad(slots[i], outType, fmt.Sprintf("spine_leaf_%d", i)), Type: outType}
	}
	frame[key(c.FuncNameMangled, expr)] = loaded
	temps = append(temps, condTemp{expr: expr, syms: loaded})
	return broadcastConds(leafCond, len(info.OutTypes)), temps
}

// commitSpineSlot commits output slot i of a per-slot spine into its temp
// slot, keeping the seeded prior value when the slot condition fails. When the
// slot's value is a directly committed mask, the failing branch frees it — the
// mask was built during extraction, so a runtime-skipped store must release it.
func (c *Compiler) commitSpineSlot(expr ast.Expression, i int, cond llvm.Value, tempName *ast.Identifier, outType Type) {
	commit := func() {
		val, owned := c.spineSlotValue(expr, i, outType)
		c.commitSlotValue(tempName, val, !owned)
		if owned {
			// Ownership moved into the slot; mark the source so mask cleanup
			// skips it (a fresh combine is simply discarded).
			val.Borrowed = true
		}
	}
	if cond.IsNil() {
		commit()
		return
	}

	ifBlock, elseBlock, contBlock := c.createIfElseCont(cond, "slot_if", "slot_else", "slot_cont")

	c.builder.SetInsertPointAtEnd(ifBlock)
	commit()
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(elseBlock)
	c.freeSkippedSlotMask(expr, i)
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
}

// freeSkippedSlotMask frees the frame mask that slot i of expr would have
// moved, on the runtime path where the slot's store was skipped. The Borrowed
// mark set while building the store arm is ignored — these are exclusive
// runtime paths, and on this one the mask was never moved.
func (c *Compiler) freeSkippedSlotMask(expr ast.Expression, i int) {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if info == nil || i >= len(info.CompareModes) || info.CompareModes[i] != CondArray {
		return
	}
	syms, ok := c.requireCondLHSFrame()[key(c.FuncNameMangled, expr)]
	if !ok || syms[i] == nil {
		return
	}
	c.freeSymbolValue(syms[i], "skipped_mask")
}

// spineSlotValue returns slot i's value for a spine node: retained frame
// values for comparisons and pre-evaluated leaves, freshly combined arithmetic
// otherwise. owned reports whether the caller receives ownership (fresh
// combines and array masks) or a borrowed view it must copy on commit.
func (c *Compiler) spineSlotValue(expr ast.Expression, i int, outType Type) (*Symbol, bool) {
	frame := c.requireCondLHSFrame()
	if syms, ok := frame[key(c.FuncNameMangled, expr)]; ok {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		if info != nil && i < len(info.CompareModes) && info.CompareModes[i] == CondArray {
			return syms[i], true
		}
		return syms[i], false
	}

	infix, ok := expr.(*ast.InfixExpression)
	if !ok {
		panic("internal: spine node missing pre-evaluated slot values")
	}
	l, _ := c.spineSlotValue(infix.Left, i, outType)
	r, _ := c.spineSlotValue(infix.Right, i, outType)
	return c.compileInfix(infix.Operator, l, r, outType), true
}

// freeFrameMasks frees the array masks retained in the current condLHS frame
// that were not moved into a result slot (moved masks are marked borrowed at
// store time). Freed masks are marked too, so later cleanups skip them.
func (c *Compiler) freeFrameMasks() {
	c.freeUnmovedMasksSince(nil)
}

// unresolvedFrameMasks snapshots the frame masks whose ownership is still
// open — not yet moved or freed (marked borrowed) by a nested resolution.
func (c *Compiler) unresolvedFrameMasks() []*Symbol {
	var masks []*Symbol
	for exprKey, lhsSyms := range c.requireCondLHSFrame() {
		exprInfo := c.ExprCache[exprKey]
		if exprInfo == nil {
			continue
		}
		for i, mode := range exprInfo.CompareModes {
			if mode == CondArray && !lhsSyms[i].Borrowed {
				masks = append(masks, lhsSyms[i])
			}
		}
	}
	return masks
}

// frameMaskKeys snapshots the current condLHS frame's keys, so a nested
// resolution (a || operand) can clean up only the masks it stashed itself.
func (c *Compiler) frameMaskKeys() map[ExprKey]struct{} {
	keys := make(map[ExprKey]struct{})
	for exprKey := range c.requireCondLHSFrame() {
		keys[exprKey] = struct{}{}
	}
	return keys
}

// freeUnmovedMasksSince frees array masks stashed in the condLHS frame since
// the snapshot (nil means all) that were not moved into a result slot, marking
// them borrowed so outer cleanups skip them.
func (c *Compiler) freeUnmovedMasksSince(before map[ExprKey]struct{}) {
	for exprKey, lhsSyms := range c.requireCondLHSFrame() {
		if _, ok := before[exprKey]; ok {
			continue
		}
		exprInfo := c.ExprCache[exprKey]
		if exprInfo == nil {
			continue
		}
		for i, mode := range exprInfo.CompareModes {
			if mode != CondArray || lhsSyms[i].Borrowed {
				continue
			}
			c.freeSymbolValue(lhsSyms[i], "")
			lhsSyms[i].Borrowed = true
		}
	}
}

// freeCondTemps releases retained operand temporaries after per-slot commits.
// Committed values are copies (or transferred masks), so the sources are freed
// unconditionally; a skipped gated leaf holds freeable zero values.
func (c *Compiler) freeCondTemps(temps []condTemp) {
	for _, tmp := range temps {
		c.freeTemporary(tmp.expr, tmp.syms)
	}
}

// underCond runs body unconditionally when cond is nil, or guarded by it.
// branchCond is not usable here: its false arm runs cleanupCondExprElse, which
// requires the condLHS frame state these call sites do not maintain.
func (c *Compiler) underCond(cond llvm.Value, ifName, contName string, body func()) {
	if cond.IsNil() {
		body()
		return
	}

	ifBlock, contBlock := c.createIfCont(cond, ifName, contName)
	c.builder.SetInsertPointAtEnd(ifBlock)
	body()
	c.builder.CreateBr(contBlock)
	c.builder.SetInsertPointAtEnd(contBlock)
}

// commitSlotValue stores value into the pre-seeded temp slot, freeing the slot's
// previous value unless it is a borrowed view. copyValue deep-copies the value
// first (for a borrowed scalar LHS that the temp must own); an array mask is
// already owned and passes through. A static string is not copied here: under a
// StrG-typed slot it lives forever (a heap copy there would never be freed),
// and under an StrH-typed slot the store's coercion makes the owned copy.
func (c *Compiler) commitSlotValue(tempIdent *ast.Identifier, value *Symbol, copyValue bool) {
	tempSym, _ := Get(c.Scopes, tempIdent.Value)
	slotElem := tempSym.Type.(Ptr).Elem
	oldValue := c.valueSymbol(tempIdent.Value, tempSym, tempIdent.Value+"_slot_old")

	toStore := value
	if copyValue && !IsStrG(value.Type) {
		toStore = c.deepCopyIfNeeded(value)
	}
	c.storeSymbolToSlot(tempSym, toStore, slotElem, tempIdent.Value+"_slot_store")

	if c.skipBorrowedOldValueFree(oldValue) {
		return
	}
	c.freeSymbolValue(oldValue, tempIdent.Value+"_slot_old")
}
