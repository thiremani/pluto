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

func call(name string, args ...ast.Expression) ast.Expression {
	return &ast.CallExpression{Token: token.Token{Type: token.LPAREN, Literal: "("}, Function: ident(name).(*ast.Identifier), Arguments: args}
}

func concat(left, right ast.Expression) ast.Expression {
	return &ast.InfixExpression{Token: token.Token{Type: token.OPERATOR, Literal: "⊕"}, Left: left, Operator: "⊕", Right: right}
}

// read is `arr[index]`, a checked access.
func read(arr string, index int64) ast.Expression {
	return &ast.ArrayRangeExpression{Token: token.Token{Type: token.LBRACK, Literal: "["}, Array: ident(arr), Range: intLit(index)}
}

func unmanagedSlot(t Type) Slot { return Slot{Type: t} }
func ownedSlot(t Type) Slot     { return Slot{Type: t, Ownership: Owned} }
func borrowedSlot(t Type, owner string) Slot {
	return Slot{Type: t, Ownership: Borrowed, Owner: owner}
}

func local(name string, t Type) Target { return Target{Kind: LocalTarget, Name: name, Type: t} }

// heapLocal is an existing owning binding holding a heap value.
func heapLocal(name string, t Type) Target {
	return Target{Kind: LocalTarget, Name: name, Type: t, TypeOwnsHeap: true, HoldsHeap: true}
}

// widenedLocal is an existing binding declared non-owning that nevertheless
// holds a heap value a previous transfer left in it.
func widenedLocal(name string, t Type) Target {
	return Target{Kind: LocalTarget, Name: name, Type: t, HoldsHeap: true}
}

// heapOutput is a function output of an owning type, holding its slot's seed
// or an earlier write.
func heapOutput(name string, t Type) Target {
	return Target{Kind: OutputTarget, Name: name, Type: t, TypeOwnsHeap: true, HoldsHeap: true}
}

// joinPlan is `res = a ⊕ b` in the body of Join, before elaboration.
func joinPlan() *AssignPlan {
	str := testType("Str")
	return &AssignPlan{
		Head:   Head{Label: "assign res", Func: "Str = Join(Str, Str)", Source: "res = a ⊕ b"},
		Evals:  []*Eval{{Result: 0, Expr: concat(ident("a"), ident("b")), Slots: []Slot{ownedSlot(str)}}},
		Commit: []Mapping{{Target: heapOutput("res", str), Outcome: OutcomeRef{Outcome: 0}}},
	}
}

// printPlan is `"x: -x", s, Pair(n)` at the script root: a literal, a
// borrowed read, and a call that yields an unmanaged slot and an owned one.
func printPlan() *PrintPlan {
	i64, str := testType("I64"), testType("Str")
	return &PrintPlan{
		Head: Head{Label: "print", Source: `"x: -x", s, Pair(n)`},
		Evals: []*Eval{
			{Result: 0, Expr: ident("s"), Slots: []Slot{borrowedSlot(str, "s")}},
			{Result: 1, Expr: call("Pair", ident("n")), Slots: []Slot{unmanagedSlot(i64), ownedSlot(str)}},
		},
		Args: []PrintArg{{Literal: strLit("x: -x").(*ast.StringLiteral)}, {Outcome: 0}, {Outcome: 1}},
	}
}

