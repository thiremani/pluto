package parser

import (
	"fmt"
	"reflect"
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
	p.registerPrefix(token.SYM_SUB, p.parsePrefixExpression)
	p.registerPrefix(token.SYM_TILDE, p.parsePrefixExpression)
	p.registerPrefix(token.SYM_SQRT, p.parsePrefixExpression)
	p.registerPrefix(token.SYM_CBRT, p.parsePrefixExpression)
	p.registerPrefix(token.SYM_FTHRT, p.parsePrefixExpression)
	p.registerPrefix(token.SYM_LPAREN, p.parseGroupedExpression)
	p.registerPrefix(token.SYM_LBRACK, p.parseArrayLiteral)

	p.infixParseFns = make(map[string]infixParseFn)
	p.registerInfix(token.SYM_COLON, p.parseRangeLiteral)

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
		var err *token.CompileError
		p.peekToken, err = p.l.NextToken()
		if err != nil {
			p.errors = append(p.errors, err)
		}
	}

	p.handleImplicitMult()
}

// peekNextToken looks ahead to see what token comes after peekToken.
// If not available in savedTokens, it loads it from the lexer.
func (p *StmtParser) peekNextToken() token.Token {
	if len(p.savedTokens) > 0 {
		return p.savedTokens[0]
	}
	nextTok, err := p.l.NextToken()
	if err != nil {
		p.errors = append(p.errors, err)
		return token.Token{Type: token.ILLEGAL}
	}
	p.savedTokens = append(p.savedTokens, nextTok)
	return nextTok
}

// Handle implicit multiplication:
// If the current token is an INT or FLOAT and the following token is an IDENT,
// and there is no whitespace between them (i.e., the current token's ending column
// equals the next token's starting column), then we assume an implicit multiplication.
// In this case, we save the IDENT token in 'savedToken', and substitute the next token
// with an implicit multiplication operator '⋅' token. This way, an input like "5var" is
// treated as "5 ⋅ var" with higher precedence than regular multiplication.
func (p *StmtParser) handleImplicitMult() {
	// Check: number followed immediately by identifier
	isNumber := p.curToken.Type == token.INT || p.curToken.Type == token.FLOAT
	isIdentNext := p.peekToken.Type == token.IDENT
	noSpace := p.curToken.Column+utf8.RuneCountInString(p.curToken.Literal) == p.peekToken.Column

	if !isNumber || !isIdentNext || !noSpace {
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
		stmt := p.parseStatement()
		if stmt != nil {
			program.Statements = append(program.Statements, stmt)
		}
		p.nextToken()
	}

	return program
}

