package lexer

import "pluto/token"

type Lexer struct {
	input        []rune
	position     int  // current position in input (points to current rune)
	readPosition int  // current reading position in input (after current rune)
	curr         rune // current rune under examination
}

func New(input string) *Lexer {
	l := &Lexer{input: []rune(input)}
	l.readRune()
	return l
}

func (l *Lexer) NextToken() token.Token {
	var tok token.Token

	l.skipWhitespace()

	switch l.curr {
	case '=':
		if l.peekRune() == '=' {
			curr := l.curr
			l.readRune()
			literal := string(curr) + string(l.curr)
			tok = token.Token{Type: token.EQ, Literal: literal}
		} else {
			tok = newToken(token.ASSIGN, l.curr)
		}
	case '+':
		tok = newToken(token.PLUS, l.curr)
	case '-':
		tok = newToken(token.MINUS, l.curr)
	case '!':
		if l.peekRune() == '=' {
			ch := l.curr
			l.readRune()
			literal := string(ch) + string(l.curr)
			tok = token.Token{Type: token.NOT_EQ, Literal: literal}
		} else {
			tok = newToken(token.BANG, l.curr)
		}
	case '/':
		tok = newToken(token.SLASH, l.curr)
	case '*':
		tok = newToken(token.ASTERISK, l.curr)
	case '<':
		tok = newToken(token.LT, l.curr)
	case '>':
		tok = newToken(token.GT, l.curr)
	case ',':
		tok = newToken(token.COMMA, l.curr)
	case '(':
		tok = newToken(token.LPAREN, l.curr)
	case ')':
		tok = newToken(token.RPAREN, l.curr)
	case 0:
		tok.Literal = ""
		tok.Type = token.EOF
	default:
		if isLetter(l.curr) {
			tok.Literal = l.readIdentifier()
			tok.Type = token.LookupIdent(tok.Literal)
			return tok
		} else if isDigit(l.curr) {
			tok.Type = token.INT
			tok.Literal = l.readNumber()
			return tok
		} else {
			tok = newToken(token.ILLEGAL, l.curr)
		}	
	}

	l.readRune()
	return tok
}

func (l *Lexer) skipWhitespace() {
	for l.curr == ' ' || l.curr == '\t' || l.curr == '\n' || l.curr == '\r' {
		l.readRune()
	}
}

func (l *Lexer) readRune() {
	if l.readPosition >= len(l.input) {
		l.curr = 0
	} else {
		l.curr = l.input[l.readPosition]
	}
	l.position = l.readPosition
	l.readPosition++
}

func (l *Lexer) peekRune() rune {
	if l.readPosition >= len(l.input) {
		return 0
	} else {
		return l.input[l.readPosition]
	}
}

func (l *Lexer) readIdentifier() string {
	position := l.position
	for isLetter(l.curr) {
		l.readRune()
	}
	return string(l.input[position:l.position])
}

func (l *Lexer) readNumber() string {
	position := l.position
	for isDigit(l.curr) {
		l.readRune()
	}
	return string(l.input[position:l.position])
}

func isLetter(ch rune) bool {
	return 'a' <= ch && ch <= 'z' || 'A' <= ch && ch <= 'Z' || ch == '_'
}

func isDigit(ch rune) bool {
	return '0' <= ch && ch <= '9'
}

func newToken(tokenType token.TokenType, curr rune) token.Token {
	return token.Token{Type: tokenType, Literal: string(curr)}
}
