package pir

import (
	"strconv"
	"strings"
	"testing"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
)

type testType string

func (t testType) String() string { return string(t) }

// sameSpelling stands in for the compiler's binding-compatibility relation;
// these tests exercise the plan invariants, not the type system.
func sameSpelling(target, outcome Type) bool { return target.String() == outcome.String() }

func intLit(value int64) ast.Expression {
	lit := strconv.FormatInt(value, 10)
	return &ast.IntegerLiteral{Token: token.Token{Type: token.INT, Literal: lit}, Value: value}
}

func ident(name string) ast.Expression {
	return &ast.Identifier{Token: token.Token{Type: token.IDENT, Literal: name}, Value: name}
}

func strLit(text string) ast.Expression {
	return &ast.StringLiteral{Token: token.Token{Type: token.STRING, Literal: text}}
}

func concat(left, right ast.Expression) ast.Expression {
	return &ast.InfixExpression{Token: token.Token{Type: token.OPERATOR, Literal: "⊕"}, Left: left, Operator: "⊕", Right: right}
}

func unmanagedSlot(t Type) Slot { return Slot{Type: t} }
func ownedSlot(t Type) Slot     { return Slot{Type: t, Ownership: Owned} }
func borrowedSlot(t Type, owner string) Slot {
	return Slot{Type: t, Ownership: Borrowed, Owner: owner}
}

func local(name string, t Type) Target { return Target{Kind: LocalTarget, Name: name, Type: t} }

// heapLocal is an existing owning binding holding a heap value.
func heapLocal(name string, t Type) Target {
	return Target{Kind: LocalTarget, Name: name, Type: t, Owns: true, Holds: true}
}

// widenedLocal is an existing binding declared non-owning that nevertheless
// holds a heap value a previous transfer left in it.
func widenedLocal(name string, t Type) Target {
	return Target{Kind: LocalTarget, Name: name, Type: t, Holds: true}
}

func swapPlan() *AssignPlan {
	return &AssignPlan{
		Label:  "assign_a_b",
		Source: "a, b = b, a",
		Evals: []*Eval{
			{Result: 0, Expr: ident("b"), Slots: []Slot{unmanagedSlot(testType("I64"))}},
			{Result: 1, Expr: ident("a"), Slots: []Slot{unmanagedSlot(testType("I64"))}},
		},
		Commit: []Mapping{
			{Target: local("a", testType("I64")), Outcome: OutcomeRef{Outcome: 0}},
			{Target: local("b", testType("I64")), Outcome: OutcomeRef{Outcome: 1}},
		},
	}
}

// heapSwapPlan is `a, b = b, a` over heap strings, before elaboration.
func heapSwapPlan() *AssignPlan {
	str := testType("Str")
	return &AssignPlan{
		Label:  "assign_a_b",
		Source: "a, b = b, a",
		Evals: []*Eval{
			{Result: 0, Expr: ident("b"), Slots: []Slot{borrowedSlot(str, "b")}},
			{Result: 1, Expr: ident("a"), Slots: []Slot{borrowedSlot(str, "a")}},
		},
		Commit: []Mapping{
			{Target: heapLocal("a", str), Outcome: OutcomeRef{Outcome: 0}},
			{Target: heapLocal("b", str), Outcome: OutcomeRef{Outcome: 1}},
		},
	}
}

// replacePlan is `x, _ = x ⊕ "!", "a" ⊕ "b"`: x's old value is replaced by an
// owned outcome, and a second owned outcome is discarded.
func replacePlan() *AssignPlan {
	str := testType("Str")
	return &AssignPlan{
		Label:  "assign_x__",
		Source: `x, _ = x ⊕ "!", "a" ⊕ "b"`,
		Evals: []*Eval{
			{Result: 0, Expr: concat(ident("x"), strLit("!")), Slots: []Slot{ownedSlot(str)}},
			{Result: 1, Expr: concat(strLit("a"), strLit("b")), Slots: []Slot{ownedSlot(str)}},
		},
		Commit: []Mapping{
			{Target: heapLocal("x", str), Outcome: OutcomeRef{Outcome: 0}},
			{Target: Target{Kind: DiscardTarget}, Outcome: OutcomeRef{Outcome: 1}},
		},
	}
}

