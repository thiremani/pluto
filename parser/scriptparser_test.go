package parser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/token"
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

func TestUnparsedAssignmentTarget(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expErrors []string
	}{
		{"spaced dot", "a .= 2", []string{
			"TestUnparsedAssignmentTarget:1:1:expected next token to be =, got . instead",
		}},
		{"operator run", "a @= 2", []string{
			"TestUnparsedAssignmentTarget:1:1:expected next token to be =, got OPERATOR instead",
		}},
		{"closing paren", "a ) = 2", []string{
			"TestUnparsedAssignmentTarget:1:1:expected next token to be =, got ) instead",
		}},
		{"only target", "@ = 2", []string{
			"TestUnparsedAssignmentTarget:1:1:no prefix parse function for @ found",
		}},
		{"second target", "a, @ = 2, 3", []string{
			"TestUnparsedAssignmentTarget:1:4:no prefix parse function for @ found",
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp := NewScriptParser(lexer.New("TestUnparsedAssignmentTarget", tt.input))
			require.NotPanics(t, func() { sp.Parse() }, "input %q", tt.input)
			require.Equal(t, tt.expErrors, sp.Errors(), "input %q", tt.input)
		})
	}
}

// A token that the lexer reports an error with stays in the stream when the
// parser reads it through its second token of lookahead.
func TestLookaheadKeepsTokenWithLexerError(t *testing.T) {
	sp := NewScriptParser(lexer.New("TestLookaheadKeepsTokenWithLexerError", `x = [a -"\q"]`))
	sp.Parse()
	require.Equal(t, []string{`TestLookaheadKeepsTokenWithLexerError:1:9:unsupported escape sequence \q`}, sp.Errors())
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

func TestNumericLiteralValues(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantInt *int64
		wantF64 *float64
	}{
		{name: "decimal separator", input: "1'000", wantInt: ptrInt64(1000)},
		{name: "hex", input: "0xffab", wantInt: ptrInt64(65451)},
		{name: "binary", input: "0b1011", wantInt: ptrInt64(11)},
		{name: "octal", input: "0o755", wantInt: ptrInt64(493)},
		{name: "float separator", input: "1'234.5'6", wantF64: ptrFloat64(1234.56)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestNumericLiteralValues", tt.input)
			sp := NewScriptParser(l)
			program := sp.Parse()
			require.Empty(t, sp.Errors())

			printStmt := requireOnlyPrintStmt(t, program)
			if tt.wantInt != nil {
				lit, ok := printStmt.Expression.Arguments[0].(*ast.IntegerLiteral)
				require.Truef(t, ok, "expected *ast.IntegerLiteral, got %T", printStmt.Expression.Arguments[0])
				require.Equal(t, *tt.wantInt, lit.Value)
				require.Equal(t, tt.input, lit.Tok().Literal)
				return
			}

			lit, ok := printStmt.Expression.Arguments[0].(*ast.FloatLiteral)
			require.Truef(t, ok, "expected *ast.FloatLiteral, got %T", printStmt.Expression.Arguments[0])
			require.Equal(t, *tt.wantF64, lit.Value)
			require.Equal(t, tt.input, lit.Tok().Literal)
		})
	}
}

func TestInvalidNumericLiteralValues(t *testing.T) {
	tests := []string{
		"0755",
		"0b01556",
		"0o89",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			l := lexer.New("TestInvalidNumericLiteralValues", input)
			sp := NewScriptParser(l)
			sp.Parse()
			require.NotEmpty(t, sp.Errors(), "expected parse error for %q", input)
			require.Contains(t, sp.Errors()[0], "could not parse")
		})
	}
}

func ptrInt64(v int64) *int64 {
	return &v
}

func ptrFloat64(v float64) *float64 {
	return &v
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
	require.Equal(t, "hello", lit.Token.Literal)
	require.Equal(t, input, lit.String())
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
		{"based hex", "y = 0x0abcx2", "y = (0x0abc ⋅ x2)"},
		{"uppercase hex prefix is implicit mult", "y = 0Xff", "y = (0 ⋅ Xff)"},
		{"uppercase binary prefix is implicit mult", "y = 0B10", "y = (0 ⋅ B10)"},
		{"uppercase octal prefix is implicit mult", "y = 0O7", "y = (0 ⋅ O7)"},
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
		{"implicit mult with space", "x = 5 a", 1, "TestImplicitMultParsingSpaces:1:5:Expression \"5\" is not a condition. Statement conditions must be comparisons or bare range/array-selection drivers"},
		{"implicit mult with space poly", "y = 1 + 2 x + 3 x^2", 1, "TestImplicitMultParsingSpaces:1:7:Expression \"(1 + 2)\" is not a condition. Statement conditions must be comparisons or bare range/array-selection drivers"},
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

func TestParenGroupParseErrors(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expErr string
	}{
		{"missing closing paren", "a = 5\nr = (a > 2 10", "expected next token to be )"},
		{"extra expression", "a = 5\nr = (a > 2 10 20)", "expected next token to be )"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestParenGroupParseErrors", tt.input)
			sp := NewScriptParser(l)
			sp.Parse()
			errs := sp.Errors()
			require.NotEmptyf(t, errs, "input %q: expected a parse error", tt.input)
			require.Containsf(t, errs[0], tt.expErr, "input %q: unexpected error %v", tt.input, errs)
		})
	}
}

