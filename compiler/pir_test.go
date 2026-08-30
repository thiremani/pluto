package compiler

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/pir"
	"tinygo.org/x/go-llvm"
)

// compileScriptPlans runs the full script pipeline and returns the statement
// plans the PIR router accepted, in source order. Validation is a test-time
// contract, so every plan the builder emitted is validated here.
func compileScriptPlans(t *testing.T, ctx llvm.Context, name, code, script string) []*pir.AssignPlan {
	t.Helper()
	cc := NewCodeCompiler(ctx, name, "", mustParseCode(t, code))
	require.Empty(t, cc.Compile())
	sc := NewScriptCompiler(ctx, name, mustParseScript(t, script), cc)
	require.Empty(t, sc.Compile())
	for _, plan := range sc.Plans {
		require.NoError(t, pir.Validate(plan))
	}
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
// RHS, calls, or non-scalar discards keep their legacy lowering.
func TestPlanRouterRejections(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planRejections", "", `x = 5
g = x > 2 13
y = 0
y = x > 2
s = "hi"
arr = [1 2]
z = arr[0]
q = 0:3
w = q + 1
_ = 0:3
g, y, s, z, w`)

	require.Equal(t, []string{"assign_x", "assign_y", "assign_q"}, planNames(plans))
}

// TestPlanRouterScriptRootOnly verifies function-body statements stay on
// legacy lowering: only script-root assignments produce plans, and a call RHS
// is itself rejected.
func TestPlanRouterScriptRootOnly(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planScriptRoot", `c = addOne(a)
    t = a + 1
    c = t * 1
`, `seed = 2
res = addOne(seed)
res`)

	require.Equal(t, []string{"assign_seed"}, planNames(plans))
}

func TestPlanRouterFreshVsExistingTargets(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	// A promoted (pointer-backed) destination stays eligible: the gated
	// statement in between forces x through memory, and the final plain
	// assignment must still plan.
	plans := compileScriptPlans(t, ctx, "planPromoted", "", `x = 1
x = x > 0 5
x = x + 1
x`)

	require.Equal(t, []string{"assign_x", "assign_x"}, planNames(plans))
}

func TestPlanValidateCatchesBuilderDrift(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	plans := compileScriptPlans(t, ctx, "planValidate", "", "v = 1\nv")
	require.Len(t, plans, 1)
	require.NoError(t, pir.Validate(plans[0]))

	broken := *plans[0]
	broken.Commit = append([]pir.Mapping(nil), broken.Commit...)
	broken.Commit[0].Outcome.Outcome = 9
	require.Error(t, pir.Validate(&broken))
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
