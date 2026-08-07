package compiler

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thiremani/pluto/ast"
	"tinygo.org/x/go-llvm"
)

func requireTargetEffects(t *testing.T, effect StatementEffect, expected ...TargetWriteEffect) {
	t.Helper()
	require.Equal(t, expected, effect.Writes)
}

func TestStatementEffectsStayAlignedAcrossMixedRHSAndDiscard(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "mixedEffects", "", mustParseCode(t, ""))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `arr = [10 20]
i = 0
x, _, y = arr[i], arr[i], i + 1
x, y`)
	stmt := ts.ScriptCompiler.Program.Statements[2].(*ast.LetStatement)
	effect := ts.ScriptCompiler.Script.Root.StatementEffects[stmt]
	requireTargetEffects(t, effect,
		TargetWriteEffect{TargetIndex: 0, Effect: MayWrite},
		TargetWriteEffect{TargetIndex: 2, Effect: MustWrite},
	)
	require.Empty(t, effect.ReadsSeed)

	firstAccess := stmt.Value[0].(*ast.ArrayRangeExpression)
	secondAccess := stmt.Value[1].(*ast.ArrayRangeExpression)
	require.Equal(t, []YieldEffect{MayYield}, ts.ExprCache[key(ts.FuncNameMangled, firstAccess)].YieldEffects)
	require.Equal(t, []YieldEffect{MayYield}, ts.ExprCache[key(ts.FuncNameMangled, secondAccess)].YieldEffects)
	require.Equal(t, []YieldEffect{MustYield}, ts.ExprCache[key(ts.FuncNameMangled, stmt.Value[2])].YieldEffects)
}

func TestFallbackYieldEffectUsesFinalAlternative(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "fallbackEffects", "", mustParseCode(t, ""))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `resolved = 1 > 0 || 2
unresolved = 1 > 0 || 2 > 0
resolved, unresolved`)
	resolved := ts.ScriptCompiler.Program.Statements[0].(*ast.LetStatement)
	unresolved := ts.ScriptCompiler.Program.Statements[1].(*ast.LetStatement)
	requireTargetEffects(t, ts.ScriptCompiler.Script.Root.StatementEffects[resolved], TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	requireTargetEffects(t, ts.ScriptCompiler.Script.Root.StatementEffects[unresolved], TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
	require.Equal(t, []YieldEffect{MustYield}, ts.ExprCache[key(ts.FuncNameMangled, resolved.Value[0])].YieldEffects)
	require.Equal(t, []YieldEffect{MayYield}, ts.ExprCache[key(ts.FuncNameMangled, unresolved.Value[0])].YieldEffects)
}

func TestLiteralDomainEffectUsesProvableEmptiness(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "literalDomainEffects", "", mustParseCode(t, ""))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `nonempty = 5 + 1:3
empty = 5 + 0:0
nonempty, empty`)
	nonempty := ts.ScriptCompiler.Program.Statements[0].(*ast.LetStatement)
	empty := ts.ScriptCompiler.Program.Statements[1].(*ast.LetStatement)
	requireTargetEffects(t, ts.ScriptCompiler.Script.Root.StatementEffects[nonempty], TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	requireTargetEffects(t, ts.ScriptCompiler.Script.Root.StatementEffects[empty], TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
}

func TestDirectCallSeedResolutionIsSeparateFromInvocationFailure(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "callEffects", "", mustParseCode(t, `y = Always(x)
    y = x

y = Maybe(x)
    y = x > 0 x`))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `existing = 7
existing = Maybe(1)
fresh = Maybe(1)
arr = [1]
other = 9
other = Always(arr[2])
existing, other`)
	program := ts.ScriptCompiler.Program
	resolved := program.Statements[1].(*ast.LetStatement)
	fresh := program.Statements[2].(*ast.LetStatement)
	callerFailure := program.Statements[5].(*ast.LetStatement)

	resolvedEffect := ts.ScriptCompiler.Script.Root.StatementEffects[resolved]
	requireTargetEffects(t, resolvedEffect, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Equal(t, []int{0}, resolvedEffect.ReadsSeed)
	require.Equal(t, []YieldEffect{MayYield}, ts.ExprCache[key(ts.FuncNameMangled, resolved.Value[0])].YieldEffects)

	freshEffect := ts.ScriptCompiler.Script.Root.StatementEffects[fresh]
	requireTargetEffects(t, freshEffect, TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
	require.Empty(t, freshEffect.ReadsSeed)

	callerFailureEffect := ts.ScriptCompiler.Script.Root.StatementEffects[callerFailure]
	requireTargetEffects(t, callerFailureEffect, TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
	require.Empty(t, callerFailureEffect.ReadsSeed)

	maybe := cc.Compiler.FuncCache[Mangle(cc.Compiler.MangledPath, "Maybe", []Type{I64})]
	always := cc.Compiler.FuncCache[Mangle(cc.Compiler.MangledPath, "Always", []Type{I64})]
	require.Equal(t, []WriteEffect{MayWrite}, maybe.OutputEffects)
	require.Equal(t, []WriteEffect{MustWrite}, always.OutputEffects)
	require.True(t, maybe.Settled)
	require.True(t, always.Settled)
}

func TestFunctionDomainWeakensOnlyPublishedOutput(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "domainEffects", "", mustParseCode(t, `y = Increment(x)
    y = x + 1`))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `result = Increment(0:0)
result`)
	mangled := Mangle(cc.Compiler.MangledPath, "Increment", []Type{Range{Iter: I64}})
	increment := cc.Compiler.FuncCache[mangled]
	require.NotNil(t, increment)
	require.True(t, increment.HasFunctionDomain)
	require.Equal(t, []WriteEffect{MayWrite}, increment.OutputEffects)

	template, ok := cc.lookupFuncTemplate("Increment", 1)
	require.True(t, ok)
	bodyStmt := template.Body.Statements[0].(*ast.LetStatement)
	requireTargetEffects(t, increment.StatementEffects[bodyStmt], TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})

	callStmt := ts.ScriptCompiler.Program.Statements[0].(*ast.LetStatement)
	requireTargetEffects(t, ts.ScriptCompiler.Script.Root.StatementEffects[callStmt], TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
}

