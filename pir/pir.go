// Package pir models one Pluto statement as a typed execution plan built
// after solving and validated before LLVM lowering (docs/Pluto IR Plan.md).
// The plan records source-language execution decisions; it contains no LLVM
// values, storage, or machine types. The compiler package owns the
// facts-to-plan adapter and the plan-to-LLVM lowerer; pir owns the node
// shapes, the structural validator, and the deterministic text rendering.
package pir

import "github.com/thiremani/pluto/ast"

// Type is the Pluto type of an outcome as the plan renders it. The concrete
// type system lives in the compiler package; pir treats a type as its name.
type Type interface {
	String() string
}

// OutcomeID identifies one value-producing node's result — %tN in the text
// form. The builder assigns IDs densely in execution order.
type OutcomeID int

// Eval evaluates one solved source expression. Ordinary arithmetic stays
// inside the expression; the builder must split out anything that affects
// evaluation strategy (ranges, conditionals, checked accesses, collectors)
// before an expression may appear here.
type Eval struct {
	Result OutcomeID
	Expr   ast.Expression
	Types  []Type // one entry per output slot
}

type TargetKind int

const (
	// LocalTarget is an ordinary local binding.
	LocalTarget TargetKind = iota
	// DiscardTarget is a `_` slot: an independent sink, never bound or named.
	DiscardTarget
)

// Target is one LHS location. A discard target has no name and no type; a
// local target records its resolved binding type from the solver — an
// independent fact, not a copy of the outcome type — so validation can
// reject a mismapped outcome.
type Target struct {
	Kind TargetKind
	Name string
	Type Type
}

// OutcomeRef addresses one slot of one eval's result.
type OutcomeRef struct {
	Outcome OutcomeID
	Slot    int
}

// Mapping is one recorded target <- outcome commit pair. The lowerer must
// consume this mapping as recorded, never rematching by name or position.
type Mapping struct {
	Target  Target
	Outcome OutcomeRef
}

// AssignPlan is the execution plan for one assignment statement. Step 3
// scope: unmanaged outcomes (scalars and Range descriptors), local and
// discard targets, and a simultaneous commit; the prepare and finish phases
// are structurally absent until carries and collectors land.
type AssignPlan struct {
	Name   string // deterministic plan symbol, e.g. assign_x
	Source string // source rendering of the statement
	Evals  []*Eval
	Commit []Mapping
}
