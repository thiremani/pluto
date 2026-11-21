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

	next, err := p.validateGate(compatible)
	if err != nil {
		return err
	}

	slots := 0
	for _, node := range p.Values() {
		switch n := node.(type) {
		case *Eval:
			if err := validateEval(p.Label, next, n, false); err != nil {
				return err
			}
			slots += len(n.Slots)
		case *Check:
			if err := validateCheck(p.Label, next, n, false); err != nil {
				return err
			}
			slots++
		case *First:
			if err := validateFirst(p.Label, next, n, compatible); err != nil {
				return err
			}
			slots++
		case *Last:
			if err := validateDense(p.Label, "last", n.Result, next); err != nil {
				return err
			}
			if err := validateLast(p.Label, fmt.Sprintf("last %%t%d", next), next+1, n, compatible, false); err != nil {
				return err
			}
			slots++
		default:
			return fmt.Errorf("plan %s: unknown execute node %T", p.Label, node)
		}
		next += Outcomes(node)
	}

	if len(p.Commit) != slots {
		return fmt.Errorf("plan %s: commit has %d mappings for %d outcome slots", p.Label, len(p.Commit), slots)
	}

	outcomes, err := p.validateOutcomes()
	if err != nil {
		return err
	}

	replaced := make(map[string]bool, slots)
	for i, m := range p.Commit {
		if err := p.validateMapping(m, outcomes[i], compatible); err != nil {
			return err
		}
		if m.Target.binds() && m.Target.HoldsHeap {
			replaced[m.Target.Name] = true
		}
	}

	return p.validateReleases(replaced)
}

// validateGate checks what a gated plan's execute region holds beside its
// values: one condition, then the gate that takes it and admits a region. The
// condition is a check, or a last whose every side is one. It returns the
// outcome ID the values start at. A gate anywhere else is no value, and the
// values' own validation rejects it.
func (p *AssignPlan) validateGate(compatible func(target, outcome Type) bool) (int, error) {
	gate := p.Gate()
	if gate == nil {
		return 0, nil
	}

	before := p.Execute[:len(p.Execute)-1]
	if len(before) != 1 {
		return 0, fmt.Errorf("plan %s: gate follows %d nodes and takes one condition", p.Label, len(before))
	}

	switch cond := before[0].(type) {
	case *Check:
		if err := validateCheck(p.Label, 0, cond, false); err != nil {
			return 0, err
		}
	case *Last:
		if err := validateDense(p.Label, "last", cond.Result, 0); err != nil {
			return 0, err
		}
		if err := validateLast(p.Label, "last %t0", 1, cond, compatible, false); err != nil {
			return 0, err
		}
		if _, isCheck := cond.final().(*Check); !isCheck {
			return 0, fmt.Errorf("plan %s: last %%t0 ends in %T, and a gate's condition ends in a check", p.Label, cond.final())
		}
	default:
		return 0, fmt.Errorf("plan %s: gate follows %T, which is no condition", p.Label, before[0])
	}
	next := Outcomes(before[0])

	if bound, _ := result(before[0]); gate.Cond != bound {
		return 0, fmt.Errorf("plan %s: gate takes %%t%d where its condition binds %%t%d", p.Label, gate.Cond, bound)
	}
	if len(gate.Body) == 0 {
		return 0, fmt.Errorf("plan %s: gate admits an empty region", p.Label)
	}
	return next, nil
}

// validateCheck checks a check whose outcome is id: one well-formed slot, and
// a scope for its accesses that matches where it sits. fallbackFollows says a
// fallback follows the side of a first it belongs to.
func validateCheck(plan string, id int, ch *Check, fallbackFollows bool) error {
	if err := validateDense(plan, "check", ch.Result, id); err != nil {
		return err
	}
	if len(ch.Slots) != 1 {
		return fmt.Errorf("plan %s: check %%t%d has %d slots", plan, id, len(ch.Slots))
	}

	node := fmt.Sprintf("check %%t%d", id)
	if err := validateScope(plan, node, ch.OnOOB, fallbackFollows); err != nil {
		return err
	}
	if err := validateSlots(plan, node, ch.Slots); err != nil {
		return err
	}
	return validateAccesses(plan, node, ch.Expr, ch.OnOOB)
}