func TestConditionThenArrayValue(t *testing.T) {
	const input = "x = a > b [1 2 3]"
	l := lexer.New("TestConditionThenArrayValue", input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Len(t, stmt.Condition, 1, "expected one condition")
	require.Truef(t, testInfixExpression(t, stmt.Condition[0], "a", ">", "b"), "condition mismatch")

	require.Len(t, stmt.Value, 1, "expected one value")
	arr, ok := stmt.Value[0].(*ast.ArrayLiteral)
	require.Truef(t, ok, "expected *ast.ArrayLiteral, got %T", stmt.Value[0])
	require.Len(t, arr.Rows, 1, "expected one row")
	require.Len(t, arr.Rows[0], 3, "expected three elements")
	require.Truef(t, testIntegerLiteral(t, arr.Rows[0][0], 1), "first element mismatch")
	require.Truef(t, testIntegerLiteral(t, arr.Rows[0][1], 2), "second element mismatch")
	require.Truef(t, testIntegerLiteral(t, arr.Rows[0][2], 3), "third element mismatch")
	require.Equal(t, "[1 2 3]", arr.String())
}

func TestConditionThenCallValue(t *testing.T) {
	const input = "x = a > b foo(1)"
	l := lexer.New("TestConditionThenCallValue", input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Len(t, stmt.Condition, 1, "expected one condition")
	require.Truef(t, testInfixExpression(t, stmt.Condition[0], "a", ">", "b"), "condition mismatch")

	require.Len(t, stmt.Value, 1, "expected one value")
	callExpr, ok := stmt.Value[0].(*ast.CallExpression)
	require.Truef(t, ok, "expected *ast.CallExpression, got %T", stmt.Value[0])
	require.Equal(t, "foo", callExpr.Function.Value)
	require.Len(t, callExpr.Arguments, 1, "expected one call argument")
	require.Truef(t, testIntegerLiteral(t, callExpr.Arguments[0], 1), "argument mismatch")
}

func TestCommaConditionRejected(t *testing.T) {
	// A statement condition is one expression; comma means positional lists
	// only. Conjunctions are spelled with &&.
	const input = "res = i < 2, j > 1  7"
	sp := NewScriptParser(lexer.New("TestCommaConditionRejected", input))
	sp.Parse()
	require.NotEmpty(t, sp.Errors(), "expected a parse error for a comma condition list")
	require.Contains(t, sp.Errors()[0], "a statement condition is a single expression; combine conditions with &&")
}

func TestConditionValueBoundaryAttachedPrefix(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		condCount int
		value     string
	}{
		{
			name:      "attached minus starts value",
			input:     "res = i < 2 -x",
			condCount: 1,
			value:     "(-x)",
		},
		{
			name:      "attached prefix range starts value",
			input:     "res = i < 2 -(0:3)",
			condCount: 1,
			value:     "(-0:3)",
		},
		{
			name:      "attached prefix hex literal starts value",
			input:     "res = i < 2 -0xff",
			condCount: 1,
			value:     "(-0xff)",
		},
		{
			name:      "attached plus starts value",
			input:     "res = i != 3 +10",
			condCount: 1,
			value:     "(+10)",
		},
		{
			// The condition's top-level && flattens into the condition list,
			// so each conjunct is validated separately and drivers still nest.
			name:      "and condition attached prefix starts value",
			input:     "res = i < 2 && j > 1 -x",
			condCount: 2,
			value:     "(-x)",
		},
		{
			name:      "chained comparison attached prefix starts value",
			input:     "res = a < b < c -d",
			condCount: 1,
			value:     "(-d)",
		},
		{
			name:      "spaced minus remains infix",
			input:     "res = i < 2 - x",
			condCount: 0,
			value:     "((i < 2) - x)",
		},
		{
			name:      "grouped comparison still splits on attached prefix",
			input:     "res = (i < 2) -x",
			condCount: 1,
			value:     "(-x)",
		},
		{
			name:      "grouped comparison still splits on attached plus",
			input:     "res = (i < 2) +10",
			condCount: 1,
			value:     "(+10)",
		},
		{
			name:      "grouped comparison stays a value with spaced operator",
			input:     "res = (i < 2) - x",
			condCount: 0,
			value:     "((i < 2) - x)",
		},
		{
			name:      "bare identifier attached prefix starts value",
			input:     "res = a -b",
			condCount: 1,
			value:     "(-b)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestConditionValueBoundaryAttachedPrefix", tt.input)
			sp := NewScriptParser(l)
			program := sp.Parse()
			require.Emptyf(t, sp.Errors(), "input %q: unexpected errors %v", tt.input, sp.Errors())

			stmt := requireOnlyLetStmt(t, program)
			require.Len(t, stmt.Condition, tt.condCount, "input %q: condition count mismatch", tt.input)
			require.Len(t, stmt.Value, 1, "input %q: expected one value", tt.input)
			require.Equal(t, tt.value, stmt.Value[0].String(), "input %q: value mismatch", tt.input)
		})
	}
}

func TestConditionValueBoundaryBareDriverArrayPrefix(t *testing.T) {
	l := lexer.New("TestConditionValueBoundaryBareDriverArrayPrefix", "res = i -[x]")
	sp := NewScriptParser(l)
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Len(t, stmt.Condition, 1, "expected bare driver condition")
	ident, ok := stmt.Condition[0].(*ast.Identifier)
	require.Truef(t, ok, "expected *ast.Identifier, got %T", stmt.Condition[0])
	require.Equal(t, "i", ident.Value)

	require.Len(t, stmt.Value, 1, "expected one value")
	prefix, ok := stmt.Value[0].(*ast.PrefixExpression)
	require.Truef(t, ok, "expected *ast.PrefixExpression, got %T", stmt.Value[0])
	require.Equal(t, "-", prefix.Operator)
	_, ok = prefix.Right.(*ast.ArrayLiteral)
	require.Truef(t, ok, "expected array literal RHS, got %T", prefix.Right)

	l = lexer.New("TestConditionValueBoundaryBareDriverArrayPrefix", "res = i - [x]")
	sp = NewScriptParser(l)
	program = sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt = requireOnlyLetStmt(t, program)
	require.Empty(t, stmt.Condition, "spaced operator should stay in value position")
	require.Len(t, stmt.Value, 1, "expected one value")
	infix, ok := stmt.Value[0].(*ast.InfixExpression)
	require.Truef(t, ok, "expected *ast.InfixExpression, got %T", stmt.Value[0])
	require.Equal(t, "-", infix.Operator)
}

