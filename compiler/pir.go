package compiler

import (
	"strings"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/pir"
)

// planLetStatement is the temporary capability router for assignments (plan
// §16 rule 2): it accepts a statement exactly when the plan path supports its
// capability combination, returning the built plan. The Step 3 slice is
// assignments of unmanaged values — scalars and Range descriptors — from
// ordinary expressions to local and scalar discard targets. Only the script
// statement loop invokes the router, so the script-root context is
// structural; function-body statements join when Step 4 handles output
// targets. Once a statement is accepted there is no fallback to legacy
// lowering: the router trusts solver facts (a missing root ExprInfo panics
// as an ICE), and plan validation is a test-time contract — the golden tests
// validate every plan the builder emits.
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

// planValueTypeSupported bounds Step 3 to unmanaged value kinds: scalars and
// Range descriptors (plan §8). Heap, multi-output, struct, and table values
// follow in later steps.
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

// planExprEligible walks one RHS tree and accepts only ordinary expression
// nodes with no range, conditional, or rewrite behavior anywhere — the
// capability flags that route to later steps (checked accesses, calls,
// collectors, and strings are excluded by the node whitelist).
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

// scriptRootBindingType is the solver's declared type for a script-root
// binding — the independent fact a plan's local target records so validation
// can catch a mismapped outcome. Nil when the solver declared no binding.
// The router runs only at the script root, where FuncNameMangled is the
// root key.
func (c *Compiler) scriptRootBindingType(name string) Type {
	return c.FuncCache[c.FuncNameMangled].Vars[name]
}

// buildLetPlan constructs the plan for an accepted statement: one eval per
// RHS in source order and the recorded target <- outcome commit mapping.
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

// lowerAssignPlan walks the plan in order and emits LLVM through the existing
// expression compiler: every eval runs against the pre-commit bindings, then
// the recorded mappings apply simultaneously. Step 3 outcomes are unmanaged,
// so the commit plans no copies, transfers, or releases — ownership
// elaboration lands with heap values in Step 4.
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
