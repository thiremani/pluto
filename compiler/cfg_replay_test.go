package compiler

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

func compileCFGReplayScript(t *testing.T, ctx llvm.Context, cc *CodeCompiler, name, source string) (*ScriptCompiler, []*token.CompileError) {
	t.Helper()

	sc := NewScriptCompiler(ctx, name, mustParseScript(t, source), cc)

	return sc, sc.Compile()
}

func TestCFGReplayColdAndWarm(t *testing.T) {
	code := mustParseCode(t, `result = Noisy(x)
    unused = x + 1
    result = x
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "cfgReplayColdWarm", "", code)
	require.Empty(t, cc.Compile())

	_, coldErrors := compileCFGReplayScript(t, ctx, cc, t.Name()+"Cold", "value = Noisy(1)\nvalue")
	require.Len(t, coldErrors, 1)
	require.Contains(t, coldErrors[0].Msg, `"unused"`)

	noisyMangled := Mangle(cc.Compiler.MangledPath, "Noisy", []Type{I64})
	coldInfo := cc.Compiler.FuncCache[noisyMangled]
	require.NotNil(t, coldInfo)
	require.True(t, coldInfo.Settled)
	require.NotNil(t, coldInfo.CFGResult)
	require.Len(t, coldInfo.CFGResult.Errors, 1)
	require.Same(t, coldInfo.CFGResult.Errors[0], coldErrors[0],
		"cold replay must return the immutable cached diagnostic")

	coldResult := coldInfo.CFGResult
	_, warmErrors := compileCFGReplayScript(t, ctx, cc, t.Name()+"Warm", "value = Noisy(2)\nvalue")
	require.Len(t, warmErrors, 1, "each script replay must report the reachable diagnostic")

	warmInfo := cc.Compiler.FuncCache[noisyMangled]
	require.Same(t, coldInfo, warmInfo, "the warm solve must reuse the settled specialization")
	require.Same(t, coldResult, warmInfo.CFGResult, "the warm solve must not republish the CFG result")
	require.Same(t, coldErrors[0], warmErrors[0],
		"warm replay must return the same cached diagnostic pointer")
}

func TestCFGReplayDeduplicatesConcreteTypes(t *testing.T) {
	code := mustParseCode(t, `result = NoisyTypes(x)
    unused = x + 1
    result = x
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "cfgReplayConcreteTypes", "", code)
	require.Empty(t, cc.Compile())

	_, errors := compileCFGReplayScript(t, ctx, cc, t.Name(), `integer = NoisyTypes(1)
floating = NoisyTypes(2.0)
integer, floating`)
	require.Len(t, errors, 1, "one template diagnostic must be reported once per script")

	integerMangled := Mangle(cc.Compiler.MangledPath, "NoisyTypes", []Type{I64})
	floatingMangled := Mangle(cc.Compiler.MangledPath, "NoisyTypes", []Type{F64})
	integerInfo := cc.Compiler.FuncCache[integerMangled]
	floatingInfo := cc.Compiler.FuncCache[floatingMangled]
	require.Len(t, integerInfo.CFGResult.Errors, 1)
	require.Len(t, floatingInfo.CFGResult.Errors, 1)
	require.NotSame(t, integerInfo.CFGResult.Errors[0], floatingInfo.CFGResult.Errors[0],
		"each specialization must retain its immutable analysis result")
	require.Equal(t, integerInfo.CFGResult.Errors[0].Error(), floatingInfo.CFGResult.Errors[0].Error())
	require.Same(t, integerInfo.CFGResult.Errors[0], errors[0],
		"replay must retain the first source-ordered diagnostic")
}

