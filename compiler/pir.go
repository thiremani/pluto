package compiler

import (
	"fmt"
	"strings"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/pir"
	"tinygo.org/x/go-llvm"
)

// lowerRoutedBody lowers a body's statements in order, routing each eligible
// assignment or print through a PIR plan recorded under the body's entry in
// c.Plans. The body is the script root (fn empty) or that of a function
// specialization without a range parameter, named by fn: a body a range
// parameter drives sits under a function-owned domain and keeps legacy
// lowering (matrix row 14b).
func (c *Compiler) lowerRoutedBody(stmts []ast.Statement, fn string) {
	// A statement may lower another body, so this body's entry comes first.
	body := len(c.Plans)
	c.Plans = append(c.Plans, nil)

	for _, stmt := range stmts {
		if !c.lowerPlanned(stmt, fn, body) {
			c.compileStatement(stmt)
		}
	}
}

// lowerPlanned plans and lowers stmt when the capability router (plan §16
// rule 2) accepts it; an accepted statement has no legacy fallback. An invalid
// plan is a compiler bug. It panics to recoverICE: skipping the statement
// would leave scope state inconsistent with the solved program for everything
// after it.
func (c *Compiler) lowerPlanned(stmt ast.Statement, fn string, body int) bool {
	switch s := stmt.(type) {
	case *ast.LetStatement:
		if !c.planGateEligible(s.Condition) || !c.planValuesEligible(s.Value, true) {
			return false
		}
		plan := c.buildLetPlan(s, fn)
		pir.Elaborate(plan)
		if err := pir.Validate(plan, planBindingCompatible); err != nil {
			panic(fmt.Sprintf("internal: invalid plan for %q: %v", s.String(), err))
		}
		c.Plans[body] = append(c.Plans[body], plan)
		c.promoteStatementBindings(s)
		c.lowerAssignPlan(plan)
		return true
	case *ast.PrintStatement:
		if !c.planValuesEligible(s.Expression.Arguments, false) {
			return false
		}
		plan := c.buildPrintPlan(s, fn)
		if err := pir.ValidatePrint(plan); err != nil {
			panic(fmt.Sprintf("internal: invalid plan for %q: %v", s.String(), err))
		}
		c.Plans[body] = append(c.Plans[body], plan)
		c.lowerPrintPlan(plan)
		return true
	}
	return false
}

// planGateEligible admits a statement without a gate, or one whose gate's
// conditions are each one check (matrix row 15). One condition is the check
// the gate takes; several are the conjuncts of an &&, the sides of a last.
func (c *Compiler) planGateEligible(conds []ast.Expression) bool {
	for _, cond := range conds {
		if !c.planCheckEligible(cond) {
			return false
		}
	}
	return true
}

// planCheckEligible admits a condition as one check: a scalar comparison of
// one slot whose operands are each an expression an eval takes, so no range
// and no condition of their own. A lazy && or || is no comparison, a chain's
// left operand is a condition, and a comparison of several slots has several
// modes; each waits for the node that plans it.
func (c *Compiler) planCheckEligible(expr ast.Expression) bool {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if len(info.CompareModes) != 1 || !info.IsCondScalar(0) {
		return false
	}

	for _, operand := range expr.(*ast.InfixExpression).Operands() {
		if !c.planExprEligible(operand, true) {
			return false
		}
	}
	return true
}

// planValuesEligible reports whether every value of a statement is an
// expression the router admits, of a type plans carry. reads says whether a
// value may hold an index read: an assignment's commit resolves the skip, and
// a print has no plan node for it yet (matrix row 31). Only lowerRoutedBody's
// statements reach it, so none sits under a function-owned domain.
func (c *Compiler) planValuesEligible(exprs []ast.Expression, reads bool) bool {
	for _, expr := range exprs {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		if !planValueTypesSupported(info.OutTypes) {
			return false
		}
		if !c.planValueEligible(expr, reads) {
			return false
		}
	}
	return true
}

// planValueEligible admits a value as one eval, as a first when it is a
// value-position ||, each side an eval of one slot, or as a last when it is a
// value-position &&. A first or a last plans where reads holds, in an
// assignment, and the solver has left a first a side that can fail before
// every fallback.
func (c *Compiler) planValueEligible(expr ast.Expression, reads bool) bool {
	if _, isAnd := ast.IsLogicalAnd(expr); isAnd {
		return reads && c.planLastEligible(expr)
	}
	if _, isOr := ast.IsLogicalOr(expr); !isOr {
		return c.planExprEligible(expr, reads)
	}
	if !reads {
		return false
	}

	for _, side := range fallbackSides(expr) {
		if len(c.ExprCache[key(c.FuncNameMangled, side)].OutTypes) != 1 || !c.planExprEligible(side, true) {
			return false
		}
	}
	return true
}

