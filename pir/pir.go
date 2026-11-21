// Package pir models one Pluto statement as a typed execution plan
// (docs/Pluto IR Plan.md): source-level execution decisions, never LLVM
// values or storage. The compiler package owns the builder and the lowerer;
// pir owns the nodes, ownership elaboration, validation, and rendering.
package pir

import "github.com/thiremani/pluto/ast"

// Type is the Pluto type of an outcome; pir treats a type as its name.
type Type interface {
	String() string
}

// OutcomeID identifies one node's result (%t<N>); IDs are dense in execution
// order.
type OutcomeID int

// NoOutcome is the result of a first or a last that binds none: one nested as
// a side of a first, whose value reaches a mapping through the first's own
// outcome. Its name would be a name nothing could reference, so it has none.
const NoOutcome OutcomeID = -1

// Ownership is an outcome slot's annotation (plan §8).
type Ownership int

const (
	// Unmanaged is a trivial value: copied freely, never released.
	Unmanaged Ownership = iota
	// Owned holds heap state the plan must consume or release exactly once.
	Owned
	// Borrowed views state a binding owns; Slot.Owner names that binding.
	Borrowed
)

// Slot is one output of an eval or a check. Type is the solver's semantic type;
// Ownership follows the value's effective storage, which may hold heap state
// the type does not show (an empty reset backed by a typed array).
type Slot struct {
	Type      Type
	Ownership Ownership
	Owner     string // the borrowed-from binding; empty unless Borrowed
}

// OOBScope is what a failed checked access in an eval or a check does (plan
// §9). The accesses of one node share one scope.
type OOBScope int

const (
	// NoChecks: the node holds no checked access, so none skips its outcome.
	NoChecks OOBScope = iota
	// SkipOutcome: a failed access skips the node's outcome, every slot of it.
	// An eval's mappings say in OnSkip what each target then holds, and a
	// check's gate rejects the statement.
	SkipOutcome
	// UseFallback: the node is in a side of a first that a fallback follows,
	// which runs where an access fails.
	UseFallback
)

// Eval evaluates one solved source expression; the builder splits out
// anything that affects evaluation strategy before an expression lands here.
type Eval struct {
	Result OutcomeID
	Expr   ast.Expression
	Slots  []Slot // one entry per output slot
	OnOOB  OOBScope
}

// MayFail reports an eval a failed check can keep from yielding.
func (ev *Eval) MayFail() bool { return ev.OnOOB != NoChecks }

// MaySkip reports an eval whose failure no fallback resolves, so it reaches
// the commit.
func (ev *Eval) MaySkip() bool { return ev.OnOOB == SkipOutcome }

// First is a value-position || (plan §4): its sides run in order until one
// yields, and that side's value is its outcome. A side before the last falls
// back to the next where it fails; where the last skips, the outcome skips.
// A side is an eval, a check, or a last — an && of its own, which binds no
// outcome. Each side yields one slot, and a chain of || is one First however
// it is grouped. Its sides take the outcome IDs after Result.
type First struct {
	Result OutcomeID
	Type   Type
	Sides  []Node
}

// MaySkip reports a first whose last side may skip, when no side yields.
func (f *First) MaySkip() bool { return f.last().(Outcome).MaySkip() }

// last returns the side no fallback follows, whose failure skips the outcome.
func (f *First) last() Node { return f.Sides[len(f.Sides)-1] }

// Last is an && (plan §4), in value position or over a gate's conditions: its
// sides run in order while each yields, and its outcome is its final side's,
// yielding where every side did. A side before the final one is a check,
// which is only tested, and the final one is a check or an eval. Each side
// has one slot, and a chain of && is one Last however it is grouped. Its
// sides take the outcome IDs after Result, or the next IDs in execution order
// where it is a side of a first and Result is NoOutcome.
type Last struct {
	Result OutcomeID
	Type   Type
	Sides  []Node
}

// MaySkip reports a last whose failure reaches the commit: every one, since
// its checks may not hold and no fallback follows a last of its own.
func (l *Last) MaySkip() bool { return true }

// final returns the side whose outcome is the last's own.
func (l *Last) final() Node { return l.Sides[len(l.Sides)-1] }

// Check evaluates one comparison (plan §4): its outcome is the left operand,
// and it yields where the comparison holds. A failed access in an operand
// skips it as a failed comparison does. It has one slot. A gate only tests
// the outcome, and a mapping takes it as a value.
type Check struct {
	Result OutcomeID
	Expr   *ast.InfixExpression
	Slots  []Slot
	OnOOB  OOBScope
}

