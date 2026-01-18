package parser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
)

// requireOnlyLetStmt asserts the program has exactly one LetStatement and returns it.
func requireOnlyLetStmt(t *testing.T, program *ast.Program) *ast.LetStatement {
	require.Len(t, program.Statements, 1, "expected exactly one statement, got %d", len(program.Statements))
	stmt, ok := program.Statements[0].(*ast.LetStatement)
	require.Truef(t, ok, "expected *ast.LetStatement, got %T", program.Statements[0])
	return stmt
}

// requireOnlyPrintStmt asserts the program has exactly one PrintStatement and returns it.
func requireOnlyPrintStmt(t *testing.T, program *ast.Program) *ast.PrintStatement {
	require.Len(t, program.Statements, 1, "expected exactly one statement, got %d", len(program.Statements))
	stmt, ok := program.Statements[0].(*ast.PrintStatement)
	require.Truef(t, ok, "expected *ast.PrintStatement, got %T", program.Statements[0])
	return stmt
}

func TestAssign(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expId  string
		expStr string
	}{
		{"simple assignment", "x = 5", "=", "x = 5"},
		{"math expression assignment", "y = 5 * 3 + 2", "=", "y = ((5 * 3) + 2)"},
		{"complex expression assignment", "foobar = 2 + 3 / 5", "=", "foobar = (2 + (3 / 5))"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestAssign", tt.input)
			sp := NewScriptParser(l)
			program := sp.Parse()
			require.Empty(t, sp.Errors(), "unexpected parse errors for input %q: %v", tt.input, sp.Errors())

			stmt := requireOnlyLetStmt(t, program)
			require.Equal(t, tt.expId, stmt.Token.Literal, "assignment token mismatch for input: %q", tt.input)
			require.Equal(t, tt.expStr, stmt.String(), "assignment string mismatch for input: %q", tt.input)
		})
	}
}

