package pir

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/thiremani/pluto/ast"
)

// Render returns the deterministic text form of a plan (plan §12); the
// in-memory tree is authoritative and this text is never parsed back. A
// function body's plan sits one level in, under the header FuncHeader gives
// its body.
func (p *AssignPlan) Render(expanded bool) string {
	var b strings.Builder
	writeExecute(&b, p.Head, p.Evals, expanded)
	b.WriteString("\n    commit\n")
	for _, m := range p.Commit {
		fmt.Fprintf(&b, "        %s <- %s", targetString(m.Target, expanded), outcomeString(p.Evals, m.Outcome))
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

	return placed(b.String(), p.Func)
}

// Render returns a print plan's text: the invocation closes the execute
// region, and the expanded view marks each owned slot it takes with [move].
func (p *PrintPlan) Render(expanded bool) string {
	var b strings.Builder
	writeExecute(&b, p.Head, p.Evals, expanded)
	fmt.Fprintf(&b, "        print %s\n", strings.Join(p.argStrings(expanded), ", "))

	return placed(b.String(), p.Func)
}

func (p *PrintPlan) argStrings(expanded bool) []string {
	var parts []string
	for _, arg := range p.Args {
		if arg.Literal != nil {
			parts = append(parts, renderPayload(arg.Literal))
			continue
		}
		for i, slot := range p.Evals[arg.Outcome].Slots {
			part := outcomeString(p.Evals, OutcomeRef{Outcome: arg.Outcome, Slot: i})
			if expanded && slot.Ownership == Owned {
				part += " [" + Move.String() + "]"
			}
			parts = append(parts, part)
		}
	}
	return parts
}

// writeExecute writes what every plan opens with: its header, its source and
// the evals of its execute region.
func writeExecute(b *strings.Builder, head Head, evals []*Eval, expanded bool) {
	fmt.Fprintf(b, "%s\n", head.Label)
	fmt.Fprintf(b, "    source %s\n", strconv.Quote(head.Source))
	b.WriteString("\n    execute\n")
	for _, ev := range evals {
		fmt.Fprintf(b, "        %%t%d = eval %s %s\n", ev.Result, slotsString(ev.Slots, expanded), renderPayload(ev.Expr))
	}
}

// placed returns a plan's text at its level: one level in for a plan of the
// function body fn names, unchanged at the script root.
func placed(text, fn string) string {
	if fn == "" {
		return text
	}
	return indent(text)
}

// FuncHeader is the line that opens a function body's plans; fn is the
// specialization's display signature.
func FuncHeader(fn string) string {
	return "function " + fn + "\n"
}

// indent moves every non-blank line one level in.
func indent(text string) string {
	lines := strings.SplitAfter(text, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			lines[i] = "    " + line
		}
	}
	return strings.Join(lines, "")
}

// renderPayload renders an eval operand on one line: the ast's own spelling
// minus the pair an operator root wraps itself in, with control characters
// escaped, both those inside a string literal and the line breaks of a
// block-layout literal. Any node the router does not admit is an ICE, so
// widening the router means admitting its kind and golden here.
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
	switch expr.(type) {
	case *ast.Identifier, *ast.IntegerLiteral, *ast.FloatLiteral, *ast.StringLiteral,
		*ast.InfixExpression, *ast.PrefixExpression, *ast.RangeLiteral, *ast.DotExpression,
		*ast.CallExpression, *ast.ArrayLiteral:
	default:
		panic(fmt.Sprintf("pir: no renderer for %T", expr))
	}
	for _, child := range ast.ExprChildren(expr) {
		checkRenderable(child)
	}
}

// slotsString lists each slot's type. The expanded view follows a type with
// its slot's ownership, so a tag sits beside the slot it describes; an
// unmanaged slot carries no obligation and prints its type alone.
func slotsString(slots []Slot, expanded bool) string {
	parts := make([]string, len(slots))
	for i, slot := range slots {
		parts[i] = slot.Type.String()
		if expanded {
			parts[i] += ownershipTag(slot)
		}
	}
	return strings.Join(parts, ", ")
}

func ownershipTag(slot Slot) string {
	switch slot.Ownership {
	case Owned:
		return " [owned]"
	case Borrowed:
		return " [borrowed=" + slot.Owner + "]"
	}
	return ""
}

func (t Transfer) String() string {
	switch t {
	case Store:
		return "store"
	case Move:
		return "move"
	case Copy:
		return "copy"
	}
	return fmt.Sprintf("transfer(%d)", int(t))
}

// targetString spells a mapping's target: `_` for a discard, the name for a
// local, and the name after `output` for a function output; the expanded
// view puts the target's type before the name.
func targetString(t Target, expanded bool) string {
	if t.Kind == DiscardTarget {
		return "_"
	}
	name := t.Name
	if expanded && t.Type != nil {
		name = t.Type.String() + " " + name
	}
	if t.Kind == OutputTarget {
		return "output " + name
	}
	return name
}

func outcomeString(evals []*Eval, ref OutcomeRef) string {
	if len(evals[ref.Outcome].Slots) > 1 {
		return fmt.Sprintf("%%t%d#%d", ref.Outcome, ref.Slot)
	}
	return fmt.Sprintf("%%t%d", ref.Outcome)
}

func (p *AssignPlan) dropString(d Drop) string {
	if d.Kind == DropReplaced {
		return "drop " + d.Target + " [old]"
	}
	return "drop " + outcomeString(p.Evals, d.Outcome)
}
