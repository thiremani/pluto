package compiler

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/pir"
	"tinygo.org/x/go-llvm"
)

// compileScript runs the full script pipeline.
func compileScript(t *testing.T, ctx llvm.Context, name, code, script string) *ScriptCompiler {
	t.Helper()
	cc := NewCodeCompiler(ctx, name, "", mustParseCode(t, code))
	require.Empty(t, cc.Compile())
	sc := NewScriptCompiler(ctx, name, mustParseScript(t, script), cc)
	if code != "" {
		linkCodeModuleForTest(t, ctx, sc.Compiler.Module, cc.Compiler.Module)
	}
	require.Empty(t, sc.Compile())
	return sc
}

// compileScriptPlans returns the script root's assignment plans, in source
// order.
func compileScriptPlans(t *testing.T, ctx llvm.Context, name, code, script string) []*pir.AssignPlan {
	t.Helper()
	return assignPlans(compileScript(t, ctx, name, code, script).Compiler.Plans[0])
}

func assignPlans(plans []pir.Plan) []*pir.AssignPlan {
	var assigns []*pir.AssignPlan
	for _, p := range plans {
		if assign, isAssign := p.(*pir.AssignPlan); isAssign {
			assigns = append(assigns, assign)
		}
	}
	return assigns
}

func planLabels[P pir.Plan](plans []P) []string {
	labels := make([]string, len(plans))
	for i, p := range plans {
		labels[i] = p.Heading().Label
	}
	return labels
}

// planOwner names a plan by the function it belongs to, empty at the script
// root, and by its label.
type planOwner struct {
	Func  string
	Label string
}

func planOwners(plans []pir.Plan) []planOwner {
	owners := make([]planOwner, len(plans))
	for i, p := range plans {
		owners[i] = planOwner{Func: p.Heading().Func, Label: p.Heading().Label}
	}
	return owners
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

	require.Equal(t, `assign a, b
    source "a, b = b, a"

    execute
        %t0 = eval I64 b
        %t1 = eval I64 a

    commit
        a = %t0
        b = %t1
`, plans[2].Render(false))

	require.Equal(t, `assign b
    source "b = (a + (2 * 3))"

    execute
        %t0 = eval I64 a + (2 * 3)

    commit
        b = %t0
`, plans[1].Render(true))

	require.Equal(t, `assign _
    source "_ = 7"

    execute
        %t0 = eval I64 7

    commit
        _ = %t0
`, plans[3].Render(false))

	require.Equal(t, `assign r
    source "r = 0:10:2"

    execute
        %t0 = eval I64:I64:I64 0:10:2

    commit
        r = %t0
`, plans[4].Render(false))

	require.Equal(t, `assign s
    source "s = r"

    execute
        %t0 = eval I64:I64:I64 r

    commit
        s = %t0
`, plans[5].Render(false))
}

