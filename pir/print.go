package pir

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/thiremani/pluto/ast"
)

// Render returns the deterministic text form of a plan (plan §12):
// lines that bind a named result read `%result = operation Type operands`, commit
// mappings `target [: Type] <- value`. `expanded` adds result shapes,
// ownership annotations, and target types. The in-memory tree is
// authoritative — this text is never parsed back.
func (p *AssignPlan) Render(expanded bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "statement %s\n", p.Label)
	fmt.Fprintf(&b, "    source %s\n", strconv.Quote(p.Source))
	b.WriteString("\n    execute\n")
	for _, ev := range p.Evals {
		fmt.Fprintf(&b, "        %%t%d = eval %s %s", ev.Result, typesString(ev.Types), renderPayload(ev.Expr))
		if expanded {
			b.WriteString(" [shape=scalar] [yield=always] [unmanaged]")
		}
		b.WriteString("\n")
	}
	b.WriteString("\n    commit\n")
	for _, m := range p.Commit {
		fmt.Fprintf(&b, "        %s <- %s\n", targetString(m.Target, expanded), p.outcomeString(m.Outcome))
	}
	return b.String()
}

// renderPayload renders an eval operand in the ast's own spelling, minus the
// pair of parentheses an operator root wraps itself in, after checking that
// every node is one the router admits; any other is an ICE, so widening the
// router means admitting its node kind and golden here.
func renderPayload(expr ast.Expression) string {
	checkRenderable(expr)
	s := expr.String()
	switch expr.(type) {
	case *ast.InfixExpression, *ast.PrefixExpression:
		return s[1 : len(s)-1]
	}
	return s
}

func checkRenderable(expr ast.Expression) {
	switch expr.(type) {
	case *ast.Identifier, *ast.IntegerLiteral, *ast.FloatLiteral,
		*ast.InfixExpression, *ast.PrefixExpression, *ast.RangeLiteral:
	default:
		panic(fmt.Sprintf("pir: no renderer for %T", expr))
	}
	for _, child := range ast.ExprChildren(expr) {
		checkRenderable(child)
	}
}

func typesString(types []Type) string {
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = t.String()
	}
	return strings.Join(names, ", ")
}

func targetString(t Target, expanded bool) string {
	if t.Kind == DiscardTarget {
		return "_"
	}
	if expanded && t.Type != nil {
		return t.Name + " : " + t.Type.String()
	}
	return t.Name
}

func (p *AssignPlan) outcomeString(ref OutcomeRef) string {
	if len(p.Evals[ref.Outcome].Types) > 1 {
		return fmt.Sprintf("%%t%d#%d", ref.Outcome, ref.Slot)
	}
	return fmt.Sprintf("%%t%d", ref.Outcome)
}
