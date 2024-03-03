package lexer

import "errors"
import "pluto/token"

type Lexer struct {
    input        []rune
    position     int  // current position in input (points to current rune)
    readPosition int  // current reading position in input (after current rune)
    curr         rune // current rune under examination
    newline      bool // at beginning of new line
    iLevel       []int // indentation level stack
    deindent     int  // number of deindent tokens to be emitted before we continue with current token
}

func New(input string) *Lexer {
    l := &Lexer{input: []rune(input)}
    l.readRune()
    return l
}

func (l *Lexer) NextToken() token.Token {
    var tok token.Token

    if l.newline {
        return l.indentToken()
    } else {
        l.skipWhitespace()
    }

    switch l.curr {
    case '=':
        if l.peekRune() == '=' {
            curr := l.curr
            l.readRune()
            literal := string(curr) + string(l.curr)
            tok = token.Token{Type: token.EQL, Literal: literal}
        } else {
            tok = newToken(token.ASSIGN, l.curr)
        }
    case '+':
        tok = newToken(token.ADD, l.curr)
    case '-':
        tok = newToken(token.SUB, l.curr)
    case '!':
        if l.peekRune() == '=' {
            ch := l.curr
            l.readRune()
            literal := string(ch) + string(l.curr)
            tok = token.Token{Type: token.NEQ, Literal: literal}
        } else {
            tok = newToken(token.NOT, l.curr)
        }
    case '/':
        tok = newToken(token.QUO, l.curr)
    case '*':
        tok = newToken(token.MUL, l.curr)
    case '<':
        tok = newToken(token.LSS, l.curr)
    case '>':
        tok = newToken(token.GTR, l.curr)
    case ',':
        tok = newToken(token.COMMA, l.curr)
    case '(':
        tok = newToken(token.LPAREN, l.curr)
    case ')':
        tok = newToken(token.RPAREN, l.curr)
    case '\n':
        l.newline = true
        tok = newToken(token.NEWLINE, l.curr)
    case 0:
        tok.Literal = ""
        tok.Type = token.EOF
    default:
        if isLetter(l.curr) {
            tok.Literal = l.readIdentifier()
            tok.Type = token.IDENT
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

func (l *Lexer) indentToken() token.Token {
    if l.deindent > 0 {
        return l.deindentToken()
    }

    indent, err := l.indentLevel()

    if err != nil {
        return newToken(token.ILLEGAL, l.curr)
    }

    if l.deindent > 0 {
        return l.deindentToken()
    }

    l.newline = false
    if indent {
        return newToken(token.INDENT, l.curr)
    }

    return l.NextToken()
}

func (l *Lexer) deindentToken() token.Token {
    l.deindent--
    if len(l.iLevel) > 0 {
        l.iLevel = l.iLevel[:len(l.iLevel) - 1]
    }
    if l.deindent == 0 {
        l.newline = false
    }

    return newToken(token.DEINDENT, l.curr)
}

func (l *Lexer) indentLevel() (bool, error) {
    currLevel := 0
    for {
        currLevel = 0
        for l.curr == ' ' {
            l.readRune()
            currLevel++
        }

        if l.curr == '#' {
            l.skipComment()
        }

        if l.curr == '\t' {
            return false, errors.New("indent using tabs not allowed")
        }

        if l.curr != '\n' {
            break
        }
        l.readRune()
    }

    if currLevel == 0 {
        l.deindent = len(l.iLevel)
        return false, nil
    }

    for idx, level := range l.iLevel {
        if currLevel < level {
            return false, errors.New("indent error")
        } else if currLevel == level {
            l.deindent = len(l.iLevel) - 1 - idx
            return false, nil
        }
    }

    ilevelLen := len(l.iLevel)
    if ilevelLen == 0 && currLevel > 0 {
        l.iLevel = append(l.iLevel, currLevel)
        return true, nil
    }

    if currLevel > l.iLevel[ilevelLen - 1] {
        l.iLevel = append(l.iLevel, currLevel)
        return true, nil
    }

    return false, nil
}

func (l *Lexer) skipComment() {
    for l.curr != '\n' {
        l.readRune()
    }
}

func (l *Lexer) skipWhitespace() {
    for l.curr == ' ' || l.curr == '\t' || l.curr == '\r' {
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