// TestPlanRouterRejections pins the capability boundary: statements with
// ranged RHS keep their legacy lowering, and so does a gate with a condition
// of several slots or with a condition for an operand (row 15), while a gate
// of plain comparisons plans, one or an && of them. A plain comparison plans
// as a value too, alone or as the final side of an &&, which plans when it is
// comparisons and then one plain value of one slot or a comparison, however
// it is grouped (row 19), and not when it holds a chain, a side of several
// slots, or a final comparison over a range. Legacy lowering also keeps a
// comparison in a cell or a call argument,
// a call given a range or an argument that can fail (matrix rows 13,
// 6c) and a literal with an index read in a cell, while an index read whose
// failure skips the statement's own value plans (row 7), and so does a ||
// after one when it is the whole value (row 7b), not under an operator, in a
// cell or a call argument, or with a side of several slots; a side of a || may
// itself be a comparison or an && that plans (row 7b), while an || for a
// statement's gate stays legacy. Ordinary heap
// values — both string
// flavours, concatenations, array literals of either layout, table
// literals — and calls with ordinary arguments plan alongside scalars and
// Range descriptors.
func TestPlanRouterRejections(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planRejections", `c = Twice(a)
    c = a * 2

lo, hi = Both(a)
    lo = a
    hi = a + 1
`, `x = 5
g = x > 2 13
ga = x > 2 && x < 9 13
gc = x > 2 < 9 13
gp, gs = Both(x) > Both(0) 13, 14
gm = x > 2 && Both(x) > Both(0) 13
la = x > 2 && 10
ln = x > 2 && (x > 3 && 10)
lb = x > 2 && x < 9
lc = x > 2 < 9 && 10
ld, le = x > 2 && Both(x)
y = 0
y = x > 2
s = "hi"
sg = s
sh = s ⊕ "!"
shc = sh
arr = [1 2]
z = arr[0]
zc = [arr[0]]
q = 0:3
w = q + 1
lr = x > 2 && q > 1
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
f = arr[9] || 1
fo = (arr[9] || 1) + 1
fc = [arr[9] || 1]
fd = Twice(arr[9] || 1)
fx = x > 9 || arr[9] || 1
fa = x > 2 && 10 || 7
fq = x > 2 < 9 && 10 || 7
fg = x > 2 || x < 9 13
fm, fn = arr[9] || Both(x)
g, ga, gc, gp, gs, gm, la, ln, lb, lc, ld, le, y, sg, shc, z, zc, w, lr, d, dq, dz, dc, m, tbl, rc, cc, f, fo, fc, fd, fx, fa, fq, fg, fm, fn`)

	require.Equal(t, []string{"assign x", "assign g", "assign ga", "assign la", "assign ln", "assign lb", "assign y", "assign y", "assign s", "assign sg", "assign sh", "assign shc", "assign arr", "assign z", "assign q", "assign _", "assign d", "assign m", "assign tbl", "assign f", "assign fx", "assign fa"}, planLabels(plans))
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

// TestPlanRouterRestoresRootAfterCall: the call plans, the body it compiles
// plans apart from the script's statements, and the assignment after the call
// pins that lazy specialization compilation restores FuncNameMangled to the
// root key (bindingSlotType relies on it).
func TestPlanRouterRestoresRootAfterCall(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	sc := compileScript(t, ctx, "planScriptRoot", `c = addOne(a)
    t = a + 1
    c = t * 1
`, `seed = 2
res = addOne(seed)
after = seed + 1
res, after`)

	require.Equal(t, []string{"assign seed", "assign res", "assign after", "print"}, planLabels(sc.Compiler.Plans[0]))
	require.Equal(t, []planOwner{
		{Label: "assign seed"}, {Label: "assign res"}, {Label: "assign after"}, {Label: "print"},
		{Func: "I64 = addOne(I64)", Label: "assign t"},
		{Func: "I64 = addOne(I64)", Label: "assign c"},
	}, planOwners(slices.Concat(sc.Compiler.Plans...)))
}

func TestPlanRouterFreshVsExistingTargets(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	// The gated statement moves x to memory; the plain assignment after it
	// must still plan.
	plans := compileScriptPlans(t, ctx, "planPromoted", "", `x = 1
x = x > 0 5
x = x + 1
x`)

	require.Equal(t, []string{"assign x", "assign x", "assign x"}, planLabels(plans))
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
		require.Contains(t, fmt.Sprint(r), `internal: invalid plan for "v = 1"`)
		require.Empty(t, slices.Concat(sc.Compiler.Plans...))
		_, vBound := Get(sc.Compiler.Scopes, "v")
		require.False(t, vBound)
		_, uBound := Get(sc.Compiler.Scopes, "u")
		require.False(t, uBound)
	}()
	sc.compileStatements()
	t.Fatal("compileStatements returned normally")
}

// TestInvalidPrintPlanPanicsBeforeLowering drives the same wiring for a
// print: an argument whose solved facts lost their output types builds an
// eval with no slots. The panic must fire before the plan is recorded.
func TestInvalidPrintPlanPanicsBeforeLowering(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	program := mustParseScript(t, "v = 1\nv")
	cc := NewCodeCompiler(ctx, "printInvalid", "", ast.NewCode())
	require.Empty(t, cc.Compile())
	sc := NewScriptCompiler(ctx, "printInvalid", program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()
	require.Empty(t, ts.Errors)

	arg := program.Statements[1].(*ast.PrintStatement).Expression.Arguments[0]
	sc.Compiler.ExprCache[key(sc.ScriptMangled, arg)].OutTypes = nil
	sc.Compiler.addMain()

	defer func() {
		r := recover()
		require.NotNil(t, r, "invalid plan must panic")
		require.Contains(t, fmt.Sprint(r), `internal: invalid plan for "v"`)
		require.Equal(t, []string{"assign v"}, planLabels(slices.Concat(sc.Compiler.Plans...)))
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

	plans := assignPlans(sc.Compiler.Plans[0])
	require.Len(t, plans, 1)
	stmt := program.Statements[0].(*ast.LetStatement)
	require.Same(t, stmt.Value[0], plans[0].Execute[0].(*pir.Eval).Expr)
	require.Equal(t, I64, plans[0].Commit[0].Target.Type)
}

// Plan §6 (discard target) and §12: a discarded bare Range descriptor is an
// unmanaged outcome with no release obligation, so it plans like a scalar.
func TestPlanGoldenRangeDiscard(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planRangeDiscard", "", "_ = 0:3")
	require.Equal(t, []string{"assign _"}, planLabels(plans))
	require.Equal(t, `assign _
    source "_ = 0:3"

    execute
        %t0 = eval I64:I64:I64 0:3

    commit
        _ = %t0
`, plans[0].Render(false))
}

// Plan §12: plan labels and bindings are Pluto identifiers, Unicode included.
func TestPlanGoldenUnicode(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planUnicode", "", "π = 3.14\nτ = π\nτ")
	require.Equal(t, []string{"assign π", "assign τ"}, planLabels(plans))
	require.Equal(t, `assign π
    source "π = 3.14"

    execute
        %t0 = eval F64 3.14

    commit
        π = %t0
`, plans[0].Render(false))
	require.Equal(t, `assign τ
    source "τ = π"

    execute
        %t0 = eval F64 π

    commit
        τ = %t0
`, plans[1].Render(false))
}

// Plan §12: a payload drops its outermost parentheses; a prefix stays bare.
func TestPlanGoldenPrefix(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planPrefix", "", "a = 1\nn = -a\nn")
	require.Equal(t, []string{"assign a", "assign n"}, planLabels(plans))
	require.Equal(t, `assign n
    source "n = (-a)"

    execute
        %t0 = eval I64 -a

    commit
        n = %t0
`, plans[1].Render(false))
}

// Plan §12: source bindings render bare, so a binding named t0 stays distinct
// from temporary %t0, and a binding named discard from the _ sink.
func TestPlanGoldenBareBindings(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planBare", "", "t0 = 1\nx = t0\ndiscard = 2\n_ = 3\nx, discard")
	require.Equal(t, []string{"assign t0", "assign x", "assign discard", "assign _"}, planLabels(plans))
	require.Equal(t, `assign x
    source "x = t0"

    execute
        %t0 = eval I64 t0

    commit
        x = %t0
`, plans[1].Render(false))
	require.Equal(t, `assign t0
    source "t0 = 1"

    execute
        %t0 = eval I64 1

    commit
        t0 = %t0
`, plans[0].Render(false))
	require.Equal(t, `assign discard
    source "discard = 2"

    execute
        %t0 = eval I64 2

    commit
        discard = %t0
`, plans[2].Render(false))
	require.Equal(t, `assign _
    source "_ = 3"

    execute
        %t0 = eval I64 3

    commit
        _ = %t0
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
	require.Equal(t, `assign h1
    source "h1 = (\"foo\" ⊕ \"bar\")"

    execute
        %t0 = eval owned Str "foo" ⊕ "bar"

    commit
        h1 = %t0 [move]
`, plans[0].Render(true))
	require.Equal(t, `assign h1, h2
    source "h1, h2 = h2, h1"

    execute
        %t0 = eval borrowed Str h2
        %t1 = eval borrowed Str h1

    commit
        h1 = %t0 [move]
        h2 = %t1 [move]
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
	require.Equal(t, `assign d1, d2
    source "d1, d2 = d1, d1"

    execute
        %t0 = eval borrowed Str d1
        %t1 = eval borrowed Str d1

    commit
        d1 = %t0 [move]
        d2 = %t1 [copy]
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
	require.Equal(t, `assign x
    source "x = (x ⊕ \"!\")"

    execute
        %t0 = eval owned Str x ⊕ "!"

    commit
        x = %t0 [move]
        drop x [old]
`, plans[1].Render(true))
	require.Equal(t, `assign _
    source "_ = (x ⊕ \"?\")"

    execute
        %t0 = eval owned Str x ⊕ "?"

    commit
        _ = %t0
        drop %t0
`, plans[2].Render(true))
	require.Equal(t, `assign _
    source "_ = x"

    execute
        %t0 = eval borrowed Str x

    commit
        _ = %t0
`, plans[3].Render(true))
	require.Equal(t, `assign x, y
    source "x, y = (y ⊕ \"!\"), x"

    execute
        %t0 = eval owned Str y ⊕ "!"
        %t1 = eval borrowed Str x

    commit
        x = %t0 [move]
        y = %t1 [move]
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
	require.Equal(t, `assign s
    source "s = \"hi\""

    execute
        %t0 = eval Str "hi"

    commit
        s = %t0 [copy]
`, plans[0].Render(true))
	require.Equal(t, `assign t
    source "t = g"

    execute
        %t0 = eval Str g

    commit
        t = %t0
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
	require.Equal(t, `assign arr1
    source "arr1 = [1 2 3]"

    execute
        %t0 = eval owned [I64] [1 2 3]

    commit
        arr1 = %t0 [move]
`, plans[0].Render(true))
	require.Equal(t, `assign arr2
    source "arr2 = arr1"

    execute
        %t0 = eval borrowed [I64] arr1

    commit
        arr2 = %t0 [copy]
`, plans[1].Render(true))
	require.Equal(t, `assign arr2
    source "arr2 = [4 5 6]"

    execute
        %t0 = eval owned [I64] [4 5 6]

    commit
        arr2 = %t0 [move]
        drop arr2 [old]
`, plans[2].Render(true))
	require.Equal(t, `assign arr1
    source "arr1 = []0"

    execute
        %t0 = eval owned [I64] []0

    commit
        arr1 = %t0 [move]
        drop arr1 [old]
`, plans[3].Render(true))
}

// Plan §16 Step 4, §17: a struct field read (matrix row 2b) and a struct
// value copy (35b) are unmanaged; a table column read (36g) is an owned
// copy and a table value copy (36b) a borrow. The table literal (36) is a
// block-layout literal: its operand is the source spelling with each line
// break escaped (plan §12), and its value an owned outcome that moves.
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
	require.Equal(t, []string{"assign n", "assign a", "assign s2", "assign scores", "assign col", "assign t2"}, planLabels(plans))
	require.Equal(t, `assign n
    source "n = p.name"

    execute
        %t0 = eval Str p.name

    commit
        n = %t0
`, plans[0].Render(true))
	require.Equal(t, `assign s2
    source "s2 = p"

    execute
        %t0 = eval Person{name:Str age:I64} p

    commit
        s2 = %t0
`, plans[2].Render(true))
	require.Equal(t, `assign scores
    source "scores = [\n  : Name Score\n    \"Ada\" 10\n]"

    execute
        %t0 = eval owned Table[Name:Str Score:I64] [\n  : Name Score\n    "Ada" 10\n]

    commit
        scores = %t0 [move]
`, plans[3].Render(true))
	require.Equal(t, `assign col
    source "col = scores.Score"

    execute
        %t0 = eval owned [I64] scores.Score

    commit
        col = %t0 [move]
`, plans[4].Render(true))
	require.Equal(t, `assign t2
    source "t2 = scores"

    execute
        %t0 = eval borrowed Table[Name:Str Score:I64] scores

    commit
        t2 = %t0 [copy]
`, plans[5].Render(true))
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
	require.Equal(t, `assign text, other
    source "text, other = (text ⊕ \"!\"), text"

    execute
        %t0 = eval owned Str text ⊕ "!"
        %t1 = eval borrowed Str text

    commit
        text = %t0 [move]
        other = %t1 [move]
`, plans[1].Render(true))
	require.Equal(t, `assign copy
    source "copy = other"

    execute
        %t0 = eval borrowed Str other

    commit
        copy = %t0 [copy]
`, plans[2].Render(true))
	require.Equal(t, `assign other
    source "other = \"new\""

    execute
        %t0 = eval Str "new"

    commit
        other = %t0 [copy]
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
	require.Equal(t, `assign s
    source "s = \"line one\nline two\""

    execute
        %t0 = eval Str "line one\nline two"

    commit
        s = %t0
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
	require.Equal(t, []string{"assign scores", "assign headerOnly", "assign taken", "assign headerOnly", "assign col", "assign direct"}, planLabels(plans))
	require.Equal(t, `assign col
    source "col = taken.Value"

    execute
        %t0 = eval owned [I64] taken.Value

    commit
        col = %t0 [move]
`, plans[4].Render(true))
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
	require.Equal(t, `assign d
    source "d = (Twice(d) + 1)"

    execute
        %t0 = eval I64 Twice(d) + 1

    commit
        d = %t0
`, plans[1].Render(true))
	require.Equal(t, `assign l, r
    source "l, r = twoStr(\"a\", \"b\")"

    execute
        %t0 = eval owned Str, owned Str twoStr("a", "b")

    commit
        l = %t0#0 [move]
        r = %t0#1 [move]
`, plans[2].Render(true))
	require.Equal(t, `assign l, _, n
    source "l, _, n = twoStr(l, r), 9"

    execute
        %t0 = eval owned Str, owned Str twoStr(l, r)
        %t1 = eval I64 9

    commit
        l = %t0#0 [move]
        _ = %t0#1
        n = %t1
        drop %t0#1
        drop l [old]
`, plans[3].Render(true))
	require.Equal(t, `assign copied, tag
    source "copied, tag = Copy(p)"

    execute
        %t0 = eval Person{name:Str age:I64}, Str Copy(p)

    commit
        copied = %t0#0
        tag = %t0#1
`, plans[4].Render(true))
}

// Matrix rows 14 and 14i: the body of a function without a range parameter
// plans. The script's plans are emitted first, then each specialization's in
// first-lowered order, so Outer's body precedes the Inner it calls. A gated
// body statement plans (row 14a: Pick's second line); a body a range
// parameter drives stays legacy (row 14b): Sum plans for a scalar x only.
func TestPlanGoldenFunctionOutputs(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	sc := compileScript(t, ctx, "planOutputs", `res = AddOne(a)
    t = a + 1
    res = t * 2

out, kept = Echo(s)
    out = s ⊕ "!"
    kept = s

out = Pick(s, n)
    out = "none"
    out = n > 0 s ⊕ "?"

res = Outer(a)
    res = Inner(a) + 1

res = Inner(a)
    res = a * 3

res = Sum(a, x)
    res = a + x
`, `n = AddOne(4)
h = "a" ⊕ "b"
o, k = Echo(h)
p = Pick(h, 1)
q = Outer(2)
one = Sum(1, 2)
acc = 0
acc = Sum(acc, 1:4)
n, o, k, p, q, one, acc`)

	plans := slices.Concat(sc.Compiler.Plans...)
	require.Equal(t, []planOwner{
		{Label: "assign n"}, {Label: "assign h"}, {Label: "assign o, k"}, {Label: "assign p"},
		{Label: "assign q"}, {Label: "assign one"}, {Label: "assign acc"}, {Label: "print"},
		{Func: "I64 = AddOne(I64)", Label: "assign t"},
		{Func: "I64 = AddOne(I64)", Label: "assign res"},
		{Func: "Str, Str = Echo(Str)", Label: "assign out"},
		{Func: "Str, Str = Echo(Str)", Label: "assign kept"},
		{Func: "Str = Pick(Str, I64)", Label: "assign out"},
		{Func: "Str = Pick(Str, I64)", Label: "assign out"},
		{Func: "I64 = Outer(I64)", Label: "assign res"},
		{Func: "I64 = Inner(I64)", Label: "assign res"},
		{Func: "I64 = Sum(I64, I64)", Label: "assign res"},
	}, planOwners(plans))

	// A function local is a local target; a function body's plan sits one
	// level in.
	require.Equal(t, `    assign t
        source "t = (a + 1)"

        execute
            %t0 = eval I64 a + 1

        commit
            t = %t0
`, plans[8].Render(false))
	// Row 14: a direct scalar output is a plain store.
	require.Equal(t, `    assign res
        source "res = (t * 2)"

        execute
            %t0 = eval I64 t * 2

        commit
            output res = %t0
`, plans[9].Render(true))
	// Row 14i: a heap output takes an owned outcome and releases what it held.
	require.Equal(t, `    assign out
        source "out = (s ⊕ \"!\")"

        execute
            %t0 = eval owned Str s ⊕ "!"

        commit
            output out = %t0 [move]
            drop out [old]
`, plans[10].Render(true))
	// A borrowed input survives the call, so the output gets its own copy.
	require.Equal(t, `    assign kept
        source "kept = s"

        execute
            %t0 = eval borrowed Str s

        commit
            output kept = %t0 [copy]
            drop kept [old]
`, plans[11].Render(true))
	// A static string entering an output whose type owns heap is copied.
	require.Equal(t, `    assign out
        source "out = \"none\""

        execute
            %t0 = eval Str "none"

        commit
            output out = %t0 [copy]
            drop out [old]
`, plans[12].Render(true))
	// Row 14a: a rejected gate leaves an output holding what it held.
	require.Equal(t, `    assign out
        source "out = (n > 0) (s ⊕ \"?\")"

        execute
            %t0 = check I64 n > 0
            gate %t0
                %t1 = eval owned Str s ⊕ "?"

        commit
            output out = %t1 [move] [on-skip=keep]
            drop out [old]
`, plans[13].Render(true))
}

// Plan §4, §9 and matrix rows 5e, 15, 16: a statement's condition plans as a
// check, and a gate takes its outcome and admits the values beneath it. A
// rejected gate leaves every mapping unapplied, so each binding carries a
// skip policy, and the two mappings of a swap still land together. What the
// check built is released; a borrow it read is not.
func TestPlanGoldenGates(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	sc := compileScript(t, ctx, "planGates", `s = MkStr(x)
    s = "s-x"
`, `n = 3
x = 1
x = n > 0 5
t = "heap" ⊕ "!"
u = "old" ⊕ "!"
fresh = n > 9 t
t, u = n > 0 u, t
arr = [10 20 30]
x = arr[1] > -1 arr[9] || 8
hc = MkStr(n) > "a" 7
bc = t > "abc" 1
_ = n > 0 MkStr(n)
x, fresh, t, u, hc, bc`)

	plans := slices.Concat(sc.Compiler.Plans...)
	require.Equal(t, []planOwner{
		{Label: "assign n"}, {Label: "assign x"}, {Label: "assign x"}, {Label: "assign t"},
		{Label: "assign u"}, {Label: "assign fresh"}, {Label: "assign t, u"}, {Label: "assign arr"},
		{Label: "assign x"}, {Label: "assign hc"}, {Label: "assign bc"}, {Label: "assign _"},
		{Label: "print"},
		{Func: "Str = MkStr(I64)", Label: "assign s"},
	}, planOwners(plans))

	// An existing target keeps its value where the gate rejects.
	require.Equal(t, `assign x
    source "x = (n > 0) 5"

    execute
        %t0 = check I64 n > 0
        gate %t0
            %t1 = eval I64 5

    commit
        x = %t1 [on-skip=keep]
`, plans[2].Render(true))
	// A fresh target holds zero there, and has no old value to release.
	require.Equal(t, `assign fresh
    source "fresh = (n > 9) t"

    execute
        %t0 = check I64 n > 9
        gate %t0
            %t1 = eval borrowed Str t

    commit
        fresh = %t1 [copy] [on-skip=zero]
`, plans[5].Render(true))
	// A gate rejects both mappings or neither, so each borrow still takes its
	// owner's old value.
	require.Equal(t, `assign t, u
    source "t, u = (n > 0) u, t"

    execute
        %t0 = check I64 n > 0
        gate %t0
            %t1 = eval borrowed Str u
            %t2 = eval borrowed Str t

    commit
        t = %t1 [move] [on-skip=keep]
        u = %t2 [move] [on-skip=keep]
`, plans[6].Render(true))
	// A read in the condition skips the check, and a first sits in the gate's
	// region like any value.
	require.Equal(t, `assign x
    source "x = (arr[1] > (-1)) (arr[9] || 8)"

    execute
        %t0 = check I64 arr[1] > (-1) [on-oob=skip]
        gate %t0
            %t1 =
                first I64
                    %t2 = eval I64 arr[9] [on-oob=fallback]
                fallback
                    %t3 = eval I64 8

    commit
        x = %t1 [on-skip=keep]
`, plans[8].Render(false))
	// The gate only tests the check, so the string the check built is released.
	require.Equal(t, `assign hc
    source "hc = (MkStr(n) > \"a\") 7"

    execute
        %t0 = check owned Str MkStr(n) > "a"
        gate %t0
            %t1 = eval I64 7

    commit
        hc = %t1 [on-skip=zero]
        drop %t0
`, plans[9].Render(true))
	// A binding the check read stays its owner's.
	require.Equal(t, `assign bc
    source "bc = (t > \"abc\") 1"

    execute
        %t0 = check borrowed(t) Str t > "abc"
        gate %t0
            %t1 = eval I64 1

    commit
        bc = %t1 [on-skip=zero]
`, plans[10].Render(true))
	// Row 5e: a discard keeps nothing, so it carries no policy; what its eval
	// built is released where the gate admitted it.
	require.Equal(t, `assign _
    source "_ = (n > 0) MkStr(n)"

    execute
        %t0 = check I64 n > 0
        gate %t0
            %t1 = eval owned Str MkStr(n)

    commit
        _ = %t1
        drop %t1
`, plans[11].Render(true))
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

// Plan §4, §12 and matrix rows 15, 19: an && plans as one last however long
// its chain, in a gate and in value position. A gate takes the last's own
// outcome. A value last yields its final side's value where every check
// before it held, so its binding carries a skip policy and takes nothing from
// another binding. What a check built is released; a discarded final side is
// released where it ran.
func TestPlanGoldenLasts(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	sc := compileScript(t, ctx, "planLasts", `s = MkStr(x)
    s = "s-x"
`, `n = 3
x = 1
three = n > 0 && MkStr(n) > "a" && MkStr(x) > "a" 7
t = "heap" ⊕ "!"
u = "old" ⊕ "!"
t = MkStr(n) > "a" && MkStr(x)
t, u = n > 0 && u, t
arr = [10 20 30]
x = n > 0 && n > 1 && arr[9]
x = n > 0 n > 2 && 10
_ = n > 0 && MkStr(n)
lb = n > 0 && MkStr(n) > "a"
x, three, t, u, lb`)

	plans := slices.Concat(sc.Compiler.Plans...)
	require.Equal(t, []planOwner{
		{Label: "assign n"}, {Label: "assign x"}, {Label: "assign three"}, {Label: "assign t"},
		{Label: "assign u"}, {Label: "assign t"}, {Label: "assign t, u"}, {Label: "assign arr"},
		{Label: "assign x"}, {Label: "assign x"}, {Label: "assign _"}, {Label: "assign lb"},
		{Label: "print"},
		{Func: "Str = MkStr(I64)", Label: "assign s"},
	}, planOwners(plans))

	// A gate's conjuncts are the sides of one last of the final one's type,
	// and the gate takes it. Each check is only tested, so the string each
	// built is released.
	require.Equal(t, `assign three
    source "three = (n > 0) && (MkStr(n) > \"a\") && (MkStr(x) > \"a\") 7"

    execute
        %t0 =
            last Str
                %t1 = check I64 n > 0
            next
                %t2 = check owned Str MkStr(n) > "a"
            next
                %t3 = check owned Str MkStr(x) > "a"
        gate %t0
            %t4 = eval I64 7

    commit
        three = %t4 [on-skip=zero]
        drop %t2
        drop %t3
`, plans[2].Render(true))
	// A value last moves what its final side built, and what its check built
	// is only tested.
	require.Equal(t, `assign t
    source "t = ((MkStr(n) > \"a\") && MkStr(x))"

    execute
        %t0 =
            last Str
                %t1 = check owned Str MkStr(n) > "a"
            next
                %t2 = eval owned Str MkStr(x)

    commit
        t = %t0 [move] [on-skip=keep]
        drop %t1
        drop t [old]
`, plans[5].Render(true))
	// A last may skip its own mapping, so neither borrow takes the other's
	// old value: where a gate moves both strings of a swap, this copies both.
	require.Equal(t, `assign t, u
    source "t, u = ((n > 0) && u), t"

    execute
        %t0 =
            last Str
                %t1 = check I64 n > 0
            next
                %t2 = eval borrowed Str u
        %t3 = eval borrowed Str t

    commit
        t = %t0 [copy] [on-skip=keep]
        u = %t3 [copy]
        drop t [old]
        drop u [old]
`, plans[6].Render(true))
	// A chain in value position is one last, and a read in its final side
	// skips it where the read fails.
	require.Equal(t, `assign x
    source "x = (((n > 0) && (n > 1)) && arr[9])"

    execute
        %t0 =
            last I64
                %t1 = check I64 n > 0
            next
                %t2 = check I64 n > 1
            next
                %t3 = eval I64 arr[9] [on-oob=skip]

    commit
        x = %t0 [on-skip=keep]
`, plans[8].Render(true))
	// A last in a gate's region is numbered after the gate's check.
	require.Equal(t, `assign x
    source "x = (n > 0) ((n > 2) && 10)"

    execute
        %t0 = check I64 n > 0
        gate %t0
            %t1 =
                last I64
                    %t2 = check I64 n > 2
                next
                    %t3 = eval I64 10

    commit
        x = %t1 [on-skip=keep]
`, plans[9].Render(true))
	// A discard keeps nothing: what the final side built is released.
	require.Equal(t, `assign _
    source "_ = ((n > 0) && MkStr(n))"

    execute
        %t0 =
            last Str
                %t1 = check I64 n > 0
            next
                %t2 = eval owned Str MkStr(n)

    commit
        _ = %t0
        drop %t2
`, plans[10].Render(true))
	// A last that ends in a comparison yields that check's left operand, and
	// what the check built is released where it failed.
	require.Equal(t, `assign lb
    source "lb = ((n > 0) && (MkStr(n) > \"a\"))"

    execute
        %t0 =
            last Str
                %t1 = check I64 n > 0
            next
                %t2 = check owned Str MkStr(n) > "a"

    commit
        lb = %t0 [move] [on-skip=zero]
        drop %t2 [on-skip]
`, plans[11].Render(true))
}

// Plan §12 and matrix rows 19, 21: a comparison as a value is one check, whose
// left operand the binding takes where the comparison holds, alone or in a
// gate's region. What an owned left operand built moves where the check holds
// and is released where it skipped.
func TestPlanGoldenCheckValues(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	sc := compileScript(t, ctx, "planCheckValues", `s = MkStr(x)
    s = "s-x"
`, `a = 3
n = 1
x = 0
x = a > 2
g = n > 0 a > 2
s = "heap" ⊕ "!"
s = MkStr(a) > "zzz"
x, g, s`)

	plans := slices.Concat(sc.Compiler.Plans...)
	require.Equal(t, []planOwner{
		{Label: "assign a"}, {Label: "assign n"}, {Label: "assign x"}, {Label: "assign x"},
		{Label: "assign g"}, {Label: "assign s"}, {Label: "assign s"},
		{Label: "print"},
		{Func: "Str = MkStr(I64)", Label: "assign s"},
	}, planOwners(plans))

	require.Equal(t, `assign x
    source "x = (a > 2)"

    execute
        %t0 = check I64 a > 2

    commit
        x = %t0 [on-skip=keep]
`, plans[3].Render(false))
	require.Equal(t, `assign g
    source "g = (n > 0) (a > 2)"

    execute
        %t0 = check I64 n > 0
        gate %t0
            %t1 = check I64 a > 2

    commit
        g = %t1 [on-skip=zero]
`, plans[4].Render(false))
	require.Equal(t, `assign s
    source "s = (MkStr(a) > \"zzz\")"

    execute
        %t0 = check owned Str MkStr(a) > "zzz"

    commit
        s = %t0 [move] [on-skip=keep]
        drop %t0 [on-skip]
        drop s [old]
`, plans[6].Render(true))
}

// Plan §3 and matrix rows 29, 29b, 29c: a print whose arguments are ordinary
// plans as one invocation. A string literal is the invocation's own operand,
// an owned slot is taken by it, and a print that can fail stays legacy.
func TestPlanGoldenPrints(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	sc := compileScript(t, ctx, "planPrints", `out, kept = Echo(s)
    out = s ⊕ "!"
    kept = s

res = Shout(a)
    "in:", a
    res = a + 1
`, `n = 4
h = "a" ⊕ "b"
"hello"
"n: -n", n
h, h ⊕ "!"
Echo(h)
[1 2] ⊕ [3]
m = Shout(n)
n > 3
n > 3 && n
m`)

	plans := slices.Concat(sc.Compiler.Plans...)
	require.Equal(t, []planOwner{
		{Label: "assign n"}, {Label: "assign h"},
		{Label: "print"}, {Label: "print"}, {Label: "print"}, {Label: "print"}, {Label: "print"},
		{Label: "assign m"}, {Label: "print"},
		{Func: "Str, Str = Echo(Str)", Label: "assign out"},
		{Func: "Str, Str = Echo(Str)", Label: "assign kept"},
		{Func: "I64 = Shout(I64)", Label: "print"},
		{Func: "I64 = Shout(I64)", Label: "assign res"},
	}, planOwners(plans))

	// A print of literals alone has no eval.
	require.Equal(t, `print
    source "\"hello\""

    execute
        print "hello"
`, plans[2].Render(true))
	// A literal with a marker stays the invocation's operand: it reads n in
	// place and is never built as one string, where the same literal as a
	// value is an owned string.
	require.Equal(t, `print
    source "\"n: -n\", n"

    execute
        %t0 = eval I64 n
        print "n: -n", %t0
`, plans[3].Render(true))
	// The invocation reads a binding's heap value and takes a temporary's.
	require.Equal(t, `print
    source "h, (h ⊕ \"!\")"

    execute
        %t0 = eval borrowed Str h
        %t1 = eval owned Str h ⊕ "!"
        print %t0, %t1 [move]
`, plans[4].Render(true))
	// Row 29c: every slot of a multi-output call prints, each owned one taken.
	require.Equal(t, `print
    source "Echo(h)"

    execute
        %t0 = eval owned Str, owned Str Echo(h)
        print %t0#0 [move], %t0#1 [move]
`, plans[5].Render(true))
	require.Equal(t, `print
    source "([1 2] ⊕ [3])"

    execute
        %t0 = eval owned [I64] [1 2] ⊕ [3]
        print %t0 [move]
`, plans[6].Render(true))
	// A print in a function body sits in that function's block.
	require.Equal(t, `    print
        source "\"in:\", a"

        execute
            %t0 = eval I64 a
            print "in:", %t0
`, plans[11].Render(false))
}

// Plan §9 and matrix rows 5d, 7: an index read outside a literal cell and a
// call argument gives its eval the out-of-bounds scope of its outcome, and
// each mapping of that outcome says what its target holds where the outcome
// skipped. A print with a read stays legacy (row 31).
func TestPlanGoldenCheckedReads(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	sc := compileScript(t, ctx, "planCheckedReads", `res = First(v)
    res = 0
    res = v[0]

res = Loud(x)
    res = x
`, `arr = [10 20 30]
sarr = ["x" "y"]
keep = 7
keep = keep * arr[5]
fresh = Loud(1) + arr[9]
s = "old" ⊕ "!"
s = s ⊕ sarr[9]
_ = sarr[1]
h1 = "one" ⊕ "!"
h2 = "two" ⊕ "!"
h1, h2, keep = h2, h1, arr[arr[9]]
h1
h1, h2 = h2, sarr[9]
r = First(arr)
arr[1]
keep, fresh, s, h1, h2, r`)

	plans := slices.Concat(sc.Compiler.Plans...)
	require.Equal(t, []planOwner{
		{Label: "assign arr"}, {Label: "assign sarr"}, {Label: "assign keep"}, {Label: "assign keep"},
		{Label: "assign fresh"}, {Label: "assign s"}, {Label: "assign s"}, {Label: "assign _"},
		{Label: "assign h1"}, {Label: "assign h2"}, {Label: "assign h1, h2, keep"}, {Label: "print"},
		{Label: "assign h1, h2"}, {Label: "assign r"}, {Label: "print"},
		{Func: "I64 = Loud(I64)", Label: "assign res"},
		{Func: "I64 = First([I64])", Label: "assign res"},
		{Func: "I64 = First([I64])", Label: "assign res"},
	}, planOwners(plans))

	// An existing target keeps its value.
	require.Equal(t, `assign keep
    source "keep = (keep * arr[5])"

    execute
        %t0 = eval I64 keep * arr[5] [on-oob=skip]

    commit
        keep = %t0 [on-skip=keep]
`, plans[3].Render(false))
	// A target the statement introduces holds zero, and a call beside the read
	// stays in the read's eval, whose own order decides whether it runs
	// (tests/cond/value_cond_expr pins that at run time).
	require.Equal(t, `assign fresh
    source "fresh = (Loud(1) + arr[9])"

    execute
        %t0 = eval I64 Loud(1) + arr[9] [on-oob=skip]

    commit
        fresh = %t0 [on-skip=zero]
`, plans[4].Render(false))
	// What a skipped outcome built is released, and the old value only where
	// the mapping replaced it.
	require.Equal(t, `assign s
    source "s = (s ⊕ sarr[9])"

    execute
        %t0 = eval owned Str s ⊕ sarr[9] [on-oob=skip]

    commit
        s = %t0 [move] [on-skip=keep]
        drop %t0 [on-skip]
        drop s [old]
`, plans[6].Render(true))
	// Row 5d: a discard keeps nothing, and its outcome is released either way.
	require.Equal(t, `assign _
    source "_ = sarr[1]"

    execute
        %t0 = eval owned Str sarr[1] [on-oob=skip]

    commit
        _ = %t0
        drop %t0
`, plans[7].Render(true))
	// A swap beside a read still moves: both of its mappings always commit.
	require.Equal(t, `assign h1, h2, keep
    source "h1, h2, keep = h2, h1, arr[arr[9]]"

    execute
        %t0 = eval borrowed Str h2
        %t1 = eval borrowed Str h1
        %t2 = eval I64 arr[arr[9]] [on-oob=skip]

    commit
        h1 = %t0 [move]
        h2 = %t1 [move]
        keep = %t2 [on-skip=keep]
`, plans[10].Render(true))
	// h2's replacement may skip, so h1 cannot take the value h2 holds.
	require.Equal(t, `assign h1, h2
    source "h1, h2 = h2, sarr[9]"

    execute
        %t0 = eval borrowed Str h2
        %t1 = eval owned Str sarr[9] [on-oob=skip]

    commit
        h1 = %t0 [copy]
        h2 = %t1 [move] [on-skip=keep]
        drop %t1 [on-skip]
        drop h1 [old]
        drop h2 [old]
`, plans[12].Render(true))
	// An output is never fresh, so it keeps the value it holds.
	require.Equal(t, `    assign res
        source "res = v[0]"

        execute
            %t0 = eval I64 v[0] [on-oob=skip]

        commit
            output res = %t0 [on-skip=keep]
`, plans[17].Render(false))
}

// Plan §4, §9 and matrix row 7b: a || after an index read plans as a first.
// The read falls back where it fails, so its mapping carries a skip policy
// only when the last side can fail too, and a chain is one first. A print
// with a fallback stays legacy.
func TestPlanGoldenFallbacks(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	sc := compileScript(t, ctx, "planFallbacks", `res = At(v, i)
    res = v[i] || -1
`, `arr = [10 20 30]
sarr = ["x" "y"]
h = "heap" ⊕ "!"
a = arr[9] || -1
s = "old" ⊕ "!"
s
s = sarr[9] || h
c = arr[9] || arr[8] || -3
g = 5
g = arr[9] || arr[8]
_ = sarr[9] || (h ⊕ "?")
p, q = arr[9] || -1, 5
r = At(arr, 9)
w = (arr[9] + 1) || -1
arr[9] || -1
a, s, c, g, p, q, r, w`)

	plans := slices.Concat(sc.Compiler.Plans...)
	require.Equal(t, []planOwner{
		{Label: "assign arr"}, {Label: "assign sarr"}, {Label: "assign h"}, {Label: "assign a"},
		{Label: "assign s"}, {Label: "print"}, {Label: "assign s"}, {Label: "assign c"},
		{Label: "assign g"}, {Label: "assign g"}, {Label: "assign _"}, {Label: "assign p, q"},
		{Label: "assign r"}, {Label: "assign w"}, {Label: "print"},
		{Func: "I64 = At([I64], I64)", Label: "assign res"},
	}, planOwners(plans))

	// A fresh target and an existing one read alike: the first always yields.
	require.Equal(t, `assign a
    source "a = (arr[9] || (-1))"

    execute
        %t0 =
            first I64
                %t1 = eval I64 arr[9] [on-oob=fallback]
            fallback
                %t2 = eval I64 -1

    commit
        a = %t0
`, plans[3].Render(true))
	// The read's copy moves and the binding is copied; what the failed read
	// built is released where the fallback ran.
	require.Equal(t, `assign s
    source "s = (sarr[9] || h)"

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
`, plans[6].Render(true))
	// A chain is one first, however it is grouped.
	require.Equal(t, `assign c
    source "c = ((arr[9] || arr[8]) || (-3))"

    execute
        %t0 =
            first I64
                %t1 = eval I64 arr[9] [on-oob=fallback]
            fallback
                %t2 = eval I64 arr[8] [on-oob=fallback]
            fallback
                %t3 = eval I64 -3

    commit
        c = %t0
`, plans[7].Render(false))
	// A last side that can fail skips the outcome, so the target keeps its
	// value.
	require.Equal(t, `assign g
    source "g = (arr[9] || arr[8])"

    execute
        %t0 =
            first I64
                %t1 = eval I64 arr[9] [on-oob=fallback]
            fallback
                %t2 = eval I64 arr[8] [on-oob=skip]

    commit
        g = %t0 [on-skip=keep]
`, plans[9].Render(false))
	// A discard releases each owned side where it ran.
	require.Equal(t, `assign _
    source "_ = (sarr[9] || (h ⊕ \"?\"))"

    execute
        %t0 =
            first Str
                %t1 = eval owned Str sarr[9] [on-oob=fallback]
            fallback
                %t2 = eval owned Str h ⊕ "?"

    commit
        _ = %t0
        drop %t1
        drop %t2
`, plans[10].Render(true))
	// The eval after a first takes the ID after its sides.
	require.Equal(t, `assign p, q
    source "p, q = (arr[9] || (-1)), 5"

    execute
        %t0 =
            first I64
                %t1 = eval I64 arr[9] [on-oob=fallback]
            fallback
                %t2 = eval I64 -1
        %t3 = eval I64 5

    commit
        p = %t0
        q = %t3
`, plans[11].Render(false))
	// A side may wrap its read, and fails where the read does.
	require.Equal(t, `assign w
    source "w = ((arr[9] + 1) || (-1))"

    execute
        %t0 =
            first I64
                %t1 = eval I64 arr[9] + 1 [on-oob=fallback]
            fallback
                %t2 = eval I64 -1

    commit
        w = %t0
`, plans[13].Render(false))
	require.Equal(t, `    assign res
        source "res = (v[i] || (-1))"

        execute
            %t0 =
                first I64
                    %t1 = eval I64 v[i] [on-oob=fallback]
                fallback
                    %t2 = eval I64 -1

        commit
            output res = %t0
`, plans[15].Render(false))
}

// A gate reads whether its check yielded and takes nothing, so what the check
// built is released before the gate's region runs (plan §8).
// Plan §4, §12: a side of a first takes its kind from the side itself, as a
// last's sides do — a comparison is a check, a value-position && is a last
// that binds no outcome, and any other side is an eval. A fallback resolves a
// failed access anywhere in the side before it, and a mapping names the node
// each side supplies its value from.
func TestPlanGoldenFirstConditions(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planFirstConditions", `s = MkStr(x)
    s = "s-x"
`, `sarr = ["x" "y"]
arr = [10 20 30]
h = "heap" ⊕ "!"
a = 5
b = 0
x = a > 2 || 7
n = 1
n = a > 9 || a > 1
z = a > 0 && 11 || 13
u = a > 9 && 1 || b > 0 && 2
c = 1 < 2 && arr[9] || -1
d = arr[9] > 0 && 1 || -1
s = sarr[9] > "a" || h
t = MkStr(a) > "a" && MkStr(b) || h
p, q = a > 0 && 11 || 13, 5
x, n, z, u, c, d, p, q
s, t, h`)

	require.Equal(t, []string{"assign sarr", "assign arr", "assign h", "assign a", "assign b", "assign x",
		"assign n", "assign n", "assign z", "assign u", "assign c", "assign d", "assign s", "assign t",
		"assign p, q"}, planLabels(plans))

	// A check side yields its left operand; the last side always yields, so
	// the mapping always commits.
	require.Equal(t, `assign x
    source "x = ((a > 2) || 7)"

    execute
        %t0 =
            first I64
                %t1 = check I64 a > 2
            fallback
                %t2 = eval I64 7

    commit
        x = %t0
`, plans[5].Render(false))
	// A check as the last side can fail, so the binding keeps its value.
	require.Equal(t, `assign n
    source "n = ((a > 9) || (a > 1))"

    execute
        %t0 =
            first I64
                %t1 = check I64 a > 9
            fallback
                %t2 = check I64 a > 1

    commit
        n = %t0 [on-skip=keep]
`, plans[7].Render(false))
	// An && side is a last that binds no outcome: its head sits where the
	// outcome line would have gone, and its value reaches the mapping through
	// the first's outcome.
	require.Equal(t, `assign z
    source "z = (((a > 0) && 11) || 13)"

    execute
        %t0 =
            first I64
                last I64
                    %t1 = check I64 a > 0
                next
                    %t2 = eval I64 11
            fallback
                %t3 = eval I64 13

    commit
        z = %t0
`, plans[8].Render(false))
	// Every side may be a last, and the IDs stay dense through each one's own
	// sides. Where no side yields, a fresh binding holds its zero.
	require.Equal(t, `assign u
    source "u = (((a > 9) && 1) || ((b > 0) && 2))"

    execute
        %t0 =
            first I64
                last I64
                    %t1 = check I64 a > 9
                next
                    %t2 = eval I64 1
            fallback
                last I64
                    %t3 = check I64 b > 0
                next
                    %t4 = eval I64 2

    commit
        u = %t0 [on-skip=zero]
`, plans[9].Render(false))
	// The fallback after a side resolves a failed access anywhere in it, a
	// nested last's final eval included.
	require.Equal(t, `assign c
    source "c = (((1 < 2) && arr[9]) || (-1))"

    execute
        %t0 =
            first I64
                last I64
                    %t1 = check I64 1 < 2
                next
                    %t2 = eval I64 arr[9] [on-oob=fallback]
            fallback
                %t3 = eval I64 -1

    commit
        c = %t0
`, plans[10].Render(false))
	// A check inside a nested last carries the scope its side's position
	// gives, as the last's final eval does.
	require.Equal(t, `assign d
    source "d = (((arr[9] > 0) && 1) || (-1))"

    execute
        %t0 =
            first I64
                last I64
                    %t1 = check I64 arr[9] > 0 [on-oob=fallback]
                next
                    %t2 = eval I64 1
            fallback
                %t3 = eval I64 -1

    commit
        d = %t0
`, plans[11].Render(false))
	// A check side with a read falls back where the read fails as where the
	// comparison does, and what it built is released where the fallback ran.
	require.Equal(t, `assign s
    source "s = ((sarr[9] > \"a\") || h)"

    execute
        %t0 =
            first Str
                %t1 = check owned Str sarr[9] > "a" [on-oob=fallback]
            fallback
                %t2 = eval borrowed Str h

    commit
        s = %t0 [move %t1] [copy %t2]
        drop %t1 [on-fallback]
`, plans[12].Render(true))
	// A mapping takes a last side's value from its final side, so the
	// transfers name %t2 and %t3; the check before it is only tested, and
	// what it built is released once it is compared.
	require.Equal(t, `assign t
    source "t = (((MkStr(a) > \"a\") && MkStr(b)) || h)"

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
`, plans[13].Render(true))
	// The value after a first takes the ID after every node inside it, a
	// nested last's own sides included.
	require.Equal(t, `assign p, q
    source "p, q = (((a > 0) && 11) || 13), 5"

    execute
        %t0 =
            first I64
                last I64
                    %t1 = check I64 a > 0
                next
                    %t2 = eval I64 11
            fallback
                %t3 = eval I64 13
        %t4 = eval I64 5

    commit
        p = %t0
        q = %t4
`, plans[14].Render(false))
}

func TestPlanGateReleasesCheckFirst(t *testing.T) {
	ir, _ := compileScriptAndCodeIR(t, "planGateRelease", `s = MkStr(x)
    s = "s-x"
`, `n = 3
hc = MkStr(n) > "a" 7
hc`)

	released := strings.Index(ir, "call void @free(ptr %call_final)")
	region := strings.Index(ir, "gate_if:")
	require.NotEqual(t, -1, released, "expected the check's string to be released")
	require.NotEqual(t, -1, region, "expected the gate's region")
	require.Less(t, released, region, "the check's string must be released before the region runs")
}

// A last's final side runs only where the checks before it held, so what it
// built is released in the block a branch on the bit that says it ran admits
// (plan §8), whether it feeds a `_`, failed a read after building, or failed
// its comparison: where it never ran, its memory holds nothing to release. A
// final side that cannot fail ran wherever the last yielded, so that bit is
// the last's own.
func TestPlanLastReleasesFinalSideWhereItRan(t *testing.T) {
	cases := []struct {
		name      string
		statement string
		ran       string
		staged    string
	}{
		{"Discarded", `_ = n > 9 && MkStr(n)`, "%last_yielded", "%discard"},
		{"FailedRead", `s = n > 9 && (sarr[9] ⊕ "!")`, "%last_ran", "%temp_free"},
		{"FailedCheck", `s = n > 9 && MkStr(n) > "a"`, "%last_ran", "%temp_free"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ir, _ := compileScriptAndCodeIR(t, "planLastRelease"+tc.name, `s = MkStr(x)
    s = "s-x"
`, `n = 3
sarr = ["x" "y"]
s = "kept"
`+tc.statement+`
n, s, sarr`)

			require.Contains(t, ir, "store i1 true, ptr "+tc.ran+".mem", "the final side must set the flag")
			require.Contains(t, ir, tc.ran+" = load i1, ptr "+tc.ran+".mem", "the branch must read the flag the final side sets")
			require.Contains(t, ir, "br i1 "+tc.ran+", label %ran_if, label %ran_cont")
			admitted := irBlock(ir, "ran_if")
			require.Contains(t, admitted, tc.staged+" = load ptr, ptr %last_side.mem")
			require.Contains(t, admitted, "call void @free(ptr "+tc.staged+")")
		})
	}
}

// A last that is a side of a first stages its value on a path of its own, so
// the bit that says its final side ran is cleared before the sides part and
// read where the mappings land: a side that never ran staged nothing to
// release, and one that ran and then failed must release what it built
// (plan §8).
func TestPlanFirstReleasesLastSideWhereItRan(t *testing.T) {
	ir, _ := compileScriptAndCodeIR(t, "planFirstLastRelease", `s = MkStr(x)
    s = "s-x"
`, `a = 5
b = 3
sarr = ["x" "y"]
s = "kept"
s = sarr[9] || MkStr(a) > "a" && MkStr(b) > "zzz"
a, b, s`)

	cleared := strings.Index(ir, "store i1 false, ptr %first_side_ran.mem")
	parted := strings.Index(ir, "first_fallback_if:")
	require.NotEqual(t, -1, cleared, "expected the side's ran bit to be cleared")
	require.NotEqual(t, -1, parted, "expected the fallback side's own block")
	require.Less(t, cleared, parted, "the bit must be cleared before the sides part")

	require.Contains(t, irBlock(ir, "last_next_cont"), "store i1 %last_ran, ptr %first_side_ran.mem",
		"the last must record whether its final side ran")
	require.Contains(t, irBlock(ir, "first_fallback_cont"), "%first_side_ran = load i1, ptr %first_side_ran.mem",
		"the bit must be read where every path reaches it")
	require.Contains(t, ir, "br i1 %first_side_ran, label %ran_if")
	admitted := irBlock(ir, "ran_if")
	require.Contains(t, admitted, "= load ptr, ptr %last_side.mem")
	require.Contains(t, admitted, "call void @free(ptr %temp_free")
}

// irBlock returns the instructions of the basic block labelled label in ir,
// one per line: those after its label, up to the blank line or the closing
// brace that ends it.
func irBlock(ir, label string) string {
	_, rest, _ := strings.Cut(ir, "\n"+label+":")
	var b strings.Builder
	for _, line := range strings.Split(rest, "\n")[1:] {
		if line == "" || line == "}" {
			break
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}