// MayFail reports a check that can fail to yield: every one, since its
// comparison may not hold.
func (ch *Check) MayFail() bool { return true }

// MaySkip reports a check whose failure reaches the commit: every one, since
// its comparison may not hold. A check that a fallback follows is a side of a
// first, whose own MaySkip reads the side no fallback follows.
func (ch *Check) MaySkip() bool { return true }

// Supplier is a node whose value a mapping can take: an eval, or a check,
// whose value is its left operand.
type Supplier interface {
	Node
	MayFail() bool
	MaySkip() bool
	slot(i int) Slot
}

func (ev *Eval) slot(i int) Slot  { return ev.Slots[i] }
func (ch *Check) slot(i int) Slot { return ch.Slots[i] }

// Gate admits or rejects the statement for its whole region (plan §4, §9):
// Body runs where the outcome Cond names yielded, and no mapping commits where
// it did not. A gate binds no outcome, takes no value, and closes the region
// it sits in.
type Gate struct {
	Cond OutcomeID
	Body []Node
}

// Node is one operation of an assignment's execute region: an *Eval, a
// *First, a *Check, a *Last or a *Gate.
type Node interface {
	node()
}

func (*Eval) node()  {}
func (*First) node() {}
func (*Check) node() {}
func (*Last) node()  {}
func (*Gate) node()  {}

// Outcome is a node whose outcome a mapping can take: an eval, a check, a
// first or a last. A gate is no outcome, and a last nested as a side of a
// first binds none, though its value still reaches a mapping.
type Outcome interface {
	Node
	MaySkip() bool
}

// result returns the outcome a node binds: none for a gate, or for a last
// that is a side of a first.
func result(node Node) (OutcomeID, bool) {
	switch n := node.(type) {
	case *Eval:
		return n.Result, true
	case *Check:
		return n.Result, true
	case *First:
		return n.Result, n.Result != NoOutcome
	case *Last:
		return n.Result, n.Result != NoOutcome
	}
	return NoOutcome, false
}

// Outcomes counts the outcomes node binds, those of the nodes inside its
// sides included, so that the node after it takes the next ID in execution
// order. It is asked about a value, never about a gate, whose body holds the
// values and is counted through them.
func Outcomes(node Node) int {
	total := 0
	if _, binds := result(node); binds {
		total = 1
	}
	for _, side := range sidesOf(node) {
		total += Outcomes(side)
	}
	return total
}

// supplier returns the node whose value node hands on: an eval or a check
// supplies its own, and a last supplies its final side's.
func supplier(node Node) Supplier {
	if l, isLast := node.(*Last); isLast {
		return supplier(l.final())
	}
	return node.(Supplier)
}

// Fallible reports a node a failure can keep from yielding, so that a
// fallback after it has something to resolve: an eval where it holds a
// checked access, and a check or a last always, a comparison being able not
// to hold. It is asked about a side of a first, and about no other node.
func Fallible(node Node) bool {
	if ev, isEval := node.(*Eval); isEval {
		return ev.MayFail()
	}
	return true
}

// sidesOf returns the sides of a first or a last, and none for any other node.
func sidesOf(node Node) []Node {
	switch n := node.(type) {
	case *First:
		return n.Sides
	case *Last:
		return n.Sides
	}
	return nil
}

// holds reports whether id names a node inside node: one of its sides, or a
// node inside one of those.
func holds(node Node, id OutcomeID) bool {
	for _, side := range sidesOf(node) {
		if bound, binds := result(side); binds && bound == id {
			return true
		}
		if holds(side, id) {
			return true
		}
	}
	return false
}

// outcomeSlotType is the type of slot i of the outcome node binds; inRange is
// false where node binds no such slot. Only an eval binds several.
func outcomeSlotType(node Node, i int) (t Type, inRange bool) {
	if ev, isEval := node.(*Eval); isEval {
		if i < 0 || i >= len(ev.Slots) {
			return nil, false
		}
		return ev.Slots[i].Type, true
	}
	if i != 0 {
		return nil, false
	}
	switch n := node.(type) {
	case *Check:
		return n.Slots[0].Type, true
	case *First:
		return n.Type, true
	case *Last:
		return n.Type, true
	}
	return nil, false
}

// word names a node's kind as the text view spells it.
func word(node Node) string {
	switch node.(type) {
	case *Eval:
		return "eval"
	case *Check:
		return "check"
	case *First:
		return "first"
	case *Last:
		return "last"
	}
	return "gate"
}

