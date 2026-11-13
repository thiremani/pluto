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

// Slot is one output of an eval. Type is the solver's semantic type;
// Ownership follows the value's effective storage, which may hold heap state
// the type does not show (an empty reset backed by a typed array).
type Slot struct {
	Type      Type
	Ownership Ownership
	Owner     string // the borrowed-from binding; empty unless Borrowed
}

// OOBScope is what a failed checked access in an eval does (plan §9). The
// accesses of one eval share one scope.
type OOBScope int

const (
	// NoChecks: the eval holds no checked access, so its outcome always yields.
	NoChecks OOBScope = iota
	// SkipOutcome: a failed access skips the eval's outcome, every slot of it,
	// and each mapping's OnSkip says what its target then holds.
	SkipOutcome
)

// Eval evaluates one solved source expression; the builder splits out
// anything that affects evaluation strategy before an expression lands here.
type Eval struct {
	Result OutcomeID
	Expr   ast.Expression
	Slots  []Slot // one entry per output slot
	OnOOB  OOBScope
}

// MaySkip reports an outcome a failed check can keep from yielding.
func (ev *Eval) MaySkip() bool { return ev.OnOOB == SkipOutcome }

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

// SkipPolicy is what a binding holds where its mapping's outcome skipped
// (plan §9).
type SkipPolicy int

const (
	NoSkip     SkipPolicy = iota // the outcome always yields, or the target is a discard, which keeps nothing
	KeepOnSkip                   // the binding keeps the value it holds
	ZeroOnSkip                   // a fresh binding holds its type's zero
)

// Mapping is one recorded target <- outcome commit pair; the lowerer
// consumes it as recorded, never rematching by name or position. It commits
// where its outcome yielded and follows OnSkip elsewhere.
type Mapping struct {
	Target   Target
	Outcome  OutcomeRef
	Transfer Transfer
	OnSkip   SkipPolicy
}

type DropKind int

const (
	DropOutcome  DropKind = iota // an owned outcome no target took
	DropReplaced                 // a local target's old value, after every mapping
	DropSkipped                  // what an outcome built before it skipped, which its binding never took
)

// Drop is one derived release (plan §8), never authored by the builder. A
// DropOutcome runs whether or not its outcome yielded, a DropSkipped only
// where it skipped, and a DropReplaced only where the mapping that replaces
// Target committed.
type Drop struct {
	Kind    DropKind
	Outcome OutcomeRef // DropOutcome, DropSkipped
	Target  string     // DropReplaced
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
// finish phases are absent until carries and collectors land.
type AssignPlan struct {
	Head
	Evals  []*Eval
	Commit []Mapping
	Drops  []Drop // derived by Elaborate, in the order they run
}

// certain reports a mapping that always commits: its outcome cannot skip.
func (p *AssignPlan) certain(m Mapping) bool {
	return !p.Evals[m.Outcome.Outcome].MaySkip()
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