func TestRecursiveOutputEffectsConvergeAcrossSCC(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "recursiveEffects", "", mustParseCode(t, `y = A(n)
    y = B(n)

y = B(n)
    y = n > 0 A(n - 1)
    y = n == 0 "done"`))
	require.Empty(t, cc.Compile())

	solveScriptTypes(t, ctx, cc, t.Name(), `result = A(2)
result`)
	a := cc.Compiler.FuncCache[Mangle(cc.Compiler.MangledPath, "A", []Type{I64})]
	b := cc.Compiler.FuncCache[Mangle(cc.Compiler.MangledPath, "B", []Type{I64})]
	require.Equal(t, []WriteEffect{MayWrite}, a.OutputEffects)
	require.Equal(t, []WriteEffect{MayWrite}, b.OutputEffects)
	require.True(t, a.Settled)
	require.True(t, b.Settled)
}

func TestEffectGraphRejectsMissingCallFacts(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "missingCallEffects", "", mustParseCode(t, `y = Outer(x)
    y = Inner(x)

y = Inner(x)
    y = x`))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `result = Outer(1)
result`)
	outerMangled := Mangle(cc.Compiler.MangledPath, "Outer", []Type{I64})
	innerMangled := Mangle(cc.Compiler.MangledPath, "Inner", []Type{I64})
	outerTemplate, ok := cc.lookupFuncTemplate("Outer", 1)
	require.True(t, ok)
	call := outerTemplate.Body.Statements[0].(*ast.LetStatement).Value[0].(*ast.CallExpression)
	delete(ts.ExprCache, key(outerMangled, call))

	require.PanicsWithValue(t,
		"internal: missing call facts for Inner in specialization "+outerMangled+" during effect graph construction",
		func() {
			ts.buildEffectGraph(map[string]struct{}{outerMangled: {}, innerMangled: {}})
		},
	)
}

func TestScriptEffectsRejectInvalidExpressionFacts(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "invalidScriptEffects", "", mustParseCode(t, ""))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `value = 1
value`)
	stmt := ts.ScriptCompiler.Program.Statements[0].(*ast.LetStatement)
	delete(ts.ExprCache, key(ts.FuncNameMangled, stmt.Value[0]))

	require.PanicsWithValue(t,
		`internal: invalid effects for script statement "value = 1"`,
		ts.deriveScriptEffects,
	)
}
