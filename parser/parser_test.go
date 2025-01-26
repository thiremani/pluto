package parser

import (
	"fmt"
	"pluto/ast"
	"pluto/lexer"
	"testing"
)

func TestAssign(t *testing.T) {
	tests := []struct {
		input  string
		expId  string
		expStr string
	} {
		{"x = 5", "=", "x = 5"},
		{"y = 5 * 3 + 2", "=", "y = ((5 * 3) + 2)"},
		{"foobar = 2 + 3 / 5", "=", "foobar = (2 + (3 / 5))"},
	}

	for _, tt := range tests {
		l := lexer.New(tt.input)
		p := New(l)
		program := p.ParseProgram()
		checkParserErrors(t, p)

		if len(program.Statements) != 1 {
			t.Fatalf("program.Statements does not contain 1 statement. got=%d", len(program.Statements))
		}

		stmt := program.Statements[0]

		if !testStmt(t, stmt, tt.expId, tt.expStr) {
			return
		}
	}
}

func TestInvalidAssignment(t *testing.T) {
    input := "123 = 5"
    l := lexer.New(input)
    p := New(l)
    p.ParseProgram()
	errs := p.Errors()
    if len(errs) == 0 {
        t.Fatalf("expected parser errors, got none")
    }

	expErr := `1:1:123:expected expression to be of type "*ast.Identifier". Instead got "*ast.IntegerLiteral"`
	if errs[0] != expErr {
		t.Fatalf("unexpected error message: %s. Expected: %s", errs[0], expErr)
	}
}

func TestMultiAssign(t *testing.T) {
	tests := []struct {
		input  string
		expId  string
		expStr string
	} {
		{"x, y = 2, 4", "=", "x, y = 2, 4"},
	}

	for _, tt := range tests {
		l := lexer.New(tt.input)
		p := New(l)
		program := p.ParseProgram()
		checkParserErrors(t, p)

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
	p := New(l)
	program := p.ParseProgram()
	checkParserErrors(t, p)

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
	p := New(l)
	program := p.ParseProgram()
	checkParserErrors(t, p)

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
		p := New(l)
		program := p.ParseProgram()
		checkParserErrors(t, p)

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
		p := New(l)
		program := p.ParseProgram()
		checkParserErrors(t, p)

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
			"!-a",
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
		p := New(l)
		p.inScript = true
		program := p.ParseProgram()
		checkParserErrors(t, p)

		actual := program.String()
		if actual != tt.expected {
			t.Errorf("expected=%q, got=%q", tt.expected, actual)
		}
	}
}

func TestConditionExpression(t *testing.T) {
	input := `a = x < y x
res = a > 3 + 2`

	l := lexer.New(input)
	p := New(l)
	program := p.ParseProgram()
	checkParserErrors(t, p)

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

	if exp.Operator!= "+" {
		t.Errorf("exp.Operator is not '+'. got=%s", exp.Operator)
	}

	if !testLiteralExpression(t, exp.Right, 2) {
		return
	}

	if !testIdentifier(t, stmt.Name[0], "res") {
		return
	}
}

func TestFunctionLiteralParsing(t *testing.T) {
	input := `y, quo = pow(x, n)
    y = 1
    quo = y / x
    y = 0:n x`

	l := lexer.New(input)
	p := New(l)
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

	if len(f.Body.Statements)!= 3 {
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
		input string
        expected []string
	} {
	{input: `a = fn()
    a = 4`, expected: []string{}},
		{input: `y = f(x)
    y = x * x`, expected: []string{"x"}},
		{input: `r = f(x, y, z)
    r = x + y + z`, expected: []string{"x", "y", "z"}},
	}

	for _, tt := range tests {
		l := lexer.New(tt.input)
		p := New(l)
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

func testInfixExpression(t *testing.T, exp ast.Expression, left interface{},
	operator string, right interface{}) bool {

	opExp, ok := exp.(*ast.InfixExpression)
	if !ok {
		t.Errorf("exp is not ast.InfixExpression. got=%T(%s)", exp, exp)
		return false
	}

	if !testLiteralExpression(t, opExp.Left, left) {
		return false
	}

	if opExp.Operator != operator {
		t.Errorf("exp.Operator is not '%s'. got=%q", operator, opExp.Operator)
		return false
	}

	if !testLiteralExpression(t, opExp.Right, right) {
		return false
	}

	return true
}

func testLiteralExpression(
	t *testing.T,
	exp ast.Expression,
	expected interface{},
) bool {
	switch v := expected.(type) {
	case int:
		return testIntegerLiteral(t, exp, int64(v))
	case int64:
		return testIntegerLiteral(t, exp, v)
	case string:
		return testIdentifier(t, exp, v)
	}
	t.Errorf("type of exp not handled. got=%T", exp)
	return false
}

func testIntegerLiteral(t *testing.T, il ast.Expression, value int64) bool {
	integ, ok := il.(*ast.IntegerLiteral)
	if !ok {
		t.Errorf("il not *ast.IntegerLiteral. got=%T", il)
		return false
	}

	if integ.Value != value {
		t.Errorf("integ.Value not %d. got=%d", value, integ.Value)
		return false
	}

	if integ.Tok().Literal != fmt.Sprintf("%d", value) {
		t.Errorf("integ.TokenLiteral not %d. got=%s", value,
			integ.Tok().Literal)
		return false
	}

	return true
}

func testIdentifier(t *testing.T, exp ast.Expression, value string) bool {
	ident, ok := exp.(*ast.Identifier)
	if !ok {
		t.Errorf("exp not *ast.Identifier. got=%T", exp)
		return false
	}

	if ident.Value != value {
		t.Errorf("ident.Value not %s. got=%s", value, ident.Value)
		return false
	}

	if ident.Tok().Literal != value {
		t.Errorf("ident.TokenLiteral not %s. got=%s", value,
			ident.Tok().Literal)
		return false
	}

	return true
}

func testStmt(t *testing.T, s ast.Statement, expToken string, expStr string) bool {
	if s.Tok().Literal != expToken {
		t.Errorf("s.TokenLiteral got=%q. Expected %q", s.Tok().Literal, expToken)
		return false
	}

	stmtStr := s.String()
	if stmtStr != expStr {
		t.Errorf("s.String got %q. Expected %q", stmtStr, expStr)
	}
	return true
}

func checkParserErrors(t *testing.T, p *Parser) {
	errors := p.Errors()
	if len(errors) == 0 {
		return
	}

	t.Errorf("parser has %d errors", len(errors))
	for _, msg := range errors {
		t.Errorf("parser error: %q", msg)
	}
	t.FailNow()
}
