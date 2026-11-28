package pir

import (
	"fmt"
	"slices"
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
	writeNodes(&b, executeIndent, p.Execute, resolvedBySkip, expanded)

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
		writeEval(&b, executeIndent, ev, resolvedBySkip, expanded)
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

// executeIndent is the level of an execute region's own lines. A selection
// that binds an outcome prints that on a line of its own, so its labels —
// first and fallback, last and next — sit one level further in and its sides
// two; the labels of a selection that binds none sit at its own level. A
// gate's region sits one level in from the gate.
const executeIndent = "        "

// level is one step of indentation.
const level = "    "

// resolver is what resolves a failure where a node sits (plan §9), which the
// plan's shape says and no node records: the fallback that follows the node's
// side of a first, or the commit, where the outcome skips.
type resolver int

const (
	resolvedBySkip resolver = iota
	resolvedByFallback
)

// writeNodes writes a region's nodes at indent; by is what resolves a failure
// in the region.
func writeNodes(b *strings.Builder, indent string, nodes []Node, by resolver, expanded bool) {
	for _, node := range nodes {
		switch n := node.(type) {
		case *Eval:
			writeEval(b, indent, n, by, expanded)
		case *First, *Last:
			writeSelection(b, indent, selectionOf(n), by, expanded)
		case *Check:
			fmt.Fprintf(b, "%s%s\n", indent, outcomeLine(n.Result, "check", n.Slots, n.Expr, n.Checked, by, expanded))
		case *Gate:
			fmt.Fprintf(b, "%sgate %%t%d\n", indent, n.Cond)
			writeNodes(b, indent+level, n.Body, by, expanded)
		}
	}
}

// writeHead writes what every plan opens with: its header, its source, and
// the line that opens its execute region. The source prints as the lines its
// file spells the statement with, one level in, so a literal that spans lines
// reads as it is laid out, with every character a reader cannot see still
// escaped. A node's operand stays one line, line breaks escaped with it
// (plan §12).
func writeHead(b *strings.Builder, head Head) {
	fmt.Fprintf(b, "%s\n", head.Label)
	b.WriteString("    source\n")
	for _, line := range strings.Split(logicalNewlines(head.Source), "\n") {
		if line == "" {
			b.WriteString("\n")
			continue
		}
		fmt.Fprintf(b, "        %s\n", escapeControls(line))
	}
	b.WriteString("\n    execute\n")
}

func writeEval(b *strings.Builder, indent string, ev *Eval, by resolver, expanded bool) {
	fmt.Fprintf(b, "%s%s\n", indent, outcomeLine(ev.Result, "eval", ev.Slots, ev.Expr, ev.Checked, by, expanded))
}

// outcomeLine is the line that binds the outcome of an eval or a check: the
// operation's word, each slot's type, the operand, and, where the operand
// holds a checked access, what resolves a failed one.
func outcomeLine(id OutcomeID, word string, slots []Slot, expr ast.Expression, checked bool, by resolver, expanded bool) string {
	operand := renderPayload(expr)
	line := fmt.Sprintf("%%t%d = %s %s %s", id, word, slotsString(slots, operand, expanded), operand)
	if checked {
		line += fmt.Sprintf(" [on-oob=%s]", by)
	}
	return line
}

// selection is what a first and a last print alike: the outcome each binds,
// the word that names it, the word that labels each side after the first,
// whether that side resolves a failure of the one before it, the type of the
// whole selection, and its sides.
type selection struct {
	id      OutcomeID
	word    string
	clause  string
	catches bool
	t       Type
	sides   []Node
}

// selectionOf is how node, a first or a last, prints and resolves.
func selectionOf(node Node) selection {
	if l, isLast := node.(*Last); isLast {
		return selection{id: l.Result, word: "last", clause: "next", t: l.Type, sides: l.Sides}
	}
	f := node.(*First)
	return selection{id: f.Result, word: "first", clause: "fallback", catches: true, t: f.Type, sides: f.Sides}
}

// sideResolver is what resolves a failure in side i: the side after it where
// the selection's sides catch, and otherwise by, which resolves a failure of
// the selection itself.
func (sel selection) sideResolver(i int, by resolver) resolver {
	if sel.catches && i < len(sel.sides)-1 {
		return resolvedByFallback
	}
	return by
}

// writeSelection writes a first or a last: the line that binds its outcome
// where it binds one, then the head line naming the selection and its type,
// each side one level further in, and the clause word before every side after
// the first. A selection that binds no outcome prints its head where the
// outcome line would have gone, so its sides stay one level in from it. by is
// what resolves a failure of the selection.
func writeSelection(b *strings.Builder, indent string, sel selection, by resolver, expanded bool) {
	head := indent
	if sel.id != NoOutcome {
		fmt.Fprintf(b, "%s%%t%d =\n", indent, sel.id)
		head = indent + level
	}

	fmt.Fprintf(b, "%s%s %s\n", head, sel.word, sel.t.String())
	for i, side := range sel.sides {
		if i > 0 {
			fmt.Fprintf(b, "%s%s\n", head, sel.clause)
		}
		writeNodes(b, head+level, []Node{side}, sel.sideResolver(i, by), expanded)
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

// indent moves every line one level in but the empty ones, which separate a
// plan's sections: a line of spaces belongs to a multi-line source and keeps
// its own indentation.
func indent(text string) string {
	lines := strings.SplitAfter(text, "\n")
	for i, line := range lines {
		if line != "" && line != "\n" {
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

// logicalNewlines rewrites every line terminator Pluto counts as one — LF,
// CRLF and CR alike, as lexer.LogicalRune reads them — to LF, so a terminator
// reads the same however a file spells it and never collides with the `\r` an
// escape in the source asks for.
func logicalNewlines(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// escapeControls spells out what a reader would not otherwise see, so that an
// operand stays on one physical line, a source line keeps its own, and two
// statements that differ never render alike: a line terminator, a tab, and
// every rune Unicode calls unprintable, the format and separator characters
// among them.
func escapeControls(s string) string {
	var b strings.Builder
	for _, r := range logicalNewlines(s) {
		switch {
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r < 0x80:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r > 0xffff:
			fmt.Fprintf(&b, `\U%08x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
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
// a last, or one of those, a node inside one or a check, each of which has
// one slot.
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

func (by resolver) String() string {
	if by == resolvedByFallback {
		return "fallback"
	}
	return "skip"
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

// writeReleases names each release by the value it frees: what a node that is
// only tested built, what a failed eval or check built, tagged with what its
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

// failure is what the failure of the node whose outcome is id does, which its
// place says as it says what a failed access does: the nearest fallback that
// follows a side holding the node runs, and with none the failure reaches the
// commit and skips the outcome.
func (p *AssignPlan) failure(id OutcomeID) resolver {
	for _, node := range p.Values() {
		if by, found := resolverOf(node, id, resolvedBySkip); found {
			return by
		}
	}
	return resolvedBySkip
}

// resolverOf finds the node that binds id, node itself or one inside its
// sides, and returns what resolves its failure; by resolves a failure of node.
func resolverOf(node Node, id OutcomeID, by resolver) (resolver, bool) {
	if bound, binds := result(node); binds && bound == id {
		return by, true
	}
	if _, isSupplier := node.(Supplier); isSupplier {
		return by, false
	}

	sel := selectionOf(node)
	for i, side := range sel.sides {
		if sideBy, found := resolverOf(side, id, sel.sideResolver(i, by)); found {
			return sideBy, true
		}
	}
	return by, false
}

// eval finds the eval whose outcome is id among the values, the only place a
// multi-slot outcome is bound: it is nil for an outcome bound inside a first
// or a last, each of whose nodes yields one slot, and for a first, a last or
// a check, which yield one too.
func (p *AssignPlan) eval(id OutcomeID) *Eval {
	for _, node := range p.Values() {
		if ev, isEval := node.(*Eval); isEval && ev.Result == id {
			return ev
		}
	}
	return nil
}
