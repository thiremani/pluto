package lexer

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/thiremani/pluto/token"
)

type Lexer struct {
	FileName     string
	input        []rune
	position     int  // current position in input (points to current rune)
	readPosition int  // current reading position in input (after current rune)
	curr         rune // current rune under examination
	lineOffset   int  // line number
	column       int  // column number in the line

	blocks  []int           // columns of the open indented blocks; innermost last
	pending queue           // tokens decided but not yet returned
	last    token.TokenType // the last token lexed; a line ending in a comma continues
}

type lexed struct {
	tok token.Token
	err *token.CompileError
}

// queue holds tokens in the order they are decided. Once drained it reuses
// its buffer, so neither push nor pop copies the tokens waiting in it.
type queue struct {
	items []lexed
	head  int
}

func (q *queue) push(t lexed) {
	q.items = append(q.items, t)
}

// pop returns the next token, valid until the next push, or nil when the
// queue is drained.
func (q *queue) pop() *lexed {
	if q.head == len(q.items) {
		q.items, q.head = q.items[:0], 0
		return nil
	}
	q.head++
	return &q.items[q.head-1]
}

const (
	eof = -1
)

const (
	INDENT_ERR       = "indentation error"
	INDENT_TAB_ERR   = "indent using tabs not allowed"
	INDENT_WIDTH_ERR = "indent each block by 4 spaces; a header's ':' by 2, with one space after it"
)

func New(fileName, input string) *Lexer {
	l := &Lexer{FileName: fileName, input: []rune(input), lineOffset: 1}
	l.readRune()
	l.startLine(l.nextLine())
	return l
}

func (l *Lexer) createToken(tokenType token.TokenType, literal string, hadSpace bool) token.Token {
	tok := l.tokenAt(tokenType, literal, l.column)
	tok.HadSpace = hadSpace
	return tok
}

// tokenAt makes a token at column on the line being read.
func (l *Lexer) tokenAt(tokenType token.TokenType, literal string, column int) token.Token {
	return token.Token{
		FileName: l.FileName,
		Type:     tokenType,
		Literal:  literal,
		Line:     l.lineOffset,
		Column:   column,
	}
}

// NextToken returns the next token. Layout tokens, decided at each line
// break, come first.
func (l *Lexer) NextToken() (token.Token, *token.CompileError) {
	if next := l.pending.pop(); next != nil {
		return next.tok, next.err
	}

	tok, err := l.lex()
	if tok.Type == token.NEWLINE && !l.lineBreak() {
		return l.NextToken()
	}
	l.last = tok.Type
	return tok, err
}

// lineBreak lays out the line after a line break; it is the one place that
// decides layout. It moves past blank and comment lines to the next line. A
// line ending in a comma continues onto that line when it is indented past
// the current block: the break reads as a space and lineBreak reports false.
// Otherwise the break ends a statement or a row, and the next line's own
// layout tokens are queued to follow it.
func (l *Lexer) lineBreak() bool {
	column, first, tab := l.nextLine()
	if l.last == token.COMMA && first != eof && column > l.level() {
		l.tabErr(tab)
		return false
	}
	l.startLine(column, first, tab)
	return true
}

// level returns the column of the innermost open block, or 1 outside blocks.
func (l *Lexer) level() int {
	if len(l.blocks) == 0 {
		return 1
	}
	return l.blocks[len(l.blocks)-1]
}

// tabErr reports a line's first indentation tab, at column tab, if it has
// one.
func (l *Lexer) tabErr(tab int) bool {
	if tab == 0 {
		return false
	}
	bad := l.tokenAt(token.ILLEGAL, "\t", tab)
	l.pending.push(lexed{bad, &token.CompileError{Token: bad, Msg: INDENT_TAB_ERR}})
	return true
}