type TargetKind int

const (
	// LocalTarget is an ordinary local binding.
	LocalTarget TargetKind = iota
	// DiscardTarget is a `_` slot: an independent sink, never bound or named.
	DiscardTarget
	// OutputTarget is a function output binding. It is bound at function
	// entry, so it is never fresh, and a commit writes only the output.
	OutputTarget
)

// Target is one LHS location; a discard has no name and no type. Type is the
// binding's merged target type. TypeOwnsHeap: that type requires heap
// cleanup. HoldsHeap: the previously stored value carries a heap-ownership
// obligation a replacement must take or release. The two differ when a
// transfer widened a static-typed binding, so both are recorded. Fresh: no
// previous value; HoldsHeap is then false, but the reverse is not inferred.
type Target struct {
	Kind TargetKind
	Name string
	Type Type

	TypeOwnsHeap bool
	Fresh        bool
	HoldsHeap    bool
}

// binds reports a named binding, local or output; both take a transfer and
// replace the value they hold under the same rules.
func (t Target) binds() bool {
	return t.Kind == LocalTarget || t.Kind == OutputTarget
}

type OutcomeRef struct {
	Outcome OutcomeID
	Slot    int
}

// Transfer is how a commit mapping hands its outcome to its target (plan
// §6, §8); Elaborate derives it. A print invocation takes an owned slot as a
// Move, which is always the case and so is not recorded.
type Transfer int

const (
	Store Transfer = iota // unmanaged value into a non-owning target
	Move                  // owned outcome handed to the target; for a borrowed outcome, the target takes the owner's old value (the owner is replaced in the same group)
	Copy                  // the target gets its own owned copy: of a borrowed outcome, or of an unmanaged value entering a TypeOwnsHeap target
)

// SkipPolicy is what a binding holds where its mapping did not commit: its
// outcome skipped, or a gate rejected the statement (plan §9).
type SkipPolicy int

const (
	NoSkipPolicy SkipPolicy = iota // the mapping always commits, or the target is a discard, which keeps nothing
	KeepOnSkip                     // the binding keeps the value it holds
	ZeroOnSkip                     // a fresh binding holds its type's zero
)

// Mapping is one recorded target = outcome commit pair; the lowerer
// consumes it as recorded, never rematching by name or position. It commits
// where its outcome yielded, under a gate only where the gate admitted, and
// follows OnSkip elsewhere. Transfers holds one entry per node that can
// supply the value: the outcome's own eval or check, each side of a first in
// order, or a last's final side.
type Mapping struct {
	Target    Target
	Outcome   OutcomeRef
	Transfers []Transfer
	OnSkip    SkipPolicy
}

// Releases are what a plan frees, derived by Elaborate (plan §8) and never
// authored by the builder, in the order they run: what a failed eval built is
// released where its mapping would have landed, so before the releases that
// follow every mapping. An OutcomeRef names an eval, a first's side included,
// or a check.
type Releases struct {
	Tested    []OutcomeRef // an owned outcome a check built and nothing takes, released once the check has compared it
	Skipped   []OutcomeRef // what an eval or a check built before it failed, which its binding never took, released only where it failed
	Discarded []OutcomeRef // an owned outcome mapped to `_`, released where its eval or check ran, whether or not it yielded
	Replaced  []string     // a binding whose old value is released after every mapping, where the mapping that replaces it committed
}

// Head is what every plan carries.
type Head struct {
	Label  string // the header: the plan's kind, then an assignment's targets, e.g. "assign a, b" or "print"; not unique, never referenced
	Func   string // display signature of the owning function specialization; empty at the script root
	Source string // source rendering of the statement
}

// Heading returns the head; both plan kinds embed Head, so it gives a Plan's
// head whatever its kind.
func (h Head) Heading() Head { return h }

// Plan is one statement's plan: an *AssignPlan or a *PrintPlan.
type Plan interface {
	Heading() Head
	Render(expanded bool) string
}

// AssignPlan is the execution plan for one assignment statement; prepare and
// finish phases are absent until carries and collectors land. Execute holds
// the execute region's nodes in execution order: the values the commit maps,
// or a gate's condition and then the gate, whose region holds them.
type AssignPlan struct {
	Head
	Execute []Node
	Commit  []Mapping
	Releases
}

// Gate returns the gate that closes the execute region, nil for a statement
// without one.
func (p *AssignPlan) Gate() *Gate {
	gate, _ := p.Execute[len(p.Execute)-1].(*Gate)
	return gate
}