// planLastEligible admits a value-position && as one last: checks, then an
// eval of one slot, whose value the last yields. A final side that is itself
// a condition waits for the node that takes a check's value.
func (c *Compiler) planLastEligible(expr ast.Expression) bool {
	sides := lastSides(expr)
	final := sides[len(sides)-1]
	for _, side := range sides[:len(sides)-1] {
		if !c.planCheckEligible(side) {
			return false
		}
	}
	return len(c.ExprCache[key(c.FuncNameMangled, final)].OutTypes) == 1 && c.planExprEligible(final, true)
}

// lastSides lists the sides of a value-position && in the order they run. A
// chain is conditions and then one value however it is grouped, so it is one
// list; any other expression is its own one side.
func lastSides(expr ast.Expression) []ast.Expression {
	and, isAnd := ast.IsLogicalAnd(expr)
	if !isAnd {
		return []ast.Expression{expr}
	}
	return append(lastSides(and.Left), lastSides(and.Right)...)
}

// fallbackSides lists the sides of a value-position || in the order they are
// tried. A chain takes the first side that yields however it is grouped, so
// it is one list; any other expression is its own one side.
func fallbackSides(expr ast.Expression) []ast.Expression {
	or, isOr := ast.IsLogicalOr(expr)
	if !isOr {
		return []ast.Expression{expr}
	}
	return append(fallbackSides(or.Left), fallbackSides(or.Right)...)
}

func planValueTypesSupported(types []Type) bool {
	for _, t := range types {
		if !planValueTypeSupported(t) {
			return false
		}
	}
	return true
}

func planValueTypeSupported(t Type) bool {
	if !IsFullyResolvedType(t) {
		return false
	}
	switch t.Kind() {
	case IntKind, FloatKind, RangeKind, StrKind, ArrayKind, TableKind, StructKind:
		return true
	default:
		return false
	}
}

// planExprEligible accepts whitelisted node kinds with no range or
// conditional behavior at any depth. A field or
// column read plans only off an identifier the router can inspect and that
// is not widened; any other receiver fails closed. Deferred, not final: the
// widened case needs ownership resolved against the receiver's effective
// schema, as planSlot does for identifiers, once compileDotExpression types
// the column by that schema too. A call is admitted with arguments that
// cannot fail and none a range, so the callee writes every output.
//
// An index read plans where reads holds, where its failure skips the
// statement's own outcome: not below a literal cell, which zero-fills
// instead, or a call argument, whose failure keeps the call from running
// (matrix rows 2d, 6c).
func (c *Compiler) planExprEligible(expr ast.Expression, reads bool) bool {
	switch e := expr.(type) {
	case *ast.IntegerLiteral, *ast.FloatLiteral, *ast.StringLiteral, *ast.Identifier,
		*ast.InfixExpression, *ast.PrefixExpression, *ast.RangeLiteral:
	case *ast.CallExpression, *ast.ArrayLiteral:
		reads = false
	case *ast.ArrayRangeExpression:
		if !reads {
			return false
		}
	case *ast.DotExpression:
		receiver, isIdent := e.Left.(*ast.Identifier)
		if !isIdent || c.widenedRead(receiver) {
			return false
		}
	default:
		return false
	}
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if info.HasRanges || len(info.Ranges) > 0 || len(info.CollectRanges) > 0 {
		return false
	}
	for _, m := range info.CompareModes {
		if m != CondNone {
			return false
		}
	}
	for _, child := range ast.ExprChildren(expr) {
		if !c.planExprEligible(child, reads) {
			return false
		}
	}
	return true
}

// planOOBScope gives an eval's index reads their scope (plan §9): a failed
// read runs the fallback that follows its side of a first, and skips the
// outcome otherwise. The router admits a read only in those two places.
func planOOBScope(expr ast.Expression, fallbackFollows bool) pir.OOBScope {
	var reads []*ast.ArrayRangeExpression
	collectArrayIndexAccesses(expr, &reads)
	switch {
	case len(reads) == 0:
		return pir.NoChecks
	case fallbackFollows:
		return pir.UseFallback
	default:
		return pir.SkipOutcome
	}
}

// planSkipPolicy is what a target holds where its mapping did not commit
// (plan §9): the value it had, or zero when the statement introduces it. A
// discard keeps nothing.
func planSkipPolicy(mayNotCommit bool, target pir.Target) pir.SkipPolicy {
	switch {
	case target.Kind == pir.DiscardTarget:
		return pir.NoSkipPolicy
	case mayNotCommit && target.Fresh:
		return pir.ZeroOnSkip
	case mayNotCommit:
		return pir.KeepOnSkip
	default:
		return pir.NoSkipPolicy
	}
}

