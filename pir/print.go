package pir

import (
	"fmt"
	"slices"
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
	writeHead(&b, p.Head)
	writeNodes(&b, executeIndent, p.Execute, expanded)

	b.WriteString("\n    commit\n")
	for _, m := range p.Commit {
		fmt.Fprintf(&b, "        %s = %s", targetString(m.Target), p.refString(m.Outcome))
		if expanded {
			b.WriteString(p.transfersString(m))
		}
		if m.OnSkip != NoSkipPolicy {
			fmt.Fprintf(&b, " [on-skip=%s]", m.OnSkip)
		}
		b.WriteString("\n")
	}
	if expanded {
		p.writeReleases(&b)
	}

	return placed(b.String(), p.Func)
}

// Render returns a print plan's text: the invocation closes the execute
// region, and the expanded view marks each owned slot it takes with [move].
func (p *PrintPlan) Render(expanded bool) string {
	var b strings.Builder
	writeHead(&b, p.Head)
	for _, ev := range p.Evals {
		writeEval(&b, executeIndent, ev, expanded)
	}
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
			part := outcomeString(p.Evals[arg.Outcome], i)
			if expanded && slot.Ownership == Owned {
				part += " [" + Move.String() + "]"
			}
			parts = append(parts, part)
		}
	}
	return parts
}

// executeIndent is the level of an execute region's own lines. The labels of
// a first, first and fallback, and of a last, last and next, sit one level
// further in and their sides two; a gate's region sits one level in from the
// gate.
const executeIndent = "        "

// level is one step of indentation.
const level = "    "

// writeNodes writes a region's nodes at indent.
func writeNodes(b *strings.Builder, indent string, nodes []Node, expanded bool) {
	for _, node := range nodes {
		switch n := node.(type) {
		case *Eval:
			writeEval(b, indent, n, expanded)
		case *First:
			writeFirst(b, indent, n, expanded)
		case *Check:
			fmt.Fprintf(b, "%s%s\n", indent, outcomeLine(n.Result, "check", n.Slots, n.Expr, n.OnOOB, expanded))
		case *Last:
			writeLast(b, indent, n, expanded)
		case *Gate:
			fmt.Fprintf(b, "%sgate %%t%d\n", indent, n.Cond)
			writeNodes(b, indent+level, n.Body, expanded)
		}
	}
}

// writeHead writes what every plan opens with: its header, its source, and
// the line that opens its execute region.
func writeHead(b *strings.Builder, head Head) {
	fmt.Fprintf(b, "%s\n", head.Label)
	fmt.Fprintf(b, "    source %s\n", strconv.Quote(head.Source))
	b.WriteString("\n    execute\n")
}

func writeEval(b *strings.Builder, indent string, ev *Eval, expanded bool) {
	fmt.Fprintf(b, "%s%s\n", indent, outcomeLine(ev.Result, "eval", ev.Slots, ev.Expr, ev.OnOOB, expanded))
}

// outcomeLine is the line that binds the outcome of an eval or a check: the
// operation's word, each slot's type, the operand, and the scope of the
// operand's checked accesses where it has any.
func outcomeLine(id OutcomeID, word string, slots []Slot, expr ast.Expression, scope OOBScope, expanded bool) string {
	operand := renderPayload(expr)
	line := fmt.Sprintf("%%t%d = %s %s %s", id, word, slotsString(slots, operand, expanded), operand)
	if scope != NoChecks {
		line += fmt.Sprintf(" [on-oob=%s]", scope)
	}
	return line
}

func writeFirst(b *strings.Builder, indent string, f *First, expanded bool) {
	fmt.Fprintf(b, "%s%%t%d =\n", indent, f.Result)
	fmt.Fprintf(b, "%s%sfirst %s\n", indent, level, f.Type.String())
	for i, side := range f.Sides {
		if i > 0 {
			fmt.Fprintf(b, "%s%sfallback\n", indent, level)
		}
		writeEval(b, indent+level+level, side, expanded)
	}
}

func writeLast(b *strings.Builder, indent string, l *Last, expanded bool) {
	fmt.Fprintf(b, "%s%%t%d =\n", indent, l.Result)
	fmt.Fprintf(b, "%s%slast %s\n", indent, level, l.Type.String())
	for i, side := range l.Sides {
		if i > 0 {
			fmt.Fprintf(b, "%s%snext\n", indent, level)
		}
		writeNodes(b, indent+level+level, []Node{side}, expanded)
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
		*ast.CallExpression, *ast.ArrayLiteral, *ast.ArrayRangeExpression:
	default:
		panic(fmt.Sprintf("pir: no renderer for %T", expr))
	}
	for _, child := range ast.ExprChildren(expr) {
		checkRenderable(child)
	}
}

