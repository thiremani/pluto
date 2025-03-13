package lexer

import (
	"pluto/token"
	"testing"
)

type Test struct {
	expectedType       token.TokenType
	expectedLiteral    string
	expectedError      string
	expectedLineOffset int
	expectedColumn     int
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

	tests := []Test{
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

	tests := []Test{
		{token.IDENT, "aesop", "", 1, 1},
		{token.ASSIGN, "=", "", 1, 7},
		{token.INT, "4", "", 1, 9},
		{token.NEWLINE, "\n", "", 1, 10},
		{token.INDENT, "b", "", 2, 5},
		{token.IDENT, "bulb", "", 2, 5},
		{token.ASSIGN, "=", "", 2, 10},
		{token.INT, "5", "", 2, 12},
		{token.NEWLINE, "\n", "", 2, 13},
		{token.ILLEGAL, "c", "3:3:c:" + INDENT_ERR, 3, 3},
	}

	checkInput(t, input, tests)
}

func TestTabErr(t *testing.T) {
	input := `aman = 5
    	bb = 10`

	tests := []Test{
		{token.IDENT, "aman", "", 1, 1},
		{token.ASSIGN, "=", "", 1, 6},
		{token.INT, "5", "", 1, 8},
		{token.NEWLINE, "\n", "", 1, 9},
		{token.ILLEGAL, "b", "2:6:b:" + INDENT_TAB_ERR, 2, 6},
	}

	checkInput(t, input, tests)

	input = `a = 5
		
    b = 6`

	tests = []Test{
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

	tests = []Test{
		{token.IDENT, "res", "", 1, 1},
		{token.ASSIGN, "=", "", 1, 5},
		{token.INT, "123", "", 1, 7},
		{token.NEWLINE, "\n", "", 1, 10},
		{token.ILLEGAL, "m", "2:2:m:" + INDENT_TAB_ERR, 2, 2},
		{token.IDENT, "m", "", 2, 2},
		{token.ASSIGN, "=", "", 2, 4},
		{token.IDENT, "n", "", 2, 6},
		{token.NEWLINE, "\n", "", 2, 7},
		{token.ILLEGAL, "q", "3:3:q:" + INDENT_TAB_ERR, 3, 3},
		{token.IDENT, "q", "", 3, 3},
		{token.ASSIGN, "=", "", 3, 5},
		{token.IDENT, "r", "", 3, 7},
	}

	checkInput(t, input, tests)
}

func TestEof(t *testing.T) {
	input := ``

	tests := []Test{
		{token.EOF, "", "", 1, 1},
	}

	checkInput(t, input, tests)

	input = `a = 10
    `
	tests = []Test{
		{token.IDENT, "a", "", 1, 1},
		{token.ASSIGN, "=", "", 1, 3},
		{token.INT, "10", "", 1, 5},
		{token.NEWLINE, "\n", "", 1, 7},
		{token.EOF, "", "", 2, 5},
	}
	checkInput(t, input, tests)

	input = `#`
	tests = []Test{
		{token.EOF, "", "", 1, 2},
	}
	checkInput(t, input, tests)
}

func TestFloat(t *testing.T) {
	input := `val = 3.14
met = 1.
`
	tests := []Test{
		{token.IDENT, "val", "", 1, 1},
		{token.ASSIGN, "=", "", 1, 5},
		{token.FLOAT, "3.14", "", 1, 7},
		{token.NEWLINE, "\n", "", 1, 11},
		{token.IDENT, "met", "", 2, 1},
		{token.ASSIGN, "=", "", 2, 5},
		{token.FLOAT, "1.", "", 2, 7},
		{token.NEWLINE, "\n", "", 2, 9},
		{token.EOF, "", "", 3, 1},
	}
	checkInput(t, input, tests)
}

func TestString(t *testing.T) {
	input := `"hello\nworld"`
	tests := []Test{
		{token.STRING, "hello\nworld", "", 1, 1},
	}
	checkInput(t, input, tests)
}

func TestUnicodeIdentifiers(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Valid Unicode identifiers
		{"变量", "变量"},           // Chinese characters
		{"αβγ", "αβγ"},         // Greek letters (general)
		{"π", "π"},             // Greek letter pi
		{"θ१२३", "θ१२३"},       // Greek letter theta with hindu numerals
		{"πθ", "πθ"},           // Multi-letter Greek identifier
		{"πθ123", "πθ123"},     // Greek letters with digits
		{"π_θ", "π_θ"},         // Greek letters with underscore
		{"_hidden", "_hidden"}, // Leading underscore
		{"i123", "i123"},       // Latin letter with digits
		{"ñandú", "ñandú"},     // Latin letters with diacritics
	}

	for _, tt := range tests {
		l := New(tt.input)
		ident := l.readIdentifier()

		if ident != tt.expected {
			t.Errorf("For input %q, expected identifier %q, got %q", tt.input, tt.expected, ident)
		}
	}
}

func TestASCIINumbers(t *testing.T) {
	tests := []struct {
		input             string
		expectedTokenType token.TokenType
		expectedLiteral   string
	}{
		// Valid ASCII number
		{"123", token.INT, "123"},
		// A number written with Arabic–Indic digits (U+0661, U+0662, U+0663)
		// Since we want to restrict numbers to ASCII, this should be treated as illegal.
		// We expect the lexer to return an ILLEGAL token for the first character.
		{"١٢٣", token.ILLEGAL, "١"},
		// A mixed case: starting with an ASCII digit should work, even if later there are non-ASCII digits.
		// (This may be subject to design: you might want to treat the entire literal as illegal.)
		{"1٢٣", token.INT, "1"},
	}

	for _, tt := range tests {
		l := New(tt.input)
		tok, _ := l.NextToken()

		if tok.Type != tt.expectedTokenType {
			t.Errorf("For input %q, expected token type %q, got %q", tt.input, tt.expectedTokenType, tok.Type)
		}
		if tok.Literal != tt.expectedLiteral {
			t.Errorf("For input %q, expected literal %q, got %q", tt.input, tt.expectedLiteral, tok.Literal)
		}
	}
}

func TestNextTokenUnexpected(t *testing.T) {
	tests := []struct {
		input       string
		expectedTok []token.Token
		expectedErr []string // expected error message per token; empty means no error.
	}{
		{
			input: "123abc",
			// "123" should be read as a number token, then "abc" as an identifier.
			expectedTok: []token.Token{
				{Type: token.INT, Literal: "123"},
				{Type: token.IDENT, Literal: "abc"},
			},
			expectedErr: []string{"", ""},
		},
		{
			input: "@abc",
			// "@" is illegal so should yield an ILLEGAL token with an error message,
			// then "abc" is lexed as an identifier.
			expectedTok: []token.Token{
				{Type: token.ILLEGAL, Literal: "@"},
				{Type: token.IDENT, Literal: "abc"},
			},
			expectedErr: []string{"1:1:@:Illegal character '@'", ""},
		},
	}

	for _, tt := range tests {
		l := New(tt.input)
		for i, expected := range tt.expectedTok {
			tok, err := l.NextToken()
			if tok.Type != expected.Type {
				t.Errorf("For input %q, token %d: expected type %q, got %q", tt.input, i, expected.Type, tok.Type)
			}
			if tok.Literal != expected.Literal {
				t.Errorf("For input %q, token %d: expected literal %q, got %q", tt.input, i, expected.Literal, tok.Literal)
			}
			expectedErr := tt.expectedErr[i]
			if expectedErr != "" {
				if err == nil || err.Error() != expectedErr {
					t.Errorf("For input %q, token %d: expected error %q, got %v", tt.input, i, expectedErr, err)
				}
			} else {
				if err != nil {
					t.Errorf("For input %q, token %d: expected no error, got %v", tt.input, i, err)
				}
			}
		}
	}
}
