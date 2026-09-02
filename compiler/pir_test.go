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
	require.Empty(t, sc.Compile())
	return sc.Plans
}

func planNames(plans []*pir.AssignPlan) []string {
	names := make([]string, len(plans))
	for i, p := range plans {
		names[i] = p.Name
	}
	return names
}

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

	require.Equal(t, []string{"assign_a", "assign_b", "assign_a_b", "assign__", "assign_r", "assign_s"}, planNames(plans))

	require.Equal(t, `pir.statement @assign_a_b
    source "a, b = b, a"

    execute
        %t0 = eval @b : I64
        %t1 = eval @a : I64

    commit simultaneous
        @a <- %t0
        @b <- %t1
`, plans[2].Render(false))

	require.Equal(t, `pir.statement @assign_b
    source "b = (a + (2 * 3))"

    execute
        %t0 = eval (@a + (2 * 3)) : I64 [shape=scalar] [yield=always] [unmanaged]

    commit simultaneous
        @b : I64 <- %t0
`, plans[1].Render(true))

	require.Equal(t, `pir.statement @assign__
    source "_ = 7"

    execute
        %t0 = eval 7 : I64

    commit simultaneous
        discard <- %t0
`, plans[3].Render(false))

	require.Equal(t, `pir.statement @assign_r
    source "r = 0:10:2"

    execute
        %t0 = eval 0:10:2 : I64:I64:I64

    commit simultaneous
        @r <- %t0
`, plans[4].Render(false))

	require.Equal(t, `pir.statement @assign_s
    source "s = r"

    execute
        %t0 = eval @r : I64:I64:I64

    commit simultaneous
        @s <- %t0
`, plans[5].Render(false))
}

// TestPlanRouterRejections pins the Step 3 capability boundary: statements
// with gates, conditional values, strings, arrays, checked accesses, ranged
// RHS, or calls keep their legacy lowering, while a discarded Range
// descriptor plans like a discarded scalar. The string
// identifier copies (sg, shc) have fully eligible expression trees, so only
// the value-kind check keeps both string flavors out.
func TestPlanRouterRejections(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planRejections", "", `x = 5
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
g, y, sg, shc, z, w`)

	require.Equal(t, []string{"assign_x", "assign_y", "assign_q", "assign__"}, planNames(plans))
}

func TestPlanValueTypeSupported(t *testing.T) {
	require.True(t, planValueTypeSupported(I64))
	require.True(t, planValueTypeSupported(F64))
	require.True(t, planValueTypeSupported(Range{Iter: I64}))
	require.False(t, planValueTypeSupported(StrG{}))
	require.False(t, planValueTypeSupported(StrH{}))
	require.False(t, planValueTypeSupported(Array{ElemType: I64, Rank: 1}))
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

	require.Equal(t, []string{"assign_seed", "assign_after"}, planNames(plans))
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

	require.Equal(t, []string{"assign_x", "assign_x"}, planNames(plans))
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
