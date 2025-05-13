package parser

import (
	"pluto/ast"
	"pluto/lexer"
	"pluto/token"
)

type SubMode int

const (
	// sub modes
	None = iota // if parsing gives an error
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

func (cp *CodeParser) Parse() *ast.Code {
	code := &ast.Code{}
	for !cp.p.curTokenIs(token.EOF) {
		stmt, _ := cp.p.parseCodeStatement()
		if stmt != nil {
			switch s := stmt.(type) {
			case *ast.ConstStatement:
				code.Const.Statements = append(code.Const.Statements, s)
				// handle op, func, struct, member func
			}
		}
		cp.p.nextToken()
	}

	return code
}
