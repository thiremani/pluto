package ast_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/parser"
)

func parseCodeFile(t *testing.T, filename, input string) *ast.Code {
	t.Helper()

	p := parser.NewCodeParser(lexer.New(filename, input))
	code := p.Parse()
	require.Empty(t, p.Errors())
	return code
}

func TestCodeAppendRecordsConflictsAndPreservesFirstDeclarations(t *testing.T) {
	earlier := parseCodeFile(t, "a.pt", `answer = 41

r = Duplicate(x)
    r = x`)
	later := parseCodeFile(t, "b.pt", `r = Duplicate(x)
    r = x

answer = 42`)

	code := ast.NewCode()
	code.Append(earlier)
	code.Append(later)

	require.Len(t, code.DeclarationConflicts, 2)
	require.Equal(t, ast.FunctionDeclaration, code.DeclarationConflicts[0].Kind)
	require.Equal(t, ast.GlobalBindingDeclaration, code.DeclarationConflicts[1].Kind)

	key := ast.FuncKey{FuncName: "Duplicate", Arity: 1}
	require.Same(t, earlier.Func.Map[key], code.Func.Map[key])
	require.Same(t, earlier.Const.Map["answer"], code.Const.Map["answer"])
}
