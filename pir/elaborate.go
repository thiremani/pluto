package pir

// Elaborate derives each mapping's transfer and the statement's releases
// (plan §6, §8) from the plan's own annotations; Validate re-checks them.
// The first mapping in commit order takes a replaced owner's value and later
// borrows of it copy, so one source feeding several targets is never moved
// twice. Materialization follows the target type (MaterializeUnmanaged);
// replacement and promotion follow the value held (HoldsHeap).
func Elaborate(p *AssignPlan) {
	replaced := make(map[string]bool, len(p.Commit))
	for _, m := range p.Commit {
		if m.Target.Kind == LocalTarget && m.Target.HoldsHeap {
			replaced[m.Target.Name] = true
		}
	}

	taken := make(map[string]bool)
	p.Drops = p.Drops[:0]
	for i := range p.Commit {
		m := &p.Commit[i]
		slot := p.Evals[m.Outcome.Outcome].Slots[m.Outcome.Slot]
		if m.Target.Kind == DiscardTarget {
			m.Transfer = Store
			if slot.Ownership == Owned {
				p.Drops = append(p.Drops, Drop{Kind: DropOutcome, Outcome: m.Outcome})
			}
			continue
		}
		m.Transfer = localTransfer(slot, m.Target, replaced, taken)
	}

	for _, m := range p.Commit {
		if replaced[m.Target.Name] && !taken[m.Target.Name] {
			p.Drops = append(p.Drops, Drop{Kind: DropReplaced, Target: m.Target.Name})
		}
	}
}

func localTransfer(slot Slot, target Target, replaced, taken map[string]bool) Transfer {
	switch slot.Ownership {
	case Owned:
		return Move
	case Borrowed:
		if replaced[slot.Owner] && !taken[slot.Owner] {
			taken[slot.Owner] = true
			return Promote
		}
		return Copy
	}
	if target.MaterializeUnmanaged {
		return Materialize
	}
	return Store
}
