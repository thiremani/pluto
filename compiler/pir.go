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
		if len(s.Condition) > 0 || !c.planValuesEligible(s.Value, true) {
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

// planValueEligible admits a value as one eval, or as a first when it is a
// value-position ||: each side an eval of one slot. A first plans where reads
// holds, in an assignment, and the solver has left it a side that can fail
// before every fallback.
func (c *Compiler) planValueEligible(expr ast.Expression, reads bool) bool {
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

// planSkipPolicy is what a target holds where its outcome skipped (plan §9):
// the value it had, or zero when the statement introduces it. A discard
// keeps nothing.
func planSkipPolicy(maySkip bool, target pir.Target) pir.SkipPolicy {
	switch {
	case !maySkip || target.Kind == pir.DiscardTarget:
		return pir.NoSkipPolicy
	case target.Fresh:
		return pir.ZeroOnSkip
	default:
		return pir.KeepOnSkip
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
// becomes a first, which has one slot.
func (c *Compiler) buildLetPlan(stmt *ast.LetStatement, fn string) *pir.AssignPlan {
	nameParts := make([]string, len(stmt.Name))
	for i, ident := range stmt.Name {
		nameParts[i] = ident.Value
	}
	plan := &pir.AssignPlan{
		Head:   pir.Head{Label: "assign " + strings.Join(nameParts, ", "), Func: fn, Source: stmt.String()},
		Commit: make([]pir.Mapping, 0, len(stmt.Name)),
	}

	next := pir.OutcomeID(0)
	for _, expr := range stmt.Value {
		if _, isOr := ast.IsLogicalOr(expr); isOr {
			first := c.buildFirst(expr, next)
			plan.Execute = append(plan.Execute, first)
			c.mapNextTarget(plan, stmt, pir.OutcomeRef{Outcome: first.Result}, first.MaySkip())
			next += pir.OutcomeID(1 + len(first.Sides))
			continue
		}

		ev := c.buildEval(expr, next, false)
		plan.Execute = append(plan.Execute, ev)
		for slot := range ev.Slots {
			c.mapNextTarget(plan, stmt, pir.OutcomeRef{Outcome: ev.Result, Slot: slot}, ev.MaySkip())
		}
		next++
	}

	return plan
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
func (c *Compiler) mapNextTarget(plan *pir.AssignPlan, stmt *ast.LetStatement, outcome pir.OutcomeRef, maySkip bool) {
	target := c.planTarget(stmt.Name[len(plan.Commit)])
	plan.Commit = append(plan.Commit, pir.Mapping{Target: target, Outcome: outcome, OnSkip: planSkipPolicy(maySkip, target)})
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
// A first's side runs on a path of its own, so its slots point to memory and
// are loaded where they are read.
type loweredSource struct {
	eval    *pir.Eval
	out     []*Symbol
	yielded llvm.Value
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
// payload.
func (c *Compiler) lowerAssignPlan(plan *pir.AssignPlan) {
	c.pushStmtCtx()
	defer c.popStmtCtx()

	// What each commit replaces, by target.
	replaced := make(map[string]*Symbol)
	for _, d := range plan.Drops {
		if d.Kind == pir.DropReplaced {
			sym, _ := Get(c.Scopes, d.Target)
			replaced[d.Target] = c.derefIfPointer(sym, d.Target+"_old_load")
		}
	}

	var nodes []loweredNode
	for _, node := range plan.Execute {
		switch n := node.(type) {
		case *pir.Eval:
			out, yielded := c.lowerEval(n)
			nodes = append(nodes, loweredNode{id: n.Result, sources: []loweredSource{{eval: n, out: out, yielded: yielded}}})
		case *pir.First:
			nodes = append(nodes, c.lowerFirst(n))
		}
	}

	c.backBranchedTargets(plan, replaced)
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
			flags[i] = c.createEntryBlockAlloca(c.mapToLLVMType(i1), "first_yielded.mem")
			c.createStore(llvm.ConstInt(c.mapToLLVMType(i1), 0, false), flags[i], i1)
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

// backBranchedTargets gives every binding whose mapping lands on one path of
// several storage that all of them reach. A binding a skip can leave alone
// moves to memory when it exists, and a fresh one starts at its zero, the
// value it holds where its outcome skipped; a commit replaces that zero, so
// it joins replaced. A first's binding takes its value on the path of the
// side that yielded, so it sits in memory too.
func (c *Compiler) backBranchedTargets(plan *pir.AssignPlan, replaced map[string]*Symbol) {
	for _, m := range plan.Commit {
		if m.Target.Kind == pir.DiscardTarget {
			continue
		}

		branched := len(m.Transfers) > 1
		switch {
		case m.OnSkip == pir.ZeroOnSkip:
			replaced[m.Target.Name] = c.seedFreshDest(m.Target.Name, m.Target.Type.(Type))
		case branched && m.Target.Fresh:
			c.bindFreshDest(m.Target.Name, m.Target.Type.(Type))
		case m.OnSkip == pir.KeepOnSkip || branched:
			c.promoteExistingSym(m.Target.Name)
		}
	}
}

// landSources commits a node's outcome from the first of its sources, from
// index i on, that yielded, and releases what each one before it built.
func (c *Compiler) landSources(plan *pir.AssignPlan, n loweredNode, i int) {
	c.withCondBranch(n.sources[i].yielded, "assign_write", func() {
		c.commitSource(plan, n, i)
	}, func() {
		c.dropSourceSlots(plan, pir.DropSkipped, n.sources[i], "temp_free")
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
		value := c.derefIfPointer(n.sources[i].out[m.Outcome.Slot], "first_side")
		c.storeValue(m.Target.Name, value, m.Transfers[i] == pir.Copy)
	}
}

// dropDiscarded releases the slots of n's sources that no target took, from
// index i on, each where it ran: a source after the first only where the one
// before it failed.
func (c *Compiler) dropDiscarded(plan *pir.AssignPlan, n loweredNode, i int) {
	c.dropSourceSlots(plan, pir.DropOutcome, n.sources[i], "discard")
	if !planDropsAny(plan, pir.DropOutcome, n.sources[i+1:]) {
		return
	}
	c.withCondBranch(c.builder.CreateNot(n.sources[i].yielded, "discard_failed"), "discard_fallback", func() {
		c.dropDiscarded(plan, n, i+1)
	}, nil)
}

// planDropsAny reports a release of the given kind for one of the sources.
func planDropsAny(plan *pir.AssignPlan, kind pir.DropKind, sources []loweredSource) bool {
	for _, d := range plan.Drops {
		for _, src := range sources {
			if d.Kind == kind && d.Outcome.Outcome == src.eval.Result {
				return true
			}
		}
	}
	return false
}

// dropSourceSlots runs the plan's releases of the given kind for src.
func (c *Compiler) dropSourceSlots(plan *pir.AssignPlan, kind pir.DropKind, src loweredSource, loadName string) {
	for _, d := range plan.Drops {
		if d.Kind == kind && d.Outcome.Outcome == src.eval.Result {
			c.freeSymbolValue(src.out[d.Outcome.Slot], loadName)
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
