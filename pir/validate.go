package pir

import "fmt"

// Validate runs on every elaborated plan before lowering (plan §14). Several
// invariants have no natural panic site — an unmapped outcome silently never
// commits, a missing release leaks — so a failure is an ICE. compatible is
// the compiler's directional binding-compatibility relation (target,
// outcome), never display or mangle equality.
func Validate(p *AssignPlan, compatible func(target, outcome Type) bool) error {
	if p.Label == "" {
		return fmt.Errorf("plan has no label")
	}
	if p.Source == "" {
		return fmt.Errorf("plan %s has no source rendering", p.Label)
	}
	if len(p.Evals) == 0 {
		return fmt.Errorf("plan %s has no evals", p.Label)
	}

	slots := 0
	for i, ev := range p.Evals {
		if err := validateEval(p.Label, i, ev); err != nil {
			return err
		}
		slots += len(ev.Slots)
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
		if m.Target.Kind == LocalTarget && m.Target.HoldsHeap {
			replaced[m.Target.Name] = true
		}
	}

	return p.validateReleases(replaced)
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

	return nil
}

func (p *AssignPlan) validateMapping(m Mapping, compatible func(target, outcome Type) bool) error {
	slot, err := p.slot(m.Outcome)
	if err != nil {
		return err
	}
	switch m.Target.Kind {
	case LocalTarget:
		if m.Target.Name == "" {
			return fmt.Errorf("plan %s: local target has no name", p.Label)
		}
		if m.Target.HoldsHeap && m.Target.Fresh {
			return fmt.Errorf("plan %s: fresh target %s holds a value", p.Label, m.Target.Name)
		}
		if !compatible(m.Target.Type, slot.Type) {
			return fmt.Errorf("plan %s: target %s %s mapped to incompatible outcome %%t%d slot %d of type %s",
				p.Label, m.Target.Type.String(), m.Target.Name, m.Outcome.Outcome, m.Outcome.Slot, slot.Type.String())
		}
		return p.validateLocalTransfer(m, slot)
	case DiscardTarget:
		if m.Target.Name != "" || m.Target.Type != nil || m.Target.MaterializeUnmanaged || m.Target.Fresh || m.Target.HoldsHeap {
			return fmt.Errorf("plan %s: discard target carries a name, type, or binding state", p.Label)
		}
		if m.Transfer != Store {
			return fmt.Errorf("plan %s: discard of %%t%d slot %d carries a transfer", p.Label, m.Outcome.Outcome, m.Outcome.Slot)
		}
	default:
		return fmt.Errorf("plan %s: unknown target kind %d", p.Label, m.Target.Kind)
	}

	return nil
}

// validateLocalTransfer pins the transfer Elaborate must have derived (plan
// §8, §14.20-22). A heap transfer into a target declared non-owning is
// legal: the binding then holds heap state the next statement reads back.
func (p *AssignPlan) validateLocalTransfer(m Mapping, slot Slot) error {
	want := Store
	switch slot.Ownership {
	case Owned:
		want = Move
	case Borrowed:
		if m.Transfer == Promote {
			if !p.replacesOwner(slot.Owner) {
				return fmt.Errorf("plan %s: target %s takes %s's old value, but %s is not replaced in this group", p.Label, m.Target.Name, slot.Owner, slot.Owner)
			}
			return nil
		}
		want = Copy
	default:
		if m.Target.MaterializeUnmanaged {
			want = Materialize
		}
	}
	if m.Transfer != want {
		return fmt.Errorf("plan %s: target %s <- %%t%d slot %d uses transfer %s; ownership requires %s",
			p.Label, m.Target.Name, m.Outcome.Outcome, m.Outcome.Slot, m.Transfer, want)
	}

	return nil
}

func (p *AssignPlan) replacesOwner(name string) bool {
	for _, m := range p.Commit {
		if m.Target.Kind == LocalTarget && m.Target.Name == name && m.Target.HoldsHeap {
			return true
		}
	}
	return false
}

// validateReleases checks plan §14.19-21: every owned outcome and every
// replaced held value is consumed or released exactly once.
func (p *AssignPlan) validateReleases(replaced map[string]bool) error {
	taken := make(map[string]int, len(replaced))
	needDrop := make(map[OutcomeRef]bool)
	for _, m := range p.Commit {
		slot := p.Evals[m.Outcome.Outcome].Slots[m.Outcome.Slot]
		if m.Transfer == Promote {
			taken[slot.Owner]++
		}
		if m.Target.Kind == DiscardTarget && slot.Ownership == Owned {
			needDrop[m.Outcome] = true
		}
	}
	for owner, n := range taken {
		if n > 1 {
			return fmt.Errorf("plan %s: %s's old value is taken by %d targets", p.Label, owner, n)
		}
	}

	droppedOutcome := make(map[OutcomeRef]bool)
	droppedTarget := make(map[string]bool)
	for _, d := range p.Drops {
		switch d.Kind {
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
	for name := range replaced {
		if taken[name] == 0 && !droppedTarget[name] {
			return fmt.Errorf("plan %s: %s's old value is neither taken nor released", p.Label, name)
		}
	}

	return nil
}

func (p *AssignPlan) slot(ref OutcomeRef) (Slot, error) {
	if ref.Outcome < 0 || int(ref.Outcome) >= len(p.Evals) {
		return Slot{}, fmt.Errorf("plan %s: commit references unknown outcome %%t%d", p.Label, ref.Outcome)
	}
	slots := p.Evals[ref.Outcome].Slots
	if ref.Slot < 0 || ref.Slot >= len(slots) {
		return Slot{}, fmt.Errorf("plan %s: commit references %%t%d slot %d out of range", p.Label, ref.Outcome, ref.Slot)
	}
	return slots[ref.Slot], nil
}
