package pir

// Elaborate derives each mapping's transfers and the statement's releases
// (plan §6, §8) from the plan's own annotations; Validate re-checks them.
// The first mapping in commit order takes a replaced owner's value and later
// borrows of it copy, so one source feeding several targets is never moved
// twice. An unmanaged input is copied into a TypeOwnsHeap target; owned and
// borrowed inputs move, copy, or transfer regardless of it. Replacement
// and promotion follow HoldsHeap.
func Elaborate(p *AssignPlan) {
	// A borrow takes its owner's old value only when its own mapping and the
	// owner's replacement both always commit: were one to land without the
	// other, two bindings would hold one value, or none.
	replaced := make(map[string]bool, len(p.Commit))
	takeable := make(map[string]bool, len(p.Commit))
	for _, m := range p.Commit {
		if m.Target.binds() && m.Target.HoldsHeap {
			replaced[m.Target.Name] = true
			takeable[m.Target.Name] = !p.maySkip(m)
		}
	}

	taken := make(map[string]bool)
	var skipped, discarded []Drop
	for i := range p.Commit {
		m := &p.Commit[i]
		sources := p.sources(m.Outcome)
		m.Transfers = make([]Transfer, len(sources))
		for j, src := range sources {
			owned := src.slot().Ownership == Owned
			if m.Target.Kind == DiscardTarget {
				if owned {
					discarded = append(discarded, Drop{Kind: DropOutcome, Outcome: src.ref})
				}
				continue
			}

			// A side of a first supplies the value on one path only, so its
			// borrow takes nothing either.
			mayTake := takeable
			if len(sources) > 1 || src.eval.MayFail() {
				mayTake = nil
			}
			if owned && src.eval.MayFail() {
				skipped = append(skipped, Drop{Kind: DropSkipped, Outcome: src.ref})
			}
			m.Transfers[j] = bindingTransfer(src.slot(), m.Target, mayTake, taken)
		}
	}

	// What a failed eval built is released where its mapping would have
	// landed, so before the releases that follow every mapping.
	p.Drops = append(skipped, discarded...)
	for _, m := range p.Commit {
		if replaced[m.Target.Name] && !taken[m.Target.Name] {
			p.Drops = append(p.Drops, Drop{Kind: DropReplaced, Target: m.Target.Name})
		}
	}
}

// bindingTransfer picks how a binding acquires its outcome; takeable holds
// the owners whose old value a borrow may take.
func bindingTransfer(slot Slot, target Target, takeable, taken map[string]bool) Transfer {
	switch slot.Ownership {
	case Owned:
		return Move
	case Borrowed:
		if takeable[slot.Owner] && !taken[slot.Owner] {
			taken[slot.Owner] = true
			return Move
		}
		return Copy
	}
	if target.TypeOwnsHeap {
		return Copy
	}
	return Store
}