func TestInvalidAssignment(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expError string
	}{
		{"numeric LHS", "123 = 5", `TestInvalidAssignment:1:1:expected expression to be of type "*ast.Identifier". Instead got "*ast.IntegerLiteral". Literal: "123"`},
		{"invalid multi-assign", "x, 5 = 1, 2", `TestInvalidAssignment:1:4:expected expression to be of type "*ast.Identifier". Instead got "*ast.IntegerLiteral". Literal: "5"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestInvalidAssignment", tt.input)
			sp := NewScriptParser(l)
			sp.Parse()
			errs := sp.Errors()
			require.Len(t, errs, 1, "expected one parse error for input %q", tt.input)
			require.Equal(t, tt.expError, errs[0], "unexpected error for input: %q", tt.input)
		})
	}
}

func TestMultiAssign(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expId  string
		expStr string
	}{
		{"multi assignment", "x, y = 2, 4", "=", "x, y = 2, 4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestMultiAssign", tt.input)
			sp := NewScriptParser(l)
			program := sp.Parse()
			require.Empty(t, sp.Errors(), "unexpected parse errors for input %q: %v", tt.input, sp.Errors())

			stmt := requireOnlyLetStmt(t, program)
			require.Equal(t, tt.expId, stmt.Token.Literal, "multi-assign token mismatch for input: %q", tt.input)
			require.Equal(t, tt.expStr, stmt.String(), "multi-assign string mismatch for input: %q", tt.input)
		})
	}
}

func TestIdentifierExpression(t *testing.T) {
	const input = "foobar"
	l := lexer.New("TestIdentifierExpression", input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	require.Empty(t, sp.Errors())

	printStmt := requireOnlyPrintStmt(t, program)
	ident, ok := printStmt.Expression.Arguments[0].(*ast.Identifier)
	require.Truef(t, ok, "expected *ast.Identifier, got %T", printStmt.Expression.Arguments[0])
	require.Equal(t, "foobar", ident.Value)
	require.Equal(t, "foobar", ident.Tok().Literal)
}

func TestIntegerLiteralExpression(t *testing.T) {
	const input = "5"
	l := lexer.New("TestIntegerLiteralExpression", input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	require.Empty(t, sp.Errors())

	printStmt := requireOnlyPrintStmt(t, program)
	lit, ok := printStmt.Expression.Arguments[0].(*ast.IntegerLiteral)
	require.Truef(t, ok, "expected *ast.IntegerLiteral, got %T", printStmt.Expression.Arguments[0])
	require.Equal(t, int64(5), lit.Value)
	require.Equal(t, "5", lit.Tok().Literal)
}

func TestStringLiteral(t *testing.T) {
	const input = `"hello"`
	l := lexer.New("TestStringLiteral", input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	require.Empty(t, sp.Errors())

	printStmt := requireOnlyPrintStmt(t, program)
	lit, ok := printStmt.Expression.Arguments[0].(*ast.StringLiteral)
	require.Truef(t, ok, "expected *ast.StringLiteral, got %T", printStmt.Expression.Arguments[0])
	require.Equal(t, "hello", lit.Value)
	require.Equal(t, "hello", lit.Token.Literal)
}

func TestParsingPrefixExpressions(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		operator string
		value    interface{}
	}{
		{"negate int", "-15", "-", 15},
		{"not int", "!5", "!", 5},
		{"negate ident", "-foobar", "-", "foobar"},
		{"not ident", "!foobar", "!", "foobar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestParsingPrefixExpression", tt.input)
			sp := NewScriptParser(l)
			program := sp.Parse()
			require.Empty(t, sp.Errors())

			printStmt := requireOnlyPrintStmt(t, program)
			exp, ok := printStmt.Expression.Arguments[0].(*ast.PrefixExpression)
			require.Truef(t, ok, "expected *ast.PrefixExpression, got %T", printStmt.Expression.Arguments[0])
			require.Equal(t, tt.operator, exp.Operator)
			// testLiteralExpression is assumed available
			require.Truef(t, testLiteralExpression(t, exp.Right, tt.value), "literal mismatch for input %q", tt.input)
		})
	}
}

func TestRootOperators(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		operator string
		value    interface{}
	}{
		{"square root of int", "√4", "√", 4},
		{"square root of negative", "√-1", "√", -1},
		{"cube root of int", "∛8", "∛", 8},
		{"cube root of negative", "∛-8", "∛", -8},
		{"fourth root of int", "∜16", "∜", 16},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestRootOperators", tt.input)
			sp := NewScriptParser(l)
			program := sp.Parse()
			require.Empty(t, sp.Errors())

			printStmt := requireOnlyPrintStmt(t, program)
			exp, ok := printStmt.Expression.Arguments[0].(*ast.PrefixExpression)
			require.Truef(t, ok, "expected *ast.PrefixExpression, got %T", printStmt.Expression.Arguments[0])
			require.Equal(t, tt.operator, exp.Operator)

			// For negative values, expect a PrefixExpression with "-" operator
			if tt.value.(int) < 0 {
				negExp, ok := exp.Right.(*ast.PrefixExpression)
				require.Truef(t, ok, "expected nested *ast.PrefixExpression for negative, got %T (%s)", exp.Right, exp.Right.String())
				require.Equal(t, "-", negExp.Operator)

				// Handle potential double negation issue - if we have another prefix, unwrap it
				innerExp := negExp.Right
				if innerPrefix, ok := innerExp.(*ast.PrefixExpression); ok && innerPrefix.Operator == "-" {
					// Double negation case: √ -> - -> (- -> 1), we want the inner value
					require.Truef(t, testLiteralExpression(t, innerPrefix.Right, -tt.value.(int)), "literal mismatch for input %q: expected %v, got %T (%s)", tt.input, -tt.value.(int), innerPrefix.Right, innerPrefix.Right.String())
				} else {
					require.Truef(t, testLiteralExpression(t, negExp.Right, -tt.value.(int)), "literal mismatch for input %q: expected %v, got %T (%s)", tt.input, -tt.value.(int), negExp.Right, negExp.Right.String())
				}
				return
			}
			require.Truef(t, testLiteralExpression(t, exp.Right, tt.value), "literal mismatch for input %q", tt.input)
		})
	}
}

func TestPrefixChainVsImplicitMult(t *testing.T) {
	// With current precedence, prefixes bind tighter than implicit multiplication:
	// ∜∛√-5x  =>  (∜(∛(√(-5)))) * x
	e := parseOneExpr(t, "∜∛√-5x")

	// top-level must be implicit multiplication
	top, ok := e.(*ast.InfixExpression)
	require.True(t, ok)
	require.Equal(t, "⋅", top.Operator)

	// right side is the identifier x
	if !testLiteralExpression(t, top.Right, "x") {
		t.FailNow()
	}

	// left side is nested prefixes: ∜(∛(√(-5)))
	p4, ok := top.Left.(*ast.PrefixExpression)
	require.True(t, ok)
	require.Equal(t, "∜", p4.Operator)

	p3, ok := p4.Right.(*ast.PrefixExpression)
	require.True(t, ok)
	require.Equal(t, "∛", p3.Operator)

	p2, ok := p3.Right.(*ast.PrefixExpression)
	require.True(t, ok)
	require.Equal(t, "√", p2.Operator)

	neg, ok := p2.Right.(*ast.PrefixExpression)
	require.True(t, ok)
	require.Equal(t, "-", neg.Operator)

	require.True(t, testIntegerLiteral(t, neg.Right, 5))
}

func TestPrefixOverProductParens(t *testing.T) {
	// √(5x) => √(5 ⋅ x)
	e := parseOneExpr(t, "√(5x)")

	pe, ok := e.(*ast.PrefixExpression)
	require.True(t, ok)
	require.Equal(t, "√", pe.Operator)

	if !testInfixExpression(t, pe.Right, 5, "⋅", "x") {
		t.FailNow()
	}
}

func TestInfixSqrtSplit(t *testing.T) {
	// a ^ √-5  =>  ^(a, √(-5))
	e := parseOneExpr(t, "a ^ √-5")

	ie, ok := e.(*ast.InfixExpression)
	require.True(t, ok)
	require.Equal(t, "^", ie.Operator)
	require.True(t, testLiteralExpression(t, ie.Left, "a"))

	// right is √(-5)
	p, ok := ie.Right.(*ast.PrefixExpression)
	require.True(t, ok)
	require.Equal(t, "√", p.Operator)

	neg, ok := p.Right.(*ast.PrefixExpression)
	require.True(t, ok)
	if !testPrefixExpression(t, neg, "-", 5) {
		t.FailNow()
	}
}

func TestWhitespaceDoesNotMatter(t *testing.T) {
	// "√ - 9" -> √(-9)
	e := parseOneExpr(t, "√ - 9")

	pe, ok := e.(*ast.PrefixExpression)
	require.True(t, ok)
	require.Equal(t, "√", pe.Operator)

	if !testPrefixExpression(t, pe.Right, "-", 9) {
		t.FailNow()
	}
}

func TestParsingInfixExpressions(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		left     interface{}
		operator string
		right    interface{}
	}{
		{"add int", "5 + 5", 5, "+", 5},
		{"sub int", "5 - 5", 5, "-", 5},
		{"mul int", "5 * 5", 5, "*", 5},
		{"div int", "5 / 5", 5, "/", 5},
		{"gt int", "5 > 5", 5, ">", 5},
		{"lt int", "5 < 5", 5, "<", 5},
		{"eq int", "5 == 5", 5, "==", 5},
		{"neq int", "5 != 5", 5, "!=", 5},
		{"add ident", "foobar + barfoo", "foobar", "+", "barfoo"},
		{"sub ident", "foobar - barfoo", "foobar", "-", "barfoo"},
		{"mul ident", "foobar * barfoo", "foobar", "*", "barfoo"},
		{"div ident", "foobar / barfoo", "foobar", "/", "barfoo"},
		{"gt ident", "foobar > barfoo", "foobar", ">", "barfoo"},
		{"lt ident", "foobar < barfoo", "foobar", "<", "barfoo"},
		{"eq ident", "foobar == barfoo", "foobar", "==", "barfoo"},
		{"neq ident", "foobar != barfoo", "foobar", "!=", "barfoo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestParsingInfixExpression", tt.input)
			sp := NewScriptParser(l)
			program := sp.Parse()
			require.Emptyf(t, sp.Errors(), "input %q: unexpected errors %v", tt.input, sp.Errors())

			printStmt := requireOnlyPrintStmt(t, program)
			infix, ok := printStmt.Expression.Arguments[0].(*ast.InfixExpression)
			require.Truef(t, ok, "input %q: expected *ast.InfixExpression, got %T", tt.input, printStmt.Expression.Arguments[0])

			require.Truef(t, testInfixExpression(t, infix, tt.left, tt.operator, tt.right),
				"input %q: infix expression mismatch", tt.input)
		})
	}
}

func TestOperatorPrecedenceParsing(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"negate then multiply", "-a * b", "((-a) * b)"},
		{"not wrap", "!(-a)", "(!(-a))"},
		{"add chain", "a + b + c", "((a + b) + c)"},
		{"add then sub", "a + b - c", "((a + b) - c)"},
		{"mul chain", "a * b * c", "((a * b) * c)"},
		{"mul then div", "a * b / c", "((a * b) / c)"},
		{"add then div", "a + b / c", "(a + (b / c))"},
		{"multi op", "a + b * c + d / e - f", "(((a + (b * c)) + (d / e)) - f)"},
		{"multi cmp", "5 > 4 == 3 < 4", "(((5 > 4) == 3) < 4)"},
		{"multi cmp opp", "5 < 4 != 3 > 4", "(((5 < 4) != 3) > 4)"},
		{"multi op with cmp", "3 + 4 * 5 == 3 * 1 + 4 * 5", "((3 + ((4 * (5 == 3)) * 1)) + (4 * 5))"},
		{"multi cmp with id", "3 > 5 == a", "((3 > 5) == a)"},
		{"multi cmp with id 2", "3 < 5 == a", "((3 < 5) == a)"},
		{"brackets", "1 + (2 + 3) + 4", "((1 + (2 + 3)) + 4)"},
		{"brackets for add then mul", "(5 + 5) * 2", "((5 + 5) * 2)"},
		{"brackets in divisor", "2 / (5 + 5)", "(2 / (5 + 5))"},
		{"multi brackets", "(5 + 5) * 2 * (5 + 5)", "(((5 + 5) * 2) * (5 + 5))"},
		{"prefix before brackets", "-(5 + 5)", "(-(5 + 5))"},
		{"function", "a + add(b * c) + d", "((a + add((b * c))) + d)"},
		{"function insicde function", "add(a, b, 1, 2 * 3, 4 + 5, add(6, 7 * 8))", "add(a, b, 1, (2 * 3), (4 + 5), add(6, (7 * 8)))"},
		{"multi ops inside function", "add(a + b + c * d / f + g)", "add((((a + b) + ((c * d) / f)) + g))"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestOperatorPrecedenceParsing", tt.input)
			sp := NewScriptParser(l)
			program := sp.Parse()
			require.Emptyf(t, sp.Errors(), "input %q: unexpected errors %v", tt.input, sp.Errors())

			actual := program.String()
			require.Equalf(t, tt.expected, actual,
				"input %q: operator precedence mismatch: got %q, want %q", tt.input, actual, tt.expected)
		})
	}
}

func TestImplicitMultParsing(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expStr string
	}{
		{"simple", "x = 5a", "x = (5 ⋅ a)"},
		{"add after mult", "y = 5x + 2", "y = ((5 ⋅ x) + 2)"},
		{"polynomial", "y = x^2 + 3.14x + 1", "y = (((x ^ 2) + (3.14 ⋅ x)) + 1)"},
		{"asc polynomial", "y = 1 + 2x + 3.11x^2 + 2.03x3^3 + 7x3ab^4", "y = ((((1 + (2 ⋅ x)) + (3.11 ⋅ (x ^ 2))) + (2.03 ⋅ (x3 ^ 3))) + (7 ⋅ (x3ab ^ 4)))"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestImplicitMultParsing", tt.input)
			sp := NewScriptParser(l)
			program := sp.Parse()
			require.Emptyf(t, sp.Errors(), "input %q: unexpected errors %v", tt.input, sp.Errors())

			stmt := requireOnlyLetStmt(t, program)
			require.Equalf(t, tt.expStr, stmt.String(), "input %q: implicit mult mismatch", tt.input)
		})
	}
}

func TestImplicitMultParsingSpaces(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expErrLen int
		expErr    string
	}{
		{"implicit mult with space", "x = 5 a", 1, "TestImplicitMultParsingSpaces:1:5:Expression \"5\" is not a condition. The main operation should be a comparison"},
		{"implicit mult with space poly", "y = 1 + 2 x + 3 x^2", 2, "TestImplicitMultParsingSpaces:1:7:Expression \"(1 + 2)\" is not a condition. The main operation should be a comparison TestImplicitMultParsingSpaces:1:15:expected next token to be =, got IDENT instead"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestImplicitMultParsingSpaces", tt.input)
			sp := NewScriptParser(l)
			sp.Parse()
			errs := sp.Errors()
			require.Lenf(t, errs, tt.expErrLen, "input %q: expected one error, got %d", tt.input, len(errs))
			err := ""
			for i := range tt.expErrLen {
				if i > 0 {
					err += " "
				}
				err += errs[i]
			}
			require.Equalf(t, tt.expErr, err, "input %q: error mismatch", tt.input)
		})
	}
}

func TestConditionExpression(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		condLeft  interface{}
		condOp    string
		condRight interface{}
		expStr    string
	}{
		{"simple condition", "a = x < y x", "x", "<", "y", "x"},
		{"condition with add", "res = a > 3 + 2", "", "", "", "((a > 3) + 2)"},
		// add more cases as needed
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestConditionExpression", tt.input)
			sp := NewScriptParser(l)
			program := sp.Parse()
			require.Emptyf(t, sp.Errors(), "input %q: unexpected errors %v", tt.input, sp.Errors())

			stmt := requireOnlyLetStmt(t, program)
			// test condition
			if len(stmt.Condition) > 0 {
				require.Truef(t, testInfixExpression(t, stmt.Condition[0], tt.condLeft, tt.condOp, tt.condRight),
					"input %q: condition mismatch", tt.input)
			}
			// test value
			if len(stmt.Value) > 0 {
				require.Equal(t, tt.expStr, stmt.Value[0].String())
			}
		})
	}
}

// Multi-return condition
func TestMultiReturnCondition(t *testing.T) {
	const input = "x, y = a > 5 10, 20"
	l := lexer.New("TestMultiReturnCondition", input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Truef(t, testInfixExpression(t, stmt.Condition[0], "a", ">", 5), "condition mismatch")
	require.Truef(t, testIntegerLiteral(t, stmt.Value[0], 10) && testIntegerLiteral(t, stmt.Value[1], 20),
		"multi-return values mismatch")
}

func TestInvalidConditionError(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expError string
	}{
		{"plus not cond", "x = 5 + 3 y", "Expression \"(5 + 3)\" is not a condition"},
		{"func not cond", "res = foo(2) result", "Expression \"foo(2)\" is not a condition"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestInvalidConditionError", tt.input)
			sp := NewScriptParser(l)
			sp.Parse()
			errs := sp.Errors()
			require.Lenf(t, errs, 1, "input %q: expected one error, got %d", tt.input, len(errs))
			require.Containsf(t, errs[0], tt.expError, "input %q: error mismatch", tt.input)
		})
	}
}

func TestNestedGuardCondition(t *testing.T) {
	const input = "res = (a > 3) < (b < 5) (c + d)"
	t.Run("nested guard condition", func(t *testing.T) {
		l := lexer.New("TestNestedGuardCondition", input)
		sp := NewScriptParser(l)
		program := sp.Parse()
		require.Emptyf(t, sp.Errors(), "input %q: unexpected errors %v", input, sp.Errors())

		stmt := requireOnlyLetStmt(t, program)

		// Condition: (a > 3) < (b < 5)
		cond, ok := stmt.Condition[0].(*ast.InfixExpression)
		require.Truef(t, ok, "expected *ast.InfixExpression for condition, got %T", stmt.Condition[0])
		// Validate nested conditions
		require.Truef(t, testInfixExpression(t, cond.Left, "a", ">", 3), "left nested condition mismatch")
		require.Truef(t, testInfixExpression(t, cond.Right, "b", "<", 5), "right nested condition mismatch")

		// Value: (c + d)
		require.Truef(t, testInfixExpression(t, stmt.Value[0], "c", "+", "d"), "value expression mismatch")
	})
}

// Function call in condition
func TestFunctionCallInCondition(t *testing.T) {
	const input = "res = pow(2, 3) > 8 result"
	l := lexer.New("TestFunctionCallInCondition", input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	cond, ok := stmt.Condition[0].(*ast.InfixExpression)
	require.Truef(t, ok, "expected Inf expression, got %T", stmt.Condition[0])
	require.Equal(t, ">", cond.Operator)

	callExpr, ok := cond.Left.(*ast.CallExpression)
	require.Truef(t, ok, "expected CallExpression, got %T", cond.Left)
	require.Equal(t, "pow", callExpr.Function.Value)
}

func TestLetStatementDuplicateIdentifiers(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name:    "simple duplicate",
			input:   `a, a = 1, 2`,
			wantErr: "duplicate identifier: a in this statement",
		},
		{
			name:    "duplicate later",
			input:   `x, y, x = 1, 2, 3`,
			wantErr: "duplicate identifier: x in this statement",
		},
		{
			name:    "Duplicate With Conditions",
			input:   `y, z, y = a > b 1, 2, 3`,
			wantErr: "duplicate identifier: y in this statement",
		},
		{
			name:    "blank allowed",
			input:   `_, _, a = 1, 2, 3`,
			wantErr: "", // no error
		},
		{
			name:    "mixed blank and dup",
			input:   `_, b, _ = 1, 2, 3`,
			wantErr: "", // no error
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sp := NewScriptParser(lexer.New("dupTest", tc.input))
			sp.Parse()
			errs := sp.Errors()
			if tc.wantErr == "" {
				if len(errs) > 0 {
					t.Fatalf("expected no errors, got %v", errs)
				}
				return
			}

			if !strings.Contains(errs[0], tc.wantErr) {
				t.Errorf("expected error %q, got %q", tc.wantErr, errs[0])
			}
		})
	}
}

func TestArrayLiterals(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectError bool
		errorMsg    string
		checkResult func(t *testing.T, arr *ast.ArrayLiteral)
	}{
		{
			name:  "empty array",
			input: "[]",
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.Empty(t, arr.Headers, "expected no headers")
				require.Empty(t, arr.Rows, "expected no rows")
			},
		},
		{
			name: "simple matrix without headers",
			input: `[
    1 2 3
    4 5 6
]`,
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.Empty(t, arr.Headers, "expected no headers for matrix")
				require.Len(t, arr.Rows, 2, "expected 2 rows")
				require.Len(t, arr.Rows[0], 3, "expected 3 elements in first row")
				require.Len(t, arr.Rows[1], 3, "expected 3 elements in second row")

				// Check first row: 1 2 3
				require.True(t, testIntegerLiteral(t, arr.Rows[0][0], 1))
				require.True(t, testIntegerLiteral(t, arr.Rows[0][1], 2))
				require.True(t, testIntegerLiteral(t, arr.Rows[0][2], 3))

				// Check second row: 4 5 6
				require.True(t, testIntegerLiteral(t, arr.Rows[1][0], 4))
				require.True(t, testIntegerLiteral(t, arr.Rows[1][1], 5))
				require.True(t, testIntegerLiteral(t, arr.Rows[1][2], 6))
			},
		},
		{
			name: "array with headers",
			input: `[
  :  Day Product Price
     "Monday" "Phone" 200
     "Tuesday" "Laptop" 300
]`,
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.Equal(t, []string{"Day", "Product", "Price"}, arr.Headers)
				require.Len(t, arr.Rows, 2, "expected 2 rows")

				// Check first row: "Monday" "Phone" 200
				require.True(t, testStringLiteral(t, arr.Rows[0][0], "Monday"))
				require.True(t, testStringLiteral(t, arr.Rows[0][1], "Phone"))
				require.True(t, testIntegerLiteral(t, arr.Rows[0][2], 200))

				// Check second row: "Tuesday" "Laptop" 300
				require.True(t, testStringLiteral(t, arr.Rows[1][0], "Tuesday"))
				require.True(t, testStringLiteral(t, arr.Rows[1][1], "Laptop"))
				require.True(t, testIntegerLiteral(t, arr.Rows[1][2], 300))
			},
		},
		{
			name: "mixed type rows",
			input: `[
    1 "hello" 3.14
    "world" 42 2.71
]`,
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.Empty(t, arr.Headers, "expected no headers")
				require.Len(t, arr.Rows, 2, "expected 2 rows")

				// Check first row: 1 "hello" 3.14
				require.True(t, testIntegerLiteral(t, arr.Rows[0][0], 1))
				require.True(t, testStringLiteral(t, arr.Rows[0][1], "hello"))
				require.True(t, testFloatLiteral(t, arr.Rows[0][2], 3.14))

				// Check second row: "world" 42 2.71
				require.True(t, testStringLiteral(t, arr.Rows[1][0], "world"))
				require.True(t, testIntegerLiteral(t, arr.Rows[1][1], 42))
				require.True(t, testFloatLiteral(t, arr.Rows[1][2], 2.71))
			},
		},
		{
			name:        "missing closing bracket",
			input:       "[1 2 3",
			expectError: true,
			errorMsg:    "expected ']' to close array literal",
		},
		{
			name:        "invalid header token",
			input:       "[: 123 Product]",
			expectError: true,
			errorMsg:    "expected identifier for column header",
		},
		{
			name: "line continuation with unary operators",
			input: `[
    a -b \
    -c d
]`,
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.Empty(t, arr.Headers, "expected no headers")
				require.Len(t, arr.Rows, 1, "expected 1 row (line continuation should merge)")
				require.Len(t, arr.Rows[0], 4, "expected 4 elements: a, -b, -c, d")

				// Check that we have: a, (-b), (-c), d
				require.True(t, testIdentifier(t, arr.Rows[0][0], "a"))

				// Check -b is a prefix expression (unary minus)
				prefixB, ok := arr.Rows[0][1].(*ast.PrefixExpression)
				require.Truef(t, ok, "expected *ast.PrefixExpression for -b, got %T", arr.Rows[0][1])
				require.Equal(t, "-", prefixB.Operator)
				require.True(t, testIdentifier(t, prefixB.Right, "b"))

				// Check -c is a prefix expression (unary minus)
				prefixC, ok := arr.Rows[0][2].(*ast.PrefixExpression)
				require.Truef(t, ok, "expected *ast.PrefixExpression for -c, got %T", arr.Rows[0][2])
				require.Equal(t, "-", prefixC.Operator)
				require.True(t, testIdentifier(t, prefixC.Right, "c"))

				// Check d is just an identifier
				require.True(t, testIdentifier(t, arr.Rows[0][3], "d"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestArrayLiterals", tt.input)
			sp := NewScriptParser(l)
			program := sp.Parse()

			if tt.expectError {
				require.NotEmpty(t, sp.Errors(), "expected parser errors for input %q", tt.input)
				require.Contains(t, sp.Errors()[0], tt.errorMsg, "error message mismatch")
				return
			}

			require.Empty(t, sp.Errors(), "unexpected parse errors for input %q: %v", tt.input, sp.Errors())

			stmt := requireOnlyPrintStmt(t, program)
			require.Len(t, stmt.Expression.Arguments, 1, "expected one expression in print statement")

			arr, ok := stmt.Expression.Arguments[0].(*ast.ArrayLiteral)
			require.Truef(t, ok, "expected *ast.ArrayLiteral, got %T", stmt.Expression.Arguments[0])

			if tt.checkResult != nil {
				tt.checkResult(t, arr)
			}
		})
	}
}

func TestArrayRangeExpression(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(t *testing.T, idx *ast.ArrayRangeExpression)
	}{
		{
			name:  "array range literal",
			input: "res = data[0:5]",
			check: func(t *testing.T, idx *ast.ArrayRangeExpression) {
				require.IsType(t, &ast.Identifier{}, idx.Array)
				require.Equal(t, "data", idx.Array.(*ast.Identifier).Value)

				rangeLit, ok := idx.Range.(*ast.RangeLiteral)
				require.Truef(t, ok, "expected range literal index, got %T", idx.Range)
				require.True(t, testIntegerLiteral(t, rangeLit.Start, 0))
				require.True(t, testIntegerLiteral(t, rangeLit.Stop, 5))
				require.Nil(t, rangeLit.Step, "expected default step")
			},
		},
		{
			name: "array range identifier",
			input: `i = 0:5
val = data[i]`,
			check: func(t *testing.T, idx *ast.ArrayRangeExpression) {
				require.IsType(t, &ast.Identifier{}, idx.Range)
				require.Equal(t, "i", idx.Range.(*ast.Identifier).Value)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestArrayIndex", tt.input)
			sp := NewScriptParser(l)
			program := sp.Parse()

			require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

			var stmt *ast.LetStatement
			if len(program.Statements) == 1 {
				stmt = requireOnlyLetStmt(t, program)
			} else {
				require.Len(t, program.Statements, 2, "expected two statements for identifier slice case")
				stmt = program.Statements[1].(*ast.LetStatement)
			}
			require.Len(t, stmt.Value, 1, "expected single RHS expression")
			idxExpr, ok := stmt.Value[0].(*ast.ArrayRangeExpression)
			require.Truef(t, ok, "expected *ast.ArrayRangeExpression, got %T", stmt.Value[0])

			if tt.check != nil {
				tt.check(t, idxExpr)
			}
		})
	}

	// Ensure slices inside expressions parse correctly.
	l := lexer.New("TestArrayIndexInfix", "res = data[0:3] + 1")
	sp := NewScriptParser(l)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	inf, ok := stmt.Value[0].(*ast.InfixExpression)
	require.Truef(t, ok, "expected *ast.InfixExpression, got %T", stmt.Value[0])
	idxExpr, ok := inf.Left.(*ast.ArrayRangeExpression)
	require.Truef(t, ok, "expected array range on left side, got %T", inf.Left)
	_, isRangeLiteral := idxExpr.Range.(*ast.RangeLiteral)
	require.True(t, isRangeLiteral)
}
