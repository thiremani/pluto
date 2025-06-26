package parser

import (
	"fmt"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"strings"
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
			[]string{"duplicate identifier: a in this statement"},
		},
		{
			"a = 5\nb, a = 10, 5",
			nil,
			[]string{"global redeclaration of constant a"},
		},
	}

	for _, tt := range tests {
		l := lexer.New("TestParseConstStatement", tt.input)
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
			[]string{"duplicate identifier: x in this statement"},
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
		l := lexer.New("TestParseFuncStatement", tt.input)
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
	l := lexer.New("TestFunctionOverloading", input)
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
	l := lexer.New("TestMixedValidInvalid", input)
	p := NewCodeParser(l)
	p.Parse()

	require.Len(t, p.Errors(), 1)
	require.Contains(t, p.Errors()[0], "duplicate identifier: x in this statement")
}

func TestFuncStatementParsing(t *testing.T) {
	input := `y, quo = pow(x, n)
    y = 1
    quo = y / x
    y = 0:n y * x`

	t.Run("parse function literal", func(t *testing.T) {
		l := lexer.New("TestFuncStatementParsing", input)
		cp := NewCodeParser(l)
		program := cp.Parse()
		require.Empty(t, cp.p.errors)

		// Verify function statement
		require.Len(t, program.Func.Statements, 1, "program should contain 1 function statement")
		fn := program.Func.Statements[0]

		t.Run("function metadata", func(t *testing.T) {
			require.Equal(t, "pow", fn.Token.Literal, "function name mismatch")
			require.Len(t, fn.Parameters, 2, "parameter count mismatch")
			require.Len(t, fn.Outputs, 2, "return value count mismatch")
		})

		t.Run("parameters", func(t *testing.T) {
			testIdentifier(t, fn.Parameters[0], "x")
			testIdentifier(t, fn.Parameters[1], "n")
		})

		t.Run("outputs", func(t *testing.T) {
			testIdentifier(t, fn.Outputs[0], "y")
			testIdentifier(t, fn.Outputs[1], "quo")
		})

		t.Run("body statements", func(t *testing.T) {
			require.Len(t, fn.Body.Statements, 3, "body statement count mismatch")

			t.Run("first assignment", func(t *testing.T) {
				stmt := fn.Body.Statements[0].(*ast.LetStatement)
				require.Len(t, stmt.Name, 1, "assignment target count")
				testIdentifier(t, stmt.Name[0], "y")
				testIntegerLiteral(t, stmt.Value[0], 1)
			})

			t.Run("second assignment", func(t *testing.T) {
				stmt := fn.Body.Statements[1].(*ast.LetStatement)
				require.Len(t, stmt.Name, 1, "assignment target count")
				testIdentifier(t, stmt.Name[0], "quo")
				testInfixExpression(t, stmt.Value[0], "y", "/", "x")
			})

			t.Run("conditional assignment", func(t *testing.T) {
				stmt := fn.Body.Statements[2].(*ast.LetStatement)
				require.Len(t, stmt.Name, 1, "assignment target count")
				testIdentifier(t, stmt.Name[0], "y")

				// Test condition
				require.Len(t, stmt.Condition, 1, "condition count")
				testInfixExpression(t, stmt.Condition[0], 0, ":", "n")

				// Test value
				require.Len(t, stmt.Value, 1, "value count")
				testInfixExpression(t, stmt.Value[0], "y", "*", "x")
			})
		})
	})
}

func TestFunctionParameterParsing(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "zero parameters",
			input:    `a = fn()\n    a = 4`,
			expected: []string{},
		},
		{
			name:     "single parameter",
			input:    `y = f(x)\n    y = x * x`,
			expected: []string{"x"},
		},
		{
			name:     "multiple parameters",
			input:    `r = f(x, y, z)\n    r = x + y + z`,
			expected: []string{"x", "y", "z"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestFunctionParameterParsing", strings.ReplaceAll(tt.input, `\n`, "\n"))
			cp := NewCodeParser(l)
			program := cp.Parse()

			// Validate parser errors first
			require.Empty(t, cp.p.errors, "parser should have no errors")

			// Check root statements
			require.NotEmpty(t, program.Func.Statements, "program should have function statements")

			stmt := program.Func.Statements[0]

			// Test parameter count
			require.Equal(t, len(tt.expected), len(stmt.Parameters),
				"parameter count mismatch")

			// Test individual parameters
			for i, expected := range tt.expected {
				t.Run(fmt.Sprintf("parameter_%d", i+1), func(t *testing.T) {
					require.GreaterOrEqual(t, len(stmt.Parameters), i+1,
						"insufficient parameters parsed")
					testIdentifier(t, stmt.Parameters[i], expected)
				})
			}
		})
	}
}
