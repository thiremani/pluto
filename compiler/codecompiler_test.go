package compiler

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/parser"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

func mustParseCodeFile(t *testing.T, filename, input string) *ast.Code {
	t.Helper()

	p := parser.NewCodeParser(lexer.New(filename, input))
	code := p.Parse()
	require.Empty(t, p.Errors(), "CodeParser should have no errors for input: %s", input)
	return code
}

func compileMergedCode(t *testing.T, files ...*ast.Code) []*token.CompileError {
	t.Helper()

	code := ast.NewCode()
	for _, file := range files {
		code.Append(file)
	}

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	return NewCodeCompiler(ctx, "mergedDeclarations", "", code).Compile()
}

func TestDuplicateFunctionsAcrossFilesAreRejected(t *testing.T) {
	earlier := mustParseCodeFile(t, "a.pt", `r = Duplicate(x)
    r = x`)
	later := mustParseCodeFile(t, "b.pt", `r = Duplicate(x)
    r = x`)

	errs := compileMergedCode(t, earlier, later)

	require.Len(t, errs, 1)
	require.Equal(t, "b.pt", errs[0].Token.FileName)
	require.Contains(t, errs[0].Msg, "Function Duplicate with 1 parameters has been previously defined")
	require.Contains(t, errs[0].Msg, "a.pt:")
}

func TestDuplicateDeclarationsWithinFileFollowSourceOrder(t *testing.T) {
	code := mustParseCodeFile(t, "same.pt", `answer = 41

r = Duplicate(x)
    r = x

r = Duplicate(x)
    r = x

answer = 42`)

	errs := compileMergedCode(t, code)

	require.Len(t, errs, 2)
	require.Contains(t, errs[0].Msg, "Function Duplicate with 1 parameters")
	require.Contains(t, errs[0].Msg, "same.pt:3:")
	require.Contains(t, errs[1].Msg, "global redeclaration of constant answer")
	require.Contains(t, errs[1].Msg, "same.pt:1:")
}

func TestFunctionOverloadsAcrossFilesAreAllowed(t *testing.T) {
	unary := mustParseCodeFile(t, "a.pt", `r = Overload(x)
    r = x`)
	binary := mustParseCodeFile(t, "b.pt", `r = Overload(x, y)
    r = x + y`)

	require.Empty(t, compileMergedCode(t, unary, binary))
}

func TestDuplicateGlobalBindingsAcrossFilesAreRejected(t *testing.T) {
	tests := []struct {
		name    string
		earlier string
		later   string
	}{
		{
			name:    "constants",
			earlier: "answer = 41",
			later:   "answer = 42",
		},
		{
			name: "struct constant and regular constant",
			earlier: `person = Person
  : name
    "Ada"`,
			later: "person = 42",
		},
		{
			name:    "regular constant and struct constant",
			earlier: "person = 42",
			later: `person = Person
  : name
    "Ada"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			earlier := mustParseCodeFile(t, "a.pt", tt.earlier)
			later := mustParseCodeFile(t, "b.pt", tt.later)

			errs := compileMergedCode(t, earlier, later)

			require.Len(t, errs, 1)
			require.Equal(t, "b.pt", errs[0].Token.FileName)
			require.Contains(t, errs[0].Msg, "global redeclaration of constant")
			require.Contains(t, errs[0].Msg, "a.pt:")
		})
	}
}
