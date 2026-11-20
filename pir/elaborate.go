package pir

// Elaborate derives each mapping's transfers and the statement's releases
// (plan §6, §8) from the plan's own annotations, trusting the shape the
// compiler builds; Validate re-checks them.
// The first mapping in commit order takes a replaced owner's value and later
// borrows of it copy, so one source feeding several targets is never moved
// twice. An unmanaged input is copied into a TypeOwnsHeap target; owned and
// borrowed inputs move, copy, or transfer regardless of it. Replacement
// and promotion follow HoldsHeap.
func Elaborate(p *AssignPlan) {
	// A borrow takes its owner's old value only when neither its own mapping
	// nor the owner's replacement may skip: were one to land without the
	// other, two bindings would hold one value, or none. A gate is no such
	// skip, since it rejects both together.
	replaced := make(map[string]bool, len(p.Commit))
	takeable := make(map[string]bool, len(p.Commit))
	for _, m := range p.Commit {
		if m.Target.binds() && m.Target.HoldsHeap {
			replaced[m.Target.Name] = true
			takeable[m.Target.Name] = !p.maySkip(m)
		}
	}

	taken := make(map[string]bool)
	var releases Releases
	for i := range p.Commit {
		m := &p.Commit[i]
		sources := p.sources(m.Outcome)
		m.Transfers = make([]Transfer, len(sources))
		for j, src := range sources {
			owned := src.slot().Ownership == Owned
			if m.Target.Kind == DiscardTarget {
				if owned {
					releases.Discarded = append(releases.Discarded, src.ref)
				}
				continue
			}

			// Nor does a borrow take anything where its own mapping may skip,
			// or where it is one side of a first, which supplies the value on
			// one path only.
			mayTake := takeable
			if len(sources) > 1 || p.maySkip(*m) {
				mayTake = nil
			}
			if owned && src.node.MayFail() {
				releases.Skipped = append(releases.Skipped, src.ref)
			}
			m.Transfers[j] = bindingTransfer(src.slot(), m.Target, mayTake, taken)
		}
	}

	for _, m := range p.Commit {
		if replaced[m.Target.Name] && !taken[m.Target.Name] {
			releases.Replaced = append(releases.Replaced, m.Target.Name)
		}
	}

	// A check that is only tested hands its outcome to nothing, so what it
	// built is released.
	for _, ch := range p.tests() {
		for i, slot := range ch.Slots {
			if slot.Ownership == Owned {
				releases.Tested = append(releases.Tested, OutcomeRef{Outcome: ch.Result, Slot: i})
			}
		}
	}
	p.Releases = releases
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
