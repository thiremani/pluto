package compiler

import (
	"fmt"
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

	require.Equal(t, []string{"assign_a", "assign_b", "assign_a_b", "assign__", "assign_r", "assign_s"}, planLabels(plans))

	require.Equal(t, `statement assign_a_b
    source "a, b = b, a"

    execute
        %t0 = eval I64 b
        %t1 = eval I64 a

    commit
        a <- %t0
        b <- %t1
`, plans[2].Render(false))

	require.Equal(t, `statement assign_b
    source "b = (a + (2 * 3))"

    execute
        %t0 = eval I64 a + (2 * 3) [shape=scalar] [yield=always] [unmanaged]

    commit
        b : I64 <- %t0
`, plans[1].Render(true))

	require.Equal(t, `statement assign__
    source "_ = 7"

    execute
        %t0 = eval I64 7

    commit
        _ <- %t0
`, plans[3].Render(false))

	require.Equal(t, `statement assign_r
    source "r = 0:10:2"

    execute
        %t0 = eval I64:I64:I64 0:10:2

    commit
        r <- %t0
`, plans[4].Render(false))

	require.Equal(t, `statement assign_s
    source "s = r"

    execute
        %t0 = eval I64:I64:I64 r

    commit
        s <- %t0
`, plans[5].Render(false))
}

// TestPlanRouterRejections pins the capability boundary: statements with
// gates, conditional values, checked accesses, ranged RHS, calls, and
// block-layout literals keep their legacy lowering, while ordinary heap
// values — both string flavours, concatenations, inline array literals —
// plan alongside scalars and Range descriptors.
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
g, y, sg, shc, z, w, d, m, tbl, rc, cc`)

	require.Equal(t, []string{"assign_x", "assign_y", "assign_s", "assign_sg", "assign_sh", "assign_shc", "assign_arr", "assign_q", "assign__"}, planLabels(plans))
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
}

// TestPlanRouterScriptRootOnly: function-body statements produce no plans,
// and the assignment after the call pins that lazy specialization compilation
// restores FuncNameMangled to the root key (scriptRootBindingType relies on it).
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

	require.Equal(t, []string{"assign_seed", "assign_after"}, planLabels(plans))
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

	require.Equal(t, []string{"assign_x", "assign_x"}, planLabels(plans))
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
	require.Equal(t, []string{"assign__"}, planLabels(plans))
	require.Equal(t, `statement assign__
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
	require.Equal(t, []string{"assign_π", "assign_τ"}, planLabels(plans))
	require.Equal(t, `statement assign_π
    source "π = 3.14"

    execute
        %t0 = eval F64 3.14

    commit
        π <- %t0
`, plans[0].Render(false))
	require.Equal(t, `statement assign_τ
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
	require.Equal(t, []string{"assign_a", "assign_n"}, planLabels(plans))
	require.Equal(t, `statement assign_n
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
	require.Equal(t, []string{"assign_t0", "assign_x", "assign_discard", "assign__"}, planLabels(plans))
	require.Equal(t, `statement assign_x
    source "x = t0"

    execute
        %t0 = eval I64 t0

    commit
        x <- %t0
`, plans[1].Render(false))
	require.Equal(t, `statement assign_t0
    source "t0 = 1"

    execute
        %t0 = eval I64 1

    commit
        t0 <- %t0
`, plans[0].Render(false))
	require.Equal(t, `statement assign_discard
    source "discard = 2"

    execute
        %t0 = eval I64 2

    commit
        discard <- %t0
`, plans[2].Render(false))
	require.Equal(t, `statement assign__
    source "_ = 3"

    execute
        %t0 = eval I64 3

    commit
        _ <- %t0
`, plans[3].Render(false))
}