func TestCFGReplayPreservesDistinctDiagnostics(t *testing.T) {
	code := mustParseCode(t, `result = NoisyPair(x)
    firstUnused = x + 1
    secondUnused = x + 2
    result = x
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "cfgReplayDistinctDiagnostics", "", code)
	require.Empty(t, cc.Compile())

	_, errors := compileCFGReplayScript(t, ctx, cc, t.Name(), `integer = NoisyPair(1)
floating = NoisyPair(2.0)
integer, floating`)
	require.Len(t, errors, 2, "distinct source diagnostics must survive specialization deduplication")
	require.Contains(t, errors[0].Msg, `"secondUnused"`)
	require.Contains(t, errors[1].Msg, `"firstUnused"`)

	integerMangled := Mangle(cc.Compiler.MangledPath, "NoisyPair", []Type{I64})
	floatingMangled := Mangle(cc.Compiler.MangledPath, "NoisyPair", []Type{F64})
	integerInfo := cc.Compiler.FuncCache[integerMangled]
	floatingInfo := cc.Compiler.FuncCache[floatingMangled]
	require.Len(t, integerInfo.CFGResult.Errors, 2)
	require.Len(t, floatingInfo.CFGResult.Errors, 2)
	for index := range errors {
		require.Same(t, integerInfo.CFGResult.Errors[index], errors[index])
	}
}

func TestUnreachableCachedCFGDiagnosticsDoNotLeak(t *testing.T) {
	code := mustParseCode(t, `result = Noisy(x)
    unused = x + 1
    result = x

result = Clean(x)
    result = x
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "cfgReplayUnreachable", "", code)
	require.Empty(t, cc.Compile())

	_, noisyErrors := compileCFGReplayScript(t, ctx, cc, t.Name()+"Noisy", "value = Noisy(1)\nvalue")
	require.Len(t, noisyErrors, 1)

	noisyMangled := Mangle(cc.Compiler.MangledPath, "Noisy", []Type{I64})
	noisyInfo := cc.Compiler.FuncCache[noisyMangled]
	require.NotNil(t, noisyInfo)
	require.NotNil(t, noisyInfo.CFGResult)
	require.Same(t, noisyInfo.CFGResult.Errors[0], noisyErrors[0])

	_, cleanErrors := compileCFGReplayScript(t, ctx, cc, t.Name()+"Clean", "value = Clean(1)\nvalue")
	require.Empty(t, cleanErrors,
		"a cached diagnostic must not replay when its specialization is unreachable")
	require.Same(t, noisyInfo, cc.Compiler.FuncCache[noisyMangled])
}

func TestWrapperReplaysSettledCalleeDiagnostics(t *testing.T) {
	code := mustParseCode(t, `result = NoisyLeaf(x)
    leafDead = x + 1
    result = x

result = Wrapper(x)
    result = NoisyLeaf(x)
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "cfgReplaySettledCallee", "", code)
	require.Empty(t, cc.Compile())

	_, leafErrors := compileCFGReplayScript(t, ctx, cc, t.Name()+"Leaf", "value = NoisyLeaf(1)\nvalue")
	require.Len(t, leafErrors, 1)

	leafMangled := Mangle(cc.Compiler.MangledPath, "NoisyLeaf", []Type{I64})
	leafInfo := cc.Compiler.FuncCache[leafMangled]
	require.True(t, leafInfo.Settled)
	require.NotNil(t, leafInfo.CFGResult)
	require.Same(t, leafInfo.CFGResult.Errors[0], leafErrors[0])

	_, wrapperErrors := compileCFGReplayScript(t, ctx, cc, t.Name()+"Wrapper", "value = Wrapper(1)\nvalue")
	require.Len(t, wrapperErrors, 1)
	require.Same(t, leafErrors[0], wrapperErrors[0])

	wrapperMangled := Mangle(cc.Compiler.MangledPath, "Wrapper", []Type{I64})
	wrapperInfo := cc.Compiler.FuncCache[wrapperMangled]
	require.True(t, wrapperInfo.Settled)
	require.NotNil(t, wrapperInfo.CFGResult)
	require.Equal(t, []string{leafMangled}, wrapperInfo.CFGResult.DirectCallees,
		"persistent replay edges must include callees settled before the wrapper batch")
}

func TestPrintOnlyUserCallReplaysCFGDiagnostics(t *testing.T) {
	code := mustParseCode(t, `result = NoisyPrint(x)
    printDead = x + 1
    result = x
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "cfgReplayPrintOnly", "", code)
	require.Empty(t, cc.Compile())

	sc, errors := compileCFGReplayScript(t, ctx, cc, t.Name(), "NoisyPrint(1)")
	require.Len(t, errors, 1)
	require.Contains(t, errors[0].Msg, `"printDead"`)

	mangled := Mangle(cc.Compiler.MangledPath, "NoisyPrint", []Type{I64})
	directCallees, _ := collectSpecializationCallEdges(sc.Compiler, sc.ScriptMangled, sc.Program.Statements)
	require.Equal(t, []string{mangled}, directCallees,
		"a user call reached only through print arguments must be a replay root")
	require.Same(t, cc.Compiler.FuncCache[mangled].CFGResult.Errors[0], errors[0])
}

