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

func TestFuncCacheReuse(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	codeSrc := `
c = add(a, b)
    c = a + b
`
	codeAST := mustParseCode(t, codeSrc)
	codeCompiler := NewCodeCompiler(ctx, "cacheTestCode", codeAST)

	// This is the shared cache that will persist across compilations.
	funcCache := make(map[string]*Func)
	exprCache := codeCompiler.Compiler.ExprCache

	t.Run("Compile with Ints to populate cache", func(t *testing.T) {
		scriptA := `x = add(1, 2)
x`
		progA := mustParseScript(t, scriptA)
		scA := NewScriptCompiler(ctx, "A", progA, codeCompiler, funcCache, exprCache)

		errs := scA.Compile()
		require.Empty(t, errs, "First script compilation should succeed")

		// Assert that the cache is now populated correctly.
		require.Len(t, funcCache, 1, "Cache size should be 1 after first compile")

		key := "$add$I64$I64"
		assert.Contains(t, funcCache, key, "Cache should contain the integer version of add")
	})

	t.Run("Compile with Ints again to test cache hit", func(t *testing.T) {
		// Get the original *Func pointer from the cache to compare against later.
		key := "$add$I64$I64"
		f1 := funcCache[key]
		require.NotNil(t, f1, "Func instance from first compile should exist")

		scriptB := `y = add(3, 4)
y`
		progB := mustParseScript(t, scriptB)
		scB := NewScriptCompiler(ctx, "B", progB, codeCompiler, funcCache, exprCache)

		errs := scB.Compile()
		require.Empty(t, errs, "Second script compilation should succeed")

		// Assert that the cache was reused, not added to.
		assert.Len(t, funcCache, 1, "Cache size should remain 1, proving reuse")

		// Assert that the instance in the cache is the exact same one.
		assert.Same(t, f1, funcCache[key], "Func instance should be the same pointer, proving no re-creation")
	})

	t.Run("Compile with Floats to test cache miss", func(t *testing.T) {
		scriptC := `z = add(1.0, 2.5)
z`
		progC := mustParseScript(t, scriptC)
		scC := NewScriptCompiler(ctx, "C", progC, codeCompiler, funcCache, exprCache)

		errs := scC.Compile()
		require.Empty(t, errs, "Third script compilation should succeed")

		// Assert that a NEW entry was added to the cache.
		assert.Len(t, funcCache, 2, "Cache size should be 2 after compiling a new type")

		mangledFloatKey := "$add$F64$F64" // Assuming float is F64
		assert.Contains(t, funcCache, mangledFloatKey, "Cache should now contain the float version of add")
	})
}
