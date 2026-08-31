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
// expressions into local and scalar discard targets. Only the script
// statement loop invokes it; an accepted statement has no legacy fallback.
func (c *Compiler) planLetStatement(stmt *ast.LetStatement) (*pir.AssignPlan, bool) {
	if len(stmt.Condition) > 0 || len(stmt.Name) != len(stmt.Value) {
		return nil, false
	}
	// The arity check above already forces every RHS to a single output slot:
	// each expression yields at least one, and the solver validated the total.
	for i, expr := range stmt.Value {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		if !planValueTypeSupported(info.OutTypes[0]) {
			return nil, false
		}
		if !c.planExprEligible(expr) {
			return nil, false
		}
		if !c.planTargetEligible(stmt.Name[i], info.OutTypes[0]) {
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
// kinds with no range, conditional, or rewrite behavior at any depth.
func (c *Compiler) planExprEligible(expr ast.Expression) bool {
	switch expr.(type) {
	case *ast.IntegerLiteral, *ast.FloatLiteral, *ast.Identifier,
		*ast.InfixExpression, *ast.PrefixExpression, *ast.RangeLiteral:
	default:
		return false
	}
	if info := c.ExprCache[key(c.FuncNameMangled, expr)]; info != nil {
		if info.HasRanges || len(info.Ranges) > 0 || len(info.CollectRanges) > 0 {
			return false
		}
		// The solver stores the node itself as its Rewrite when nothing
		// changed; only a replacement node signals range scalarization.
		if info.Rewrite != nil && info.Rewrite != expr {
			return false
		}
		for _, m := range info.CompareModes {
			if m != CondNone {
				return false
			}
		}
	}
	for _, child := range ast.ExprChildren(expr) {
		if !c.planExprEligible(child) {
			return false
		}
	}
	return true
}

// planTargetEligible accepts local bindings and scalar discards. A symbol
// bound from function argument context routes to the later call/output steps.
func (c *Compiler) planTargetEligible(ident *ast.Identifier, outType Type) bool {
	if isDiscard(ident) {
		k := outType.Kind()
		return k == IntKind || k == FloatKind
	}
	if c.scriptRootBindingType(ident.Value) == nil {
		return false
	}
	sym, exists := Get(c.Scopes, ident.Value)
	if !exists {
		return true
	}
	return !sym.FuncArg && !sym.ReadOnly
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