func elaborated(p *AssignPlan) *AssignPlan {
	Elaborate(p)
	return p
}

func TestValidateAccepts(t *testing.T) {
	for name, p := range map[string]*AssignPlan{
		"scalar swap":    swapPlan(),
		"heap swap":      elaborated(heapSwapPlan()),
		"replace + drop": elaborated(replacePlan()),
	} {
		if err := Validate(p, sameSpelling); err != nil {
			t.Fatalf("%s: valid plan rejected: %v", name, err)
		}
	}
}

// Plan §8: elaboration derives transfers and releases from the annotations.
func TestElaborate(t *testing.T) {
	str := testType("Str")
	cases := []struct {
		name      string
		plan      *AssignPlan
		transfers []Transfer
		drops     []Drop
	}{
		{"scalar swap stores", swapPlan(), []Transfer{Store, Store}, nil},
		{"heap swap transfers both, copies nothing", heapSwapPlan(), []Transfer{Promote, Promote}, nil},
		{"owned replacement moves and releases the old value", replacePlan(), []Transfer{Move, Store},
			[]Drop{{Kind: DropOutcome, Outcome: OutcomeRef{Outcome: 1}}, {Kind: DropReplaced, Target: "x"}}},
		{"duplicate source: first takes, second copies", &AssignPlan{
			Label: "assign_d1_d2", Source: "d1, d2 = d1, d1",
			Evals: []*Eval{
				{Result: 0, Expr: ident("d1"), Slots: []Slot{borrowedSlot(str, "d1")}},
				{Result: 1, Expr: ident("d1"), Slots: []Slot{borrowedSlot(str, "d1")}},
			},
			Commit: []Mapping{
				{Target: heapLocal("d1", str), Outcome: OutcomeRef{Outcome: 0}},
				{Target: heapLocal("d2", str), Outcome: OutcomeRef{Outcome: 1}},
			},
		}, []Transfer{Promote, Copy}, []Drop{{Kind: DropReplaced, Target: "d2"}}},
		{"borrow of a surviving owner copies", &AssignPlan{
			Label: "assign_t", Source: "t = s",
			Evals:  []*Eval{{Result: 0, Expr: ident("s"), Slots: []Slot{borrowedSlot(str, "s")}}},
			Commit: []Mapping{{Target: Target{Kind: LocalTarget, Name: "t", Type: str, Owns: true, Fresh: true}, Outcome: OutcomeRef{Outcome: 0}}},
		}, []Transfer{Copy}, nil},
		{"unmanaged into an owning target materializes", &AssignPlan{
			Label: "assign_s", Source: `s = "hi"`,
			Evals:  []*Eval{{Result: 0, Expr: strLit("hi"), Slots: []Slot{unmanagedSlot(str)}}},
			Commit: []Mapping{{Target: heapLocal("s", str), Outcome: OutcomeRef{Outcome: 0}}},
		}, []Transfer{Materialize}, []Drop{{Kind: DropReplaced, Target: "s"}}},
		{"heap transfer into a non-owning fresh target is legal", &AssignPlan{
			Label: "assign_other", Source: "other = text",
			Evals:  []*Eval{{Result: 0, Expr: ident("text"), Slots: []Slot{borrowedSlot(str, "text")}}},
			Commit: []Mapping{{Target: Target{Kind: LocalTarget, Name: "other", Type: str, Fresh: true}, Outcome: OutcomeRef{Outcome: 0}}},
		}, []Transfer{Copy}, nil},
		{"widened binding releases its held value on a plain store", &AssignPlan{
			Label: "assign_other", Source: `other = "new"`,
			Evals:  []*Eval{{Result: 0, Expr: strLit("new"), Slots: []Slot{unmanagedSlot(str)}}},
			Commit: []Mapping{{Target: widenedLocal("other", str), Outcome: OutcomeRef{Outcome: 0}}},
		}, []Transfer{Store}, []Drop{{Kind: DropReplaced, Target: "other"}}},
		{"widened owner is taken by a promoted borrow", &AssignPlan{
			Label: "assign_a_other", Source: "a, other = other, a",
			Evals: []*Eval{
				{Result: 0, Expr: ident("other"), Slots: []Slot{borrowedSlot(str, "other")}},
				{Result: 1, Expr: ident("a"), Slots: []Slot{borrowedSlot(str, "a")}},
			},
			Commit: []Mapping{
				{Target: heapLocal("a", str), Outcome: OutcomeRef{Outcome: 0}},
				{Target: widenedLocal("other", str), Outcome: OutcomeRef{Outcome: 1}},
			},
		}, []Transfer{Promote, Promote}, nil},
		{"discarded borrow releases nothing", &AssignPlan{
			Label: "assign__", Source: "_ = h",
			Evals:  []*Eval{{Result: 0, Expr: ident("h"), Slots: []Slot{borrowedSlot(str, "h")}}},
			Commit: []Mapping{{Target: Target{Kind: DiscardTarget}, Outcome: OutcomeRef{Outcome: 0}}},
		}, []Transfer{Store}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Elaborate(tc.plan)
			for i, m := range tc.plan.Commit {
				if m.Transfer != tc.transfers[i] {
					t.Errorf("mapping %d transfer = %s, want %s", i, m.Transfer, tc.transfers[i])
				}
			}
			if len(tc.plan.Drops) != len(tc.drops) {
				t.Fatalf("drops = %v, want %v", tc.plan.Drops, tc.drops)
			}
			for i, d := range tc.plan.Drops {
				if d != tc.drops[i] {
					t.Errorf("drop %d = %v, want %v", i, d, tc.drops[i])
				}
			}
			if err := Validate(tc.plan, sameSpelling); err != nil {
				t.Fatalf("elaborated plan rejected: %v", err)
			}
		})
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
		{"NoSlots", func(p *AssignPlan) { p.Evals[0].Slots = nil }, "no output slots"},
		{"NilDiscardedType", func(p *AssignPlan) {
			p.Commit[0].Target = Target{Kind: DiscardTarget}
			p.Evals[0].Slots = []Slot{{}}
		}, "slot 0 has no type"},
		{"OwnerWithoutBorrow", func(p *AssignPlan) { p.Evals[0].Slots[0].Owner = "b" }, "names owner b but is not borrowed"},
		{"BorrowWithoutOwner", func(p *AssignPlan) { p.Evals[0].Slots[0].Ownership = Borrowed }, "borrowed from no owner"},
		{"UnknownOwnership", func(p *AssignPlan) { p.Evals[0].Slots[0].Ownership = 9 }, "unknown ownership"},
		{"MissingMapping", func(p *AssignPlan) { p.Commit = p.Commit[:1] }, "1 mappings for 2 outcome slots"},
		{"UnnamedLocal", func(p *AssignPlan) { p.Commit[0].Target.Name = "" }, "local target has no name"},
		{"TypeMismatch", func(p *AssignPlan) { p.Evals[0].Slots[0].Type = testType("F64") }, "target a : I64 mapped to incompatible outcome %t0 slot 0 : F64"},
		{"NamedDiscard", func(p *AssignPlan) { p.Commit[0].Target = Target{Kind: DiscardTarget, Name: "x"} }, "discard target carries"},
		{"TypedDiscard", func(p *AssignPlan) { p.Commit[0].Target = Target{Kind: DiscardTarget, Type: testType("I64")} }, "discard target carries"},
		{"OwningDiscard", func(p *AssignPlan) { p.Commit[0].Target = Target{Kind: DiscardTarget, Owns: true} }, "discard target carries"},
		{"HoldingDiscard", func(p *AssignPlan) { p.Commit[0].Target = Target{Kind: DiscardTarget, Holds: true} }, "discard target carries"},
		{"FreshTargetHolds", func(p *AssignPlan) {
			p.Commit[0].Target.Fresh = true
			p.Commit[0].Target.Holds = true
		}, "fresh target a holds a value"},
		{"DiscardWithTransfer", func(p *AssignPlan) {
			p.Commit[0].Target = Target{Kind: DiscardTarget}
			p.Commit[0].Transfer = Move
		}, "discard of %t0 slot 0 carries a transfer"},
		{"UnknownOutcome", func(p *AssignPlan) { p.Commit[0].Outcome.Outcome = 5 }, "unknown outcome"},
		{"SlotOutOfRange", func(p *AssignPlan) { p.Commit[0].Outcome.Slot = 3 }, "out of range"},
		{"NegativeOutcome", func(p *AssignPlan) { p.Commit[0].Outcome.Outcome = -1 }, "unknown outcome"},
		{"NegativeSlot", func(p *AssignPlan) { p.Commit[0].Outcome.Slot = -1 }, "out of range"},
		{"DoubleConsume", func(p *AssignPlan) { p.Commit[1].Outcome = p.Commit[0].Outcome }, "consumed twice"},
		{"UnknownTargetKind", func(p *AssignPlan) { p.Commit[0].Target.Kind = 7 }, "unknown target kind"},
		{"UnmanagedMoved", func(p *AssignPlan) { p.Commit[0].Transfer = Move }, "uses transfer move; ownership requires store"},
		{"UnmanagedIntoOwnerNotMaterialized", func(p *AssignPlan) { p.Commit[0].Target.Owns = true }, "uses transfer store; ownership requires materialize"},
		{"OwnedNotMoved", func(p *AssignPlan) { p.Evals[0].Slots[0].Ownership = Owned }, "uses transfer store; ownership requires move"},
		{"BorrowedNotCopied", func(p *AssignPlan) { p.Evals[0].Slots[0] = borrowedSlot(testType("I64"), "b") }, "uses transfer store; ownership requires copy"},
		{"PromoteOfSurvivingOwner", func(p *AssignPlan) {
			p.Evals[0].Slots[0] = borrowedSlot(testType("I64"), "z")
			p.Commit[0].Transfer = Promote
		}, "target a takes z's old value, but z is not replaced in this group"},
		{"PromoteOfFreshOwner", func(p *AssignPlan) {
			p.Evals[0].Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfer = Promote
			p.Commit[1].Target.Owns = true
			p.Commit[1].Target.Fresh = true
			p.Commit[1].Transfer = Materialize
		}, "b is not replaced in this group"},
		{"PromoteOfOwnerHoldingNothing", func(p *AssignPlan) {
			p.Evals[0].Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfer = Promote
			p.Commit[1].Target.Owns = true
			p.Commit[1].Transfer = Materialize
		}, "b is not replaced in this group"},
		{"OwnerTakenTwice", func(p *AssignPlan) {
			p.Evals[0].Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Evals[1].Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfer = Promote
			p.Commit[1].Transfer = Promote
			p.Commit[1].Target.Holds = true
		}, "b's old value is taken by 2 targets"},
		{"ReplacedNeverReleased", func(p *AssignPlan) { p.Commit[0].Target.Holds = true }, "a's old value is neither taken nor released"},
		{"ReplacedTakenAndDropped", func(p *AssignPlan) {
			p.Evals[0].Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfer = Promote
			p.Commit[1].Target.Holds = true
			p.Drops = []Drop{{Kind: DropReplaced, Target: "b"}}
		}, "b's old value is both taken and dropped"},
		{"DropOfUnreplacedTarget", func(p *AssignPlan) { p.Drops = []Drop{{Kind: DropReplaced, Target: "a"}} }, "a holds no replaced value"},
		{"DropOfFreshTarget", func(p *AssignPlan) {
			p.Commit[0].Target.Owns = true
			p.Commit[0].Target.Fresh = true
			p.Commit[0].Transfer = Materialize
			p.Drops = []Drop{{Kind: DropReplaced, Target: "a"}}
		}, "a holds no replaced value"},
		{"ReplacedDroppedTwice", func(p *AssignPlan) {
			p.Commit[0].Target.Holds = true
			p.Drops = []Drop{{Kind: DropReplaced, Target: "a"}, {Kind: DropReplaced, Target: "a"}}
		}, "a's old value dropped twice"},
		{"DropOfMappedOutcome", func(p *AssignPlan) { p.Drops = []Drop{{Kind: DropOutcome, Outcome: OutcomeRef{Outcome: 0}}} }, "not a discarded owned outcome"},
		{"DropOfDiscardedBorrow", func(p *AssignPlan) {
			p.Evals[0].Slots[0] = borrowedSlot(testType("I64"), "z")
			p.Commit[0].Target = Target{Kind: DiscardTarget}
			p.Drops = []Drop{{Kind: DropOutcome, Outcome: OutcomeRef{Outcome: 0}}}
		}, "not a discarded owned outcome"},
		{"DiscardedOwnedNeverReleased", func(p *AssignPlan) {
			p.Evals[0].Slots[0].Ownership = Owned
			p.Commit[0].Target = Target{Kind: DiscardTarget}
		}, "discarded owned outcome %t0 slot 0 is never released"},
		{"DiscardedOwnedDroppedTwice", func(p *AssignPlan) {
			p.Evals[0].Slots[0].Ownership = Owned
			p.Commit[0].Target = Target{Kind: DiscardTarget}
			p.Drops = []Drop{{Kind: DropOutcome, Outcome: OutcomeRef{Outcome: 0}}, {Kind: DropOutcome, Outcome: OutcomeRef{Outcome: 0}}}
		}, "%t0 slot 0 dropped twice"},
		{"UnknownDropKind", func(p *AssignPlan) { p.Drops = []Drop{{Kind: 4}} }, "unknown drop kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := swapPlan()
			tc.mutate(p)
			err := Validate(p, sameSpelling)
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
			{Result: 0, Expr: intLit(5), Slots: []Slot{unmanagedSlot(testType("I64"))}},
			{Result: 1, Expr: intLit(7), Slots: []Slot{unmanagedSlot(testType("I64"))}},
		},
		Commit: []Mapping{
			{Target: local("x", testType("I64")), Outcome: OutcomeRef{Outcome: 0}},
			{Target: Target{Kind: DiscardTarget}, Outcome: OutcomeRef{Outcome: 1}},
		},
	}
	want := `statement assign_x__
    source "x, _ = 5, 7"

    execute
        %t0 = eval I64 5
        %t1 = eval I64 7

    commit
        x <- %t0
        _ <- %t1
`
	if got := p.Render(false); got != want {
		t.Fatalf("concise render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// Plan §12: a multi-output outcome is addressed per slot with a trailing
// numeric selector; the expanded view annotates each slot in order.
func TestRenderMultiOutput(t *testing.T) {
	p := &AssignPlan{
		Label:  "assign_a_b",
		Source: "a, b = pair",
		Evals: []*Eval{
			{Result: 0, Expr: ident("pair"), Slots: []Slot{unmanagedSlot(testType("I64")), ownedSlot(testType("Str"))}},
		},
		Commit: []Mapping{
			{Target: local("a", testType("I64")), Outcome: OutcomeRef{Outcome: 0, Slot: 0}},
			{Target: heapLocal("b", testType("Str")), Outcome: OutcomeRef{Outcome: 0, Slot: 1}},
		},
	}
	Elaborate(p)
	if err := Validate(p, sameSpelling); err != nil {
		t.Fatalf("multi-output plan rejected: %v", err)
	}
	want := `statement assign_a_b
    source "a, b = pair"

    execute
        %t0 = eval I64, Str pair

    commit
        a <- %t0#0
        b <- %t0#1
`
	if got := p.Render(false); got != want {
		t.Fatalf("multi-output render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	wantExpanded := `statement assign_a_b
    source "a, b = pair"

    execute
        %t0 = eval I64, Str pair [shape=scalar] [yield=always] [unmanaged] [owned]

    commit
        a : I64 <- %t0#0
        b : Str <- %t0#1 [move]
        drop b [replaced]
`
	if got := p.Render(true); got != wantExpanded {
		t.Fatalf("multi-output expanded render mismatch:\ngot:\n%s\nwant:\n%s", got, wantExpanded)
	}
}

// Plan §12: the renderer rejects an unsupported node anywhere in the tree,
// not only at the root, and a block-layout literal that has no one-line
// spelling.
func TestRenderRejectsUnsupportedNode(t *testing.T) {
	call := &ast.CallExpression{Token: token.Token{Type: token.LPAREN, Literal: "("}, Function: ident("f").(*ast.Identifier)}
	tests := []struct {
		name string
		expr ast.Expression
		want string
	}{
		{"root", call, "pir: no renderer for *ast.CallExpression"},
		{"under infix", &ast.InfixExpression{Left: ident("a"), Operator: "+", Right: call}, "pir: no renderer for *ast.CallExpression"},
		{"two edges deep", &ast.InfixExpression{Left: ident("a"), Operator: "+", Right: &ast.PrefixExpression{Operator: "-", Right: call}}, "pir: no renderer for *ast.CallExpression"},
		{"block literal", &ast.ArrayLiteral{Block: true, Rows: [][]ast.Expression{{intLit(1)}, {intLit(2)}}}, "pir: no one-line renderer for a block-layout array literal"},
		{"table literal", &ast.ArrayLiteral{Headers: []string{"Name"}, Rows: [][]ast.Expression{{strLit("Ann")}}}, "pir: no one-line renderer for a block-layout array literal"},
		{"call in array cell", &ast.ArrayLiteral{Rows: [][]ast.Expression{{intLit(1), call}}}, "pir: no renderer for *ast.CallExpression"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := swapPlan()
			p.Evals[0].Expr = tt.expr
			if got := renderPanic(p); got != tt.want {
				t.Fatalf("panic = %v, want %v", got, tt.want)
			}
		})
	}
}

func renderPanic(p *AssignPlan) (v any) {
	defer func() { v = recover() }()
	p.Render(false)
	return nil
}

// Plan §12: expanded view adds shapes, ownership, and target types; an
// unmanaged store carries no transfer annotation.
func TestRenderExpanded(t *testing.T) {
	p := swapPlan()
	got := p.Render(true)
	want := `statement assign_a_b
    source "a, b = b, a"

    execute
        %t0 = eval I64 b [shape=scalar] [yield=always] [unmanaged]
        %t1 = eval I64 a [shape=scalar] [yield=always] [unmanaged]

    commit
        a : I64 <- %t0
        b : I64 <- %t1
`
	if got != want {
		t.Fatalf("expanded render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// Plan §8, §17: copies are loud — a heap swap shows two transfers and no
// copy or release; an owned replacement shows the move, the discard's
// release, and the replaced value's release after the mappings.
func TestRenderExpandedOwnership(t *testing.T) {
	want := `statement assign_a_b
    source "a, b = b, a"

    execute
        %t0 = eval Str b [shape=scalar] [yield=always] [borrowed=b]
        %t1 = eval Str a [shape=scalar] [yield=always] [borrowed=a]

    commit
        a : Str <- %t0 [transfer]
        b : Str <- %t1 [transfer]
`
	if got := elaborated(heapSwapPlan()).Render(true); got != want {
		t.Fatalf("heap swap render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	want = `statement assign_x__
    source "x, _ = x ⊕ \"!\", \"a\" ⊕ \"b\""

    execute
        %t0 = eval Str x ⊕ "!" [shape=scalar] [yield=always] [owned]
        %t1 = eval Str "a" ⊕ "b" [shape=scalar] [yield=always] [owned]

    commit
        x : Str <- %t0 [move]
        _ <- %t1
        drop %t1
        drop x [replaced]
`
	if got := elaborated(replacePlan()).Render(true); got != want {
		t.Fatalf("replace render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	// The concise view omits every derived decision.
	want = `statement assign_x__
    source "x, _ = x ⊕ \"!\", \"a\" ⊕ \"b\""

    execute
        %t0 = eval Str x ⊕ "!"
        %t1 = eval Str "a" ⊕ "b"

    commit
        x <- %t0
        _ <- %t1
`
	if got := elaborated(replacePlan()).Render(false); got != want {
		t.Fatalf("replace concise render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// Plan §12: control characters and Unicode line breaks inside string
// literals never break the one-operation-per-line format, at the root or
// nested under an operator; ordinary non-ASCII stays raw.
func TestRenderEscapesControls(t *testing.T) {
	multi := strLit("a\nb\tc\rd\x01e")
	cases := []struct {
		expr ast.Expression
		want string
	}{
		{multi, `%t0 = eval Str "a\nb\tc\rd\x01e"`},
		{concat(multi, strLit("z")), `%t0 = eval Str "a\nb\tc\rd\x01e" ⊕ "z"`},
		{strLit("π\u0085x\u2028y\u2029z\u009f"), `%t0 = eval Str "π\u0085x\u2028y\u2029z\u009f"`},
	}
	for _, tc := range cases {
		p := swapPlan()
		p.Evals = p.Evals[:1]
		p.Evals[0].Expr = tc.expr
		p.Evals[0].Slots[0].Type = testType("Str")
		p.Commit = p.Commit[:1]
		got := p.Render(false)
		if !strings.Contains(got, "        "+tc.want+"\n") || strings.Count(got, "\n") != 8 {
			t.Fatalf("render mismatch:\n%s", got)
		}
	}
}