// startLine queues the layout tokens of the line about to be read, whose
// content starts with first at column. A tab in its indentation is reported
// and the line stays at the current level. Otherwise the line opens a block
// 4 spaces deeper (another depth is reported, and the block opens anyway),
// returns to an enclosing block (a level none has is reported), or stays. A
// header's ':' hangs 2 spaces left of its block.
func (l *Lexer) startLine(column int, first rune, tab int) {
	if l.tabErr(tab) || first == eof {
		return
	}

	level := column
	if first == ':' {
		level += 2
	}
	if level == 1 {
		l.dedentTo(0, column, first)
		return
	}
	if base := l.level(); level > base {
		at := l.tokenAt(token.INDENT, string(first), column)
		var err *token.CompileError
		if level != base+4 {
			err = &token.CompileError{Token: at, Msg: INDENT_WIDTH_ERR}
		}
		l.blocks = append(l.blocks, level)
		l.pending.push(lexed{at, err})
		return
	}
	for i := len(l.blocks) - 1; i >= 0 && level <= l.blocks[i]; i-- {
		if level == l.blocks[i] {
			l.dedentTo(i+1, column, first)
			return
		}
	}
	bad := l.tokenAt(token.ILLEGAL, string(first), column)
	l.pending.push(lexed{bad, &token.CompileError{Token: bad, Msg: INDENT_ERR + ". At char: " + string(first)}})
}

// dedentTo leaves every block above the first n, with a DEINDENT for each at
// the line's first token, first at column. A line that leaves no block makes
// no token.
func (l *Lexer) dedentTo(n, column int, first rune) {
	if len(l.blocks) <= n {
		return
	}
	at := l.tokenAt(token.DEINDENT, string(first), column)
	for len(l.blocks) > n {
		l.blocks = l.blocks[:len(l.blocks)-1]
		l.pending.push(lexed{at, nil})
	}
}

func (l *Lexer) lex() (token.Token, *token.CompileError) {
	var tok token.Token
	var err *token.CompileError

	hadSpace := l.skipWhitespace()

	if l.curr == '#' {
		l.skipComment()
	}

	switch l.curr {
	case '\n':
		tok = l.createToken(token.NEWLINE, token.SYM_NEWLINE, hadSpace)
	case '"':
		tok = l.createToken(token.STRING, token.SYM_DQUOTE, hadSpace)
		l.readRune()
		tok.Literal, err = l.readString(tok)
	case ':':
		tok = l.createToken(token.COLON, token.SYM_COLON, hadSpace)
	case ',':
		tok = l.createToken(token.COMMA, token.SYM_COMMA, hadSpace)
	case '(':
		tok = l.createToken(token.LPAREN, token.SYM_LPAREN, hadSpace)
	case ')':
		tok = l.createToken(token.RPAREN, token.SYM_RPAREN, hadSpace)
	case '[':
		tok = l.createToken(token.LBRACK, token.SYM_LBRACK, hadSpace)
	case ']':
		tok = l.createToken(token.RBRACK, token.SYM_RBRACK, hadSpace)
	case '.':
		// Float literals can start with '.' (e.g. .5)
		if IsDecimal(l.peekRune()) {
			tok = l.createToken(token.FLOAT, "", hadSpace)
			tok.Literal, _ = l.readNumber()
			return tok, nil
		}
		tok = l.createToken(token.PERIOD, token.SYM_PERIOD, hadSpace)
	case 0:
		if !l.atEOF() {
			tok = l.createToken(token.ILLEGAL, string(l.curr), hadSpace)
			err = &token.CompileError{Token: tok, Msg: "NUL character is not allowed in source"}
			break
		}
		fallthrough
	case eof:
		tok = l.createToken(token.EOF, "", hadSpace)
	case '=':
		if l.peekRune() == '=' {
			tok = l.createToken(token.EQL, token.SYM_EQL, hadSpace)
			l.readRune()
		} else {
			tok = l.createToken(token.ASSIGN, token.SYM_ASSIGN, hadSpace)
		}
	case '<':
		if l.peekRune() == '=' {
			tok = l.createToken(token.LEQ, token.SYM_LEQ, hadSpace)
			l.readRune()
		} else if l.peekRune() == '<' {
			tok = l.createToken(token.OPERATOR, token.SYM_SHL, hadSpace)
			l.readRune()
		} else {
			tok = l.createToken(token.LSS, token.SYM_LSS, hadSpace)
		}
	case '>':
		if l.peekRune() == '=' {
			tok = l.createToken(token.GEQ, token.SYM_GEQ, hadSpace)
			l.readRune()
		} else if l.peekRune() == '>' {
			l.readRune()
			if l.peekRune() == '>' {
				tok = l.createToken(token.OPERATOR, token.SYM_SHR, hadSpace)
				l.readRune()
			} else {
				tok = l.createToken(token.OPERATOR, token.SYM_ASR, hadSpace)
			}
		} else {
			tok = l.createToken(token.GTR, token.SYM_GTR, hadSpace)
		}
	case '!':
		if l.peekRune() == '=' {
			tok = l.createToken(token.NEQ, token.SYM_NEQ, hadSpace)
			l.readRune()
			l.readRune()
			return tok, nil
		}
		fallthrough
	default:
		if IsLetter(l.curr) {
			tok = l.createToken(token.IDENT, "", hadSpace)
			tok.Literal = l.readIdentifier()
			return tok, nil
		} else if IsDecimal(l.curr) {
			tok = l.createToken(token.INT, "", hadSpace)
			var isFloat bool
			tok.Literal, isFloat = l.readNumber()
			if isFloat {
				tok.Type = token.FLOAT
			}
			return tok, nil
		} else if IsOperator(l.curr) {
			// Read a maximal sequence of operator characters.
			tok = l.createToken(token.OPERATOR, "", hadSpace)
			tok.Literal = l.readOperator()
			return tok, nil
		} else {
			ch := string(l.curr)
			tok = l.createToken(token.ILLEGAL, ch, hadSpace)
			err = &token.CompileError{
				Token: tok,
				Msg:   "Illegal character '" + ch + "'",
			}
		}
	}

	l.readRune()
	return tok, err
}