// storedType is the type of the value a binding holds now, the source for
// ownership. It is the third type fact beside an RHS slot's semantic type
// (ExprCache.OutTypes) and a target's merged type (bindingSlotType), and it
// differs from both: a store keeps a heap string's flavor even into a binding
// declared static, and `text = "old"` stores a heap copy while a later read
// solves as StrG. A Ptr-wrapped binding stores its pointee.
func storedType(sym *Symbol) Type {
	if ptr, isPtr := sym.Type.(Ptr); isPtr {
		return ptr.Elem
	}
	return sym.Type
}

// widenedRead reports a binding read whose effective storage differs from
// its solved type.
func (c *Compiler) widenedRead(ident *ast.Identifier) bool {
	solved := c.ExprCache[key(c.FuncNameMangled, ident)].OutTypes[0]
	sym, _ := c.lookupNamedSymbol(ident.Value)
	return !TypeEqual(storedType(sym), solved)
}

// planSlot keeps the solver's type as the slot type and takes ownership from
// effective storage.
func (c *Compiler) planSlot(expr ast.Expression, t Type) pir.Slot {
	ident, isIdent := expr.(*ast.Identifier)
	storage := t
	if isIdent {
		sym, _ := c.lookupNamedSymbol(ident.Value)
		storage = storedType(sym)
	}
	if !typeNeedsCleanup(storage) {
		return pir.Slot{Type: t}
	}
	if isIdent {
		return pir.Slot{Type: t, Ownership: pir.Borrowed, Owner: ident.Value}
	}
	return pir.Slot{Type: t, Ownership: pir.Owned}
}

// planTarget describes one LHS slot. Only an existing binding has an old
// value to replace, so Fresh and HoldsHeap read c.Scopes, as the legacy
// old-value capture does. A function output is bound at function entry: it
// exists from the first statement on, holding its slot's seed until the body
// writes it.
func (c *Compiler) planTarget(ident *ast.Identifier) pir.Target {
	if isDiscard(ident) {
		return pir.Target{Kind: pir.DiscardTarget}
	}

	kind := pir.LocalTarget
	if _, isOutput := c.outputSlotTypes[ident.Value]; isOutput {
		kind = pir.OutputTarget
	}
	targetType := c.bindingSlotType(ident.Value, nil)
	sym, exists := Get(c.Scopes, ident.Value)
	return pir.Target{
		Kind: kind,
		Name: ident.Value,
		Type: targetType,

		TypeOwnsHeap: typeNeedsCleanup(targetType),
		Fresh:        !exists,
		HoldsHeap:    exists && typeNeedsCleanup(storedType(sym)),
	}
}

// buildLetPlan maps each expression's output slots onto the next targets in
// order; the solver validated that the totals match. A value-position ||
// becomes a first and a value-position && a last, each of one slot. A
// statement's condition becomes the gate that takes it, whose region holds
// the values: a gate may leave every mapping unapplied.
func (c *Compiler) buildLetPlan(stmt *ast.LetStatement, fn string) *pir.AssignPlan {
	nameParts := make([]string, len(stmt.Name))
	for i, ident := range stmt.Name {
		nameParts[i] = ident.Value
	}
	plan := &pir.AssignPlan{
		Head:   pir.Head{Label: "assign " + strings.Join(nameParts, ", "), Func: fn, Source: stmt.String()},
		Commit: make([]pir.Mapping, 0, len(stmt.Name)),
	}

	cond, next := c.buildCondition(stmt.Condition)
	gated := cond != nil

	var values []pir.Node
	for _, expr := range stmt.Value {
		if _, isAnd := ast.IsLogicalAnd(expr); isAnd {
			sides := lastSides(expr)
			last := c.buildLast(sides, len(sides)-1, c.ExprCache[key(c.FuncNameMangled, expr)].OutTypes[0], next)
			values = append(values, last)
			c.mapNextTarget(plan, stmt, pir.OutcomeRef{Outcome: last.Result}, true)
			next += pir.OutcomeID(1 + len(sides))
			continue
		}

		if _, isOr := ast.IsLogicalOr(expr); isOr {
			first := c.buildFirst(expr, next)
			values = append(values, first)
			c.mapNextTarget(plan, stmt, pir.OutcomeRef{Outcome: first.Result}, gated || first.MaySkip())
			next += pir.OutcomeID(1 + len(first.Sides))
			continue
		}

		ev := c.buildEval(expr, next, false)
		values = append(values, ev)
		for slot := range ev.Slots {
			c.mapNextTarget(plan, stmt, pir.OutcomeRef{Outcome: ev.Result, Slot: slot}, gated || ev.MaySkip())
		}
		next++
	}

	plan.Execute = values
	if gated {
		plan.Execute = []pir.Node{cond, &pir.Gate{Cond: conditionID, Body: values}}
	}
	return plan
}

