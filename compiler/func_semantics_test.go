package compiler

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"tinygo.org/x/go-llvm"

	"github.com/thiremani/pluto/token"
)

func TestFuncCacheReusePreservesDestinationSlotTypes(t *testing.T) {
	tests := []struct {
		name       string
		moduleName string
		funcName   string
		code       string
		script     string
		slotType   Array
	}{
		{
			name:       "integer array",
			moduleName: "cacheSlotIntArray",
			funcName:   "Reset",
			code: `
res = Reset(k)
    a = [10 20 30]
    a = k > 0 []
    res = a ⊕ [7]
`,
			script: `v = Reset(0)
v`,
			slotType: Array{ElemType: I64, Rank: 1},
		},
		{
			name:       "heap string array",
			moduleName: "cacheSlotHeapStringArray",
			funcName:   "ResetStrings",
			code: `
res = ResetStrings(k)
    a = ["left" ⊕ "!"]
    a = k > 0 []
    res = a ⊕ ["right" ⊕ "!"]
`,
			script: `v = ResetStrings(0)
v`,
			slotType: Array{ElemType: StrH{}, Rank: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := llvm.NewContext()
			defer ctx.Dispose()

			codeCompiler := NewCodeCompiler(ctx, tt.moduleName, "", mustParseCode(t, tt.code))
			require.Empty(t, codeCompiler.Compile())

			funcCache := make(map[string]*Func)
			exprCache := codeCompiler.Compiler.ExprCache
			compile := func() {
				sc := NewScriptCompiler(
					ctx,
					mustParseScript(t, tt.script),
					codeCompiler,
					funcCache,
					exprCache,
				)
				require.Empty(t, sc.Compile())
			}

			compile()
			compile()

			mangled := Mangle(codeCompiler.Compiler.MangledPath, tt.funcName, []Type{I64})
			cachedFunc := funcCache[mangled]
			require.NotNil(t, cachedFunc)
			require.NotNil(t, cachedFunc.semantics, "a completed specialization must publish its semantic snapshot")
			slotType, ok := cachedFunc.semantics.bindingType("a")
			require.True(t, ok, "the specialization cache must own the function-local binding type")
			require.Equal(t, tt.slotType, slotType, "the specialization cache must preserve the concrete destination slot type")
		})
	}
}

func TestFuncCacheReuseUsesNestedCalleeSemantics(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	codeCompiler := NewCodeCompiler(ctx, "cacheNestedSlotTypes", "", mustParseCode(t, `
res = Outer(k)
    res = Inner(k)

res = Inner(k)
    a = [10 20 30]
    a = k > 0 []
    res = a ⊕ [7]
`))
	require.Empty(t, codeCompiler.Compile())

	funcCache := make(map[string]*Func)
	compile := func() *ScriptCompiler {
		sc := NewScriptCompiler(
			ctx,
			mustParseScript(t, "v = Outer(0)\nv"),
			codeCompiler,
			funcCache,
			codeCompiler.Compiler.ExprCache,
		)
		require.Empty(t, sc.Compile())
		return sc
	}

	compile()
	outerMangled := Mangle(codeCompiler.Compiler.MangledPath, "Outer", []Type{I64})
	innerMangled := Mangle(codeCompiler.Compiler.MangledPath, "Inner", []Type{I64})
	cachedOuter := funcCache[outerMangled]
	cachedInner := funcCache[innerMangled]
	require.NotNil(t, cachedOuter)
	require.NotNil(t, cachedOuter.semantics, "the root specialization must be cached before reuse")
	require.NotNil(t, cachedInner)
	require.NotNil(t, cachedInner.semantics, "the nested specialization must be cached before reuse")

	want := Array{ElemType: I64, Rank: 1}
	got, ok := cachedInner.semantics.bindingType("a")
	require.True(t, ok, "the cached nested callee must own its binding inventory")
	require.Equal(t, want, got)

	secondScript := compile()
	require.True(
		t,
		strings.Contains(secondScript.Compiler.GenerateIR(), "call i64 @arr_i64_len(ptr %a_cond_final)"),
		"lazy nested lowering must read the callee's concrete array destination from its semantic snapshot",
	)
}

