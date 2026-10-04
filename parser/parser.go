package parser

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/token"
)

// Precedence levels - using floats to allow inserting any intermediate level
const (
	LOWEST      = 0.0 + iota // iota works with floats when used in a float expression
	ASSIGN                   // =
	COMMA                    // ,
	COND_OR                  // ||
	COND_AND                 // && (binds tighter than ||: c && v || w == (c && v) || w)
	BITWISE_OR               // |
	BITWISE_XOR              // ⊕
	BITWISE_AND              // &
	SHIFT                    // << >> >>>
	SUM                      // + -
	PRODUCT                  // * / ÷ %
	EXP                      // ^
	IMPLICIT                 // ⋅ (implicit multiplication)
	COLON                    // :
	LESSGREATER              // < > == != <= >=
	PREFIX                   // -X !X √X
	CALL                     // myFunction(X)
)

// leftBindingPower: how strongly an operator binds to its left operand
var leftBindingPower = map[string]float64{
	token.SYM_ASSIGN:   ASSIGN,
	token.SYM_COMMA:    COMMA,
	token.SYM_COND_OR:  COND_OR,
	token.SYM_COND_AND: COND_AND,
	token.SYM_OR:       BITWISE_OR,
	token.SYM_XOR:      BITWISE_XOR,
	token.SYM_AND:      BITWISE_AND,
	token.SYM_SHL:      SHIFT,
	token.SYM_SHR:      SHIFT,
	token.SYM_ASR:      SHIFT,
	token.SYM_ADD:      SUM,
	token.SYM_SUB:      SUM,
	token.SYM_CONCAT:   SUM, // ⊕ array concatenation
	token.SYM_MUL:      PRODUCT,
	token.SYM_DIV:      PRODUCT,
	token.SYM_QUO:      PRODUCT,
	token.SYM_MOD:      PRODUCT,
	token.SYM_IMPL_MUL: EXP - 0.25, // 8.75: Between RBP(^)=8.5 and EXP=9, allows ⋅ in exponents but not on left of ^
	token.SYM_EXP:      EXP,
	token.SYM_COLON:    COLON,
	token.SYM_EQL:      LESSGREATER,
	token.SYM_LSS:      LESSGREATER,
	token.SYM_GTR:      LESSGREATER,
	token.SYM_NEQ:      LESSGREATER,
	token.SYM_LEQ:      LESSGREATER,
	token.SYM_GEQ:      LESSGREATER,
	token.SYM_LPAREN:   CALL,
}

// rightBindingPower: how strongly an operator binds to its right operand
// Only include operators where RBP != LBP (non-left-associative operators)
// For left-associative operators, RBP defaults to LBP
var rightBindingPower = map[string]float64{
	token.SYM_EXP: EXP - 0.5, // 8.5: Right-associative, blocks * but allows ⋅ in exponents
}

// Kept for compatibility, now uses leftBindingPower
var precedences = leftBindingPower

type (
	prefixParseFn  func() ast.Expression
	infixParseFn   func(ast.Expression) ast.Expression
	postfixParseFn func(ast.Expression) ast.Expression
)

// Token Management System for Pluto Parser
//
// Token flow: [Lexer] -> peekToken -> curToken
//                ↑                        ↓
//            savedTokens <─────────── (processing)
//
// savedTokens acts as a buffer for tokens that need to be processed
// before getting the next token from the lexer.

type StmtParser struct {
	l      *lexer.Lexer
	errors []*token.CompileError

	curToken    token.Token
	peekToken   token.Token
	savedTokens []token.Token // FIFO queue of synthetic tokens from operator splits or inserting a * in something like 9x

	prefixParseFns  map[string]prefixParseFn
	infixParseFns   map[string]infixParseFn
	postfixParseFns map[string]postfixParseFn

	// The innermost parseExpression's split mode, threaded to ||/&& right
	// sides: they combine conditions, so an attached prefix after a complete
	// right-side condition must start the value there too.
	splitMode prefixSplitMode

	blankIdents []token.Token // tracks blank identifiers during parsing
}

func New(l *lexer.Lexer) *StmtParser {
	p := &StmtParser{
		l:      l,
		errors: []*token.CompileError{},
	}

	p.prefixParseFns = make(map[string]prefixParseFn)
	p.registerPrefix(token.STR_IDENT, p.parseIdentifier)
	p.registerPrefix(token.STR_INT, p.parseIntegerLiteral)
	p.registerPrefix(token.STR_FLOAT, p.parseFloatLiteral)
	p.registerPrefix(token.STR_STRING, p.parseStringLiteral)

	p.registerPrefix(token.SYM_BANG, p.parsePrefixExpression)
	p.registerPrefix(token.SYM_ADD, p.parsePrefixExpression)
	p.registerPrefix(token.SYM_SUB, p.parsePrefixExpression)
	p.registerPrefix(token.SYM_TILDE, p.parsePrefixExpression)
	p.registerPrefix(token.SYM_SQRT, p.parsePrefixExpression)
	p.registerPrefix(token.SYM_CBRT, p.parsePrefixExpression)
	p.registerPrefix(token.SYM_FTHRT, p.parsePrefixExpression)
	p.registerPrefix(token.SYM_LPAREN, p.parseGroupedExpression)
	p.registerPrefix(token.SYM_LBRACK, p.parseArrayLiteral)
	p.registerPrefix(token.SYM_NEWLINE, p.parseLineBreak)

	p.infixParseFns = make(map[string]infixParseFn)
	p.registerInfix(token.SYM_COLON, p.parseRangeLiteral)

	p.registerInfix(token.SYM_COND_OR, p.parseInfixExpression)
	p.registerInfix(token.SYM_COND_AND, p.parseInfixExpression)
	p.registerInfix(token.SYM_OR, p.parseInfixExpression)
	p.registerInfix(token.SYM_XOR, p.parseInfixExpression)
	p.registerInfix(token.SYM_AND, p.parseInfixExpression)
	p.registerInfix(token.SYM_SHL, p.parseInfixExpression)
	p.registerInfix(token.SYM_SHR, p.parseInfixExpression)
	p.registerInfix(token.SYM_ASR, p.parseInfixExpression)
	p.registerInfix(token.SYM_ADD, p.parseInfixExpression)
	p.registerInfix(token.SYM_SUB, p.parseInfixExpression)
	p.registerInfix(token.SYM_CONCAT, p.parseInfixExpression)
	p.registerInfix(token.SYM_MUL, p.parseInfixExpression)
	p.registerInfix(token.SYM_IMPL_MUL, p.parseInfixExpression)
	p.registerInfix(token.SYM_DIV, p.parseInfixExpression)
	p.registerInfix(token.SYM_QUO, p.parseInfixExpression)
	p.registerInfix(token.SYM_MOD, p.parseInfixExpression)
	p.registerInfix(token.SYM_EXP, p.parseInfixExpression) // Right-associative via rightBindingPower
	p.registerInfix(token.SYM_EQL, p.parseInfixExpression)
	p.registerInfix(token.SYM_LSS, p.parseInfixExpression)
	p.registerInfix(token.SYM_GTR, p.parseInfixExpression)
	p.registerInfix(token.SYM_NEQ, p.parseInfixExpression)
	p.registerInfix(token.SYM_LEQ, p.parseInfixExpression)
	p.registerInfix(token.SYM_GEQ, p.parseInfixExpression)

	p.postfixParseFns = make(map[string]postfixParseFn)
	p.registerPostfix(token.SYM_LPAREN, p.parseCallPostfix)
	p.registerPostfix(token.SYM_LBRACK, p.parseArrayRangePostfix)
	p.registerPostfix(token.SYM_PERIOD, p.parseDotPostfix)

	// Read two tokens, so curToken and peekToken are both set
	p.nextToken()
	p.nextToken()

	return p
}