// conditionID is the outcome a gate's condition binds: it runs first.
const conditionID = pir.OutcomeID(0)

// buildCondition plans a gate's conditions as the node the gate takes, and
// returns the outcome ID after it: one condition is a check, and the
// conjuncts of an && are the sides of a last of the final one's type. It
// returns no node for a statement without a gate.
func (c *Compiler) buildCondition(conds []ast.Expression) (pir.Node, pir.OutcomeID) {
	switch len(conds) {
	case 0:
		return nil, conditionID
	case 1:
		return c.buildCheck(conds[0].(*ast.InfixExpression), conditionID), conditionID + 1
	}

	t := c.ExprCache[key(c.FuncNameMangled, conds[len(conds)-1])].OutTypes[0]
	return c.buildLast(conds, len(conds), t, conditionID), conditionID + pir.OutcomeID(1+len(conds))
}

// buildLast plans sides as one last of type t, whose outcome is id; its sides
// take the IDs after it. The first checks of them are comparisons, each a
// check, and a side after those is an eval.
func (c *Compiler) buildLast(sides []ast.Expression, checks int, t Type, id pir.OutcomeID) *pir.Last {
	last := &pir.Last{Result: id, Type: t, Sides: make([]pir.Node, len(sides))}
	for i, side := range sides {
		sideID := id + pir.OutcomeID(1+i)
		if i < checks {
			last.Sides[i] = c.buildCheck(side.(*ast.InfixExpression), sideID)
			continue
		}
		last.Sides[i] = c.buildEval(side, sideID, false)
	}
	return last
}

// buildCheck plans the comparison cmp as one check, whose outcome is its left
// operand.
func (c *Compiler) buildCheck(cmp *ast.InfixExpression, id pir.OutcomeID) *pir.Check {
	t := c.ExprCache[key(c.FuncNameMangled, cmp)].OutTypes[0]
	return &pir.Check{Result: id, Expr: cmp, Slots: []pir.Slot{c.planSlot(cmp.Left, t)}, OnOOB: planOOBScope(cmp, false)}
}

// buildEval plans expr as one eval; fallbackFollows says it is a side of a
// first with a side after it.
func (c *Compiler) buildEval(expr ast.Expression, id pir.OutcomeID, fallbackFollows bool) *pir.Eval {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	ev := &pir.Eval{Result: id, Expr: expr, Slots: make([]pir.Slot, len(info.OutTypes)), OnOOB: planOOBScope(expr, fallbackFollows)}
	for slot, t := range info.OutTypes {
		ev.Slots[slot] = c.planSlot(expr, t)
	}
	return ev
}

// buildFirst plans the value-position || expr, whose outcome is id; its sides
// take the IDs after it.
func (c *Compiler) buildFirst(expr ast.Expression, id pir.OutcomeID) *pir.First {
	sides := fallbackSides(expr)
	first := &pir.First{
		Result: id,
		Type:   c.ExprCache[key(c.FuncNameMangled, expr)].OutTypes[0],
		Sides:  make([]*pir.Eval, len(sides)),
	}
	for i, side := range sides {
		first.Sides[i] = c.buildEval(side, id+pir.OutcomeID(1+i), i < len(sides)-1)
	}
	return first
}

// mapNextTarget maps an outcome slot onto the statement's next target.
func (c *Compiler) mapNextTarget(plan *pir.AssignPlan, stmt *ast.LetStatement, outcome pir.OutcomeRef, mayNotCommit bool) {
	target := c.planTarget(stmt.Name[len(plan.Commit)])
	plan.Commit = append(plan.Commit, pir.Mapping{Target: target, Outcome: outcome, OnSkip: planSkipPolicy(mayNotCommit, target)})
}

// buildPrintPlan gives each argument to the invocation in source order: a
// string literal as itself, since print formats it in place, and any other
// argument as the outcome of its eval.
func (c *Compiler) buildPrintPlan(stmt *ast.PrintStatement, fn string) *pir.PrintPlan {
	plan := &pir.PrintPlan{Head: pir.Head{Label: "print", Func: fn, Source: stmt.String()}}
	for _, expr := range stmt.Expression.Arguments {
		if lit, isLit := expr.(*ast.StringLiteral); isLit {
			plan.Args = append(plan.Args, pir.PrintArg{Literal: lit})
			continue
		}

		ev := c.buildEval(expr, pir.OutcomeID(len(plan.Evals)), false)
		plan.Evals = append(plan.Evals, ev)
		plan.Args = append(plan.Args, pir.PrintArg{Outcome: ev.Result})
	}
	return plan
}

// planBindingCompatible is the validator's type relation: directional
// binding-slot compatibility, never display or mangle equality.
func planBindingCompatible(target, outcome pir.Type) bool {
	return bindingSlotCompatible(target.(Type), outcome.(Type))
}

