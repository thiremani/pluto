package lexer

import (
	"pluto/token"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Lexer struct {
	input        []rune
	position     int   // current position in input (points to current rune)
	readPosition int   // current reading position in input (after current rune)
	curr         rune  // current rune under examination
	lineOffset   int   // line number
	column       int   // column number in the line
	onNewline    bool  // at beginning of new line
	indentStack  []int // indentation level stack
	toDeindent   int   // number of deindent tokens to be emitted before we continue with current token
}

const (
	eof = -1
)

const (
	INDENT_ERR     = "indentation error"
	INDENT_TAB_ERR = "indent using tabs not allowed"
)

func New(input string) *Lexer {
	l := &Lexer{input: []rune(input), lineOffset: 1, onNewline: true}
	l.readRune()
	return l
}

func (l *Lexer) createToken(tokenType token.TokenType, literal string) token.Token {
	return token.Token{
		Type:    tokenType,
		Literal: literal,
		Line:    l.lineOffset,
		Column:  l.column,
	}
}

func (l *Lexer) NextToken() (token.Token, *token.CompileError) {
	var tok token.Token
	var err *token.CompileError

	if l.onNewline {
		return l.indentToken()
	} else {
		l.skipWhitespace()
	}

	if l.curr == '#' {
		l.skipComment()
	}

	switch l.curr {
	case '\n':
		tok = l.createToken(token.NEWLINE, token.SYM_NEWLINE)
		l.newLine()
		l.onNewline = true
	case '"':
		tok = l.createToken(token.STRING, token.SYM_DQUOTE)
		l.readRune()
		tok.Literal = l.readString()
	case ':':
		tok = l.createToken(token.COLON, token.SYM_COLON)
	case ',':
		tok = l.createToken(token.COMMA, token.SYM_COMMA)
	case '(':
		tok = l.createToken(token.LPAREN, token.SYM_LPAREN)
	case ')':
		tok = l.createToken(token.RPAREN, token.SYM_RPAREN)
	case 0:
		fallthrough
	case eof:
		tok = l.createToken(token.EOF, "")
	case '=':
		if l.peekRune() == '=' {
			tok = l.createToken(token.EQL, token.SYM_EQL)
			l.readRune()
		} else {
			tok = l.createToken(token.ASSIGN, token.SYM_ASSIGN)
		}
	case '<':
		if l.peekRune() == '=' {
			tok = l.createToken(token.LEQ, token.SYM_LEQ)
			l.readRune()
		} else if l.peekRune() == '<' {
			tok = l.createToken(token.OPERATOR, token.SYM_SHL)
			l.readRune()
		} else {
			tok = l.createToken(token.LSS, token.SYM_LSS)
		}
	case '>':
		if l.peekRune() == '=' {
			tok = l.createToken(token.GEQ, token.SYM_GEQ)
			l.readRune()
		} else if l.peekRune() == '>' {
			l.readRune()
			if l.peekRune() == '>' {
				tok = l.createToken(token.OPERATOR, token.SYM_SHR)
				l.readRune()
			} else {
				tok = l.createToken(token.OPERATOR, token.SYM_ASR)
			}
		} else {
			tok = l.createToken(token.GTR, token.SYM_GTR)
		}
	case '!':
		if l.peekRune() == '=' {
			tok = l.createToken(token.NEQ, token.SYM_NEQ)
			l.readRune()
			l.readRune()
			return tok, nil
		}
		fallthrough
	default:
		if IsLetter(l.curr) {
			tok = l.createToken(token.IDENT, "")
			tok.Literal = l.readIdentifier()
			return tok, nil
		} else if IsDecimal(l.curr) || (l.curr == '.' && IsDecimal(l.peekRune())) {
			tok = l.createToken(token.INT, "")
			var isFloat bool
			tok.Literal, isFloat = l.readNumber()
			if isFloat {
				tok.Type = token.FLOAT
			}
			return tok, nil
		} else if IsOperator(l.curr) {
			// Read a maximal sequence of operator characters.
			tok = l.createToken(token.OPERATOR, "")
			tok.Literal = l.readOperator()
			return tok, nil
		} else {
			ch := string(l.curr)
			tok = l.createToken(token.ILLEGAL, ch)
			err = &token.CompileError{
				Token: tok,
				Msg:   "Illegal character '" + ch + "'",
			}
		}
	}

	l.readRune()
	return tok, err
}

func (l *Lexer) indentToken() (token.Token, *token.CompileError) {
	if l.toDeindent > 0 {
		return l.deindentToken()
	}

	indent, err := l.indentLevel()

	if err != nil {
		return l.createToken(token.ILLEGAL, string(l.curr)), err
	}

	if l.toDeindent > 0 {
		return l.deindentToken()
	}

	l.onNewline = false
	if indent {
		return l.createToken(token.INDENT, string(l.curr)), nil
	}

	return l.NextToken()
}

func (l *Lexer) deindentToken() (token.Token, *token.CompileError) {
	l.toDeindent--
	if len(l.indentStack) > 0 {
		l.indentStack = l.indentStack[:len(l.indentStack)-1]
	}
	if l.toDeindent == 0 {
		l.onNewline = false
	}

	return l.createToken(token.DEINDENT, string(l.curr)), nil
}

func (l *Lexer) skipNewlineSpaces() *token.CompileError {
	var err *token.CompileError
	for {
		for l.curr == ' ' {
			l.readRune()
		}

		if l.curr == '#' {
			l.skipComment()
		}

		for l.curr == '\t' {
			l.readRune()
			err = &token.CompileError{
				Token: l.createToken(token.ILLEGAL, string(l.curr)),
				Msg:   INDENT_TAB_ERR,
			}
		}

		if l.curr != '\n' {
			break
		}
		err = nil
		l.newLine()
		l.readRune()
	}
	return err
}

func (l *Lexer) indentLevel() (bool, *token.CompileError) {
	err := l.skipNewlineSpaces()
	if err != nil {
		l.onNewline = false
		return false, err
	}

	if l.curr == eof || l.curr == 0 {
		l.onNewline = false
		return false, nil
	}

	if l.column == 1 {
		l.toDeindent = len(l.indentStack)
		return false, nil
	}

	if len(l.indentStack) == 0 {
		l.indentStack = append(l.indentStack, l.column)
		return true, nil
	}

	if l.column > l.indentStack[len(l.indentStack)-1] {
		// new indentation level
		l.indentStack = append(l.indentStack, l.column)
		return true, nil
	}

	for i := len(l.indentStack) - 1; i >= 0; i-- {
		level := l.indentStack[i]
		if l.column == level {
			// found matching level -> dedent to it
			l.toDeindent = len(l.indentStack) - 1 - i
			return false, nil
		} else if l.column > level {
			return false, &token.CompileError{
				Token: l.createToken(token.ILLEGAL, string(l.curr)),
				Msg:   INDENT_ERR,
			}
		}
	}

	// column in > 1 but does not match any level in the indentStack
	return false, &token.CompileError{
		Token: l.createToken(token.ILLEGAL, string(l.curr)),
		Msg:   INDENT_ERR,
	}
}

func (l *Lexer) skipComment() {
	for l.curr != '\n' {
		if l.curr == eof || l.curr == 0 {
			return
		}
		l.readRune()
	}
}

func (l *Lexer) skipWhitespace() {
	for l.curr == ' ' || l.curr == '\t' || l.curr == '\r' {
		l.readRune()
	}
}

func (l *Lexer) newLine() {
	l.lineOffset++
	l.column = 0
}

func (l *Lexer) readRune() {
	if l.readPosition >= len(l.input) {
		l.curr = 0
	} else {
		l.curr = l.input[l.readPosition]
	}
	l.position = l.readPosition
	l.readPosition++
	l.column++
}

func (l *Lexer) readString() string {
	var out strings.Builder
	for l.curr != '"' && l.curr != 0 {
		if l.curr == '\\' {
			l.readRune()
			switch l.curr {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case '"':
				out.WriteByte('"')
			case '\\':
				out.WriteByte('\\')
			default:
				out.WriteRune(l.curr) // Handle invalid escapes literally
			}
		} else {
			out.WriteRune(l.curr)
		}
		l.readRune()
	}
	return out.String()
}

func (l *Lexer) peekRune() rune {
	if l.readPosition >= len(l.input) {
		return 0
	} else {
		return l.input[l.readPosition]
	}
}

// readIdentifier reads a Unicode identifier from the input.
// it assumes first character (rune) is a letter
func (l *Lexer) readIdentifier() string {
	startPos := l.position
	l.readRune() // Consume first character

	// Read subsequent valid characters (letters, digits, `_`)
	for IsLetterOrDigit(l.curr) {
		l.readRune()
	}

	return string(l.input[startPos:l.position])
}

func (l *Lexer) readNumber() (string, bool) {
	isFloat := false
	position := l.position
	for IsDecimal(l.curr) {
		l.readRune()
	}

	// Check for decimal point
	if l.curr == '.' {
		isFloat = true
		l.readRune()
		// Read fractional part
		for IsDecimal(l.curr) {
			l.readRune()
		}
	}

	return string(l.input[position:l.position]), isFloat
}

// readOperator consumes a maximal sequence of operator characters and returns the combined string.
func (l *Lexer) readOperator() string {
	startPos := l.position
	for IsOperator(l.curr) {
		l.readRune()
	}
	return string(l.input[startPos:l.position])
}

// isLetter checks if a rune is a valid start of an identifier (Unicode letter or `_`).
// this function is optimized and referenced from the implementation in scanner.go of the Go compiler.
// optimization is the if condition that quickly returns for ASCII characters
func IsLetter(ch rune) bool {
	return 'a' <= lower(ch) && lower(ch) <= 'z' || ch == '_' || ch >= utf8.RuneSelf && unicode.IsLetter(ch)
}

// isLetterOrDigit checks if a rune can be part of an identifier (Unicode letter, digit, `_`).
func IsLetterOrDigit(ch rune) bool {
	return IsLetter(ch) || IsDigit(ch)
}

// this function is optimized and referenced from the implementation in scanner.go of the Go compiler.
// optimization is the if condition that quickly returns for ASCII characters
func IsDigit(ch rune) bool {
	return IsDecimal(ch) || ch >= utf8.RuneSelf && unicode.IsDigit(ch)
}

// isOperator returns true if the rune is one of the allowed ASCII operator characters or unicode symbol
func IsOperator(ch rune) bool {
	if ch < 128 {
		// For ASCII, explicitly list allowed operator characters.
		switch ch {
		// Exclude '=' because it's used for assignment or comparisons.
		case '+', '-', '*', '/', '%', '!', '&', '|', '^', '~', '?', '@', '$', '\\':
			return true
		default:
			return false
		}
	}
	// For non-ASCII, allow characters in math symbols, other symbols,
	// currency symbols (Sc), and modifier symbols (Sk).
	return unicode.Is(unicode.Sm, ch) ||
		unicode.Is(unicode.So, ch) ||
		unicode.Is(unicode.Sc, ch) ||
		unicode.Is(unicode.Sk, ch)
}

func IsDecimal(ch rune) bool { return '0' <= ch && ch <= '9' }

func lower(ch rune) rune { return ('a' - 'A') | ch } // returns lower-case ch iff ch is ASCII letter