func (p *StmtParser) nextToken() {
	// always advance current token
	p.curToken = p.peekToken

	// get new peek token from queue or lexer
	if len(p.savedTokens) > 0 {
		p.peekToken = p.savedTokens[0]
		p.savedTokens = p.savedTokens[1:]
	} else {
		p.peekToken = p.lexToken()
	}

	p.handleImplicitMult()
}

// peekNextToken looks ahead to see what token comes after peekToken.
// If not available in savedTokens, it loads it from the lexer.
func (p *StmtParser) peekNextToken() token.Token {
	if len(p.savedTokens) > 0 {
		return p.savedTokens[0]
	}
	nextTok := p.lexToken()
	p.savedTokens = append(p.savedTokens, nextTok)
	return nextTok
}

// lexToken reads the next token from the lexer and records the error the
// lexer reports with it, if any.
func (p *StmtParser) lexToken() token.Token {
	tok, err := p.l.NextToken()
	if err != nil {
		p.errors = append(p.errors, err)
	}
	return tok
}

// Handle implicit multiplication:
// If the current token is an INT or FLOAT and the following token is an IDENT
// with no space or line break before it (HadSpace counts both), then we assume
// an implicit multiplication.
// In this case, we save the IDENT token in 'savedToken', and substitute the next token
// with an implicit multiplication operator '⋅' token. This way, an input like "5var" is
// treated as "5 ⋅ var" with higher precedence than regular multiplication.
func (p *StmtParser) handleImplicitMult() {
	isNumber := p.curToken.Type == token.INT || p.curToken.Type == token.FLOAT
	isIdentNext := p.peekToken.Type == token.IDENT
	if !isNumber || !isIdentNext || p.peekToken.HadSpace {
		return
	}

	// Create implicit multiplication token with higher precedence than regular *
	mul := token.Token{
		Type:     token.OPERATOR,
		Literal:  token.SYM_IMPL_MUL,
		FileName: p.peekToken.FileName,
		Line:     p.peekToken.Line,
		Column:   p.peekToken.Column,
	}

	// Insert: cur | ⋅ | ident
	p.savedTokens = append([]token.Token{p.peekToken}, p.savedTokens...)
	p.peekToken = mul
}

// ============ Operator Splitting ============

type OpType int

const (
	PrefixOp OpType = iota
	InfixOp
)

// findLongestOp finds the longest matching operator in the parse function map
func (p *StmtParser) findLongestOp(lit string, opType OpType) string {
	best := ""
	switch opType {
	case PrefixOp:
		for op := range p.prefixParseFns {
			if strings.HasPrefix(lit, op) && len(op) > len(best) {
				best = op
			}
		}
	case InfixOp:
		for op := range p.infixParseFns {
			if strings.HasPrefix(lit, op) && len(op) > len(best) {
				best = op
			}
		}
	}
	return best
}

// splitOperator splits "√-" -> ["√", "-"]
func (p *StmtParser) splitOperator(tok token.Token, opType OpType) []token.Token {
	if tok.Type != token.OPERATOR {
		return []token.Token{tok}
	}

	match := p.findLongestOp(tok.Literal, opType)
	if match == "" || match == tok.Literal {
		return []token.Token{tok} // No split needed
	}

	// Split into matched part and remainder
	result := []token.Token{
		{
			Type:     token.OPERATOR,
			Literal:  match,
			FileName: tok.FileName,
			Line:     tok.Line,
			Column:   tok.Column,
			// Preserve source spacing so attached-prefix boundary checks still
			// work after an operator run is split into parseable pieces.
			HadSpace: tok.HadSpace,
		},
	}

	remainder := tok.Literal[len(match):]
	if remainder != "" {
		result = append(result, token.Token{
			Type:     token.OPERATOR,
			Literal:  remainder,
			FileName: tok.FileName,
			Line:     tok.Line,
			Column:   tok.Column + utf8.RuneCountInString(match),
		})
	}

	return result
}

// normalizeCurrentPrefixOperator splits current token for prefix parsing
func (p *StmtParser) normalizeCurrentPrefixOperator() {
	tokens := p.splitOperator(p.curToken, PrefixOp)
	if len(tokens) <= 1 {
		return
	}

	// Update current to matched part, queue the rest
	p.curToken = tokens[0]

	// Set next token to the immediate remainder part, and queue the original peek
	// plus any further remainder (avoiding duplicating tokens[1]).
	originalPeek := p.peekToken
	p.peekToken = tokens[1]

	queue := []token.Token{}
	if len(tokens) > 2 {
		queue = append(queue, tokens[2:]...)
	}
	queue = append(queue, originalPeek)
	p.savedTokens = append(queue, p.savedTokens...)
}

// normalizePeekInfixOperator splits peek token for infix parsing
func (p *StmtParser) normalizePeekInfixOperator() {
	tokens := p.splitOperator(p.peekToken, InfixOp)
	if len(tokens) <= 1 {
		return
	}

	// Replace peek with matched part, queue the rest
	p.peekToken = tokens[0]
	p.savedTokens = append(tokens[1:], p.savedTokens...)
}

func (p *StmtParser) curTokenIs(t token.TokenType) bool {
	return p.curToken.Type == t
}

func (p *StmtParser) peekTokenIs(t token.TokenType) bool {
	return p.peekToken.Type == t
}

func (p *StmtParser) expectPeek(t token.TokenType) bool {
	if p.peekTokenIs(t) {
		p.nextToken()
		return true
	} else {
		p.peekError(t)
		return false
	}
}

func (p *StmtParser) Errors() []string {
	var msgs []string
	for _, err := range p.errors {
		msgs = append(msgs, err.Error())
	}
	return msgs
}

func (p *StmtParser) errMsg(tokenLoc string, expToken, gotToken token.TokenType) *token.CompileError {
	msg := fmt.Sprintf("expected %s to be %s, got %s instead", tokenLoc, expToken, gotToken)
	return &token.CompileError{
		Token: p.curToken,
		Msg:   msg,
	}
}

func (p *StmtParser) illegalToken(t token.Token) *token.CompileError {
	msg := "Illegal token"
	return &token.CompileError{
		Token: t,
		Msg:   msg,
	}
}

func (p *StmtParser) peekError(t token.TokenType) {
	p.errors = append(p.errors, p.errMsg("next token", t, p.peekToken.Type))
}

func (p *StmtParser) curError(t token.TokenType) {
	p.errors = append(p.errors, p.errMsg("current token", t, p.curToken.Type))
}

func (p *StmtParser) noPrefixParseFnError(t token.Token) {
	msg := fmt.Sprintf("no prefix parse function for %s found", t.TokenTypeWithOp())
	ce := &token.CompileError{
		Token: p.curToken,
		Msg:   msg,
	}
	p.errors = append(p.errors, ce)
}

func (p *StmtParser) stmtEnded() bool {
	return p.peekTokenIs(token.NEWLINE) || p.peekTokenIs(token.EOF)
}

func (p *StmtParser) ParseProgram() *ast.Program {
	program := &ast.Program{}
	program.Statements = []ast.Statement{}
	for !p.curTokenIs(token.EOF) {
		if stmt := p.parseStatement(); stmt != nil {
			program.Statements = append(program.Statements, stmt)
		} else {
			p.skipLine()
		}
		p.nextToken()
	}

	return program
}

