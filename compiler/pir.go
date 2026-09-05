package compiler

import (
	"fmt"
	"strings"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/pir"
)

// planLetStatement is the capability router (plan §16 rule 2). Only the
// script statement loop invokes it, so every named target is a script-root
// binding; an accepted statement has no legacy fallback.
func (c *Compiler) planLetStatement(stmt *ast.LetStatement) (*pir.AssignPlan, bool) {
	if len(stmt.Condition) > 0 || len(stmt.Name) != len(stmt.Value) {
		return nil, false
	}
	// The arity check above already forces every RHS to a single output slot:
	// each expression yields at least one, and the solver validated the total.
	for _, expr := range stmt.Value {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		if !planValueTypeSupported(info.OutTypes[0]) {
			return nil, false
		}
		if !c.planExprEligible(expr) {
			return nil, false
		}
	}
	return c.buildLetPlan(stmt), true
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
// the column by that schema too.
func (c *Compiler) planExprEligible(expr ast.Expression) bool {
	switch e := expr.(type) {
	case *ast.IntegerLiteral, *ast.FloatLiteral, *ast.StringLiteral, *ast.Identifier,
		*ast.InfixExpression, *ast.PrefixExpression, *ast.RangeLiteral:
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

// planSlot keeps the solver's type as the slot type — an empty reset reads
// as [Empty] whatever backs it — and takes ownership from effective storage.
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

// planLocalTarget describes one LHS binding. Only an existing script-local
// binding has an old value to replace, so Fresh and HoldsHeap read c.Scopes,
// as the legacy old-value capture does.
func (c *Compiler) planLocalTarget(name string) pir.Target {
	targetType := c.bindingSlotType(name, nil)
	sym, exists := Get(c.Scopes, name)
	return pir.Target{
		Kind: pir.LocalTarget,
		Name: name,
		Type: targetType,

		TypeOwnsHeap: typeNeedsCleanup(targetType),
		Fresh:        !exists,
		HoldsHeap:    exists && typeNeedsCleanup(storedType(sym)),
	}
}

func (c *Compiler) buildLetPlan(stmt *ast.LetStatement) *pir.AssignPlan {
	evals := make([]*pir.Eval, len(stmt.Value))
	for i, expr := range stmt.Value {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		evals[i] = &pir.Eval{
			Result: pir.OutcomeID(i),
			Expr:   expr,
			Slots:  []pir.Slot{c.planSlot(expr, info.OutTypes[0])},
		}
	}

	commit := make([]pir.Mapping, len(stmt.Name))
	nameParts := make([]string, len(stmt.Name))
	for i, ident := range stmt.Name {
		nameParts[i] = ident.Value
		target := pir.Target{Kind: pir.DiscardTarget}
		if !isDiscard(ident) {
			target = c.planLocalTarget(ident.Value)
		}
		commit[i] = pir.Mapping{Target: target, Outcome: pir.OutcomeRef{Outcome: pir.OutcomeID(i)}}
	}

	return &pir.AssignPlan{
		Label:  "assign_" + strings.Join(nameParts, "_"),
		Source: stmt.String(),
		Evals:  evals,
		Commit: commit,
	}
}

// planBindingCompatible is the validator's type relation: directional
// binding-slot compatibility, never display or mangle equality.
func planBindingCompatible(target, outcome pir.Type) bool {
	return bindingSlotCompatible(target.(Type), outcome.(Type))
}

// lowerAssignPlan implements an elaborated plan (plan §6, §13) and decides
// nothing itself: old values are captured before any eval runs, and the
// releases run after every mapping has landed so a swap never reads a freed
// payload. The two panics turn a mismatch between the plan's ownership and
// the values actually produced or stored into an ICE.
func (c *Compiler) lowerAssignPlan(plan *pir.AssignPlan) {
	c.pushStmtCtx()
	defer c.popStmtCtx()

	replaced := make(map[string]*Symbol)
	for _, d := range plan.Drops {
		if d.Kind == pir.DropReplaced {
			sym, _ := Get(c.Scopes, d.Target)
			replaced[d.Target] = c.valueSymbol(d.Target, sym, d.Target+"_old_load")
		}
	}

	outs := make([][]*Symbol, len(plan.Evals))
	for i, ev := range plan.Evals {
		outs[i] = c.compileExpression(ev.Expr, nil)
		for s, slot := range ev.Slots {
			if (slot.Ownership == pir.Unmanaged) == typeNeedsCleanup(outs[i][s].Type) {
				panic(fmt.Sprintf("plan %s: eval %%t%d slot %d annotated %v but lowers to %s", plan.Label, i, s, slot.Ownership, outs[i][s].Type.String()))
			}
		}
	}
	for _, m := range plan.Commit {
		if m.Target.Kind == pir.DiscardTarget {
			continue
		}
		c.storeValue(m.Target.Name, outs[m.Outcome.Outcome][m.Outcome.Slot], m.Transfer == pir.Copy)
		sym, _ := c.lookupNamedSymbol(m.Target.Name)
		holds := typeNeedsCleanup(storedType(sym))
		if holds != (m.Transfer != pir.Store) {
			panic(fmt.Sprintf("plan %s: target %s after %s holds heap state: %t", plan.Label, m.Target.Name, m.Transfer, holds))
		}
	}
	for _, d := range plan.Drops {
		switch d.Kind {
		case pir.DropOutcome:
			c.freeSymbolValue(outs[d.Outcome.Outcome][d.Outcome.Slot], "discard")
		case pir.DropReplaced:
			c.freeSymbolValue(replaced[d.Target], "old_assign")
		default:
			panic(fmt.Sprintf("plan %s: unknown drop kind %d", plan.Label, d.Kind))
		}
	}
}
