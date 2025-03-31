package parser

import (
	"pluto/ast"
	"pluto/lexer"
	"pluto/token"
)

type SubMode int

const (
	// sub modes
	SubModeNone = iota // if parsing gives an error
	Const
	Operator
	Function
	Struct
)

type CodeParser struct {
	p *StmtParser
}

func NewCodeParser(l *lexer.Lexer) *CodeParser {
	return &CodeParser{
		p: New(l),
	}
}

func (cp *CodeParser) Errors() []string {
	return cp.p.Errors()
}

func (cp *CodeParser) Parse() *ast.Program {
	program := &ast.Program{}
	program.Statements = []ast.Statement{}
	for !cp.p.curTokenIs(token.EOF) {
		stmt, _ := cp.p.parseCodeStatement()
		if stmt != nil {
			program.Statements = append(program.Statements, stmt)
		}
		cp.p.nextToken()
	}

	return program
}

func (cp *CodeParser) parseConstants(program *ast.Program) {

}

func (cp *CodeParser) parseOperators(program *ast.Program) {
}

func (cp *CodeParser) parseFunctions(program *ast.Program) {
}

func (cp *CodeParser) parseStructs(program *ast.Program) {

}