func (p *StmtParser) parseStatement() ast.Statement {
	if p.skipIndented() {
		return nil
	}
	firstToken := p.curToken
	p.blankIdents = nil // reset for new statement
	expList := p.parseExpList(prefixSplitNone)
	// An expression that failed to parse is nil and has already reported its
	// error.
	if slices.Contains(expList, nil) {
		return nil
	}

	if p.stmtEnded() {
		p.nextToken()
		// Print statement - blanks not allowed
		p.errorOnBlanks()
		return &ast.PrintStatement{
			Token: firstToken,
			Expression: &ast.CallExpression{
				Token:     firstToken,
				Function:  &ast.Identifier{Token: firstToken, Value: ""},
				Arguments: expList,
			},
		}
	}

	if !p.expectPeek(token.ASSIGN) {
		return nil
	}

	// It's an assignment - LHS blanks are valid (discard pattern)
	p.blankIdents = nil

	identList, ce := p.toIdentList(expList)
	if ce != nil {
		p.errors = append(p.errors, ce)
		return nil
	}

	p.checkNoDuplicates(identList)
	if stmt := p.parseLetStatement(identList); stmt != nil {
		return stmt
	}
	return nil
}

func (p *StmtParser) parseCodeStatement() ast.Statement {
	if p.skipIndented() {
		return nil
	}
	// Every statement in code mode starts with identifiers
	if !p.curTokenIs(token.IDENT) {
		p.curError(token.IDENT)
		return nil
	}

	idents := p.parseIdentifiers()
	if idents == nil {
		return nil
	}

	if !p.expectPeek(token.ASSIGN) {
		return nil
	}
	assignTok := p.curToken

	if p.peekToken.IsConstant() {
		s := p.parseConstStatement(idents)
		// below code is needed as we should return nil if ConstStatement is nil, and not interface to nil
		if s != nil {
			return s
		}
		return nil
	}

	if p.peekTokenIs(token.IDENT) {
		// In code mode, an identifier after '=' starts either a function signature
		// (`name(...)`) or a nominal struct literal (`TypeName`).
		if stmt := p.parseFuncOrStructStatement(assignTok, idents); stmt != nil {
			return stmt
		}
	}
	// TODO operator definitions
	return nil
}

func (p *StmtParser) parseFuncOrStructStatement(assignTok token.Token, idents []*ast.Identifier) ast.Statement {
	p.nextToken()
	if p.peekTokenIs(token.LPAREN) {
		fTok := p.curToken
		p.nextToken()
		f := p.parseFuncStatement(fTok, idents)
		if f != nil {
			return f
		}
		return nil
	}

	if p.peekTokenIs(token.NEWLINE) || p.peekTokenIs(token.EOF) {
		s := p.parseStructLiteralStatement(assignTok, idents, p.curToken)
		if s != nil {
			return s
		}
	}
	return nil
}

func (p *StmtParser) parseConstStatement(idents []*ast.Identifier) *ast.ConstStatement {
	stmt := &ast.ConstStatement{
		Token: p.curToken,
		Name:  idents,
		Value: []ast.Expression{},
	}

	// assume constant assignments
	p.nextToken()
	stmt.Value = p.parseConstants()
	if !p.stmtEnded() {
		msg := fmt.Sprintf("Expected %q or %q to end the statement", token.NEWLINE, token.EOF)
		ce := &token.CompileError{
			Token: p.curToken,
			Msg:   msg,
		}
		p.errors = append(p.errors, ce)
		return nil
	}
	p.nextToken()
	return stmt
}

// parseConstants expects first token to be a constant
// it parses the rest by assuming comma separated values
func (p *StmtParser) parseConstants() []ast.Expression {
	values := []ast.Expression{}
	values = append(values, p.parseConstant())
	for p.peekTokenIs(token.COMMA) {
		p.nextToken()
		if !p.peekToken.IsConstant() {
			msg := fmt.Sprintf("%q is not a constant", p.curToken.Literal)
			ce := &token.CompileError{
				Token: p.curToken,
				Msg:   msg,
			}
			p.errors = append(p.errors, ce)
			continue
		}
		p.nextToken()
		values = append(values, p.parseConstant())
	}
	return values
}

// parseConstant parses constant value
// it assumes that curtoken is a constant (int, float or string)
func (p *StmtParser) parseConstant() ast.Expression {
	// currently only supports int, float, and string
	// TODO: support rune, imag (a + bi)
	switch p.curToken.Type {
	case token.INT:
		return p.parseIntegerLiteral()
	case token.FLOAT:
		return p.parseFloatLiteral()
	case token.STRING:
		return p.parseStringLiteral()
	}
	return nil
}

func (p *StmtParser) parseStructHeaders() ([]token.Token, bool) {
	headerToks := []token.Token{}
	seen := make(map[string]struct{})

	for !p.curTokenIs(token.NEWLINE) && !p.curTokenIs(token.EOF) && !p.curTokenIs(token.DEINDENT) {
		if !p.curTokenIs(token.IDENT) {
			p.errors = append(p.errors, &token.CompileError{
				Token: p.curToken,
				Msg:   fmt.Sprintf("expected identifier for struct field header, got %s", p.curToken.Type),
			})
			return nil, false
		}

		p.validateIdentifier(p.curToken)
		if _, ok := seen[p.curToken.Literal]; ok {
			p.errors = append(p.errors, &token.CompileError{
				Token: p.curToken,
				Msg:   fmt.Sprintf("duplicate struct field header: %s", p.curToken.Literal),
			})
			return nil, false
		}

		seen[p.curToken.Literal] = struct{}{}
		headerToks = append(headerToks, p.curToken)
		p.nextToken()
	}
	p.errorOnBlanks()

	if len(headerToks) == 0 {
		p.errors = append(p.errors, &token.CompileError{
			Token: p.curToken,
			Msg:   "struct definition must include at least one field header",
		})
		return nil, false
	}

	return headerToks, true
}

func (p *StmtParser) parseStructRowConstants() ([]ast.Expression, bool) {
	row := []ast.Expression{}

	for !p.curTokenIs(token.NEWLINE) && !p.curTokenIs(token.EOF) && !p.curTokenIs(token.DEINDENT) {
		if p.curTokenIs(token.COMMA) {
			p.errors = append(p.errors, &token.CompileError{
				Token: p.curToken,
				Msg:   "struct value row values must be separated by spaces, not commas",
			})
			return nil, false
		}

		if !p.curToken.IsConstant() {
			p.errors = append(p.errors, &token.CompileError{
				Token: p.curToken,
				Msg:   fmt.Sprintf("struct value row must contain constants only, got %s", p.curToken.TokenTypeWithOp()),
			})
			return nil, false
		}

		row = append(row, p.parseConstant())
		p.nextToken()
	}

	if len(row) == 0 {
		p.errors = append(p.errors, &token.CompileError{
			Token: p.curToken,
			Msg:   "struct definition requires one data row",
		})
		return nil, false
	}

	return row, true
}

func (p *StmtParser) parseStructLiteralStatement(assignTok token.Token, idents []*ast.Identifier, typeTok token.Token) *ast.StructStatement {
	if len(idents) != 1 {
		p.errors = append(p.errors, &token.CompileError{
			Token: assignTok,
			Msg:   "struct definition must bind exactly one constant name",
		})
		return nil
	}
	stmt := &ast.StructStatement{
		Token: assignTok,
		Name:  idents[0],
		Value: &ast.StructLiteral{
			Token: typeTok,
		},
	}

	if p.peekTokenIs(token.EOF) {
		return stmt
	}

	// Caller only enters this path when the type name is followed by NEWLINE or EOF.
	p.nextToken() // consume NEWLINE
	if !p.peekTokenIs(token.INDENT) {
		return stmt
	}
	p.checkBlockIndent(p.peekToken)
	p.nextToken() // consume INDENT
	p.nextToken() // move to first token in the struct body
	if !p.parseStructBody(stmt.Value) {
		p.leaveBlock()
		return nil
	}
	return stmt
}

