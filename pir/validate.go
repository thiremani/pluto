package pir

import (
	"fmt"

	"github.com/thiremani/pluto/ast"
)

// Validate runs on every elaborated plan before lowering (plan §14). Several
// invariants have no natural panic site — an unmapped outcome silently never
// commits, a missing release leaks — so a failure is an ICE. compatible is
// the compiler's directional binding-compatibility relation (target,
// outcome), never display or mangle equality.
func Validate(p *AssignPlan, compatible func(target, outcome Type) bool) error {
	if err := validateHead(p.Head); err != nil {
		return err
	}
	if len(p.Execute) == 0 {
		return fmt.Errorf("plan %s has no evals", p.Label)
	}

	next, slots := 0, 0
	for _, node := range p.Execute {
		switch n := node.(type) {
		case *Eval:
			if err := validateEval(p.Label, next, n); err != nil {
				return err
			}
			if n.OnOOB == UseFallback {
				return fmt.Errorf("plan %s: eval %%t%d falls back, but is no side of a first", p.Label, next)
			}
			next++
			slots += len(n.Slots)
		case *First:
			if err := validateFirst(p.Label, next, n, compatible); err != nil {
				return err
			}
			next += 1 + len(n.Sides)
			slots++
		default:
			return fmt.Errorf("plan %s: unknown execute node %T", p.Label, node)
		}
	}

	if len(p.Commit) != slots {
		return fmt.Errorf("plan %s: commit has %d mappings for %d outcome slots", p.Label, len(p.Commit), slots)
	}

	consumed := make(map[OutcomeRef]bool, slots)
	replaced := make(map[string]bool, slots)
	for _, m := range p.Commit {
		if err := p.validateMapping(m, compatible); err != nil {
			return err
		}
		if consumed[m.Outcome] {
			return fmt.Errorf("plan %s: outcome %%t%d slot %d consumed twice", p.Label, m.Outcome.Outcome, m.Outcome.Slot)
		}
		consumed[m.Outcome] = true
		if m.Target.binds() && m.Target.HoldsHeap {
			replaced[m.Target.Name] = true
		}
	}

	return p.validateReleases(replaced)
}

// validateFirst checks a first whose outcome is id: at least two sides of one
// slot each, numbered after it, that fit its type. A side with a side after
// it falls back where it fails, so it must be able to fail, and the last has
// nothing to fall back to.
func validateFirst(plan string, id int, f *First, compatible func(target, outcome Type) bool) error {
	if f.Result != OutcomeID(id) {
		return fmt.Errorf("plan %s: first has result ID %d where %d is next; IDs must be dense in execution order", plan, f.Result, id)
	}
	if f.Type == nil {
		return fmt.Errorf("plan %s: first %%t%d has no type", plan, id)
	}
	if len(f.Sides) < 2 {
		return fmt.Errorf("plan %s: first %%t%d has %d sides and needs a fallback", plan, id, len(f.Sides))
	}

	last := len(f.Sides) - 1
	for i, side := range f.Sides {
		sideID := id + 1 + i
		if err := validateEval(plan, sideID, side); err != nil {
			return err
		}
		if len(side.Slots) != 1 {
			return fmt.Errorf("plan %s: side %%t%d of first %%t%d has %d slots", plan, sideID, id, len(side.Slots))
		}
		if !compatible(f.Type, side.Slots[0].Type) {
			return fmt.Errorf("plan %s: side %%t%d of type %s does not fit first %%t%d of type %s", plan, sideID, side.Slots[0].Type.String(), id, f.Type.String())
		}
		if i < last && side.OnOOB != UseFallback {
			return fmt.Errorf("plan %s: side %%t%d of first %%t%d has out-of-bounds scope %s; the fallback after it needs fallback", plan, sideID, id, side.OnOOB)
		}
		if i == last && side.OnOOB == UseFallback {
			return fmt.Errorf("plan %s: the last side %%t%d of first %%t%d falls back to nothing", plan, sideID, id)
		}
	}

	return nil
}

// ValidatePrint runs on every print plan before lowering (plan §14.1,
// §14.12): the invocation takes the outcomes in evaluation order, each exactly
// once, so the line reads as its evals run and none goes unprinted or prints
// twice. A print of literals alone has no evals.
func ValidatePrint(p *PrintPlan) error {
	if err := validateHead(p.Head); err != nil {
		return err
	}
	if len(p.Args) == 0 {
		return fmt.Errorf("plan %s has no arguments", p.Label)
	}
	for i, ev := range p.Evals {
		if err := validateEval(p.Label, i, ev); err != nil {
			return err
		}
		if ev.MayFail() {
			return fmt.Errorf("plan %s: print takes %%t%d, which may fail, and a print resolves no failed access yet", p.Label, i)
		}
	}

	next := OutcomeID(0)
	for _, arg := range p.Args {
		if arg.Literal != nil {
			continue
		}
		if arg.Outcome != next {
			return fmt.Errorf("plan %s: print takes %%t%d where %%t%d is next in evaluation order", p.Label, arg.Outcome, next)
		}
		next++
	}
	if int(next) != len(p.Evals) {
		return fmt.Errorf("plan %s: print takes %d of %d outcomes", p.Label, next, len(p.Evals))
	}

	return nil
}

