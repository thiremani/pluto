package pir

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/thiremani/pluto/ast"
)

// Render returns the deterministic text form of a plan (plan §12); the
// in-memory tree is authoritative and this text is never parsed back.
func (p *AssignPlan) Render(expanded bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "statement %s\n", p.Label)
	fmt.Fprintf(&b, "    source %s\n", strconv.Quote(p.Source))
	b.WriteString("\n    execute\n")
	for _, ev := range p.Evals {
		fmt.Fprintf(&b, "        %%t%d = eval %s %s", ev.Result, typesString(ev.Slots), renderPayload(ev.Expr))
		if expanded {
			b.WriteString(" [shape=scalar] [yield=always]" + ownershipString(ev.Slots))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n    commit\n")
	for _, m := range p.Commit {
		fmt.Fprintf(&b, "        %s <- %s", targetString(m.Target, expanded), p.outcomeString(m.Outcome))
		if expanded && m.Transfer != Store {
			fmt.Fprintf(&b, " [%s]", m.Transfer)
		}
		b.WriteString("\n")
	}
	if expanded {
		for _, d := range p.Drops {
			fmt.Fprintf(&b, "        %s\n", p.dropString(d))
		}
	}
	return b.String()
}

// renderPayload renders an eval operand on one line: the ast's own spelling
// minus the pair an operator root wraps itself in, with control characters
// inside string literals escaped. Any node the router does not admit is an
// ICE, so widening the router means admitting its kind and golden here.
func renderPayload(expr ast.Expression) string {
	checkRenderable(expr)
	s := expr.String()
	switch expr.(type) {
	case *ast.InfixExpression, *ast.PrefixExpression:
		s = s[1 : len(s)-1]
	}
	return escapeControls(s)
}

// escapeControls keeps an operand on one physical line: C0 and C1 controls
// and the Unicode line and paragraph separators are escaped.
func escapeControls(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x80 && unicode.IsControl(r):
			fmt.Fprintf(&b, `\x%02x`, r)
		case unicode.IsControl(r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func checkRenderable(expr ast.Expression) {
	switch e := expr.(type) {
	case *ast.Identifier, *ast.IntegerLiteral, *ast.FloatLiteral, *ast.StringLiteral,
		*ast.InfixExpression, *ast.PrefixExpression, *ast.RangeLiteral, *ast.DotExpression:
	case *ast.ArrayLiteral:
		if e.Block || len(e.Headers) > 0 {
			panic("pir: no one-line renderer for a block-layout array literal")
		}
	default:
		panic(fmt.Sprintf("pir: no renderer for %T", expr))
	}
	for _, child := range ast.ExprChildren(expr) {
		checkRenderable(child)
	}
}

func typesString(slots []Slot) string {
	names := make([]string, len(slots))
	for i, slot := range slots {
		names[i] = slot.Type.String()
	}
	return strings.Join(names, ", ")
}

func ownershipString(slots []Slot) string {
	var b strings.Builder
	for _, slot := range slots {
		switch slot.Ownership {
		case Owned:
			b.WriteString(" [owned]")
		case Borrowed:
			b.WriteString(" [borrowed=" + slot.Owner + "]")
		default:
			b.WriteString(" [unmanaged]")
		}
	}
	return b.String()
}

func (t Transfer) String() string {
	switch t {
	case Store:
		return "store"
	case Materialize:
		return "materialize"
	case Move:
		return "move"
	case Copy:
		return "copy"
	case Promote:
		return "transfer"
	}
	return fmt.Sprintf("transfer(%d)", int(t))
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
	if len(p.Evals[ref.Outcome].Slots) > 1 {
		return fmt.Sprintf("%%t%d#%d", ref.Outcome, ref.Slot)
	}
	return fmt.Sprintf("%%t%d", ref.Outcome)
}

func (p *AssignPlan) dropString(d Drop) string {
	if d.Kind == DropReplaced {
		return "drop " + d.Target + " [replaced]"
	}
	return "drop " + p.outcomeString(d.Outcome)
}
