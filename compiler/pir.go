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
		if len(s.Condition) > 0 || !c.planValuesEligible(s.Value) {
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
		if !c.planValuesEligible(s.Expression.Arguments) {
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
// ordinary expression of a type plans carry. Only lowerRoutedBody's
// statements reach it, so none sits under a function-owned domain.
func (c *Compiler) planValuesEligible(exprs []ast.Expression) bool {
	for _, expr := range exprs {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		if !planValueTypesSupported(info.OutTypes) {
			return false
		}
		if !c.planExprEligible(expr) {
			return false
		}
	}
	return true
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

// planExprEligible accepts ordinary expression trees only: whitelisted node
// kinds with no range or conditional behavior at any depth. Block-layout
// literals wait for a one-line eval-operand spelling (plan §12). A field or
// column read plans only off an identifier the router can inspect and that
// is not widened; any other receiver fails closed. Deferred, not final: the
// widened case needs ownership resolved against the receiver's effective
// schema, as planSlot does for identifiers, once compileDotExpression types
// the column by that schema too. A call is admitted with ordinary arguments
// only: none can fail and none is a range, so the callee writes every output.
func (c *Compiler) planExprEligible(expr ast.Expression) bool {
	switch e := expr.(type) {
	case *ast.IntegerLiteral, *ast.FloatLiteral, *ast.StringLiteral, *ast.Identifier,
		*ast.InfixExpression, *ast.PrefixExpression, *ast.RangeLiteral, *ast.CallExpression:
	case *ast.ArrayLiteral:
		if e.Block || len(e.Headers) > 0 {
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
		if !c.planExprEligible(child) {
			return false
		}
	}
	return true
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
// order; the solver validated that the totals match.
func (c *Compiler) buildLetPlan(stmt *ast.LetStatement, fn string) *pir.AssignPlan {
	evals := make([]*pir.Eval, len(stmt.Value))
	commit := make([]pir.Mapping, 0, len(stmt.Name))
	for i, expr := range stmt.Value {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		evals[i] = &pir.Eval{Result: pir.OutcomeID(i), Expr: expr, Slots: make([]pir.Slot, len(info.OutTypes))}

		for slot, t := range info.OutTypes {
			evals[i].Slots[slot] = c.planSlot(expr, t)
			commit = append(commit, pir.Mapping{
				Target:  c.planTarget(stmt.Name[len(commit)]),
				Outcome: pir.OutcomeRef{Outcome: pir.OutcomeID(i), Slot: slot},
			})
		}
	}

	nameParts := make([]string, len(stmt.Name))
	for i, ident := range stmt.Name {
		nameParts[i] = ident.Value
	}

	return &pir.AssignPlan{
		Head:   pir.Head{Label: "assign " + strings.Join(nameParts, ", "), Func: fn, Source: stmt.String()},
		Evals:  evals,
		Commit: commit,
	}
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

		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		ev := &pir.Eval{Result: pir.OutcomeID(len(plan.Evals)), Expr: expr, Slots: make([]pir.Slot, len(info.OutTypes))}
		for slot, t := range info.OutTypes {
			ev.Slots[slot] = c.planSlot(expr, t)
		}
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

// lowerAssignPlan implements an elaborated plan (plan §6, §13) and decides
// nothing itself: old values are captured before any eval runs, and the
// releases run after every mapping has landed so a swap never reads a freed
// payload.
func (c *Compiler) lowerAssignPlan(plan *pir.AssignPlan) {
	c.pushStmtCtx()
	defer c.popStmtCtx()

	replaced := make(map[string]*Symbol)
	for _, d := range plan.Drops {
		if d.Kind == pir.DropReplaced {
			sym, _ := Get(c.Scopes, d.Target)
			replaced[d.Target] = c.derefIfPointer(sym, d.Target+"_old_load")
		}
	}

	outs := make([][]*Symbol, len(plan.Evals))
	for i, ev := range plan.Evals {
		outs[i] = c.compileExpression(ev.Expr, nil)
	}
	for _, m := range plan.Commit {
		if m.Target.Kind == pir.DiscardTarget {
			continue
		}
		c.storeValue(m.Target.Name, outs[m.Outcome.Outcome][m.Outcome.Slot], m.Transfer == pir.Copy)
	}
	for _, d := range plan.Drops {
		switch d.Kind {
		case pir.DropOutcome:
			c.freeSymbolValue(outs[d.Outcome.Outcome][d.Outcome.Slot], "discard")
		case pir.DropReplaced:
			c.freeSymbolValue(replaced[d.Target], "old_assign")
		default:
			panic(fmt.Sprintf("internal: plan %s: unknown drop kind %d", plan.Label, d.Kind))
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
