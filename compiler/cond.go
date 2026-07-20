package compiler

import (
	"fmt"
	"slices"

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

// andGates short-circuits the i1 gates of a statement's condition list ("did
// every condition yield?"). Every entry is a validated failable scalar gate,
// so compileGate returns a non-nil value. Bounds-guard folding is the caller's
// concern.
func (c *Compiler) andGates(exprs []ast.Expression) llvm.Value {
	if len(exprs) == 0 {
		return llvm.Value{}
	}
	cond := c.compileGate(exprs[0])
	for _, expr := range exprs[1:] {
		cond = c.compileShortCircuitAnd(cond, func() llvm.Value {
			return c.compileGate(expr)
		}, "stmt_and")
	}
	return cond
}

// evalConditions ANDs the i1 gates of a list of condition expressions and folds
// in the bounds-guard check. The caller must pushBoundsGuard before and
// popBoundsGuard after.
func (c *Compiler) evalConditions(exprs []ast.Expression, guardPtr llvm.Value) llvm.Value {
	// Every expression reaching evalConditions is a validated failable scalar
	// gate; splitCondRanges routes bare range drivers into the range list. Thus
	// compileGate returns a non-nil gate for each expression.
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

// OutputSlot bundles a conditional assignment's three parallel facts: the real
// destination, the borrowed temp slot its value is staged in, and the resolved
// slot type. Threading one []OutputSlot instead of three lockstep slices keeps
// dest/temp/outType aligned and turns sub-range slicing into a single
// expression. The temp name embeds a unique counter, so the dest-to-temp
// binding is established once at creation and carried, never recomputed.
type OutputSlot struct {
	dest    *ast.Identifier
	temp    *ast.Identifier
	outType Type
}

// slotAssignIdents returns the temp slots each value is written into and the
// real destinations that own move/copy and old-value cleanup — the write and
// ownership identifiers compileAssignments/newExprAssign consume, in that order.
func slotAssignIdents(slots []OutputSlot) (temps, dests []*ast.Identifier) {
	temps = make([]*ast.Identifier, len(slots))
	dests = make([]*ast.Identifier, len(slots))
	for i, s := range slots {
		temps[i] = s.temp
		dests[i] = s.dest
	}
	return temps, dests
}

func slotTempStrings(slots []OutputSlot) []string {
	names := make([]string, len(slots))
	for i, s := range slots {
		names[i] = s.temp.Value
	}
	return names
}

func (c *Compiler) createConditionalTempOutputs(stmt *ast.LetStatement) []OutputSlot {
	outTypes := c.resolvedDestTypes(stmt.Name, c.collectOutTypes(stmt))
	return c.createConditionalTempOutputsFor(stmt.Name, outTypes)
}

func (c *Compiler) createConditionalTempOutputsFor(dest []*ast.Identifier, outTypes []Type) []OutputSlot {
	slots := make([]OutputSlot, len(dest))
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
		slots[i] = OutputSlot{dest: ident, temp: tempIdent, outType: outTypes[i]}
	}
	return slots
}

func (c *Compiler) commitConditionalOutputs(slots []OutputSlot) {
	for _, s := range slots {
		tempSym, _ := Get(c.Scopes, s.temp.Value)

		finalType := s.outType
		finalVal := c.createLoad(tempSym.Val, finalType, s.dest.Value+"_cond_final")
		finalSym := &Symbol{
			Val:  finalVal,
			Type: finalType,
		}

		oldSym, exists := Get(c.Scopes, s.dest.Value)
		if !exists {
			Put(c.Scopes, s.dest.Value, finalSym)
			continue
		}

		if _, ok := oldSym.Type.(Ptr); ok {
			c.storeSymbolToSlot(oldSym, finalSym, oldSym.Type.(Ptr).Elem, s.dest.Value+"_cond_commit")

			// Keep pointer element type in sync (important for string ownership flags).
			updated := GetCopy(oldSym)
			updated.Type = Ptr{Elem: finalType}
			// oldSym came from Get above and nothing since touched the scope stack, so
			// SetExisting always finds the binding — and it must update it in its own
			// scope (which may be an outer block), not shadow it via Put in the current.
			SetExisting(c.Scopes, s.dest.Value, updated)
			continue
		}

		// Non-pointer symbols are replaced directly. Old value ownership is already
		// handled in the IF branch assignment into temp slots.
		Put(c.Scopes, s.dest.Value, finalSym)
	}
}

// aliasCondDests maps existing destination names to conditional temp slots so
// RHS reads during IF-branch assignment see the latest temp writes.
func (c *Compiler) aliasCondDests(slots []OutputSlot) map[string]*Symbol {
	aliases := make(map[string]*Symbol, len(slots))

	for _, s := range slots {
		oldSym, exists := Get(c.Scopes, s.dest.Value)
		if !exists {
			continue
		}
		tempSym, ok := Get(c.Scopes, s.temp.Value)
		if !ok {
			continue
		}
		aliases[s.dest.Value] = oldSym
		SetExisting(c.Scopes, s.dest.Value, tempSym)
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
func (c *Compiler) compileCondAssignments(slots []OutputSlot, exprs []ast.Expression) {
	aliases := c.aliasCondDests(slots)
	temps, dests := slotAssignIdents(slots)
	c.compileAssignments(temps, dests, exprs)
	c.restoreCondDests(aliases)
}

// compileCondExprAssigns compiles staged RHS expressions with destinations
// aliased to their temp slots. Bounds bits stay nil: the caller's guard
// (cond_value_guard) owns skip/commit for the whole staged group.
func (c *Compiler) compileCondExprAssigns(slots []OutputSlot, exprs []ast.Expression) []exprAssign {
	aliases := c.aliasCondDests(slots)
	defer c.restoreCondDests(aliases)

	temps, dests := slotAssignIdents(slots)
	oldValues := c.captureOldValues(temps)
	assigns := make([]exprAssign, 0, len(exprs))
	i := 0
	for _, expr := range exprs {
		res := c.compileExpression(expr, temps[i:])
		assigns = append(assigns, c.newExprAssign(expr, llvm.Value{}, res, temps[i:], dests[i:], oldValues[i:]))
		i += len(res)
	}
	return assigns
}

func (c *Compiler) compileCondAssignmentsWithGuard(slots []OutputSlot, exprs []ast.Expression, guardPtr llvm.Value) {
	assigns := c.compileCondExprAssigns(slots, exprs)
	c.finishAssignmentsWithGuard(assigns, guardPtr)
}

func (c *Compiler) createStageTempOutputsFor(commit []OutputSlot) []OutputSlot {
	stage := make([]OutputSlot, len(commit))
	for i, cs := range commit {
		outType := cs.outType

		tempName := fmt.Sprintf("condstage_%s_%d", cs.temp.Value, c.tmpCounter)
		c.tmpCounter++
		tempIdent := &ast.Identifier{Value: tempName}

		ptr := c.createEntryBlockAlloca(c.mapToLLVMType(outType), tempName+".mem")
		stageTempSym := &Symbol{
			Val:      ptr,
			Type:     Ptr{Elem: outType},
			Borrowed: true,
		}
		// Seed each iteration's private stage slot from the commit temp's current
		// running value, so a local skip keeps the value carried across iterations.
		seed := c.resolveDestSeed(cs.temp, outType)
		seed = c.deepCopyIfNeeded(seed)
		c.storeSymbolToSlot(stageTempSym, seed, outType, tempName+"_seed")
		Put(c.Scopes, tempName, stageTempSym)
		stage[i] = OutputSlot{dest: cs.dest, temp: tempIdent, outType: outType}
	}
	return stage
}

func (c *Compiler) commitStageTempOutputs(commit []OutputSlot, stage []OutputSlot) {
	for i := range stage {
		stageSym, _ := Get(c.Scopes, stage[i].temp.Value)
		stagedValue := c.valueSymbol(stage[i].temp.Value, stageSym, stage[i].temp.Value+"_stage_final")
		c.commitSlotValue(commit[i].temp, stagedValue, false)
	}
}

func (c *Compiler) stageCondRangedExpr(expr ast.Expression, stage []OutputSlot) {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	stageAliases := c.aliasCondDests(stage)
	defer c.restoreCondDests(stageAliases)

	compileStageAssign := func() {
		guardPtr := c.pushBoundsGuard("cond_value_guard")
		defer c.popBoundsGuard()

		c.compileCondAssignmentsWithGuard(stage, []ast.Expression{expr}, guardPtr)
	}

	c.withLoopNestVersioned(info.Ranges, []ast.Expression{expr}, func() {
		// Iterators bound by this loop leave no pending ranges, so a per-slot
		// value expression commits slot-by-slot here too — a ranged statement
		// condition must not change the value's per-slot semantics.
		if c.perSlotCommittable(expr, info) {
			c.compilePerSlotAssign(expr, info, stage, llvm.Value{})
			return
		}
		if c.hasCondExprInTree(expr) {
			c.compileCondExprValue(expr, llvm.Value{}, compileStageAssign)
			return
		}

		compileStageAssign()
	})
}

// stageCondRangedAssignments stages every RHS into a fresh per-iteration stage
// temp and returns the stage slots flat, aligned 1:1 with commit. The outer
// alias resolves destination reads to commit temps (this iteration's running
// values) throughout staging; stageCondRangedExpr adds an inner alias to each
// expression's own stage temp for self-referential reads/writes. No stage folds
// into a commit temp until commitCondRangedStages, so sibling RHS expressions
// read the iteration-start values.
func (c *Compiler) stageCondRangedAssignments(assignExprs []ast.Expression, commit []OutputSlot) []OutputSlot {
	stage := make([]OutputSlot, 0, len(commit))
	assignTargetIdx := 0

	allAliases := c.aliasCondDests(commit)
	defer c.restoreCondDests(allAliases)

	for _, expr := range assignExprs {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		numOutputs := len(info.OutTypes)
		exprStage := c.createStageTempOutputsFor(commit[assignTargetIdx : assignTargetIdx+numOutputs])

		c.stageCondRangedExpr(expr, exprStage)
		stage = append(stage, exprStage...)
		assignTargetIdx += numOutputs
	}

	return stage
}

// commitCondRangedStages folds every staged value back into its commit temp
// (flat, 1:1 with commit) once all RHS expressions have been staged, then drops
// the stage temps from scope.
func (c *Compiler) commitCondRangedStages(commit, stage []OutputSlot) {
	c.commitStageTempOutputs(commit, stage)
	DeleteBulk(c.Scopes, slotTempStrings(stage))
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

	slots := c.createConditionalTempOutputs(stmt)

	ifBlock, contBlock := c.createIfCont(cond, "if", "continue")

	c.builder.SetInsertPointAtEnd(ifBlock)
	c.compileCondAssignments(slots, stmt.Value)
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
	c.commitConditionalOutputs(slots)
	DeleteBulk(c.Scopes, slotTempStrings(slots))
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

	// Array-literal cells are local-resolution boundaries: each cell resolves
	// its own condition at its own level. Statement-level extraction must stop
	// here — descending in would lift a cell's comparison into a gate over the
	// whole assignment, which keeps the old value on failure (breaking local
	// resolution) and double-evaluates the cell, leaking its heap LHS on the
	// pass-through path.
	if _, ok := expr.(*ast.ArrayLiteral); ok {
		return nil, temps
	}

	// A value-position || resolves per slot: slot i falls back only when slot i
	// of the left side failed to yield.
	if or, ok := ast.IsLogicalOr(expr); ok && info.HasFallbackOr() {
		return c.extractFallbackOrSlots(or, info, temps)
	}

	// A value-position && gates per slot: slot i yields the right side's slot i
	// only when slot i of the left side yielded.
	if and, ok := ast.IsLogicalAnd(expr); ok && info.HasCondAnd() {
		return c.extractGatingAndSlots(and, info, temps)
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

	// An out-of-bounds read while evaluating an operand is a failed condition
	// on every lane the operand feeds (the read yields nothing, so nothing
	// computed from it does either).
	guardPtr := c.pushBoundsGuard("cmp_bounds_guard")
	left := c.compileExpression(infix.Left, nil)
	right := c.compileExpression(infix.Right, nil)
	var boundsOK llvm.Value
	if c.stmtBoundsUsed() {
		boundsOK = c.createLoad(guardPtr, Int{Width: 1}, "cmp_bounds_ok")
	}
	c.popBoundsGuard()

	conds := make([]llvm.Value, len(left))
	lhsSyms := make([]*Symbol, len(left))
	for i := range left {
		operandCond := c.andConds(slotCondAt(lConds, i), slotCondAt(rConds, i), fmt.Sprintf("operand_cond_%d", i))
		operandCond = c.andConds(operandCond, boundsOK, fmt.Sprintf("operand_bounds_%d", i))
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

	// Track the left operand's payloads with the same symbols the frame
	// retains (the deref'd LHS; the mask's already-released source for array
	// slots), so a consumer free that marks the frame entry borrowed also
	// neutralizes this temp. Skip a chained inner comparison (a > b < c): its
	// value came from substituting that already-retained comparison, so it is
	// the SAME symbol already tracked — re-tracking would free it twice.
	if _, chained := frame[key(c.FuncNameMangled, infix.Left)]; !chained {
		trackSyms := make([]*Symbol, len(left))
		for i := range left {
			trackSyms[i] = lhsSyms[i]
			if info.IsMask(i) {
				trackSyms[i] = left[i]
			}
		}
		temps = append(temps, condTemp{expr: infix.Left, syms: trackSyms})
	}

	// Right is comparison-only; left is retained for condLHS substitution. A
	// parenthesized comparison on the right (a > (b < c)) is released here,
	// once — recording the free marks its frame entry so the cleanups skip
	// the same payload on every later path.
	c.freeConsumedTemporary(infix.Right, right)
	return conds, temps
}

// extractFallbackOrSlots resolves a value-position || into the shared
// extraction form: chosen values stashed in the condLHS frame under the ||
// node, yield flags returned as the slot conditions. Every result slot ends
// up owned or a freeable zero (an StrH zero is an owned heap copy, an array
// zero is null), so the slots travel as one unconditional temporary across
// both commit conventions (move-on-true vs copy-then-free).
func (c *Compiler) extractFallbackOrSlots(or *ast.InfixExpression, info *ExprInfo, temps []condTemp) ([]llvm.Value, []condTemp) {
	slots := c.newLogicalSlots(info.OutTypes)

	lConds := c.resolveLogicalSide(slots, or.Left, nil)

	// The right side evaluates at most once, as a unit, when any slot missed;
	// its per-slot stores then fill only still-empty slots. The solver rejects
	// a left that cannot fail, so the fold is never nil.
	leftAllYielded := c.foldSlotConds(lConds)
	if leftAllYielded.IsNil() {
		panic("internal: value-position || requires a failable left operand")
	}
	someMissed := c.builder.CreateNot(leftAllYielded, "or_some_missed")
	c.withCondBranch(someMissed, "or_rhs", func() {
		c.resolveLogicalSide(slots, or.Right, c.slotMissedGate(slots))
	}, nil)

	return c.finishLogicalSlots(slots, or, temps)
}

// extractGatingAndSlots resolves a value-position && into the shared
// extraction form: the right side's values stashed in the condLHS frame under
// the && node, per-slot yield flags returned as the slot conditions. The left
// contributes only its yield flags — its values are never a result, so its
// temporaries are released on the spot. The right evaluates at most once, as
// a unit, when any left slot yielded; its per-slot stores then fill only the
// slots whose left side yielded, so slot i yields exactly when every side's
// slot i does.
func (c *Compiler) extractGatingAndSlots(and *ast.InfixExpression, info *ExprInfo, temps []condTemp) ([]llvm.Value, []condTemp) {
	before := c.frameMaskKeys()
	lConds, leftTemps := c.prepareSpine(and.Left, nil)
	c.freeCondTemps(leftTemps)
	c.freeUnmovedMasksSince(before)

	// The solver rejects a left that cannot fail, so the fold is never nil.
	if c.foldSlotConds(lConds).IsNil() {
		panic("internal: value-position && requires a failable left operand")
	}

	// Map the left's conditions onto the result arity: fold when a multi-slot
	// condition gates one value (every slot must yield), broadcast when one
	// condition gates a multi-slot value.
	if len(lConds) != len(info.OutTypes) {
		lConds = broadcastConds(c.foldSlotConds(lConds), len(info.OutTypes))
	}

	slots := c.newLogicalSlots(info.OutTypes)
	c.withCondBranch(c.orSlotConds(lConds, "and_some_yielded"), "and_rhs", func() {
		c.resolveLogicalSide(slots, and.Right, func(i int) llvm.Value {
			return slotCondAt(lConds, i)
		})
	}, nil)

	return c.finishLogicalSlots(slots, and, temps)
}

// finishLogicalSlots loads a ||/&& lowering's resolved slots, stashes them in
// the condLHS frame under the operator node, and tracks them as one
// unconditional temporary.
func (c *Compiler) finishLogicalSlots(slots []logicalSlot, node ast.Expression, temps []condTemp) ([]llvm.Value, []condTemp) {
	conds, loaded := c.loadLogicalResults(slots)
	frame := c.requireCondLHSFrame()
	frame[key(c.FuncNameMangled, node)] = loaded
	temps = append(temps, condTemp{expr: node, syms: loaded})
	return conds, temps
}

// slotMissedGate restricts a ||'s right-side stores to slots the left did not
// already fill.
func (c *Compiler) slotMissedGate(slots []logicalSlot) func(int) llvm.Value {
	i1 := Int{Width: 1}
	return func(i int) llvm.Value {
		yielded := c.createLoad(slots[i].yield, i1, fmt.Sprintf("or_need_%d", i))
		return c.builder.CreateNot(yielded, fmt.Sprintf("or_miss_%d", i))
	}
}

// orSlotConds ORs per-slot conditions into "any slot yields". A nil entry
// means that slot always yields, so the fold is nil (always).
func (c *Compiler) orSlotConds(conds []llvm.Value, name string) llvm.Value {
	var res llvm.Value
	for _, sc := range conds {
		if sc.IsNil() {
			return llvm.Value{}
		}
		if res.IsNil() {
			res = sc
			continue
		}
		res = c.builder.CreateOr(res, sc, name)
	}
	return res
}

// logicalSlot is one output slot of a value-position ||/&& lowering: the
// slot's type and the allocas holding its resolved value and yield flag.
type logicalSlot struct {
	outType Type
	value   llvm.Value // alloca for the resolved value
	yield   llvm.Value // i1 alloca: did this slot yield?
}

func (c *Compiler) newLogicalSlots(outTypes []Type) []logicalSlot {
	i1 := Int{Width: 1}
	i1Ty := c.mapToLLVMType(i1)
	slots := make([]logicalSlot, len(outTypes))
	for i, outType := range outTypes {
		slots[i] = logicalSlot{
			outType: outType,
			value:   c.createEntryBlockAlloca(c.mapToLLVMType(outType), fmt.Sprintf("logical_slot_%d.mem", i)),
			yield:   c.createEntryBlockAlloca(i1Ty, fmt.Sprintf("logical_yield_%d.mem", i)),
		}
		c.createStore(llvm.ConstInt(i1Ty, 0, false), slots[i].yield, i1)
	}
	return slots
}

// resolveLogicalSide resolves one ||/&& operand into the result slots.
// takeGate (nil = unrestricted) further gates each slot's store: a ||'s right
// side fills only slots the left left empty, a &&'s right side fills only
// slots whose left side yielded.
func (c *Compiler) resolveLogicalSide(slots []logicalSlot, side ast.Expression, takeGate func(int) llvm.Value) []llvm.Value {
	before := c.frameMaskKeys()
	conds, sideTemps := c.prepareSpine(side, nil)

	for i, sl := range slots {
		cond := slotCondAt(conds, i)
		if takeGate != nil {
			cond = c.andConds(takeGate(i), cond, fmt.Sprintf("logical_take_%d", i))
		}
		c.withSlotCondBranch(side, i, cond, fmt.Sprintf("logical_store_%d", i), func() {
			c.storeLogicalValue(side, i, sl)
		})
	}

	c.freeCondTemps(sideTemps)
	c.freeUnmovedMasksSince(before)
	return conds
}

// loadLogicalResults zero-seeds the slots that never yielded and returns the
// yield flags and loaded values per slot.
func (c *Compiler) loadLogicalResults(slots []logicalSlot) ([]llvm.Value, []*Symbol) {
	i1 := Int{Width: 1}
	conds := make([]llvm.Value, len(slots))
	loaded := make([]*Symbol, len(slots))
	for i, sl := range slots {
		yield := c.createLoad(sl.yield, i1, fmt.Sprintf("logical_yield_%d", i))
		c.withCondBranch(c.builder.CreateNot(yield, fmt.Sprintf("logical_zero_%d", i)), fmt.Sprintf("logical_seed_%d", i), func() {
			zero := c.makeZeroValue(sl.outType)
			c.createStore(zero.Val, sl.value, sl.outType)
		}, nil)
		conds[i] = yield
		loaded[i] = &Symbol{Val: c.createLoad(sl.value, sl.outType, fmt.Sprintf("logical_slot_%d", i)), Type: sl.outType}
	}
	return conds, loaded
}

// cleanupCondExprElse frees temporaries retained during cond-expr extraction
// that are not consumed when the condition evaluates to false, plus the frame
// masks a nested resolution has not already released.
func (c *Compiler) cleanupCondExprElse(temps []condTemp) {
	c.freeCondTemps(temps)
	c.freeUnmovedMasksSince(nil)
}

// compileCondExprValue gates value-position cond expressions on the ANDed
// per-slot conditions: comparisons compose as AND, and value-position ||/&&
// contribute their yield flags after resolving through logical slots.
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
	c.withCondBranch(cond, "cond", onTrue, func() {
		c.cleanupCondExprElse(temps)
		onFalse()
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

type statementArrayCollector struct {
	literal     *ast.ArrayLiteral
	destination *ast.Identifier
	oldValue    *Symbol
	scalar      *ArrayAccumulator
	stacked     *stackedArrayAccumulator
}

func (c *Compiler) newStatementArrayCollector(lit *ast.ArrayLiteral, destination *ast.Identifier, arrayType Array) *statementArrayCollector {
	collector := &statementArrayCollector{
		literal:     lit,
		destination: destination,
		oldValue:    c.captureOldValues([]*ast.Identifier{destination})[0],
	}
	if arrayLiteralHasArrayCells(lit, arrayType) {
		collector.stacked = c.newStackedArrayAccumulator(arrayType)
	} else {
		collector.scalar = c.NewArrayAccumulator(arrayType)
	}
	return collector
}

func (c *Compiler) appendStatementArrayCollector(collector *statementArrayCollector) {
	if collector.stacked != nil {
		c.appendStackedArrayCollector(collector.stacked, collector.literal)
		return
	}
	c.appendArrayLiteral(collector.scalar, collector.literal)
}

func (c *Compiler) commitStatementArrayCollector(collector *statementArrayCollector) {
	var result *Symbol
	if collector.stacked != nil {
		result = c.stackedArrayAccumulatorResult(collector.stacked)
	} else {
		result = c.ArrayAccResult(collector.scalar)
	}
	c.storeAccumulatedArray(collector.destination, result, collector.oldValue)
}

// compileCondRangedStatement lowers ranged statement conditions.
// Statement conditions are shared across the whole assignment. They determine
// the outer admitted iteration domain, while each RHS expression keeps any
// extra local drivers to itself inside that shared gate. Top-level inline array
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

	collectors := []*statementArrayCollector{}

	targetIdx := 0
	for _, expr := range stmt.Value {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		numOutputs := len(info.OutTypes)

		if lit, ok := expr.(*ast.ArrayLiteral); ok && isInlineArrayCollector(lit) {
			arrayType := info.OutTypes[0].(Array)
			collectors = append(collectors, c.newStatementArrayCollector(lit, stmt.Name[targetIdx], arrayType))

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

	var assignSlots []OutputSlot
	if hasAssigns {
		assignSlots = c.createConditionalTempOutputsFor(assignDests, assignOutTypes)
	}

	appendCollectors := func() {
		for _, collector := range collectors {
			c.appendStatementArrayCollector(collector)
		}
	}

	// Guards and RHS staging must read the same loop-carried destination slots.
	aliases := c.aliasCondDests(assignSlots)
	c.withCondRangeLoop(condRanges, condExprs, loopProbes, "cond_iter_guard", "cond_iter_if", "cond_iter_cont", func() {
		c.compileCondRangedIteration(assignExprs, assignSlots, appendCollectors)
	})
	c.restoreCondDests(aliases)

	if hasAssigns {
		c.commitConditionalOutputs(assignSlots)
		DeleteBulk(c.Scopes, slotTempStrings(assignSlots))
	}

	for _, collector := range collectors {
		c.commitStatementArrayCollector(collector)
	}
}

func (c *Compiler) storeAccumulatedArray(dest *ast.Identifier, result, oldValue *Symbol) {
	c.storeValue(dest.Value, result, false)
	if oldValue == nil || c.skipBorrowedOldValueFree(oldValue) {
		return
	}
	c.freeSymbolValue(oldValue, "old_accum")
}

// compileCondRangedIteration runs inside the per-iteration body of
// compileCondRangedStatement. It stages scalar assignments independently
// and appends array literal cells to accumulators.
func (c *Compiler) compileCondRangedIteration(
	assignExprs []ast.Expression,
	assignSlots []OutputSlot,
	appendCollectors func(),
) {
	// Accum-only: no assigns, just push cells.
	if len(assignSlots) == 0 {
		appendCollectors()
		return
	}

	// The outer statement condition admits one iteration here, but sibling RHS
	// expressions may still skip later in that same admitted step. Each RHS first
	// writes into a private stage temp under its own bounds guard, so a local skip
	// leaves that stage temp seeded with the prior value without suppressing
	// sibling RHS writes.
	stage := c.stageCondRangedAssignments(assignExprs, assignSlots)
	appendCollectors()
	c.commitCondRangedStages(assignSlots, stage)
}

// compileCondExprStatement handles let statements that have conditional
// expressions (comparisons) embedded in their value expressions.
// Each value expression is processed independently: its conditions are
// ANDed with statement conditions and branched on separately, so
// p, q = a > 2, d < 10 evaluates each condition independently rather
// than ANDing them all-or-nothing.
func (c *Compiler) compileCondExprStatement(stmt *ast.LetStatement, stmtCond llvm.Value) {
	c.prePromoteConditionalCallArgs(stmt.Value)

	slots := c.createConditionalTempOutputs(stmt)

	targetIdx := 0
	for _, expr := range stmt.Value {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		numOutputs := len(info.OutTypes)
		exprSlots := slots[targetIdx : targetIdx+numOutputs]

		// A ranged tree with a value-position ||/&& cannot lower inline: the
		// logical node needs extraction to pre-resolve it, and extraction needs
		// bound iterators. Loop first — the per-iteration
		// body re-enters the standard extraction with no pending ranges.
		if len(c.pendingLoopRanges(info.Ranges)) > 0 && treeHasLogicalCond(c.ExprCache, c.FuncNameMangled, expr) {
			c.withCondBranch(stmtCond, "ranged_logical", func() {
				c.stageCondRangedExpr(expr, exprSlots)
			}, nil)
			targetIdx += numOutputs
			continue
		}

		// A multi-return value expression whose slots carry independent
		// conditions (Pair > Pair, (Pair > Pair) + Pair(1, 1), Mix > Mix, ...)
		// commits each slot under its own condition: array slots mask
		// (overwrite), scalar slots keep the seeded prior value when their
		// condition fails — the same keep-old a single comparison gives. A
		// failing slot affects only itself. The cell-wise AND is reserved for
		// gate/condition position and || fallback; everything else keeps the
		// single ANDed gate below.
		if c.perSlotCommittable(expr, info) {
			c.compilePerSlotAssign(expr, info, exprSlots, stmtCond)
		} else {
			c.compileCondExprValue(expr, stmtCond, func() {
				c.compileCondAssignments(exprSlots, []ast.Expression{expr})
			})
		}

		targetIdx += numOutputs
	}

	c.commitConditionalOutputs(slots)
	DeleteBulk(c.Scopes, slotTempStrings(slots))
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
// value-position || or &&, or an arithmetic infix tree over one — the shapes
// whose output slots stay aligned with their operands' slots, so per-slot
// conditions are well-defined all the way to the root.
func (c *Compiler) isSlotAlignedSpine(expr ast.Expression) bool {
	infix, ok := expr.(*ast.InfixExpression)
	if !ok {
		return false
	}
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if len(c.pendingLoopRanges(info.Ranges)) > 0 {
		return false
	}
	// Any conditional slot mode — comparison, mask, ||, && — makes this node
	// a per-slot root.
	if slices.ContainsFunc(info.CompareModes, func(m CondMode) bool { return m != CondNone }) {
		return true
	}
	return c.isSlotAlignedSpine(infix.Left) || c.isSlotAlignedSpine(infix.Right)
}

// compilePerSlotAssign lowers a per-slot-committable value expression into its
// pre-seeded temp slots. Extraction evaluates the comparisons and spine leaves
// once (folding any out-of-bounds read into the affected lanes' conditions);
// each slot then commits under its own condition — array slots store their
// mask, scalar slots store their value when the slot condition holds and keep
// the seeded prior value otherwise. Spine arithmetic for a slot compiles
// inside that slot's branch, so a combine (e.g. a division) runs only when its
// slot commits. The whole lowering sits behind any statement condition.
func (c *Compiler) compilePerSlotAssign(expr ast.Expression, info *ExprInfo, slots []OutputSlot, stmtCond llvm.Value) {
	c.withCondBranch(stmtCond, "stmt_cond", func() {
		c.pushCondLHSFrame()
		defer c.popCondLHSFrame()

		conds, temps := c.prepareSpine(expr, nil)
		for i, outType := range info.OutTypes {
			c.commitSpineSlot(expr, i, slotCondAt(conds, i), slots[i].temp, outType)
		}

		// Masks not moved into a slot (freed by a slot's else arm, or consumed
		// by arithmetic) are swept here; moved ones were marked at store.
		c.freeUnmovedMasksSince(nil)
		c.freeCondTemps(temps)
	}, nil)
}

// prepareSpine extracts per-slot conditions for a spine and pre-evaluates its
// leaves. Comparisons are evaluated by extraction (retaining LHS values and
// masks in the condLHS frame); any other leaf (call, literal, identifier, ...)
// is evaluated once here and its slot values stashed in the frame, gated on
// its own condition when it has one — a call whose argument comparison fails
// must not run, and the slots reading it are gated by that same condition.
func (c *Compiler) prepareSpine(expr ast.Expression, temps []condTemp) ([]llvm.Value, []condTemp) {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if infix, ok := expr.(*ast.InfixExpression); ok &&
		((infix.IsLogicalOr() && info.HasFallbackOr()) || (infix.IsLogicalAnd() && info.HasCondAnd())) {
		return c.extractSlotConds(expr, temps)
	}

	infix, isInfix := expr.(*ast.InfixExpression)
	if isInfix && info.HasAnyComparison() && len(c.pendingLoopRanges(info.Ranges)) == 0 {
		return c.extractComparisonSlots(infix, info, temps)
	}

	if isInfix && c.isSlotAlignedSpine(expr) {
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
	leafCond := c.foldSlotConds(leafConds)
	if leafCond.IsNil() {
		guardPtr := c.pushBoundsGuard("leaf_bounds_guard")
		syms := c.compileExpression(expr, nil)
		var boundsOK llvm.Value
		if c.stmtBoundsUsed() {
			boundsOK = c.createLoad(guardPtr, Int{Width: 1}, "leaf_bounds_ok")
		}
		c.popBoundsGuard()
		frame[key(c.FuncNameMangled, expr)] = syms
		temps = append(temps, condTemp{expr: expr, syms: syms})
		return broadcastConds(boundsOK, len(info.OutTypes)), temps
	}

	slots := make([]llvm.Value, len(info.OutTypes))
	for i, outType := range info.OutTypes {
		slots[i] = c.createEntryBlockAlloca(c.mapToLLVMType(outType), fmt.Sprintf("spine_leaf_%d.mem", i))
	}

	// The guard alloca lives in the entry block and is seeded in-bounds, so a
	// skipped leaf reads back as clean.
	guardPtr := c.pushBoundsGuard("leaf_bounds_guard")
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
	if c.stmtBoundsUsed() {
		boundsOK := c.createLoad(guardPtr, Int{Width: 1}, "leaf_bounds_ok")
		leafCond = c.andConds(leafCond, boundsOK, "leaf_and_bounds")
	}
	c.popBoundsGuard()

	loaded := make([]*Symbol, len(info.OutTypes))
	for i, outType := range info.OutTypes {
		loaded[i] = &Symbol{Val: c.createLoad(slots[i], outType, fmt.Sprintf("spine_leaf_%d", i)), Type: outType}
	}
	frame[key(c.FuncNameMangled, expr)] = loaded
	temps = append(temps, condTemp{expr: expr, syms: loaded})
	return broadcastConds(leafCond, len(info.OutTypes)), temps
}

// commitSpineSlot commits output slot i of a per-slot spine into its temp
// slot, keeping the seeded prior value when the slot condition fails.
func (c *Compiler) commitSpineSlot(expr ast.Expression, i int, cond llvm.Value, tempName *ast.Identifier, outType Type) {
	c.withSlotCondBranch(expr, i, cond, "slot", func() {
		val, owned := c.spineSlotValue(expr, i, outType)
		c.commitSlotValue(tempName, val, !owned)
		if owned {
			// Ownership moved into the slot; mark the source so mask cleanup
			// skips it (a fresh combine is simply discarded).
			val.Borrowed = true
		}
	})
}

// withSlotCondBranch emits store for slot i of expr under cond (unconditionally
// when nil). The skipped arm frees the mask that slot would have moved: masks
// are built during extraction, so a runtime-skipped store must release one.
func (c *Compiler) withSlotCondBranch(expr ast.Expression, i int, cond llvm.Value, name string, store func()) {
	c.withCondBranch(cond, name, store, func() {
		c.freeSkippedSlotMask(expr, i)
	})
}

// storeLogicalValue writes slot i of side into a ||/&& result slot and marks it
// yielded. A borrowed view must deref before the copy decision — a leaf's
// slot value may be Ptr-wrapped, and copying the wrapper aliases the pointee
// into a slot that outlives the freed source (a past double-free). A static
// string is left to the store's coercion, which copies only into heap-owned
// slot types.
func (c *Compiler) storeLogicalValue(side ast.Expression, i int, sl logicalSlot) {
	val, owned := c.spineSlotValue(side, i, sl.outType)
	if owned {
		val.Borrowed = true
	} else {
		val = c.derefIfPointer(val, fmt.Sprintf("logical_take_val_%d", i))
		val = c.deepCopyIfNeeded(val)
	}
	coerced := c.coerceSymbolForType(val, sl.outType, fmt.Sprintf("logical_val_%d", i))
	c.createStore(coerced.Val, sl.value, coerced.Type)
	i1 := Int{Width: 1}
	c.createStore(llvm.ConstInt(c.mapToLLVMType(i1), 1, false), sl.yield, i1)
}

// freeSkippedSlotMask frees the frame mask that slot i of expr would have
// moved, on the runtime path where the slot's store was skipped. The Borrowed
// mark set while building the store arm is ignored — these are exclusive
// runtime paths, and on this one the mask was never moved.
func (c *Compiler) freeSkippedSlotMask(expr ast.Expression, i int) {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if !info.IsMask(i) {
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
		if info.IsMask(i) {
			return syms[i], true
		}
		return syms[i], false
	}

	infix, ok := expr.(*ast.InfixExpression)
	if !ok {
		panic("internal: spine node missing pre-evaluated slot values")
	}
	// A node combined directly must be plain arithmetic: every conditional
	// node (comparison, mask, ||, &&) was pre-resolved into the frame above,
	// so a conditional mode here means a new CondMode slipped past the spine
	// dispatch unclassified.
	if info := c.ExprCache[key(c.FuncNameMangled, expr)]; info != nil &&
		slices.ContainsFunc(info.CompareModes, func(m CondMode) bool { return m != CondNone }) {
		panic(fmt.Sprintf("internal: conditional spine node %s combined as plain arithmetic", infix.Operator))
	}
	l, _ := c.spineSlotValue(infix.Left, i, outType)
	r, _ := c.spineSlotValue(infix.Right, i, outType)
	return c.compileInfix(infix.Operator, l, r, outType), true
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
		for i := range exprInfo.CompareModes {
			if !exprInfo.IsMask(i) || lhsSyms[i].Borrowed {
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

// withCondBranch emits onTrue under cond and onFalse (nillable) otherwise. A nil
// cond means unconditional: onTrue runs inline and onFalse is dead. Blocks are
// named name_if / name_else / name_cont.
func (c *Compiler) withCondBranch(cond llvm.Value, name string, onTrue, onFalse func()) {
	if cond.IsNil() {
		onTrue()
		return
	}

	if onFalse == nil {
		ifBlock, contBlock := c.createIfCont(cond, name+"_if", name+"_cont")
		c.builder.SetInsertPointAtEnd(ifBlock)
		onTrue()
		c.builder.CreateBr(contBlock)
		c.builder.SetInsertPointAtEnd(contBlock)
		return
	}

	ifBlock, elseBlock, contBlock := c.createIfElseCont(cond, name+"_if", name+"_else", name+"_cont")

	c.builder.SetInsertPointAtEnd(ifBlock)
	onTrue()
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(elseBlock)
	onFalse()
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
	if copyValue {
		toStore = c.deepCopyIfNeeded(value)
	}
	c.storeSymbolToSlot(tempSym, toStore, slotElem, tempIdent.Value+"_slot_store")

	if c.skipBorrowedOldValueFree(oldValue) {
		return
	}
	c.freeSymbolValue(oldValue, tempIdent.Value+"_slot_old")
}
