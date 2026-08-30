package pir

import "fmt"

// Validate checks an AssignPlan against the structural invariants the Step 3
// slice can express (plan §14). A validation failure on a router-accepted
// statement is an internal compiler error; the caller decides how to fail.
// Fields the builder unconditionally sets are not nil-checked — an
// impossible nil panics here or in lowering and surfaces as an ICE like any
// other. Outcome types are the one exception: a slot mapped to a discard is
// touched only by -emit-pir rendering, so a nil there would make plan
// validity depend on the output mode instead of failing every compile.
// Type compatibility is checked by rendered name —
// pir has no type system, so semantic equality stays the builder's contract.
// Name equality suffices for the slice's scalar and Range types; Step 4
// needs real compatibility semantics (a valid empty-array reset renders
// differently from its target).
func Validate(p *AssignPlan) error {
	if p.Name == "" {
		return fmt.Errorf("plan has no name")
	}
	if p.Source == "" {
		return fmt.Errorf("plan %s has no source rendering", p.Name)
	}
	if len(p.Evals) == 0 {
		return fmt.Errorf("plan %s has no evals", p.Name)
	}

	slots := 0
	for i, ev := range p.Evals {
		if ev.Result != OutcomeID(i) {
			return fmt.Errorf("plan %s: eval %d has result ID %d; IDs must be dense in execution order", p.Name, i, ev.Result)
		}
		if len(ev.Types) == 0 {
			return fmt.Errorf("plan %s: eval %%t%d has no output slots", p.Name, i)
		}
		for s, t := range ev.Types {
			if t == nil {
				return fmt.Errorf("plan %s: eval %%t%d slot %d has no type", p.Name, i, s)
			}
		}
		slots += len(ev.Types)
	}

	if len(p.Commit) != slots {
		return fmt.Errorf("plan %s: commit has %d mappings for %d outcome slots", p.Name, len(p.Commit), slots)
	}
	consumed := make(map[OutcomeRef]bool, slots)
	for _, m := range p.Commit {
		switch m.Target.Kind {
		case LocalTarget:
			if m.Target.Name == "" {
				return fmt.Errorf("plan %s: local target has no name", p.Name)
			}
		case DiscardTarget:
			if m.Target.Name != "" {
				return fmt.Errorf("plan %s: discard target carries name %q", p.Name, m.Target.Name)
			}
			if m.Target.Type != nil {
				return fmt.Errorf("plan %s: discard target carries a type", p.Name)
			}
		default:
			return fmt.Errorf("plan %s: unknown target kind %d", p.Name, m.Target.Kind)
		}
		if m.Outcome.Outcome < 0 || int(m.Outcome.Outcome) >= len(p.Evals) {
			return fmt.Errorf("plan %s: commit references unknown outcome %%t%d", p.Name, m.Outcome.Outcome)
		}
		if m.Outcome.Slot < 0 || m.Outcome.Slot >= len(p.Evals[m.Outcome.Outcome].Types) {
			return fmt.Errorf("plan %s: commit references %%t%d slot %d out of range", p.Name, m.Outcome.Outcome, m.Outcome.Slot)
		}
		if m.Target.Kind == LocalTarget {
			outcomeType := p.Evals[m.Outcome.Outcome].Types[m.Outcome.Slot]
			if m.Target.Type.String() != outcomeType.String() {
				return fmt.Errorf("plan %s: target @%s : %s mapped to outcome %%t%d slot %d : %s",
					p.Name, m.Target.Name, m.Target.Type.String(), m.Outcome.Outcome, m.Outcome.Slot, outcomeType.String())
			}
		}
		if consumed[m.Outcome] {
			return fmt.Errorf("plan %s: outcome %%t%d slot %d consumed twice", p.Name, m.Outcome.Outcome, m.Outcome.Slot)
		}
		consumed[m.Outcome] = true
	}
	return nil
}
