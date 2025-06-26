package parser

import (
	"fmt"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/token"
	"reflect"
	"strconv"
	"unicode/utf8"
)

const (
	_ int = iota
	LOWEST
	ASSIGN      // =
	COMMA       // ,
	COLON       // :
	BITWISE_OR  // |
	BITWISE_XOR // ⊕
	BITWISE_AND // &
	SHIFT       // << >> >>>
	SUM         // +
	PRODUCT     // *
	EXP         // ^
	LESSGREATER // > or <
	PREFIX      // -X or !X
	CALL        // myFunction(X)
)

var precedences = map[string]int{
	token.SYM_ASSIGN: ASSIGN,
	token.SYM_COMMA:  COMMA,
	token.SYM_COLON:  COLON,
	token.SYM_OR:     BITWISE_OR,
	token.SYM_XOR:    BITWISE_XOR,
	token.SYM_AND:    BITWISE_AND,
	token.SYM_SHL:    SHIFT,
	token.SYM_SHR:    SHIFT,
	token.SYM_ASR:    SHIFT,
	token.SYM_ADD:    SUM,
	token.SYM_SUB:    SUM,
	token.SYM_MUL:    PRODUCT,
	token.SYM_DIV:    PRODUCT,
	token.SYM_QUO:    PRODUCT,
	token.SYM_MOD:    PRODUCT,
	token.SYM_EXP:    EXP,
	token.SYM_EQL:    LESSGREATER,
	token.SYM_LSS:    LESSGREATER,
	token.SYM_GTR:    LESSGREATER,
	token.SYM_NEQ:    LESSGREATER,
	token.SYM_LEQ:    LESSGREATER,
	token.SYM_GEQ:    LESSGREATER,
	token.SYM_LPAREN: CALL,
}

type (
	prefixParseFn func() ast.Expression
	infixParseFn  func(ast.Expression) ast.Expression
)

type StmtParser struct {
	l      *lexer.Lexer
	errors []*token.CompileError

	curToken   token.Token
	peekToken  token.Token
	savedToken token.Token

	prefixParseFns map[string]prefixParseFn
	infixParseFns  map[string]infixParseFn
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
	p.registerPrefix(token.SYM_LPAREN, p.parseGroupedExpression)

	p.infixParseFns = make(map[string]infixParseFn)
	p.registerInfix(token.SYM_COLON, p.parseInfixExpression)
	p.registerInfix(token.SYM_OR, p.parseInfixExpression)
	p.registerInfix(token.SYM_XOR, p.parseInfixExpression)
	p.registerInfix(token.SYM_AND, p.parseInfixExpression)
	p.registerInfix(token.SYM_SHL, p.parseInfixExpression)
	p.registerInfix(token.SYM_SHR, p.parseInfixExpression)
	p.registerInfix(token.SYM_ASR, p.parseInfixExpression)
	p.registerInfix(token.SYM_ADD, p.parseInfixExpression)
	p.registerInfix(token.SYM_SUB, p.parseInfixExpression)
	p.registerInfix(token.SYM_MUL, p.parseInfixExpression)
	p.registerInfix(token.SYM_DIV, p.parseInfixExpression)
	p.registerInfix(token.SYM_QUO, p.parseInfixExpression)
	p.registerInfix(token.SYM_MOD, p.parseInfixExpression)
	p.registerInfix(token.SYM_EXP, p.parseInfixExpression)
	p.registerInfix(token.SYM_EQL, p.parseInfixExpression)
	p.registerInfix(token.SYM_LSS, p.parseInfixExpression)
	p.registerInfix(token.SYM_GTR, p.parseInfixExpression)
	p.registerInfix(token.SYM_NEQ, p.parseInfixExpression)
	p.registerInfix(token.SYM_LEQ, p.parseInfixExpression)
	p.registerInfix(token.SYM_GEQ, p.parseInfixExpression)

	// Read two tokens, so curToken and peekToken are both set
	p.nextToken()
	p.nextToken()

	return p
}