// nextLine moves past blank and comment lines to the start of the next line
// with a token and returns that line's indentation, which it leaves unread.
// Tabs on blank and comment lines are not checked. A comment that a NUL
// character ends leaves the rest of its line to the lexer, so that line is
// one with a token.
func (l *Lexer) nextLine() (column int, first rune, tab int) {
	for {
		column, first, tab = l.indentation()
		if first != '\n' && first != '#' {
			return column, first, tab
		}
		l.skipComment()
		if l.atEOF() {
			return column, eof, 0
		}
		if l.curr != '\n' {
			return column, first, tab
		}
		l.readRune()
	}
}

// indentation scans the leading spaces and tabs of the line being read
// without consuming them. It returns the column where the line's content
// starts, the rune there, and the column of the line's first tab, or 0 for
// none. At the end of input the rune is eof and the tab 0.
func (l *Lexer) indentation() (column int, first rune, tab int) {
	// Within a line, position and column advance together, so the line
	// starts column-1 runes before position.
	i := l.position - l.column + 1
	column = 1
	for ; i < len(l.input) && (l.input[i] == ' ' || l.input[i] == '\t'); i++ {
		if l.input[i] == '\t' && tab == 0 {
			tab = column
		}
		column++
	}
	if i >= len(l.input) {
		return column, eof, 0
	}
	first, _ = LogicalRune(l.input, i)
	return column, first, tab
}

// skipComment moves to the end of the line. A NUL character ends the comment
// early, so the lexer reports it.
func (l *Lexer) skipComment() {
	for l.curr != '\n' {
		if l.curr == eof || l.curr == 0 {
			return
		}
		l.readRune()
	}
}