func swapPlan() *AssignPlan {
	return &AssignPlan{
		Head: Head{Label: "assign a, b", Source: "a, b = b, a"},
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
		Head: Head{Label: "assign a, b", Source: "a, b = b, a"},
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
		Head: Head{Label: "assign x, _", Source: `x, _ = x ⊕ "!", "a" ⊕ "b"`},
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

// skipPlan is `kept, fresh, _ = sarr[9], sarr[0], sarr[1]` over heap strings,
// before elaboration: each read may skip, into a binding that holds a value,
// a fresh binding, and a discard.
func skipPlan() *AssignPlan {
	str := testType("Str")
	return &AssignPlan{
		Head: Head{Label: "assign kept, fresh, _", Source: "kept, fresh, _ = sarr[9], sarr[0], sarr[1]"},
		Evals: []*Eval{
			{Result: 0, Expr: read("sarr", 9), Slots: []Slot{ownedSlot(str)}, OnOOB: SkipOutcome},
			{Result: 1, Expr: read("sarr", 0), Slots: []Slot{ownedSlot(str)}, OnOOB: SkipOutcome},
			{Result: 2, Expr: read("sarr", 1), Slots: []Slot{ownedSlot(str)}, OnOOB: SkipOutcome},
		},
		Commit: []Mapping{
			{Target: heapLocal("kept", str), Outcome: OutcomeRef{Outcome: 0}, OnSkip: KeepOnSkip},
			{Target: Target{Kind: LocalTarget, Name: "fresh", Type: str, TypeOwnsHeap: true, Fresh: true}, Outcome: OutcomeRef{Outcome: 1}, OnSkip: ZeroOnSkip},
			{Target: Target{Kind: DiscardTarget}, Outcome: OutcomeRef{Outcome: 2}},
		},
	}
}

func TestValidateAccepts(t *testing.T) {
	heapSwap := heapSwapPlan()
	Elaborate(heapSwap)
	replace := replacePlan()
	Elaborate(replace)
	for name, p := range map[string]*AssignPlan{
		"scalar swap":    swapPlan(),
		"heap swap":      heapSwap,
		"replace + drop": replace,
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
		{"heap swap moves both old values, copies nothing", heapSwapPlan(), []Transfer{Move, Move}, nil},
		{"owned replacement moves and releases the old value", replacePlan(), []Transfer{Move, Store},
			[]Drop{{Kind: DropOutcome, Outcome: OutcomeRef{Outcome: 1}}, {Kind: DropReplaced, Target: "x"}}},
		{"duplicate source: first takes, second copies", &AssignPlan{
			Head: Head{Label: "assign d1, d2", Source: "d1, d2 = d1, d1"},
			Evals: []*Eval{
				{Result: 0, Expr: ident("d1"), Slots: []Slot{borrowedSlot(str, "d1")}},
				{Result: 1, Expr: ident("d1"), Slots: []Slot{borrowedSlot(str, "d1")}},
			},
			Commit: []Mapping{
				{Target: heapLocal("d1", str), Outcome: OutcomeRef{Outcome: 0}},
				{Target: heapLocal("d2", str), Outcome: OutcomeRef{Outcome: 1}},
			},
		}, []Transfer{Move, Copy}, []Drop{{Kind: DropReplaced, Target: "d2"}}},
		{"borrow of a surviving owner copies", &AssignPlan{
			Head:   Head{Label: "assign t", Source: "t = s"},
			Evals:  []*Eval{{Result: 0, Expr: ident("s"), Slots: []Slot{borrowedSlot(str, "s")}}},
			Commit: []Mapping{{Target: Target{Kind: LocalTarget, Name: "t", Type: str, TypeOwnsHeap: true, Fresh: true}, Outcome: OutcomeRef{Outcome: 0}}},
		}, []Transfer{Copy}, nil},
		{"unmanaged into an owning target is copied", &AssignPlan{
			Head:   Head{Label: "assign s", Source: `s = "hi"`},
			Evals:  []*Eval{{Result: 0, Expr: strLit("hi"), Slots: []Slot{unmanagedSlot(str)}}},
			Commit: []Mapping{{Target: heapLocal("s", str), Outcome: OutcomeRef{Outcome: 0}}},
		}, []Transfer{Copy}, []Drop{{Kind: DropReplaced, Target: "s"}}},
		{"heap transfer into a non-owning fresh target is legal", &AssignPlan{
			Head:   Head{Label: "assign other", Source: "other = text"},
			Evals:  []*Eval{{Result: 0, Expr: ident("text"), Slots: []Slot{borrowedSlot(str, "text")}}},
			Commit: []Mapping{{Target: Target{Kind: LocalTarget, Name: "other", Type: str, Fresh: true}, Outcome: OutcomeRef{Outcome: 0}}},
		}, []Transfer{Copy}, nil},
		{"widened binding releases its held value on a plain store", &AssignPlan{
			Head:   Head{Label: "assign other", Source: `other = "new"`},
			Evals:  []*Eval{{Result: 0, Expr: strLit("new"), Slots: []Slot{unmanagedSlot(str)}}},
			Commit: []Mapping{{Target: widenedLocal("other", str), Outcome: OutcomeRef{Outcome: 0}}},
		}, []Transfer{Store}, []Drop{{Kind: DropReplaced, Target: "other"}}},
		{"widened owner is taken by a moved borrow", &AssignPlan{
			Head: Head{Label: "assign a, other", Source: "a, other = other, a"},
			Evals: []*Eval{
				{Result: 0, Expr: ident("other"), Slots: []Slot{borrowedSlot(str, "other")}},
				{Result: 1, Expr: ident("a"), Slots: []Slot{borrowedSlot(str, "a")}},
			},
			Commit: []Mapping{
				{Target: heapLocal("a", str), Outcome: OutcomeRef{Outcome: 0}},
				{Target: widenedLocal("other", str), Outcome: OutcomeRef{Outcome: 1}},
			},
		}, []Transfer{Move, Move}, nil},
		{"discarded borrow releases nothing", &AssignPlan{
			Head:   Head{Label: "assign _", Source: "_ = h"},
			Evals:  []*Eval{{Result: 0, Expr: ident("h"), Slots: []Slot{borrowedSlot(str, "h")}}},
			Commit: []Mapping{{Target: Target{Kind: DiscardTarget}, Outcome: OutcomeRef{Outcome: 0}}},
		}, []Transfer{Store}, nil},
		{"output releases the value it held, as a local does", joinPlan(), []Transfer{Move},
			[]Drop{{Kind: DropReplaced, Target: "res"}}},
		{"swapped outputs take each other's old values", &AssignPlan{
			Head: Head{Label: "assign a, b", Func: "Str, Str = Swap(Str, Str)", Source: "a, b = b, a"},
			Evals: []*Eval{
				{Result: 0, Expr: ident("b"), Slots: []Slot{borrowedSlot(str, "b")}},
				{Result: 1, Expr: ident("a"), Slots: []Slot{borrowedSlot(str, "a")}},
			},
			Commit: []Mapping{
				{Target: heapOutput("a", str), Outcome: OutcomeRef{Outcome: 0}},
				{Target: heapOutput("b", str), Outcome: OutcomeRef{Outcome: 1}},
			},
		}, []Transfer{Move, Move}, nil},
		{"a skipped owned outcome is released, a discarded one either way, an old value where replaced", skipPlan(),
			[]Transfer{Move, Move, Store}, []Drop{
				{Kind: DropSkipped, Outcome: OutcomeRef{Outcome: 0}},
				{Kind: DropSkipped, Outcome: OutcomeRef{Outcome: 1}},
				{Kind: DropOutcome, Outcome: OutcomeRef{Outcome: 2}},
				{Kind: DropReplaced, Target: "kept"},
			}},
		{"a borrow copies when its owner's replacement may skip", &AssignPlan{
			Head: Head{Label: "assign a, b", Source: "a, b = b, sarr[9]"},
			Evals: []*Eval{
				{Result: 0, Expr: ident("b"), Slots: []Slot{borrowedSlot(str, "b")}},
				{Result: 1, Expr: read("sarr", 9), Slots: []Slot{ownedSlot(str)}, OnOOB: SkipOutcome},
			},
			Commit: []Mapping{
				{Target: heapLocal("a", str), Outcome: OutcomeRef{Outcome: 0}},
				{Target: heapLocal("b", str), Outcome: OutcomeRef{Outcome: 1}, OnSkip: KeepOnSkip},
			},
		}, []Transfer{Copy, Move}, []Drop{
			{Kind: DropSkipped, Outcome: OutcomeRef{Outcome: 1}},
			{Kind: DropReplaced, Target: "a"},
			{Kind: DropReplaced, Target: "b"},
		}},
		// No source statement builds a borrowed outcome that may skip yet.
		{"a borrow that may skip copies though its owner is replaced", &AssignPlan{
			Head: Head{Label: "assign a, b", Source: `a, b = views[9], "new"`},
			Evals: []*Eval{
				{Result: 0, Expr: read("views", 9), Slots: []Slot{borrowedSlot(str, "b")}, OnOOB: SkipOutcome},
				{Result: 1, Expr: strLit("new"), Slots: []Slot{unmanagedSlot(str)}},
			},
			Commit: []Mapping{
				{Target: heapLocal("a", str), Outcome: OutcomeRef{Outcome: 0}, OnSkip: KeepOnSkip},
				{Target: heapLocal("b", str), Outcome: OutcomeRef{Outcome: 1}},
			},
		}, []Transfer{Copy, Copy}, []Drop{
			{Kind: DropReplaced, Target: "a"},
			{Kind: DropReplaced, Target: "b"},
		}},
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
		{"UnnamedLocal", func(p *AssignPlan) { p.Commit[0].Target.Name = "" }, "binding target has no name"},
		{"OutputOutsideFunction", func(p *AssignPlan) { p.Commit[0].Target.Kind = OutputTarget }, "output target a outside a function body"},
		{"FreshOutput", func(p *AssignPlan) {
			p.Func = "I64, I64 = Swap(I64, I64)"
			p.Commit[0].Target.Kind = OutputTarget
			p.Commit[0].Target.Fresh = true
		}, "output target a is fresh"},
		{"TypeMismatch", func(p *AssignPlan) { p.Evals[0].Slots[0].Type = testType("F64") }, "target I64 a mapped to incompatible outcome %t0 slot 0 of type F64"},
		{"NamedDiscard", func(p *AssignPlan) { p.Commit[0].Target = Target{Kind: DiscardTarget, Name: "x"} }, "discard target carries"},
		{"TypedDiscard", func(p *AssignPlan) { p.Commit[0].Target = Target{Kind: DiscardTarget, Type: testType("I64")} }, "discard target carries"},
		{"OwningDiscard", func(p *AssignPlan) { p.Commit[0].Target = Target{Kind: DiscardTarget, TypeOwnsHeap: true} }, "discard target carries"},
		{"HoldingDiscard", func(p *AssignPlan) { p.Commit[0].Target = Target{Kind: DiscardTarget, HoldsHeap: true} }, "discard target carries"},
		{"FreshTargetHolds", func(p *AssignPlan) {
			p.Commit[0].Target.Fresh = true
			p.Commit[0].Target.HoldsHeap = true
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
		{"UnmanagedIntoOwnerNotCopied", func(p *AssignPlan) { p.Commit[0].Target.TypeOwnsHeap = true }, "uses transfer store; ownership requires copy"},
		{"OwnedNotMoved", func(p *AssignPlan) { p.Evals[0].Slots[0].Ownership = Owned }, "uses transfer store; ownership requires move"},
		{"BorrowedNotCopied", func(p *AssignPlan) { p.Evals[0].Slots[0] = borrowedSlot(testType("I64"), "b") }, "uses transfer store; ownership requires copy"},
		{"MovedBorrowOfSurvivingOwner", func(p *AssignPlan) {
			p.Evals[0].Slots[0] = borrowedSlot(testType("I64"), "z")
			p.Commit[0].Transfer = Move
		}, "target a takes z's old value, but z is not replaced in this group"},
		{"MovedBorrowOfFreshOwner", func(p *AssignPlan) {
			p.Evals[0].Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfer = Move
			p.Commit[1].Target.TypeOwnsHeap = true
			p.Commit[1].Target.Fresh = true
			p.Commit[1].Transfer = Copy
		}, "b is not replaced in this group"},
		{"MovedBorrowOfOwnerHoldingNothing", func(p *AssignPlan) {
			p.Evals[0].Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfer = Move
			p.Commit[1].Target.TypeOwnsHeap = true
			p.Commit[1].Transfer = Copy
		}, "b is not replaced in this group"},
		{"OwnerTakenTwice", func(p *AssignPlan) {
			p.Evals[0].Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Evals[1].Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfer = Move
			p.Commit[1].Transfer = Move
			p.Commit[1].Target.HoldsHeap = true
		}, "b's old value is taken by 2 targets"},
		{"ReplacedNeverReleased", func(p *AssignPlan) { p.Commit[0].Target.HoldsHeap = true }, "a's old value is neither taken nor released"},
		{"ReplacedTakenAndDropped", func(p *AssignPlan) {
			p.Evals[0].Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfer = Move
			p.Commit[1].Target.HoldsHeap = true
			p.Drops = []Drop{{Kind: DropReplaced, Target: "b"}}
		}, "b's old value is both taken and dropped"},
		{"DropOfUnreplacedTarget", func(p *AssignPlan) { p.Drops = []Drop{{Kind: DropReplaced, Target: "a"}} }, "a holds no replaced value"},
		{"DropOfFreshTarget", func(p *AssignPlan) {
			p.Commit[0].Target.TypeOwnsHeap = true
			p.Commit[0].Target.Fresh = true
			p.Commit[0].Transfer = Copy
			p.Drops = []Drop{{Kind: DropReplaced, Target: "a"}}
		}, "a holds no replaced value"},
		{"ReplacedDroppedTwice", func(p *AssignPlan) {
			p.Commit[0].Target.HoldsHeap = true
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
		{"AccessWithoutScope", func(p *AssignPlan) { p.Evals[0].Expr = read("arr", 0) }, "eval %t0 holds a checked access with no out-of-bounds scope"},
		{"ScopeWithoutAccess", func(p *AssignPlan) { p.Evals[0].OnOOB = SkipOutcome }, "eval %t0 has an out-of-bounds scope and no checked access"},
		{"AccessInLiteralCell", func(p *AssignPlan) {
			p.Evals[0].Expr = &ast.ArrayLiteral{Rows: [][]ast.Expression{{read("arr", 0)}}}
		}, "eval %t0 holds a checked access in a literal cell"},
		{"SkipWithoutPolicy", func(p *AssignPlan) {
			p.Evals[0].Expr, p.Evals[0].OnOOB = read("arr", 0), SkipOutcome
		}, "target a <- %t0 slot 0 has skip policy none; its outcome and target require keep"},
		{"KeepOnFreshTarget", func(p *AssignPlan) {
			p.Evals[0].Expr, p.Evals[0].OnOOB = read("arr", 0), SkipOutcome
			p.Commit[0].Target.Fresh = true
			p.Commit[0].OnSkip = KeepOnSkip
		}, "has skip policy keep; its outcome and target require zero"},
		{"PolicyOnCertainOutcome", func(p *AssignPlan) { p.Commit[0].OnSkip = KeepOnSkip }, "has skip policy keep; its outcome and target require none"},
		{"PolicyOnDiscard", func(p *AssignPlan) {
			p.Evals[0].Expr, p.Evals[0].OnOOB = read("arr", 0), SkipOutcome
			p.Commit[0].Target = Target{Kind: DiscardTarget}
			p.Commit[0].OnSkip = KeepOnSkip
		}, "target _ <- %t0 slot 0 has skip policy keep; its outcome and target require none"},
		{"MovedBorrowOfOwnerThatMaySkip", func(p *AssignPlan) {
			p.Evals[0].Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfer = Move
			p.Evals[1].Expr, p.Evals[1].OnOOB = read("arr", 0), SkipOutcome
			p.Commit[1].Target.HoldsHeap = true
			p.Commit[1].OnSkip = KeepOnSkip
		}, "target a takes b's old value, but one of the two mappings may skip without the other"},
		{"MovedBorrowThatMaySkip", func(p *AssignPlan) {
			p.Evals[0].Expr, p.Evals[0].OnOOB = read("arr", 0), SkipOutcome
			p.Evals[0].Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfer = Move
			p.Commit[0].OnSkip = KeepOnSkip
			p.Commit[1].Target.HoldsHeap = true
		}, "target a takes b's old value, but one of the two mappings may skip without the other"},
		{"SkippedOwnedNeverReleased", func(p *AssignPlan) {
			p.Evals[0].Expr, p.Evals[0].OnOOB = read("arr", 0), SkipOutcome
			p.Evals[0].Slots[0].Ownership = Owned
			p.Commit[0].Transfer = Move
			p.Commit[0].OnSkip = KeepOnSkip
		}, "owned outcome %t0 slot 0 is never released where it skips"},
		{"SkipDropOfCertainOutcome", func(p *AssignPlan) {
			p.Evals[0].Slots[0].Ownership = Owned
			p.Commit[0].Transfer = Move
			p.Drops = []Drop{{Kind: DropSkipped, Outcome: OutcomeRef{Outcome: 0}}}
		}, "skip-path drop of %t0 slot 0"},
		{"SkippedDroppedTwice", func(p *AssignPlan) {
			p.Evals[0].Expr, p.Evals[0].OnOOB = read("arr", 0), SkipOutcome
			p.Evals[0].Slots[0].Ownership = Owned
			p.Commit[0].Transfer = Move
			p.Commit[0].OnSkip = KeepOnSkip
			p.Drops = []Drop{{Kind: DropSkipped, Outcome: OutcomeRef{Outcome: 0}}, {Kind: DropSkipped, Outcome: OutcomeRef{Outcome: 0}}}
		}, "skipped %t0 slot 0 dropped twice"},
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

// Plan §3, §14.12: the invocation takes the outcomes in evaluation order,
// each exactly once. A print of literals alone has no evals and is valid.
func TestValidatePrint(t *testing.T) {
	if err := ValidatePrint(printPlan()); err != nil {
		t.Fatalf("valid print plan rejected: %v", err)
	}
	literals := &PrintPlan{
		Head: Head{Label: "print", Source: `"hello"`},
		Args: []PrintArg{{Literal: strLit("hello").(*ast.StringLiteral)}},
	}
	if err := ValidatePrint(literals); err != nil {
		t.Fatalf("print of a literal alone rejected: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*PrintPlan)
		wantErr string
	}{
		{"NoSource", func(p *PrintPlan) { p.Source = "" }, "no source"},
		{"NoArguments", func(p *PrintPlan) { p.Args = nil }, "has no arguments"},
		{"SparseResultID", func(p *PrintPlan) { p.Evals[1].Result = 5 }, "dense"},
		{"OutOfOrder", func(p *PrintPlan) { p.Args[1].Outcome, p.Args[2].Outcome = 1, 0 }, "print takes %t1 where %t0 is next in evaluation order"},
		{"Unprinted", func(p *PrintPlan) { p.Args = p.Args[:2] }, "print takes 1 of 2 outcomes"},
		{"ExtraOutcome", func(p *PrintPlan) { p.Args = append(p.Args, PrintArg{Outcome: 2}) }, "print takes 3 of 2 outcomes"},
		{"MaySkip", func(p *PrintPlan) {
			p.Evals[0].Expr, p.Evals[0].OnOOB = read("arr", 0), SkipOutcome
		}, "print takes %t0, which may skip"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := printPlan()
			tc.mutate(p)
			err := ValidatePrint(p)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// Plan §3, §12: a print plan has no commit. Its invocation closes the execute
// region, prints a literal as written and every slot of an outcome, and in
// the expanded view marks each owned slot it takes.
func TestRenderPrint(t *testing.T) {
	p := printPlan()
	want := `print
    source "\"x: -x\", s, Pair(n)"

    execute
        %t0 = eval Str s
        %t1 = eval I64, Str Pair(n)
        print "x: -x", %t0, %t1#0, %t1#1
`
	if got := p.Render(false); got != want {
		t.Fatalf("print render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	wantExpanded := `print
    source "\"x: -x\", s, Pair(n)"

    execute
        %t0 = eval Str [borrowed=s] s
        %t1 = eval I64, Str [owned] Pair(n)
        print "x: -x", %t0, %t1#0, %t1#1 [move]
`
	if got := p.Render(true); got != wantExpanded {
		t.Fatalf("print expanded render mismatch:\ngot:\n%s\nwant:\n%s", got, wantExpanded)
	}
}

// Plan §12: text form, concise view.
func TestRenderConcise(t *testing.T) {
	p := &AssignPlan{
		Head: Head{Label: "assign x, _", Source: "x, _ = 5, 7"},
		Evals: []*Eval{
			{Result: 0, Expr: intLit(5), Slots: []Slot{unmanagedSlot(testType("I64"))}},
			{Result: 1, Expr: intLit(7), Slots: []Slot{unmanagedSlot(testType("I64"))}},
		},
		Commit: []Mapping{
			{Target: local("x", testType("I64")), Outcome: OutcomeRef{Outcome: 0}},
			{Target: Target{Kind: DiscardTarget}, Outcome: OutcomeRef{Outcome: 1}},
		},
	}
	want := `assign x, _
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
		Head: Head{Label: "assign a, b", Source: "a, b = Pair(x)"},
		Evals: []*Eval{
			{Result: 0, Expr: call("Pair", ident("x")), Slots: []Slot{unmanagedSlot(testType("I64")), ownedSlot(testType("Str"))}},
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
	want := `assign a, b
    source "a, b = Pair(x)"

    execute
        %t0 = eval I64, Str Pair(x)

    commit
        a <- %t0#0
        b <- %t0#1
`
	if got := p.Render(false); got != want {
		t.Fatalf("multi-output render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	wantExpanded := `assign a, b
    source "a, b = Pair(x)"

    execute
        %t0 = eval I64, Str [owned] Pair(x)

    commit
        I64 a <- %t0#0
        Str b <- %t0#1 [move]
        drop b [old]
`
	if got := p.Render(true); got != wantExpanded {
		t.Fatalf("multi-output expanded render mismatch:\ngot:\n%s\nwant:\n%s", got, wantExpanded)
	}
}

// Plan §6, §12: a function body's plan sits one level in, under the header
// its body opens with, and an output mapping is spelled `output`, before the
// type in the expanded view.
func TestRenderFunctionOutput(t *testing.T) {
	p := joinPlan()
	Elaborate(p)
	if got, want := FuncHeader(p.Func), "function Str = Join(Str, Str)\n"; got != want {
		t.Fatalf("function header mismatch: got %q, want %q", got, want)
	}

	want := `    assign res
        source "res = a ⊕ b"

        execute
            %t0 = eval Str a ⊕ b

        commit
            output res <- %t0
`
	if got := p.Render(false); got != want {
		t.Fatalf("function output render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	wantExpanded := `    assign res
        source "res = a ⊕ b"

        execute
            %t0 = eval Str [owned] a ⊕ b

        commit
            output Str res <- %t0 [move]
            drop res [old]
`
	if got := p.Render(true); got != wantExpanded {
		t.Fatalf("function output expanded render mismatch:\ngot:\n%s\nwant:\n%s", got, wantExpanded)
	}
}

// Plan §12: the renderer rejects an unsupported node anywhere in the tree,
// not only at the root.
func TestRenderRejectsUnsupportedNode(t *testing.T) {
	person := &ast.StructLiteral{Token: token.Token{Type: token.IDENT, Literal: "Person"}, Row: []ast.Expression{intLit(1)}}
	tests := []struct {
		name string
		expr ast.Expression
		want string
	}{
		{"root", person, "pir: no renderer for *ast.StructLiteral"},
		{"under infix", &ast.InfixExpression{Left: ident("a"), Operator: "+", Right: person}, "pir: no renderer for *ast.StructLiteral"},
		{"two edges deep", &ast.InfixExpression{Left: ident("a"), Operator: "+", Right: &ast.PrefixExpression{Operator: "-", Right: person}}, "pir: no renderer for *ast.StructLiteral"},
		{"in an array cell", &ast.ArrayLiteral{Rows: [][]ast.Expression{{intLit(1), person}}}, "pir: no renderer for *ast.StructLiteral"},
		{"as a call argument", call("f", person), "pir: no renderer for *ast.StructLiteral"},
		{"as a read's index", &ast.ArrayRangeExpression{Array: ident("arr"), Range: person}, "pir: no renderer for *ast.StructLiteral"},
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

// Plan §12: expanded view adds target types; an unmanaged slot carries no
// ownership tag and an unmanaged store no transfer annotation.
func TestRenderExpanded(t *testing.T) {
	p := swapPlan()
	got := p.Render(true)
	want := `assign a, b
    source "a, b = b, a"

    execute
        %t0 = eval I64 b
        %t1 = eval I64 a

    commit
        I64 a <- %t0
        I64 b <- %t1
`
	if got != want {
		t.Fatalf("expanded render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// Plan §8, §17: copies are loud — a heap swap shows two transfers and no
// copy or release; an owned replacement shows the move, the discard's
// release, and the replaced value's release after the mappings.
func TestRenderExpandedOwnership(t *testing.T) {
	want := `assign a, b
    source "a, b = b, a"

    execute
        %t0 = eval Str [borrowed=b] b
        %t1 = eval Str [borrowed=a] a

    commit
        Str a <- %t0 [move]
        Str b <- %t1 [move]
`
	heapSwap := heapSwapPlan()
	Elaborate(heapSwap)
	if got := heapSwap.Render(true); got != want {
		t.Fatalf("heap swap render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	want = `assign x, _
    source "x, _ = x ⊕ \"!\", \"a\" ⊕ \"b\""

    execute
        %t0 = eval Str [owned] x ⊕ "!"
        %t1 = eval Str [owned] "a" ⊕ "b"

    commit
        Str x <- %t0 [move]
        _ <- %t1
        drop %t1
        drop x [old]
`
	replace := replacePlan()
	Elaborate(replace)
	if got := replace.Render(true); got != want {
		t.Fatalf("replace render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	// The concise view omits every derived decision.
	want = `assign x, _
    source "x, _ = x ⊕ \"!\", \"a\" ⊕ \"b\""

    execute
        %t0 = eval Str x ⊕ "!"
        %t1 = eval Str "a" ⊕ "b"

    commit
        x <- %t0
        _ <- %t1
`
	if got := replace.Render(false); got != want {
		t.Fatalf("replace concise render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// Plan §9, §12: an eval that may skip names its out-of-bounds scope, each
// binding it maps to says what it holds on a skip, in both views, and the
// expanded view lists the releases in the order they run.
func TestRenderSkip(t *testing.T) {
	p := skipPlan()
	Elaborate(p)
	want := `assign kept, fresh, _
    source "kept, fresh, _ = sarr[9], sarr[0], sarr[1]"

    execute
        %t0 = eval Str sarr[9] [on-oob=skip]
        %t1 = eval Str sarr[0] [on-oob=skip]
        %t2 = eval Str sarr[1] [on-oob=skip]

    commit
        kept <- %t0 [on-skip=keep]
        fresh <- %t1 [on-skip=zero]
        _ <- %t2
`
	if got := p.Render(false); got != want {
		t.Fatalf("concise render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	want = `assign kept, fresh, _
    source "kept, fresh, _ = sarr[9], sarr[0], sarr[1]"

    execute
        %t0 = eval Str [owned] sarr[9] [on-oob=skip]
        %t1 = eval Str [owned] sarr[0] [on-oob=skip]
        %t2 = eval Str [owned] sarr[1] [on-oob=skip]

    commit
        Str kept <- %t0 [move] [on-skip=keep]
        Str fresh <- %t1 [move] [on-skip=zero]
        _ <- %t2
        drop %t0 [skipped]
        drop %t1 [skipped]
        drop %t2
        drop kept [old]
`
	if got := p.Render(true); got != want {
		t.Fatalf("expanded render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// Plan §12: control characters and Unicode line breaks inside string
// literals never break the one-operation-per-line format, at the root or
// nested under an operator; ordinary non-ASCII stays raw.
func TestRenderEscapesControls(t *testing.T) {
	multi := strLit("a\nb\tc\rd\x01e\x7f")
	cases := []struct {
		expr ast.Expression
		want string
	}{
		{multi, `%t0 = eval Str "a\nb\tc\rd\x01e\x7f"`},
		{concat(multi, strLit("z")), `%t0 = eval Str "a\nb\tc\rd\x01e\x7f" ⊕ "z"`},
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

// Plan §12: a slot's ownership prints beside its own type, so two layouts
// over identical displayed types render differently.
func TestRenderMultiSlotOwnershipPositions(t *testing.T) {
	str := testType("Str")
	first := renderDiscardedPair(t, []Slot{unmanagedSlot(str), borrowedSlot(str, "x")})
	second := renderDiscardedPair(t, []Slot{borrowedSlot(str, "x"), unmanagedSlot(str)})
	if !strings.Contains(first, "eval Str, Str [borrowed=x] pair\n") {
		t.Fatalf("first layout lost its positions:\n%s", first)
	}
	if !strings.Contains(second, "eval Str [borrowed=x], Str pair\n") {
		t.Fatalf("second layout lost its positions:\n%s", second)
	}
	if first == second {
		t.Fatal("swapped ownership layouts rendered identically")
	}
}

// renderDiscardedPair renders `_, _ = pair` with the given two-slot ownership
// layout in the expanded view.
func renderDiscardedPair(t *testing.T, slots []Slot) string {
	t.Helper()
	p := &AssignPlan{
		Head:  Head{Label: "assign _, _", Source: "_, _ = pair"},
		Evals: []*Eval{{Result: 0, Expr: ident("pair"), Slots: slots}},
		Commit: []Mapping{
			{Target: Target{Kind: DiscardTarget}, Outcome: OutcomeRef{Outcome: 0, Slot: 0}},
			{Target: Target{Kind: DiscardTarget}, Outcome: OutcomeRef{Outcome: 0, Slot: 1}},
		},
	}
	Elaborate(p)
	if err := Validate(p, sameSpelling); err != nil {
		t.Fatalf("plan rejected: %v", err)
	}
	return p.Render(true)
}
