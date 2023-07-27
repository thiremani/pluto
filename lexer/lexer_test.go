package lexer

import (
    "testing"
    "pluto/token"
)

type Test struct {
    expectedType token.TokenType
    expectedLiteral string
}

func checkInput(t *testing.T, input string, tests []Test) {
    l := New(input)

	for i, tt := range tests {
		tok := l.NextToken()

		if tok.Type != tt.expectedType {
			t.Fatalf("tests[%d] - tokentype wrong. expected=%q, got=%q",
				i, tt.expectedType, tok.Type)
		}

		if tok.Literal != tt.expectedLiteral {
			t.Fatalf("tests[%d] - literal wrong. expected=%q, got=%q",
				i, tt.expectedLiteral, tok.Literal)
		}
	}
}

func TestNextToken(t *testing.T) {
    input := `five = 5
        # Test comment
# another comment
    ten = 10
    # abc
    # def
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

    tests := []Test {
        {token.IDENT, "five"},
        {token.ASSIGN, "="},
        {token.INT, "5"},
        {token.NEWLINE, "\n"},
        {token.INDENT, "t"},
        {token.IDENT, "ten"},
        {token.ASSIGN, "="},
        {token.INT, "10"},
        {token.NEWLINE, "\n"},
        {token.DEF, "def"},
        {token.IDENT, "add"},
        {token.LPAREN, "("},
        {token.IDENT, "x"},
        {token.COMMA, ","},
        {token.IDENT, "y"},
        {token.RPAREN, ")"},
        {token.NEWLINE, "\n"},
        {token.INDENT, "r"},
        {token.RETURN, "return"},
        {token.IDENT, "x"},
        {token.PLUS, "+"},
        {token.IDENT, "y"},
        {token.NEWLINE, "\n"},
        {token.DEINDENT, "r"},
        {token.IDENT, "result"},
        {token.ASSIGN, "="},
        {token.IDENT, "add"},
        {token.LPAREN, "("},
        {token.IDENT, "five"},
        {token.COMMA, ","},
        {token.IDENT, "ten"},
        {token.RPAREN, ")"},
        {token.NEWLINE, "\n"},
        {token.BANG, "!"},
        {token.MINUS, "-"},
        {token.SLASH, "/"},
        {token.ASTERISK, "*"},
        {token.INT, "5"},
        {token.NEWLINE, "\n"},
        {token.INT, "5"},
        {token.LT, "<"},
        {token.INT, "10"},
        {token.GT, ">"},
        {token.INT, "5"},
        {token.NEWLINE, "\n"},
        {token.IF, "if"},
        {token.INT, "5"},
        {token.LT, "<"},
        {token.INT, "10"},
        {token.NEWLINE, "\n"},
        {token.INDENT, "r"},
        {token.RETURN, "return"},
        {token.TRUE, "true"},
        {token.NEWLINE, "\n"},
        {token.DEINDENT, "e"},
        {token.ELSE, "else"},
        {token.NEWLINE, "\n"},
        {token.INDENT, "r"},
        {token.RETURN, "return"},
        {token.FALSE, "false"},
        {token.NEWLINE, "\n"},
        {token.DEINDENT, "1"},
        {token.DEINDENT, "1"},
        {token.INT, "10"},
        {token.EQ, "=="},
        {token.INT, "10"},
        {token.NEWLINE, "\n"},
        {token.INDENT, "1"},
        {token.INT, "10"},
        {token.NOT_EQ, "!="},
        {token.INT, "9"},
        {token.NEWLINE, "\n"},
        {token.EOF, ""},
    }

    checkInput(t, input, tests)
}

func TestIndentErr(t *testing.T) {
    input := `aesop = 4
    bulb = 5
  cat = 3
    `

    tests := []Test {
        {token.IDENT, "aesop"},
        {token.ASSIGN, "="},
        {token.INT, "4"},
        {token.NEWLINE, "\n"},
        {token.INDENT, "b"},
        {token.IDENT, "bulb"},
        {token.ASSIGN, "="},
        {token.INT, "5"},
        {token.NEWLINE, "\n"},
        {token.ILLEGAL, "c"},
    }

    checkInput(t, input, tests)
}