// validateDense checks that the node word names binds the ID that follows
// every node before it.
func validateDense(plan, word string, id OutcomeID, next int) error {
	if id != OutcomeID(next) {
		return fmt.Errorf("plan %s: %s has result ID %d where %d is next; IDs must be dense in execution order", plan, word, id, next)
	}
	return nil
}

// validateScope checks plan §9: a node's out-of-bounds scope says what
// resolves a failed access, which is where the node sits. A fallback resolves
// it where one follows the node's side of a first, and the commit resolves it
// everywhere else, as a skipped outcome.
func validateScope(plan, node string, scope OOBScope, fallbackFollows bool) error {
	if fallbackFollows && scope == SkipOutcome {
		return fmt.Errorf("plan %s: %s skips a failed access, which the fallback after its side resolves", plan, node)
	}
	if !fallbackFollows && scope == UseFallback {
		return fmt.Errorf("plan %s: %s falls back, but no fallback follows it", plan, node)
	}
	return nil
}

// validateLast checks a last, named as an error names it, whose sides start
// at sideStart: at least two sides of one slot each, numbered in execution
// order. Every side before the final one is a check, and the final one, a
// check or an eval, fits the last's type. fallbackFollows says a fallback
// follows the side of a first the last belongs to, which resolves a failed
// access in any of its sides.
func validateLast(plan, name string, sideStart int, l *Last, compatible func(target, outcome Type) bool, fallbackFollows bool) error {
	if l.Type == nil {
		return fmt.Errorf("plan %s: %s has no type", plan, name)
	}
	if len(l.Sides) < 2 {
		return fmt.Errorf("plan %s: %s has %d sides and needs a next", plan, name, len(l.Sides))
	}

	final := len(l.Sides) - 1
	for i, side := range l.Sides {
		sideID := sideStart + i
		switch n := side.(type) {
		case *Check:
			if err := validateCheck(plan, sideID, n, fallbackFollows); err != nil {
				return err
			}
			if i == final && !compatible(l.Type, n.Slots[0].Type) {
				return fmt.Errorf("plan %s: side %%t%d of type %s does not fit %s of type %s", plan, sideID, n.Slots[0].Type.String(), name, l.Type.String())
			}
		case *Eval:
			if i < final {
				return fmt.Errorf("plan %s: side %%t%d of %s is an eval before the final side, where a side is a check", plan, sideID, name)
			}
			if err := validateEval(plan, sideID, n, fallbackFollows); err != nil {
				return err
			}
			if len(n.Slots) != 1 {
				return fmt.Errorf("plan %s: side %%t%d of %s has %d slots", plan, sideID, name, len(n.Slots))
			}
			if !compatible(l.Type, n.Slots[0].Type) {
				return fmt.Errorf("plan %s: side %%t%d of type %s does not fit %s of type %s", plan, sideID, n.Slots[0].Type.String(), name, l.Type.String())
			}
		default:
			return fmt.Errorf("plan %s: side %%t%d of %s is %T, which is no check or eval", plan, sideID, name, side)
		}
	}

	return nil
}

// validateFirst checks a first whose outcome is id: at least two sides, each
// yielding one slot that fits its type, numbered after it in execution order.
// A side a fallback follows runs it where it fails, so it must be able to
// fail, and the last side has nothing to fall back to.
func validateFirst(plan string, id int, f *First, compatible func(target, outcome Type) bool) error {
	if err := validateDense(plan, "first", f.Result, id); err != nil {
		return err
	}
	if f.Type == nil {
		return fmt.Errorf("plan %s: first %%t%d has no type", plan, id)
	}
	if len(f.Sides) < 2 {
		return fmt.Errorf("plan %s: first %%t%d has %d sides and needs a fallback", plan, id, len(f.Sides))
	}

	sideID := id + 1
	for i, side := range f.Sides {
		if err := validateFirstSide(plan, f, i, sideID, side, compatible); err != nil {
			return err
		}
		sideID += Outcomes(side)
	}
	return nil
}