func validateHead(h Head) error {
	if h.Label == "" {
		return fmt.Errorf("plan has no label")
	}
	if h.Source == "" {
		return fmt.Errorf("plan %s has no source rendering", h.Label)
	}
	return nil
}

func validateEval(plan string, i int, ev *Eval) error {
	if ev.Result != OutcomeID(i) {
		return fmt.Errorf("plan %s: eval %d has result ID %d; IDs must be dense in execution order", plan, i, ev.Result)
	}
	if len(ev.Slots) == 0 {
		return fmt.Errorf("plan %s: eval %%t%d has no output slots", plan, i)
	}
	for s, slot := range ev.Slots {
		if slot.Type == nil {
			return fmt.Errorf("plan %s: eval %%t%d slot %d has no type", plan, i, s)
		}
		switch slot.Ownership {
		case Unmanaged, Owned:
			if slot.Owner != "" {
				return fmt.Errorf("plan %s: eval %%t%d slot %d names owner %s but is not borrowed", plan, i, s, slot.Owner)
			}
		case Borrowed:
			if slot.Owner == "" {
				return fmt.Errorf("plan %s: eval %%t%d slot %d is borrowed from no owner", plan, i, s)
			}
		default:
			return fmt.Errorf("plan %s: eval %%t%d slot %d has unknown ownership %d", plan, i, s, slot.Ownership)
		}
	}

	return validateAccesses(plan, i, ev)
}

// validateAccesses checks plan §14.15: every checked access has an explicit
// out-of-bounds scope, which its eval records. A literal cell resolves a
// failed access itself, a scope no plan node records.
func validateAccesses(plan string, i int, ev *Eval) error {
	switch ev.OnOOB {
	case NoChecks, SkipOutcome, UseFallback:
	default:
		return fmt.Errorf("plan %s: eval %%t%d has unknown out-of-bounds scope %d", plan, i, ev.OnOOB)
	}

	outcome, cell := checkedAccesses(ev.Expr, false)
	if cell > 0 {
		return fmt.Errorf("plan %s: eval %%t%d holds a checked access in a literal cell, a scope no plan node records", plan, i)
	}
	if outcome > 0 && !ev.MayFail() {
		return fmt.Errorf("plan %s: eval %%t%d holds a checked access with no out-of-bounds scope", plan, i)
	}
	if outcome == 0 && ev.MayFail() {
		return fmt.Errorf("plan %s: eval %%t%d has an out-of-bounds scope and no checked access", plan, i)
	}

	return nil
}

// checkedAccesses counts expr's checked accesses by the region that resolves
// a failed one (plan §9): the eval's outcome, or a literal cell around the
// access.
func checkedAccesses(expr ast.Expression, inCell bool) (outcome, cell int) {
	switch expr.(type) {
	case *ast.ArrayRangeExpression:
		if inCell {
			cell++
		} else {
			outcome++
		}
	case *ast.ArrayLiteral:
		inCell = true
	}

	for _, child := range ast.ExprChildren(expr) {
		childOutcome, childCell := checkedAccesses(child, inCell)
		outcome += childOutcome
		cell += childCell
	}
	return outcome, cell
}

func (p *AssignPlan) validateMapping(m Mapping, compatible func(target, outcome Type) bool) error {
	outcome, err := p.outcomeType(m.Outcome)
	if err != nil {
		return err
	}
	sources := p.sources(m.Outcome)
	if len(m.Transfers) != len(sources) {
		return fmt.Errorf("plan %s: target %s = %%t%d slot %d has %d transfers for the %d evals that can supply it",
			p.Label, targetString(m.Target), m.Outcome.Outcome, m.Outcome.Slot, len(m.Transfers), len(sources))
	}
	if err := p.validateSkipPolicy(m); err != nil {
		return err
	}
	switch m.Target.Kind {
	case LocalTarget:
		return p.validateBinding(m, outcome, sources, compatible)
	case OutputTarget:
		if p.Func == "" {
			return fmt.Errorf("plan %s: output target %s outside a function body", p.Label, m.Target.Name)
		}
		if m.Target.Fresh {
			return fmt.Errorf("plan %s: output target %s is fresh", p.Label, m.Target.Name)
		}
		return p.validateBinding(m, outcome, sources, compatible)
	case DiscardTarget:
		if m.Target.Name != "" || m.Target.Type != nil || m.Target.TypeOwnsHeap || m.Target.Fresh || m.Target.HoldsHeap {
			return fmt.Errorf("plan %s: discard target carries a name, type, or binding state", p.Label)
		}
		for _, transfer := range m.Transfers {
			if transfer != Store {
				return fmt.Errorf("plan %s: discard of %%t%d slot %d carries a transfer", p.Label, m.Outcome.Outcome, m.Outcome.Slot)
			}
		}
	default:
		return fmt.Errorf("plan %s: unknown target kind %d", p.Label, m.Target.Kind)
	}

	return nil
}