func TestFuncCacheRejectsIncompleteLocalSemantics(t *testing.T) {
	tests := []struct {
		name       string
		moduleName string
		code       string
		reason     string
	}{
		{
			name:       "unresolved binding",
			moduleName: "cacheIncompleteBinding",
			code: `
res = Outer(k)
    unresolved = Loop(k)
    unresolved
    res = 1

value = Loop(k)
    value = Loop(k)
`,
			reason: "an unresolved source binding must prevent semantic publication",
		},
		{
			name:       "unresolved assignment occurrence",
			moduleName: "cacheIncompleteAssignment",
			code: `
res = Outer(k)
    res = k > 0 Loop(k)
    res = k == 0 1

value = Loop(k)
    value = Loop(k)
`,
			reason: "every assignment occurrence must resolve before semantic publication",
		},
		{
			name:       "unresolved print expression",
			moduleName: "cacheIncompletePrint",
			code: `
res = Outer(k)
    Loop(k)
    res = 1

value = Loop(k)
    value = Loop(k)
`,
			reason: "semantic readiness must validate non-assignment expressions too",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := llvm.NewContext()
			defer ctx.Dispose()

			codeCompiler := NewCodeCompiler(ctx, tt.moduleName, "", mustParseCode(t, tt.code))
			require.Empty(t, codeCompiler.Compile())

			funcCache := make(map[string]*Func)
			compile := func() []*token.CompileError {
				sc := NewScriptCompiler(
					ctx,
					mustParseScript(t, "v = Outer(0)\nv"),
					codeCompiler,
					funcCache,
					codeCompiler.Compiler.ExprCache,
				)
				return sc.Compile()
			}

			firstErrs := compile()
			require.NotEmpty(t, firstErrs)
			require.Contains(t, firstErrs[0].Msg, "Function Outer is not converging")

			outerMangled := Mangle(codeCompiler.Compiler.MangledPath, "Outer", []Type{I64})
			cachedOuter := funcCache[outerMangled]
			require.NotNil(t, cachedOuter)
			require.True(t, cachedOuter.OutputTypesInferred(), "the independent base assignment makes the output concrete")
			require.Nil(t, cachedOuter.semantics, tt.reason)

			secondErrs := compile()
			require.NotEmpty(t, secondErrs)
			require.Equal(t, firstErrs[0].Msg, secondErrs[0].Msg, "warm-cache compilation must reject the same incomplete specialization")
			require.Nil(t, cachedOuter.semantics)
		})
	}
}

func TestFuncCacheRejectsIncompleteNestedCallee(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	codeCompiler := NewCodeCompiler(ctx, "cacheIncompleteNested", "", mustParseCode(t, `
res = Outer(k)
    res = Inner(k)

res = Inner(k)
    unresolved = Loop(k)
    unresolved
    res = 1

value = Loop(k)
    value = Loop(k)

res = Good(k)
    res = k + 1
`))
	require.Empty(t, codeCompiler.Compile())

	funcCache := make(map[string]*Func)
	compile := func() []*token.CompileError {
		sc := NewScriptCompiler(
			ctx,
			mustParseScript(t, "v = Outer(0)\nv"),
			codeCompiler,
			funcCache,
			codeCompiler.Compiler.ExprCache,
		)
		return sc.Compile()
	}

	firstErrs := compile()
	require.NotEmpty(t, firstErrs)
	require.Contains(t, firstErrs[0].Msg, "Function Inner is not converging")

	outerMangled := Mangle(codeCompiler.Compiler.MangledPath, "Outer", []Type{I64})
	innerMangled := Mangle(codeCompiler.Compiler.MangledPath, "Inner", []Type{I64})
	cachedOuter := funcCache[outerMangled]
	cachedInner := funcCache[innerMangled]
	require.NotNil(t, cachedOuter)
	require.NotNil(t, cachedOuter.semantics, "the outer specialization may be locally complete")
	require.NotNil(t, cachedInner)
	require.True(t, cachedInner.OutputTypesInferred(), "the nested output becomes concrete independently of its unresolved local")
	require.Nil(t, cachedInner.semantics, "an incomplete reachable callee must remain unavailable to lowering")

	secondErrs := compile()
	require.NotEmpty(t, secondErrs)
	require.Equal(t, firstErrs[0].Msg, secondErrs[0].Msg, "warm-cache compilation must revalidate the transitive dependency")
	require.Nil(t, cachedInner.semantics)

	unrelated := NewScriptCompiler(
		ctx,
		mustParseScript(t, "v = Good(4)\nv"),
		codeCompiler,
		funcCache,
		codeCompiler.Compiler.ExprCache,
	)
	require.Empty(t, unrelated.Compile(), "an unreachable incomplete cache entry must not block a valid specialization")
}

func TestFuncCacheFinalizesSingleRootMutualRecursion(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	codeCompiler := NewCodeCompiler(ctx, "cacheMutualRecursion", "", mustParseCode(t, `
x, y = IsEven(n)
    x, y = n != 0 IsOdd(n - 1)
    x = n == 1 "no"
    x = n == 0 "yes"

x, y = IsOdd(n)
    x, y = n != 0 IsEven(n - 1)
    y = n == 1 "no"
    y = n == 0 "yes"
`))
	require.Empty(t, codeCompiler.Compile())

	funcCache := make(map[string]*Func)
	compile := func() *ScriptCompiler {
		sc := NewScriptCompiler(
			ctx,
			mustParseScript(t, `x, y = IsEven(4)
x, y`),
			codeCompiler,
			funcCache,
			codeCompiler.Compiler.ExprCache,
		)
		require.Empty(t, sc.Compile())
		return sc
	}

	first := compile()
	second := compile()

	evenMangled := Mangle(codeCompiler.Compiler.MangledPath, "IsEven", []Type{I64})
	oddMangled := Mangle(codeCompiler.Compiler.MangledPath, "IsOdd", []Type{I64})
	cachedEven := funcCache[evenMangled]
	cachedOdd := funcCache[oddMangled]
	require.NotNil(t, cachedEven)
	require.NotNil(t, cachedEven.semantics)
	require.NotNil(t, cachedOdd)
	require.NotNil(t, cachedOdd.semantics, "the reachable half of a recursion cycle must finalize without a second script root")
	require.True(t, cachedOdd.OutputTypesInferred())

	for _, sc := range []*ScriptCompiler{first, second} {
		ir := sc.Compiler.GenerateIR()
		require.Contains(t, ir, "define void @"+evenMangled)
		require.Contains(t, ir, "define void @"+oddMangled)
	}
}
