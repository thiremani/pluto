package lexer

import (
	"testing"
    "pluto/token"
)

type Test struct {
    expectedType token.TokenType
    expectedLiteral string
    expectedError string
    expectedLineOffset int
    expectedColumn int
}

func checkInput(t *testing.T, input string, tests []Test) {
    l := New(input)

	for i, tt := range tests {
		tok, err := l.NextToken()

		if tok.Type != tt.expectedType {
            t.Errorf("expected error=%q, got=%q", tt.expectedError, err)
			t.Fatalf("tests[%d] - tokentype wrong. expected=%q, got=%q",
				i, tt.expectedType, tok.Type)
		}

		if tok.Literal != tt.expectedLiteral {
			t.Fatalf("tests[%d] - literal wrong. expected=%q, got=%q",
				i, tt.expectedLiteral, tok.Literal)
		}

        if tok.Line != tt.expectedLineOffset {
            t.Fatalf("tests[%d] - linenumber wrong. expected=%d, got=%d",
            i, tt.expectedLineOffset, tok.Line)
        }

        if tok.Column != tt.expectedColumn {
            t.Fatalf("tests[%d] - column wrong. expected=%d, got=%d",
            i, tt.expectedColumn, tok.Column)
        }

        if err != nil && err.Error() != tt.expectedError {
            t.Fatalf("tests[%d] - error wrong. expected=%q, got=%q",
            i, tt.expectedError, err)
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
    res = add(x, y)
        res = x + y
    result = add(five, ten)
    !-/*5
    5 < 10 > 5
    


    b = 5
    a = 10
        b > 2 3

10 == 10
    10 != 9
    `

    tests := []Test {
        {token.IDENT, "five", "", 1, 1},
        {token.ASSIGN, "=", "", 1, 6},
        {token.INT, "5", "", 1, 8},
        {token.NEWLINE, "\n", "", 1, 9},
        {token.INDENT, "t", "", 4, 5},
        {token.IDENT, "ten", "", 4, 5},
        {token.ASSIGN, "=", "", 4, 9},
        {token.INT, "10", "", 4, 11},
        {token.NEWLINE, "\n", "", 4, 13},
        {token.IDENT, "res", "", 7, 5},
        {token.ASSIGN, "=", "", 7, 9},
        {token.IDENT, "add", "", 7, 11},
        {token.LPAREN, "(", "", 7, 14},
        {token.IDENT, "x", "", 7, 15},
        {token.COMMA, ",", "", 7, 16},
        {token.IDENT, "y", "", 7, 18},
        {token.RPAREN, ")", "", 7, 19},
        {token.NEWLINE, "\n", "", 7, 20},
        {token.INDENT, "r", "", 8, 9},
        {token.IDENT, "res", "", 8, 9},
        {token.ASSIGN, "=", "", 8, 13},
        {token.IDENT, "x", "", 8, 15},
        {token.ADD, "+", "", 8, 17},
        {token.IDENT, "y", "", 8, 19},
        {token.NEWLINE, "\n", "", 8, 20},
        {token.DEINDENT, "r", "", 9, 5},
        {token.IDENT, "result", "", 9, 5},
        {token.ASSIGN, "=", "", 9, 12},
        {token.IDENT, "add", "", 9, 14},
        {token.LPAREN, "(", "", 9, 17},
        {token.IDENT, "five", "", 9, 18},
        {token.COMMA, ",", "", 9, 22},
        {token.IDENT, "ten", "", 9, 24},
        {token.RPAREN, ")", "", 9, 27},
        {token.NEWLINE, "\n", "", 9, 28},
        {token.NOT, "!", "", 10, 5},
        {token.SUB, "-", "", 10, 6},
        {token.QUO, "/", "", 10, 7},
        {token.MUL, "*", "", 10, 8},
        {token.INT, "5", "", 10, 9},
        {token.NEWLINE, "\n", "", 10, 10},
        {token.INT, "5", "", 11, 5},
        {token.LSS, "<", "", 11, 7},
        {token.INT, "10", "", 11, 9},
        {token.GTR, ">", "", 11, 12},
        {token.INT, "5", "", 11, 14},
        {token.NEWLINE, "\n", "", 11, 15},
        {token.IDENT, "b", "", 15, 5},
        {token.ASSIGN, "=", "", 15, 7},
        {token.INT, "5", "", 15, 9},
        {token.NEWLINE, "\n", "", 15, 10},
        {token.IDENT, "a", "", 16, 5},
        {token.ASSIGN, "=", "", 16, 7},
        {token.INT, "10", "", 16, 9},
        {token.NEWLINE, "\n", "", 16, 11},
        {token.INDENT, "b", "", 17, 9},
        {token.IDENT, "b", "", 17, 9},
        {token.GTR, ">", "", 17, 11},
        {token.INT, "2", "", 17, 13},
        {token.INT, "3", "", 17, 15},
        {token.NEWLINE, "\n", "", 17, 16},
        {token.DEINDENT, "1", "", 19, 1},
        {token.DEINDENT, "1", "", 19, 1},
        {token.INT, "10", "", 19, 1},
        {token.EQL, "==", "", 19, 4},
        {token.INT, "10", "", 19, 7},
        {token.NEWLINE, "\n", "", 19, 9},
        {token.INDENT, "1", "", 20, 5},
        {token.INT, "10", "", 20, 5},
        {token.NEQ, "!=", "", 20, 8},
        {token.INT, "9", "", 20, 11},
        {token.NEWLINE, "\n", "", 20, 12},
        {token.EOF, "", "", 21, 5},
    }

    checkInput(t, input, tests)
}

func TestIndentErr(t *testing.T) {
    input := `aesop = 4
    bulb = 5
  cat = 3
    `

    tests := []Test {
        {token.IDENT, "aesop", "", 1, 1},
        {token.ASSIGN, "=", "", 1, 7},
        {token.INT, "4", "", 1, 9},
        {token.NEWLINE, "\n", "", 1, 10},
        {token.INDENT, "b", "", 2, 5},
        {token.IDENT, "bulb", "", 2, 5},
        {token.ASSIGN, "=", "", 2, 10},
        {token.INT, "5", "", 2, 12},
        {token.NEWLINE, "\n", "", 2, 13},
        {token.ILLEGAL, "c", "indentation error", 3, 3},
    }

    checkInput(t, input, tests)
}

func TestTabErr(t *testing.T) {
    input := `aman = 5
    	bb = 10`

    tests := []Test {
        {token.IDENT, "aman", "", 1, 1},
        {token.ASSIGN, "=", "", 1, 6},
        {token.INT, "5", "", 1, 8},
        {token.NEWLINE, "\n", "", 1, 9},
        {token.ILLEGAL, "b", "indent using tabs not allowed", 2, 6},
    }

    checkInput(t, input, tests)

    input = `a = 5
		
    b = 6`

    tests = []Test {
        {token.IDENT, "a", "", 1, 1},
        {token.ASSIGN, "=", "", 1, 3},
        {token.INT, "5", "", 1, 5},
        {token.NEWLINE, "\n", "", 1, 6},
        {token.INDENT, "b", "", 3, 5},
        {token.IDENT, "b", "", 3, 5},
        {token.ASSIGN, "=", "", 3, 7},
        {token.INT, "6", "", 3, 9},
        {token.EOF, "", "", 3, 10},
    }

    checkInput(t, input, tests)

    input = `res = 123
	m = n
		q = r`

    tests = []Test {
        {token.IDENT, "res", "", 1, 1},
        {token.ASSIGN, "=", "", 1, 5},
        {token.INT, "123", "", 1, 7},
        {token.NEWLINE, "\n", "", 1, 10},
        {token.ILLEGAL, "m", "indent using tabs not allowed", 2, 2},
        {token.IDENT, "m", "", 2, 2},
        {token.ASSIGN, "=", "", 2, 4},
        {token.IDENT, "n", "", 2, 6},
		{token.NEWLINE, "\n", "", 2, 7},
        {token.ILLEGAL, "q", "indent using tabs not allowed", 3, 3},
        {token.IDENT, "q", "", 3, 3},
        {token.ASSIGN, "=", "", 3, 5},
        {token.IDENT, "r", "", 3, 7},
    }

	checkInput(t, input, tests)
}

func TestEof(t *testing.T) {
    input := ``

    tests := []Test {
        {token.EOF, "", "", 1, 1},
    }

    checkInput(t, input, tests)

    input = `a = 10
    `
    tests = []Test {
        {token.IDENT, "a", "", 1, 1},
        {token.ASSIGN, "=", "", 1, 3},
        {token.INT, "10", "", 1, 5},
        {token.NEWLINE, "\n", "", 1, 7},
        {token.EOF, "", "", 2, 5},
    }
    checkInput(t, input, tests)

    input = `#`
    tests = []Test {
        {token.EOF, "", "", 1, 2},
    }
    checkInput(t, input, tests)
}