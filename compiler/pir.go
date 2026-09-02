package compiler

import (
	"strings"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/pir"
)

// planLetStatement is the temporary capability router (plan §16 rule 2): it
// accepts a statement when the plan path supports its combination and
// returns the built plan, which the caller validates before lowering. Step 3
// accepts unmanaged values — scalars and Range descriptors — from ordinary
// expressions into local and discard targets; a discarded unmanaged outcome
// carries no release obligation. Only the script statement loop invokes it,
// so every named target is a script-root binding and discards bind nothing;
// an accepted statement has no legacy fallback.
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

// planValueTypeSupported bounds Step 3 to unmanaged value kinds (plan §8).
func planValueTypeSupported(t Type) bool {
	if !IsFullyResolvedType(t) {
		return false
	}
	switch t.Kind() {
	case IntKind, FloatKind, RangeKind:
		return true
	default:
		return false
	}
}

// planExprEligible accepts only ordinary expression trees: whitelisted node
// kinds with no range or conditional behavior at any depth.
func (c *Compiler) planExprEligible(expr ast.Expression) bool {
	switch expr.(type) {
	case *ast.IntegerLiteral, *ast.FloatLiteral, *ast.Identifier,
		*ast.InfixExpression, *ast.PrefixExpression, *ast.RangeLiteral:
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

func (c *Compiler) buildLetPlan(stmt *ast.LetStatement) *pir.AssignPlan {
	evals := make([]*pir.Eval, len(stmt.Value))
	for i, expr := range stmt.Value {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		evals[i] = &pir.Eval{
			Result: pir.OutcomeID(i),
			Expr:   expr,
			Types:  []pir.Type{info.OutTypes[0]},
		}
	}

	commit := make([]pir.Mapping, len(stmt.Name))
	nameParts := make([]string, len(stmt.Name))
	for i, ident := range stmt.Name {
		nameParts[i] = ident.Value
		target := pir.Target{Kind: pir.LocalTarget, Name: ident.Value, Type: c.scriptRootBindingType(ident.Value)}
		if isDiscard(ident) {
			target = pir.Target{Kind: pir.DiscardTarget}
		}
		commit[i] = pir.Mapping{Target: target, Outcome: pir.OutcomeRef{Outcome: pir.OutcomeID(i)}}
	}

	return &pir.AssignPlan{
		Name:   "assign_" + strings.Join(nameParts, "_"),
		Source: stmt.String(),
		Evals:  evals,
		Commit: commit,
	}
}

// lowerAssignPlan evaluates every eval against the pre-commit bindings, then
// applies the recorded mappings. Step 3 outcomes are unmanaged, so the
// commit plans no copies or releases (ownership elaboration is Step 4).
func (c *Compiler) lowerAssignPlan(plan *pir.AssignPlan) {
	c.pushStmtCtx()
	defer c.popStmtCtx()

	outs := make([][]*Symbol, len(plan.Evals))
	for i, ev := range plan.Evals {
		outs[i] = c.compileExpression(ev.Expr, nil)
	}
	for _, m := range plan.Commit {
		if m.Target.Kind == pir.DiscardTarget {
			// An unmanaged discarded outcome carries no release obligation.
			continue
		}
		c.storeValue(m.Target.Name, outs[m.Outcome.Outcome][m.Outcome.Slot], false)
	}
}
