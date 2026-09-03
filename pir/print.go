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

// renderPayload delimits an eval operand as exactly one parenthesized
// expression, so the type/expression boundary stays visible even when a
// type contains spaces.
func renderPayload(expr ast.Expression) string {
	switch expr.(type) {
	case *ast.InfixExpression, *ast.PrefixExpression:
		return renderExpr(expr)
	default:
		return "(" + renderExpr(expr) + ")"
	}
}

// renderExpr mirrors the ast String shapes with source bindings under the
// @ sigil. It covers exactly the node kinds the router admits; any other is
// an ICE, so widening the router means adding its renderer and golden here.
func renderExpr(expr ast.Expression) string {
	switch e := expr.(type) {
	case *ast.Identifier:
		return "@" + e.Value
	case *ast.InfixExpression:
		return "(" + renderExpr(e.Left) + " " + e.Operator + " " + renderExpr(e.Right) + ")"
	case *ast.PrefixExpression:
		return "(" + e.Operator + renderExpr(e.Right) + ")"
	case *ast.IntegerLiteral:
		return e.Token.Literal
	case *ast.FloatLiteral:
		return e.Token.Literal
	case *ast.RangeLiteral:
		s := renderExpr(e.Start) + ":" + renderExpr(e.Stop)
		if e.Step != nil {
			s += ":" + renderExpr(e.Step)
		}
		return s
	default:
		panic(fmt.Sprintf("pir: no renderer for %T", expr))
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
		return "discard"
	}
	if expanded && t.Type != nil {
		return "@" + t.Name + " : " + t.Type.String()
	}
	return "@" + t.Name
}

func (p *AssignPlan) outcomeString(ref OutcomeRef) string {
	if len(p.Evals[ref.Outcome].Types) > 1 {
		return fmt.Sprintf("%%t%d#%d", ref.Outcome, ref.Slot)
	}
	return fmt.Sprintf("%%t%d", ref.Outcome)
}