// parseStructBody reads a struct definition's header and value row into
// value. Its block's DEINDENT stays current for CodeParser to consume.
func (p *StmtParser) parseStructBody(value *ast.StructLiteral) bool {
	if !p.curTokenIs(token.COLON) {
		p.errors = append(p.errors, &token.CompileError{
			Token: p.curToken,
			Msg:   "struct definition must start with ':' field header row",
		})
		return false
	}

	colon := p.curToken
	p.nextToken()
	p.checkHeaderColon(colon)
	headers, ok := p.parseStructHeaders()
	if !ok {
		return false
	}

	if !p.curTokenIs(token.NEWLINE) {
		p.errors = append(p.errors, &token.CompileError{
			Token: p.curToken,
			Msg:   "expected NEWLINE after struct field headers",
		})
		return false
	}

	p.nextToken()
	if p.curToken.Column != headers[0].Column {
		p.errors = append(p.errors, &token.CompileError{
			Token: p.curToken,
			Msg:   "struct value row must align with the first field header",
		})
		return false
	}
	row, ok := p.parseStructRowConstants()
	if !ok {
		return false
	}

	if len(row) != len(headers) {
		p.errors = append(p.errors, &token.CompileError{
			Token: value.Token,
			Msg:   fmt.Sprintf("struct value row has %d values, expected %d", len(row), len(headers)),
		})
		return false
	}

	if p.curTokenIs(token.NEWLINE) {
		p.nextToken()
	}

	if !p.curTokenIs(token.EOF) && !p.curTokenIs(token.DEINDENT) {
		p.errors = append(p.errors, &token.CompileError{
			Token: p.curToken,
			Msg:   "struct definition supports exactly one value row",
		})
		return false
	}

	value.Headers = headers
	value.Row = row
	return true
}

// flattenCondAnd returns a condition's top-level && conjuncts, left to right.
// The condition slot's && is the statement-level conjunction/domain list: each
// conjunct is validated like a former comma-list element, so bare range
// drivers nest (i && j walks the cartesian product) and comparisons gate. The
// compiler short-circuits the resulting condition list left to right.
func flattenCondAnd(exp ast.Expression) []ast.Expression {
	if infix, ok := ast.IsLogicalAnd(exp); ok {
		return append(flattenCondAnd(infix.Left), flattenCondAnd(infix.Right)...)
	}
	return []ast.Expression{exp}
}

func (p *StmtParser) conditionsOk(expList []ast.Expression) bool {
	before := len(p.errors)
	for _, exp := range expList {
		if p.isCondition(exp) {
			continue
		}
		msg := fmt.Sprintf("Expression %q is not a condition. Statement conditions must be comparisons or bare range/array-selection drivers", exp.String())
		ce := &token.CompileError{
			Token: exp.Tok(),
			Msg:   msg,
		}
		p.errors = append(p.errors, ce)
	}
	return len(p.errors) == before
}

func (p *StmtParser) parseLetStatement(identList []*ast.Identifier) *ast.LetStatement {
	stmt := &ast.LetStatement{
		Token:     p.curToken,
		Name:      identList,
		Value:     []ast.Expression{},
		Condition: []ast.Expression{},
	}

	p.nextToken()
	if p.curTokenIs(token.NEWLINE) || p.curTokenIs(token.EOF) {
		p.errors = append(p.errors, &token.CompileError{
			Token: stmt.Token,
			Msg:   "an assignment's value starts on the same line as its '='",
		})
		return nil
	}
	expList := p.parseExpList(prefixSplitAfterCondition)
	p.errorOnBlanks()
	// If parsing the RHS produced any nil expressions, abort this let-statement
	// to avoid panics downstream; errors are already recorded.
	for _, e := range expList {
		if e == nil {
			return nil
		}
	}
	if p.stmtEnded() {
		stmt.Value = expList
		p.nextToken()
		return stmt
	}

	// A statement condition is one expression — comma means positional lists
	// only. Conjunctions are spelled with the operator: a > 2 && b > 3  value.
	if len(expList) > 1 {
		p.errors = append(p.errors, &token.CompileError{
			Token: expList[1].Tok(),
			Msg:   "a statement condition is a single expression; combine conditions with && (a > 2 && b > 3  value)",
		})
		return nil
	}
	if !p.conditionsOk(expList) {
		return nil
	}

	stmt.Condition = flattenCondAnd(expList[0])

	p.nextToken()
	stmt.Value = p.parseExpList(prefixSplitNone)
	p.errorOnBlanks()
	if slices.Contains(stmt.Value, nil) {
		return nil
	}

	if p.stmtEnded() {
		p.nextToken()
		return stmt
	}

	msg := fmt.Sprintf("Expected either NEWLINE or EOF token. Instead got %+v", p.peekToken)
	ce := &token.CompileError{
		Token: p.curToken,
		Msg:   msg,
	}
	p.errors = append(p.errors, ce)
	return nil
}

func (p *StmtParser) isCondition(exp ast.Expression) bool {
	if exp.Tok().IsComparison() {
		return true
	}

	if infix, ok := ast.IsLogicalOr(exp); ok {
		return p.isCondition(infix.Left) && p.isCondition(infix.Right)
	}
	if infix, ok := ast.IsLogicalAnd(exp); ok {
		return p.isCondition(infix.Left) && p.isCondition(infix.Right)
	}

	switch exp.(type) {
	case *ast.Identifier, *ast.RangeLiteral, *ast.ArrayRangeExpression:
		return true
	default:
		return false
	}
}

func (p *StmtParser) toIdentList(expList []ast.Expression) ([]*ast.Identifier, *token.CompileError) {
	identifiers := []*ast.Identifier{}
	var ce *token.CompileError
	for _, exp := range expList {
		identifier, ok := exp.(*ast.Identifier)
		if !ok {
			msg := fmt.Sprintf("expected expression to be of type %q. Instead got %q. Literal: %q", reflect.TypeOf(identifier), reflect.TypeOf(exp), exp.Tok().Literal)
			ce = &token.CompileError{
				Token: exp.Tok(),
				Msg:   msg,
			}
			break
		}
		identifiers = append(identifiers, identifier)
	}
	return identifiers, ce
}

func (p *StmtParser) parseExpList(splitPrefix prefixSplitMode) []ast.Expression {
	expList := []ast.Expression{p.parseExpression(LOWEST, splitPrefix)}
	for !p.atLineEnd() && p.peekTokenIs(token.COMMA) {
		p.nextToken()
		p.nextToken()
		expList = append(expList, p.parseExpression(LOWEST, splitPrefix))
	}
	return expList
}

type prefixSplitMode int

const (
	prefixSplitNone prefixSplitMode = iota
	prefixSplitAlways
	prefixSplitAfterCondition
)

func (p *StmtParser) parseExpression(precedence float64, splitPrefix prefixSplitMode) ast.Expression {
	saved := p.splitMode
	p.splitMode = splitPrefix
	defer func() { p.splitMode = saved }()

	// ignore illegal tokens
	for p.curTokenIs(token.ILLEGAL) {
		p.illegalToken(p.curToken)
		p.nextToken()
	}

	// If current token is an OPERATOR run, split it for prefix use (e.g., "√-1").
	if p.curToken.Type == token.OPERATOR {
		p.normalizeCurrentPrefixOperator()
	}

	prefix := p.prefixParseFns[p.curToken.TokenTypeWithOp()]
	if prefix == nil {
		p.noPrefixParseFnError(p.curToken)
		return nil
	}
	leftExp := prefix()
	if leftExp == nil {
		return nil
	}

	return p.parseExpressionTail(precedence, splitPrefix, leftExp)
}

