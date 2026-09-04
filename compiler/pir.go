package compiler

import (
	"fmt"
	"strings"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/pir"
)

// planLetStatement is the temporary capability router (plan §16 rule 2): it
// accepts a statement when the plan path supports its combination and
// returns the built plan, which the caller elaborates and validates before
// lowering. It accepts ordinary expressions — no gate, range, conditional,
// checked access, or call — over unmanaged and heap value kinds into local
// and discard targets. Only the script statement loop invokes it, so every
// named target is a script-root binding and discards bind nothing; an
// accepted statement has no legacy fallback.
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

// planValueTypeSupported admits the value kinds whose commit and release the
// lowerer implements: unmanaged scalars and Range descriptors, and the heap
// kinds — strings, arrays, tables — plus struct values (plan §8). Function
// values and call-only ArrayRange descriptors stay out.
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

// planExprEligible accepts only ordinary expression trees: whitelisted node
// kinds with no range or conditional behavior at any depth. A block-layout
// array literal (rank-2 rows, a table) has no one-line spelling for the eval
// operand yet, so it waits with the literal kinds the renderer refuses.
func (c *Compiler) planExprEligible(expr ast.Expression) bool {
	switch e := expr.(type) {
	case *ast.IntegerLiteral, *ast.FloatLiteral, *ast.StringLiteral, *ast.Identifier,
		*ast.InfixExpression, *ast.PrefixExpression, *ast.RangeLiteral, *ast.DotExpression:
	case *ast.ArrayLiteral:
		if e.Block || len(e.Headers) > 0 {
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

// scriptRootBindingType is the solver's declared type for a binding — an
// independent fact the plan's local target records so validation can catch a
// mismapped outcome. FuncNameMangled is the root key wherever this runs.
func (c *Compiler) scriptRootBindingType(name string) Type {
	return c.FuncCache[c.FuncNameMangled].Vars[name]
}

// effectiveBindingType is the type of the value a binding holds right now —
// the compiler's own notion of effective storage, which legacy lowering
// consults for every copy and release. It can differ from the solver's
// flow-typed read and from the declared slot type: a store keeps a heap
// string's flavor even into a binding declared static, and a binding read
// after `text = "old"` solves as static while the binding stores the
// materialized heap copy. A binding with no value yet has its declared
// type; a name that is not a binding here keeps the solver's type.
func (c *Compiler) effectiveBindingType(name string, solved Type) Type {
	sym, source := c.lookupNamedSymbol(name)
	if source == symbolMissing {
		return c.bindingSlotType(name, solved)
	}
	if ptr, isPtr := sym.Type.(Ptr); isPtr {
		return ptr.Elem
	}
	return sym.Type
}

// planSlot annotates one outcome slot (plan §8): a value whose type holds no
// heap state is unmanaged whatever produced it; a binding read borrows that
// binding's effective state; every other producer — a concatenation, an
// array literal, a heap-formatted string, a copied table column — yields a
// fresh owned value.
func (c *Compiler) planSlot(expr ast.Expression, t Type) pir.Slot {
	ident, isIdent := expr.(*ast.Identifier)
	if isIdent {
		t = c.effectiveBindingType(ident.Value, t)
	}
	if !typeNeedsCleanup(t) {
		return pir.Slot{Type: t}
	}
	if isIdent {
		return pir.Slot{Type: t, Ownership: pir.Borrowed, Owner: ident.Value}
	}
	return pir.Slot{Type: t, Ownership: pir.Owned}
}

// planLocalTarget records a script-root binding as a target: its declared
// type, whether that type materializes unmanaged values, whether the
// binding is fresh, and whether the value it holds now owns heap state —
// read from its effective storage, since a heap value transferred into a
// binding declared static is still the binding's to release.
func (c *Compiler) planLocalTarget(name string) pir.Target {
	declared := c.scriptRootBindingType(name)
	_, exists := Get(c.Scopes, name)
	holds := exists && typeNeedsCleanup(c.effectiveBindingType(name, declared))
	return pir.Target{Kind: pir.LocalTarget, Name: name, Type: declared, Owns: typeNeedsCleanup(declared), Fresh: !exists, Holds: holds}
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

// planBindingCompatible is the validator's type relation: the compiler's
// directional binding-slot compatibility (StrG into StrH, an empty-array
// reset), never display or mangle equality. Both types were recorded by the
// builder from compiler types, so the assertions cannot fail.
func planBindingCompatible(target, outcome pir.Type) bool {
	return bindingSlotCompatible(target.(Type), outcome.(Type))
}

// lowerAssignPlan implements an elaborated plan (plan §6, §13): the old
// values of replaced targets are captured first, every eval is compiled
// against the pre-commit bindings, the mappings land in order with the
// transfer the plan recorded, and the derived releases run last so no
// mapping reads a freed payload. It decides nothing itself.
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
			// The plan's ownership decisions are only sound if an unmanaged
			// slot really holds no heap state: a misclassified value would be
			// shared and then released, so builder drift dies here as an ICE.
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
		// The next statement's plan reads this binding's ownership back from
		// its effective storage, so the store must have left exactly the
		// heap state the transfer says it did.
		holds := typeNeedsCleanup(c.effectiveBindingType(m.Target.Name, m.Target.Type.(Type)))
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
