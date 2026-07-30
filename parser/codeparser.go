package parser

import (
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/token"
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
	code := ast.NewCode()
	for !cp.p.curTokenIs(token.EOF) {
		stmt := cp.p.parseCodeStatement()
		if stmt == nil {
			cp.p.nextToken()
			continue
		}

		switch s := stmt.(type) {
		case *ast.ConstStatement:
			cp.addConstStatement(code, s)
		case *ast.FuncStatement:
			cp.addFuncStatement(code, s)
		case *ast.StructStatement:
			code.Statements = append(code.Statements, s)
		}
		cp.p.nextToken()
	}

	if len(cp.p.errors) > 0 {
		return nil
	}
	return code
}

func (cp *CodeParser) addConstStatement(code *ast.Code, s *ast.ConstStatement) {
	prevLen := len(cp.p.errors)
	cp.p.checkNoDuplicates(s.Name)

	if len(cp.p.errors) > prevLen {
		return
	}

	code.Statements = append(code.Statements, s)
}

func (cp *CodeParser) addFuncStatement(code *ast.Code, s *ast.FuncStatement) {
	prevLen := len(cp.p.errors)

	// Check no duplicates among parameters and outputs
	// The same name CAN appear in both outputs and parameters when we call the function.
	// Blanks ("_") are not allowed in function definitions.
	cp.p.checkNoDuplicates(append(s.Parameters, s.Outputs...))

	if len(cp.p.errors) > prevLen {
		// If there are errors, we don't add the statement to the code.
		return
	}

	code.Statements = append(code.Statements, s)
}
