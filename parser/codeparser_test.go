package parser

import (
	"pluto/ast"
	"pluto/lexer"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseConstStatement(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
		errs     []string
	}{
		{
			"a = 5",
			[]string{"a"},
			nil,
		},
		{
			"a, b = 5, 10",
			[]string{"a", "b"},
			nil,
		},
		{
			"a, a = 1, 2",
			nil,
			[]string{"duplicate identifier in this statement: a"},
		},
		{
			"a = 5\nb, a = 10, 5",
			nil,
			[]string{"global redeclaration of constant a"},
		},
	}

	for _, tt := range tests {
		l := lexer.New(tt.input)
		p := NewCodeParser(l)
		code := p.Parse()

		if tt.errs != nil {
			require.Len(t, p.Errors(), len(tt.errs))
			for i, err := range tt.errs {
				require.Contains(t, p.Errors()[i], err)
			}
			continue
		}

		require.Empty(t, p.Errors())
		require.Len(t, code.Const.Statements, 1)

		stmt := code.Const.Statements[0]
		require.Len(t, stmt.Name, len(tt.expected))
		for i, ident := range stmt.Name {
			require.Equal(t, tt.expected[i], ident.Value)
		}
	}
}

func TestParseFuncStatement(t *testing.T) {
	tests := []struct {
		input  string
		name   string
		params []string
		errs   []string
	}{
		{
			`y = square(x)
    y = x * x`,
			"square",
			[]string{"x"},
			nil,
		},
		{
			`sum = add(a, b)
    sum = a + b`,
			"add",
			[]string{"a", "b"},
			nil,
		},
		{
			`log = logger()
    print("log")`,
			"logger",
			[]string{},
			nil,
		},
		{
			`bad = func(x, x)
    bed = x * 2`,
			"func",
			nil,
			[]string{"duplicate parameter x"},
		},
		{
			`empty = func(x,)
    x = x + 1`,
			"func",
			nil,
			[]string{"expected next token to be IDENT, got ) instead"},
		},
	}

	for _, tt := range tests {
		l := lexer.New(tt.input)
		p := NewCodeParser(l)
		code := p.Parse()

		if tt.errs != nil {
			require.NotEmpty(t, p.Errors())
			for _, err := range tt.errs {
				require.Contains(t, p.Errors()[0], err)
			}
			continue
		}

		require.Empty(t, p.Errors())
		require.Len(t, code.Func.Statements, 1)

		fn := code.Func.Statements[0]
		require.Equal(t, tt.name, fn.Token.Literal)
		require.Len(t, fn.Parameters, len(tt.params))
		for i, param := range fn.Parameters {
			require.Equal(t, tt.params[i], param.Value)
		}
	}
}

func TestFunctionOverloading(t *testing.T) {
	input := `c = add(a, b)
    y = a + b

y = add(a, b, c)
    a + b + c
`
	l := lexer.New(input)
	p := NewCodeParser(l)
	code := p.Parse()

	require.Empty(t, p.Errors())
	require.Len(t, code.Func.Statements, 2)

	// Verify both functions exist with different arities
	key1 := ast.FuncKey{FuncName: "add", Arity: 2}
	key2 := ast.FuncKey{FuncName: "add", Arity: 3}
	require.NotNil(t, code.Func.Map[key1])
	require.NotNil(t, code.Func.Map[key2])
}

func TestMixedValidInvalid(t *testing.T) {
	input := `valid = 42
invalid = f(x, x)
    invalid = x * 2
`
	l := lexer.New(input)
	p := NewCodeParser(l)
	p.Parse()

	require.Len(t, p.Errors(), 1)
	require.Contains(t, p.Errors()[0], "duplicate parameter x")
}

/*
func TestFunctionLiteralParsing(t *testing.T) {
	input := `y, quo = pow(x, n)
    y = 1
    quo = y / x
    y = 0:n x`

	l := lexer.New(input)
	p := New(l, false)
	program := p.ParseProgram()
	checkParserErrors(t, p)

	if len(program.Statements) != 1 {
		t.Fatalf("program.Statements does not contain %d statements. got=%d\n", 1, len(program.Statements))
	}

	stmt, ok := program.Statements[0].(*ast.LetStatement)
	if !ok {
		t.Fatalf("program.Statements[0] is not an ast.ExpressionStatement. got=%T", program.Statements[0])
	}

	if !testIdentifier(t, stmt.Name[0], "y") {
		return
	}

	if !testIdentifier(t, stmt.Name[1], "quo") {
		return
	}

	if len(stmt.Value) != 1 {
		t.Fatalf("stmt does not contain %d value. got=%d\n", 1, len(stmt.Value))
	}

	f, ok := stmt.Value[0].(*ast.FunctionLiteral)
	if !ok {
		t.Fatalf("stmt.Expression is not ast.FunctionLiteral. got=%T",
			stmt.Value[0])
	}

	if len(f.Parameters) != 2 {
		t.Fatalf("function literal parameters wrong. want 2, got=%d\n",
			len(f.Parameters))
	}

	if !testIdentifier(t, f.Parameters[0], "x") {
		return
	}

	if !testIdentifier(t, f.Parameters[1], "n") {
		return
	}

	if len(f.Body.Statements) != 3 {
		t.Fatalf("function literal body has wrong number of statements. want 3, got=%d\n", len(f.Body.Statements))
	}

	stmt1 := f.Body.Statements[0].(*ast.LetStatement)
	if !testIdentifier(t, stmt1.Name[0], "y") {
		return
	}
	if !testIntegerLiteral(t, stmt1.Value[0], 1) {
		return
	}

	stmt3 := f.Body.Statements[2].(*ast.LetStatement)
	if !testIdentifier(t, stmt3.Name[0], "y") {
		return
	}

	if !testInfixExpression(t, stmt3.Condition[0], 0, ":", "n") {
		return
	}

	if !testIdentifier(t, stmt3.Value[0], "x") {
		return
	}
}

func TestFunctionParameterParsing(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{input: `a = fn()
    a = 4`, expected: []string{}},
		{input: `y = f(x)
    y = x * x`, expected: []string{"x"}},
		{input: `r = f(x, y, z)
    r = x + y + z`, expected: []string{"x", "y", "z"}},
	}

	for _, tt := range tests {
		l := lexer.New(tt.input)
		p := New(l, false)
		program := p.ParseProgram()
		checkParserErrors(t, p)

		val := program.Statements[0].(*ast.LetStatement).Value
		function := val[0].(*ast.FunctionLiteral)

		if len(function.Parameters) != len(tt.expected) {
			t.Errorf("length parameters wrong. want %d, got=%d\n",
				len(tt.expected), len(function.Parameters))
		}

		for i, ident := range tt.expected {
			testLiteralExpression(t, function.Parameters[i], ident)
		}
	}
}

*/