// parseExpressionTail continues parsing an expression using precedence climbing.
// It handles infix/postfix operators and can stop before an attached prefix
// operator when the current split mode says the left side is complete. Array
// rows use this to split `[a -b]`; let statements use the same spacing check to
// split `cond -value` at the condition/value boundary.
//
// Parameters:
//   - precedence: minimum binding power - stops when next operator has lower precedence
//   - splitPrefix: controls whether an attached prefix starts the next expression
//   - left: the left-hand expression already parsed
//
// Spacing rules used by peekStartsAttachedPrefix:
//   - `a - b` (space before and after `-`) → subtraction (infix)
//   - `a -b` (space before, not after `-`) → split before `-b` when allowed
//   - `a-b` (no space before `-`) → subtraction (infix, normal precedence)
func (p *StmtParser) parseExpressionTail(precedence float64, splitPrefix prefixSplitMode, left ast.Expression) ast.Expression {
	for left != nil {
		var consumed bool
		left, consumed = p.tryPostfix(left)
		if consumed {
			continue
		}

		if precedence >= p.peekPrecedence() {
			break
		}

		// Normalize operator first to get its true form
		if p.peekToken.Type == token.OPERATOR {
			p.normalizePeekInfixOperator()
		}

		infix := p.infixParseFns[p.peekToken.TokenTypeWithOp()]
		if infix == nil {
			return left
		}

		if p.shouldSplitAttachedPrefix(splitPrefix, left) && p.peekStartsAttachedPrefix() {
			break
		}

		// Process as infix operator
		p.nextToken()
		left = infix(left)
	}
	return left
}

func (p *StmtParser) peekStartsAttachedPrefix() bool {
	if !p.peekToken.HadSpace {
		return false
	}
	if p.prefixParseFns[p.peekToken.TokenTypeWithOp()] == nil {
		return false
	}
	return !p.peekNextToken().HadSpace
}

func (p *StmtParser) shouldSplitAttachedPrefix(mode prefixSplitMode, left ast.Expression) bool {
	switch mode {
	case prefixSplitAlways:
		return true
	case prefixSplitAfterCondition:
		// Split after anything that can be a statement condition. The type solver
		// later rejects bare identifiers that are not range drivers or bool values.
		// Use a spaced operator (`i - x`) to keep the left side in value-position
		// arithmetic, mirroring how array rows treat attached prefix.
		return p.isCondition(left)
	default:
		return false
	}
}

func (p *StmtParser) allowCallPostfix(left ast.Expression) bool {
	switch left.(type) {
	case *ast.InfixExpression:
		return false
	default:
		return true
	}
}

func (p *StmtParser) tryPostfix(left ast.Expression) (ast.Expression, bool) {
	key := p.peekToken.TokenTypeWithOp()
	postfix := p.postfixParseFns[key]
	if postfix == nil {
		return left, false
	}
	// Postfix call/index operations must be connected to their target:
	// f(x), a[1], a[1:3]. If there is whitespace before the postfix token,
	// treat it as the next expression instead.
	if (key == token.SYM_LBRACK || key == token.SYM_LPAREN || key == token.SYM_PERIOD) && p.peekToken.HadSpace {
		return left, false
	}
	if key == token.SYM_LPAREN && !p.allowCallPostfix(left) {
		return left, false
	}
	p.nextToken()
	return postfix(left), true
}

func (p *StmtParser) parseArrayRangeExpression(array ast.Expression) ast.Expression {
	rangeExpr := &ast.ArrayRangeExpression{Token: p.curToken, Array: array}

	// Parse the expression inside the brackets.
	p.nextToken()
	idx := p.parseExpression(LOWEST, prefixSplitNone)
	if idx == nil {
		return nil
	}
	rangeExpr.Range = idx

	if !p.expectPeek(token.RBRACK) {
		return nil
	}

	return rangeExpr
}

func (p *StmtParser) peekPrecedence() float64 {
	if prec, ok := precedences[p.peekToken.TokenTypeWithOp()]; ok {
		return prec
	}

	return LOWEST
}

func (p *StmtParser) curPrecedence() float64 {
	if prec, ok := precedences[p.curToken.TokenTypeWithOp()]; ok {
		return prec
	}

	return LOWEST
}

func (p *StmtParser) parseIdentifier() ast.Expression {
	p.validateIdentifier(p.curToken)
	return &ast.Identifier{Token: p.curToken, Value: p.curToken.Literal}
}

func (p *StmtParser) parseIntegerLiteral() ast.Expression {
	lit := &ast.IntegerLiteral{Token: p.curToken}

	value, err := parseIntegerLiteralValue(cleanNumberLiteral(p.curToken.Literal))
	if err != nil {
		msg := fmt.Sprintf("could not parse %q as integer", p.curToken.Literal)
		ce := &token.CompileError{
			Token: p.curToken,
			Msg:   msg,
		}
		p.errors = append(p.errors, ce)
		return nil
	}

	lit.Value = value

	return lit
}

func parseIntegerLiteralValue(lit string) (int64, error) {
	if len(lit) <= 1 || lit[0] != '0' {
		return strconv.ParseInt(lit, 10, 64)
	}

	switch lit[1] {
	case 'b':
		return strconv.ParseInt(lit[2:], 2, 64)
	case 'o':
		return strconv.ParseInt(lit[2:], 8, 64)
	case 'x':
		return strconv.ParseInt(lit[2:], 16, 64)
	default:
		if lexer.IsDecimal(rune(lit[1])) {
			return 0, fmt.Errorf("leading-zero integer literal")
		}
		return strconv.ParseInt(lit, 10, 64)
	}
}

func (p *StmtParser) parseFloatLiteral() ast.Expression {
	lit := &ast.FloatLiteral{Token: p.curToken}
	value, err := strconv.ParseFloat(cleanNumberLiteral(p.curToken.Literal), 64)
	if err != nil {
		msg := fmt.Sprintf("could not parse %q as float", p.curToken.Literal)
		ce := &token.CompileError{Token: p.curToken, Msg: msg}
		p.errors = append(p.errors, ce)
		return nil
	}

	lit.Value = value
	return lit
}

func cleanNumberLiteral(lit string) string {
	return strings.ReplaceAll(lit, "'", "")
}

func (p *StmtParser) parseStringLiteral() ast.Expression {
	return &ast.StringLiteral{Token: p.curToken}
}

// Layout errors: brackets take no part in layout, so a line break inside
// them is legal only where these rules allow it.
const (
	blockIndent    = "    " // the indentation a block adds to the line that opens it
	blockIndentErr = "indent each block by 4 spaces; a header's ':' by 2, with one space after it"
	strayIndentErr = "unexpected indentation: the line before it opens no block"
	inlineArrayErr = "an inline array stays on one line; to span lines, end the line with '[' and indent its rows"
	blockRowsErr   = "a block literal's rows are indented 4 spaces past the line of its '['"
	blockCloseErr  = lexer.BLOCK_CLOSE_ERR
	lineBreakErr   = "break a line only after a comma, and indent the next line"
	headerColonErr = "a header's ':' has one space after it, so its names line up with the values"
)

func (p *StmtParser) parseArrayLiteral() ast.Expression {
	arr := &ast.ArrayLiteral{
		Token:   p.curToken, // the '[' token
		Headers: []string{},
		Rows:    [][]ast.Expression{},
		Indices: make(map[string][]int),
	}

	p.nextToken() // consume the '[' token
	arr.Block = p.curTokenIs(token.NEWLINE)
	var ok bool
	if arr.Block {
		ok = p.parseBlockLiteral(arr)
	} else {
		ok = p.parseInlineLiteral(arr)
	}
	if !ok {
		return nil
	}
	p.parseStatedTypes(arr)
	// Do not consume the closing ']' (or the sample after it) here. Align
	// with grouped-expression behavior and leave curToken at the literal's
	// last token; callers (statement parsing) will advance past newline/EOF as
	// appropriate.
	return arr
}

// parseInlineLiteral reads the cells of a literal that stays on the line of
// its '[', leaving curToken at its ']'.
func (p *StmtParser) parseInlineLiteral(arr *ast.ArrayLiteral) bool {
	if p.curTokenIs(token.COLON) {
		p.errors = append(p.errors, &token.CompileError{
			Token: p.curToken,
			Msg:   "a table's header goes on its own line after '['",
		})
		return false
	}
	row, ok := p.parseRow()
	if len(row) > 0 {
		arr.Rows = append(arr.Rows, row)
	}
	switch {
	case p.curTokenIs(token.RBRACK):
		return ok
	case !ok:
		// The cell that failed has reported the error.
	case p.curTokenIs(token.NEWLINE):
		p.errors = append(p.errors, &token.CompileError{Token: p.curToken, Msg: inlineArrayErr})
	default:
		p.errors = append(p.errors, &token.CompileError{Token: p.curToken, Msg: "expected ']' to close array literal"})
	}
	return false
}

