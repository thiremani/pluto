package parser

import (
	"fmt"
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

	// Check for global redeclarations against the code map.
	for _, id := range s.Name {
		if _, ok := code.Const.Map[id.Value]; ok {
			msg := fmt.Sprintf("global redeclaration of constant %s", id.Value)
			ce := &token.CompileError{
				Token: id.Token,
				Msg:   msg,
			}
			cp.p.errors = append(cp.p.errors, ce)
		}
	}

	if len(cp.p.errors) > prevLen {
		// If there are errors, we don't add the statement to the code.
		return
	}

	// add the statement to the code if no errors were found
	code.Const.Statements = append(code.Const.Statements, s)
	for _, id := range s.Name {
		code.Const.Map[id.Value] = s
	}
}

func (cp *CodeParser) addFuncStatement(code *ast.Code, s *ast.FuncStatement) {
	prevLen := len(cp.p.errors)

	fKey := ast.FuncKey{
		FuncName: s.Token.Literal,
		Arity:    len(s.Parameters),
	}
	if _, ok := code.Func.Map[fKey]; ok {
		msg := fmt.Sprintf("Function %s with %d parameters has been previously defined", s.Token.Literal, len(s.Parameters))
		ce := &token.CompileError{
			Token: s.Token,
			Msg:   msg,
		}
		cp.p.errors = append(cp.p.errors, ce)
		return
	}
	// check parameters are distinct and not ""

	// Duplicate Input Parameters
	cp.p.checkNoDuplicates(s.Parameters)

	// Duplicate Output Parameters
	cp.p.checkNoDuplicates(s.Outputs)

	// Input/Output Name Conflicts (NEW & Optional but Recommended)
	paramNames := make(map[string]struct{})
	for _, p := range s.Parameters {
		paramNames[p.Value] = struct{}{}
	}
	for _, o := range s.Outputs {
		if _, exists := paramNames[o.Value]; exists {
			msg := fmt.Sprintf("identifier: %s cannot be used as both an input and an output parameter", o.Value)
			ce := &token.CompileError{Token: o.Token, Msg: msg}
			cp.p.errors = append(cp.p.errors, ce)
		}
	}

	if len(cp.p.errors) > prevLen {
		// If there are errors, we don't add the statement to the code.
		return
	}

	code.Func.Statements = append(code.Func.Statements, s)
	code.Func.Map[fKey] = s
}