func TestCFGReplayDeduplicatesScalarCompanion(t *testing.T) {
	code := mustParseCode(t, `result = GatherNoisy(arr)
    i = 0:3
    result = [ScaleNoisy(arr[i])]

result = ScaleNoisy(x)
    unusedScale = x
    result = x
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "cfgReplayScalarCompanion", "", code)
	require.Empty(t, cc.Compile())

	sc, errors := compileCFGReplayScript(t, ctx, cc, t.Name(), `arr = [10 20 30]
result = GatherNoisy(arr)
result`)
	require.Len(t, errors, 1, "a primary specialization and scalar companion share one source diagnostic")

	gatherMangled := Mangle(cc.Compiler.MangledPath, "GatherNoisy", []Type{Array{ElemType: I64, Rank: 1}})
	gatherTemplate, ok := cc.lookupFuncTemplate("GatherNoisy", 1)
	require.True(t, ok)
	gatherCalls := collectBodyCalls(gatherTemplate.Body.Statements)
	require.Len(t, gatherCalls, 1)
	gatherCall := gatherCalls[0]
	callInfo := sc.Compiler.ExprCache[key(gatherMangled, gatherCall)]
	require.True(t, callInfo.ScalarCallVariantEnsured)

	primaryMangled := Mangle(cc.Compiler.MangledPath, "ScaleNoisy", callInfo.CallParamTypes)
	scalarMangled := Mangle(cc.Compiler.MangledPath, "ScaleNoisy", callInfo.ScalarCallParamTypes)
	require.NotEqual(t, primaryMangled, scalarMangled)
	primaryInfo := cc.Compiler.FuncCache[primaryMangled]
	scalarInfo := cc.Compiler.FuncCache[scalarMangled]
	require.Len(t, primaryInfo.CFGResult.Errors, 1)
	require.Len(t, scalarInfo.CFGResult.Errors, 1)
	require.NotSame(t, primaryInfo.CFGResult.Errors[0], scalarInfo.CFGResult.Errors[0],
		"both actual lowering targets must remain independently analyzed")
	require.Equal(t, primaryInfo.CFGResult.Errors[0].Error(), scalarInfo.CFGResult.Errors[0].Error())
	require.Same(t, primaryInfo.CFGResult.Errors[0], errors[0])
}

func TestDiamondCFGReplayIsOnceAndDeterministic(t *testing.T) {
	code := mustParseCode(t, `result = Shared(x)
    sharedDead = x + 1
    result = x

result = Left(x)
    leftDead = x + 2
    result = Shared(x)

result = Right(x)
    rightDead = x + 3
    result = Shared(x)

result = Diamond(x)
    diamondDead = x + 4
    left = Left(x)
    right = Right(x)
    result = left + right
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "cfgReplayDiamond", "", code)
	require.Empty(t, cc.Compile())

	_, errors := compileCFGReplayScript(t, ctx, cc, t.Name(), "value = Diamond(1)\nvalue")
	require.Len(t, errors, 4)

	for i, name := range []string{"diamondDead", "leftDead", "sharedDead", "rightDead"} {
		require.Contains(t, errors[i].Msg, `"`+name+`"`,
			"diagnostics must replay in root-first depth-first source order")
	}

	sharedMangled := Mangle(cc.Compiler.MangledPath, "Shared", []Type{I64})
	sharedError := cc.Compiler.FuncCache[sharedMangled].CFGResult.Errors[0]
	sharedOccurrences := 0
	for _, cfgError := range errors {
		if cfgError == sharedError {
			sharedOccurrences++
		}
	}
	require.Equal(t, 1, sharedOccurrences,
		"the visited set must replay a shared diamond callee exactly once")
}

func TestCFGResultsAreIndependentPerType(t *testing.T) {
	code := mustParseCode(t, `result = MaskOrKeep(x)
    result = x
    result = x > 0
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "cfgPerSpecialization", "", code)
	require.Empty(t, cc.Compile())

	_, scalarErrors := compileCFGReplayScript(t, ctx, cc, t.Name()+"Scalar", "value = MaskOrKeep(1)\nvalue")
	require.Empty(t, scalarErrors,
		"a scalar comparison may skip its write, so the preceding output remains live")

	scalarMangled := Mangle(cc.Compiler.MangledPath, "MaskOrKeep", []Type{I64})
	scalarResult := cc.Compiler.FuncCache[scalarMangled].CFGResult
	require.NotNil(t, scalarResult)
	require.Empty(t, scalarResult.Errors)

	_, arrayErrors := compileCFGReplayScript(t, ctx, cc, t.Name()+"Array", "value = MaskOrKeep([1 2])\nvalue")
	require.NotEmpty(t, arrayErrors,
		"an array comparison materializes unconditionally and overwrites the preceding output")

	arrayMangled := Mangle(cc.Compiler.MangledPath, "MaskOrKeep", []Type{
		Array{ElemType: I64, Rank: 1},
	})
	arrayResult := cc.Compiler.FuncCache[arrayMangled].CFGResult
	require.NotNil(t, arrayResult)
	require.NotSame(t, scalarResult, arrayResult)
	require.NotEmpty(t, arrayResult.Errors)
	require.Contains(t, arrayResult.Errors[0].Msg, `unconditional assignment to "result"`)
}