// loweredSource is one eval that can supply a node's outcome, after it ran:
// its slots, and the bit that says whether it yielded, nil when it always
// does. The bit covers every slot: an eval yields or fails as a whole.
// A side of a first or a last runs on a path of its own, so its slots point
// to memory and are loaded where they are read. A last's final side runs only
// where the sides before it yielded, and ran is the bit that says it did; it
// is nil for an eval that always runs.
type loweredSource struct {
	eval    *pir.Eval
	out     []*Symbol
	yielded llvm.Value
	ran     llvm.Value
}

// loweredNode is one node of a plan's execute region after it ran: the evals
// that can supply its outcome, in the order they were tried.
type loweredNode struct {
	id      pir.OutcomeID
	sources []loweredSource
}

// lowerAssignPlan implements an elaborated plan (plan §6, §9, §13) and
// decides nothing itself: old values are captured before any eval runs, each
// outcome's mappings land where it yielded, and the releases of replaced
// values run after every mapping has landed, so a swap never reads a freed
// payload. Under a gate the values run and commit only where its condition
// yielded, so every binding has storage both paths reach before they part.
// A target the statement introduces is no name until the values have run: a
// string's marker that spells it stays text.
func (c *Compiler) lowerAssignPlan(plan *pir.AssignPlan) {
	c.pushStmtCtx()
	defer c.popStmtCtx()

	// What each commit replaces, by target.
	replaced := make(map[string]*Symbol)
	for _, name := range plan.Replaced {
		sym, _ := Get(c.Scopes, name)
		replaced[name] = c.derefIfPointer(sym, name+"_old_load")
	}

	cond := plan.Condition()
	if cond == nil {
		nodes := c.lowerValues(plan)
		c.nameFreshTargets(c.backBranchedTargets(plan), replaced)
		c.commitValues(plan, nodes, replaced)
		return
	}

	admitted := c.lowerCondition(cond, plan.Tested)
	fresh := c.backBranchedTargets(plan)
	c.withCondBranch(admitted, "gate", func() {
		nodes := c.lowerValues(plan)
		c.nameFreshTargets(fresh, replaced)
		c.commitValues(plan, nodes, replaced)
	}, nil)
}

// lowerCondition runs a gate's condition and returns the bit that says
// whether it yielded. A gate only tests its condition, so what each check
// built is released once it is compared.
func (c *Compiler) lowerCondition(cond pir.Node, tested []pir.OutcomeRef) llvm.Value {
	if check, isCheck := cond.(*pir.Check); isCheck {
		return c.lowerTested(check, tested)
	}
	return c.lowerLast(cond.(*pir.Last), tested).yielded
}

// lowerTested runs a check whose outcome is only tested: it releases what the
// check built and returns the bit that says whether it yielded.
func (c *Compiler) lowerTested(ch *pir.Check, tested []pir.OutcomeRef) llvm.Value {
	out, yielded := c.lowerCheck(ch)
	for _, ref := range tested {
		if ref.Outcome == ch.Result {
			c.freeSymbolValue(out[ref.Slot], "check_free")
		}
	}
	return yielded
}

// lowerValues runs the nodes whose outcomes the commit maps.
func (c *Compiler) lowerValues(plan *pir.AssignPlan) []loweredNode {
	var nodes []loweredNode
	for _, node := range plan.Values() {
		switch n := node.(type) {
		case *pir.Eval:
			out, yielded := c.lowerEval(n)
			nodes = append(nodes, loweredNode{id: n.Result, sources: []loweredSource{{eval: n, out: out, yielded: yielded}}})
		case *pir.First:
			nodes = append(nodes, c.lowerFirst(n))
		case *pir.Last:
			nodes = append(nodes, loweredNode{id: n.Result, sources: []loweredSource{c.lowerLast(n, plan.Tested)}})
		}
	}
	return nodes
}

// commitValues lands every node's mappings, then runs the releases that
// follow them.
func (c *Compiler) commitValues(plan *pir.AssignPlan, nodes []loweredNode, replaced map[string]*Symbol) {
	for _, n := range nodes {
		c.landSources(plan, n, 0)
	}

	for _, n := range nodes {
		c.dropDiscarded(plan, n, 0)
	}
	for _, n := range nodes {
		c.withCondBranch(c.nodeYielded(n), "assign_free", func() {
			c.dropReplaced(plan, n.id, replaced)
		}, nil)
	}
}

