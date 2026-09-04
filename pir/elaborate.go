package pir

// Elaborate derives the ownership decisions of a commit group (plan §6, §8):
// the transfer of every mapping and the releases the statement owes at its
// exit. It runs between the builder and Validate and consults only the
// plan's own annotations; the validator re-checks every decision.
//
// A borrowed outcome is promoted to transfer when its owner is a local
// target whose held heap value is replaced in this group and no earlier
// mapping already took that value; the first mapping in commit order wins
// and the rest copy, so one source feeding several targets is never moved
// twice. A replaced held value nothing took is released after every mapping
// has landed, and so is an owned outcome mapped to a discard. Materialization
// follows the declared type (Owns); replacement follows the effective one
// (Holds).
func Elaborate(p *AssignPlan) {
	replaced := make(map[string]bool, len(p.Commit))
	for _, m := range p.Commit {
		if m.Target.Kind == LocalTarget && m.Target.Holds {
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
	if target.Owns {
		return Materialize
	}
	return Store
}