// Plan §6, §8, §17: a heap swap is two promoted borrows — zero copies, zero
// releases — while the fresh bindings before it move their owned outcomes.
func TestPlanGoldenHeapSwap(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planHeapSwap", "", `h1 = "foo" ⊕ "bar"
h2 = "baz" ⊕ "qux"
h1, h2 = h2, h1
h1, h2`)
	require.Equal(t, []string{"assign_h1", "assign_h2", "assign_h1_h2"}, planLabels(plans))
	require.Equal(t, `statement assign_h1
    source "h1 = (\"foo\" ⊕ \"bar\")"

    execute
        %t0 = eval Str "foo" ⊕ "bar" [shape=scalar] [yield=always] [owned]

    commit
        h1 : Str <- %t0 [move]
`, plans[0].Render(true))
	require.Equal(t, `statement assign_h1_h2
    source "h1, h2 = h2, h1"

    execute
        %t0 = eval Str h2 [shape=scalar] [yield=always] [borrowed=h2]
        %t1 = eval Str h1 [shape=scalar] [yield=always] [borrowed=h1]

    commit
        h1 : Str <- %t0 [transfer]
        h2 : Str <- %t1 [transfer]
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
	require.Equal(t, `statement assign_d1_d2
    source "d1, d2 = d1, d1"

    execute
        %t0 = eval Str d1 [shape=scalar] [yield=always] [borrowed=d1]
        %t1 = eval Str d1 [shape=scalar] [yield=always] [borrowed=d1]

    commit
        d1 : Str <- %t0 [transfer]
        d2 : Str <- %t1 [copy]
        drop d2 [replaced]
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
	require.Equal(t, []string{"assign_x", "assign_x", "assign__", "assign__", "assign_y", "assign_x_y"}, planLabels(plans))
	require.Equal(t, `statement assign_x
    source "x = (x ⊕ \"!\")"

    execute
        %t0 = eval Str x ⊕ "!" [shape=scalar] [yield=always] [owned]

    commit
        x : Str <- %t0 [move]
        drop x [replaced]
`, plans[1].Render(true))
	require.Equal(t, `statement assign__
    source "_ = (x ⊕ \"?\")"

    execute
        %t0 = eval Str x ⊕ "?" [shape=scalar] [yield=always] [owned]

    commit
        _ <- %t0
        drop %t0
`, plans[2].Render(true))
	require.Equal(t, `statement assign__
    source "_ = x"

    execute
        %t0 = eval Str x [shape=scalar] [yield=always] [borrowed=x]

    commit
        _ <- %t0
`, plans[3].Render(true))
	require.Equal(t, `statement assign_x_y
    source "x, y = (y ⊕ \"!\"), x"

    execute
        %t0 = eval Str y ⊕ "!" [shape=scalar] [yield=always] [owned]
        %t1 = eval Str x [shape=scalar] [yield=always] [borrowed=x]

    commit
        x : Str <- %t0 [move]
        y : Str <- %t1 [transfer]
        drop y [replaced]
`, plans[5].Render(true))
}

// Plan §8, §12: a static string into a heap-string binding materializes an
// owned copy, which the directional compatibility relation admits though
// both flavours display as Str; a binding that only ever holds static
// strings owns nothing and stores plainly.
func TestPlanGoldenMaterialize(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planMaterialize", "", `s = "hi"
s = s ⊕ "!"
g = "static"
t = g
s, t`)
	require.Equal(t, []string{"assign_s", "assign_s", "assign_g", "assign_t"}, planLabels(plans))
	require.Equal(t, `statement assign_s
    source "s = \"hi\""

    execute
        %t0 = eval Str "hi" [shape=scalar] [yield=always] [unmanaged]

    commit
        s : Str <- %t0 [materialize]
`, plans[0].Render(true))
	require.Equal(t, `statement assign_t
    source "t = g"

    execute
        %t0 = eval Str g [shape=scalar] [yield=always] [unmanaged]

    commit
        t : Str <- %t0
`, plans[3].Render(true))
}

// Plan §8, §12: inline array literals are owned outcomes, an array read is
// a borrow that copies, and an empty-literal reset is an unmanaged value
// materialized into the owning binding — the second case the directional
// relation exists for.
func TestPlanGoldenArrays(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planArrays", "", `arr1 = [1 2 3]
arr2 = arr1
arr1, arr2
arr2 = [4 5 6]
arr1 = []
arr1, arr2`)
	require.Equal(t, []string{"assign_arr1", "assign_arr2", "assign_arr2", "assign_arr1"}, planLabels(plans))
	require.Equal(t, `statement assign_arr1
    source "arr1 = [1 2 3]"

    execute
        %t0 = eval [I64] [1 2 3] [shape=scalar] [yield=always] [owned]

    commit
        arr1 : [I64] <- %t0 [move]
`, plans[0].Render(true))
	require.Equal(t, `statement assign_arr2
    source "arr2 = arr1"

    execute
        %t0 = eval [I64] arr1 [shape=scalar] [yield=always] [borrowed=arr1]

    commit
        arr2 : [I64] <- %t0 [copy]
`, plans[1].Render(true))
	require.Equal(t, `statement assign_arr2
    source "arr2 = [4 5 6]"

    execute
        %t0 = eval [I64] [4 5 6] [shape=scalar] [yield=always] [owned]

    commit
        arr2 : [I64] <- %t0 [move]
        drop arr2 [replaced]
`, plans[2].Render(true))
	require.Equal(t, `statement assign_arr1
    source "arr1 = []"

    execute
        %t0 = eval [Empty] [] [shape=scalar] [yield=always] [unmanaged]

    commit
        arr1 : [I64] <- %t0 [materialize]
        drop arr1 [replaced]
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
scores =
[
  : Name Score
    "Ada" 10
]
col = scores.Score
t2 = scores
n, a, s2.age, col, t2`)
	require.Equal(t, []string{"assign_n", "assign_a", "assign_s2", "assign_col", "assign_t2"}, planLabels(plans))
	require.Equal(t, `statement assign_n
    source "n = p.name"

    execute
        %t0 = eval Str p.name [shape=scalar] [yield=always] [unmanaged]

    commit
        n : Str <- %t0
`, plans[0].Render(true))
	require.Equal(t, `statement assign_s2
    source "s2 = p"

    execute
        %t0 = eval Person{name:Str age:I64} p [shape=scalar] [yield=always] [unmanaged]

    commit
        s2 : Person{name:Str age:I64} <- %t0
`, plans[2].Render(true))
	require.Equal(t, `statement assign_col
    source "col = scores.Score"

    execute
        %t0 = eval [I64] scores.Score [shape=scalar] [yield=always] [owned]

    commit
        col : [I64] <- %t0 [move]
`, plans[3].Render(true))
	require.Equal(t, `statement assign_t2
    source "t2 = scores"

    execute
        %t0 = eval Table[Name:Str Score:I64] scores [shape=scalar] [yield=always] [borrowed=scores]

    commit
        t2 : Table[Name:Str Score:I64] <- %t0 [copy]
`, plans[4].Render(true))
}

// Plan §8: ownership is read from a binding's effective storage, not the
// solver's flow-typed read. text solves as a static string after
// `text = "old"` but stores a materialized heap copy; other is declared
// static yet takes text's heap buffer by transfer, so its later read is a
// borrow and replacing it releases the held value — a plain store, since the
// declared type materializes nothing.
func TestPlanGoldenEffectiveStorage(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planEffective", "", `text = "old"
text, other = text ⊕ "!", text
copy = other
other = "new"
copy, other, text`)
	require.Equal(t, []string{"assign_text", "assign_text_other", "assign_copy", "assign_other"}, planLabels(plans))
	require.Equal(t, `statement assign_text_other
    source "text, other = (text ⊕ \"!\"), text"

    execute
        %t0 = eval Str text ⊕ "!" [shape=scalar] [yield=always] [owned]
        %t1 = eval Str text [shape=scalar] [yield=always] [borrowed=text]

    commit
        text : Str <- %t0 [move]
        other : Str <- %t1 [transfer]
`, plans[1].Render(true))
	require.Equal(t, `statement assign_copy
    source "copy = other"

    execute
        %t0 = eval Str other [shape=scalar] [yield=always] [borrowed=other]

    commit
        copy : Str <- %t0 [copy]
`, plans[2].Render(true))
	require.Equal(t, `statement assign_other
    source "other = \"new\""

    execute
        %t0 = eval Str "new" [shape=scalar] [yield=always] [unmanaged]

    commit
        other : Str <- %t0
        drop other [replaced]
`, plans[3].Render(true))
	require.False(t, plans[3].Commit[0].Target.Owns)
	require.True(t, plans[3].Commit[0].Target.Holds)
}

// Plan §8: the same widening through an empty-array reset — the read of arr
// solves as [Empty] before the concrete literal merges in, so other is
// declared empty yet holds the materialized [I64] array it took.
func TestPlanGoldenEffectiveStorageArray(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planEffectiveArray", "", `arr = []
other = arr
arr = [1 2]
copy = other
other = []
copy, other, arr`)
	require.Equal(t, []string{"assign_arr", "assign_other", "assign_arr", "assign_copy", "assign_other"}, planLabels(plans))
	require.Equal(t, `statement assign_other
    source "other = arr"

    execute
        %t0 = eval [I64] arr [shape=scalar] [yield=always] [borrowed=arr]

    commit
        other : [Empty] <- %t0 [copy]
`, plans[1].Render(true))
	require.Equal(t, `statement assign_other
    source "other = []"

    execute
        %t0 = eval [Empty] [] [shape=scalar] [yield=always] [unmanaged]

    commit
        other : [Empty] <- %t0
        drop other [replaced]
`, plans[4].Render(true))
}