// lowerCheck compares a check's operands. It returns the left operand, which
// is the check's outcome, and the bit that says whether it yielded. The right
// operand is only compared, so it is released here. A read past the end gives
// a zero the comparison still computes with, and clears the bit.
func (c *Compiler) lowerCheck(ch *pir.Check) ([]*Symbol, llvm.Value) {
	if ch.OnOOB == pir.NoChecks {
		return c.compareOperands(ch.Expr)
	}

	guard := c.pushBoundsGuard("check_bounds_guard")
	defer c.popBoundsGuard()

	out, held := c.compareOperands(ch.Expr)
	inBounds := c.createLoad(guard, Int{Width: 1}, "check_bounds_ok")
	return out, c.builder.CreateAnd(held, inBounds, "check_yielded")
}

// compareOperands evaluates a comparison of one scalar slot: its left operand
// and the bit that says whether the comparison holds.
func (c *Compiler) compareOperands(cmp *ast.InfixExpression) ([]*Symbol, llvm.Value) {
	left := c.compileExpression(cmp.Left, nil)
	right := c.compileExpression(cmp.Right, nil)
	out, held := c.compareScalars(cmp.Operator, left[0], right[0])
	c.freeTemporary(cmp.Right, right)
	return []*Symbol{out}, held
}

// lowerEval compiles one eval and returns its outcome with the bit that says
// whether it yielded; the bit is nil for an eval that always does. An eval
// that may fail runs under a bounds guard of its own, which each of its
// index reads ANDs its in-bounds bit into (plan §9). A read past the end
// gives a zero that later operators still compute with, and a call lowered
// after it does not run, so the outcome holds a value where it failed too.
func (c *Compiler) lowerEval(ev *pir.Eval) ([]*Symbol, llvm.Value) {
	if !ev.MayFail() {
		return c.compileExpression(ev.Expr, nil), llvm.Value{}
	}

	guard := c.pushBoundsGuard("assign_bounds_guard")
	defer c.popBoundsGuard()

	out := c.compileExpression(ev.Expr, nil)
	return out, c.createLoad(guard, Int{Width: 1}, "assign_bounds_ok")
}

// lowerFirst runs a first's sides in order until one yields (plan §4). A
// side's yield flag starts false, so it reads false wherever the side did
// not run.
func (c *Compiler) lowerFirst(f *pir.First) loweredNode {
	i1 := Int{Width: 1}
	sides := make([]loweredSource, len(f.Sides))
	flags := make([]llvm.Value, len(f.Sides))
	for i, side := range f.Sides {
		sides[i] = loweredSource{eval: side}
		if side.MayFail() {
			flags[i] = c.clearedFlag("first_yielded.mem")
		}
	}

	c.lowerSides(sides, flags, 0)

	for i, flag := range flags {
		if !flag.IsNil() {
			sides[i].yielded = c.createLoad(flag, i1, "first_yielded")
		}
	}
	return loweredNode{id: f.Result, sources: sides}
}

// lowerSides runs side i, stages its value and its yield flag, and runs the
// sides after it only where it failed.
func (c *Compiler) lowerSides(sides []loweredSource, flags []llvm.Value, i int) {
	out, yielded := c.lowerEval(sides[i].eval)
	slot := c.createEntryBlockAlloca(c.mapToLLVMType(out[0].Type), "first_side.mem")
	c.createStore(out[0].Val, slot, out[0].Type)
	sides[i].out = []*Symbol{{Val: slot, Type: Ptr{Elem: out[0].Type}}}
	if yielded.IsNil() {
		return
	}

	c.createStore(yielded, flags[i], Int{Width: 1})
	if i+1 == len(sides) {
		return
	}
	c.withCondBranch(c.builder.CreateNot(yielded, "first_failed"), "first_fallback", func() {
		c.lowerSides(sides, flags, i+1)
	}, nil)
}

// clearedFlag allocates a bit of memory that reads false until a path sets it.
func (c *Compiler) clearedFlag(name string) llvm.Value {
	i1 := Int{Width: 1}
	flag := c.createEntryBlockAlloca(c.mapToLLVMType(i1), name)
	c.createStore(llvm.ConstInt(c.mapToLLVMType(i1), 0, false), flag, i1)
	return flag
}

// lowerLast runs a last's sides in order while each yields (plan §4). It
// returns the final side as the one source of the last's outcome, with the
// bit that says every side yielded. A final side that is a check is only
// tested, so the source then holds nothing but that bit.
func (c *Compiler) lowerLast(l *pir.Last, tested []pir.OutcomeRef) loweredSource {
	i1 := Int{Width: 1}
	src := loweredSource{}
	yielded := c.clearedFlag("last_yielded.mem")
	ran := yielded
	if final, isEval := l.Sides[len(l.Sides)-1].(*pir.Eval); isEval && final.MayFail() {
		ran = c.clearedFlag("last_ran.mem")
	}

	c.lowerLastSides(l.Sides, tested, &src, yielded, ran)

	src.yielded = c.createLoad(yielded, i1, "last_yielded")
	src.ran = src.yielded
	if ran != yielded {
		src.ran = c.createLoad(ran, i1, "last_ran")
	}
	return src
}

