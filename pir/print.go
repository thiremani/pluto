package pir

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/thiremani/pluto/ast"
)

// Render returns the deterministic text form of a plan (plan §12): four-space
// indentation, no tabs or braces, %name for plan outcomes, @name for semantic
// targets. The concise view is the semantic plan; the expanded view adds
// result shapes and ownership annotations. The in-memory tree stays
// authoritative — this text is a rendering, never parsed back.
func (p *AssignPlan) Render(expanded bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "pir.statement @%s\n", p.Name)
	fmt.Fprintf(&b, "    source %s\n", strconv.Quote(p.Source))
	b.WriteString("\n    execute\n")
	for _, ev := range p.Evals {
		fmt.Fprintf(&b, "        %%t%d = eval %s : %s", ev.Result, renderExpr(ev.Expr), typesString(ev.Types))
		if expanded {
			b.WriteString(" [shape=scalar] [yield=always] [unmanaged]")
		}
		b.WriteString("\n")
	}
	b.WriteString("\n    commit simultaneous\n")
	for _, m := range p.Commit {
		fmt.Fprintf(&b, "        %s <- %s\n", targetString(m.Target, expanded), p.outcomeString(m.Outcome))
	}
	return b.String()
}

// renderExpr renders an eval operand with source bindings under the @ sigil,
// mirroring the ast String shapes otherwise. Node kinds outside the current
// plan capability fall back to the source rendering.
func renderExpr(expr ast.Expression) string {
	switch e := expr.(type) {
	case *ast.Identifier:
		return "@" + e.Value
	case *ast.InfixExpression:
		return "(" + renderExpr(e.Left) + " " + e.Operator + " " + renderExpr(e.Right) + ")"
	case *ast.PrefixExpression:
		return "(" + e.Operator + renderExpr(e.Right) + ")"
	case *ast.RangeLiteral:
		s := renderExpr(e.Start) + ":" + renderExpr(e.Stop)
		if e.Step != nil {
			s += ":" + renderExpr(e.Step)
		}
		return s
	default:
		return expr.String()
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

// outcomeString renders a slot reference; the slot suffix appears only for a
// multi-slot eval, so single-outcome plans stay free of index noise.
func (p *AssignPlan) outcomeString(ref OutcomeRef) string {
	if len(p.Evals[ref.Outcome].Types) > 1 {
		return fmt.Sprintf("%%t%d.%d", ref.Outcome, ref.Slot)
	}
	return fmt.Sprintf("%%t%d", ref.Outcome)
}