// validateFirstSide checks side i of f, whose own nodes start at sideID: an
// eval or a check of one slot fitting the first's type, or a last — an && of
// its own, which binds no outcome, its value reaching a mapping through the
// first's. A side a fallback follows must be able to fail. A message names a
// side by the outcome it binds, and a nested last by its place.
func validateFirstSide(plan string, f *First, i, sideID int, side Node, compatible func(target, outcome Type) bool) error {
	fallbackFollows := i < len(f.Sides)-1
	name := fmt.Sprintf("side %%t%d", sideID)
	switch n := side.(type) {
	case *Eval:
		if err := validateEval(plan, sideID, n, fallbackFollows); err != nil {
			return err
		}
		if len(n.Slots) != 1 {
			return fmt.Errorf("plan %s: %s of first %%t%d has %d slots", plan, name, f.Result, len(n.Slots))
		}
		if fallbackFollows && !n.MayFail() {
			return fmt.Errorf("plan %s: %s of first %%t%d cannot fail, so the fallback after it would never run", plan, name, f.Result)
		}
		return validateFirstSideType(plan, name, n.Slots[0].Type, f, compatible)
	case *Check:
		if err := validateCheck(plan, sideID, n, fallbackFollows); err != nil {
			return err
		}
		return validateFirstSideType(plan, name, n.Slots[0].Type, f, compatible)
	case *Last:
		name = fmt.Sprintf("the last in side %d", i+1)
		if n.Result != NoOutcome {
			return fmt.Errorf("plan %s: %s of first %%t%d binds outcome %%t%d, and a side of a first binds none", plan, name, f.Result, n.Result)
		}
		if err := validateLast(plan, fmt.Sprintf("%s of first %%t%d", name, f.Result), sideID, n, compatible, fallbackFollows); err != nil {
			return err
		}
		return validateFirstSideType(plan, name, n.Type, f, compatible)
	}
	return fmt.Errorf("plan %s: side %d of first %%t%d is %T, which is no eval, check or last", plan, i+1, f.Result, side)
}