// validateSkipPolicy checks plan §9: a binding whose outcome may skip keeps
// its value, or holds zero when it is fresh, and no other mapping carries a
// policy.
func (p *AssignPlan) validateSkipPolicy(m Mapping) error {
	want := NoSkipPolicy
	if m.Target.binds() && p.maySkip(m) {
		want = KeepOnSkip
		if m.Target.Fresh {
			want = ZeroOnSkip
		}
	}
	if m.OnSkip != want {
		return fmt.Errorf("plan %s: target %s = %%t%d slot %d has skip policy %s; its outcome and target require %s",
			p.Label, targetString(m.Target), m.Outcome.Outcome, m.Outcome.Slot, m.OnSkip, want)
	}

	return nil
}

// validateBinding checks a local or output mapping: a named target of a
// compatible type that takes its outcome, from each eval that can supply it,
// by the derived transfer.
func (p *AssignPlan) validateBinding(m Mapping, outcome Type, sources []source, compatible func(target, outcome Type) bool) error {
	if m.Target.Name == "" {
		return fmt.Errorf("plan %s: binding target has no name", p.Label)
	}
	if m.Target.HoldsHeap && m.Target.Fresh {
		return fmt.Errorf("plan %s: fresh target %s holds a value", p.Label, m.Target.Name)
	}
	if !compatible(m.Target.Type, outcome) {
		return fmt.Errorf("plan %s: target %s %s mapped to incompatible outcome %%t%d slot %d of type %s",
			p.Label, m.Target.Type.String(), m.Target.Name, m.Outcome.Outcome, m.Outcome.Slot, outcome.String())
	}

	for i, src := range sources {
		if err := p.validateSourceTransfer(m, src, m.Transfers[i], len(sources) == 1); err != nil {
			return err
		}
	}
	return nil
}

// validateSourceTransfer pins the transfer Elaborate must have derived for
// one eval that can supply m's value (plan §8, §14.20-22); alone says it is
// the only one. A heap transfer into a target declared non-owning is legal:
// the binding then holds heap state the next statement reads back.
func (p *AssignPlan) validateSourceTransfer(m Mapping, src source, transfer Transfer, alone bool) error {
	slot := src.slot()
	want := Store
	switch slot.Ownership {
	case Owned:
		want = Move
	case Borrowed:
		if transfer == Move {
			if !alone {
				return fmt.Errorf("plan %s: target %s takes %s's old value from %%t%d, one side of a first, which supplies it on one path only", p.Label, m.Target.Name, slot.Owner, src.ref.Outcome)
			}
			replacement, replaced := p.replacement(slot.Owner)
			if !replaced {
				return fmt.Errorf("plan %s: target %s takes %s's old value, but %s is not replaced in this group", p.Label, m.Target.Name, slot.Owner, slot.Owner)
			}
			if p.maySkip(m) || p.maySkip(replacement) {
				return fmt.Errorf("plan %s: target %s takes %s's old value, but one of the two mappings may skip without the other", p.Label, m.Target.Name, slot.Owner)
			}
			return nil
		}
		want = Copy
	default:
		if m.Target.TypeOwnsHeap {
			want = Copy
		}
	}
	if transfer != want {
		return fmt.Errorf("plan %s: target %s = %%t%d slot %d uses transfer %s; ownership requires %s",
			p.Label, m.Target.Name, src.ref.Outcome, src.ref.Slot, transfer, want)
	}

	return nil
}

// replacement returns the mapping that replaces the value name holds.
func (p *AssignPlan) replacement(name string) (Mapping, bool) {
	for _, m := range p.Commit {
		if m.Target.binds() && m.Target.Name == name && m.Target.HoldsHeap {
			return m, true
		}
	}
	return Mapping{}, false
}