func (p *StmtParser) nextToken() {
	if p.savedToken != (token.Token{}) {
		p.curToken = p.peekToken
		p.peekToken = p.savedToken
		p.savedToken = token.Token{}
		return
	}

	var err *token.CompileError
	p.curToken = p.peekToken
	p.peekToken, err = p.l.NextToken()
	if err != nil {
		p.errors = append(p.errors, err)
	}

	// Handle implicit multiplication:
	// If the current token is an INT or FLOAT and the following token is an IDENT,
	// and there is no whitespace between them (i.e., the current token's ending column
	// equals the next token's starting column), then we assume an implicit multiplication.
	// In this case, we save the IDENT token in 'savedToken', and substitute the next token
	// with a multiplication operator '*' token. This way, an input like "5var" is treated as "5 * var".
	if (p.curToken.Type == token.INT || p.curToken.Type == token.FLOAT) && p.peekToken.Type == token.IDENT && p.curToken.Column+utf8.RuneCountInString(p.curToken.Literal) == p.peekToken.Column {
		p.savedToken = p.peekToken
		p.peekToken = token.Token{
			Type:    token.OPERATOR,
			Literal: token.SYM_MUL,
			Line:    p.savedToken.Line,
			Column:  p.savedToken.Column,
		}
	}
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
	expList := p.parseExpList()

	if p.stmtEnded() {
		p.nextToken()
		return &ast.PrintStatement{
			Token:      firstToken,
			Expression: expList,
		}
	}

	if !p.expectPeek(token.ASSIGN) {
		return nil
	}

	identList, ce := p.toIdentList(expList)
	if ce != nil {
		p.errors = append(p.errors, ce)
		return nil
	}

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
	// TODO operator, function, struct definitions
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
	expList := p.parseExpList()
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

	if p.stmtEnded() {
		p.nextToken()
		return stmt
	}

	msg := fmt.Sprintf("Expected either NEWLINE or EOF token. Instead got %q", p.peekToken)
	ce := &token.CompileError{
		Token: p.curToken,
		Msg:   msg,
	}
	p.errors = append(p.errors, ce)
	return nil
}

func (p *StmtParser) isCondition(exp ast.Expression) bool {
	ie, ok := exp.(*ast.InfixExpression)
	if !ok {
		return false
	}

	return ie.Token.IsComparison()
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
	expList := []ast.Expression{p.parseExpression(LOWEST)}
	for p.peekTokenIs(token.COMMA) {
		p.nextToken()
		p.nextToken()
		expList = append(expList, p.parseExpression(LOWEST))
	}
	return expList
}

func (p *StmtParser) parseExpression(precedence int) ast.Expression {
	// ignore illegal tokens
	for p.curTokenIs(token.ILLEGAL) {
		p.illegalToken(p.curToken)
		p.nextToken()
	}

	prefix := p.prefixParseFns[p.curToken.TokenTypeWithOp()]
	if prefix == nil {
		p.noPrefixParseFnError(p.curToken)
		return nil
	}
	leftExp := prefix()

	if leftExp.Tok().Type == token.IDENT && p.peekToken.Type == token.LPAREN {
		p.nextToken()
		leftExp = p.parseCallExpression(leftExp)
	}

	for precedence < p.peekPrecedence() {
		infix := p.infixParseFns[p.peekToken.TokenTypeWithOp()]
		if infix == nil {
			return leftExp
		}
		p.nextToken()
		leftExp = infix(leftExp)
	}

	return leftExp
}

func (p *StmtParser) peekPrecedence() int {
	if p, ok := precedences[p.peekToken.TokenTypeWithOp()]; ok {
		return p
	}

	return LOWEST
}

func (p *StmtParser) curPrecedence() int {
	if p, ok := precedences[p.curToken.TokenTypeWithOp()]; ok {
		return p
	}

	return LOWEST
}

func (p *StmtParser) parseIdentifier() ast.Expression {
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

func (p *StmtParser) parsePrefixExpression() ast.Expression {
	expression := &ast.PrefixExpression{
		Token:    p.curToken,
		Operator: p.curToken.Literal,
	}

	p.nextToken()

	expression.Right = p.parseExpression(PREFIX)

	return expression
}

func (p *StmtParser) parseInfixExpression(left ast.Expression) ast.Expression {
	expression := &ast.InfixExpression{
		Token:    p.curToken,
		Operator: p.curToken.Literal,
		Left:     left,
	}

	precedence := p.curPrecedence()
	p.nextToken()
	expression.Right = p.parseExpression(precedence)

	return expression
}

func (p *StmtParser) parseGroupedExpression() ast.Expression {
	p.nextToken()

	exp := p.parseExpression(LOWEST)

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
func (p *StmtParser) parseIdentifiers() []*ast.Identifier {
	identifiers := []*ast.Identifier{}

	ident := &ast.Identifier{Token: p.curToken, Value: p.curToken.Literal}
	identifiers = append(identifiers, ident)

	for p.peekTokenIs(token.COMMA) {
		p.nextToken()
		if !p.expectPeek(token.IDENT) {
			return nil
		}
		ident := &ast.Identifier{Token: p.curToken, Value: p.curToken.Literal}
		identifiers = append(identifiers, ident)
	}

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

func (p *StmtParser) parseCallArguments() []ast.Expression {
	args := []ast.Expression{}

	if p.peekTokenIs(token.RPAREN) {
		p.nextToken()
		return args
	}

	p.nextToken()
	args = append(args, p.parseExpression(LOWEST))

	for p.peekTokenIs(token.COMMA) {
		p.nextToken()
		p.nextToken()
		args = append(args, p.parseExpression(LOWEST))
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

// checkNoDuplicates walks a slice of identifiers and returns
// a CompileError for each name that appears more than once.
// It skips blank‐identifier (“_”).
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