// slotsString lists each slot's type. The expanded view puts a slot's
// ownership before its type, so the word sits beside the slot it describes;
// an unmanaged slot carries no obligation and prints its type alone.
func slotsString(slots []Slot, operand string, expanded bool) string {
	parts := make([]string, len(slots))
	for i, slot := range slots {
		parts[i] = slot.Type.String()
		if expanded {
			parts[i] = ownershipWord(slot, operand) + parts[i]
		}
	}
	return strings.Join(parts, ", ")
}

// ownershipWord spells a slot's ownership, a fact about the slot and no part
// of its type. A borrow names its owner unless the operand already is that
// name.
func ownershipWord(slot Slot, operand string) string {
	switch slot.Ownership {
	case Owned:
		return "owned "
	case Borrowed:
		if operand == slot.Owner {
			return "borrowed "
		}
		return "borrowed(" + slot.Owner + ") "
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
// local, and the name after `output` for a function output.
func targetString(t Target) string {
	switch t.Kind {
	case DiscardTarget:
		return "_"
	case OutputTarget:
		return "output " + t.Name
	}
	return t.Name
}

// outcomeString names slot i of ev's outcome; a slot selector follows only
// where the outcome has several.
func outcomeString(ev *Eval, i int) string {
	if len(ev.Slots) > 1 {
		return fmt.Sprintf("%%t%d#%d", ev.Result, i)
	}
	return fmt.Sprintf("%%t%d", ev.Result)
}

// refString names what ref refers to: a slot of an eval outside a first or
// a last, or one of those, a side of one or a check, each of which has one
// slot.
func (p *AssignPlan) refString(ref OutcomeRef) string {
	if ev := p.eval(ref.Outcome); ev != nil {
		return outcomeString(ev, ref.Slot)
	}
	return fmt.Sprintf("%%t%d", ref.Outcome)
}

// transfersString spells how a mapping's target acquires its value: one word
// where every eval that can supply it agrees, nothing for a plain store, and
// otherwise a word for each, beside the eval it applies to.
func (p *AssignPlan) transfersString(m Mapping) string {
	if len(slices.Compact(slices.Clone(m.Transfers))) == 1 {
		if m.Transfers[0] == Store {
			return ""
		}
		return fmt.Sprintf(" [%s]", m.Transfers[0])
	}

	var b strings.Builder
	for i, src := range p.sources(m.Outcome) {
		fmt.Fprintf(&b, " [%s %s]", m.Transfers[i], p.refString(src.ref))
	}
	return b.String()
}

func (scope OOBScope) String() string {
	switch scope {
	case NoChecks:
		return "none"
	case SkipOutcome:
		return "skip"
	case UseFallback:
		return "fallback"
	}
	return fmt.Sprintf("oob-scope(%d)", int(scope))
}

func (policy SkipPolicy) String() string {
	switch policy {
	case NoSkipPolicy:
		return "none"
	case KeepOnSkip:
		return "keep"
	case ZeroOnSkip:
		return "zero"
	}
	return fmt.Sprintf("skip-policy(%d)", int(policy))
}

// writeReleases names each release by the value it frees: what a check built
// and nothing takes, what a failed eval or check built, tagged with what its
// failure does, an outcome mapped to `_`, or a target's old value.
func (p *AssignPlan) writeReleases(b *strings.Builder) {
	for _, ref := range p.Tested {
		fmt.Fprintf(b, "        drop %s\n", p.refString(ref))
	}
	for _, ref := range p.Skipped {
		fmt.Fprintf(b, "        drop %s [on-%s]\n", p.refString(ref), p.failure(ref.Outcome))
	}
	for _, ref := range p.Discarded {
		fmt.Fprintf(b, "        drop %s\n", p.refString(ref))
	}
	for _, name := range p.Replaced {
		fmt.Fprintf(b, "        drop %s [old]\n", name)
	}
}

// failure is what the failure of the eval or check whose outcome is id does:
// an eval's accesses name it, and a failed check skips its outcome.
func (p *AssignPlan) failure(id OutcomeID) OOBScope {
	if ev := p.eval(id); ev != nil {
		return ev.OnOOB
	}
	return SkipOutcome
}

// eval finds the eval whose outcome is id, a side of a first or a last
// included; it is nil when id names a first, a last or a check.
func (p *AssignPlan) eval(id OutcomeID) *Eval {
	for _, node := range p.Values() {
		switch n := node.(type) {
		case *Eval:
			if n.Result == id {
				return n
			}
		case *First:
			for _, side := range n.Sides {
				if side.Result == id {
					return side
				}
			}
		case *Last:
			if side, isEval := n.final().(*Eval); isEval && side.Result == id {
				return side
			}
		}
	}
	return nil
}