// lowerLastSides runs the first of sides and, where it yielded, the sides
// after it. A side before the final one is a check that is only tested. The
// final side sets the yielded flag, and an eval there stages its value in src
// and sets the ran flag.
func (c *Compiler) lowerLastSides(sides []pir.Node, tested []pir.OutcomeRef, src *loweredSource, yielded, ran llvm.Value) {
	i1 := Int{Width: 1}
	if len(sides) > 1 {
		held := c.lowerTested(sides[0].(*pir.Check), tested)
		c.withCondBranch(held, "last_next", func() {
			c.lowerLastSides(sides[1:], tested, src, yielded, ran)
		}, nil)
		return
	}

	final, isEval := sides[0].(*pir.Eval)
	if !isEval {
		c.createStore(c.lowerTested(sides[0].(*pir.Check), tested), yielded, i1)
		return
	}

	out, evalYielded := c.lowerEval(final)
	slot := c.createEntryBlockAlloca(c.mapToLLVMType(out[0].Type), "last_side.mem")
	c.createStore(out[0].Val, slot, out[0].Type)
	src.eval = final
	src.out = []*Symbol{{Val: slot, Type: Ptr{Elem: out[0].Type}}}

	held := llvm.ConstInt(c.mapToLLVMType(i1), 1, false)
	c.createStore(held, ran, i1)
	if !evalYielded.IsNil() {
		c.createStore(evalYielded, yielded, i1)
	}
}

// nodeYielded is the bit that says whether a node's outcome yielded: some
// source did. It is nil when the last one always yields.
func (c *Compiler) nodeYielded(n loweredNode) llvm.Value {
	yielded := n.sources[len(n.sources)-1].yielded
	if yielded.IsNil() {
		return yielded
	}
	for _, src := range n.sources[:len(n.sources)-1] {
		yielded = c.builder.CreateOr(src.yielded, yielded, "first_any_yielded")
	}
	return yielded
}

// freshSlot is the memory of a target the statement introduces, before the
// statement names it, and the zero it holds where a mapping may leave it
// unwritten.
type freshSlot struct {
	name string
	t    Type
	mem  llvm.Value
	zero *Symbol
}

// backBranchedTargets gives every binding whose mapping lands on one path of
// several storage that all of them reach. A binding a skip can leave alone
// moves to memory when it exists, and a fresh one starts at its zero, the
// value it holds where its mapping did not commit. A first's binding takes its
// value on the path of the side that yielded, so it sits in memory too. The
// fresh bindings' memory is returned unnamed, for nameFreshTargets.
func (c *Compiler) backBranchedTargets(plan *pir.AssignPlan) []freshSlot {
	var fresh []freshSlot
	for _, m := range plan.Commit {
		if m.Target.Kind == pir.DiscardTarget {
			continue
		}

		branched := len(m.Transfers) > 1
		switch {
		case m.OnSkip == pir.ZeroOnSkip:
			slot := c.newFreshSlot(m.Target)
			slot.zero = c.makeZeroValue(slot.t)
			c.createStore(slot.zero.Val, slot.mem, slot.t)
			fresh = append(fresh, slot)
		case branched && m.Target.Fresh:
			fresh = append(fresh, c.newFreshSlot(m.Target))
		case m.OnSkip == pir.KeepOnSkip || branched:
			c.promoteExistingSym(m.Target.Name)
		}
	}
	return fresh
}

// newFreshSlot allocates memory nothing has written yet for a fresh target.
func (c *Compiler) newFreshSlot(target pir.Target) freshSlot {
	t := target.Type.(Type)
	return freshSlot{name: target.Name, t: t, mem: c.createEntryBlockAlloca(c.mapToLLVMType(t), target.Name+".mem")}
}

// nameFreshTargets binds each fresh target's name to its memory, once the
// statement's values have run. A commit replaces the zero a slot holds, so
// the zero joins replaced.
func (c *Compiler) nameFreshTargets(fresh []freshSlot, replaced map[string]*Symbol) {
	for _, slot := range fresh {
		Put(c.Scopes, slot.name, &Symbol{Val: slot.mem, Type: Ptr{Elem: slot.t}})
		if slot.zero != nil {
			replaced[slot.name] = slot.zero
		}
	}
}

// landSources commits a node's outcome from the first of its sources, from
// index i on, that yielded, and releases what each one before it built.
func (c *Compiler) landSources(plan *pir.AssignPlan, n loweredNode, i int) {
	c.withCondBranch(n.sources[i].yielded, "assign_write", func() {
		c.commitSource(plan, n, i)
	}, func() {
		c.dropRanSlots(plan.Skipped, n.sources[i], "temp_free")
		if i+1 < len(n.sources) {
			c.landSources(plan, n, i+1)
		}
	})
}