func (p *StmtParser) parseStatement() ast.Statement {
	firstToken := p.curToken
	p.blankIdents = nil // reset for new statement
	expList := p.parseExpList()

	if p.stmtEnded() {
		p.nextToken()
		// Print statement - blanks not allowed
		p.errorOnBlanks()
		return &ast.PrintStatement{
			Token:      firstToken,
			Expression: expList,
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
	return p.parseLetStatement(identList)
}

func (p *StmtParser) parseCodeStatement() ast.Statement {
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

	if p.peekToken.IsConstant() {
		s := p.parseConstStatement(idents)
		// below code is needed as we should return nil if ConstStatement is nil, and not interface to nil
		if s != nil {
			return s
		}
		return nil
	}

	if p.peekTokenIs(token.IDENT) {
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
	}
	// TODO operator, struct definitions
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

func (p *StmtParser) conditionsOk(expList []ast.Expression) bool {
	for _, exp := range expList {
		if p.isCondition(exp) {
			continue
		}
		msg := fmt.Sprintf("Expression %q is not a condition. The main operation should be a comparison", exp.String())
		ce := &token.CompileError{
			Token: exp.Tok(),
			Msg:   msg,
		}
		p.errors = append(p.errors, ce)
	}
	return len(p.errors) == 0
}

func (p *StmtParser) parseLetStatement(identList []*ast.Identifier) *ast.LetStatement {
	stmt := &ast.LetStatement{
		Token:     p.curToken,
		Name:      identList,
		Value:     []ast.Expression{},
		Condition: []ast.Expression{},
	}

	p.nextToken()
	// Allow line breaks/indentation between '=' and the first RHS expression
	// so multi-line constructs (arrays, grouped expressions) work naturally.
	p.skipArrayFormatting()
	expList := p.parseExpList()
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

	if !p.conditionsOk(expList) {
		return nil
	}

	stmt.Condition = expList

	p.nextToken()
	stmt.Value = p.parseExpList()
	p.errorOnBlanks()

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
	return exp.Tok().IsComparison()
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

func (p *StmtParser) parseExpList() []ast.Expression {
	expList := []ast.Expression{p.parseExpression(LOWEST, false)}
	for p.peekTokenIs(token.COMMA) {
		p.nextToken()
		p.nextToken()
		expList = append(expList, p.parseExpression(LOWEST, false))
	}
	return expList
}

func (p *StmtParser) parseExpression(precedence float64, spacesMatter bool) ast.Expression {
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

	return p.parseExpressionTail(precedence, spacesMatter, leftExp)
}

// parseExpressionTail continues parsing an expression using precedence climbing.
// It handles infix/postfix operators and, when spacesMatter is true, uses spacing
// to disambiguate operators that can be both prefix and infix (e.g., `-` for subtraction vs negation).
//
// Parameters:
//   - precedence: minimum binding power - stops when next operator has lower precedence
//   - spacesMatter: if true, uses spacing to decide when to start new array elements
//   - left: the left-hand expression already parsed
//
// Spacing rules (when spacesMatter is true):
//   - `a - b` (space before and after `-`) → subtraction (infix)
//   - `a -b` (space before, not after `-`) → two elements: a and -b (prefix)
//   - `a-b` (no space before `-`) → subtraction (infix, normal precedence)
//
// This allows natural array syntax like `[1 -2 3]` for `[1, -2, 3]` while
// still supporting `[1 - 2 3]` for `[-1, 3]`.
func (p *StmtParser) parseExpressionTail(precedence float64, spacesMatter bool, left ast.Expression) ast.Expression {
	for {
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

		// When spaces matter, check if operator should start a new element
		// If operator has space before it but not after (e.g., `a -b`),
		// it starts a new element as a prefix operator
		if spacesMatter && p.peekToken.HadSpace {
			peekNextTok := p.peekNextToken()
			if !peekNextTok.HadSpace {
				// Operator is directly attached to next token → prefix (new element)
				break
			}
		}

		// Process as infix operator
		p.nextToken()
		left = infix(left)
	}
	return left
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
	idx := p.parseExpression(LOWEST, false)
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

	value, err := strconv.ParseInt(p.curToken.Literal, 0, 64)
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

func (p *StmtParser) parseFloatLiteral() ast.Expression {
	lit := &ast.FloatLiteral{Token: p.curToken}
	value, err := strconv.ParseFloat(p.curToken.Literal, 64)
	if err != nil {
		msg := fmt.Sprintf("could not parse %q as float", p.curToken.Literal)
		ce := &token.CompileError{Token: p.curToken, Msg: msg}
		p.errors = append(p.errors, ce)
		return nil
	}

	lit.Value = value
	return lit
}

func (p *StmtParser) parseStringLiteral() ast.Expression {
	return &ast.StringLiteral{
		Token: p.curToken,
		Value: p.curToken.Literal,
	}
}

func (p *StmtParser) parseArrayLiteral() ast.Expression {
	arr := &ast.ArrayLiteral{
		Token:   p.curToken, // the '[' token
		Headers: []string{},
		Rows:    [][]ast.Expression{},
		Indices: make(map[string][]int),
	}

	p.nextToken() // consume the '[' token

	for p.curTokenIs(token.NEWLINE) {
		p.nextToken()
	}

	// Skip any whitespace/indentation after opening bracket
	p.skipArrayFormatting()

	// Parse headers if present
	if p.curTokenIs(token.COLON) {
		p.nextToken() // consume ':'
		if !p.parseHeader(arr) {
			return nil
		}
	}

	// Parse rows
	for !p.curTokenIs(token.RBRACK) && !p.curTokenIs(token.EOF) {
		p.skipArrayFormatting()

		if p.curTokenIs(token.RBRACK) {
			break
		}

		row := p.parseRow()
		if len(row) > 0 {
			arr.Rows = append(arr.Rows, row)
		}
	}

	if !p.curTokenIs(token.RBRACK) {
		p.errors = append(p.errors, &token.CompileError{
			Token: p.curToken,
			Msg:   "expected ']' to close array literal",
		})
		return nil
	}
	// Do not consume the closing ']' here. Align with grouped-expression
	// behavior and leave curToken at the closing token; callers (statement
	// parsing) will advance past newline/EOF as appropriate.
	return arr
}

func (p *StmtParser) skipArrayFormatting() {
	for p.curTokenIs(token.NEWLINE) ||
		p.curTokenIs(token.INDENT) ||
		p.curTokenIs(token.DEINDENT) {
		p.nextToken()
	}
}

// parseHeader parses column headers after ':'
func (p *StmtParser) parseHeader(arr *ast.ArrayLiteral) bool {
	for !p.curTokenIs(token.RBRACK) && !p.curTokenIs(token.EOF) && !p.curTokenIs(token.NEWLINE) {
		// handle line continuation first
		if p.curTokenIs(token.BACKSLASH) && p.peekTokenIs(token.NEWLINE) {
			// Line continuation for long headers
			p.nextToken()
			p.nextToken()
			continue
		}

		if p.curTokenIs(token.IDENT) {
			p.validateIdentifier(p.curToken)
			arr.Headers = append(arr.Headers, p.curToken.Literal)
			p.nextToken()
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
	return true
}

// parseRow parses a single data row and returns it
func (p *StmtParser) parseRow() []ast.Expression {
	row := []ast.Expression{}

	// Parse elements in this row until newline or ']'
	for !p.curTokenIs(token.RBRACK) && !p.curTokenIs(token.EOF) && !p.curTokenIs(token.NEWLINE) {
		// handle line continuation first
		if p.curTokenIs(token.BACKSLASH) && p.peekTokenIs(token.NEWLINE) {
			// Line continuation for long rows
			p.nextToken()
			p.nextToken()
			p.skipArrayFormatting() // Skip any indentation on continued line
			continue
		}

		expr := p.parseExpression(LOWEST, true)
		if expr != nil {
			row = append(row, expr)
		}

		// Advance to next token for the next element
		p.nextToken()
	}

	return row
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

	rl.Stop = p.parseExpression(precedence, false)
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
		rl.Step = p.parseExpression(precedence, false)
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

	expression.Right = p.parseExpression(PREFIX, false)

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

	p.nextToken()
	expression.Right = p.parseExpression(rbp, false)

	return expression
}

func (p *StmtParser) parseGroupedExpression() ast.Expression {
	p.nextToken()

	exp := p.parseExpression(LOWEST, false)

	if !p.expectPeek(token.RPAREN) {
		return nil
	}

	return exp
}

// assumes current token is token.NEWLINE
func (p *StmtParser) parseBlockStatement() *ast.BlockStatement {
	if !p.peekTokenIs(token.INDENT) {
		p.peekError(token.INDENT)
	}
	p.nextToken()
	p.nextToken()
	block := &ast.BlockStatement{Token: p.curToken}
	block.Statements = []ast.Statement{}

	for !p.curTokenIs(token.DEINDENT) && !p.curTokenIs(token.EOF) {
		stmt := p.parseStatement()
		if stmt != nil {
			block.Statements = append(block.Statements, stmt)
		}
		p.nextToken()
	}

	return block
}

func (p *StmtParser) parseFuncStatement(fTok token.Token, outputs []*ast.Identifier) *ast.FuncStatement {
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

	if !p.expectPeek(token.IDENT) {
		return nil
	}

	identifiers := p.parseIdentifiers()

	if !p.expectPeek(token.RPAREN) {
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
	return ce
}

func (p *StmtParser) parseCallPostfix(base ast.Expression) ast.Expression {
	if _, ok := base.(*ast.Identifier); !ok {
		p.errors = append(p.errors, &token.CompileError{Token: base.Tok(), Msg: "function calls must target identifiers"})
		return base
	}
	return p.parseCallExpression(base)
}

func (p *StmtParser) parseArrayRangePostfix(array ast.Expression) ast.Expression {
	return p.parseArrayRangeExpression(array)
}

func (p *StmtParser) parseCallArguments() []ast.Expression {
	args := []ast.Expression{}

	if p.peekTokenIs(token.RPAREN) {
		p.nextToken()
		return args
	}

	p.nextToken()
	args = append(args, p.parseExpression(LOWEST, false))

	for p.peekTokenIs(token.COMMA) {
		p.nextToken()
		p.nextToken()
		args = append(args, p.parseExpression(LOWEST, false))
	}

	if !p.expectPeek(token.RPAREN) {
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
