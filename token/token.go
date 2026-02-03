package token

import (
	"fmt"
	"strconv"
	"strings"
)

type TokenType int

const (
	// Special tokens.
	ILLEGAL TokenType = iota
	EOF
	COMMENT

	// Literal tokens.
	literal_beg
	IDENT // add, foobar, x, y, ...
	const_beg
	INT    // 1343456
	FLOAT  // 123.45
	IMAG   // 123.45i
	RUNE        // 'a'
	STRING      // "abc" (static/global string)
	HEAP_STRING // "abc". (heap-allocated string)
	const_end
	literal_end

	// Operator and punctuation tokens.
	operator_beg
	ASSIGN   // =
	OPERATOR // generic operator for arithmetic etc.
	LPAREN   // (
	LBRACK   // [
	LBRACE   // {
	COMMA    // ,
	PERIOD   // .
	RPAREN   // )
	RBRACK   // ]
	RBRACE   // }
	operator_end

	// Comparison tokens.
	comparison_beg
	EQL // ==
	LSS // <
	GTR // >

	NEQ // !=
	LEQ // <=
	GEQ // >=

	COLON // : is if we want to loop from 0:n. For eg: y += 0:n x
	comparison_end

	// Other tokens.
	NEWLINE
	BACKSLASH
	INDENT
	DEINDENT
)

const (
	// prefixes
	SYM_BANG   = "!"
	SYM_ASSIGN = "="

	// root symbols
	SYM_SQRT  = "√" // square root (U+221A)
	SYM_CBRT  = "∛" // cube root (U+221B)
	SYM_FTHRT = "∜" // fourth root (U+221C)

	// arithmetic symbols
	SYM_ADD      = "+"
	SYM_SUB      = "-"
	SYM_MUL      = "*"
	SYM_IMPL_MUL = "⋅" // implicit multiplication (e.g., 2x) - higher precedence than regular *
	SYM_DIV      = "/"
	SYM_QUO      = "÷"
	SYM_MOD      = "%"
	SYM_EXP      = "^"

	// comparison symbols
	SYM_EQL = "=="
	SYM_LSS = "<"
	SYM_GTR = ">"

	SYM_NEQ = "!="
	SYM_LEQ = "<="
	SYM_GEQ = ">="

	// bitwise symbols
	SYM_AND   = "&"
	SYM_OR    = "|"
	SYM_XOR   = "⊻"
	SYM_TILDE = "~"

	// array concatenation
	SYM_CONCAT = "⊕"

	// shift symbols
	SYM_SHL = "<<"
	SYM_ASR = ">>"
	SYM_SHR = ">>>"

	// punctuation symbols
	SYM_LPAREN = "("
	SYM_LBRACK = "["
	SYM_LBRACE = "{"
	SYM_COMMA  = ","
	SYM_PERIOD = "."
	SYM_COLON  = ":"
	SYM_RPAREN = ")"
	SYM_RBRACK = "]"
	SYM_RBRACE = "}"

	SYM_DQUOTE    = "\""
	SYM_SQUOTE    = "'"
	SYM_ACCENT    = "`"
	SYM_BACKSLASH = "\\"
	SYM_NEWLINE   = "\n"
	SYM_TAB       = "\t"

	SYM_COMMENT = "#"
)

const (
	STR_ILLEGAL  = "ILLEGAL"
	STR_EOF      = "EOF"
	STR_COMMENT  = "COMMENT"
	STR_IDENT    = "IDENT"
	STR_INT      = "INT"
	STR_FLOAT    = "FLOAT"
	STR_IMAG     = "IMAG"
	STR_RUNE        = "RUNE"
	STR_STRING      = "STRING"
	STR_HEAP_STRING = "HEAP_STRING"
	STR_OPERATOR    = "OPERATOR"
	STR_INDENT   = "INDENT"
	STR_DEINDENT = "DEINDENT"
)

var tokens = [...]string{
	ILLEGAL: STR_ILLEGAL,

	EOF:     STR_EOF,
	COMMENT: STR_COMMENT,

	IDENT:  STR_IDENT,
	INT:    STR_INT,
	FLOAT:  STR_FLOAT,
	IMAG:   STR_IMAG,
	RUNE:        STR_RUNE,
	STRING:      STR_STRING,
	HEAP_STRING: STR_HEAP_STRING,

	OPERATOR: STR_OPERATOR,

	ASSIGN: SYM_ASSIGN,
	LPAREN: SYM_LPAREN,
	LBRACK: SYM_LBRACK,
	LBRACE: SYM_LBRACE,
	COMMA:  SYM_COMMA,
	PERIOD: SYM_PERIOD,
	COLON:  SYM_COLON, // COLON as punctuation
	RPAREN: SYM_RPAREN,
	RBRACK: SYM_RBRACK,
	RBRACE: SYM_RBRACE,

	EQL: SYM_EQL,
	LSS: SYM_LSS,
	GTR: SYM_GTR,

	NEQ: SYM_NEQ,
	LEQ: SYM_LEQ,
	GEQ: SYM_GEQ,

	NEWLINE:  SYM_NEWLINE,
	INDENT:   STR_INDENT,
	DEINDENT: STR_DEINDENT,
}

type Token struct {
	FileName string
	Type     TokenType
	Literal  string
	Line     int
	Column   int
	HadSpace bool // true if there was any space before this token
}

type CompileErrors []*CompileError

func (t Token) IsComparison() bool {
	return comparison_beg < t.Type && comparison_end > t.Type
}

func (t Token) IsConstant() bool {
	return const_beg < t.Type && const_end > t.Type
}

func (tokenType TokenType) String() string {
	s := ""
	if 0 <= tokenType && tokenType < TokenType(len(tokens)) {
		s = tokens[tokenType]
	}

	if s == "" {
		s = "token(" + strconv.Itoa(int(tokenType)) + ")"
	}

	return s
}

// TokenTypeWithOp returns token type string if it is not an operator
// if it is an operator then it returns the operator literal
func (t Token) TokenTypeWithOp() string {
	if t.Type == OPERATOR {
		return t.Literal
	}
	return t.Type.String()
}

type CompileError struct {
	Token Token
	Msg   string
}

func (ce *CompileError) Error() string {
	// core location prefix
	prefix := fmt.Sprintf("%d:%d", ce.Token.Line, ce.Token.Column)
	if ce.Token.FileName != "" {
		prefix = fmt.Sprintf("%s:%s", ce.Token.FileName, prefix)
	}
	// message, optionally quoting the literal
	return fmt.Sprintf("%s:%s", prefix, ce.Msg)
}

func (ces CompileErrors) String() string {
	var b strings.Builder
	for _, e := range ces {
		b.WriteString(e.Error())
		b.WriteRune('\n')
	}
	return b.String()
}
