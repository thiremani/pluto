package pir

import "fmt"

// Validate runs on every accepted plan before lowering (plan §14): several
// invariants have no guaranteed panic site (an unmapped outcome silently
// never commits; a nil discarded-outcome type fails only under -emit-pir),
// so a failure is an ICE, never a silent miscompile. Types are compared by
// rendered name — sufficient for scalars and Range; Step 4 needs real
// compatibility semantics (an empty-array reset renders unlike its target).
func Validate(p *AssignPlan) error {
	if len(p.Evals) == 0 {
		return fmt.Errorf("plan %s has no evals", p.Name)
	}
	slots := 0
	for i, ev := range p.Evals {
		if err := validateEval(p.Name, i, ev); err != nil {
			return err
		}
		slots += len(ev.Types)
	}
	if len(p.Commit) != slots {
		return fmt.Errorf("plan %s: commit has %d mappings for %d outcome slots", p.Name, len(p.Commit), slots)
	}
	consumed := make(map[OutcomeRef]bool, slots)
	for _, m := range p.Commit {
		if err := p.validateMapping(m); err != nil {
			return err
		}
		if consumed[m.Outcome] {
			return fmt.Errorf("plan %s: outcome %%t%d slot %d consumed twice", p.Name, m.Outcome.Outcome, m.Outcome.Slot)
		}
		consumed[m.Outcome] = true
	}
	return nil
}

func validateEval(plan string, i int, ev *Eval) error {
	if ev.Result != OutcomeID(i) {
		return fmt.Errorf("plan %s: eval %d has result ID %d; IDs must be dense in execution order", plan, i, ev.Result)
	}
	if len(ev.Types) == 0 {
		return fmt.Errorf("plan %s: eval %%t%d has no output slots", plan, i)
	}
	for s, t := range ev.Types {
		if t == nil {
			return fmt.Errorf("plan %s: eval %%t%d slot %d has no type", plan, i, s)
		}
	}
	return nil
}

func (p *AssignPlan) validateMapping(m Mapping) error {
	outcomeType, err := p.slotType(m.Outcome)
	if err != nil {
		return err
	}
	switch m.Target.Kind {
	case LocalTarget:
		if m.Target.Name == "" {
			return fmt.Errorf("plan %s: local target has no name", p.Name)
		}
		if m.Target.Type.String() != outcomeType.String() {
			return fmt.Errorf("plan %s: target @%s : %s mapped to outcome %%t%d slot %d : %s",
				p.Name, m.Target.Name, m.Target.Type.String(), m.Outcome.Outcome, m.Outcome.Slot, outcomeType.String())
		}
	case DiscardTarget:
		if m.Target.Name != "" || m.Target.Type != nil {
			return fmt.Errorf("plan %s: discard target carries a name or type", p.Name)
		}
	default:
		return fmt.Errorf("plan %s: unknown target kind %d", p.Name, m.Target.Kind)
	}
	return nil
}

func (p *AssignPlan) slotType(ref OutcomeRef) (Type, error) {
	if ref.Outcome < 0 || int(ref.Outcome) >= len(p.Evals) {
		return nil, fmt.Errorf("plan %s: commit references unknown outcome %%t%d", p.Name, ref.Outcome)
	}
	types := p.Evals[ref.Outcome].Types
	if ref.Slot < 0 || ref.Slot >= len(types) {
		return nil, fmt.Errorf("plan %s: commit references %%t%d slot %d out of range", p.Name, ref.Outcome, ref.Slot)
	}
	return types[ref.Slot], nil
}