func TestBareRangeDriverCondition(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		condCheck func(*testing.T, ast.Expression)
	}{
		{
			name:  "identifier driver",
			input: "xs = i [1]",
			condCheck: func(t *testing.T, expr ast.Expression) {
				ident, ok := expr.(*ast.Identifier)
				require.Truef(t, ok, "expected *ast.Identifier, got %T", expr)
				require.Equal(t, "i", ident.Value)
			},
		},
		{
			name:  "range literal driver",
			input: "xs = 0:3 [1]",
			condCheck: func(t *testing.T, expr ast.Expression) {
				rl, ok := expr.(*ast.RangeLiteral)
				require.Truef(t, ok, "expected *ast.RangeLiteral, got %T", expr)
				require.Truef(t, testIntegerLiteral(t, rl.Start, 0), "range start mismatch")
				require.Truef(t, testIntegerLiteral(t, rl.Stop, 3), "range stop mismatch")
			},
		},
		{
			name:  "array range driver",
			input: "xs = arr[0:3] [1]",
			condCheck: func(t *testing.T, expr ast.Expression) {
				ar, ok := expr.(*ast.ArrayRangeExpression)
				require.Truef(t, ok, "expected *ast.ArrayRangeExpression, got %T", expr)
				ident, ok := ar.Array.(*ast.Identifier)
				require.Truef(t, ok, "expected array identifier, got %T", ar.Array)
				require.Equal(t, "arr", ident.Value)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New("TestBareRangeDriverCondition", tt.input)
			sp := NewScriptParser(l)
			program := sp.Parse()
			require.Emptyf(t, sp.Errors(), "input %q: unexpected errors %v", tt.input, sp.Errors())

			stmt := requireOnlyLetStmt(t, program)
			require.Len(t, stmt.Condition, 1, "expected one condition")
			tt.condCheck(t, stmt.Condition[0])

			require.Len(t, stmt.Value, 1, "expected one value")
			_, ok := stmt.Value[0].(*ast.ArrayLiteral)
			require.Truef(t, ok, "expected array literal value, got %T", stmt.Value[0])
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

func TestLogicalOrCondition(t *testing.T) {
	const input = "res = a > 3 || b < 5 value"
	l := lexer.New("TestLogicalOrCondition", input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Len(t, stmt.Condition, 1, "expected one condition")
	cond, ok := stmt.Condition[0].(*ast.InfixExpression)
	require.Truef(t, ok, "expected *ast.InfixExpression, got %T", stmt.Condition[0])
	require.Equal(t, "||", cond.Operator)
	require.Truef(t, testInfixExpression(t, cond.Left, "a", ">", 3), "left condition mismatch")
	require.Truef(t, testInfixExpression(t, cond.Right, "b", "<", 5), "right condition mismatch")

	require.Len(t, stmt.Value, 1, "expected one value")
	require.True(t, testIdentifier(t, stmt.Value[0], "value"))
}

func TestLogicalOrConditionValueBoundary(t *testing.T) {
	const input = "res = a > 5 || b > 6 c > 0 || d"
	l := lexer.New("TestLogicalOrConditionValueBoundary", input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Len(t, stmt.Condition, 1, "expected one condition")
	cond, ok := stmt.Condition[0].(*ast.InfixExpression)
	require.Truef(t, ok, "expected *ast.InfixExpression, got %T", stmt.Condition[0])
	require.Equal(t, "||", cond.Operator)
	require.Truef(t, testInfixExpression(t, cond.Left, "a", ">", 5), "left condition mismatch")
	require.Truef(t, testInfixExpression(t, cond.Right, "b", ">", 6), "right condition mismatch")

	require.Len(t, stmt.Value, 1, "expected one value")
	value, ok := stmt.Value[0].(*ast.InfixExpression)
	require.Truef(t, ok, "expected *ast.InfixExpression, got %T", stmt.Value[0])
	require.Equal(t, "||", value.Operator)
	require.Truef(t, testInfixExpression(t, value.Left, "c", ">", 0), "left value mismatch")
	require.Truef(t, testIdentifier(t, value.Right, "d"), "right value mismatch")
}

func TestLogicalOrValueExpression(t *testing.T) {
	const input = "res = a > 3 || b < 5"
	l := lexer.New("TestLogicalOrValueExpression", input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Empty(t, stmt.Condition)
	require.Len(t, stmt.Value, 1, "expected one value")
	require.Equal(t, "((a > 3) || (b < 5))", stmt.Value[0].String())
}

func TestLogicalOrDoesNotConsumeBitwiseOr(t *testing.T) {
	const input = "res = 6 | 3"
	l := lexer.New("TestLogicalOrDoesNotConsumeBitwiseOr", input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Empty(t, stmt.Condition)
	require.Len(t, stmt.Value, 1, "expected one value")
	require.Truef(t, testInfixExpression(t, stmt.Value[0], 6, "|", 3), "bitwise OR value mismatch")
}

func TestLogicalAndValueExpression(t *testing.T) {
	const input = "x = a > 2 && 10"
	sp := NewScriptParser(lexer.New("TestLogicalAndValueExpression", input))
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Empty(t, stmt.Condition)
	require.Len(t, stmt.Value, 1, "expected one value")
	and, ok := stmt.Value[0].(*ast.InfixExpression)
	require.Truef(t, ok, "expected *ast.InfixExpression, got %T", stmt.Value[0])
	require.Equal(t, "&&", and.Operator)
	require.Truef(t, testInfixExpression(t, and.Left, "a", ">", 2), "left mismatch")
	require.Truef(t, testIntegerLiteral(t, and.Right, 10), "right mismatch")
}

func TestLogicalAndChainLeftAssociative(t *testing.T) {
	// a > 2 && b > 3 && 10 parses as ((a > 2 && b > 3) && 10): last one wins
	// when all hold.
	const input = "x = a > 2 && b > 3 && 10"
	sp := NewScriptParser(lexer.New("TestLogicalAndChainLeftAssociative", input))
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Len(t, stmt.Value, 1, "expected one value")
	outer, ok := stmt.Value[0].(*ast.InfixExpression)
	require.Truef(t, ok, "expected *ast.InfixExpression, got %T", stmt.Value[0])
	require.Equal(t, "&&", outer.Operator)
	require.Truef(t, testIntegerLiteral(t, outer.Right, 10), "outer right mismatch")
	inner, ok := outer.Left.(*ast.InfixExpression)
	require.Truef(t, ok, "expected inner infix, got %T", outer.Left)
	require.Equal(t, "&&", inner.Operator)
	require.Truef(t, testInfixExpression(t, inner.Left, "a", ">", 2), "inner left mismatch")
	require.Truef(t, testInfixExpression(t, inner.Right, "b", ">", 3), "inner right mismatch")
}

func TestLogicalAndBindsTighterThanOr(t *testing.T) {
	// c && v || w parses as (c && v) || w: an if-else, since && binds tighter.
	const input = "x = a > 2 && 10 || 7"
	sp := NewScriptParser(lexer.New("TestLogicalAndBindsTighterThanOr", input))
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Len(t, stmt.Value, 1, "expected one value")
	or, ok := stmt.Value[0].(*ast.InfixExpression)
	require.Truef(t, ok, "expected *ast.InfixExpression, got %T", stmt.Value[0])
	require.Equal(t, "||", or.Operator)
	and, ok := or.Left.(*ast.InfixExpression)
	require.Truef(t, ok, "expected && on the left of ||, got %T", or.Left)
	require.Equal(t, "&&", and.Operator)
	require.Truef(t, testInfixExpression(t, and.Left, "a", ">", 2), "cond mismatch")
	require.Truef(t, testIntegerLiteral(t, and.Right, 10), "value mismatch")
	require.Truef(t, testIntegerLiteral(t, or.Right, 7), "fallback mismatch")
}

func TestLogicalAndDoesNotConsumeBitwiseAnd(t *testing.T) {
	// A single & stays bitwise AND.
	const input = "x = 6 & 3"
	sp := NewScriptParser(lexer.New("TestLogicalAndDoesNotConsumeBitwiseAnd", input))
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Empty(t, stmt.Condition)
	require.Len(t, stmt.Value, 1, "expected one value")
	require.Truef(t, testInfixExpression(t, stmt.Value[0], 6, "&", 3), "bitwise AND value mismatch")
}

func TestLogicalAndInArrayCell(t *testing.T) {
	const input = "x = [1  a > 2 && 5  3]"
	sp := NewScriptParser(lexer.New("TestLogicalAndInArrayCell", input))
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Len(t, stmt.Value, 1, "expected one value")
	arr, ok := stmt.Value[0].(*ast.ArrayLiteral)
	require.Truef(t, ok, "expected *ast.ArrayLiteral, got %T", stmt.Value[0])
	require.Len(t, arr.Rows, 1)
	require.Len(t, arr.Rows[0], 3, "expected three cells")
	and, ok := arr.Rows[0][1].(*ast.InfixExpression)
	require.Truef(t, ok, "expected && cell, got %T", arr.Rows[0][1])
	require.Equal(t, "&&", and.Operator)
	require.Truef(t, testInfixExpression(t, and.Left, "a", ">", 2), "cell cond mismatch")
	require.Truef(t, testIntegerLiteral(t, and.Right, 5), "cell value mismatch")
}

func TestParenComparisonStaysPlain(t *testing.T) {
	// Parentheses preserve a plain value-position comparison.
	const input = "x = (a > 2)"
	sp := NewScriptParser(lexer.New("TestParenComparisonStaysPlain", input))
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Len(t, stmt.Value, 1, "expected one value")
	require.Truef(t, testInfixExpression(t, stmt.Value[0], "a", ">", 2), "expected plain comparison")
}

func TestParenGroupingUnaffected(t *testing.T) {
	// `(x + 1)` is not a condition, so it stays ordinary grouping.
	const input = "y = (x + 1)"
	sp := NewScriptParser(lexer.New("TestParenGroupingUnaffected", input))
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Len(t, stmt.Value, 1, "expected one value")
	require.Truef(t, testInfixExpression(t, stmt.Value[0], "x", "+", 1), "expected plain grouped expr")
}

func TestParenGroupedIdentifierThenInfix(t *testing.T) {
	// `(a) + 1` is plain grouping followed by infix: `a + 1`.
	const input = "x = (a) + 1"
	sp := NewScriptParser(lexer.New("TestParenGroupedIdentifierThenInfix", input))
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Len(t, stmt.Value, 1, "expected one value")
	require.Truef(t, testInfixExpression(t, stmt.Value[0], "a", "+", 1), "expected plain grouped expr a + 1")
}

func TestParenAttachedPrefixIsPlainGrouping(t *testing.T) {
	// Parens are pure grouping: `(a > 2 -b)` parses its single sub-expression
	// without the statement level's condition split, so `-b` continues as
	// subtraction. Comparison binds tightest, so the group is `(a > 2) - b`.
	const input = "x = (a > 2 -b)"
	sp := NewScriptParser(lexer.New("TestParenAttachedPrefixIsPlainGrouping", input))
	program := sp.Parse()
	require.Emptyf(t, sp.Errors(), "unexpected errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Len(t, stmt.Value, 1, "expected one value")
	sub, ok := stmt.Value[0].(*ast.InfixExpression)
	require.Truef(t, ok, "expected *ast.InfixExpression, got %T", stmt.Value[0])
	require.Equal(t, "-", sub.Operator)
	require.Truef(t, testInfixExpression(t, sub.Left, "a", ">", 2), "left mismatch")
	require.Truef(t, testIdentifier(t, sub.Right, "b"), "right mismatch")

	// Spaced subtraction is unaffected.
	sp2 := NewScriptParser(lexer.New("paren-sub", "y = (a - b)"))
	prog2 := sp2.Parse()
	require.Empty(t, sp2.Errors())
	st2 := requireOnlyLetStmt(t, prog2)
	require.Truef(t, testInfixExpression(t, st2.Value[0], "a", "-", "b"), "spaced subtraction should be plain")
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

// An element type after a literal with cells is read with the literal and
// reported once; the assignment on the next line is kept.
func TestElementTypeAfterCellsKeepsNextStatement(t *testing.T) {
	sp := NewScriptParser(lexer.New(t.Name(), "x = [1 2]0\ny = 3"))
	program := sp.Parse()

	require.Equal(t, []string{
		t.Name() + ":1:10:an element type is only written on an empty array; cells give a literal its type, as in [1.0 2 3]",
	}, sp.Errors())
	require.Len(t, program.Statements, 2)
	require.Equal(t, "x = [1 2]", program.Statements[0].String())
	require.Equal(t, "y = 3", program.Statements[1].String())
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
			name:  "empty integer array",
			input: "[]0",
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.Empty(t, arr.Headers, "expected no headers")
				require.Empty(t, arr.Rows, "expected no rows")
				require.False(t, arr.Block)
				require.True(t, testIntegerLiteral(t, arr.Sample, 0))
				require.Equal(t, "[]0", arr.String())
			},
		},
		{
			name:  "empty float array",
			input: "[]0.0",
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.IsType(t, &ast.FloatLiteral{}, arr.Sample)
				require.Equal(t, "[]0.0", arr.String())
			},
		},
		{
			name:  "empty string array",
			input: `[]""`,
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.IsType(t, &ast.StringLiteral{}, arr.Sample)
				require.Equal(t, `[]""`, arr.String())
			},
		},
		{
			name:  "empty array typed by a variable",
			input: "[]plane",
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.True(t, testIdentifier(t, arr.Sample, "plane"))
				require.Equal(t, "[]plane", arr.String())
			},
		},
		{
			name:  "empty block array",
			input: "[\n]0",
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.True(t, arr.Block)
				require.Empty(t, arr.Rows)
				require.True(t, testIntegerLiteral(t, arr.Sample, 0))
				require.Equal(t, "[\n]0", arr.String())
			},
		},
		{
			name:  "table with column types",
			input: "[\n  : Name(\"\") Score(0)\n]",
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.Equal(t, []string{"Name", "Score"}, arr.Headers)
				require.Empty(t, arr.Rows)
				require.Nil(t, arr.Sample)
				require.Len(t, arr.ColumnTypes, 2)
				require.IsType(t, &ast.StringLiteral{}, arr.ColumnTypes[0])
				require.True(t, testIntegerLiteral(t, arr.ColumnTypes[1], 0))
				require.Equal(t, "[\n  : Name(\"\") Score(0)\n]", arr.String())
			},
		},
		{
			name:        "untyped empty array",
			input:       "[]",
			expectError: true,
			errorMsg:    "an empty array needs its element type",
		},
		{
			name:        "detached sample",
			input:       "[] 0",
			expectError: true,
			errorMsg:    "an empty array needs its element type",
		},
		{
			name:        "untyped empty block",
			input:       "[\n]",
			expectError: true,
			errorMsg:    "an empty array needs its element type",
		},
		{
			name:        "nonzero integer sample",
			input:       "[]5",
			expectError: true,
			errorMsg:    "written as a zero value",
		},
		{
			name:        "nonzero float sample",
			input:       "[]2.3",
			expectError: true,
			errorMsg:    "written as a zero value",
		},
		{
			name:        "float sample without leading zero",
			input:       "[].0",
			expectError: true,
			errorMsg:    "written as a zero value",
		},
		{
			name:        "call as sample",
			input:       "[]F32(0.0)",
			expectError: true,
			errorMsg:    "the sample after [] is a zero value or a variable name",
		},
		{
			name:        "header-only table without column types",
			input:       "[\n  : Name Score\n]",
			expectError: true,
			errorMsg:    "a table without rows needs a type on every column",
		},
		{
			name:        "missing column type",
			input:       "[\n  : Name(\"\") Score\n]",
			expectError: true,
			errorMsg:    "a table without rows needs a type on every column",
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.Len(t, arr.ColumnTypes, 2)
				require.Nil(t, arr.ColumnTypes[1])
				require.Equal(t, "[\n  : Name(\"\") Score\n]", arr.String())
			},
		},
		{
			name:        "nonzero column type",
			input:       "[\n  : Name(\"\") Score(1)\n]",
			expectError: true,
			errorMsg:    "a column's type is written as a zero value: Score(0)",
		},
		{
			name:        "array-valued column type",
			input:       "[\n  : Name(\"\") Scores([]0.0)\n]",
			expectError: true,
			errorMsg:    "a column's type is written as a zero value: Scores(0)",
		},
		{
			name:        "detached column type",
			input:       "[\n  : Name (\"\") Score(0)\n]",
			expectError: true,
			errorMsg:    "a column type attaches to its name: Name(0)",
		},
		{
			name:        "column types with data rows",
			input:       "[\n  : Name(\"\") Score(0)\n    \"Ada\" 10\n]",
			expectError: true,
			errorMsg:    "column types are only written on a table without rows",
		},
		{
			name:        "element type after cells",
			input:       "[1 2 3]0.0",
			expectError: true,
			errorMsg:    "1:8:an element type is only written on an empty array; cells give a literal its type, as in [1.0 2 3]",
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.Nil(t, arr.Sample)
				require.Equal(t, "[1 2 3]", arr.String())
			},
		},
		{
			name:        "element type after a block",
			input:       "[\n    1 2\n]0",
			expectError: true,
			errorMsg:    "an element type is only written on an empty array",
		},
		{
			name:        "element type after a table",
			input:       "[\n  : Name Score\n    \"Ada\" 10\n]0",
			expectError: true,
			errorMsg:    "an element type is only written on an empty array",
		},
		{
			name:        "element type after a table without rows",
			input:       "[\n  : Name(\"\") Score(0)\n]0",
			expectError: true,
			errorMsg:    "3:2:an element type is only written on an empty array",
		},
		{
			// The attached value belongs to the inner literal, not the row.
			name:        "element type after a cell",
			input:       "[[1 2]0]",
			expectError: true,
			errorMsg:    "an element type is only written on an empty array",
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.Len(t, arr.Rows, 1)
				require.Len(t, arr.Rows[0], 1)
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
				require.True(t, arr.Block)
				require.Len(t, arr.Rows, 2, "expected 2 rows")
				require.Len(t, arr.Rows[0], 3, "expected 3 elements in first row")
				require.Len(t, arr.Rows[1], 3, "expected 3 elements in second row")
				require.Equal(t, "[\n    1 2 3\n    4 5 6\n]", arr.String())

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
			name: "one-row block array",
			input: `[
    1 2 3
]`,
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.True(t, arr.Block)
				require.Len(t, arr.Rows, 1)
				require.Equal(t, "[\n    1 2 3\n]", arr.String())
			},
		},
		{
			name: "array with headers",
			input: `[
  : Day Product Price
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

				require.Equal(t, "[\n  : Day Product Price\n    \"Monday\" \"Phone\" 200\n    \"Tuesday\" \"Laptop\" 300\n]", arr.String())
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
			name:        "inline literal across lines",
			input:       "[1 2\n3 4]",
			expectError: true,
			errorMsg:    inlineArrayErr,
		},
		{
			name:        "block literal rows not indented",
			input:       "[\n1 2\n]",
			expectError: true,
			errorMsg:    blockRowsErr,
		},
		{
			name:        "invalid header token",
			input:       "[\n  : 123 Product\n]",
			expectError: true,
			errorMsg:    "expected identifier for column header",
		},
		{
			name:        "header marker without columns",
			input:       "[\n  :\n]",
			expectError: true,
			errorMsg:    "expected at least one column header after ':'",
		},
		{
			name:        "header on the bracket's line",
			input:       "[ : Name(\"\") Score(0) ]",
			expectError: true,
			errorMsg:    "a table's header goes on its own line after '['",
		},
		{
			name:  "unary operators start cells",
			input: "[a -b -c d]",
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.Empty(t, arr.Headers, "expected no headers")
				require.False(t, arr.Block)
				require.Len(t, arr.Rows, 1, "expected 1 row")
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
		{
			name:  "block literal",
			input: "[\n    1 2\n    3 4\n]",
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.True(t, arr.Block)
				require.Len(t, arr.Rows, 2)
				require.Equal(t, "[\n    1 2\n    3 4\n]", arr.String())
			},
		},
		{
			name:  "attached unary plus",
			input: `[a +b]`,
			checkResult: func(t *testing.T, arr *ast.ArrayLiteral) {
				require.Empty(t, arr.Headers, "expected no headers")
				require.Len(t, arr.Rows, 1, "expected one row")
				require.Len(t, arr.Rows[0], 2, "expected two elements: a, +b")
				require.True(t, testIdentifier(t, arr.Rows[0][0], "a"))

				prefixB, ok := arr.Rows[0][1].(*ast.PrefixExpression)
				require.Truef(t, ok, "expected *ast.PrefixExpression for +b, got %T", arr.Rows[0][1])
				require.Equal(t, "+", prefixB.Operator)
				require.True(t, testIdentifier(t, prefixB.Right, "b"))
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
				if tt.checkResult == nil {
					return
				}
			} else {
				require.Empty(t, sp.Errors(), "unexpected parse errors for input %q: %v", tt.input, sp.Errors())
			}

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

// A literal or a parenthesized list laid out against the rules is one error,
// and the statements after it keep their structure.
func TestLayoutKeepsStatements(t *testing.T) {
	const name = "TestLayoutKeepsStatements:"
	for _, tt := range []struct {
		name      string
		input     string
		expErrors []string
	}{
		{"nested block literals", "x = [\n    [\n        1 2\n    ]\n]", nil},
		{"block literal", "m = [\n    1 2\n    3 4\n]", nil},
		{"unclosed block literal", "x = [\n    1 2", []string{name + "3:1:" + blockCloseErr}},
		{"unclosed block table", "t = [\n  : Name(\"\") Score(0)", []string{name + "3:1:" + blockCloseErr}},
		{"inline literal across lines", "x = [1 2\n    3 4]", []string{name + "1:9:" + inlineArrayErr}},
		{"inline literal across lines, 5 spaces in", "x = [1 2\n     3 4]", []string{name + "1:9:" + inlineArrayErr}},
		{"line indented past its block", "x = 1\n    y = 2", []string{name + "2:5:" + strayIndentErr}},
		{"line indented 2 spaces past its block", "x = 1\n  y = 2", []string{name + "2:3:" + strayIndentErr}},
		{"row indented past its rows", "m = [\n    1 2\n      3 4\n]", []string{name + "3:7:" + strayIndentErr}},
		{"block literal's ']' after a stray line's unclosed literal", "x = [\n    1\n        [\n            2\n]", []string{name + "3:9:" + strayIndentErr, name + "5:1:" + blockCloseErr, name + "6:1:" + blockCloseErr}},
		{"block literal closed on its last row", "m = [\n    1 2\n    3 4]", []string{name + "3:8:" + blockCloseErr}},
		{"block literal closed at its rows' indentation", "m = [\n    1 2\n    ]", []string{name + "3:5:" + blockCloseErr}},
		{"unclosed call", "x = f(1", []string{name + "1:8:" + lineBreakErr}},
		{"call broken before a 2-space continuation", "x = f(x\n  y,\n  z)", []string{name + "1:8:" + lineBreakErr}},
		{"grouped expression broken after an operator", "x = (1 +\n    2)", []string{name + "1:9:" + lineBreakErr}},
		{"grouped expression broken after an operator, 5 spaces in", "x = (1 +\n     2)", []string{name + "1:9:" + lineBreakErr}},
		{"operator at the end of a line", "x = 1 +\n    2", []string{name + "1:8:" + lineBreakErr}},
		{"operator before an unindented line", "x = 1 +", []string{name + "1:8:" + lineBreakErr}},
		{"prefix operator at the end of a line", "x = -\n    2", []string{name + "1:6:" + lineBreakErr}},
		{"operator at the end of a block literal's row", "m = [\n    1 +\n        2\n]", []string{name + "2:8:" + lineBreakErr}},
		{"first argument on the next line", "x = f(\n    x, y)", []string{name + "1:7:" + lineBreakErr}},
		{"closing parenthesis on its own line", "x = f(x,\n    y\n)", []string{name + "2:6:" + lineBreakErr}},
		{"grouped expression across lines", "x = (1\n    + 2)", []string{name + "1:7:" + lineBreakErr}},
		{"call broken in a block literal's row", "x = [\n    f(1", []string{name + "2:8:" + lineBreakErr, name + "3:1:" + blockCloseErr}},
		{"call broken in a row, with its line continued", "x = [\n    f(1\n        2)\n    3 4\n]", []string{name + "2:8:" + lineBreakErr}},
		{"call broken in an inline literal", "x = [1 f(2", []string{name + "1:11:" + lineBreakErr}},
		{"block literal before an unindented line", "x = [", []string{name + "2:1:" + blockRowsErr}},
		{"block literal argument before an unindented line", "x = f([", []string{name + "2:1:" + blockRowsErr}},
		{"block literal's ']' between its '[' line and its rows", "m = [\n    1 2\n  ]", []string{name + "3:3:" + blockCloseErr}},
		{"inner literal's ']' at the outer literal's column", "x = [\n    [\n        1 2\n]\n]", []string{name + "4:1:" + blockCloseErr}},
		{"empty inner literal's ']' at the outer literal's column", "x = [\n    [\n]0\n]", []string{name + "3:1:" + blockCloseErr}},
		{"inner literal closed on its last row", "x = [\n    [\n        1 2]\n]", []string{name + "3:12:" + blockCloseErr, name + "4:1:" + blockCloseErr, name + "4:1:" + blockCloseErr}},
		{"table header with only a comment", "t = [\n  :# note\n    1 2\n]", []string{name + "2:3:expected at least one column header after ':'"}},
		{"line that starts with a comma after a broken call", "x = f(1 +\n, 2)", []string{name + "1:10:" + lineBreakErr, name + "2:1:no prefix parse function for , found"}},
		{"line that starts with a comma after a broken value", "x = 1 +\n, 2", []string{name + "1:8:" + lineBreakErr, name + "2:1:no prefix parse function for , found"}},
		{"table header that fails", "t = [\n  : a 1 )\n    1 2\n]", []string{name + "2:7:expected identifier for column header, got INT"}},
		{"table header without a space after its ':'", "t = [\n  :a b\n    1 2\n]", []string{name + "2:3:" + lexer.HEADER_COLON_ERR}},
		{"table header with two spaces after its ':'", "t = [\n  :  a b\n    1 2\n]", []string{name + "2:3:" + lexer.HEADER_COLON_ERR}},
		{"table header with a tab after its ':'", "t = [\n  :\ta b\n    1 2\n]", []string{name + "2:3:" + lexer.HEADER_COLON_ERR}},
		{"NUL in a comment inside a literal", "x = [\n    1 2\n    # a\x00b\n    3 4\n]", []string{name + "3:8:NUL character is not allowed in source"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sp := NewScriptParser(lexer.New("TestLayoutKeepsStatements", tt.input+"\nafter = 7\nafter"))
			program := sp.Parse()
			require.Equal(t, tt.expErrors, sp.Errors())
			stmts := program.Statements
			require.GreaterOrEqual(t, len(stmts), 2)
			for _, stmt := range stmts {
				require.NotNil(t, stmt) // a statement that failed leaves no node
			}
			require.Equal(t, "after = 7", stmts[len(stmts)-2].String())
			require.Equal(t, "after", stmts[len(stmts)-1].String())
		})
	}
}

// A block literal prints in its own layout, so a literal nested in a row,
// directly or inside a call, prints at that row's indentation, and a
// multi-line string in a row keeps its text.
func TestBlockLiteralPrintsItsLayout(t *testing.T) {
	for _, input := range []string{
		"m = [\n    [\n        1 2\n    ]\n]",
		"m = [\n    f([\n        1 2\n    ])\n]",
		"t = [\n    [\n      : a b\n        1 2\n    ]\n]",
		"m = [\n    \"a\nb\" 1\n]",
		"m = [\n    \"a\\\"\nb\" 1\n]",
	} {
		sp := NewScriptParser(lexer.New("TestBlockLiteralPrintsItsLayout", input))
		program := sp.Parse()
		require.Empty(t, sp.Errors(), input)
		require.Len(t, program.Statements, 1, input)
		require.Equal(t, input, program.Statements[0].String())
	}
}

// An assignment's value starts on the same line as its '=', even when a
// comment follows the '=' or the value is a bracket. The statement after it
// still parses.
func TestValueStartsOnAssignmentLine(t *testing.T) {
	const msg = "TestValueStartsOnAssignmentLine:1:3:an assignment's value starts on the same line as its '='"
	for _, tt := range []struct {
		name  string
		input string
	}{
		{"value on an indented line", "x =\n    1"},
		{"value on an unindented line", "x =\n1"},
		{"comment after =", "x = # note\n    1"},
		{"bracket on the next line", "m =\n[\n    1 2\n]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sp := NewScriptParser(lexer.New("TestValueStartsOnAssignmentLine", tt.input+"\nafter = 7"))
			program := sp.Parse()
			require.NotEmpty(t, sp.Errors())
			require.Equal(t, msg, sp.Errors()[0])
			found := false
			for _, stmt := range program.Statements {
				if let, ok := stmt.(*ast.LetStatement); ok && let != nil && let.Name[0].Value == "after" {
					found = true
				}
			}
			require.True(t, found, "the statement after the assignment still parses")
		})
	}

	sp := NewScriptParser(lexer.New("TestValueStartsOnAssignmentLine", "x ="))
	sp.Parse()
	require.Equal(t, []string{msg}, sp.Errors())
}

// A line ending in a comma continues on the next line when that line is
// indented, so a call or a function's arguments can span lines.
func TestLineBreakAfterComma(t *testing.T) {
	for _, tt := range []struct {
		name   string
		input  string
		expect []string
	}{
		{"arguments after a comma", "x = f(x,\n    y, z)\na = x * x", []string{"x = f(x, y, z)", "a = (x * x)"}},
		{"one argument per line", "x = f(x,\n    y,\n    z)\na = x * x", []string{"x = f(x, y, z)", "a = (x * x)"}},
		{"block literal argument", "x = f([\n    1 2\n    3 4\n])", []string{"x = f([\n    1 2\n    3 4\n])"}},
		{"block literal argument on a continued line", "x = f(1,\n    [\n    2 3\n])", []string{"x = f(1, [\n    2 3\n])"}},
		{"successive block literal arguments", "x = f([\n    1 2\n], [\n    3 4\n])", []string{"x = f([\n    1 2\n], [\n    3 4\n])"}},
		{"parentheses in a block literal's row", "m = [\n    (1 + 2) 3\n    4 5\n]", []string{"m = [\n    (1 + 2) 3\n    4 5\n]"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sp := NewScriptParser(lexer.New("TestLineBreakAfterComma", tt.input))
			program := sp.Parse()
			require.Empty(t, sp.Errors())
			var got []string
			for _, stmt := range program.Statements {
				got = append(got, stmt.String())
			}
			require.Equal(t, tt.expect, got)
		})
	}
}

// A failed operand ends its expression: no operator or call applies to what
// failed, so these report errors instead of crashing the parser.
func TestFailedOperandEndsExpression(t *testing.T) {
	for _, input := range []string{"x = foo[]()", "a=0:3:=:", "3.5:(,(1\ny10:3"} {
		sp := NewScriptParser(lexer.New("TestFailedOperandEndsExpression", input))
		require.NotPanics(t, func() { sp.Parse() }, input)
		require.NotEmpty(t, sp.Errors(), input)
	}
}

// A part that fails fails what holds it, up to its statement, which leaves no
// node and reports no more errors; the statement after it parses.
func TestFailedPartFailsWhatHoldsIt(t *testing.T) {
	const noPrefix = "no prefix parse function for "
	for _, tt := range []struct{ input, err string }{
		{"x = 1 < 2 && f(] y", "1:16:" + noPrefix + "] found"},
		{"x = f(], 2)", "1:7:" + noPrefix + "] found"},
		{"x = 1 + )", "1:9:" + noPrefix + ") found"},
		{"x = -)", "1:6:" + noPrefix + ") found"},
		{"x = a.b(1)", "1:6:function calls must target identifiers"},
		{"x = a. + 1", "1:6:expected next token to be IDENT, got OPERATOR instead"},
		{"x = [1 ) 2]", "1:8:" + noPrefix + ") found"},
		{"x = [\n    1 )\n]", "2:7:" + noPrefix + ") found"},
		{"m = [\n    1 2\n      3 4\n]", "3:7:" + strayIndentErr},
		{"y = a > 0 )", "1:11:" + noPrefix + ") found"},
		{")", "1:1:" + noPrefix + ") found"},
	} {
		sp := NewScriptParser(lexer.New("TestFailedPartFailsWhatHoldsIt", tt.input+"\nafter = 3"))
		var program *ast.Program
		require.NotPanics(t, func() { program = sp.Parse() }, tt.input)
		require.Equal(t, []string{"TestFailedPartFailsWhatHoldsIt:" + tt.err}, sp.Errors(), tt.input)
		requireWholeStatements(t, program.Statements)
		require.Equal(t, []string{"after = 3"}, statementStrings(program.Statements), tt.input)
	}
}

// A statement's conditions are judged by its own errors, so a conditional
// statement after an error still parses.
func TestConditionAfterError(t *testing.T) {
	sp := NewScriptParser(lexer.New("TestConditionAfterError", "x = )\ny = a > 0 1"))
	program := sp.Parse()
	require.Equal(t, []string{"TestConditionAfterError:1:5:no prefix parse function for ) found"}, sp.Errors())
	require.Equal(t, []string{"y = (a > 0) 1"}, statementStrings(program.Statements))
}

// FuzzParse checks the parser's failure contract on any input: no panic, no
// statement that holds a failed part, and a statement after the input that
// parses whatever the input's errors.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"x = 1 < 2 && f(] y",
		"x = [\n    f(1",
		"x = [1 f(2",
		"x = f(1,\n    [\n        2 3\n    ])",
		"x = (1 +\n    2)",
		"t = [\n  : a b\n    1 2\n]",
		"y = F(x)\n    m = [\n        1 2\n]",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if !lexesSentinel(src) {
			t.Skip("an open string runs on past the input")
		}
		program := NewScriptParser(lexer.New("FuzzParse", src+"\nsentinel = 7")).Parse()
		requireWholeStatements(t, program.Statements)
		require.NotEmpty(t, program.Statements)
		require.Equal(t, "sentinel = 7", program.Statements[len(program.Statements)-1].String())
		NewCodeParser(lexer.New("FuzzParse", src)).Parse()
	})
}

// lexesSentinel reports whether the line FuzzParse appends to src lexes as
// tokens of its own, rather than inside a string that src leaves open.
func lexesSentinel(src string) bool {
	var lits []string
	l := lexer.New("FuzzParse", src+"\nsentinel = 7")
	for tok, _ := l.NextToken(); tok.Type != token.EOF; tok, _ = l.NextToken() {
		lits = append(lits, tok.Literal)
	}
	n := len(lits)
	return n >= 3 && lits[n-3] == "sentinel" && lits[n-2] == "=" && lits[n-1] == "7"
}

// requireWholeStatements fails on a statement that is a typed nil or holds a
// nil expression anywhere.
func requireWholeStatements(t *testing.T, stmts []ast.Statement) {
	t.Helper()
	for _, stmt := range stmts {
		require.NotNil(t, stmt)
		switch s := stmt.(type) {
		case *ast.LetStatement:
			requireWholeExpressions(t, s.Value...)
			requireWholeExpressions(t, s.Condition...)
		case *ast.PrintStatement:
			requireWholeExpressions(t, s.Expression)
		}
	}
}

func requireWholeExpressions(t *testing.T, exprs ...ast.Expression) {
	t.Helper()
	for _, expr := range exprs {
		require.NotNil(t, expr)
		requireWholeExpressions(t, ast.ExprChildren(expr)...)
	}
}

func statementStrings(stmts []ast.Statement) []string {
	strs := make([]string, len(stmts))
	for i, stmt := range stmts {
		strs[i] = stmt.String()
	}
	return strs
}

// An index bracket keeps its expression on its line.
func TestIndexStaysOnOneLine(t *testing.T) {
	sp := NewScriptParser(lexer.New("TestIndexStaysOnOneLine", "value = data[\n    i]"))
	sp.Parse()
	require.Equal(t, []string{"TestIndexStaysOnOneLine:1:14:" + lineBreakErr}, sp.Errors())
}

// Pluto has no line continuation: a backslash is an illegal character in a
// row, a table header or an expression, and the first error says so. The
// errors after it are the usual recovery after an illegal character.
func TestBackslashIsIllegal(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input string
		pos   string
	}{
		{"row", "x = [1 2 \\\n    3 4]", "1:10"},
		{"table header", "t = [\n  : A(0) \\\n    B(0)\n]", "2:10"},
		{"expression", "x = 1 + \\\n2", "1:9"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sp := NewScriptParser(lexer.New("TestBackslashIsIllegal", tt.input))
			sp.Parse()
			require.NotEmpty(t, sp.Errors())
			require.Equal(t, "TestBackslashIsIllegal:"+tt.pos+":Illegal character '\\'", sp.Errors()[0])
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
				require.Len(t, program.Statements, 2, "expected two statements for identifier range case")
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

	// Ensure array ranges inside expressions parse correctly.
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

func TestDotExpression(t *testing.T) {
	input := `val = p.age`
	sp := NewScriptParser(lexer.New("TestDotExpression", input))
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	stmt := requireOnlyLetStmt(t, program)
	require.Len(t, stmt.Value, 1)
	dot, ok := stmt.Value[0].(*ast.DotExpression)
	require.Truef(t, ok, "expected *ast.DotExpression, got %T", stmt.Value[0])
	require.Equal(t, "age", dot.Field)
	left, ok := dot.Left.(*ast.Identifier)
	require.True(t, ok, "expected identifier on left side of dot")
	require.Equal(t, "p", left.Value)
}

func TestDotExpressionDisallowsWhitespace(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"space before dot", `p .name`},
		{"space after dot", `p. age`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp := NewScriptParser(lexer.New(tt.name, tt.input))
			_ = sp.Parse()
			require.NotEmpty(t, sp.Errors(), "expected parse error for spaced dot access")
		})
	}
}
