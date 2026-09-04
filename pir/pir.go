// Package pir models one Pluto statement as a typed execution plan
// (docs/Pluto IR Plan.md): source-level execution decisions, never LLVM
// values or storage. The compiler package owns the facts-to-plan adapter and
// the lowerer; pir owns the nodes, ownership elaboration, the validator, and
// the text rendering.
package pir

import "github.com/thiremani/pluto/ast"

// Type is the Pluto type of an outcome; pir treats a type as its name — the
// concrete type system lives in the compiler package.
type Type interface {
	String() string
}

// OutcomeID identifies one node's result (%t<N>); IDs are dense in execution
// order. Rendered names are display only — nothing parses them.
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

// Slot is one output of an eval: its type and ownership annotation.
type Slot struct {
	Type      Type
	Ownership Ownership
	Owner     string // the borrowed-from binding; empty unless Borrowed
}

// Eval evaluates one solved source expression. The builder must split out
// anything that affects evaluation strategy (ranges, conditionals, checked
// accesses, collectors) before an expression may appear here.
type Eval struct {
	Result OutcomeID
	Expr   ast.Expression
	Slots  []Slot // one entry per output slot
}

type TargetKind int

const (
	// LocalTarget is an ordinary local binding.
	LocalTarget TargetKind = iota
	// DiscardTarget is a `_` slot: an independent sink, never bound or named.
	DiscardTarget
)

// Target is one LHS location. A discard has no name and no type; a local
// records its solver-declared binding type — an independent fact, not a copy
// of the outcome type — so validation can reject a mismapped outcome.
//
// Owns reports that the binding's declared type holds heap state, so an
// unmanaged value stored here is materialized into an owned copy. Fresh
// reports that the binding has no value yet, so the commit replaces nothing.
// Holds reports that the value the binding currently holds owns heap state
// and must be released when replaced; it is the binding's effective
// storage, which can be heap while the declared type is not — a heap value
// moved, copied, or transferred into a binding keeps its flavor — so Holds
// is recorded separately from Owns and is always false when Fresh.
type Target struct {
	Kind  TargetKind
	Name  string
	Type  Type
	Owns  bool
	Fresh bool
	Holds bool
}

// OutcomeRef addresses one slot of one eval's result.
type OutcomeRef struct {
	Outcome OutcomeID
	Slot    int
}

// Transfer is how a commit mapping hands its outcome to its target, derived
// by Elaborate from the slot's ownership and the target (plan §6, §8).
type Transfer int

const (
	// Store writes an unmanaged value into a non-owning target.
	Store Transfer = iota
	// Materialize writes an unmanaged value into an owning target, which
	// makes an owned copy of it.
	Materialize
	// Move hands an owned outcome to the target.
	Move
	// Copy gives the target its own copy of a borrowed outcome; the owner
	// survives.
	Copy
	// Promote is a borrow promoted to transfer: the owner is replaced in the
	// same group, so the target takes the owner's old value without a copy.
	Promote
)

// Mapping is one recorded target <- outcome commit pair; the lowerer must
// consume it as recorded, never rematching by name or position.
type Mapping struct {
	Target   Target
	Outcome  OutcomeRef
	Transfer Transfer
}

type DropKind int

const (
	// DropOutcome releases an owned outcome no target took.
	DropOutcome DropKind = iota
	// DropReplaced releases a local target's old value once every mapping
	// has landed.
	DropReplaced
)

// Drop is one derived release (plan §8): never authored by the builder,
// always at the statement's exit after every mapping.
type Drop struct {
	Kind    DropKind
	Outcome OutcomeRef // DropOutcome
	Target  string     // DropReplaced
}

// AssignPlan is the execution plan for one assignment statement. The prepare
// and finish phases are structurally absent until carries and collectors land.
type AssignPlan struct {
	Label  string // derived from the targets, e.g. assign_x; not unique, never referenced
	Source string // source rendering of the statement
	Evals  []*Eval
	Commit []Mapping
	Drops  []Drop // derived by Elaborate
}
