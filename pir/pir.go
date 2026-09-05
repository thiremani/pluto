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

// Eval evaluates one solved source expression; the builder splits out
// anything that affects evaluation strategy before an expression lands here.
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

type OutcomeRef struct {
	Outcome OutcomeID
	Slot    int
}

// Transfer is how a commit mapping hands its outcome to its target (plan
// §6, §8); Elaborate derives it.
type Transfer int

const (
	Store       Transfer = iota // unmanaged value into a non-owning target
	Materialize                 // unmanaged value into an owning target: an owned copy is made
	Move                        // owned outcome handed to the target
	Copy                        // borrowed outcome copied; the owner survives
	Promote                     // borrow promoted to transfer: the owner is replaced in the same group
)

// Mapping is one recorded target <- outcome commit pair; the lowerer
// consumes it as recorded, never rematching by name or position.
type Mapping struct {
	Target   Target
	Outcome  OutcomeRef
	Transfer Transfer
}

type DropKind int

const (
	DropOutcome  DropKind = iota // an owned outcome no target took
	DropReplaced                 // a local target's old value, after every mapping
)

// Drop is one derived release (plan §8), never authored by the builder.
type Drop struct {
	Kind    DropKind
	Outcome OutcomeRef // DropOutcome
	Target  string     // DropReplaced
}

// AssignPlan is the execution plan for one assignment statement; prepare and
// finish phases are absent until carries and collectors land.
type AssignPlan struct {
	Label  string // derived from the targets, e.g. assign_x; not unique, never referenced
	Source string // source rendering of the statement
	Evals  []*Eval
	Commit []Mapping
	Drops  []Drop // derived by Elaborate
}
