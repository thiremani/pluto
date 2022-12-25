package lexer

import (
	"testing"

	"pluto/token"
)

func TestNextToken(t *testing.T) {
	input := `five = 5
	ten = 10
	def add(x, y)
		return x + y
	result = add(five, ten)
	!-/*5
	5 < 10 > 5
	
	if 5 < 10
		return true
	else
		return false
	
	10 == 10
	10 != 9
	`

	tests := []struct {
		expType token.TokenType
		expLiteral string
	} {
		{token.IDENT, "five"},
		{token.ASSIGN, "="},
		{token.INT, "5"},
		{token.IDENT, "ten"},
		{token.ASSIGN, "="},
		{token.INT, "10"},
		{token.DEF, "def"},
		{token.IDENT, "add"},
		{token.LPAREN, "("},
		{token.IDENT, "x"},
		{token.COMMA, ","},
		{token.IDENT, "y"},
		{token.RPAREN, ")"},
		{token.RETURN, "return"},
		{token.IDENT, "x"},
		{token.PLUS, "+"},
		{token.IDENT, "y"},
		{token.IDENT, "result"},
		{token.ASSIGN, "="},
		{token.IDENT, "add"},
		{token.LPAREN, "("},
		{token.IDENT, "five"},
		{token.COMMA, ","},
		{token.IDENT, "ten"},
		{token.RPAREN, ")"},
		{token.BANG, "!"},
		{token.MINUS, "-"},
		{token.SLASH, "/"},
		{token.ASTERISK, "*"},
		{token.INT, "5"},
		{token.INT, "5"},
		{token.LT, "<"},
		{token.INT, "10"},
		{token.GT, ">"},
		{token.INT, "5"},
		{token.IF, "if"},
		{token.INT, "5"},
		{token.LT, "<"},
		{token.INT, "10"},
		{token.RETURN, "return"},
		{token.TRUE, "true"},
		{token.ELSE, "else"},
		{token.RETURN, "return"},
		{token.FALSE, "false"},
		{token.INT, "10"},
		{token.EQ, "=="},
		{token.INT, "10"},
		{token.INT, "10"},
		{token.NOT_EQ, "!="},
		{token.INT, "9"},
		{token.EOF, ""},
	}

	l := New(input)

	for i, tt := range tests {
		tok := l.NextToken()

		if tok.Type != tt.expType {
			t.Fatalf("tests[%d] - tokentype wrong. expected=%q, got=%q", i, tt.expType, tok.Type)
		}

		if tok.Literal != tt.expLiteral {
			t.Fatalf("tests[%d] - literal wrong. expected=%q, got=%q", i, tt.expLiteral, tok.Literal)
		}
	}
}