// Package pir models one Pluto statement as a typed execution plan
// (docs/Pluto IR Plan.md): source-level execution decisions, never LLVM
// values or storage. The compiler package owns the facts-to-plan adapter and
// the lowerer; pir owns the nodes, the validator, and the text rendering.
package pir

import "github.com/thiremani/pluto/ast"

// Type is the Pluto type of an outcome; pir treats a type as its name — the
// concrete type system lives in the compiler package.
type Type interface {
	String() string
}

// OutcomeID identifies one node's result (%tN); IDs are dense in execution
// order.
type OutcomeID int

// Eval evaluates one solved source expression. The builder must split out
// anything that affects evaluation strategy (ranges, conditionals, checked
// accesses, collectors) before an expression may appear here.
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

// Target is one LHS location. A discard has no name and no type; a local
// records its solver-declared binding type — an independent fact, not a copy
// of the outcome type — so validation can reject a mismapped outcome.
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

// Mapping is one recorded target <- outcome commit pair; the lowerer must
// consume it as recorded, never rematching by name or position.
type Mapping struct {
	Target  Target
	Outcome OutcomeRef
}

// AssignPlan is the execution plan for one assignment statement. The prepare
// and finish phases are structurally absent until carries and collectors land.
type AssignPlan struct {
	Name   string // deterministic plan symbol, e.g. assign_x
	Source string // source rendering of the statement
	Evals  []*Eval
	Commit []Mapping
}