// validateReleases checks plan §14.19-21: every owned outcome and every
// replaced held value is consumed or released exactly once, on each path. An
// owned outcome a binding takes where it yields is released where it fails.
func (p *AssignPlan) validateReleases(replaced map[string]bool) error {
	taken := make(map[string]int, len(replaced))
	needDrop := make(map[OutcomeRef]bool)
	needSkipDrop := make(map[OutcomeRef]bool)
	for _, m := range p.Commit {
		for i, src := range p.sources(m.Outcome) {
			slot := src.slot()
			// Only a moved borrow takes its owner's old value; moving an owned
			// outcome does not itself take the target's old value.
			if slot.Ownership == Borrowed && m.Transfers[i] == Move {
				taken[slot.Owner]++
			}
			if slot.Ownership != Owned {
				continue
			}
			if m.Target.Kind == DiscardTarget {
				needDrop[src.ref] = true
			} else if src.eval.MayFail() {
				needSkipDrop[src.ref] = true
			}
		}
	}
	for owner, n := range taken {
		if n > 1 {
			return fmt.Errorf("plan %s: %s's old value is taken by %d targets", p.Label, owner, n)
		}
	}

	droppedOutcome := make(map[OutcomeRef]bool)
	droppedSkipped := make(map[OutcomeRef]bool)
	droppedTarget := make(map[string]bool)
	for _, d := range p.Drops {
		switch d.Kind {
		case DropSkipped:
			if !needSkipDrop[d.Outcome] {
				return fmt.Errorf("plan %s: skip-path drop of %%t%d slot %d, which is not an owned outcome a binding takes only where it yields", p.Label, d.Outcome.Outcome, d.Outcome.Slot)
			}
			if droppedSkipped[d.Outcome] {
				return fmt.Errorf("plan %s: skipped %%t%d slot %d dropped twice", p.Label, d.Outcome.Outcome, d.Outcome.Slot)
			}
			droppedSkipped[d.Outcome] = true
		case DropOutcome:
			if !needDrop[d.Outcome] {
				return fmt.Errorf("plan %s: drop of %%t%d slot %d, which is not a discarded owned outcome", p.Label, d.Outcome.Outcome, d.Outcome.Slot)
			}
			if droppedOutcome[d.Outcome] {
				return fmt.Errorf("plan %s: %%t%d slot %d dropped twice", p.Label, d.Outcome.Outcome, d.Outcome.Slot)
			}
			droppedOutcome[d.Outcome] = true
		case DropReplaced:
			if !replaced[d.Target] {
				return fmt.Errorf("plan %s: drop of %s's old value, but %s holds no replaced value", p.Label, d.Target, d.Target)
			}
			if taken[d.Target] > 0 {
				return fmt.Errorf("plan %s: %s's old value is both taken and dropped", p.Label, d.Target)
			}
			if droppedTarget[d.Target] {
				return fmt.Errorf("plan %s: %s's old value dropped twice", p.Label, d.Target)
			}
			droppedTarget[d.Target] = true
		default:
			return fmt.Errorf("plan %s: unknown drop kind %d", p.Label, d.Kind)
		}
	}

	for ref := range needDrop {
		if !droppedOutcome[ref] {
			return fmt.Errorf("plan %s: discarded owned outcome %%t%d slot %d is never released", p.Label, ref.Outcome, ref.Slot)
		}
	}
	for ref := range needSkipDrop {
		if !droppedSkipped[ref] {
			return fmt.Errorf("plan %s: owned outcome %%t%d slot %d is never released where it fails", p.Label, ref.Outcome, ref.Slot)
		}
	}
	for name := range replaced {
		if taken[name] == 0 && !droppedTarget[name] {
			return fmt.Errorf("plan %s: %s's old value is neither taken nor released", p.Label, name)
		}
	}

	return nil
}

// outcomeType returns the type of the outcome slot a mapping takes: a slot of
// an eval outside a first, or a first's one slot.
func (p *AssignPlan) outcomeType(ref OutcomeRef) (Type, error) {
	for _, node := range p.Execute {
		switch n := node.(type) {
		case *Eval:
			if n.Result != ref.Outcome {
				continue
			}
			if ref.Slot < 0 || ref.Slot >= len(n.Slots) {
				return nil, fmt.Errorf("plan %s: commit references %%t%d slot %d out of range", p.Label, ref.Outcome, ref.Slot)
			}
			return n.Slots[ref.Slot].Type, nil
		case *First:
			if n.Result == ref.Outcome {
				if ref.Slot != 0 {
					return nil, fmt.Errorf("plan %s: commit references %%t%d slot %d out of range", p.Label, ref.Outcome, ref.Slot)
				}
				return n.Type, nil
			}
			for _, side := range n.Sides {
				if side.Result == ref.Outcome {
					return nil, fmt.Errorf("plan %s: commit references %%t%d, a side of first %%t%d, whose outcome a mapping takes instead", p.Label, ref.Outcome, n.Result)
				}
			}
		}
	}
	return nil, fmt.Errorf("plan %s: commit references unknown outcome %%t%d", p.Label, ref.Outcome)
}
