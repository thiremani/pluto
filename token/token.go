package token

import "strconv"

type TokenType int

const (
	ILLEGAL = iota
	EOF
	COMMENT

	literal_beg
	// Identifiers + literals
	IDENT  // add, foobar, x, y, ...
	INT    // 1343456
	FLOAT  // 123.45
	IMAG   // 123.45i
	RUNE   // 'a'
	STRING // "abc"
	literal_end

	operator_beg
	// Operators and delimiters
	ASSIGN // =
	NOT    // !

	ADD // +
	SUB // -
	MUL // *
	QUO // /
	REM // %

	AND     // &
	OR      // |
	XOR     // ^
	SHL     // <<
	SHR     // >>
	AND_NOT // &^

	ADD_ASSIGN // +=
	SUB_ASSIGN // -=
	MUL_ASSIGN // *=
	QUO_ASSIGN // /=
	REM_ASSIGN // %=

	AND_ASSIGN     // &=
	OR_ASSIGN      // |=
	XOR_ASSIGN     // ^=
	SHL_ASSIGN     // <<=
	SHR_ASSIGN     // >>=
	AND_NOT_ASSIGN // &^=

	LPAREN // (
	LBRACK // [
	LBRACE // {
	COMMA  // ,
	PERIOD // .

	RPAREN // )
	RBRACK // ]
	RBRACE // }
	operator_end

	comparison_beg
	EQL // ==
	LSS // <
	GTR // >

	NEQ // !=
	LEQ // <=
	GEQ // >=
	COLON
	comparison_end

	NEWLINE
	INDENT
	DEINDENT
)

var tokens = [...]string{
	ILLEGAL: "ILLEGAL",

	EOF:     "EOF",
	COMMENT: "COMMENT",

	IDENT:  "IDENT",
	INT:    "INT",
	FLOAT:  "FLOAT",
	IMAG:   "IMAG",
	RUNE:   "RUNE",
	STRING: "STRING",

	ASSIGN: "=",
	NOT:    "!",

	ADD: "+",
	SUB: "-",
	MUL: "*",
	QUO: "/",
	REM: "%",

	AND:     "&",
	OR:      "|",
	XOR:     "^",
	SHL:     "<<",
	SHR:     ">>",
	AND_NOT: "&^",

	ADD_ASSIGN: "+=",
	SUB_ASSIGN: "-=",
	MUL_ASSIGN: "*=",
	QUO_ASSIGN: "/=",
	REM_ASSIGN: "%=",

	AND_ASSIGN:     "&=",
	OR_ASSIGN:      "|=",
	XOR_ASSIGN:     "^=",
	SHL_ASSIGN:     "<<=",
	SHR_ASSIGN:     ">>=",
	AND_NOT_ASSIGN: "&^=",

	COLON: ":",

	LPAREN: "(",
	LBRACK: "[",
	LBRACE: "{",
	COMMA:  ",",
	PERIOD: ".",

	RPAREN: ")",
	RBRACK: "]",
	RBRACE: "}",

	EQL: "==",
	LSS: "<",
	GTR: ">",

	NEQ: "!=",
	LEQ: "<=",
	GEQ: ">=",

	NEWLINE:  "\n",
	INDENT:   "INDENT",
	DEINDENT: "DEINDENT",
}

type Token struct {
	Type    TokenType
	Literal string
	Line    int
	Column  int
}

func (t Token) IsComparison() bool {
	return comparison_beg < t.Type && comparison_end > t.Type
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

type CompileError struct {
	Token Token
	Msg   string
}

func (ce *CompileError) Error() string {
	return strconv.Itoa(ce.Token.Line) + ":" + strconv.Itoa(ce.Token.Column) + ":" + ce.Token.Literal + ":" + ce.Msg
}
