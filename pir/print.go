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
// ownership annotations, target types, each mapping's derived transfer, and
// the derived releases after the mappings. The in-memory tree is
// authoritative — this text is never parsed back.
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

// renderPayload renders an eval operand in the ast's own spelling, minus the
// pair of parentheses an operator root wraps itself in, after checking that
// every node is one the router admits; any other is an ICE, so widening the
// router means admitting its node kind and golden here. Block-layout array
// literals print on several lines and have no one-line spelling yet, so
// they stay outside the router and the renderer alike.
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

// ownershipString renders one annotation per slot, in slot order.
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