// parseBlockLiteral reads a literal whose '[' ends its line: an optional
// header and the rows, as a block 4 spaces in, then the ']' on its own line.
// It leaves curToken at the ']'.
func (p *StmtParser) parseBlockLiteral(arr *ast.ArrayLiteral) bool {
	if p.peekTokenIs(token.RBRACK) {
		p.nextToken()
		return true
	}
	if !p.peekTokenIs(token.INDENT) {
		p.errors = append(p.errors, &token.CompileError{Token: p.peekToken, Msg: blockRowsErr})
		return false
	}
	p.checkBlockIndent(p.peekToken)
	p.nextToken() // the line break after '['
	p.nextToken() // the rows' INDENT

	ok := !p.curTokenIs(token.COLON) || p.parseTableHeader(arr)
	if !ok {
		p.skipLine()
	}
	for !p.curTokenIs(token.DEINDENT) && !p.curTokenIs(token.EOF) {
		if p.skipIndented() {
			ok = false
		} else if !p.curTokenIs(token.RBRACK) {
			row, rowOK := p.parseRow()
			if len(row) > 0 {
				arr.Rows = append(arr.Rows, row)
			}
			if !rowOK {
				ok = false
				p.skipLine()
			}
		}
		if p.curTokenIs(token.RBRACK) {
			p.errors = append(p.errors, &token.CompileError{Token: p.curToken, Msg: blockCloseErr})
			p.leaveBlock()
			return false
		}
		p.nextToken() // past the end of the line
	}
	if p.curTokenIs(token.DEINDENT) && p.peekTokenIs(token.RBRACK) {
		p.nextToken() // leave the rows' block
		return ok
	}
	missing := p.curToken
	if p.curTokenIs(token.DEINDENT) {
		missing = p.peekToken // the literal ends at its DEINDENT
	}
	p.errors = append(p.errors, &token.CompileError{Token: missing, Msg: blockCloseErr})
	return false
}

// parseTableHeader reads a table's header line, from its ':' to the end of
// the line.
func (p *StmtParser) parseTableHeader(arr *ast.ArrayLiteral) bool {
	colon := p.curToken
	p.nextToken() // consume ':'
	if p.atLineEnd() || p.curTokenIs(token.RBRACK) {
		p.errors = append(p.errors, &token.CompileError{
			Token: colon,
			Msg:   "expected at least one column header after ':'",
		})
		return false
	}
	p.checkHeaderColon(colon)
	return p.parseHeader(arr)
}

// checkHeaderColon reports a header whose first name, the current token, is
// not one space after its ':'.
func (p *StmtParser) checkHeaderColon(colon token.Token) {
	if p.curTokenIs(token.IDENT) && p.curToken.Column != colon.Column+2 {
		p.errors = append(p.errors, &token.CompileError{Token: p.curToken, Msg: headerColonErr})
	}
}

// skipLine moves past the rest of a failed line to its end, past the lines
// indented under it too, so the next statement or row starts on a line of
// its own.
func (p *StmtParser) skipLine() {
	for !p.atLineEnd() {
		p.nextToken()
	}
	if p.curTokenIs(token.NEWLINE) && p.peekTokenIs(token.INDENT) {
		p.nextToken()
		p.skipBlock()
	}
}

// checkBlockIndent reports a block that its INDENT says is not indented 4
// spaces past the line that opens it.
func (p *StmtParser) checkBlockIndent(indent token.Token) {
	if indent.Literal != blockIndent {
		p.errors = append(p.errors, &token.CompileError{Token: indent, Msg: blockIndentErr})
	}
}

// skipIndented reports a line indented past its block where no block opens,
// and moves past it and the lines under it to the DEINDENT that ends them.
func (p *StmtParser) skipIndented() bool {
	if !p.curTokenIs(token.INDENT) {
		return false
	}
	p.errors = append(p.errors, &token.CompileError{Token: p.curToken, Msg: strayIndentErr})
	p.skipBlock()
	return true
}

// skipBlock moves from the current INDENT to the DEINDENT that ends its
// block.
func (p *StmtParser) skipBlock() {
	p.nextToken()
	p.leaveBlock()
}

// leaveBlock moves to the DEINDENT that ends the block the parser is in, so
// a construct that fails inside its block takes all of the block's lines.
func (p *StmtParser) leaveBlock() {
	for depth := 0; !p.curTokenIs(token.EOF); p.nextToken() {
		switch p.curToken.Type {
		case token.INDENT:
			depth++
		case token.DEINDENT:
			if depth == 0 {
				return
			}
			depth--
		}
	}
}

// atLineEnd reports whether the parser is at the end of a line: its line
// break, the DEINDENT that ends the lines indented under it, or the end of
// the input.
func (p *StmtParser) atLineEnd() bool {
	return p.curTokenIs(token.NEWLINE) || p.curTokenIs(token.DEINDENT) || p.curTokenIs(token.EOF)
}

// parseStatedTypes reads the sample after a literal's brackets and checks the
// element types the literal states. Only a literal without cells states them:
// an empty array with its sample, a table without rows with a type on every
// column header. Any other literal takes its types from its cells. The literal
// stays whole either way, so parsing continues past a reported error.
func (p *StmtParser) parseStatedTypes(arr *ast.ArrayLiteral) {
	if len(arr.Headers) == 0 && len(arr.Rows) == 0 {
		p.parseArraySample(arr)
		return
	}
	p.rejectSample(arr)
	switch {
	case len(arr.Rows) == 0 && (len(arr.ColumnTypes) == 0 || slices.Contains(arr.ColumnTypes, nil)):
		p.errors = append(p.errors, &token.CompileError{
			Token: arr.Token,
			Msg:   `a table without rows needs a type on every column, as in Name("") Score(0)`,
		})
	case len(arr.Rows) > 0 && len(arr.ColumnTypes) > 0:
		p.errors = append(p.errors, &token.CompileError{
			Token: arr.Token,
			Msg:   "column types are only written on a table without rows",
		})
	}
}

// rejectSample reads a sample attached to the closing bracket of a literal
// with a header or cells, and reports it.
func (p *StmtParser) rejectSample(arr *ast.ArrayLiteral) {
	if !p.sampleFollows() {
		return
	}
	p.nextToken()
	msg := "an element type is only written on an empty array"
	if len(arr.Rows) > 0 {
		msg += "; cells give a literal its type, as in [1.0 2 3]"
	}
	p.errors = append(p.errors, &token.CompileError{Token: p.curToken, Msg: msg})
}

// sampleFollows reports whether a number, string or name is attached to the
// closing bracket at curToken, where an empty array's sample goes.
func (p *StmtParser) sampleFollows() bool {
	return !p.peekToken.HadSpace && slices.Contains([]token.TokenType{token.INT, token.FLOAT, token.STRING, token.IDENT}, p.peekToken.Type)
}

