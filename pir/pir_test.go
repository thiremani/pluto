package pir

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
)

type testType string

func (t testType) String() string { return string(t) }

type unknownNode OutcomeID

func (unknownNode) node() {}

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

func greater(left, right ast.Expression) *ast.InfixExpression {
	return &ast.InfixExpression{Token: token.Token{Type: token.OPERATOR, Literal: ">"}, Left: left, Operator: ">", Right: right}
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
		Head:    Head{Label: "assign res", Func: "Str = Join(Str, Str)", Source: "res = a ⊕ b"},
		Execute: []Node{&Eval{Result: 0, Expr: concat(ident("a"), ident("b")), Slots: []Slot{ownedSlot(str)}}},
		Commit:  []Mapping{{Target: heapOutput("res", str), Outcome: OutcomeRef{Outcome: 0}}},
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
		Execute: []Node{
			&Eval{Result: 0, Expr: ident("b"), Slots: []Slot{unmanagedSlot(testType("I64"))}},
			&Eval{Result: 1, Expr: ident("a"), Slots: []Slot{unmanagedSlot(testType("I64"))}},
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
		Execute: []Node{
			&Eval{Result: 0, Expr: ident("b"), Slots: []Slot{borrowedSlot(str, "b")}},
			&Eval{Result: 1, Expr: ident("a"), Slots: []Slot{borrowedSlot(str, "a")}},
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
		Execute: []Node{
			&Eval{Result: 0, Expr: concat(ident("x"), strLit("!")), Slots: []Slot{ownedSlot(str)}},
			&Eval{Result: 1, Expr: concat(strLit("a"), strLit("b")), Slots: []Slot{ownedSlot(str)}},
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
		Execute: []Node{
			&Eval{Result: 0, Expr: read("sarr", 9), Slots: []Slot{ownedSlot(str)}, Checked: true},
			&Eval{Result: 1, Expr: read("sarr", 0), Slots: []Slot{ownedSlot(str)}, Checked: true},
			&Eval{Result: 2, Expr: read("sarr", 1), Slots: []Slot{ownedSlot(str)}, Checked: true},
		},
		Commit: []Mapping{
			{Target: heapLocal("kept", str), Outcome: OutcomeRef{Outcome: 0}, OnSkip: KeepOnSkip},
			{Target: Target{Kind: LocalTarget, Name: "fresh", Type: str, TypeOwnsHeap: true, Fresh: true}, Outcome: OutcomeRef{Outcome: 1}, OnSkip: ZeroOnSkip},
			{Target: Target{Kind: DiscardTarget}, Outcome: OutcomeRef{Outcome: 2}},
		},
	}
}

// firstPlan is `s = sarr[9] || h` over heap strings, before elaboration: an
// owned read that falls back to a borrow.
func firstPlan() *AssignPlan {
	str := testType("Str")
	return &AssignPlan{
		Head: Head{Label: "assign s", Source: "s = sarr[9] || h"},
		Execute: []Node{&First{Result: 0, Type: str, Sides: []Node{
			&Eval{Result: 1, Expr: read("sarr", 9), Slots: []Slot{ownedSlot(str)}, Checked: true},
			&Eval{Result: 2, Expr: ident("h"), Slots: []Slot{borrowedSlot(str, "h")}},
		}}},
		Commit: []Mapping{{Target: heapLocal("s", str), Outcome: OutcomeRef{Outcome: 0}}},
	}
}

// chainPlan is `n, z, m = 5, sarr[9] || sarr[8] || sarr[7], 7`, before
// elaboration: a first between two evals, whose last side may skip too, into
// a fresh binding.
func chainPlan() *AssignPlan {
	i64, str := testType("I64"), testType("Str")
	return &AssignPlan{
		Head: Head{Label: "assign n, z, m", Source: "n, z, m = 5, sarr[9] || sarr[8] || sarr[7], 7"},
		Execute: []Node{
			&Eval{Result: 0, Expr: intLit(5), Slots: []Slot{unmanagedSlot(i64)}},
			&First{Result: 1, Type: str, Sides: []Node{
				&Eval{Result: 2, Expr: read("sarr", 9), Slots: []Slot{ownedSlot(str)}, Checked: true},
				&Eval{Result: 3, Expr: read("sarr", 8), Slots: []Slot{ownedSlot(str)}, Checked: true},
				&Eval{Result: 4, Expr: read("sarr", 7), Slots: []Slot{ownedSlot(str)}, Checked: true},
			}},
			&Eval{Result: 5, Expr: intLit(7), Slots: []Slot{unmanagedSlot(i64)}},
		},
		Commit: []Mapping{
			{Target: local("n", i64), Outcome: OutcomeRef{Outcome: 0}},
			{Target: Target{Kind: LocalTarget, Name: "z", Type: str, TypeOwnsHeap: true, Fresh: true}, Outcome: OutcomeRef{Outcome: 1}, OnSkip: ZeroOnSkip},
			{Target: local("m", i64), Outcome: OutcomeRef{Outcome: 5}},
		},
	}
}

// firstCheckPlan is `s = sarr[9] > "a" || h` over heap strings, before
// elaboration: a check side whose failed read the fallback after it resolves,
// as a failed comparison is, falling back to a borrow.
func firstCheckPlan() *AssignPlan {
	str := testType("Str")
	return &AssignPlan{
		Head: Head{Label: "assign s", Source: `s = sarr[9] > "a" || h`},
		Execute: []Node{&First{Result: 0, Type: str, Sides: []Node{
			&Check{Result: 1, Expr: greater(read("sarr", 9), strLit("a")), Slots: []Slot{ownedSlot(str)}, Checked: true},
			&Eval{Result: 2, Expr: ident("h"), Slots: []Slot{borrowedSlot(str, "h")}},
		}}},
		Commit: []Mapping{{Target: heapLocal("s", str), Outcome: OutcomeRef{Outcome: 0}}},
	}
}

// firstLastPlan is `t = MkStr(a) > "a" && MkStr(b) || h` over heap strings,
// before elaboration: a side that is an && of its own, which binds no
// outcome, its final side supplying the value where its check held.
func firstLastPlan() *AssignPlan {
	str := testType("Str")
	return &AssignPlan{
		Head: Head{Label: "assign t", Source: `t = MkStr(a) > "a" && MkStr(b) || h`},
		Execute: []Node{&First{Result: 0, Type: str, Sides: []Node{
			&Last{Result: NoOutcome, Type: str, Sides: []Node{
				&Check{Result: 1, Expr: greater(call("MkStr", ident("a")), strLit("a")), Slots: []Slot{ownedSlot(str)}},
				&Eval{Result: 2, Expr: call("MkStr", ident("b")), Slots: []Slot{ownedSlot(str)}},
			}},
			&Eval{Result: 3, Expr: ident("h"), Slots: []Slot{borrowedSlot(str, "h")}},
		}}},
		Commit: []Mapping{{Target: heapLocal("t", str), Outcome: OutcomeRef{Outcome: 0}}},
	}
}

// firstLastsPlan is `s = sarr[9] > "a" && sarr[8] || sarr[7] > "b" && sarr[6]`
// over heap strings, before elaboration: each side is an && of its own whose
// check and final side both read, and only the first has a fallback after it.
func firstLastsPlan() *AssignPlan {
	str := testType("Str")
	return &AssignPlan{
		Head: Head{Label: "assign s", Source: `s = sarr[9] > "a" && sarr[8] || sarr[7] > "b" && sarr[6]`},
		Execute: []Node{&First{Result: 0, Type: str, Sides: []Node{
			&Last{Result: NoOutcome, Type: str, Sides: []Node{
				&Check{Result: 1, Expr: greater(read("sarr", 9), strLit("a")), Slots: []Slot{ownedSlot(str)}, Checked: true},
				&Eval{Result: 2, Expr: read("sarr", 8), Slots: []Slot{ownedSlot(str)}, Checked: true},
			}},
			&Last{Result: NoOutcome, Type: str, Sides: []Node{
				&Check{Result: 3, Expr: greater(read("sarr", 7), strLit("b")), Slots: []Slot{ownedSlot(str)}, Checked: true},
				&Eval{Result: 4, Expr: read("sarr", 6), Slots: []Slot{ownedSlot(str)}, Checked: true},
			}},
		}}},
		Commit: []Mapping{{Target: heapLocal("s", str), Outcome: OutcomeRef{Outcome: 0}, OnSkip: KeepOnSkip}},
	}
}

// gatePlan is `s = sarr[1] > "m" sarr[9]` over heap strings, before
// elaboration: the check reads a copy the gate only tests, and the gate's
// region reads another into a binding that holds a value.
func gatePlan() *AssignPlan {
	str := testType("Str")
	return &AssignPlan{
		Head: Head{Label: "assign s", Source: `s = sarr[1] > "m" sarr[9]`},
		Execute: []Node{
			&Check{Result: 0, Expr: greater(read("sarr", 1), strLit("m")), Slots: []Slot{ownedSlot(str)}, Checked: true},
			&Gate{Cond: 0, Body: []Node{
				&Eval{Result: 1, Expr: read("sarr", 9), Slots: []Slot{ownedSlot(str)}, Checked: true},
			}},
		},
		Commit: []Mapping{{Target: heapLocal("s", str), Outcome: OutcomeRef{Outcome: 1}, OnSkip: KeepOnSkip}},
	}
}

// lastGatePlan is `s = sarr[1] > "m" && t > "a" u` over heap strings, before
// elaboration: the gate takes a last of two checks, the first of which reads
// a copy, and the region reads a binding.
func lastGatePlan() *AssignPlan {
	str := testType("Str")
	return &AssignPlan{
		Head: Head{Label: "assign s", Source: `s = sarr[1] > "m" && t > "a" u`},
		Execute: []Node{
			&Last{Result: 0, Type: str, Sides: []Node{
				&Check{Result: 1, Expr: greater(read("sarr", 1), strLit("m")), Slots: []Slot{ownedSlot(str)}, Checked: true},
				&Check{Result: 2, Expr: greater(ident("t"), strLit("a")), Slots: []Slot{borrowedSlot(str, "t")}},
			}},
			&Gate{Cond: 0, Body: []Node{
				&Eval{Result: 3, Expr: ident("u"), Slots: []Slot{borrowedSlot(str, "u")}},
			}},
		},
		Commit: []Mapping{{Target: heapLocal("s", str), Outcome: OutcomeRef{Outcome: 3}, OnSkip: KeepOnSkip}},
	}
}

// lastValuePlan is `s = sarr[1] > "m" && sarr[9]` over heap strings, before
// elaboration: the check reads a copy it only tests, and the final side reads
// the copy the binding takes.
func lastValuePlan() *AssignPlan {
	str := testType("Str")
	return &AssignPlan{
		Head: Head{Label: "assign s", Source: `s = sarr[1] > "m" && sarr[9]`},
		Execute: []Node{
			&Last{Result: 0, Type: str, Sides: []Node{
				&Check{Result: 1, Expr: greater(read("sarr", 1), strLit("m")), Slots: []Slot{ownedSlot(str)}, Checked: true},
				&Eval{Result: 2, Expr: read("sarr", 9), Slots: []Slot{ownedSlot(str)}, Checked: true},
			}},
		},
		Commit: []Mapping{{Target: heapLocal("s", str), Outcome: OutcomeRef{Outcome: 0}, OnSkip: KeepOnSkip}},
	}
}

// checkPlan is `s = MkStr(n) > "a"` over heap strings, before elaboration: the
// binding takes the string the check built, where the comparison holds.
func checkPlan() *AssignPlan {
	str := testType("Str")
	return &AssignPlan{
		Head:    Head{Label: "assign s", Source: `s = MkStr(n) > "a"`},
		Execute: []Node{&Check{Result: 0, Expr: greater(call("MkStr", ident("n")), strLit("a")), Slots: []Slot{ownedSlot(str)}}},
		Commit:  []Mapping{{Target: heapLocal("s", str), Outcome: OutcomeRef{Outcome: 0}, OnSkip: KeepOnSkip}},
	}
}

// firstGatePlan is `s = sarr[9] > "a" || t > "b" u` over heap strings, before
// elaboration: the gate takes a first of two checks, the first of which reads
// a copy, and the region reads a binding.
func firstGatePlan() *AssignPlan {
	str := testType("Str")
	return &AssignPlan{
		Head: Head{Label: "assign s", Source: `s = sarr[9] > "a" || t > "b" u`},
		Execute: []Node{
			&First{Result: 0, Type: str, Sides: []Node{
				&Check{Result: 1, Expr: greater(read("sarr", 9), strLit("a")), Slots: []Slot{ownedSlot(str)}, Checked: true},
				&Check{Result: 2, Expr: greater(ident("t"), strLit("b")), Slots: []Slot{borrowedSlot(str, "t")}},
			}},
			&Gate{Cond: 0, Body: []Node{
				&Eval{Result: 3, Expr: ident("u"), Slots: []Slot{borrowedSlot(str, "u")}},
			}},
		},
		Commit: []Mapping{{Target: heapLocal("s", str), Outcome: OutcomeRef{Outcome: 3}, OnSkip: KeepOnSkip}},
	}
}

// testedFirstPlan is `x = (sarr[9] || MkStr(b) > "a") && 7`, before
// elaboration: a first of strings that a last of an integer only tests, its
// own sides an eval and a check.
func testedFirstPlan() *AssignPlan {
	i64, str := testType("I64"), testType("Str")
	return &AssignPlan{
		Head: Head{Label: "assign x", Source: `x = (sarr[9] || MkStr(b) > "a") && 7`},
		Execute: []Node{&Last{Result: 0, Type: i64, Sides: []Node{
			&First{Result: NoOutcome, Type: str, Sides: []Node{
				&Eval{Result: 1, Expr: read("sarr", 9), Slots: []Slot{ownedSlot(str)}, Checked: true},
				&Check{Result: 2, Expr: greater(call("MkStr", ident("b")), strLit("a")), Slots: []Slot{ownedSlot(str)}},
			}},
			&Eval{Result: 3, Expr: intLit(7), Slots: []Slot{unmanagedSlot(i64)}},
		}}},
		Commit: []Mapping{{Target: local("x", i64), Outcome: OutcomeRef{Outcome: 0}, OnSkip: KeepOnSkip}},
	}
}

// nestedFirstPlan is `s = n > 0 && (sarr[9] > "a" || sarr[8] > "b") || h` over
// heap strings, before elaboration: a first in a last in a first, the inner
// one the last's final side, so three nodes can supply the value.
func nestedFirstPlan() *AssignPlan {
	i64, str := testType("I64"), testType("Str")
	return &AssignPlan{
		Head: Head{Label: "assign s", Source: `s = n > 0 && (sarr[9] > "a" || sarr[8] > "b") || h`},
		Execute: []Node{&First{Result: 0, Type: str, Sides: []Node{
			&Last{Result: NoOutcome, Type: str, Sides: []Node{
				&Check{Result: 1, Expr: greater(ident("n"), intLit(0)), Slots: []Slot{unmanagedSlot(i64)}},
				&First{Result: NoOutcome, Type: str, Sides: []Node{
					&Check{Result: 2, Expr: greater(read("sarr", 9), strLit("a")), Slots: []Slot{ownedSlot(str)}, Checked: true},
					&Check{Result: 3, Expr: greater(read("sarr", 8), strLit("b")), Slots: []Slot{ownedSlot(str)}, Checked: true},
				}},
			}},
			&Eval{Result: 4, Expr: ident("h"), Slots: []Slot{borrowedSlot(str, "h")}},
		}}},
		Commit: []Mapping{{Target: heapLocal("s", str), Outcome: OutcomeRef{Outcome: 0}}},
	}
}

// finalFirstPlan is `s = n > 0 && (sarr[9] > "a" || sarr[8] > "b")` over heap
// strings, before elaboration: a value last whose final side is a first, with
// no fallback after the last itself.
func finalFirstPlan() *AssignPlan {
	p := nestedFirstPlan()
	last := sideLastAt(p, 0, 0)
	last.Result = 0
	p.Head.Source = `s = n > 0 && (sarr[9] > "a" || sarr[8] > "b")`
	p.Execute = []Node{last}
	p.Commit[0].OnSkip = KeepOnSkip
	return p
}

// lastFirstAt is side j, a first, of the last that is node i of p's execute
// region.
func lastFirstAt(p *AssignPlan, i, j int) *First { return lastAt(p, i).Sides[j].(*First) }

// lastAt is node i of p's execute region, a last.
func lastAt(p *AssignPlan, i int) *Last { return p.Execute[i].(*Last) }

// evalAt is node i of p's execute region, an eval.
func evalAt(p *AssignPlan, i int) *Eval { return p.Execute[i].(*Eval) }

// firstAt is node i of p's execute region, a first.
func firstAt(p *AssignPlan, i int) *First { return p.Execute[i].(*First) }

// sideEvalAt is side j of the first that is node i of p's execute region, an
// eval.
func sideEvalAt(p *AssignPlan, i, j int) *Eval { return firstAt(p, i).Sides[j].(*Eval) }

// sideCheckAt is side j of the first that is node i of p's execute region, a
// check.
func sideCheckAt(p *AssignPlan, i, j int) *Check { return firstAt(p, i).Sides[j].(*Check) }

// sideLastAt is side j of the first that is node i of p's execute region, a
// last.
func sideLastAt(p *AssignPlan, i, j int) *Last { return firstAt(p, i).Sides[j].(*Last) }

func TestValidateAccepts(t *testing.T) {
	scalarSwap := swapPlan()
	Elaborate(scalarSwap)
	heapSwap := heapSwapPlan()
	Elaborate(heapSwap)
	replace := replacePlan()
	Elaborate(replace)
	firstCheck := firstCheckPlan()
	Elaborate(firstCheck)
	firstLast := firstLastPlan()
	Elaborate(firstLast)
	testedFirst := testedFirstPlan()
	Elaborate(testedFirst)
	for name, p := range map[string]*AssignPlan{
		"scalar swap":    scalarSwap,
		"heap swap":      heapSwap,
		"replace + drop": replace,
		"check side":     firstCheck,
		"&& side":        firstLast,
		"tested || side": testedFirst,
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
		transfers [][]Transfer
		releases  Releases
	}{
		{"scalar swap stores", swapPlan(), [][]Transfer{{Store}, {Store}}, Releases{}},
		{"heap swap moves both old values, copies nothing", heapSwapPlan(), [][]Transfer{{Move}, {Move}}, Releases{}},
		{"owned replacement moves and releases the old value", replacePlan(), [][]Transfer{{Move}, {Store}},
			Releases{Discarded: []OutcomeRef{{Outcome: 1}}, Replaced: []string{"x"}}},
		{"duplicate source: first takes, second copies", &AssignPlan{
			Head: Head{Label: "assign d1, d2", Source: "d1, d2 = d1, d1"},
			Execute: []Node{
				&Eval{Result: 0, Expr: ident("d1"), Slots: []Slot{borrowedSlot(str, "d1")}},
				&Eval{Result: 1, Expr: ident("d1"), Slots: []Slot{borrowedSlot(str, "d1")}},
			},
			Commit: []Mapping{
				{Target: heapLocal("d1", str), Outcome: OutcomeRef{Outcome: 0}},
				{Target: heapLocal("d2", str), Outcome: OutcomeRef{Outcome: 1}},
			},
		}, [][]Transfer{{Move}, {Copy}}, Releases{Replaced: []string{"d2"}}},
		{"borrow of a surviving owner copies", &AssignPlan{
			Head:    Head{Label: "assign t", Source: "t = s"},
			Execute: []Node{&Eval{Result: 0, Expr: ident("s"), Slots: []Slot{borrowedSlot(str, "s")}}},
			Commit:  []Mapping{{Target: Target{Kind: LocalTarget, Name: "t", Type: str, TypeOwnsHeap: true, Fresh: true}, Outcome: OutcomeRef{Outcome: 0}}},
		}, [][]Transfer{{Copy}}, Releases{}},
		{"unmanaged into an owning target is copied", &AssignPlan{
			Head:    Head{Label: "assign s", Source: `s = "hi"`},
			Execute: []Node{&Eval{Result: 0, Expr: strLit("hi"), Slots: []Slot{unmanagedSlot(str)}}},
			Commit:  []Mapping{{Target: heapLocal("s", str), Outcome: OutcomeRef{Outcome: 0}}},
		}, [][]Transfer{{Copy}}, Releases{Replaced: []string{"s"}}},
		{"heap transfer into a non-owning fresh target is legal", &AssignPlan{
			Head:    Head{Label: "assign other", Source: "other = text"},
			Execute: []Node{&Eval{Result: 0, Expr: ident("text"), Slots: []Slot{borrowedSlot(str, "text")}}},
			Commit:  []Mapping{{Target: Target{Kind: LocalTarget, Name: "other", Type: str, Fresh: true}, Outcome: OutcomeRef{Outcome: 0}}},
		}, [][]Transfer{{Copy}}, Releases{}},
		{"widened binding releases its held value on a plain store", &AssignPlan{
			Head:    Head{Label: "assign other", Source: `other = "new"`},
			Execute: []Node{&Eval{Result: 0, Expr: strLit("new"), Slots: []Slot{unmanagedSlot(str)}}},
			Commit:  []Mapping{{Target: widenedLocal("other", str), Outcome: OutcomeRef{Outcome: 0}}},
		}, [][]Transfer{{Store}}, Releases{Replaced: []string{"other"}}},
		{"widened owner is taken by a moved borrow", &AssignPlan{
			Head: Head{Label: "assign a, other", Source: "a, other = other, a"},
			Execute: []Node{
				&Eval{Result: 0, Expr: ident("other"), Slots: []Slot{borrowedSlot(str, "other")}},
				&Eval{Result: 1, Expr: ident("a"), Slots: []Slot{borrowedSlot(str, "a")}},
			},
			Commit: []Mapping{
				{Target: heapLocal("a", str), Outcome: OutcomeRef{Outcome: 0}},
				{Target: widenedLocal("other", str), Outcome: OutcomeRef{Outcome: 1}},
			},
		}, [][]Transfer{{Move}, {Move}}, Releases{}},
		{"discarded borrow releases nothing", &AssignPlan{
			Head:    Head{Label: "assign _", Source: "_ = h"},
			Execute: []Node{&Eval{Result: 0, Expr: ident("h"), Slots: []Slot{borrowedSlot(str, "h")}}},
			Commit:  []Mapping{{Target: Target{Kind: DiscardTarget}, Outcome: OutcomeRef{Outcome: 0}}},
		}, [][]Transfer{{Store}}, Releases{}},
		{"output releases the value it held, as a local does", joinPlan(), [][]Transfer{{Move}},
			Releases{Replaced: []string{"res"}}},
		{"swapped outputs take each other's old values", &AssignPlan{
			Head: Head{Label: "assign a, b", Func: "Str, Str = Swap(Str, Str)", Source: "a, b = b, a"},
			Execute: []Node{
				&Eval{Result: 0, Expr: ident("b"), Slots: []Slot{borrowedSlot(str, "b")}},
				&Eval{Result: 1, Expr: ident("a"), Slots: []Slot{borrowedSlot(str, "a")}},
			},
			Commit: []Mapping{
				{Target: heapOutput("a", str), Outcome: OutcomeRef{Outcome: 0}},
				{Target: heapOutput("b", str), Outcome: OutcomeRef{Outcome: 1}},
			},
		}, [][]Transfer{{Move}, {Move}}, Releases{}},
		{"a skipped owned outcome is released, a discarded one either way, an old value where replaced", skipPlan(),
			[][]Transfer{{Move}, {Move}, {Store}}, Releases{
				Skipped:   []OutcomeRef{{Outcome: 0}, {Outcome: 1}},
				Discarded: []OutcomeRef{{Outcome: 2}},
				Replaced:  []string{"kept"},
			}},
		{"a borrow copies when its owner's replacement may skip", &AssignPlan{
			Head: Head{Label: "assign a, b", Source: "a, b = b, sarr[9]"},
			Execute: []Node{
				&Eval{Result: 0, Expr: ident("b"), Slots: []Slot{borrowedSlot(str, "b")}},
				&Eval{Result: 1, Expr: read("sarr", 9), Slots: []Slot{ownedSlot(str)}, Checked: true},
			},
			Commit: []Mapping{
				{Target: heapLocal("a", str), Outcome: OutcomeRef{Outcome: 0}},
				{Target: heapLocal("b", str), Outcome: OutcomeRef{Outcome: 1}, OnSkip: KeepOnSkip},
			},
		}, [][]Transfer{{Copy}, {Move}}, Releases{Skipped: []OutcomeRef{{Outcome: 1}}, Replaced: []string{"a", "b"}}},
		// No source statement builds a borrowed outcome that may skip yet.
		{"a borrow that may skip copies though its owner is replaced", &AssignPlan{
			Head: Head{Label: "assign a, b", Source: `a, b = views[9], "new"`},
			Execute: []Node{
				&Eval{Result: 0, Expr: read("views", 9), Slots: []Slot{borrowedSlot(str, "b")}, Checked: true},
				&Eval{Result: 1, Expr: strLit("new"), Slots: []Slot{unmanagedSlot(str)}},
			},
			Commit: []Mapping{
				{Target: heapLocal("a", str), Outcome: OutcomeRef{Outcome: 0}, OnSkip: KeepOnSkip},
				{Target: heapLocal("b", str), Outcome: OutcomeRef{Outcome: 1}},
			},
		}, [][]Transfer{{Copy}, {Copy}}, Releases{Replaced: []string{"a", "b"}}},
		{"a first moves its owned side, copies its borrowed one, and releases what the failed read built", firstPlan(),
			[][]Transfer{{Move, Copy}}, Releases{Skipped: []OutcomeRef{{Outcome: 1}}, Replaced: []string{"s"}}},
		{"a side of a first copies though its owner is replaced", &AssignPlan{
			Head: Head{Label: "assign a, b", Source: `a, b = sarr[9] || b, "new"`},
			Execute: []Node{
				&First{Result: 0, Type: str, Sides: []Node{
					&Eval{Result: 1, Expr: read("sarr", 9), Slots: []Slot{ownedSlot(str)}, Checked: true},
					&Eval{Result: 2, Expr: ident("b"), Slots: []Slot{borrowedSlot(str, "b")}},
				}},
				&Eval{Result: 3, Expr: strLit("new"), Slots: []Slot{unmanagedSlot(str)}},
			},
			Commit: []Mapping{
				{Target: heapLocal("a", str), Outcome: OutcomeRef{Outcome: 0}},
				{Target: heapLocal("b", str), Outcome: OutcomeRef{Outcome: 3}},
			},
		}, [][]Transfer{{Move, Copy}, {Copy}}, Releases{Skipped: []OutcomeRef{{Outcome: 1}}, Replaced: []string{"a", "b"}}},
		{"a discarded first releases each owned side where it ran", &AssignPlan{
			Head: Head{Label: "assign _", Source: `_ = sarr[9] || h ⊕ "?"`},
			Execute: []Node{&First{Result: 0, Type: str, Sides: []Node{
				&Eval{Result: 1, Expr: read("sarr", 9), Slots: []Slot{ownedSlot(str)}, Checked: true},
				&Eval{Result: 2, Expr: concat(ident("h"), strLit("?")), Slots: []Slot{ownedSlot(str)}},
			}}},
			Commit: []Mapping{{Target: Target{Kind: DiscardTarget}, Outcome: OutcomeRef{Outcome: 0}}},
		}, [][]Transfer{{Store, Store}}, Releases{Discarded: []OutcomeRef{{Outcome: 1}, {Outcome: 2}}}},
		{"a first takes a last side's value from its final side, and releases what the side's check built", firstLastPlan(),
			[][]Transfer{{Move, Copy}}, Releases{Tested: []OutcomeRef{{Outcome: 1}}, Replaced: []string{"t"}}},
		{"a first whose last side may skip releases each side where it failed", chainPlan(),
			[][]Transfer{{Store}, {Move, Move, Move}, {Store}}, Releases{Skipped: []OutcomeRef{{Outcome: 2}, {Outcome: 3}, {Outcome: 4}}}},
		{"a gate releases what its check built, and its region's values as any other", gatePlan(),
			[][]Transfer{{Move}}, Releases{Tested: []OutcomeRef{{Outcome: 0}}, Skipped: []OutcomeRef{{Outcome: 1}}, Replaced: []string{"s"}}},
		{"a gate's last releases what each of its checks built", lastGatePlan(),
			[][]Transfer{{Copy}}, Releases{Tested: []OutcomeRef{{Outcome: 1}}, Replaced: []string{"s"}}},
		{"a value last moves its final side and releases the side it only tested", lastValuePlan(),
			[][]Transfer{{Move}}, Releases{Tested: []OutcomeRef{{Outcome: 1}}, Skipped: []OutcomeRef{{Outcome: 2}}, Replaced: []string{"s"}}},
		{"a gate's first releases what each of its checks built", firstGatePlan(),
			[][]Transfer{{Copy}}, Releases{Tested: []OutcomeRef{{Outcome: 1}}, Replaced: []string{"s"}}},
		{"a first a last only tests releases what its eval and its check built", testedFirstPlan(),
			[][]Transfer{{Store}}, Releases{Tested: []OutcomeRef{{Outcome: 1}, {Outcome: 2}}}},
		{"a last takes a final first's value from either side, and releases each where it failed", finalFirstPlan(),
			[][]Transfer{{Move, Move}}, Releases{Skipped: []OutcomeRef{{Outcome: 2}, {Outcome: 3}}, Replaced: []string{"s"}}},
		{"a first takes a last side's value from each node its final first holds", nestedFirstPlan(),
			[][]Transfer{{Move, Move, Copy}}, Releases{Skipped: []OutcomeRef{{Outcome: 2}, {Outcome: 3}}, Replaced: []string{"s"}}},
		{"a check is only tested in a last however deep the last sits", &AssignPlan{
			Head: Head{Label: "assign s", Source: `s = n > 0 && (MkStr(a) > "a" && h || sarr[8] > "b")`},
			Execute: []Node{&Last{Result: 0, Type: str, Sides: []Node{
				&Check{Result: 1, Expr: greater(ident("n"), intLit(0)), Slots: []Slot{unmanagedSlot(testType("I64"))}},
				&First{Result: NoOutcome, Type: str, Sides: []Node{
					&Last{Result: NoOutcome, Type: str, Sides: []Node{
						&Check{Result: 2, Expr: greater(call("MkStr", ident("a")), strLit("a")), Slots: []Slot{ownedSlot(str)}},
						&Eval{Result: 3, Expr: ident("h"), Slots: []Slot{borrowedSlot(str, "h")}},
					}},
					&Check{Result: 4, Expr: greater(read("sarr", 8), strLit("b")), Slots: []Slot{ownedSlot(str)}, Checked: true},
				}},
			}}},
			Commit: []Mapping{{Target: heapLocal("s", str), Outcome: OutcomeRef{Outcome: 0}, OnSkip: KeepOnSkip}},
		}, [][]Transfer{{Copy, Move}}, Releases{Tested: []OutcomeRef{{Outcome: 2}}, Skipped: []OutcomeRef{{Outcome: 4}}, Replaced: []string{"s"}}},
		{"a borrow under a last takes nothing: its checks may fail", &AssignPlan{
			Head: Head{Label: "assign a, b", Source: "a, b = n > 0 && b, a"},
			Execute: []Node{
				&Last{Result: 0, Type: str, Sides: []Node{
					&Check{Result: 1, Expr: greater(ident("n"), intLit(0)), Slots: []Slot{unmanagedSlot(testType("I64"))}},
					&Eval{Result: 2, Expr: ident("b"), Slots: []Slot{borrowedSlot(str, "b")}},
				}},
				&Eval{Result: 3, Expr: ident("a"), Slots: []Slot{borrowedSlot(str, "a")}},
			},
			Commit: []Mapping{
				{Target: heapLocal("a", str), Outcome: OutcomeRef{Outcome: 0}, OnSkip: KeepOnSkip},
				{Target: heapLocal("b", str), Outcome: OutcomeRef{Outcome: 3}},
			},
		}, [][]Transfer{{Copy}, {Copy}}, Releases{Replaced: []string{"a", "b"}}},
		{"a check's value moves where it holds and is released where it failed", checkPlan(),
			[][]Transfer{{Move}}, Releases{Skipped: []OutcomeRef{{Outcome: 0}}, Replaced: []string{"s"}}},
		{"a borrowed check takes nothing: its comparison may fail", &AssignPlan{
			Head: Head{Label: "assign a, b", Source: `a, b = b > "", a`},
			Execute: []Node{
				&Check{Result: 0, Expr: greater(ident("b"), strLit("")), Slots: []Slot{borrowedSlot(str, "b")}},
				&Eval{Result: 1, Expr: ident("a"), Slots: []Slot{borrowedSlot(str, "a")}},
			},
			Commit: []Mapping{
				{Target: heapLocal("a", str), Outcome: OutcomeRef{Outcome: 0}, OnSkip: KeepOnSkip},
				{Target: heapLocal("b", str), Outcome: OutcomeRef{Outcome: 1}},
			},
		}, [][]Transfer{{Copy}, {Copy}}, Releases{Replaced: []string{"a", "b"}}},
		{"a value last ending in a check moves its final side and releases the side it only tested", &AssignPlan{
			Head: Head{Label: "assign s", Source: `s = MkStr(n) > "a" && MkStr(x) > "b"`},
			Execute: []Node{&Last{Result: 0, Type: str, Sides: []Node{
				&Check{Result: 1, Expr: greater(call("MkStr", ident("n")), strLit("a")), Slots: []Slot{ownedSlot(str)}},
				&Check{Result: 2, Expr: greater(call("MkStr", ident("x")), strLit("b")), Slots: []Slot{ownedSlot(str)}},
			}}},
			Commit: []Mapping{{Target: heapLocal("s", str), Outcome: OutcomeRef{Outcome: 0}, OnSkip: KeepOnSkip}},
		}, [][]Transfer{{Move}}, Releases{Tested: []OutcomeRef{{Outcome: 1}}, Skipped: []OutcomeRef{{Outcome: 2}}, Replaced: []string{"s"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Elaborate(tc.plan)
			for i, m := range tc.plan.Commit {
				if !slices.Equal(m.Transfers, tc.transfers[i]) {
					t.Errorf("mapping %d transfers = %v, want %v", i, m.Transfers, tc.transfers[i])
				}
			}
			if !slices.Equal(tc.plan.Tested, tc.releases.Tested) ||
				!slices.Equal(tc.plan.Skipped, tc.releases.Skipped) ||
				!slices.Equal(tc.plan.Discarded, tc.releases.Discarded) ||
				!slices.Equal(tc.plan.Replaced, tc.releases.Replaced) {
				t.Errorf("releases = %+v, want %+v", tc.plan.Releases, tc.releases)
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
		{"NoEvals", func(p *AssignPlan) { p.Execute = nil }, "no evals"},
		{"UnknownNode", func(p *AssignPlan) { p.Execute = append(p.Execute, unknownNode(2)) }, "unknown execute node pir.unknownNode"},
		{"SparseResultID", func(p *AssignPlan) { evalAt(p, 1).Result = 2 }, "dense"},
		{"NoSlots", func(p *AssignPlan) { evalAt(p, 0).Slots = nil }, "no output slots"},
		{"NilDiscardedType", func(p *AssignPlan) {
			p.Commit[0].Target = Target{Kind: DiscardTarget}
			evalAt(p, 0).Slots = []Slot{{}}
		}, "slot 0 has no type"},
		{"OwnerWithoutBorrow", func(p *AssignPlan) { evalAt(p, 0).Slots[0].Owner = "b" }, "names owner b but is not borrowed"},
		{"BorrowWithoutOwner", func(p *AssignPlan) { evalAt(p, 0).Slots[0].Ownership = Borrowed }, "borrowed from no owner"},
		{"UnknownOwnership", func(p *AssignPlan) { evalAt(p, 0).Slots[0].Ownership = 9 }, "unknown ownership"},
		{"MissingMapping", func(p *AssignPlan) { p.Commit = p.Commit[:1] }, "1 mappings for 2 outcome slots"},
		{"UnnamedLocal", func(p *AssignPlan) { p.Commit[0].Target.Name = "" }, "binding target has no name"},
		{"OutputOutsideFunction", func(p *AssignPlan) { p.Commit[0].Target.Kind = OutputTarget }, "output target a outside a function body"},
		{"FreshOutput", func(p *AssignPlan) {
			p.Func = "I64, I64 = Swap(I64, I64)"
			p.Commit[0].Target.Kind = OutputTarget
			p.Commit[0].Target.Fresh = true
		}, "output target a is fresh"},
		{"TypeMismatch", func(p *AssignPlan) { evalAt(p, 0).Slots[0].Type = testType("F64") }, "target I64 a mapped to incompatible outcome %t0 slot 0 of type F64"},
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
			p.Commit[0].Transfers = []Transfer{Move}
		}, "discard of %t0 slot 0 carries a transfer"},
		{"UnknownOutcome", func(p *AssignPlan) { p.Commit[0].Outcome.Outcome = 5 }, "unknown outcome"},
		{"SlotOutOfRange", func(p *AssignPlan) { p.Commit[0].Outcome.Slot = 3 }, "out of range"},
		{"NegativeOutcome", func(p *AssignPlan) { p.Commit[0].Outcome.Outcome = -1 }, "unknown outcome"},
		{"NegativeSlot", func(p *AssignPlan) { p.Commit[0].Outcome.Slot = -1 }, "out of range"},
		{"DoubleConsume", func(p *AssignPlan) { p.Commit[1].Outcome = p.Commit[0].Outcome }, "consumed twice"},
		{"UnknownTargetKind", func(p *AssignPlan) { p.Commit[0].Target.Kind = 7 }, "unknown target kind"},
		{"UnmanagedMoved", func(p *AssignPlan) { p.Commit[0].Transfers = []Transfer{Move} }, "uses transfer move; ownership requires store"},
		{"UnmanagedIntoOwnerNotCopied", func(p *AssignPlan) { p.Commit[0].Target.TypeOwnsHeap = true }, "uses transfer store; ownership requires copy"},
		{"OwnedNotMoved", func(p *AssignPlan) { evalAt(p, 0).Slots[0].Ownership = Owned }, "uses transfer store; ownership requires move"},
		{"BorrowedNotCopied", func(p *AssignPlan) { evalAt(p, 0).Slots[0] = borrowedSlot(testType("I64"), "b") }, "uses transfer store; ownership requires copy"},
		{"MovedBorrowOfSurvivingOwner", func(p *AssignPlan) {
			evalAt(p, 0).Slots[0] = borrowedSlot(testType("I64"), "z")
			p.Commit[0].Transfers = []Transfer{Move}
		}, "target a takes z's old value, but z is not replaced in this group"},
		{"MovedBorrowOfFreshOwner", func(p *AssignPlan) {
			evalAt(p, 0).Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfers = []Transfer{Move}
			p.Commit[1].Target.TypeOwnsHeap = true
			p.Commit[1].Target.Fresh = true
			p.Commit[1].Transfers = []Transfer{Copy}
		}, "b is not replaced in this group"},
		{"MovedBorrowOfOwnerHoldingNothing", func(p *AssignPlan) {
			evalAt(p, 0).Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfers = []Transfer{Move}
			p.Commit[1].Target.TypeOwnsHeap = true
			p.Commit[1].Transfers = []Transfer{Copy}
		}, "b is not replaced in this group"},
		{"OwnerReplacedFromUnknownOutcome", func(p *AssignPlan) {
			evalAt(p, 0).Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfers = []Transfer{Move}
			p.Commit[1].Target.HoldsHeap = true
			p.Commit[1].Outcome.Outcome = 5
		}, "commit references unknown outcome %t5"},
		{"OwnerTakenTwice", func(p *AssignPlan) {
			evalAt(p, 0).Slots[0] = borrowedSlot(testType("I64"), "b")
			evalAt(p, 1).Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfers = []Transfer{Move}
			p.Commit[1].Transfers = []Transfer{Move}
			p.Commit[1].Target.HoldsHeap = true
		}, "b's old value is taken by 2 targets"},
		{"ReplacedNeverReleased", func(p *AssignPlan) { p.Commit[0].Target.HoldsHeap = true }, "a's old value is neither taken nor released"},
		{"ReplacedTakenAndDropped", func(p *AssignPlan) {
			evalAt(p, 0).Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfers = []Transfer{Move}
			p.Commit[1].Target.HoldsHeap = true
			p.Replaced = []string{"b"}
		}, "b's old value is both taken and dropped"},
		{"DropOfUnreplacedTarget", func(p *AssignPlan) { p.Replaced = []string{"a"} }, "a holds no replaced value"},
		{"DropOfFreshTarget", func(p *AssignPlan) {
			p.Commit[0].Target.TypeOwnsHeap = true
			p.Commit[0].Target.Fresh = true
			p.Commit[0].Transfers = []Transfer{Copy}
			p.Replaced = []string{"a"}
		}, "a holds no replaced value"},
		{"ReplacedDroppedTwice", func(p *AssignPlan) {
			p.Commit[0].Target.HoldsHeap = true
			p.Replaced = []string{"a", "a"}
		}, "a's old value dropped twice"},
		{"DropOfMappedOutcome", func(p *AssignPlan) { p.Discarded = []OutcomeRef{{Outcome: 0}} }, "not a discarded owned outcome"},
		{"DropOfDiscardedBorrow", func(p *AssignPlan) {
			evalAt(p, 0).Slots[0] = borrowedSlot(testType("I64"), "z")
			p.Commit[0].Target = Target{Kind: DiscardTarget}
			p.Discarded = []OutcomeRef{{Outcome: 0}}
		}, "not a discarded owned outcome"},
		{"DiscardedOwnedNeverReleased", func(p *AssignPlan) {
			evalAt(p, 0).Slots[0].Ownership = Owned
			p.Commit[0].Target = Target{Kind: DiscardTarget}
		}, "discarded owned outcome %t0 slot 0 is never released"},
		{"DiscardedOwnedDroppedTwice", func(p *AssignPlan) {
			evalAt(p, 0).Slots[0].Ownership = Owned
			p.Commit[0].Target = Target{Kind: DiscardTarget}
			p.Discarded = []OutcomeRef{{Outcome: 0}, {Outcome: 0}}
		}, "%t0 slot 0 dropped twice"},
		{"AccessNotChecked", func(p *AssignPlan) { evalAt(p, 0).Expr = read("arr", 0) }, "eval %t0 holds a checked access and is not marked checked"},
		{"CheckedWithoutAccess", func(p *AssignPlan) { evalAt(p, 0).Checked = true }, "eval %t0 is marked checked and holds no checked access"},
		{"AccessInLiteralCell", func(p *AssignPlan) {
			evalAt(p, 0).Expr = &ast.ArrayLiteral{Rows: [][]ast.Expression{{read("arr", 0)}}}
		}, "eval %t0 holds a checked access in a literal cell"},
		{"SkipWithoutPolicy", func(p *AssignPlan) {
			evalAt(p, 0).Expr, evalAt(p, 0).Checked = read("arr", 0), true
		}, "target a = %t0 slot 0 has skip policy none; its outcome and target require keep"},
		{"KeepOnFreshTarget", func(p *AssignPlan) {
			evalAt(p, 0).Expr, evalAt(p, 0).Checked = read("arr", 0), true
			p.Commit[0].Target.Fresh = true
			p.Commit[0].OnSkip = KeepOnSkip
		}, "has skip policy keep; its outcome and target require zero"},
		{"PolicyOnCertainOutcome", func(p *AssignPlan) { p.Commit[0].OnSkip = KeepOnSkip }, "has skip policy keep; its outcome and target require none"},
		{"PolicyOnDiscard", func(p *AssignPlan) {
			evalAt(p, 0).Expr, evalAt(p, 0).Checked = read("arr", 0), true
			p.Commit[0].Target = Target{Kind: DiscardTarget}
			p.Commit[0].OnSkip = KeepOnSkip
		}, "target _ = %t0 slot 0 has skip policy keep; its outcome and target require none"},
		{"MovedBorrowOfOwnerThatMaySkip", func(p *AssignPlan) {
			evalAt(p, 0).Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfers = []Transfer{Move}
			evalAt(p, 1).Expr, evalAt(p, 1).Checked = read("arr", 0), true
			p.Commit[1].Target.HoldsHeap = true
			p.Commit[1].OnSkip = KeepOnSkip
		}, "target a takes b's old value, but one of the two mappings may skip without the other"},
		{"MovedBorrowThatMaySkip", func(p *AssignPlan) {
			evalAt(p, 0).Expr, evalAt(p, 0).Checked = read("arr", 0), true
			evalAt(p, 0).Slots[0] = borrowedSlot(testType("I64"), "b")
			p.Commit[0].Transfers = []Transfer{Move}
			p.Commit[0].OnSkip = KeepOnSkip
			p.Commit[1].Target.HoldsHeap = true
		}, "target a takes b's old value, but one of the two mappings may skip without the other"},
		{"SkippedOwnedNeverReleased", func(p *AssignPlan) {
			evalAt(p, 0).Expr, evalAt(p, 0).Checked = read("arr", 0), true
			evalAt(p, 0).Slots[0].Ownership = Owned
			p.Commit[0].Transfers = []Transfer{Move}
			p.Commit[0].OnSkip = KeepOnSkip
		}, "owned outcome %t0 slot 0 is never released where it fails"},
		{"SkipDropOfCertainOutcome", func(p *AssignPlan) {
			evalAt(p, 0).Slots[0].Ownership = Owned
			p.Commit[0].Transfers = []Transfer{Move}
			p.Skipped = []OutcomeRef{{Outcome: 0}}
		}, "skip-path drop of %t0 slot 0"},
		{"SkippedDroppedTwice", func(p *AssignPlan) {
			evalAt(p, 0).Expr, evalAt(p, 0).Checked = read("arr", 0), true
			evalAt(p, 0).Slots[0].Ownership = Owned
			p.Commit[0].Transfers = []Transfer{Move}
			p.Commit[0].OnSkip = KeepOnSkip
			p.Skipped = []OutcomeRef{{Outcome: 0}, {Outcome: 0}}
		}, "skipped %t0 slot 0 dropped twice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := swapPlan()
			Elaborate(p)
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

// Plan §4, §14: a first's invariants, each broken on `s = sarr[9] || h`.
func TestValidateFirstRejects(t *testing.T) {
	str := testType("Str")
	cases := []struct {
		name    string
		mutate  func(*AssignPlan)
		wantErr string
	}{
		{"FirstIDOutOfOrder", func(p *AssignPlan) { firstAt(p, 0).Result = 3 }, "first has result ID 3 where 0 is next; IDs must be dense"},
		{"SparseSideID", func(p *AssignPlan) { sideEvalAt(p, 0, 1).Result = 5 }, "eval 2 has result ID 5; IDs must be dense"},
		{"NoType", func(p *AssignPlan) { firstAt(p, 0).Type = nil }, "first %t0 has no type"},
		{"OneSide", func(p *AssignPlan) { firstAt(p, 0).Sides = firstAt(p, 0).Sides[:1] }, "first %t0 has 1 sides and needs a fallback"},
		{"SideWithTwoSlots", func(p *AssignPlan) {
			sideEvalAt(p, 0, 1).Slots = []Slot{borrowedSlot(str, "h"), borrowedSlot(str, "h")}
		}, "side %t2 of first %t0 has 2 slots"},
		{"SideTypeMismatch", func(p *AssignPlan) { sideEvalAt(p, 0, 1).Slots[0].Type = testType("I64") }, "side %t2 of type I64 does not fit first %t0 of type Str"},
		{"SideCheckedWithoutAccess", func(p *AssignPlan) { sideEvalAt(p, 0, 0).Expr = ident("t") }, "eval %t1 is marked checked and holds no checked access"},
		{"FallbackAfterSideThatCannotFail", func(p *AssignPlan) {
			sideEvalAt(p, 0, 0).Expr, sideEvalAt(p, 0, 0).Checked = ident("t"), false
		}, "side %t1 of first %t0 cannot fail, so the fallback after it would never run"},
		{"MappedSide", func(p *AssignPlan) { p.Commit[0].Outcome.Outcome = 1 }, "commit references %t1, which sits inside first %t0"},
		{"FirstSlotOutOfRange", func(p *AssignPlan) { p.Commit[0].Outcome.Slot = 1 }, "commit references %t0 slot 1 out of range"},
		{"TransferPerSide", func(p *AssignPlan) { p.Commit[0].Transfers = []Transfer{Move} }, "has 1 transfers for the 2 nodes that can supply it"},
		{"BorrowedSideMoved", func(p *AssignPlan) { p.Commit[0].Transfers = []Transfer{Move, Move} }, "target s takes h's old value from %t2, one side of a first"},
		{"OwnedSideNotMoved", func(p *AssignPlan) { p.Commit[0].Transfers = []Transfer{Copy, Copy} }, "target s = %t1 slot 0 uses transfer copy; ownership requires move"},
		{"FailedSideNeverReleased", func(p *AssignPlan) { p.Skipped = nil }, "owned outcome %t1 slot 0 is never released where it fails"},
		{"PolicyOnFirstThatAlwaysYields", func(p *AssignPlan) { p.Commit[0].OnSkip = KeepOnSkip }, "target s = %t0 slot 0 has skip policy keep; its outcome and target require none"},
		{"NoPolicyOnFirstThatMaySkip", func(p *AssignPlan) {
			firstAt(p, 0).Sides[1] = &Eval{Result: 2, Expr: read("sarr", 8), Slots: []Slot{ownedSlot(str)}, Checked: true}
		}, "target s = %t0 slot 0 has skip policy none; its outcome and target require keep"},
		{"DiscardedFirstWithTransfer", func(p *AssignPlan) {
			p.Commit[0].Target = Target{Kind: DiscardTarget}
		}, "discard of %t0 slot 0 carries a transfer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := firstPlan()
			Elaborate(p)
			tc.mutate(p)
			err := Validate(p, sameSpelling)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// Plan §4, §14: what a first's sides must be, broken on `s = sarr[9] > "a"
// || h`, whose side is a check, and on `t = MkStr(a) > "a" && MkStr(b) || h`,
// whose first side is an && of its own.
func TestValidateFirstSideRejects(t *testing.T) {
	cases := []struct {
		name    string
		plan    func() *AssignPlan
		mutate  func(*AssignPlan)
		wantErr string
	}{
		{"CheckSideNotChecked", firstCheckPlan, func(p *AssignPlan) {
			sideCheckAt(p, 0, 0).Checked = false
		}, "check %t1 holds a checked access and is not marked checked"},
		{"CheckSideTypeMismatch", firstCheckPlan, func(p *AssignPlan) {
			firstAt(p, 0).Type = testType("I64")
		}, "side %t1 of type Str does not fit first %t0 of type I64"},
		{"NestedLastBindsOutcome", firstLastPlan, func(p *AssignPlan) {
			sideLastAt(p, 0, 0).Result = 1
		}, "the last in side 1 of first %t0 binds outcome %t1, and a side of a first binds none"},
		{"NestedLastWithOneSide", firstLastPlan, func(p *AssignPlan) {
			l := sideLastAt(p, 0, 0)
			l.Sides = l.Sides[:1]
		}, "the last in side 1 of first %t0 has 1 sides and needs a next"},
		{"NestedLastTypeMismatch", firstLastPlan, func(p *AssignPlan) {
			firstAt(p, 0).Type = testType("I64")
		}, "the last in side 1 of type Str does not fit first %t0 of type I64"},
		{"SparseIDInNestedLast", firstLastPlan, func(p *AssignPlan) {
			sideLastAt(p, 0, 0).Sides[1].(*Eval).Result = 7
		}, "eval 2 has result ID 7; IDs must be dense"},
		{"SideAfterNestedLast", firstLastPlan, func(p *AssignPlan) {
			sideEvalAt(p, 0, 1).Result = 9
		}, "eval 3 has result ID 9; IDs must be dense"},
		{"MappedNodeInsideNestedLast", firstLastPlan, func(p *AssignPlan) {
			p.Commit[0].Outcome.Outcome = 2
		}, "commit references %t2, which sits inside first %t0"},
		{"MappedNestedLastItself", firstLastPlan, func(p *AssignPlan) {
			p.Commit[0].Outcome.Outcome = NoOutcome
		}, "commit references unknown outcome %t-1"},
		{"GateAsSide", firstLastPlan, func(p *AssignPlan) {
			firstAt(p, 0).Sides[0] = &Gate{Cond: 1}
		}, "side 1 of first %t0 is *pir.Gate, which is no eval, check or last"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.plan()
			Elaborate(p)
			tc.mutate(p)
			err := Validate(p, sameSpelling)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// Plan §12: a first binds its outcome on a line of its own, with each side
// under a label beneath it, first and then fallback, and a failed read names
// what its failure does. A mapping names a transfer per side where the sides
// differ, and a release of what a failed side built carries that side's word.
func TestRenderFirst(t *testing.T) {
	p := firstPlan()
	Elaborate(p)
	want := `assign s
    source
        s = sarr[9] || h

    execute
        %t0 =
            first Str
                %t1 = eval Str sarr[9] [on-oob=fallback]
            fallback
                %t2 = eval Str h

    commit
        s = %t0
`
	if got := p.Render(false); got != want {
		t.Fatalf("concise render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	want = `assign s
    source
        s = sarr[9] || h

    execute
        %t0 =
            first Str
                %t1 = eval owned Str sarr[9] [on-oob=fallback]
            fallback
                %t2 = eval borrowed Str h

    commit
        s = %t0 [move %t1] [copy %t2]
        drop %t1 [on-fallback]
        drop s [old]
`
	if got := p.Render(true); got != want {
		t.Fatalf("expanded render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}

	chain := chainPlan()
	Elaborate(chain)
	if err := Validate(chain, sameSpelling); err != nil {
		t.Fatalf("chain plan rejected: %v", err)
	}
	want = `assign n, z, m
    source
        n, z, m = 5, sarr[9] || sarr[8] || sarr[7], 7

    execute
        %t0 = eval I64 5
        %t1 =
            first Str
                %t2 = eval owned Str sarr[9] [on-oob=fallback]
            fallback
                %t3 = eval owned Str sarr[8] [on-oob=fallback]
            fallback
                %t4 = eval owned Str sarr[7] [on-oob=skip]
        %t5 = eval I64 7

    commit
        n = %t0
        z = %t1 [move] [on-skip=zero]
        m = %t5
        drop %t2 [on-fallback]
        drop %t3 [on-fallback]
        drop %t4 [on-skip]
`
	if got := chain.Render(true); got != want {
		t.Fatalf("chain render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}

	// A side that is a last binds no outcome, so its head sits where the
	// outcome line would have gone and its own sides stay one level in from
	// it. A mapping names the node each side supplies the value from.
	nested := firstLastPlan()
	Elaborate(nested)
	if err := Validate(nested, sameSpelling); err != nil {
		t.Fatalf("plan with a last for a side rejected: %v", err)
	}
	want = `assign t
    source
        t = MkStr(a) > "a" && MkStr(b) || h

    execute
        %t0 =
            first Str
                last Str
                    %t1 = check owned Str MkStr(a) > "a"
                next
                    %t2 = eval owned Str MkStr(b)
            fallback
                %t3 = eval borrowed Str h

    commit
        t = %t0 [move %t2] [copy %t3]
        drop %t1
        drop t [old]
`
	if got := nested.Render(true); got != want {
		t.Fatalf("nested last render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}

	// A failed read inside a last is resolved as the last's own failure is:
	// by the fallback after its side, or by a skip in the side none follows.
	// What a final side built is released under the same word.
	lasts := firstLastsPlan()
	Elaborate(lasts)
	if err := Validate(lasts, sameSpelling); err != nil {
		t.Fatalf("plan with a last for each side rejected: %v", err)
	}
	want = `assign s
    source
        s = sarr[9] > "a" && sarr[8] || sarr[7] > "b" && sarr[6]

    execute
        %t0 =
            first Str
                last Str
                    %t1 = check owned Str sarr[9] > "a" [on-oob=fallback]
                next
                    %t2 = eval owned Str sarr[8] [on-oob=fallback]
            fallback
                last Str
                    %t3 = check owned Str sarr[7] > "b" [on-oob=skip]
                next
                    %t4 = eval owned Str sarr[6] [on-oob=skip]

    commit
        s = %t0 [move] [on-skip=keep]
        drop %t1
        drop %t3
        drop %t2 [on-fallback]
        drop %t4 [on-skip]
        drop s [old]
`
	if got := lasts.Render(true); got != want {
		t.Fatalf("render of a last for each side mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// Plan §4, §14: what a first must be as a gate's condition and as a side of a
// last, broken on `s = sarr[9] > "a" || t > "b" u`, whose gate takes a first,
// on `x = (sarr[9] || MkStr(b) > "a") && 7`, whose last only tests one, and on
// `s = n > 0 && (sarr[9] > "a" || sarr[8] > "b")`, whose last ends in one.
func TestValidateNestedFirstRejects(t *testing.T) {
	str := testType("Str")
	cases := []struct {
		name    string
		plan    func() *AssignPlan
		mutate  func(*AssignPlan)
		wantErr string
	}{
		{"ConditionUnvalidated", firstGatePlan, func(p *AssignPlan) {
			firstAt(p, 0).Sides = firstAt(p, 0).Sides[:1]
		}, "first %t0 has 1 sides and needs a fallback"},
		{"ConditionCannotFail", firstGatePlan, func(p *AssignPlan) {
			firstAt(p, 0).Sides[1] = &Eval{Result: 2, Expr: ident("t"), Slots: []Slot{borrowedSlot(str, "t")}}
		}, "first %t0 cannot fail, so the gate that takes it would never reject"},
		{"TestedConditionNeverReleased", firstGatePlan, func(p *AssignPlan) { p.Tested = nil }, "owned outcome %t1 slot 0, which is only tested, is never released"},
		{"ValueIDRepeatsASide", firstGatePlan, func(p *AssignPlan) { p.Gate().Body[0].(*Eval).Result = 2 }, "eval 3 has result ID 2; IDs must be dense"},
		{"NestedFirstBindsOutcome", testedFirstPlan, func(p *AssignPlan) {
			lastFirstAt(p, 0, 0).Result = 1
		}, "the first in side 1 of last %t0 binds outcome %t1, and a side of a last binds none"},
		{"NestedFirstUnvalidated", testedFirstPlan, func(p *AssignPlan) {
			f := lastFirstAt(p, 0, 0)
			f.Sides = f.Sides[1:]
		}, "the first in side 1 of last %t0 has 1 sides and needs a fallback"},
		{"SideAfterNestedFirst", testedFirstPlan, func(p *AssignPlan) {
			lastAt(p, 0).Sides[1].(*Eval).Result = 2
		}, "eval 3 has result ID 2; IDs must be dense"},
		{"TestedEvalNeverReleased", testedFirstPlan, func(p *AssignPlan) {
			p.Tested = p.Tested[1:]
		}, "owned outcome %t1 slot 0, which is only tested, is never released"},
		{"FinalFirstOfAnotherType", finalFirstPlan, func(p *AssignPlan) {
			lastAt(p, 0).Type = testType("I64")
		}, "the first in side 2 of last %t0 has type Str, which does not fit the last's type I64"},
		{"TransferPerSideOfFinalFirst", finalFirstPlan, func(p *AssignPlan) {
			p.Commit[0].Transfers = []Transfer{Move}
		}, "has 1 transfers for the 2 nodes that can supply it"},
		{"LastAsSideOfLast", finalFirstPlan, func(p *AssignPlan) {
			lastAt(p, 0).Sides[1] = &Last{Result: NoOutcome, Type: str}
		}, "side 2 of last %t0 is *pir.Last, which is no check, eval or first"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.plan()
			Elaborate(p)
			tc.mutate(p)
			err := Validate(p, sameSpelling)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// Plan §12: a gate takes a first's own outcome, and a first that is a side of
// a last binds none, as a last in a first does. What resolves a failure is the
// nearest fallback that follows a side holding the node, however deep, and a
// release carries that word; what a node that is only tested built is dropped
// without one.
func TestRenderNestedFirst(t *testing.T) {
	gated := firstGatePlan()
	Elaborate(gated)
	want := `assign s
    source
        s = sarr[9] > "a" || t > "b" u

    execute
        %t0 =
            first Str
                %t1 = check owned Str sarr[9] > "a" [on-oob=fallback]
            fallback
                %t2 = check borrowed(t) Str t > "b"
        gate %t0
            %t3 = eval borrowed Str u

    commit
        s = %t3 [copy] [on-skip=keep]
        drop %t1
        drop s [old]
`
	if got := gated.Render(true); got != want {
		t.Fatalf("gate render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}

	final := finalFirstPlan()
	Elaborate(final)
	want = `assign s
    source
        s = n > 0 && (sarr[9] > "a" || sarr[8] > "b")

    execute
        %t0 =
            last Str
                %t1 = check I64 n > 0
            next
                first Str
                    %t2 = check owned Str sarr[9] > "a" [on-oob=fallback]
                fallback
                    %t3 = check owned Str sarr[8] > "b" [on-oob=skip]

    commit
        s = %t0 [move] [on-skip=keep]
        drop %t2 [on-fallback]
        drop %t3 [on-skip]
        drop s [old]
`
	if got := final.Render(true); got != want {
		t.Fatalf("final first render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}

	nested := nestedFirstPlan()
	Elaborate(nested)
	want = `assign s
    source
        s = n > 0 && (sarr[9] > "a" || sarr[8] > "b") || h

    execute
        %t0 =
            first Str
                last Str
                    %t1 = check I64 n > 0
                next
                    first Str
                        %t2 = check owned Str sarr[9] > "a" [on-oob=fallback]
                    fallback
                        %t3 = check owned Str sarr[8] > "b" [on-oob=fallback]
            fallback
                %t4 = eval borrowed Str h

    commit
        s = %t0 [move %t2] [move %t3] [copy %t4]
        drop %t2 [on-fallback]
        drop %t3 [on-fallback]
        drop s [old]
`
	if got := nested.Render(true); got != want {
		t.Fatalf("nested render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}

	tested := testedFirstPlan()
	Elaborate(tested)
	want = `assign x
    source
        x = (sarr[9] || MkStr(b) > "a") && 7

    execute
        %t0 =
            last I64
                first Str
                    %t1 = eval owned Str sarr[9] [on-oob=fallback]
                fallback
                    %t2 = check owned Str MkStr(b) > "a"
            next
                %t3 = eval I64 7

    commit
        x = %t0 [on-skip=keep]
        drop %t1
        drop %t2
`
	if got := tested.Render(true); got != want {
		t.Fatalf("tested first render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// Plan §4, §9, §14: a gate's invariants, each broken on
// `s = sarr[1] > "m" sarr[9]`.
func TestValidateGateRejects(t *testing.T) {
	str := testType("Str")
	cases := []struct {
		name    string
		mutate  func(*AssignPlan)
		wantErr string
	}{
		{"TwoChecksBeforeGate", func(p *AssignPlan) {
			p.Execute = []Node{p.Execute[0], p.Execute[0], p.Execute[1]}
		}, "gate follows 2 nodes and takes one condition"},
		{"EvalBeforeGate", func(p *AssignPlan) {
			p.Execute[0] = &Eval{Result: 0, Expr: ident("t"), Slots: []Slot{borrowedSlot(str, "t")}}
		}, "gate follows *pir.Eval, which is no condition"},
		{"GateInsideGate", func(p *AssignPlan) { p.Gate().Body = []Node{&Gate{Cond: 0, Body: p.Gate().Body}} }, "unknown execute node *pir.Gate"},
		{"EmptyRegion", func(p *AssignPlan) { p.Gate().Body = nil }, "gate admits an empty region"},
		{"GateTakesAnotherOutcome", func(p *AssignPlan) { p.Gate().Cond = 1 }, "gate takes %t1 where its condition binds %t0"},
		{"SparseCheckID", func(p *AssignPlan) { p.Execute[0].(*Check).Result = 1 }, "check has result ID 1 where 0 is next; IDs must be dense"},
		{"ValueIDRepeatsCheck", func(p *AssignPlan) { p.Gate().Body[0].(*Eval).Result = 0 }, "eval 1 has result ID 0; IDs must be dense"},
		{"CheckWithTwoSlots", func(p *AssignPlan) {
			p.Condition().(*Check).Slots = []Slot{ownedSlot(str), ownedSlot(str)}
		}, "check %t0 has 2 slots"},
		{"CheckSlotWithoutType", func(p *AssignPlan) { p.Condition().(*Check).Slots[0].Type = nil }, "check %t0 slot 0 has no type"},
		{"CheckAccessNotChecked", func(p *AssignPlan) { p.Condition().(*Check).Checked = false }, "check %t0 holds a checked access and is not marked checked"},
		{"MappedCheck", func(p *AssignPlan) { p.Commit[0].Outcome.Outcome = 0 }, "commit references unknown outcome %t0"},
		{"NoPolicyUnderGate", func(p *AssignPlan) {
			p.Gate().Body[0] = &Eval{Result: 1, Expr: ident("t"), Slots: []Slot{borrowedSlot(str, "t")}}
			p.Commit[0].OnSkip = NoSkipPolicy
		}, "target s = %t1 slot 0 has skip policy none; its outcome and target require keep"},
		{"TestedNeverReleased", func(p *AssignPlan) { p.Tested = nil }, "owned outcome %t0 slot 0, which is only tested, is never released"},
		{"TestedDroppedTwice", func(p *AssignPlan) { p.Tested = []OutcomeRef{{Outcome: 0}, {Outcome: 0}} }, "tested %t0 slot 0 dropped twice"},
		{"TestedDropOfBorrow", func(p *AssignPlan) {
			p.Condition().(*Check).Slots[0] = borrowedSlot(str, "x")
		}, "drop of %t0 slot 0, which is not an owned outcome that is only tested"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := gatePlan()
			Elaborate(p)
			tc.mutate(p)
			err := Validate(p, sameSpelling)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// Plan §12: a check binds its outcome like an eval, and the gate that takes
// it sits beneath with its region one level in. Each binding says what it
// holds where the gate rejects, and what the check built is released first.
func TestRenderGate(t *testing.T) {
	p := gatePlan()
	Elaborate(p)
	want := `assign s
    source
        s = sarr[1] > "m" sarr[9]

    execute
        %t0 = check Str sarr[1] > "m" [on-oob=skip]
        gate %t0
            %t1 = eval Str sarr[9] [on-oob=skip]

    commit
        s = %t1 [on-skip=keep]
`
	if got := p.Render(false); got != want {
		t.Fatalf("concise render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	want = `assign s
    source
        s = sarr[1] > "m" sarr[9]

    execute
        %t0 = check owned Str sarr[1] > "m" [on-oob=skip]
        gate %t0
            %t1 = eval owned Str sarr[9] [on-oob=skip]

    commit
        s = %t1 [move] [on-skip=keep]
        drop %t0
        drop %t1 [on-skip]
        drop s [old]
`
	if got := p.Render(true); got != want {
		t.Fatalf("expanded render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// Plan §4, §14: a last's invariants, each broken on a value last,
// `s = sarr[1] > "m" && sarr[9]`, or on a gate's.
func TestValidateLastRejects(t *testing.T) {
	str := testType("Str")
	cases := []struct {
		name    string
		plan    func() *AssignPlan
		mutate  func(*AssignPlan)
		wantErr string
	}{
		{"SparseID", lastValuePlan, func(p *AssignPlan) { lastAt(p, 0).Result = 1 }, "last has result ID 1 where 0 is next; IDs must be dense"},
		{"NoType", lastValuePlan, func(p *AssignPlan) { lastAt(p, 0).Type = nil }, "last %t0 has no type"},
		{"OneSide", lastValuePlan, func(p *AssignPlan) { lastAt(p, 0).Sides = lastAt(p, 0).Sides[1:] }, "last %t0 has 1 sides and needs a next"},
		{"EvalBeforeFinal", lastValuePlan, func(p *AssignPlan) {
			lastAt(p, 0).Sides[0] = &Eval{Result: 1, Expr: ident("t"), Slots: []Slot{borrowedSlot(str, "t")}}
		}, "side %t1 of last %t0 is an eval before the final side, where a side is a check or a first"},
		{"SideIsNoCheckEvalOrFirst", lastValuePlan, func(p *AssignPlan) { lastAt(p, 0).Sides[0] = &Gate{} }, "side 1 of last %t0 is *pir.Gate, which is no check, eval or first"},
		{"CheckSideUnvalidated", lastValuePlan, func(p *AssignPlan) {
			lastAt(p, 0).Sides[0].(*Check).Slots = []Slot{ownedSlot(str), ownedSlot(str)}
		}, "check %t1 has 2 slots"},
		{"FinalEvalUnvalidated", lastValuePlan, func(p *AssignPlan) { lastAt(p, 0).Sides[1].(*Eval).Checked = false }, "eval %t2 holds a checked access and is not marked checked"},
		{"FinalEvalWithTwoSlots", lastValuePlan, func(p *AssignPlan) {
			lastAt(p, 0).Sides[1].(*Eval).Slots = []Slot{ownedSlot(str), ownedSlot(str)}
		}, "side %t2 of last %t0 has 2 slots"},
		{"FinalEvalOfAnotherType", lastValuePlan, func(p *AssignPlan) { lastAt(p, 0).Sides[1].(*Eval).Slots[0].Type = testType("I64") }, "side %t2 of type I64 does not fit last %t0 of type Str"},
		{"MappedSide", lastValuePlan, func(p *AssignPlan) { p.Commit[0].Outcome.Outcome = 2 }, "commit references %t2, which sits inside last %t0, whose outcome a mapping takes instead"},
		{"SlotOutOfRange", lastValuePlan, func(p *AssignPlan) { p.Commit[0].Outcome.Slot = 1 }, "commit references %t0 slot 1 out of range"},
		{"NoPolicy", lastValuePlan, func(p *AssignPlan) {
			lastAt(p, 0).Sides[1] = &Eval{Result: 2, Expr: ident("t"), Slots: []Slot{borrowedSlot(str, "t")}}
			p.Commit[0].Transfers, p.Skipped = []Transfer{Copy}, nil
			p.Commit[0].OnSkip = NoSkipPolicy
		}, "target s = %t0 slot 0 has skip policy none; its outcome and target require keep"},
		{"TestedSideNeverReleased", lastValuePlan, func(p *AssignPlan) { p.Tested = nil }, "owned outcome %t1 slot 0, which is only tested, is never released"},
		{"ConditionOfAnotherType", lastGatePlan, func(p *AssignPlan) { lastAt(p, 0).Type = testType("I64") }, "side %t2 of type Str does not fit last %t0 of type I64"},
		{"ConditionUnvalidated", lastGatePlan, func(p *AssignPlan) { lastAt(p, 0).Sides = lastAt(p, 0).Sides[:1] }, "last %t0 has 1 sides and needs a next"},
		{"GateTakesASide", lastGatePlan, func(p *AssignPlan) { p.Gate().Cond = 2 }, "gate takes %t2 where its condition binds %t0"},
		{"ValueIDRepeatsASide", lastGatePlan, func(p *AssignPlan) { p.Gate().Body[0].(*Eval).Result = 2 }, "eval 3 has result ID 2; IDs must be dense"},
		{"TestedConditionNeverReleased", lastGatePlan, func(p *AssignPlan) { p.Tested = nil }, "owned outcome %t1 slot 0, which is only tested, is never released"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.plan()
			Elaborate(p)
			tc.mutate(p)
			err := Validate(p, sameSpelling)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// Plan §12: a last prints as a first does, its sides in order under next. A
// gate takes the last's own outcome, and a mapping takes it by one transfer,
// the final side's. Each side that was only tested is dropped by name.
func TestRenderLast(t *testing.T) {
	gated := lastGatePlan()
	Elaborate(gated)
	want := `assign s
    source
        s = sarr[1] > "m" && t > "a" u

    execute
        %t0 =
            last Str
                %t1 = check Str sarr[1] > "m" [on-oob=skip]
            next
                %t2 = check Str t > "a"
        gate %t0
            %t3 = eval Str u

    commit
        s = %t3 [on-skip=keep]
`
	if got := gated.Render(false); got != want {
		t.Fatalf("concise gate render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}

	value := lastValuePlan()
	Elaborate(value)
	want = `assign s
    source
        s = sarr[1] > "m" && sarr[9]

    execute
        %t0 =
            last Str
                %t1 = check owned Str sarr[1] > "m" [on-oob=skip]
            next
                %t2 = eval owned Str sarr[9] [on-oob=skip]

    commit
        s = %t0 [move] [on-skip=keep]
        drop %t1
        drop %t2 [on-skip]
        drop s [old]
`
	if got := value.Render(true); got != want {
		t.Fatalf("expanded value render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// Plan §4, §14: a check a mapping takes is validated as a gate's is, and the
// mapping takes its one slot.
func TestValidateCheckRejects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*AssignPlan)
		wantErr string
	}{
		{"CheckUnvalidated", func(p *AssignPlan) { p.Execute[0].(*Check).Checked = true }, "check %t0 is marked checked and holds no checked access"},
		{"SlotOutOfRange", func(p *AssignPlan) { p.Commit[0].Outcome.Slot = 1 }, "commit references %t0 slot 1 out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := checkPlan()
			Elaborate(p)
			tc.mutate(p)
			err := Validate(p, sameSpelling)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// Plan §12: a check a mapping takes prints as a gate's does, and what it built
// is dropped where it skipped.
func TestRenderCheck(t *testing.T) {
	p := checkPlan()
	Elaborate(p)
	want := `assign s
    source
        s = MkStr(n) > "a"

    execute
        %t0 = check Str MkStr(n) > "a"

    commit
        s = %t0 [on-skip=keep]
`
	if got := p.Render(false); got != want {
		t.Fatalf("concise render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	want = `assign s
    source
        s = MkStr(n) > "a"

    execute
        %t0 = check owned Str MkStr(n) > "a"

    commit
        s = %t0 [move] [on-skip=keep]
        drop %t0 [on-skip]
        drop s [old]
`
	if got := p.Render(true); got != want {
		t.Fatalf("expanded render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
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
			p.Evals[0].Expr, p.Evals[0].Checked = read("arr", 0), true
		}, "print takes %t0, which may fail"},
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
    source
        "x: -x", s, Pair(n)

    execute
        %t0 = eval Str s
        %t1 = eval I64, Str Pair(n)
        print "x: -x", %t0, %t1#0, %t1#1
`
	if got := p.Render(false); got != want {
		t.Fatalf("print render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	wantExpanded := `print
    source
        "x: -x", s, Pair(n)

    execute
        %t0 = eval borrowed Str s
        %t1 = eval I64, owned Str Pair(n)
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
		Execute: []Node{
			&Eval{Result: 0, Expr: intLit(5), Slots: []Slot{unmanagedSlot(testType("I64"))}},
			&Eval{Result: 1, Expr: intLit(7), Slots: []Slot{unmanagedSlot(testType("I64"))}},
		},
		Commit: []Mapping{
			{Target: local("x", testType("I64")), Outcome: OutcomeRef{Outcome: 0}},
			{Target: Target{Kind: DiscardTarget}, Outcome: OutcomeRef{Outcome: 1}},
		},
	}
	want := `assign x, _
    source
        x, _ = 5, 7

    execute
        %t0 = eval I64 5
        %t1 = eval I64 7

    commit
        x = %t0
        _ = %t1
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
		Execute: []Node{
			&Eval{Result: 0, Expr: call("Pair", ident("x")), Slots: []Slot{unmanagedSlot(testType("I64")), ownedSlot(testType("Str"))}},
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
    source
        a, b = Pair(x)

    execute
        %t0 = eval I64, Str Pair(x)

    commit
        a = %t0#0
        b = %t0#1
`
	if got := p.Render(false); got != want {
		t.Fatalf("multi-output render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	wantExpanded := `assign a, b
    source
        a, b = Pair(x)

    execute
        %t0 = eval I64, owned Str Pair(x)

    commit
        a = %t0#0
        b = %t0#1 [move]
        drop b [old]
`
	if got := p.Render(true); got != wantExpanded {
		t.Fatalf("multi-output expanded render mismatch:\ngot:\n%s\nwant:\n%s", got, wantExpanded)
	}
}

// Plan §6, §12: a function body's plan sits one level in, under the header
// its body opens with, and an output mapping is spelled `output`.
func TestRenderFunctionOutput(t *testing.T) {
	p := joinPlan()
	Elaborate(p)
	if got, want := FuncHeader(p.Func), "function Str = Join(Str, Str)\n"; got != want {
		t.Fatalf("function header mismatch: got %q, want %q", got, want)
	}

	want := `    assign res
        source
            res = a ⊕ b

        execute
            %t0 = eval Str a ⊕ b

        commit
            output res = %t0
`
	if got := p.Render(false); got != want {
		t.Fatalf("function output render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	wantExpanded := `    assign res
        source
            res = a ⊕ b

        execute
            %t0 = eval owned Str a ⊕ b

        commit
            output res = %t0 [move]
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
			evalAt(p, 0).Expr = tt.expr
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

// Plan §12: the expanded view adds nothing to an unmanaged plan: an unmanaged
// slot carries no ownership word and an unmanaged store no transfer annotation.
func TestRenderExpanded(t *testing.T) {
	p := swapPlan()
	Elaborate(p)
	got := p.Render(true)
	want := `assign a, b
    source
        a, b = b, a

    execute
        %t0 = eval I64 b
        %t1 = eval I64 a

    commit
        a = %t0
        b = %t1
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
    source
        a, b = b, a

    execute
        %t0 = eval borrowed Str b
        %t1 = eval borrowed Str a

    commit
        a = %t0 [move]
        b = %t1 [move]
`
	heapSwap := heapSwapPlan()
	Elaborate(heapSwap)
	if got := heapSwap.Render(true); got != want {
		t.Fatalf("heap swap render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	want = `assign x, _
    source
        x, _ = x ⊕ "!", "a" ⊕ "b"

    execute
        %t0 = eval owned Str x ⊕ "!"
        %t1 = eval owned Str "a" ⊕ "b"

    commit
        x = %t0 [move]
        _ = %t1
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
    source
        x, _ = x ⊕ "!", "a" ⊕ "b"

    execute
        %t0 = eval Str x ⊕ "!"
        %t1 = eval Str "a" ⊕ "b"

    commit
        x = %t0
        _ = %t1
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
    source
        kept, fresh, _ = sarr[9], sarr[0], sarr[1]

    execute
        %t0 = eval Str sarr[9] [on-oob=skip]
        %t1 = eval Str sarr[0] [on-oob=skip]
        %t2 = eval Str sarr[1] [on-oob=skip]

    commit
        kept = %t0 [on-skip=keep]
        fresh = %t1 [on-skip=zero]
        _ = %t2
`
	if got := p.Render(false); got != want {
		t.Fatalf("concise render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	want = `assign kept, fresh, _
    source
        kept, fresh, _ = sarr[9], sarr[0], sarr[1]

    execute
        %t0 = eval owned Str sarr[9] [on-oob=skip]
        %t1 = eval owned Str sarr[0] [on-oob=skip]
        %t2 = eval owned Str sarr[1] [on-oob=skip]

    commit
        kept = %t0 [move] [on-skip=keep]
        fresh = %t1 [move] [on-skip=zero]
        _ = %t2
        drop %t0 [on-skip]
        drop %t1 [on-skip]
        drop %t2
        drop kept [old]
`
	if got := p.Render(true); got != want {
		t.Fatalf("expanded render mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// Plan §12: a plan's source prints as the lines it was given, one level in,
// so a literal that spans lines reads as it is laid out. Every character a reader
// could not otherwise see is escaped, a line of spaces keeps its own
// indentation at either level, and only an empty line stays bare, since those
// separate a plan's sections.
func TestRenderSource(t *testing.T) {
	cases := []struct {
		name   string
		source string
		fn     string
		want   string
	}{
		{"OneLine", "a, b = b, a", "", "        a, b = b, a"},
		{"Lines", "m = [\n  1 2\n  3 4\n]", "", "        m = [\n          1 2\n          3 4\n        ]"},
		{"EmptyLine", "s = \"a\n\nb\"", "", "        s = \"a\n\n        b\""},
		{"SpaceOnlyLine", "s = \"a\n \nb\"", "", "        s = \"a\n         \n        b\""},
		{"TabOnlyLine", "s = \"a\n\t\nb\"", "", "        s = \"a\n        \\t\n        b\""},
		{"Controls", "s = \"a\x01b\x7fc\td\"", "", "        s = \"a\\x01b\\x7fc\\td\""},
		{"Invisible", "s = \"x\u200by\"", "", "        s = \"x\\u200by\""},
		{"QuoteAndBackslash", `s = "a\"b\\c"`, "", "        s = \"a\\\"b\\\\c\""},
		{"CRLFIsOneBreak", "s = \"a\r\nb\"", "", "        s = \"a\n        b\""},
		{"CRIsOneBreak", "s = \"a\rb\"", "", "        s = \"a\n        b\""},
		{"LinesInFunctionBody", "m = [\n  1 2\n]", "I64 = F(I64)", "            m = [\n              1 2\n            ]"},
		{"EmptyLineInFunctionBody", "s = \"a\n\nb\"", "I64 = F(I64)", "            s = \"a\n\n            b\""},
		{"SpaceOnlyLineInFunctionBody", "s = \"a\n \nb\"", "I64 = F(I64)", "            s = \"a\n             \n            b\""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := swapPlan()
			p.Source = tc.source
			p.Func = tc.fn
			head := "assign a, b\n    source\n"
			if tc.fn != "" {
				head = "    assign a, b\n        source\n"
			}
			got := p.Render(false)
			if want := head + tc.want + "\n\n"; !strings.HasPrefix(got, want) {
				t.Fatalf("source mismatch:\ngot:\n%q\nwant prefix:\n%q", got, want)
			}
		})
	}
}

// Plan §12: control characters and Unicode line breaks inside string
// literals never break the one-operation-per-line format, at the root or
// nested under an operator; ordinary non-ASCII stays raw. A terminator the
// file spells CR is one line break, so it renders as the escape a source
// asks for with `\r` does not.
func TestRenderEscapesControls(t *testing.T) {
	multi := strLit("a\nb\tc\rd\x01e\x7f")
	cases := []struct {
		expr ast.Expression
		want string
	}{
		{multi, `%t0 = eval Str "a\nb\tc\nd\x01e\x7f"`},
		{concat(multi, strLit("z")), `%t0 = eval Str "a\nb\tc\nd\x01e\x7f" ⊕ "z"`},
		{strLit("a\r\nb"), `%t0 = eval Str "a\nb"`},
		{strLit(`a\rb`), `%t0 = eval Str "a\rb"`},
		{strLit("π\u0085x\u2028y\u2029z\u009f"), `%t0 = eval Str "π\u0085x\u2028y\u2029z\u009f"`},
		{strLit("x\u200by\U000e0001z"), `%t0 = eval Str "x\u200by\U000e0001z"`},
	}
	for _, tc := range cases {
		p := swapPlan()
		p.Execute = p.Execute[:1]
		evalAt(p, 0).Expr = tc.expr
		evalAt(p, 0).Slots[0].Type = testType("Str")
		p.Commit = p.Commit[:1]
		got := p.Render(false)
		if !strings.Contains(got, "        "+tc.want+"\n") || strings.Count(got, "\n") != 9 {
			t.Fatalf("render mismatch:\n%s", got)
		}
	}
}

// Plan §12: a slot's ownership prints before its own type, so two layouts
// over identical displayed types render differently, and a borrow whose
// operand is not its owner's name names the owner.
func TestRenderMultiSlotOwnershipPositions(t *testing.T) {
	str := testType("Str")
	first := renderDiscardedPair(t, []Slot{unmanagedSlot(str), borrowedSlot(str, "x")})
	second := renderDiscardedPair(t, []Slot{borrowedSlot(str, "x"), unmanagedSlot(str)})
	if !strings.Contains(first, "eval Str, borrowed(x) Str pair\n") {
		t.Fatalf("first layout lost its positions:\n%s", first)
	}
	if !strings.Contains(second, "eval borrowed(x) Str, Str pair\n") {
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
		Head:    Head{Label: "assign _, _", Source: "_, _ = pair"},
		Execute: []Node{&Eval{Result: 0, Expr: ident("pair"), Slots: slots}},
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
