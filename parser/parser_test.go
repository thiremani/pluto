package parser

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
)

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

func checkParserErrors(t *testing.T, p *StmtParser) {
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

func testLiteral(t *testing.T, lit ast.Expression, exp interface{}) {
	switch v := exp.(type) {
	case int64:
		if v >= 0 {
			testIntegerLiteral(t, lit, v)
			return
		}

		// expect a PrefixExpression
		pe, ok := lit.(*ast.PrefixExpression)
		require.True(t, ok, "expected *ast.PrefixExpression for negative, got %T", lit)
		require.Equal(t, "-", pe.Operator)

		// and its operand should be the absolute value
		testIntegerLiteral(t, pe.Right, -v)
	case string:
		testIdentifier(t, lit, v)
	case nil:
		require.Nil(t, lit, "expected nil literal, got %T", lit)
	default:
		t.Errorf("type of exp not handled. got=%T", lit)
	}
}

func TestParseRangeLiteral(t *testing.T) {
	tests := []struct {
		input     string
		wantStart interface{} // int64 or string
		wantStop  interface{} // int64 or string
		wantStep  interface{} // int64, string, or nil
	}{
		{
			input:     "x = 0:5 x",
			wantStart: int64(0),
			wantStop:  int64(5),
			wantStep:  nil,
		},
		{
			input:     "x = 0:n x",
			wantStart: int64(0),
			wantStop:  "n",
			wantStep:  nil,
		},
		{
			input:     "x = 0:10:2 x",
			wantStart: int64(0),
			wantStop:  int64(10),
			wantStep:  int64(2),
		},
		{
			input:     "x = 5:0:-1 x",
			wantStart: int64(5),
			wantStop:  int64(0),
			wantStep:  int64(-1),
		},
		{
			input:     "x = a:b:c x",
			wantStart: "a",
			wantStop:  "b",
			wantStep:  "c",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			l := lexer.New("range_test", tt.input)
			p := New(l)
			program := p.ParseProgram()
			require.Empty(t, p.Errors())

			// Should be one LetStatement: x = <range> x
			require.Len(t, program.Statements, 1, "expected exactly one statement")
			stmt, ok := program.Statements[0].(*ast.LetStatement)
			require.True(t, ok, "expected *ast.LetStatement, got %T", program.Statements[0])

			// And the RHS value must be a RangeLiteral
			require.Len(t, stmt.Condition, 1, "expected statement to have exactly one condition")
			rl, ok := stmt.Condition[0].(*ast.RangeLiteral)
			require.True(t, ok, "expected *ast.RangeLiteral, got %T", stmt.Condition[0])

			require.Equal(t, ":", rl.Token.Literal)
			testLiteral(t, rl.Start, tt.wantStart)
			testLiteral(t, rl.Stop, tt.wantStop)
			testLiteral(t, rl.Step, tt.wantStep)
		})
	}
}
