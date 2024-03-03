package parser

import (
	"fmt"
	"pluto/ast"
	"pluto/lexer"
	"pluto/token"
	"strconv"
	"reflect"
)

const (
	_ int = iota
	LOWEST
	ASSIGN      // =
	COMMA       // ,
	SUM         // +
	PRODUCT     // *
	LESSGREATER // > or <
	PREFIX      // -X or !X
	CALL        // myFunction(X)
)

var precedences = map[token.TokenType]int{
	token.ASSIGN:   ASSIGN,
	token.COMMA:    COMMA,
	token.EQL:      LESSGREATER,
	token.NEQ:      LESSGREATER,
	token.LSS:      LESSGREATER,
	token.GTR:      LESSGREATER,
	token.ADD:      SUM,
	token.SUB:      SUM,
	token.QUO:      PRODUCT,
	token.MUL:      PRODUCT,
	token.LPAREN:   CALL,
}

type (
	prefixParseFn      func() ast.Expression
	infixParseFn       func(ast.Expression) ast.Expression
)

type Parser struct {
	l      *lexer.Lexer
	errors []string

	curToken  token.Token
	peekToken token.Token

	prefixParseFns      map[token.TokenType]prefixParseFn
	infixParseFns       map[token.TokenType]infixParseFn
}

func New(l *lexer.Lexer) *Parser {
	p := &Parser{
		l:      l,
		errors: []string{},
	}

	p.prefixParseFns = make(map[token.TokenType]prefixParseFn)
	p.registerPrefix(token.IDENT, p.parseIdentifier)
	p.registerPrefix(token.INT, p.parseIntegerLiteral)
	p.registerPrefix(token.NOT, p.parsePrefixExpression)
	p.registerPrefix(token.SUB, p.parsePrefixExpression)
	p.registerPrefix(token.LPAREN, p.parseGroupedExpression)

	p.infixParseFns = make(map[token.TokenType]infixParseFn)
	p.registerInfix(token.ADD, p.parseInfixExpression)
	p.registerInfix(token.SUB, p.parseInfixExpression)
	p.registerInfix(token.QUO, p.parseInfixExpression)
	p.registerInfix(token.MUL, p.parseInfixExpression)
	p.registerInfix(token.EQL, p.parseInfixExpression)
	p.registerInfix(token.NEQ, p.parseInfixExpression)
	p.registerInfix(token.LSS, p.parseInfixExpression)
	p.registerInfix(token.GTR, p.parseInfixExpression)

	p.registerInfix(token.LPAREN, p.parseCallExpression)

	// Read two tokens, so curToken and peekToken are both set
	p.nextToken()
	p.nextToken()

	return p
}

func (p *Parser) nextToken() {
	p.curToken = p.peekToken
	p.peekToken = p.l.NextToken()
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
	return p.errors
}

func (p *Parser) peekError(t token.TokenType) {
	msg := fmt.Sprintf("expected next token to be %s, got %s instead",
		t, p.peekToken)
	p.errors = append(p.errors, msg)
}

func (p *Parser) noPrefixParseFnError(t token.TokenType) {
	msg := fmt.Sprintf("no prefix parse function for %s found", t)
	p.errors = append(p.errors, msg)
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
			Token: firstToken,
			Expression: expList,
		}
	}

	if !p.expectPeek(token.ASSIGN) {
		return nil
	}

	stmt := &ast.LetStatement{Token: p.curToken}
	stmt.Name = p.toIdentList(expList)
	if len(stmt.Name) != len(expList) {
		idx := len(stmt.Name) + 1
		if idx < len(expList) {
			msg := fmt.Sprintf("Expected %q to be a variable name", expList[idx])
			p.errors = append(p.errors, msg)
		}
		return nil
	}

	p.nextToken()
	expList = p.parseExpList()
	if p.stmtEnded() {
		stmt.Value = expList
		return p.endStatement(stmt)
	}

	// condition expression
	for _, exp := range expList {
		if !p.isCondition(exp) {
			msg := fmt.Sprintf("Expression %q is not a condition. The main operation should be a comparison", exp.String())
			p.errors = append(p.errors, msg)
			return nil
		}
	}

	stmt.Condition = expList

	p.nextToken()
	stmt.Value = p.parseExpList()

	if p.stmtEnded() {
		return p.endStatement(stmt)
	}

	msg := fmt.Sprintf("Expected either NEWLINE or EOF token. Instead got %q", p.peekToken)
	p.errors = append(p.errors, msg)
	return nil
}