// skipWhitespace moves past spaces and tabs and reports whether the next
// token is apart from the previous one; a line break before it counts as
// space.
func (l *Lexer) skipWhitespace() bool {
	hadSpace := l.column == 1
	for l.curr == ' ' || l.curr == '\t' {
		hadSpace = true
		l.readRune()
	}
	return hadSpace
}

// LogicalRune returns the logical rune at raw index i and the raw index
// just past it: a CRLF pair is one logical '\n' spanning two raw runes,
// a lone CR is '\n'. All raw-source walkers share this primitive.
func LogicalRune(raw []rune, i int) (rune, int) {
	r := raw[i]
	if r != '\r' {
		return r, i + 1
	}
	if i+1 < len(raw) && raw[i+1] == '\n' {
		return '\n', i + 2
	}
	return '\n', i + 1
}

// readRune advances to the next logical rune via LogicalRune; l.input,
// position, and readPosition hold raw decoded runes and rune indexes
// (not byte offsets). Leaving a logical newline advances the line count
// here, at the single point of consumption.
func (l *Lexer) readRune() {
	if l.curr == '\n' {
		l.lineOffset++
		l.column = 0
	}
	if l.readPosition >= len(l.input) {
		l.curr = 0
		l.position = l.readPosition
		l.readPosition++
		l.column++
		return
	}
	l.position = l.readPosition
	l.curr, l.readPosition = LogicalRune(l.input, l.readPosition)
	l.column++
}

func (l *Lexer) atEOF() bool {
	return l.curr == 0 && l.position >= len(l.input)
}

func (l *Lexer) readString(tok token.Token) (string, *token.CompileError) {
	var firstErr *token.CompileError
	start := l.position
	setError := func(msg string) {
		if firstErr == nil {
			firstErr = &token.CompileError{Token: tok, Msg: msg}
		}
	}

	for l.curr != '"' && !l.atEOF() {
		if l.curr == 0 {
			setError("NUL character is not allowed in string literals")
			l.readRune()
			continue
		}
		if l.curr == '\\' {
			_, next, escapeErr := DecodeStringEscape(l.input, l.position)
			if escapeErr != nil {
				setError(escapeErr.Error())
			}
			// next is a raw index, so advance until the raw readPosition
			// reaches it; a CRLF pair straddling next is consumed whole.
			for l.readPosition < next {
				l.readRune()
			}
		}
		l.readRune()
	}
	if l.atEOF() {
		setError("unterminated string literal")
	}
	return string(l.input[start:l.position]), firstErr
}