// commitSource lands the mappings of n's outcome with the value source i
// supplies, loaded where it sits in memory.
func (c *Compiler) commitSource(plan *pir.AssignPlan, n loweredNode, i int) {
	for _, m := range plan.Commit {
		if m.Outcome.Outcome != n.id || m.Target.Kind == pir.DiscardTarget {
			continue
		}
		value := c.derefIfPointer(n.sources[i].out[m.Outcome.Slot], "commit_value")
		c.storeValue(m.Target.Name, value, m.Transfers[i] == pir.Copy)
	}
}

// dropDiscarded releases what n's sources built for `_`, from index i on,
// each where it ran: a source after the first only where the one before it
// failed.
func (c *Compiler) dropDiscarded(plan *pir.AssignPlan, n loweredNode, i int) {
	c.dropRanSlots(plan.Discarded, n.sources[i], "discard")
	if !discardsAny(plan, n.sources[i+1:]) {
		return
	}
	c.withCondBranch(c.builder.CreateNot(n.sources[i].yielded, "discard_failed"), "discard_fallback", func() {
		c.dropDiscarded(plan, n, i+1)
	}, nil)
}

// discardsAny reports whether the plan discards an owned slot of one of the
// sources.
func discardsAny(plan *pir.AssignPlan, sources []loweredSource) bool {
	for _, ref := range plan.Discarded {
		for _, src := range sources {
			if ref.Outcome == src.eval.Result {
				return true
			}
		}
	}
	return false
}

// dropRanSlots releases the slots of src that refs names, where src ran.
func (c *Compiler) dropRanSlots(refs []pir.OutcomeRef, src loweredSource, loadName string) {
	if !namesSource(refs, src) {
		return
	}
	c.withCondBranch(src.ran, "ran", func() {
		c.dropSourceSlots(refs, src, loadName)
	}, nil)
}

// namesSource reports whether refs names a slot of src.
func namesSource(refs []pir.OutcomeRef, src loweredSource) bool {
	for _, ref := range refs {
		if ref.Outcome == src.eval.Result {
			return true
		}
	}
	return false
}

// dropSourceSlots releases the slots of src that refs names.
func (c *Compiler) dropSourceSlots(refs []pir.OutcomeRef, src loweredSource, loadName string) {
	for _, ref := range refs {
		if ref.Outcome == src.eval.Result {
			c.freeSymbolValue(src.out[ref.Slot], loadName)
		}
	}
}

// dropReplaced releases what the mappings of outcome id replaced.
func (c *Compiler) dropReplaced(plan *pir.AssignPlan, id pir.OutcomeID, replaced map[string]*Symbol) {
	for _, m := range plan.Commit {
		if m.Outcome.Outcome != id {
			continue
		}
		if old, held := replaced[m.Target.Name]; held {
			c.freeSymbolValue(old, "old_assign")
		}
	}
}

// lowerPrintPlan implements a print plan (plan §3) and decides nothing
// itself. How the line is assembled is the backend's choice: each argument is
// formatted where it is evaluated, and one write prints the line.
func (c *Compiler) lowerPrintPlan(plan *pir.PrintPlan) {
	c.pushStmtCtx()
	defer c.popStmtCtx()

	var formatStr string
	var args []llvm.Value
	var toFree []llvm.Value
	for _, arg := range plan.Args {
		if arg.Literal != nil {
			c.appendPrintLiteral(arg.Literal, &formatStr, &args, &toFree)
			continue
		}
		c.appendPrintOutcome(plan.Evals[arg.Outcome], &formatStr, &args, &toFree)
	}

	c.writePrintLine(formatStr, args, toFree)
}

// appendPrintOutcome evaluates one argument and adds its slots to the line.
// The invocation releases each owned slot it took: a string after the write,
// which reads it, and any other value once it is formatted.
func (c *Compiler) appendPrintOutcome(ev *pir.Eval, formatStr *string, args *[]llvm.Value, toFree *[]llvm.Value) {
	syms := c.compileExpression(ev.Expr, nil)
	loaded := make([]*Symbol, len(syms))
	for i, sym := range syms {
		loaded[i] = c.derefIfPointer(sym, "print_deref")
		owned := ev.Slots[i].Ownership == pir.Owned
		c.appendPrintSymbol(loaded[i], ev.Expr.Tok(), owned && loaded[i].Type.Kind() == StrKind, formatStr, args, toFree)
	}

	for i, sym := range syms {
		if ev.Slots[i].Ownership == pir.Owned && loaded[i].Type.Kind() != StrKind {
			c.freeSymbolValue(sym, "temp_free")
		}
	}
}
