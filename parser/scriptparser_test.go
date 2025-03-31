package parser

import (
	"pluto/ast"
	"pluto/lexer"
	"strings"
	"testing"
)

func TestAssign(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expId  string
		expStr string
	}{
		{
			name:   "simple assignment",
			input:  "x = 5",
			expId:  "=",
			expStr: "x = 5",
		},
		{
			name:   "math expression assignment",
			input:  "y = 5 * 3 + 2",
			expId:  "=",
			expStr: "y = ((5 * 3) + 2)",
		},
		{
			name:   "complex expression assignment",
			input:  "foobar = 2 + 3 / 5",
			expId:  "=",
			expStr: "foobar = (2 + (3 / 5))",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New(tt.input)
			sp := NewScriptParser(l)
			program := sp.Parse()
			checkParserErrors(t, sp.p)

			if len(program.Statements) != 1 {
				t.Fatalf("program.Statements does not contain 1 statement. got=%d", len(program.Statements))
			}

			stmt := program.Statements[0]
			if !testStmt(t, stmt, tt.expId, tt.expStr) {
				return
			}
		})
	}
}

func TestInvalidAssignment(t *testing.T) {
	tests := []struct {
		input    string
		expError string
	}{
		{
			input:    "123 = 5",
			expError: `1:1:123:expected expression to be of type "*ast.Identifier". Instead got "*ast.IntegerLiteral"`,
		},
		{
			input:    "x, 5 = 1, 2", // Invalid identifier in multi-assign
			expError: `1:4:5:expected expression to be of type "*ast.Identifier". Instead got "*ast.IntegerLiteral"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			l := lexer.New(tt.input)
			sp := NewScriptParser(l)
			sp.Parse()
			errs := sp.Errors()
			if len(errs) == 0 {
				t.Fatal("expected parser errors, got none")
			}
			if errs[0] != tt.expError {
				t.Fatalf("unexpected error: %s\nwant: %s", errs[0], tt.expError)
			}
		})
	}
}

func TestMultiAssign(t *testing.T) {
	tests := []struct {
		input  string
		expId  string
		expStr string
	}{
		{"x, y = 2, 4", "=", "x, y = 2, 4"},
	}

	for _, tt := range tests {
		l := lexer.New(tt.input)
		sp := NewScriptParser(l)
		program := sp.Parse()
		checkParserErrors(t, sp.p)

		if len(program.Statements) != 1 {
			t.Fatalf("program.Statements does not contain 1 statement. got=%d", len(program.Statements))
		}

		stmt := program.Statements[0]

		if !testStmt(t, stmt, tt.expId, tt.expStr) {
			return
		}
	}
}

func TestIdentifierExpression(t *testing.T) {
	input := "foobar"

	l := lexer.New(input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	checkParserErrors(t, sp.p)

	if len(program.Statements) != 1 {
		t.Fatalf("program has not enough statements. got=%d",
			len(program.Statements))
	}
	stmt, ok := program.Statements[0].(*ast.PrintStatement)
	if !ok {
		t.Fatalf("program.Statements[0] is not ast.PrintStatement. got=%T",
			program.Statements[0])
	}

	ident, ok := stmt.Expression[0].(*ast.Identifier)
	if !ok {
		t.Fatalf("exp not *ast.Identifier. got=%T", stmt.Expression)
	}
	if ident.Value != "foobar" {
		t.Errorf("ident.Value not %s. got=%s", "foobar", ident.Value)
	}
	if ident.Tok().Literal != "foobar" {
		t.Errorf("ident.TokenLiteral not %s. got=%s", "foobar",
			ident.Tok().Literal)
	}
}

func TestIntegerLiteralExpression(t *testing.T) {
	input := "5"

	l := lexer.New(input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	checkParserErrors(t, sp.p)

	if len(program.Statements) != 1 {
		t.Fatalf("program has not enough statements. got=%d",
			len(program.Statements))
	}
	stmt, ok := program.Statements[0].(*ast.PrintStatement)
	if !ok {
		t.Fatalf("program.Statements[0] is not ast.ExpressionStatement. got=%T",
			program.Statements[0])
	}

	literal, ok := stmt.Expression[0].(*ast.IntegerLiteral)
	if !ok {
		t.Fatalf("exp not *ast.IntegerLiteral. got=%T", stmt.Expression)
	}
	if literal.Value != 5 {
		t.Errorf("literal.Value not %d. got=%d", 5, literal.Value)
	}
	if literal.Tok().Literal != "5" {
		t.Errorf("literal.TokenLiteral not %s. got=%s", "5",
			literal.Tok().Literal)
	}
}

func TestStringLiteral(t *testing.T) {
	input := `"hello"`
	l := lexer.New(input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	stmt := program.Statements[0].(*ast.PrintStatement)
	literal := stmt.Expression[0].(*ast.StringLiteral)
	if literal.Value != "hello" {
		t.Errorf("literal.Value not %q. got=%q", "hello", literal.Value)
	}
}

func TestParsingPrefixExpressions(t *testing.T) {
	prefixTests := []struct {
		input    string
		operator string
		value    interface{}
	}{
		{"!5", "!", 5},
		{"-15", "-", 15},
		{"!foobar", "!", "foobar"},
		{"-foobar", "-", "foobar"},
	}

	for _, tt := range prefixTests {
		l := lexer.New(tt.input)
		sp := NewScriptParser(l)
		program := sp.Parse()
		checkParserErrors(t, sp.p)

		if len(program.Statements) != 1 {
			t.Fatalf("program.Statements does not contain %d statements. got=%d\n",
				1, len(program.Statements))
		}

		stmt, ok := program.Statements[0].(*ast.PrintStatement)
		if !ok {
			t.Fatalf("program.Statements[0] is not ast.ExpressionStatement. got=%T",
				program.Statements[0])
		}

		exp, ok := stmt.Expression[0].(*ast.PrefixExpression)
		if !ok {
			t.Fatalf("stmt is not ast.PrefixExpression. got=%T", stmt.Expression)
		}
		if exp.Operator != tt.operator {
			t.Fatalf("exp.Operator is not '%s'. got=%s",
				tt.operator, exp.Operator)
		}
		if !testLiteralExpression(t, exp.Right, tt.value) {
			return
		}
	}
}

func TestParsingInfixExpressions(t *testing.T) {
	infixTests := []struct {
		input      string
		leftValue  interface{}
		operator   string
		rightValue interface{}
	}{
		{"5 + 5", 5, "+", 5},
		{"5 - 5", 5, "-", 5},
		{"5 * 5", 5, "*", 5},
		{"5 / 5", 5, "/", 5},
		{"5 > 5", 5, ">", 5},
		{"5 < 5", 5, "<", 5},
		{"5 == 5", 5, "==", 5},
		{"5 != 5", 5, "!=", 5},
		{"foobar + barfoo", "foobar", "+", "barfoo"},
		{"foobar - barfoo", "foobar", "-", "barfoo"},
		{"foobar * barfoo", "foobar", "*", "barfoo"},
		{"foobar / barfoo", "foobar", "/", "barfoo"},
		{"foobar > barfoo", "foobar", ">", "barfoo"},
		{"foobar < barfoo", "foobar", "<", "barfoo"},
		{"foobar == barfoo", "foobar", "==", "barfoo"},
		{"foobar != barfoo", "foobar", "!=", "barfoo"},
	}

	for _, tt := range infixTests {
		l := lexer.New(tt.input)
		sp := NewScriptParser(l)
		program := sp.Parse()
		checkParserErrors(t, sp.p)

		if len(program.Statements) != 1 {
			t.Fatalf("program.Statements does not contain %d statements. got=%d\n",
				1, len(program.Statements))
		}

		stmt, ok := program.Statements[0].(*ast.PrintStatement)
		if !ok {
			t.Fatalf("program.Statements[0] is not ast.ExpressionStatement. got=%T",
				program.Statements[0])
		}

		if !testInfixExpression(t, stmt.Expression[0], tt.leftValue,
			tt.operator, tt.rightValue) {
			return
		}
	}
}

func TestOperatorPrecedenceParsing(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			"-a * b",
			"((-a) * b)",
		},
		{
			"!(-a)",
			"(!(-a))",
		},
		{
			"a + b + c",
			"((a + b) + c)",
		},
		{
			"a + b - c",
			"((a + b) - c)",
		},
		{
			"a * b * c",
			"((a * b) * c)",
		},
		{
			"a * b / c",
			"((a * b) / c)",
		},
		{
			"a + b / c",
			"(a + (b / c))",
		},
		{
			"a + b * c + d / e - f",
			"(((a + (b * c)) + (d / e)) - f)",
		},
		{
			"5 > 4 == 3 < 4",
			"(((5 > 4) == 3) < 4)",
		},
		{
			"5 < 4 != 3 > 4",
			"(((5 < 4) != 3) > 4)",
		},
		{
			"3 + 4 * 5 == 3 * 1 + 4 * 5",
			"((3 + ((4 * (5 == 3)) * 1)) + (4 * 5))",
		},
		{
			"3 > 5 == a",
			"((3 > 5) == a)",
		},
		{
			"3 < 5 == a",
			"((3 < 5) == a)",
		},
		{
			"1 + (2 + 3) + 4",
			"((1 + (2 + 3)) + 4)",
		},
		{
			"(5 + 5) * 2",
			"((5 + 5) * 2)",
		},
		{
			"2 / (5 + 5)",
			"(2 / (5 + 5))",
		},
		{
			"(5 + 5) * 2 * (5 + 5)",
			"(((5 + 5) * 2) * (5 + 5))",
		},
		{
			"-(5 + 5)",
			"(-(5 + 5))",
		},
		{
			"a + add(b * c) + d",
			"((a + add((b * c))) + d)",
		},
		{
			"add(a, b, 1, 2 * 3, 4 + 5, add(6, 7 * 8))",
			"add(a, b, 1, (2 * 3), (4 + 5), add(6, (7 * 8)))",
		},
		{
			"add(a + b + c * d / f + g)",
			"add((((a + b) + ((c * d) / f)) + g))",
		},
	}

	for _, tt := range tests {
		l := lexer.New(tt.input)
		sp := NewScriptParser(l)
		program := sp.Parse()
		checkParserErrors(t, sp.p)

		actual := program.String()
		if actual != tt.expected {
			t.Errorf("expected=%q, got=%q", tt.expected, actual)
		}
	}
}

func TestImplicitMultParsing(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expStr string
	}{
		{
			name:   "simple multiplication",
			input:  "x = 5a",
			expStr: "x = (5 * a)",
		},
		{
			name:   "multiplication and addition",
			input:  "y = 5x + 2",
			expStr: "y = ((5 * x) + 2)",
		},
		{
			name:   "complex function parsing",
			input:  "y = x^2 + 3.14x + 1",
			expStr: "y = (((x ^ 2) + (3.14 * x)) + 1)",
		},
		{
			name:   "reverse function parsing",
			input:  "y = 1 + 2x + 3.11x^2 + 2.03x3^3 + 7x3ab^4",
			expStr: "y = ((((1 + (2 * x)) + (3.11 * (x ^ 2))) + (2.03 * (x3 ^ 3))) + (7 * (x3ab ^ 4)))",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New(tt.input)
			sp := NewScriptParser(l)
			program := sp.Parse()
			checkParserErrors(t, sp.p)

			if len(program.Statements) != 1 {
				t.Fatalf("program.Statements does not contain 1 statement. got=%d", len(program.Statements))
			}

			_, ok := program.Statements[0].(*ast.LetStatement)
			if !ok {
				t.Fatalf("program.Statements[0] is not ast.ExpressionStatement. got=%T",
					program.Statements[0])
			}

			s := program.String()
			if s != tt.expStr {
				t.Errorf("expected=%q, got=%q", tt.expStr, s)
			}
		})
	}
}

func TestImplicitMultParsingSpaces(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expErr string
	}{
		{
			name:   "simple multiplication",
			input:  "x = 5 a",
			expErr: "1:5:5:Expression \"5\" is not a condition. The main operation should be a comparison",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := lexer.New(tt.input)
			sp := NewScriptParser(l)
			sp.Parse()
			errs := sp.Errors()
			if len(errs) != 1 {
				t.Errorf("Expected 1 error, got %d", len(errs))
			}

			if errs[0] != tt.expErr {
				t.Errorf("expected=%q, got=%q", tt.expErr, errs[0])
			}
		})
	}
}

func TestConditionExpression(t *testing.T) {
	input := `a = x < y x
res = a > 3 + 2`

	l := lexer.New(input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	checkParserErrors(t, sp.p)

	if len(program.Statements) != 2 {
		t.Fatalf("program.Statements does not contain %d statements. got=%d\n",
			1, len(program.Statements))
	}

	stmt, ok := program.Statements[0].(*ast.LetStatement)
	if !ok {
		t.Fatalf("program.Statements[0] is not ast.ExpressionStatement. got=%T",
			program.Statements[0])
	}

	if !testInfixExpression(t, stmt.Condition[0], "x", "<", "y") {
		return
	}

	if !testIdentifier(t, stmt.Name[0], "a") {
		return
	}

	if !testLiteralExpression(t, stmt.Value[0], "x") {
		return
	}

	stmt, ok = program.Statements[1].(*ast.LetStatement)
	if !ok {
		t.Fatalf("program.Statements[1] is not ast.ExpressionStatement. got=%T",
			program.Statements[1])
	}

	exp := stmt.Value[0].(*ast.InfixExpression)
	left := exp.Left
	if !testInfixExpression(t, left, "a", ">", 3) {
		return
	}

	if exp.Operator != "+" {
		t.Errorf("exp.Operator is not '+'. got=%s", exp.Operator)
	}

	if !testLiteralExpression(t, exp.Right, 2) {
		return
	}

	if !testIdentifier(t, stmt.Name[0], "res") {
		return
	}
}

func TestNestedGuardCondition(t *testing.T) {
	input := `res = (a > 3) < (b < 5) (c + d)`
	l := lexer.New(input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	checkParserErrors(t, sp.p)

	if len(program.Statements) != 1 {
		t.Fatalf("program.Statements does not contain 1 statement. got=%d", len(program.Statements))
	}

	stmt, ok := program.Statements[0].(*ast.LetStatement)
	if !ok {
		t.Fatalf("stmt is not ast.LetStatement. got=%T", program.Statements[0])
	}

	// Condition: (a > 3) < (b < 5)
	condInfix, ok := stmt.Condition[0].(*ast.InfixExpression)
	if !ok {
		t.Fatalf("condition is not infix expression. got=%T", stmt.Condition[0])
	}

	// Validate nested conditions
	if !testInfixExpression(t, condInfix.Left, "a", ">", 3) {
		return
	}
	if !testInfixExpression(t, condInfix.Right, "b", "<", 5) {
		return
	}

	// Value: (c + d)
	if !testInfixExpression(t, stmt.Value[0], "c", "+", "d") {
		return
	}
}

func TestMultiReturnCondition(t *testing.T) {
	input := "x, y = a > 5 10, 20" // If "a > 5", assign x=10, y=20

	l := lexer.New(input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	checkParserErrors(t, sp.p)

	stmt := program.Statements[0].(*ast.LetStatement)
	// Verify condition
	if !testInfixExpression(t, stmt.Condition[0], "a", ">", 5) {
		return
	}
	// Verify values
	if !testIntegerLiteral(t, stmt.Value[0], 10) || !testIntegerLiteral(t, stmt.Value[1], 20) {
		t.Fatal("values not parsed correctly")
	}
}

func TestInvalidConditionError(t *testing.T) {
	tests := []struct {
		input    string
		expError string
	}{
		{
			input:    "x = 5 + 3 y", // "+" is not a comparison
			expError: "Expression \"(5 + 3)\" is not a condition",
		},
		{
			input:    "res = foo(2) result", // Function call is not a comparison
			expError: "Expression \"foo(2)\" is not a condition",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			l := lexer.New(tt.input)
			sp := NewScriptParser(l)
			sp.Parse()
			errs := sp.Errors()

			if len(errs) == 0 {
				t.Fatal("expected parser error, got none")
			}
			if !strings.Contains(errs[0], tt.expError) {
				t.Fatalf("wrong error: %q (expected %q)", errs[0], tt.expError)
			}
		})
	}
}

func TestFunctionCallInCondition(t *testing.T) {
	input := "res = pow(2, 3) > 8 result"

	l := lexer.New(input)
	sp := NewScriptParser(l)
	program := sp.Parse()
	checkParserErrors(t, sp.p)

	stmt := program.Statements[0].(*ast.LetStatement)
	// Condition: "pow(2, 3) > 8"
	cond, ok := stmt.Condition[0].(*ast.InfixExpression)
	if !ok || cond.Operator != ">" {
		t.Fatalf("condition not parsed as infix expression")
	}
	// Left side: "pow(2, 3)"
	leftCall, ok := cond.Left.(*ast.CallExpression)
	if !ok || leftCall.Function.Value != "pow" {
		t.Fatal("function call in condition not parsed")
	}
}