// Condition returns the node whose outcome a gate takes, the first node of a
// gated region: a check, or a last of checks. It is nil for a statement
// without a gate.
func (p *AssignPlan) Condition() Node {
	if p.Gate() == nil {
		return nil
	}
	return p.Execute[0]
}

// tests returns the checks whose outcomes are only tested, in the order they
// run: a gate's condition, then the checks before the final side of each
// value last, whose final side supplies its value.
func (p *AssignPlan) tests() []*Check {
	var checks []*Check
	switch cond := p.Condition().(type) {
	case *Check:
		checks = append(checks, cond)
	case *Last:
		checks = appendChecks(checks, cond.Sides)
	}

	for _, node := range p.Values() {
		checks = appendTested(checks, node)
	}
	return checks
}

// appendTested adds the checks of node whose outcomes are only tested: those
// before the final side of a last, wherever the last sits.
func appendTested(checks []*Check, node Node) []*Check {
	switch n := node.(type) {
	case *Last:
		return appendChecks(checks, n.Sides[:len(n.Sides)-1])
	case *First:
		for _, side := range n.Sides {
			checks = appendTested(checks, side)
		}
	}
	return checks
}

// appendChecks adds the checks among sides to checks.
func appendChecks(checks []*Check, sides []Node) []*Check {
	for _, side := range sides {
		if ch, isCheck := side.(*Check); isCheck {
			checks = append(checks, ch)
		}
	}
	return checks
}

// Values returns the nodes whose outcomes the commit maps: a gate's region,
// or the whole execute region of a statement without a gate.
func (p *AssignPlan) Values() []Node {
	if gate := p.Gate(); gate != nil {
		return gate.Body
	}
	return p.Execute
}

// source is one node a mapping can take its value from, and the slot it
// gives.
type source struct {
	ref  OutcomeRef
	node Supplier
}

func (s source) slot() Slot { return s.node.slot(s.ref.Slot) }

// value returns the node among the values that binds outcome id, nil when
// none does.
func (p *AssignPlan) value(id OutcomeID) Node {
	for _, node := range p.Values() {
		if bound, binds := result(node); binds && bound == id {
			return node
		}
	}
	return nil
}

// sources returns the nodes that can supply ref's value, in the order they
// run: the outcome's own eval or check, each side of a first, or the final
// side of a last. It is empty when ref names none of them.
func (p *AssignPlan) sources(ref OutcomeRef) []source {
	switch n := p.value(ref.Outcome).(type) {
	case Supplier:
		return []source{{ref: ref, node: n}}
	case *First:
		sides := make([]source, len(n.Sides))
		for i, side := range n.Sides {
			sides[i] = sourceOf(side)
		}
		return sides
	case *Last:
		return []source{sourceOf(n)}
	}
	return nil
}

// sourceOf is the node a mapping takes node's value from, and the slot it
// gives.
func sourceOf(node Node) source {
	sup := supplier(node)
	id, _ := result(sup)
	return source{ref: OutcomeRef{Outcome: id}, node: sup}
}

// maySkip reports a mapping whose own outcome may skip, which the node that
// binds it answers: a first through the side no fallback follows, and a last
// always, its checks being able to fail. A gate's rejection is no skip of one
// mapping: it leaves every mapping of the statement unapplied together.
func (p *AssignPlan) maySkip(m Mapping) bool {
	return p.value(m.Outcome.Outcome).(Outcome).MaySkip()
}

// mayNotCommit reports a mapping that some path leaves unapplied, so that its
// binding needs a skip policy: its outcome may skip, or a gate may reject the
// statement.
func (p *AssignPlan) mayNotCommit(m Mapping) bool {
	return p.Gate() != nil || p.maySkip(m)
}

// PrintArg is one argument of a print invocation: a string literal, or an
// outcome whose every slot is printed in order. The invocation formats a
// literal in place, reading the bindings its markers name, and never builds
// the interpolated string, so a literal has no eval and no outcome to
// release. Outcome is unused beside a Literal.
type PrintArg struct {
	Literal *ast.StringLiteral
	Outcome OutcomeID
}

// PrintPlan is the execution plan for one print statement (plan §3): its
// evals, then one invocation that prints every argument on one line. It has
// no commit. The invocation takes each owned slot it is handed and releases
// it, and reads any other slot where it lies.
type PrintPlan struct {
	Head
	Evals []*Eval
	Args  []PrintArg
}