// validateFirstSideType checks that a side's own type fits the first's, which
// names the whole selection.
func validateFirstSideType(plan, name string, side Type, f *First, compatible func(target, outcome Type) bool) error {
	if !compatible(f.Type, side) {
		return fmt.Errorf("plan %s: %s of type %s does not fit first %%t%d of type %s", plan, name, side.String(), f.Result, f.Type.String())
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
		if err := validateEval(p.Label, i, ev, false); err != nil {
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

// validateEval checks an eval whose outcome is i; fallbackFollows says a
// fallback follows the side of a first it belongs to.
func validateEval(plan string, i int, ev *Eval, fallbackFollows bool) error {
	if ev.Result != OutcomeID(i) {
		return fmt.Errorf("plan %s: eval %d has result ID %d; IDs must be dense in execution order", plan, i, ev.Result)
	}
	if len(ev.Slots) == 0 {
		return fmt.Errorf("plan %s: eval %%t%d has no output slots", plan, i)
	}

	node := fmt.Sprintf("eval %%t%d", i)
	if err := validateScope(plan, node, ev.OnOOB, fallbackFollows); err != nil {
		return err
	}
	if err := validateSlots(plan, node, ev.Slots); err != nil {
		return err
	}
	return validateAccesses(plan, node, ev.Expr, ev.OnOOB)
}

// validateSlots checks the slots of node, an eval or a check named as an
// error names it: each has a type, and names an owner exactly when borrowed.
func validateSlots(plan, node string, slots []Slot) error {
	for s, slot := range slots {
		if slot.Type == nil {
			return fmt.Errorf("plan %s: %s slot %d has no type", plan, node, s)
		}
		switch slot.Ownership {
		case Unmanaged, Owned:
			if slot.Owner != "" {
				return fmt.Errorf("plan %s: %s slot %d names owner %s but is not borrowed", plan, node, s, slot.Owner)
			}
		case Borrowed:
			if slot.Owner == "" {
				return fmt.Errorf("plan %s: %s slot %d is borrowed from no owner", plan, node, s)
			}
		default:
			return fmt.Errorf("plan %s: %s slot %d has unknown ownership %d", plan, node, s, slot.Ownership)
		}
	}
	return nil
}

// validateAccesses checks plan §14.15: every checked access has an explicit
// out-of-bounds scope, which its node, an eval or a check, records. A literal
// cell resolves a failed access itself, a scope no plan node records.
func validateAccesses(plan, node string, expr ast.Expression, scope OOBScope) error {
	switch scope {
	case NoChecks, SkipOutcome, UseFallback:
	default:
		return fmt.Errorf("plan %s: %s has unknown out-of-bounds scope %d", plan, node, scope)
	}

	outcome, cell := checkedAccesses(expr, false)
	if cell > 0 {
		return fmt.Errorf("plan %s: %s holds a checked access in a literal cell, a scope no plan node records", plan, node)
	}
	if outcome > 0 && scope == NoChecks {
		return fmt.Errorf("plan %s: %s holds a checked access with no out-of-bounds scope", plan, node)
	}
	if outcome == 0 && scope != NoChecks {
		return fmt.Errorf("plan %s: %s has an out-of-bounds scope and no checked access", plan, node)
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

// validateOutcomes checks that each mapping takes an outcome slot a value
// binds, no two the same, and returns each slot's type. Validate runs it
// before checking any mapping further: a moved borrow asks whether the
// mapping that replaces its owner may skip, and that mapping may come later.
func (p *AssignPlan) validateOutcomes() ([]Type, error) {
	types := make([]Type, len(p.Commit))
	consumed := make(map[OutcomeRef]bool, len(p.Commit))
	for i, m := range p.Commit {
		outcome, err := p.outcomeType(m.Outcome)
		if err != nil {
			return nil, err
		}
		if consumed[m.Outcome] {
			return nil, fmt.Errorf("plan %s: outcome %%t%d slot %d consumed twice", p.Label, m.Outcome.Outcome, m.Outcome.Slot)
		}
		consumed[m.Outcome] = true
		types[i] = outcome
	}
	return types, nil
}

func (p *AssignPlan) validateMapping(m Mapping, outcome Type, compatible func(target, outcome Type) bool) error {
	sources := p.sources(m.Outcome)
	if len(m.Transfers) != len(sources) {
		return fmt.Errorf("plan %s: target %s = %%t%d slot %d has %d transfers for the %d nodes that can supply it",
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

// validateSkipPolicy checks plan §9: a binding whose mapping may not commit
// keeps its value, or holds zero when it is fresh, and no other mapping
// carries a policy.
func (p *AssignPlan) validateSkipPolicy(m Mapping) error {
	want := NoSkipPolicy
	if m.Target.binds() && p.mayNotCommit(m) {
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
// compatible type that takes its outcome, from each node that can supply it,
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
// one node that can supply m's value (plan §8, §14.20-22); alone says it is
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
// owned outcome a binding takes where it yields is released where it fails,
// and one a check only tested is released whether or not it yielded.
func (p *AssignPlan) validateReleases(replaced map[string]bool) error {
	taken := make(map[string]int, len(replaced))
	needDiscardDrop := make(map[OutcomeRef]bool)
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
				needDiscardDrop[src.ref] = true
			} else if src.node.MayFail() {
				needSkipDrop[src.ref] = true
			}
		}
	}
	for owner, n := range taken {
		if n > 1 {
			return fmt.Errorf("plan %s: %s's old value is taken by %d targets", p.Label, owner, n)
		}
	}

	needTestDrop := make(map[OutcomeRef]bool)
	for _, ch := range p.tests() {
		for i, slot := range ch.Slots {
			if slot.Ownership == Owned {
				needTestDrop[OutcomeRef{Outcome: ch.Result, Slot: i}] = true
			}
		}
	}
	droppedTested := make(map[OutcomeRef]bool)
	for _, ref := range p.Tested {
		if !needTestDrop[ref] {
			return fmt.Errorf("plan %s: drop of %%t%d slot %d, which is not an owned outcome a check only tested", p.Label, ref.Outcome, ref.Slot)
		}
		if droppedTested[ref] {
			return fmt.Errorf("plan %s: tested %%t%d slot %d dropped twice", p.Label, ref.Outcome, ref.Slot)
		}
		droppedTested[ref] = true
	}
	for ref := range needTestDrop {
		if !droppedTested[ref] {
			return fmt.Errorf("plan %s: owned outcome %%t%d slot %d, which a check only tested, is never released", p.Label, ref.Outcome, ref.Slot)
		}
	}

	droppedSkipped := make(map[OutcomeRef]bool)
	for _, ref := range p.Skipped {
		if !needSkipDrop[ref] {
			return fmt.Errorf("plan %s: skip-path drop of %%t%d slot %d, which is not an owned outcome a binding takes only where it yields", p.Label, ref.Outcome, ref.Slot)
		}
		if droppedSkipped[ref] {
			return fmt.Errorf("plan %s: skipped %%t%d slot %d dropped twice", p.Label, ref.Outcome, ref.Slot)
		}
		droppedSkipped[ref] = true
	}
	droppedDiscarded := make(map[OutcomeRef]bool)
	for _, ref := range p.Discarded {
		if !needDiscardDrop[ref] {
			return fmt.Errorf("plan %s: drop of %%t%d slot %d, which is not a discarded owned outcome", p.Label, ref.Outcome, ref.Slot)
		}
		if droppedDiscarded[ref] {
			return fmt.Errorf("plan %s: %%t%d slot %d dropped twice", p.Label, ref.Outcome, ref.Slot)
		}
		droppedDiscarded[ref] = true
	}
	droppedTarget := make(map[string]bool)
	for _, name := range p.Replaced {
		if !replaced[name] {
			return fmt.Errorf("plan %s: drop of %s's old value, but %s holds no replaced value", p.Label, name, name)
		}
		if taken[name] > 0 {
			return fmt.Errorf("plan %s: %s's old value is both taken and dropped", p.Label, name)
		}
		if droppedTarget[name] {
			return fmt.Errorf("plan %s: %s's old value dropped twice", p.Label, name)
		}
		droppedTarget[name] = true
	}

	for ref := range needDiscardDrop {
		if !droppedDiscarded[ref] {
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
// an eval, or the one slot of a check, a first or a last, never a node inside
// one of those.
func (p *AssignPlan) outcomeType(ref OutcomeRef) (Type, error) {
	for _, node := range p.Values() {
		if bound, binds := result(node); binds && bound == ref.Outcome {
			t, inRange := outcomeSlotType(node, ref.Slot)
			if !inRange {
				return nil, fmt.Errorf("plan %s: commit references %%t%d slot %d out of range", p.Label, ref.Outcome, ref.Slot)
			}
			return t, nil
		}
		if holds(node, ref.Outcome) {
			bound, _ := result(node)
			return nil, fmt.Errorf("plan %s: commit references %%t%d, which sits inside %s %%t%d, whose outcome a mapping takes instead", p.Label, ref.Outcome, word(node), bound)
		}
	}
	return nil, fmt.Errorf("plan %s: commit references unknown outcome %%t%d", p.Label, ref.Outcome)
}