// parseArraySample reads the zero value or variable attached to an empty
// array's closing bracket, leaving curToken at it.
func (p *StmtParser) parseArraySample(arr *ast.ArrayLiteral) {
	if !p.sampleFollows() {
		p.errors = append(p.errors, &token.CompileError{
			Token: arr.Token,
			Msg:   `an empty array needs its element type: write []0, []0.0 or []""`,
		})
		return
	}

	p.nextToken()
	var sample ast.Expression
	switch p.curToken.Type {
	case token.IDENT:
		sample = p.parseIdentifier()
		p.errorOnBlanks()
	case token.INT:
		sample = p.parseIntegerLiteral()
	case token.FLOAT:
		sample = p.parseFloatLiteral()
	case token.STRING:
		sample = p.parseStringLiteral()
	}
	if sample == nil {
		return
	}

	if _, isName := sample.(*ast.Identifier); !isName && !isZeroSample(sample) {
		p.errors = append(p.errors, &token.CompileError{
			Token: sample.Tok(),
			Msg:   `an empty array's element type is written as a zero value: []0, []0.0 or []""`,
		})
		return
	}
	key := p.peekToken.TokenTypeWithOp()
	if !p.peekToken.HadSpace && (key == token.SYM_LBRACK || key == token.SYM_LPAREN || key == token.SYM_PERIOD) {
		p.errors = append(p.errors, &token.CompileError{
			Token: p.peekToken,
			Msg:   "the sample after [] is a zero value or a variable name",
		})
		return
	}
	arr.Sample = sample
}

// isZeroSample reports whether expr is the zero value that names an element
// type: 0, 0.0 or "".
func isZeroSample(expr ast.Expression) bool {
	switch e := expr.(type) {
	case *ast.IntegerLiteral:
		return e.Token.Literal == "0"
	case *ast.FloatLiteral:
		return e.Token.Literal == "0.0"
	case *ast.StringLiteral:
		return e.Token.Literal == ""
	default:
		return false
	}
}

// parseHeader parses column headers after ':'. A table without data rows
// types each column with a zero value attached to its name: Name("") Score(0).
func (p *StmtParser) parseHeader(arr *ast.ArrayLiteral) bool {
	var columnTypes []ast.Expression
	typed := false
	for !p.curTokenIs(token.RBRACK) && !p.curTokenIs(token.EOF) && !p.curTokenIs(token.NEWLINE) {
		if p.curTokenIs(token.IDENT) {
			header := p.curToken
			p.validateIdentifier(header)
			arr.Headers = append(arr.Headers, header.Literal)
			p.nextToken()
			columnType, ok := p.parseColumnType(header)
			if !ok {
				return false
			}
			columnTypes = append(columnTypes, columnType)
			typed = typed || columnType != nil
			continue
		}

		// Expected identifier for column header
		p.errors = append(p.errors, &token.CompileError{
			Token: p.curToken,
			Msg:   fmt.Sprintf("expected identifier for column header, got %s", p.curToken.Type),
		})
		return false
	}
	p.errorOnBlanks() // headers cannot be blank
	if typed {
		arr.ColumnTypes = columnTypes
	}
	return true
}

// parseColumnType parses the zero value in parentheses attached to a column
// header, returning nil when the header has none.
func (p *StmtParser) parseColumnType(header token.Token) (ast.Expression, bool) {
	if !p.curTokenIs(token.LPAREN) {
		return nil, true
	}
	name := header.Literal
	if p.curToken.HadSpace {
		p.errors = append(p.errors, &token.CompileError{
			Token: p.curToken,
			Msg:   fmt.Sprintf("a column type attaches to its name: %s(0), %s(0.0) or %s(\"\")", name, name, name),
		})
		return nil, false
	}

	p.nextToken() // consume '('
	var sample ast.Expression
	switch p.curToken.Type {
	case token.INT:
		sample = p.parseIntegerLiteral()
	case token.FLOAT:
		sample = p.parseFloatLiteral()
	case token.STRING:
		sample = p.parseStringLiteral()
	}
	if sample == nil || !isZeroSample(sample) {
		p.errors = append(p.errors, &token.CompileError{
			Token: p.curToken,
			Msg:   fmt.Sprintf("a column's type is written as a zero value: %s(0), %s(0.0) or %s(\"\")", name, name, name),
		})
		return nil, false
	}
	if !p.expectPeek(token.RPAREN) {
		return nil, false
	}
	p.nextToken() // consume ')'
	return sample, true
}

// parseRow parses a row's cells up to a ']' or the end of its line, where it
// stops. A cell that fails leaves the row failed, and the row reads on.
func (p *StmtParser) parseRow() ([]ast.Expression, bool) {
	row := []ast.Expression{}
	ok := true
	for !p.curTokenIs(token.RBRACK) && !p.atLineEnd() {
		expr := p.parseExpression(LOWEST, prefixSplitAlways)
		ok = ok && expr != nil
		if expr != nil {
			row = append(row, expr)
		}
		if !p.atLineEnd() {
			p.nextToken()
		}
	}
	return row, ok
}

// parseRangeLiteral is called when we encounter a ':' in an infix position.
// The `left` argument is the expression that was just parsed before the ':'.
func (p *StmtParser) parseRangeLiteral(left ast.Expression) ast.Expression {
	// `left` is the "start" of our range.
	// p.curToken is the `:`.
	rl := &ast.RangeLiteral{
		Token: p.curToken,
		Start: left,
	}

	// Get the precedence of the ':' operator to handle right-associativity correctly.
	precedence := p.curPrecedence()
	p.nextToken() // Consume the ':'

	rl.Stop = p.parseExpression(precedence, prefixSplitNone)
	if rl.Stop == nil {
		p.errors = append(p.errors, &token.CompileError{
			Token: p.curToken,
			Msg:   "expected expression after ':' for range stop",
		})
		return nil
	}

	if p.peekTokenIs(token.COLON) {
		p.nextToken()
		p.nextToken()
		rl.Step = p.parseExpression(precedence, prefixSplitNone)
		if rl.Step == nil {
			p.errors = append(p.errors, &token.CompileError{
				Token: p.curToken,
				Msg:   "expected expression after second ':' for range step",
			})
			return nil
		}
	}

	return rl
}

func (p *StmtParser) parsePrefixExpression() ast.Expression {
	expression := &ast.PrefixExpression{
		Token:    p.curToken,
		Operator: p.curToken.Literal,
	}

	p.nextToken()

	expression.Right = p.parseExpression(PREFIX, prefixSplitNone)
	if expression.Right == nil {
		return nil
	}
	return expression
}

func (p *StmtParser) parseInfixExpression(left ast.Expression) ast.Expression {
	expression := &ast.InfixExpression{
		Token:    p.curToken,
		Operator: p.curToken.Literal,
		Left:     left,
	}

	// Use right binding power if defined, otherwise use left binding power (left-associative)
	rbp, hasRBP := rightBindingPower[p.curToken.Literal]
	if !hasRBP {
		rbp = p.curPrecedence() // Default: RBP = LBP (left-associative)
	}

	// ||/&& combine conditions, so their right side keeps the surrounding
	// condition-split mode: in `i < 2 && j > 1 -x` the attached prefix
	// starts the value, exactly as after a single condition.
	mode := prefixSplitNone
	if expression.Operator == token.SYM_COND_OR || expression.Operator == token.SYM_COND_AND {
		mode = p.splitMode
	}

	p.nextToken()
	expression.Right = p.parseExpression(rbp, mode)
	if expression.Right == nil {
		return nil
	}
	return expression
}

// parseGroupedExpression parses parentheses as pure grouping: one
// sub-expression closed by ')'. Conditional values are spelled with the
// operators — `cond && value`, `cond && value || fallback` — not a
// parenthesized (cond value) form.
func (p *StmtParser) parseGroupedExpression() ast.Expression {
	if p.parenBreak() {
		return nil
	}
	p.nextToken()

	exp := p.parseExpression(LOWEST, prefixSplitNone)
	if exp == nil {
		return nil
	}
	if p.parenBreak() || !p.expectPeek(token.RPAREN) {
		return nil
	}
	return exp
}

// parseLineBreak reports a line that ends where an operand starts.
func (p *StmtParser) parseLineBreak() ast.Expression {
	p.errors = append(p.errors, &token.CompileError{Token: p.curToken, Msg: lineBreakErr})
	return nil
}

