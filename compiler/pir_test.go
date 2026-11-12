package compiler

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/pir"
	"tinygo.org/x/go-llvm"
)

// compileScriptPlans runs the full script pipeline and returns the statement
// plans the PIR router accepted, in source order.
func compileScriptPlans(t *testing.T, ctx llvm.Context, name, code, script string) []*pir.AssignPlan {
	t.Helper()
	cc := NewCodeCompiler(ctx, name, "", mustParseCode(t, code))
	require.Empty(t, cc.Compile())
	sc := NewScriptCompiler(ctx, name, mustParseScript(t, script), cc)
	if code != "" {
		linkCodeModuleForTest(t, ctx, sc.Compiler.Module, cc.Compiler.Module)
	}
	require.Empty(t, sc.Compile())
	return sc.Plans
}

func planLabels(plans []*pir.AssignPlan) []string {
	labels := make([]string, len(plans))
	for i, p := range plans {
		labels[i] = p.Label
	}
	return labels
}

// Plan §6 (simultaneous commit, swap) and §12 (text form).
func TestPlanGolden(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planGolden", "", `a = 5
b = a + 2 * 3
a, b = b, a
_ = 7
r = 0:10:2
s = r
a, b
s`)

	require.Equal(t, []string{"assign a", "assign b", "assign a, b", "assign _", "assign r", "assign s"}, planLabels(plans))

	require.Equal(t, `statement assign a, b
    source "a, b = b, a"

    execute
        %t0 = eval I64 b
        %t1 = eval I64 a

    commit
        a <- %t0
        b <- %t1
`, plans[2].Render(false))

	require.Equal(t, `statement assign b
    source "b = (a + (2 * 3))"

    execute
        %t0 = eval I64 a + (2 * 3)

    commit
        I64 b <- %t0
`, plans[1].Render(true))

	require.Equal(t, `statement assign _
    source "_ = 7"

    execute
        %t0 = eval I64 7

    commit
        _ <- %t0
`, plans[3].Render(false))

	require.Equal(t, `statement assign r
    source "r = 0:10:2"

    execute
        %t0 = eval I64:I64:I64 0:10:2

    commit
        r <- %t0
`, plans[4].Render(false))

	require.Equal(t, `statement assign s
    source "s = r"

    execute
        %t0 = eval I64:I64:I64 r

    commit
        s <- %t0
`, plans[5].Render(false))
}

// TestPlanRouterRejections pins the capability boundary: statements with
// gates, conditional values, checked accesses, ranged RHS, and block-layout
// literals keep their legacy lowering, and so does a call given a range or
// an argument that can fail (matrix rows 13, 6c), while ordinary heap
// values — both string flavours, concatenations, inline array literals —
// and calls with ordinary arguments plan alongside scalars and Range
// descriptors.
func TestPlanRouterRejections(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planRejections", `c = Twice(a)
    c = a * 2
`, `x = 5
g = x > 2 13
y = 0
y = x > 2
s = "hi"
sg = s
sh = s ⊕ "!"
shc = sh
arr = [1 2]
z = arr[0]
q = 0:3
w = q + 1
_ = 0:3
d = Twice(x)
dq = Twice(q)
dz = Twice(arr[0])
dc = Twice(x > 2)
m = [
    1 2
    3 4
]
tbl = [
  : Name Score
    "Ada" 1
]
rc = [q]
cc = [x > 2]
g, y, sg, shc, z, w, d, dq, dz, dc, m, tbl, rc, cc`)

	require.Equal(t, []string{"assign x", "assign y", "assign s", "assign sg", "assign sh", "assign shc", "assign arr", "assign q", "assign _", "assign d"}, planLabels(plans))
}

func TestPlanValueTypeSupported(t *testing.T) {
	require.True(t, planValueTypeSupported(I64))
	require.True(t, planValueTypeSupported(F64))
	require.True(t, planValueTypeSupported(Range{Iter: I64}))
	require.True(t, planValueTypeSupported(StrG{}))
	require.True(t, planValueTypeSupported(StrH{}))
	require.True(t, planValueTypeSupported(Array{ElemType: I64, Rank: 1}))
	require.True(t, planValueTypeSupported(Table{Columns: []TableColumn{{Name: "Score", ElemType: I64}}}))
	require.False(t, planValueTypeSupported(Array{ElemType: Unresolved{}, Rank: 1}))
	require.False(t, planValueTypeSupported(ArrayRange{Array: Array{ElemType: I64, Rank: 1}, Range: Range{Iter: I64}}))
	require.False(t, planValueTypeSupported(Func{}))
	require.True(t, planValueTypesSupported([]Type{I64, StrH{}}))
	require.False(t, planValueTypesSupported([]Type{I64, Func{}}))
}

// TestPlanRouterScriptRootOnly: the call plans, the statements of the body it
// compiles produce no plans, and the assignment after the call pins that lazy
// specialization compilation restores FuncNameMangled to the root key
// (bindingSlotType relies on it).
func TestPlanRouterScriptRootOnly(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planScriptRoot", `c = addOne(a)
    t = a + 1
    c = t * 1
`, `seed = 2
res = addOne(seed)
after = seed + 1
res, after`)

	require.Equal(t, []string{"assign seed", "assign res", "assign after"}, planLabels(plans))
}

func TestPlanRouterFreshVsExistingTargets(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	// The gated statement promotes x to memory; the final plain assignment
	// must still plan.
	plans := compileScriptPlans(t, ctx, "planPromoted", "", `x = 1
x = x > 0 5
x = x + 1
x`)

	require.Equal(t, []string{"assign x", "assign x"}, planLabels(plans))
}

// TestInvalidPlanPanicsBeforeLowering drives the production
// build -> validate -> panic wiring: corrupting the solver's binding type is
// builder drift entering through the exact facts the builder reads. The
// panic must fire before the plan is recorded, its target installed, or any
// following statement compiled.
func TestInvalidPlanPanicsBeforeLowering(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "planInvalid", "", ast.NewCode())
	require.Empty(t, cc.Compile())
	sc := NewScriptCompiler(ctx, "planInvalid", mustParseScript(t, "v = 1\nu = v + 1\nu"), cc)
	ts := NewTypeSolver(sc)
	ts.Solve()
	require.Empty(t, ts.Errors)

	sc.Script.Root.Vars["v"] = F64
	sc.Compiler.addMain()

	defer func() {
		r := recover()
		require.NotNil(t, r, "invalid plan must panic")
		require.Contains(t, fmt.Sprint(r), `invalid plan for "v = 1"`)
		require.Empty(t, sc.Plans)
		_, vBound := Get(sc.Compiler.Scopes, "v")
		require.False(t, vBound)
		_, uBound := Get(sc.Compiler.Scopes, "u")
		require.False(t, uBound)
	}()
	sc.compileStatements()
	t.Fatal("compileStatements returned normally")
}

func TestPlanEvalReferencesSolvedAST(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	program := mustParseScript(t, "v = 1 + 2\nv")
	cc := NewCodeCompiler(ctx, "planAST", "", ast.NewCode())
	require.Empty(t, cc.Compile())
	sc := NewScriptCompiler(ctx, "planAST", program, cc)
	require.Empty(t, sc.Compile())

	plans := sc.Plans
	require.Len(t, plans, 1)
	stmt := program.Statements[0].(*ast.LetStatement)
	require.Same(t, stmt.Value[0], plans[0].Evals[0].Expr)
	require.Equal(t, I64, plans[0].Commit[0].Target.Type)
}

// Plan §6 (discard target) and §12: a discarded bare Range descriptor is an
// unmanaged outcome with no release obligation, so it plans like a scalar.
func TestPlanGoldenRangeDiscard(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planRangeDiscard", "", "_ = 0:3")
	require.Equal(t, []string{"assign _"}, planLabels(plans))
	require.Equal(t, `statement assign _
    source "_ = 0:3"

    execute
        %t0 = eval I64:I64:I64 0:3

    commit
        _ <- %t0
`, plans[0].Render(false))
}

// Plan §12: plan labels and bindings are Pluto identifiers, Unicode included.
func TestPlanGoldenUnicode(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planUnicode", "", "π = 3.14\nτ = π\nτ")
	require.Equal(t, []string{"assign π", "assign τ"}, planLabels(plans))
	require.Equal(t, `statement assign π
    source "π = 3.14"

    execute
        %t0 = eval F64 3.14

    commit
        π <- %t0
`, plans[0].Render(false))
	require.Equal(t, `statement assign τ
    source "τ = π"

    execute
        %t0 = eval F64 π

    commit
        τ <- %t0
`, plans[1].Render(false))
}

// Plan §12: a payload drops its outermost parentheses; a prefix stays bare.
func TestPlanGoldenPrefix(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planPrefix", "", "a = 1\nn = -a\nn")
	require.Equal(t, []string{"assign a", "assign n"}, planLabels(plans))
	require.Equal(t, `statement assign n
    source "n = (-a)"

    execute
        %t0 = eval I64 -a

    commit
        n <- %t0
`, plans[1].Render(false))
}

// Plan §12: source bindings render bare, so a binding named t0 stays distinct
// from temporary %t0, and a binding named discard from the _ sink.
func TestPlanGoldenBareBindings(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planBare", "", "t0 = 1\nx = t0\ndiscard = 2\n_ = 3\nx, discard")
	require.Equal(t, []string{"assign t0", "assign x", "assign discard", "assign _"}, planLabels(plans))
	require.Equal(t, `statement assign x
    source "x = t0"

    execute
        %t0 = eval I64 t0

    commit
        x <- %t0
`, plans[1].Render(false))
	require.Equal(t, `statement assign t0
    source "t0 = 1"

    execute
        %t0 = eval I64 1

    commit
        t0 <- %t0
`, plans[0].Render(false))
	require.Equal(t, `statement assign discard
    source "discard = 2"

    execute
        %t0 = eval I64 2

    commit
        discard <- %t0
`, plans[2].Render(false))
	require.Equal(t, `statement assign _
    source "_ = 3"

    execute
        %t0 = eval I64 3

    commit
        _ <- %t0
`, plans[3].Render(false))
}

// Plan §6, §8, §17: a heap swap is two moved borrows — zero copies, zero
// releases — while the fresh bindings before it move their owned outcomes.
func TestPlanGoldenHeapSwap(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planHeapSwap", "", `h1 = "foo" ⊕ "bar"
h2 = "baz" ⊕ "qux"
h1, h2 = h2, h1
h1, h2`)
	require.Equal(t, []string{"assign h1", "assign h2", "assign h1, h2"}, planLabels(plans))
	require.Equal(t, `statement assign h1
    source "h1 = (\"foo\" ⊕ \"bar\")"

    execute
        %t0 = eval Str [owned] "foo" ⊕ "bar"

    commit
        Str h1 <- %t0 [move]
`, plans[0].Render(true))
	require.Equal(t, `statement assign h1, h2
    source "h1, h2 = h2, h1"

    execute
        %t0 = eval Str [borrowed=h2] h2
        %t1 = eval Str [borrowed=h1] h1

    commit
        Str h1 <- %t0 [move]
        Str h2 <- %t1 [move]
`, plans[2].Render(true))
}

// Plan §8, §17: one owned source feeding two targets is taken once and
// copied once, and the target whose old value nothing took releases it.
func TestPlanGoldenDuplicateSource(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planDupSource", "", `d1 = "dup" ⊕ "test"
d2 = "other" ⊕ "!"
d1, d2
d1, d2 = d1, d1
d1, d2`)
	require.Equal(t, `statement assign d1, d2
    source "d1, d2 = d1, d1"

    execute
        %t0 = eval Str [borrowed=d1] d1
        %t1 = eval Str [borrowed=d1] d1

    commit
        Str d1 <- %t0 [move]
        Str d2 <- %t1 [copy]
        drop d2 [old]
`, plans[2].Render(true))
}

// Plan §6, §8: replacing an owned value moves the outcome and releases the
// old value after the mapping; a discarded owned outcome is released, a
// discarded borrow is not; a mixed group shows move, transfer, and release
// together.
func TestPlanGoldenReplaceAndDiscard(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planReplace", "", `x = "a" ⊕ "b"
x = x ⊕ "!"
_ = x ⊕ "?"
_ = x
y = "c" ⊕ "d"
x, y = y ⊕ "!", x
x, y`)
	require.Equal(t, []string{"assign x", "assign x", "assign _", "assign _", "assign y", "assign x, y"}, planLabels(plans))
	require.Equal(t, `statement assign x
    source "x = (x ⊕ \"!\")"

    execute
        %t0 = eval Str [owned] x ⊕ "!"

    commit
        Str x <- %t0 [move]
        drop x [old]
`, plans[1].Render(true))
	require.Equal(t, `statement assign _
    source "_ = (x ⊕ \"?\")"

    execute
        %t0 = eval Str [owned] x ⊕ "?"

    commit
        _ <- %t0
        drop %t0
`, plans[2].Render(true))
	require.Equal(t, `statement assign _
    source "_ = x"

    execute
        %t0 = eval Str [borrowed=x] x

    commit
        _ <- %t0
`, plans[3].Render(true))
	require.Equal(t, `statement assign x, y
    source "x, y = (y ⊕ \"!\"), x"

    execute
        %t0 = eval Str [owned] y ⊕ "!"
        %t1 = eval Str [borrowed=x] x

    commit
        Str x <- %t0 [move]
        Str y <- %t1 [move]
        drop y [old]
`, plans[5].Render(true))
}

// Plan §8, §12: a static string into a heap-string binding is copied into an
// owned heap string, which the directional compatibility relation admits
// though both flavours display as Str; a binding that only ever holds static
// strings owns nothing and stores plainly.
func TestPlanGoldenStaticIntoHeap(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planStaticIntoHeap", "", `s = "hi"
s = s ⊕ "!"
g = "static"
t = g
s, t`)
	require.Equal(t, []string{"assign s", "assign s", "assign g", "assign t"}, planLabels(plans))
	require.Equal(t, `statement assign s
    source "s = \"hi\""

    execute
        %t0 = eval Str "hi"

    commit
        Str s <- %t0 [copy]
`, plans[0].Render(true))
	require.Equal(t, `statement assign t
    source "t = g"

    execute
        %t0 = eval Str g

    commit
        Str t <- %t0
`, plans[3].Render(true))
}

// Plan §8, §12: inline array literals are owned outcomes, an array read is
// a borrow that copies, and an empty-literal reset is an unmanaged value
// copied into the owning binding — the second case the directional
// relation exists for.
func TestPlanGoldenArrays(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planArrays", "", `arr1 = [1 2 3]
arr2 = arr1
arr1, arr2
arr2 = [4 5 6]
arr1 = []0
arr1, arr2`)
	require.Equal(t, []string{"assign arr1", "assign arr2", "assign arr2", "assign arr1"}, planLabels(plans))
	require.Equal(t, `statement assign arr1
    source "arr1 = [1 2 3]"

    execute
        %t0 = eval [I64] [owned] [1 2 3]

    commit
        [I64] arr1 <- %t0 [move]
`, plans[0].Render(true))
	require.Equal(t, `statement assign arr2
    source "arr2 = arr1"

    execute
        %t0 = eval [I64] [borrowed=arr1] arr1

    commit
        [I64] arr2 <- %t0 [copy]
`, plans[1].Render(true))
	require.Equal(t, `statement assign arr2
    source "arr2 = [4 5 6]"

    execute
        %t0 = eval [I64] [owned] [4 5 6]

    commit
        [I64] arr2 <- %t0 [move]
        drop arr2 [old]
`, plans[2].Render(true))
	require.Equal(t, `statement assign arr1
    source "arr1 = []0"

    execute
        %t0 = eval [I64] [owned] []0

    commit
        [I64] arr1 <- %t0 [move]
        drop arr1 [old]
`, plans[3].Render(true))
}

// Plan §16 Step 4, §17: a struct field read (matrix row 2b) and a struct
// value copy (35b) are unmanaged; a table column read (36g) is an owned
// copy and a table value copy (36b) a borrow. The table literal itself is a
// block-layout literal and stays legacy.
func TestPlanGoldenStructAndTable(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planStructTable", `p = Person
  : name age
    "Tejas" 35
`, `n = p.name
a = p.age
s2 = p
scores = [
  : Name Score
    "Ada" 10
]
col = scores.Score
t2 = scores
n, a, s2.age, col, t2`)
	require.Equal(t, []string{"assign n", "assign a", "assign s2", "assign col", "assign t2"}, planLabels(plans))
	require.Equal(t, `statement assign n
    source "n = p.name"

    execute
        %t0 = eval Str p.name

    commit
        Str n <- %t0
`, plans[0].Render(true))
	require.Equal(t, `statement assign s2
    source "s2 = p"

    execute
        %t0 = eval Person{name:Str age:I64} p

    commit
        Person{name:Str age:I64} s2 <- %t0
`, plans[2].Render(true))
	require.Equal(t, `statement assign col
    source "col = scores.Score"

    execute
        %t0 = eval [I64] [owned] scores.Score

    commit
        [I64] col <- %t0 [move]
`, plans[3].Render(true))
	require.Equal(t, `statement assign t2
    source "t2 = scores"

    execute
        %t0 = eval Table[Name:Str Score:I64] [borrowed=scores] scores

    commit
        Table[Name:Str Score:I64] t2 <- %t0 [copy]
`, plans[4].Render(true))
}

// Plan §8: ownership is read from a binding's effective storage, not the
// solver's flow-typed read. text solves as a static string after
// `text = "old"` but stores a materialized heap copy, so its read borrows.
// other takes text's heap buffer by transfer and is declared heap like text,
// so replacing it copies the literal and releases the held value. pir's
// TestElaborate pins a target declared static that holds heap.
func TestPlanGoldenEffectiveStorage(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planEffective", "", `text = "old"
text, other = text ⊕ "!", text
copy = other
other = "new"
copy, other, text`)
	require.Equal(t, []string{"assign text", "assign text, other", "assign copy", "assign other"}, planLabels(plans))
	require.Equal(t, `statement assign text, other
    source "text, other = (text ⊕ \"!\"), text"

    execute
        %t0 = eval Str [owned] text ⊕ "!"
        %t1 = eval Str [borrowed=text] text

    commit
        Str text <- %t0 [move]
        Str other <- %t1 [move]
`, plans[1].Render(true))
	require.Equal(t, `statement assign copy
    source "copy = other"

    execute
        %t0 = eval Str [borrowed=other] other

    commit
        Str copy <- %t0 [copy]
`, plans[2].Render(true))
	require.Equal(t, `statement assign other
    source "other = \"new\""

    execute
        %t0 = eval Str "new"

    commit
        Str other <- %t0 [copy]
        drop other [old]
`, plans[3].Render(true))
	require.True(t, plans[3].Commit[0].Target.TypeOwnsHeap)
	require.True(t, plans[3].Commit[0].Target.HoldsHeap)
}

// Plan §12: a multiline string literal is one eval operand on one line, its
// control characters escaped; the source line is quoted the same way.
func TestPlanGoldenMultilineString(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planMultiline", "", "s = \"line one\nline two\"\ns")
	require.Equal(t, `statement assign s
    source "s = \"line one\nline two\""

    execute
        %t0 = eval Str "line one\nline two"

    commit
        s <- %t0
`, plans[0].Render(false))
}

// Plan §16 Step 4: a column read off a copy of a typed empty table plans like
// one off the full table, since the copy's read type is its storage.
func TestPlanGoldenTypedEmptyTableReceiver(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planTypedEmptyReceiver", "", `scores = [
  : Name Value
    "Ada" 10
]
headerOnly = [
  : Name("") Value(0)
]
taken = headerOnly
headerOnly = scores
col = taken.Value
direct = scores.Value
col, direct, taken, headerOnly`)
	require.Equal(t, []string{"assign taken", "assign headerOnly", "assign col", "assign direct"}, planLabels(plans))
	require.Equal(t, `statement assign col
    source "col = taken.Value"

    execute
        %t0 = eval [I64] [owned] taken.Value

    commit
        [I64] col <- %t0 [move]
`, plans[2].Render(true))
}

// Plan §5, §6, §16 Step 4: a call stays inside its eval, alone or under an
// operator, and may read the target it commits to (matrix row 6). A
// multi-output call is one eval with a slot per output, owned when its type
// holds heap state (6b); each mapping names its outcome and slot, and a
// discarded owned slot is released (3, 5c). A struct or static string
// output is unmanaged (35d).
func TestPlanGoldenCalls(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planCalls", `p = Person
  : name age
    "Tejas" 35

c = Twice(a)
    c = a * 2

s1, s2 = twoStr(left, right)
    s1, s2 = left ⊕ "L", right ⊕ "R"

out, tag = Copy(current)
    out = current
    tag = out.name
`, `d = 5
d = Twice(d) + 1
l, r = twoStr("a", "b")
l, _, n = twoStr(l, r), 9
copied, tag = Copy(p)
d, l, r, n, copied.age, tag`)
	require.Equal(t, []string{"assign d", "assign d", "assign l, r", "assign l, _, n", "assign copied, tag"}, planLabels(plans))
	require.Equal(t, `statement assign d
    source "d = (Twice(d) + 1)"

    execute
        %t0 = eval I64 Twice(d) + 1

    commit
        I64 d <- %t0
`, plans[1].Render(true))
	require.Equal(t, `statement assign l, r
    source "l, r = twoStr(\"a\", \"b\")"

    execute
        %t0 = eval Str [owned], Str [owned] twoStr("a", "b")

    commit
        Str l <- %t0#0 [move]
        Str r <- %t0#1 [move]
`, plans[2].Render(true))
	require.Equal(t, `statement assign l, _, n
    source "l, _, n = twoStr(l, r), 9"

    execute
        %t0 = eval Str [owned], Str [owned] twoStr(l, r)
        %t1 = eval I64 9

    commit
        Str l <- %t0#0 [move]
        _ <- %t0#1
        I64 n <- %t1
        drop %t0#1
        drop l [old]
`, plans[3].Render(true))
	require.Equal(t, `statement assign copied, tag
    source "copied, tag = Copy(p)"

    execute
        %t0 = eval Person{name:Str age:I64}, Str Copy(p)

    commit
        Person{name:Str age:I64} copied <- %t0#0
        Str tag <- %t0#1
`, plans[4].Render(true))
}

// A planned statement moves the bindings it passes by reference to memory
// before any of its evals run, as a legacy statement does
// (promoteStatementBindings). arr is stored ahead of the call to Bump, not
// where First reads it.
func TestPlanPromotesReferenceArgsFirst(t *testing.T) {
	ir, _ := compileScriptAndCodeIR(t, "planPromotion", `r = Bump(x)
    r = x + 1

n = First(v)
    n = 0
    n = v[0]
`, `arr = [5 6]
a, b = Bump(1), First(arr)
a, b`)

	promoted := strings.Index(ir, "store ptr %arr_new, ptr %arr.mem")
	firstEval := strings.Index(ir, "%Bump_ret = call")
	require.NotEqual(t, -1, promoted, "expected arr to be stored to memory")
	require.NotEqual(t, -1, firstEval, "expected the call to Bump")
	require.Less(t, promoted, firstEval, "arr must reach memory before the statement's first eval")
}
