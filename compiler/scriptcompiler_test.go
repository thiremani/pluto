package compiler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tinygo.org/x/go-llvm"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/parser"
)

// mustParseScript is a helper to parse script code for testing.
func mustParseScript(t *testing.T, input string) *ast.Program {
	l := lexer.New("test.spt", input)
	p := parser.NewScriptParser(l)
	prog := p.Parse()
	require.Empty(t, p.Errors(), "Parser should have no errors for input: %s", input)
	return prog
}

// mustParseCode is a helper to parse .pt file code for testing.
func mustParseCode(t *testing.T, input string) *ast.Code {
	l := lexer.New("test.pt", input)
	p := parser.NewCodeParser(l)
	code := p.Parse()
	require.Empty(t, p.Errors(), "CodeParser should have no errors for input: %s", input)
	return code
}

func TestScriptRootIsNamespacedAndCached(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "scriptRoots", "", ast.NewCode())
	report := NewScriptCompiler(ctx, "report", mustParseScript(t, "value = 1\nvalue"), cc)
	summary := NewScriptCompiler(ctx, "summary", mustParseScript(t, "value = 2\nvalue"), cc)

	require.Equal(t, MangleScript(cc.Compiler.MangledPath, "report"), report.Script.Mangle())
	require.NotEqual(t, report.Script.Mangle(), summary.Script.Mangle())
	require.NotEqual(t, report.Script.Mangle(), (&Script{Name: "report", MangledPath: "other"}).Mangle())
	require.Same(t, report.Script.Root, cc.Compiler.FuncCache[report.Script.Mangle()])
	require.Same(t, summary.Script.Root, cc.Compiler.FuncCache[summary.Script.Mangle()])
	require.Equal(t, report.Script.Mangle(), report.Compiler.FuncNameMangled)
	require.Equal(t, summary.Script.Mangle(), summary.Compiler.FuncNameMangled)

	reportSolver := NewTypeSolver(report)
	reportSolver.Solve()
	require.Empty(t, reportSolver.Errors)
	require.Equal(t, I64, report.Script.Root.Vars["value"])
}

func TestFuncCacheReuse(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	codeSrc := `
c = add(a, b)
    c = a + b
`
	codeAST := mustParseCode(t, codeSrc)
	codeCompiler := NewCodeCompiler(ctx, "cacheTestCode", "", codeAST)
	require.Empty(t, codeCompiler.Compile())

	funcCache := codeCompiler.Compiler.FuncCache

	t.Run("Compile with Ints to populate cache", func(t *testing.T) {
		scriptA := `x = add(1, 2)
x`
		progA := mustParseScript(t, scriptA)
		scA := NewScriptCompiler(ctx, t.Name(), progA, codeCompiler)

		errs := scA.Compile()
		require.Empty(t, errs, "First script compilation should succeed")

		// Assert that the cache is now populated correctly.
		require.Len(t, funcCache, 2, "cache should contain the script root and integer specialization")

		key := "Pt_13cacheTestCode_p_3add_f2_I64_I64"
		assert.Contains(t, funcCache, key, "Cache should contain the integer version of add")
	})

	t.Run("Compile with Ints again to test cache hit", func(t *testing.T) {
		// Get the original *FuncInfo pointer from the cache to compare against later.
		key := "Pt_13cacheTestCode_p_3add_f2_I64_I64"
		f1 := funcCache[key]
		require.NotNil(t, f1, "FuncInfo instance from first compile should exist")

		scriptB := `y = add(3, 4)
y`
		progB := mustParseScript(t, scriptB)
		scB := NewScriptCompiler(ctx, t.Name(), progB, codeCompiler)

		errs := scB.Compile()
		require.Empty(t, errs, "Second script compilation should succeed")

		// Assert that the cache was reused, not added to.
		assert.Len(t, funcCache, 3, "the second script root should reuse the integer specialization")

		// Assert that the instance in the cache is the exact same one.
		assert.Same(t, f1, funcCache[key], "FuncInfo instance should be the same pointer, proving no re-creation")
	})

	t.Run("Compile with Floats to test cache miss", func(t *testing.T) {
		scriptC := `z = add(1.0, 2.5)
z`
		progC := mustParseScript(t, scriptC)
		scC := NewScriptCompiler(ctx, t.Name(), progC, codeCompiler)

		errs := scC.Compile()
		require.Empty(t, errs, "Third script compilation should succeed")

		// Assert that a NEW entry was added to the cache.
		assert.Len(t, funcCache, 5, "the third script root should add one float specialization")

		mangledFloatKey := "Pt_13cacheTestCode_p_3add_f2_F64_F64"
		assert.Contains(t, funcCache, mangledFloatKey, "Cache should now contain the float version of add")
	})
}
