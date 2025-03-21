package parser

import (
	"fmt"
	"pluto/ast"
	"pluto/lexer"
	"pluto/token"
	"reflect"
	"strconv"
)

const (
	_ int = iota
	LOWEST
	ASSIGN      // =
	COMMA       // ,
	COLON       // :
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
	token.SYM_ADD:    SUM,
	token.SYM_SUB:    SUM,
	token.SYM_MUL:    PRODUCT,
	token.SYM_DIV:    PRODUCT,
	token.SYM_QUO:    PRODUCT,
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

type Parser struct {
	l      *lexer.Lexer
	errors []*token.CompileError

	curToken  token.Token
	peekToken token.Token
	inScript  bool
	inBlock   bool

	prefixParseFns map[string]prefixParseFn
	infixParseFns  map[string]infixParseFn
}

func New(l *lexer.Lexer, isScript bool) *Parser {
	p := &Parser{
		l:      l,
		errors: []*token.CompileError{},

		inScript: isScript,
		inBlock:  false,
	}

	p.prefixParseFns = make(map[string]prefixParseFn)
	p.registerPrefix(token.STR_IDENT, p.parseIdentifier)
	p.registerPrefix(token.STR_INT, p.parseIntegerLiteral)
	p.registerPrefix(token.STR_FLOAT, p.parseFloatLiteral)
	p.registerPrefix(token.STR_STRING, p.parseStringLiteral)
	p.registerPrefix(token.SYM_BANG, p.parsePrefixExpression)
	p.registerPrefix(token.SYM_SUB, p.parsePrefixExpression)
	p.registerPrefix(token.SYM_LPAREN, p.parseGroupedExpression)

	p.infixParseFns = make(map[string]infixParseFn)
	p.registerInfix(token.SYM_COLON, p.parseInfixExpression)
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

func (p *Parser) nextToken() {
	var err *token.CompileError
	p.curToken = p.peekToken
	p.peekToken, err = p.l.NextToken()
	if err != nil {
		p.errors = append(p.errors, err)
	}
}

func (p *Parser) curTokenIs(t token.TokenType) bool {
	return p.curToken.Type == t
}

func (p *Parser) peekTokenIs(t token.TokenType) bool {
	return p.peekToken.Type == t
}

func (p *Parser) expectPeek(t token.TokenType) bool {
	if p.peekTokenIs(t) {
		p.nextToken()
		return true
	} else {
		p.peekError(t)
		return false
	}
}

func (p *Parser) Errors() []string {
	var msgs []string
	for _, err := range p.errors {
		msgs = append(msgs, err.Error())
	}
	return msgs
}

func (p *Parser) errMsg(tokenLoc string, expToken, gotToken token.TokenType) *token.CompileError {
	msg := fmt.Sprintf("expected %s to be %s, got %s instead", tokenLoc, expToken, gotToken)
	return &token.CompileError{
		Token: p.curToken,
		Msg:   msg,
	}
}

func (p *Parser) illegalToken(t token.Token) *token.CompileError {
	msg := "Illegal token"
	return &token.CompileError{
		Token: t,
		Msg:   msg,
	}
}

func (p *Parser) peekError(t token.TokenType) {
	p.errors = append(p.errors, p.errMsg("next token", t, p.peekToken.Type))
}

func (p *Parser) curError(t token.TokenType) {
	p.errors = append(p.errors, p.errMsg("current token", t, p.curToken.Type))
}

func (p *Parser) noPrefixParseFnError(t token.Token) {
	msg := fmt.Sprintf("no prefix parse function for %s found", t.TokenTypeWithOp())
	ce := &token.CompileError{
		Token: p.curToken,
		Msg:   msg,
	}
	p.errors = append(p.errors, ce)
}

func (p *Parser) stmtEnded() bool {
	return p.peekTokenIs(token.NEWLINE) || p.peekTokenIs(token.EOF)
}

func (p *Parser) ParseProgram() *ast.Program {
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

func (p *Parser) parseStatement() ast.Statement {
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

func (p *Parser) parseLetStatement(identList []*ast.Identifier) *ast.LetStatement {
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
		return p.endLetStatement(stmt)
	}

	// condition expression
	for _, exp := range expList {
		if !p.isCondition(exp) {
			msg := fmt.Sprintf("Expression %q is not a condition. The main operation should be a comparison", exp.String())
			ce := &token.CompileError{
				Token: exp.Tok(),
				Msg:   msg,
			}
			p.errors = append(p.errors, ce)
			return nil
		}
	}

	stmt.Condition = expList

	p.nextToken()
	stmt.Value = p.parseExpList()

	if p.stmtEnded() {
		return p.endLetStatement(stmt)
	}

	msg := fmt.Sprintf("Expected either NEWLINE or EOF token. Instead got %q", p.peekToken)
	ce := &token.CompileError{
		Token: p.curToken,
		Msg:   msg,
	}
	p.errors = append(p.errors, ce)
	return nil
}

func (p *Parser) endLetStatement(stmt *ast.LetStatement) *ast.LetStatement {
	p.nextToken()
	if len(stmt.Value) == 1 {
		if f, ok := stmt.Value[0].(*ast.FunctionLiteral); ok {
			return p.parseCompleteFunction(stmt, f)
		}
	}
	// check number of identifiers and expressions are equal
	if len(stmt.Name) != len(stmt.Value) {
		msg := fmt.Sprintf("Number of variables to be assigned is %d. But number of expressions provided is %d", len(stmt.Name), len(stmt.Value))
		ce := &token.CompileError{
			Token: stmt.Token,
			Msg:   msg,
		}
		p.errors = append(p.errors, ce)
		return nil
	}

	return stmt
}

func (p *Parser) parseCompleteFunction(stmt *ast.LetStatement, f *ast.FunctionLiteral) *ast.LetStatement {
	f.Outputs = stmt.Name
	p.inBlock = true
	f.Body = p.parseBlockStatement()
	return stmt
}

func (p *Parser) isCondition(exp ast.Expression) bool {
	ie, ok := exp.(*ast.InfixExpression)
	if !ok {
		return false
	}

	return ie.Token.IsComparison()
}

func (p *Parser) toIdentList(expList []ast.Expression) ([]*ast.Identifier, *token.CompileError) {
	identifiers := []*ast.Identifier{}
	var ce *token.CompileError
	for _, exp := range expList {
		identifier, ok := exp.(*ast.Identifier)
		if !ok {
			msg := fmt.Sprintf("expected expression to be of type %q. Instead got %q", reflect.TypeOf(identifier), reflect.TypeOf(exp))
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

func (p *Parser) parseExpList() []ast.Expression {
	expList := []ast.Expression{p.parseExpression(LOWEST)}
	for p.peekTokenIs(token.COMMA) {
		p.nextToken()
		p.nextToken()
		expList = append(expList, p.parseExpression(LOWEST))
	}
	return expList
}

func (p *Parser) parseFunction(f ast.Expression) ast.Expression {
	// is function defintion or function call
	if p.inBlock || p.inScript {
		return p.parseCallExpression(f)
	}

	// TODO check this later
	// Actually if function is previously defined then we should be in script mode
	// We should also check on the next line there is no indentation

	return p.parseFunctionLiteral(f)
}

func (p *Parser) parseExpression(precedence int) ast.Expression {
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
		leftExp = p.parseFunction(leftExp)
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

func (p *Parser) peekPrecedence() int {
	if p, ok := precedences[p.peekToken.TokenTypeWithOp()]; ok {
		return p
	}

	return LOWEST
}

func (p *Parser) curPrecedence() int {
	if p, ok := precedences[p.curToken.TokenTypeWithOp()]; ok {
		return p
	}

	return LOWEST
}

func (p *Parser) parseIdentifier() ast.Expression {
	return &ast.Identifier{Token: p.curToken, Value: p.curToken.Literal}
}

func (p *Parser) parseIntegerLiteral() ast.Expression {
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

func (p *Parser) parseFloatLiteral() ast.Expression {
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

func (p *Parser) parseStringLiteral() ast.Expression {
	return &ast.StringLiteral{
		Token: p.curToken,
		Value: p.curToken.Literal,
	}
}

func (p *Parser) parsePrefixExpression() ast.Expression {
	expression := &ast.PrefixExpression{
		Token:    p.curToken,
		Operator: p.curToken.Literal,
	}

	p.nextToken()

	expression.Right = p.parseExpression(PREFIX)

	return expression
}

func (p *Parser) parseInfixExpression(left ast.Expression) ast.Expression {
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

func (p *Parser) parseGroupedExpression() ast.Expression {
	p.nextToken()

	exp := p.parseExpression(LOWEST)

	if !p.expectPeek(token.RPAREN) {
		return nil
	}

	return exp
}

func (p *Parser) parseBlockStatement() *ast.BlockStatement {
	block := &ast.BlockStatement{Token: p.curToken}
	block.Statements = []ast.Statement{}

	p.nextToken()
	if !p.curTokenIs(token.INDENT) {
		p.curError(token.INDENT)
	}
	p.nextToken()

	for !p.curTokenIs(token.DEINDENT) && !p.curTokenIs(token.EOF) {
		stmt := p.parseStatement()
		if stmt != nil {
			block.Statements = append(block.Statements, stmt)
		}
		p.nextToken()
	}

	p.inBlock = false
	return block
}

func (p *Parser) parseFunctionLiteral(f ast.Expression) ast.Expression {
	fl := &ast.FunctionLiteral{
		Token:      f.Tok(),
		Parameters: []*ast.Identifier{},
		Outputs:    []*ast.Identifier{},
		Body: &ast.BlockStatement{
			Token:      p.curToken,
			Statements: []ast.Statement{},
		},
	}

	fl.Parameters = p.parseFunctionParameters()

	if !p.peekTokenIs(token.NEWLINE) {
		p.peekError(token.NEWLINE)
		return nil
	}

	// TODO either handle block statement below or remove line below
	// lit.Body = p.parseBlockStatement()

	return fl
}

func (p *Parser) parseIdentifiers() []*ast.Identifier {
	identifiers := []*ast.Identifier{}
	ident := &ast.Identifier{Token: p.curToken, Value: p.curToken.Literal}
	identifiers = append(identifiers, ident)

	for p.peekTokenIs(token.COMMA) {
		p.nextToken()
		p.nextToken()
		ident := &ast.Identifier{Token: p.curToken, Value: p.curToken.Literal}
		identifiers = append(identifiers, ident)
	}

	return identifiers
}

func (p *Parser) parseFunctionParameters() []*ast.Identifier {
	identifiers := []*ast.Identifier{}

	if p.peekTokenIs(token.RPAREN) {
		p.nextToken()
		return identifiers
	}

	p.nextToken()
	identifiers = p.parseIdentifiers()

	if !p.expectPeek(token.RPAREN) {
		return nil
	}

	return identifiers
}

func (p *Parser) parseCallExpression(f ast.Expression) ast.Expression {
	ce := &ast.CallExpression{
		Token:    p.curToken,
		Function: &ast.Identifier{Token: f.Tok(), Value: f.Tok().Literal},
	}

	ce.Arguments = p.parseCallArguments()
	return ce
}

func (p *Parser) parseCallArguments() []ast.Expression {
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

func (p *Parser) registerPrefix(op string, fn prefixParseFn) {
	p.prefixParseFns[op] = fn
}

func (p *Parser) registerInfix(op string, fn infixParseFn) {
	p.infixParseFns[op] = fn
}