// DecodeStringEscape decodes the escape beginning at start and returns its
// runtime bytes and the first source index after it.
func DecodeStringEscape(raw []rune, start int) (string, int, error) {
	if start+1 >= len(raw) {
		return `\`, start + 1, fmt.Errorf("incomplete escape sequence")
	}

	escaped := raw[start+1]
	switch escaped {
	case 'n':
		return "\n", start + 2, nil
	case 't':
		return "\t", start + 2, nil
	case 'r':
		return "\r", start + 2, nil
	case 'b':
		return "\b", start + 2, nil
	case 'f':
		return "\f", start + 2, nil
	case '"':
		return `"`, start + 2, nil
	case '\\':
		return `\`, start + 2, nil
	case '-', '%':
		return string(escaped), start + 2, nil
	case 'x':
		return decodeByteEscape(raw, start)
	case 'u':
		return decodeUnicodeEscape(raw, start, 4)
	case 'U':
		return decodeUnicodeEscape(raw, start, 8)
	case 0:
		return string(escaped), start + 2, fmt.Errorf("NUL character is not allowed in string literals")
	case '0':
		return string(escaped), start + 2, fmt.Errorf(`NUL escape \0 is not supported`)
	case '\n', '\r':
		// Report the logical newline and consume the full break, so the
		// diagnostic and next are the same for LF, CRLF, and CR sources.
		_, next := LogicalRune(raw, start+1)
		return "\n", next, fmt.Errorf("unsupported escape sequence \\\n")
	default:
		return string(escaped), start + 2, fmt.Errorf(`unsupported escape sequence \%c`, escaped)
	}
}

func decodeFixedHexValue(raw []rune, start, digits int) (uint32, int, error) {
	prefix := raw[start+1]
	digitStart := start + 2
	end := digitStart + digits
	if end > len(raw) {
		return 0, len(raw), fmt.Errorf(`invalid \%c escape: expected exactly %d hexadecimal digits`, prefix, digits)
	}

	var value uint32
	for i := digitStart; i < end; i++ {
		digit, ok := hexDigitValue(raw[i])
		if !ok {
			if raw[i] == 0 {
				return 0, i, fmt.Errorf("NUL character is not allowed in string literals")
			}
			return 0, i, fmt.Errorf(`invalid \%c escape: expected exactly %d hexadecimal digits`, prefix, digits)
		}
		value = value<<4 | uint32(digit)
	}
	return value, end, nil
}

func decodeByteEscape(raw []rune, start int) (string, int, error) {
	value, end, err := decodeFixedHexValue(raw, start, 2)
	if err != nil {
		return "x", end, err
	}
	escape := string(raw[start:end])
	if value == 0 {
		return "x", end, fmt.Errorf("NUL escape %s is not supported", escape)
	}
	return string([]byte{byte(value)}), end, nil
}

func decodeUnicodeEscape(raw []rune, start, digits int) (string, int, error) {
	value, end, err := decodeFixedHexValue(raw, start, digits)
	if err != nil {
		return string(raw[start+1]), end, err
	}
	escape := string(raw[start:end])
	if value == 0 {
		return string(raw[start+1]), end, fmt.Errorf("NUL escape %s is not supported", escape)
	}
	if value > 0x10ffff {
		return string(raw[start+1]), end, fmt.Errorf("invalid Unicode escape %s: code point exceeds U+10FFFF", escape)
	}
	if 0xd800 <= value && value <= 0xdfff {
		return string(raw[start+1]), end, fmt.Errorf("invalid Unicode escape %s: surrogate code points are not supported", escape)
	}
	return string(rune(value)), end, nil
}

// DecodeStringLiteral converts lexer-validated raw string contents to their
// runtime value.
func DecodeStringLiteral(raw string) string {
	runes := []rune(raw)
	var out strings.Builder
	for i := 0; i < len(runes); {
		if runes[i] == '\\' {
			value, next, _ := DecodeStringEscape(runes, i)
			out.WriteString(value)
			i = next
			continue
		}
		// A physical line break decodes to '\n' so runtime values do not
		// depend on checkout line endings; the \r escape is unaffected.
		r, next := LogicalRune(runes, i)
		out.WriteRune(r)
		i = next
	}
	return out.String()
}

func hexDigitValue(ch rune) (byte, bool) {
	switch {
	case '0' <= ch && ch <= '9':
		return byte(ch - '0'), true
	case 'a' <= ch && ch <= 'f':
		return byte(ch-'a') + 10, true
	case 'A' <= ch && ch <= 'F':
		return byte(ch-'A') + 10, true
	default:
		return 0, false
	}
}

// peekRune returns the next logical rune without advancing; like readRune
// it presents a raw CR as '\n'.
func (l *Lexer) peekRune() rune {
	if l.readPosition >= len(l.input) {
		return 0
	}
	r, _ := LogicalRune(l.input, l.readPosition)
	return r
}

// readIdentifier reads a Unicode identifier from the input.
// It assumes the first rune is a valid identifier start.
func (l *Lexer) readIdentifier() string {
	startPos := l.position
	l.readRune() // Consume first character

	// Read subsequent valid characters (letters, digits, combining marks, `_`)
	for IsLetterOrDigit(l.curr) {
		l.readRune()
	}

	return string(l.input[startPos:l.position])
}

func (l *Lexer) readNumber() (string, bool) {
	position := l.position

	if l.curr == '0' {
		switch l.peekRune() {
		case 'b':
			l.readRune()
			l.readRune()
			l.readBaseLiteralTail(numberBaseBinary)
			return string(l.input[position:l.position]), false
		case 'o':
			l.readRune()
			l.readRune()
			l.readBaseLiteralTail(numberBaseOctal)
			return string(l.input[position:l.position]), false
		case 'x':
			l.readRune()
			l.readRune()
			l.readBaseLiteralTail(numberBaseHex)
			return string(l.input[position:l.position]), false
		}
	}

	l.readDigitsAndSeparators(numberBaseDecimal)

	if l.curr == '.' {
		l.readRune()
		l.readDigitsAndSeparators(numberBaseDecimal)
		return string(l.input[position:l.position]), true
	}

	return string(l.input[position:l.position]), false
}

type numberBase int

const (
	numberBaseDecimal numberBase = iota
	numberBaseBinary
	numberBaseOctal
	numberBaseHex
)

func (l *Lexer) readBaseLiteralTail(base numberBase) {
	l.readDigitsAndSeparators(base)
	// Keep malformed based literals as one token: `0b01556` should fail as a
	// single bad integer literal, not lex as `0b01` followed by `556`.
	l.readInvalidBaseDigitTail(base)
}

func (l *Lexer) readInvalidBaseDigitTail(base numberBase) {
	for {
		if isInvalidDigitForBase(l.curr, base) {
			l.readRune()
			continue
		}
		if l.curr == '\'' && isInvalidDigitForBase(l.peekRune(), base) {
			l.readRune()
			continue
		}
		return
	}
}

func isInvalidDigitForBase(ch rune, base numberBase) bool {
	return IsDecimal(ch) && !isDigitForBase(ch, base)
}

func (l *Lexer) readDigitsAndSeparators(base numberBase) {
	for {
		if isDigitForBase(l.curr, base) {
			l.readRune()
			continue
		}
		if l.curr == '\'' && isDigitForBase(l.peekRune(), base) {
			l.readRune()
			continue
		}
		return
	}
}

func isDigitForBase(ch rune, base numberBase) bool {
	switch base {
	case numberBaseBinary:
		return ch == '0' || ch == '1'
	case numberBaseOctal:
		return '0' <= ch && ch <= '7'
	case numberBaseDecimal:
		return IsDecimal(ch)
	case numberBaseHex:
		return IsDecimal(ch) || 'a' <= lower(ch) && lower(ch) <= 'f'
	default:
		return false
	}
}

// readOperator consumes a maximal sequence of operator characters and returns the combined string.
func (l *Lexer) readOperator() string {
	startPos := l.position
	for IsOperator(l.curr) {
		l.readRune()
	}
	return string(l.input[startPos:l.position])
}

// IsLetter checks if a rune is a valid start of an identifier (Unicode letter or `_`).
// This function is optimized and referenced from the implementation in scanner.go of the Go compiler.
func IsLetter(ch rune) bool {
	return 'a' <= lower(ch) && lower(ch) <= 'z' || ch == '_' || ch >= utf8.RuneSelf && unicode.IsLetter(ch)
}

// IsLetterOrDigit checks if a rune can be part of an identifier
// (Unicode letter, digit, combining mark, or `_`).
// Combining marks (Mn, Mc, Me) are allowed after any identifier character.
// This is inspired by UAX #31 but is a simplified subset, not full XID_Continue.
func IsLetterOrDigit(ch rune) bool {
	if IsLetter(ch) || IsDigit(ch) {
		return true
	}
	if ch < utf8.RuneSelf {
		return false
	}
	return unicode.Is(unicode.Mn, ch) || unicode.Is(unicode.Mc, ch) || unicode.Is(unicode.Me, ch)
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
		case '+', '-', '*', '/', '%', '!', '&', '|', '^', '~', '?', '@', '$':
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
