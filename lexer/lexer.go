package lexer

import (
	"pluto/token"
    "strings"
)

type Lexer struct {
    input          []rune
    position       int  // current position in input (points to current rune)
    readPosition   int  // current reading position in input (after current rune)
    curr           rune // current rune under examination
    lineOffset     int  // line number
    column         int  // column number in the line
    onNewline      bool // at beginning of new line
    indentStack    []int // indentation level stack
    toDeindent     int  // number of deindent tokens to be emitted before we continue with current token
}

const (
    eof = -1
)

const (
    INDENT_ERR = "indentation error"
    INDENT_TAB_ERR = "indent using tabs not allowed"
)

func New(input string) *Lexer {
    l := &Lexer{input: []rune(input), lineOffset: 1, onNewline: true}
    l.readRune()
    return l
}

func (l *Lexer) createToken(tokenType token.TokenType, literal string) token.Token {
    return token.Token {
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

    switch l.curr {
    case '=':
        if l.peekRune() == '=' {
            tok = l.createToken(token.EQL, "")
            curr := l.curr
            l.readRune()
            tok.Literal = string(curr) + string(l.curr)
        } else {
            tok = l.createToken(token.ASSIGN, string(l.curr))
        }
    case '+':
        tok = l.createToken(token.ADD, string(l.curr))
    case '-':
        tok = l.createToken(token.SUB, string(l.curr))
    case '!':
        if l.peekRune() == '=' {
            tok = l.createToken(token.NEQ, "")
            ch := l.curr
            l.readRune()
            tok.Literal = string(ch) + string(l.curr)
        } else {
            tok = l.createToken(token.NOT, string(l.curr))
        }
    case '/':
        tok = l.createToken(token.QUO, string(l.curr))
    case '*':
        tok = l.createToken(token.MUL, string(l.curr))
    case ':':
        tok = l.createToken(token.COLON, string(l.curr))
    case '<':
        tok = l.createToken(token.LSS, string(l.curr))
    case '>':
        tok = l.createToken(token.GTR, string(l.curr))
    case ',':
        tok = l.createToken(token.COMMA, string(l.curr))
    case '(':
        tok = l.createToken(token.LPAREN, string(l.curr))
    case ')':
        tok = l.createToken(token.RPAREN, string(l.curr))
    case '\n':
        tok = l.createToken(token.NEWLINE, string(l.curr))
        l.newLine()
        l.onNewline = true
    case '"':
        tok = l.createToken(token.STRING, "")
        l.readRune()
        tok.Literal = l.readString()
    case 0:
        fallthrough
    case eof:
        tok = l.createToken(token.EOF, "")
    default:
        if isLetter(l.curr) {
            tok = l.createToken(token.IDENT, "")
            tok.Literal = l.readIdentifier()
            return tok, nil
        } else if isDigit(l.curr) || (l.curr == '.' && isDigit(l.peekRune())) {
            tok = l.createToken(token.INT, "")
            var isFloat bool
            tok.Literal, isFloat = l.readNumber()
            if isFloat {
                tok.Type = token.FLOAT
            }
            return tok, nil
        } else {
            tok = l.createToken(token.ILLEGAL, string(l.curr))
            err = &token.CompileError {
                Token: tok,
                Msg:   "Illegal character '" + string(l.curr) + "'",
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
        l.indentStack = l.indentStack[:len(l.indentStack) - 1]
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

    for idx, level := range l.indentStack {
        if l.column < level {
            return false, &token.CompileError {
                Token: l.createToken(token.ILLEGAL, string(l.curr)),
                Msg:   INDENT_ERR,
            }
        } else if l.column == level {
            l.toDeindent = len(l.indentStack) - 1 - idx
            return false, nil
        }

        if idx == len(l.indentStack) - 1 {
            l.indentStack = append(l.indentStack, l.column)
            return true, nil
        }
    }

    return false, nil
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
            case 'n': out.WriteByte('\n')
            case 't': out.WriteByte('\t')
            case '"': out.WriteByte('"')
            case '\\': out.WriteByte('\\')
            default: out.WriteRune(l.curr) // Handle invalid escapes literally
            }
        } else {
            out.WriteRune(l.curr)
        }
        l.readRune()
    }
    l.readRune() // Skip closing "
    return out.String()
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

func (l *Lexer) readNumber() (string, bool) {
    isFloat := false
    position := l.position
    for isDigit(l.curr) {
        l.readRune()
    }

    // Check for decimal point
    if l.curr == '.' {
        isFloat = true
        l.readRune()
        // Read fractional part
        for isDigit(l.curr) {
            l.readRune()
        }
    }

    return string(l.input[position:l.position]), isFloat
}

func isLetter(ch rune) bool {
    return 'a' <= ch && ch <= 'z' || 'A' <= ch && ch <= 'Z' || ch == '_'
}

func isDigit(ch rune) bool {
    return '0' <= ch && ch <= '9'
}