// parenBreak reports a line break at peekToken inside parentheses, where a
// line breaks only after a comma, before an indented line. The lines that
// continue the parentheses, up to a ')' that starts a line, belong to them.
func (p *StmtParser) parenBreak() bool {
	if !p.peekTokenIs(token.NEWLINE) {
		return false
	}
	p.errors = append(p.errors, &token.CompileError{Token: p.peekToken, Msg: lineBreakErr})
	p.skipLine()
	if p.peekTokenIs(token.RPAREN) {
		p.nextToken()
		if p.peekTokenIs(token.NEWLINE) {
			p.nextToken()
		}
	}
	return true
}

// assumes current token is token.NEWLINE
func (p *StmtParser) parseBlockStatement() *ast.BlockStatement {
	if p.peekTokenIs(token.INDENT) {
		p.checkBlockIndent(p.peekToken)
	} else {
		p.peekError(token.INDENT)
	}
	p.nextToken()
	p.nextToken()
	block := &ast.BlockStatement{Token: p.curToken}
	block.Statements = []ast.Statement{}

	for !p.curTokenIs(token.DEINDENT) && !p.curTokenIs(token.EOF) {
		if stmt := p.parseStatement(); stmt != nil {
			block.Statements = append(block.Statements, stmt)
		} else {
			p.skipLine()
		}
		p.nextToken()
	}

	return block
}

func (p *StmtParser) parseFuncStatement(fTok token.Token, outputs []*ast.Identifier) *ast.FuncStatement {
	// Validate function name (no __, no trailing _, no blank)
	p.validateIdentifier(fTok)
	p.errorOnBlanks()

	f := &ast.FuncStatement{
		Token:      fTok,
		Parameters: []*ast.Identifier{},
		Outputs:    outputs,
		Body: &ast.BlockStatement{
			Statements: []ast.Statement{},
		},
	}

	fp := p.parseFunctionParameters()
	if fp == nil {
		return nil
	}
	f.Parameters = fp

	if !p.peekTokenIs(token.NEWLINE) {
		p.peekError(token.NEWLINE)
		return nil
	}

	p.nextToken()
	f.Body = p.parseBlockStatement()

	return f
}

// parseIdentifiers expects curToken to be an identifier
// it checks if the remaining tokens are identifiers
// Used for code declarations (function outputs, params) where blank "_" is not allowed
func (p *StmtParser) parseIdentifiers() []*ast.Identifier {
	identifiers := []*ast.Identifier{}

	p.validateIdentifier(p.curToken)
	ident := &ast.Identifier{Token: p.curToken, Value: p.curToken.Literal}
	identifiers = append(identifiers, ident)

	for p.peekTokenIs(token.COMMA) {
		p.nextToken()
		if !p.expectPeek(token.IDENT) {
			return nil
		}
		p.validateIdentifier(p.curToken)
		ident := &ast.Identifier{Token: p.curToken, Value: p.curToken.Literal}
		identifiers = append(identifiers, ident)
	}

	p.errorOnBlanks() // code declarations cannot use blank identifiers
	return identifiers
}

func (p *StmtParser) parseFunctionParameters() []*ast.Identifier {
	if p.peekTokenIs(token.RPAREN) {
		p.nextToken()
		return []*ast.Identifier{}
	}

	if p.parenBreak() || !p.expectPeek(token.IDENT) {
		return nil
	}

	identifiers := p.parseIdentifiers()

	if p.parenBreak() || !p.expectPeek(token.RPAREN) {
		return nil
	}

	return identifiers
}

func (p *StmtParser) parseCallExpression(f ast.Expression) ast.Expression {
	ce := &ast.CallExpression{
		Token:    p.curToken,
		Function: &ast.Identifier{Token: f.Tok(), Value: f.Tok().Literal},
	}

	ce.Arguments = p.parseCallArguments()
	if ce.Arguments == nil {
		return nil
	}
	return ce
}

func (p *StmtParser) parseCallPostfix(base ast.Expression) ast.Expression {
	if _, ok := base.(*ast.Identifier); !ok {
		p.errors = append(p.errors, &token.CompileError{Token: base.Tok(), Msg: "function calls must target identifiers"})
		return nil
	}
	return p.parseCallExpression(base)
}

func (p *StmtParser) parseArrayRangePostfix(array ast.Expression) ast.Expression {
	return p.parseArrayRangeExpression(array)
}

func (p *StmtParser) parseDotPostfix(base ast.Expression) ast.Expression {
	dotTok := p.curToken
	if !p.expectPeek(token.IDENT) {
		return nil
	}
	if p.curToken.HadSpace {
		p.errors = append(p.errors, &token.CompileError{
			Token: p.curToken,
			Msg:   "no whitespace allowed after '.' in field access",
		})
		return nil
	}
	p.validateIdentifier(p.curToken)
	p.errorOnBlanks()
	return &ast.DotExpression{
		Token: dotTok,
		Left:  base,
		Field: p.curToken.Literal,
	}
}

func (p *StmtParser) parseCallArguments() []ast.Expression {
	args := []ast.Expression{}

	if p.peekTokenIs(token.RPAREN) {
		p.nextToken()
		return args
	}
	if p.parenBreak() {
		return nil
	}

	p.nextToken()
	args = append(args, p.parseExpression(LOWEST, prefixSplitNone))

	for !p.atLineEnd() && p.peekTokenIs(token.COMMA) {
		p.nextToken()
		if p.parenBreak() {
			return nil
		}
		p.nextToken()
		args = append(args, p.parseExpression(LOWEST, prefixSplitNone))
	}

	if slices.Contains(args, nil) || p.parenBreak() || !p.expectPeek(token.RPAREN) {
		return nil
	}

	return args
}

func (p *StmtParser) registerPrefix(op string, fn prefixParseFn) {
	p.prefixParseFns[op] = fn
}

func (p *StmtParser) registerInfix(op string, fn infixParseFn) {
	p.infixParseFns[op] = fn
}

func (p *StmtParser) registerPostfix(op string, fn postfixParseFn) {
	p.postfixParseFns[op] = fn
}

// validateIdentifier checks identifier naming rules per C ABI spec.
// Rules:
//   - "__" is not allowed anywhere
//   - Trailing "_" is not allowed (except for single "_")
//   - Single "_" (blank) is tracked for later decision by caller
func (p *StmtParser) validateIdentifier(tok token.Token) {
	ident := tok.Literal
	if ident == "_" {
		p.blankIdents = append(p.blankIdents, tok)
		return
	}
	if strings.Contains(ident, "__") {
		p.errors = append(p.errors, &token.CompileError{
			Token: tok,
			Msg:   "identifier cannot contain '__'",
		})
	}
	if strings.HasSuffix(ident, "_") {
		p.errors = append(p.errors, &token.CompileError{
			Token: tok,
			Msg:   "identifier cannot end with '_'",
		})
	}
}

// errorOnBlanks converts tracked blank identifiers to errors and clears the list.
func (p *StmtParser) errorOnBlanks() {
	for _, tok := range p.blankIdents {
		p.errors = append(p.errors, &token.CompileError{
			Token: tok,
			Msg:   "blank identifier '_' cannot be used as a value",
		})
	}
	p.blankIdents = nil
}

// checkNoDuplicates walks a slice of identifiers and reports
// a CompileError for each name that appears more than once.
// Blank identifiers ("_") are skipped - for let statements they're valid discards,
// and for code declarations they're already caught by parseIdentifiers/errorOnBlanks.
func (p *StmtParser) checkNoDuplicates(ids []*ast.Identifier) {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		name := id.Value
		if name == "_" {
			continue
		}
		if _, ok := seen[name]; ok {
			p.errors = append(p.errors, &token.CompileError{
				Token: id.Token,
				Msg:   fmt.Sprintf("duplicate identifier: %s in this statement", name),
			})
			continue
		}
		seen[name] = struct{}{}
	}
}