func (p *Parser) endStatement(stmt *ast.LetStatement) ast.Statement {
	// check number of identifiers and expressions are equal
	if len(stmt.Name) != len(stmt.Value) {
		msg := fmt.Sprintf("Number of variables to be assigned is %d. But number of expressions provided is %d", len(stmt.Name), len(stmt.Value))
		p.errors = append(p.errors, msg)
		return nil
	}

	p.nextToken()
	return stmt
}

func (p *Parser) isCondition(exp ast.Expression) bool {
	ie, ok := exp.(*ast.InfixExpression)
	if !ok {
		return false
	}

	return ie.Token.IsComparison()
}

func (p *Parser) toIdentList(expList []ast.Expression) []*ast.Identifier {
	identifiers := []*ast.Identifier{}
	for _, exp := range expList {
		identifier, ok := exp.(*ast.Identifier)
		if !ok {
			msg := fmt.Sprintf("expected expression to be of type %q. Instead got %q", reflect.TypeOf(identifier), reflect.TypeOf(exp))
			p.errors = append(p.errors, msg)
			break
		}
		identifiers = append(identifiers, identifier)
	}
	return identifiers
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

func (p *Parser) parseExpression(precedence int) ast.Expression {
	prefix := p.prefixParseFns[p.curToken.Type]
	if prefix == nil {
		p.noPrefixParseFnError(p.curToken.Type)
		return nil
	}
	leftExp := prefix()

	for precedence < p.peekPrecedence() {
		infix := p.infixParseFns[p.peekToken.Type]
		if infix == nil {
			return leftExp
		}
		p.nextToken()
		leftExp = infix(leftExp)
	}

	return leftExp
}

func (p *Parser) peekPrecedence() int {
	if p, ok := precedences[p.peekToken.Type]; ok {
		return p
	}

	return LOWEST
}

func (p *Parser) curPrecedence() int {
	if p, ok := precedences[p.curToken.Type]; ok {
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
		p.errors = append(p.errors, msg)
		return nil
	}

	lit.Value = value

	return lit
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

	for !p.curTokenIs(token.DEINDENT) && !p.curTokenIs(token.EOF) {
		stmt := p.parseStatement()
		if stmt != nil {
			block.Statements = append(block.Statements, stmt)
		}
		p.nextToken()
	}

	return block
}

func (p *Parser) parseFunctionLiteral() ast.Expression {
	lit := &ast.FunctionLiteral{Token: p.curToken}

	if !p.expectPeek(token.LPAREN) {
		return nil
	}

	lit.Parameters = p.parseFunctionParameters()
	if !p.expectPeek(token.NEWLINE) {
		return nil
	}
	p.nextToken()

	if !p.expectPeek(token.INDENT) {
		return nil
	}

	lit.Body = p.parseBlockStatement()

	return lit
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

func (p *Parser) parseCallExpression(function ast.Expression) ast.Expression {
	exp := &ast.CallExpression{Token: p.curToken, Function: function}
	exp.Arguments = p.parseCallArguments()
	return exp
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

func (p *Parser) registerPrefix(tokenType token.TokenType, fn prefixParseFn) {
	p.prefixParseFns[tokenType] = fn
}

func (p *Parser) registerInfix(tokenType token.TokenType, fn infixParseFn) {
	p.infixParseFns[tokenType] = fn
}