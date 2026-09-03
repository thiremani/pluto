package pir

import (
	"strings"
	"testing"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
)

type testType string

func (t testType) String() string { return string(t) }

func intLit(lit string) ast.Expression {
	return &ast.IntegerLiteral{Token: token.Token{Literal: lit}}
}

func ident(name string) ast.Expression {
	return &ast.Identifier{Token: token.Token{Literal: name}, Value: name}
}

func swapPlan() *AssignPlan {
	return &AssignPlan{
		Label:  "assign_a_b",
		Source: "a, b = b, a",
		Evals: []*Eval{
			{Result: 0, Expr: ident("b"), Types: []Type{testType("I64")}},
			{Result: 1, Expr: ident("a"), Types: []Type{testType("I64")}},
		},
		Commit: []Mapping{
			{Target: Target{Kind: LocalTarget, Name: "a", Type: testType("I64")}, Outcome: OutcomeRef{Outcome: 0}},
			{Target: Target{Kind: LocalTarget, Name: "b", Type: testType("I64")}, Outcome: OutcomeRef{Outcome: 1}},
		},
	}
}

func TestValidateAccepts(t *testing.T) {
	if err := Validate(swapPlan()); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
}

// Plan §14: validation invariants.
func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*AssignPlan)
		wantErr string
	}{
		{"NoLabel", func(p *AssignPlan) { p.Label = "" }, "no label"},
		{"NoSource", func(p *AssignPlan) { p.Source = "" }, "no source"},
		{"NoEvals", func(p *AssignPlan) { p.Evals = nil }, "no evals"},
		{"SparseResultID", func(p *AssignPlan) { p.Evals[1].Result = 2 }, "dense"},
		{"NoSlots", func(p *AssignPlan) { p.Evals[0].Types = nil }, "no output slots"},
		{"NilDiscardedType", func(p *AssignPlan) {
			p.Commit[0].Target = Target{Kind: DiscardTarget}
			p.Evals[0].Types = []Type{nil}
		}, "slot 0 has no type"},
		{"MissingMapping", func(p *AssignPlan) { p.Commit = p.Commit[:1] }, "1 mappings for 2 outcome slots"},
		{"UnnamedLocal", func(p *AssignPlan) { p.Commit[0].Target.Name = "" }, "local target has no name"},
		{"TypeMismatch", func(p *AssignPlan) { p.Evals[0].Types = []Type{testType("F64")} }, "target @a : I64 mapped to outcome %t0 slot 0 : F64"},
		{"NamedDiscard", func(p *AssignPlan) { p.Commit[0].Target = Target{Kind: DiscardTarget, Name: "x"} }, "discard target carries"},
		{"TypedDiscard", func(p *AssignPlan) { p.Commit[0].Target = Target{Kind: DiscardTarget, Type: testType("I64")} }, "discard target carries"},
		{"UnknownOutcome", func(p *AssignPlan) { p.Commit[0].Outcome.Outcome = 5 }, "unknown outcome"},
		{"SlotOutOfRange", func(p *AssignPlan) { p.Commit[0].Outcome.Slot = 3 }, "out of range"},
		{"NegativeOutcome", func(p *AssignPlan) { p.Commit[0].Outcome.Outcome = -1 }, "unknown outcome"},
		{"NegativeSlot", func(p *AssignPlan) { p.Commit[0].Outcome.Slot = -1 }, "out of range"},
		{"DoubleConsume", func(p *AssignPlan) { p.Commit[1].Outcome = p.Commit[0].Outcome }, "consumed twice"},
		{"UnknownTargetKind", func(p *AssignPlan) { p.Commit[0].Target.Kind = 7 }, "unknown target kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := swapPlan()
			tc.mutate(p)
			err := Validate(p)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

// Plan §12: text form, concise view.
func TestRenderConcise(t *testing.T) {
	p := &AssignPlan{
		Label:  "assign_x__",
		Source: "x, _ = 5, 7",
		Evals: []*Eval{
			{Result: 0, Expr: intLit("5"), Types: []Type{testType("I64")}},
			{Result: 1, Expr: intLit("7"), Types: []Type{testType("I64")}},
		},
		Commit: []Mapping{
			{Target: Target{Kind: LocalTarget, Name: "x", Type: testType("I64")}, Outcome: OutcomeRef{Outcome: 0}},
			{Target: Target{Kind: DiscardTarget}, Outcome: OutcomeRef{Outcome: 1}},
		},
	}
	want := `statement assign_x__
    source "x, _ = 5, 7"

    execute
        %t0 = eval I64 (5)
        %t1 = eval I64 (7)

    commit
        @x <- %t0
        discard <- %t1
`
	if got := p.Render(false); got != want {
		t.Fatalf("concise render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// Plan §12: a multi-output outcome is addressed per slot with a trailing
// numeric selector.
func TestRenderMultiOutput(t *testing.T) {
	p := &AssignPlan{
		Label:  "assign_a_b",
		Source: "a, b = pair",
		Evals: []*Eval{
			{Result: 0, Expr: ident("pair"), Types: []Type{testType("I64"), testType("F64")}},
		},
		Commit: []Mapping{
			{Target: Target{Kind: LocalTarget, Name: "a", Type: testType("I64")}, Outcome: OutcomeRef{Outcome: 0, Slot: 0}},
			{Target: Target{Kind: LocalTarget, Name: "b", Type: testType("F64")}, Outcome: OutcomeRef{Outcome: 0, Slot: 1}},
		},
	}
	if err := Validate(p); err != nil {
		t.Fatalf("multi-output plan rejected: %v", err)
	}
	want := `statement assign_a_b
    source "a, b = pair"

    execute
        %t0 = eval I64, F64 (@pair)

    commit
        @a <- %t0#0
        @b <- %t0#1
`
	if got := p.Render(false); got != want {
		t.Fatalf("multi-output render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// Plan §12: the renderer covers exactly the router's node kinds. A
// well-formed string literal rendered fine under the old ast fallback, so
// only the explicit rejection produces this exact panic value.
func TestRenderRejectsUnsupportedNode(t *testing.T) {
	p := swapPlan()
	p.Evals[0].Expr = &ast.StringLiteral{Token: token.Token{Literal: "hi"}}
	defer func() {
		if got := recover(); got != "pir: no renderer for *ast.StringLiteral" {
			t.Fatalf("expected the renderer rejection panic, got %v", got)
		}
	}()
	p.Render(false)
}

// Plan §12: expanded view adds shapes, ownership, and target types.
func TestRenderExpanded(t *testing.T) {
	p := swapPlan()
	got := p.Render(true)
	want := `statement assign_a_b
    source "a, b = b, a"

    execute
        %t0 = eval I64 (@b) [shape=scalar] [yield=always] [unmanaged]
        %t1 = eval I64 (@a) [shape=scalar] [yield=always] [unmanaged]

    commit
        @a : I64 <- %t0
        @b : I64 <- %t1
`
	if got != want {
		t.Fatalf("expanded render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}
