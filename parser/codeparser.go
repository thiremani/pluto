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
	hasDuplicate := false
	tmpMap := make(map[string]struct{})
	for _, id := range s.Name {
		msg := ""
		if _, ok := code.Const.Map[id.Value]; ok {
			msg = fmt.Sprintf("global redeclaration of constant %s", id.Value)
		} else if _, ok := tmpMap[id.Value]; ok {
			msg = fmt.Sprintf("duplicate identifier in this statement: %s", id.Value)
		}
		if msg != "" {
			ce := &token.CompileError{
				Token: id.Token,
				Msg:   msg,
			}
			cp.p.errors = append(cp.p.errors, ce)
			hasDuplicate = true
			continue
		}
		tmpMap[id.Value] = struct{}{}
	}
	if hasDuplicate {
		return
	}

	// Add the statement ONCE after verifying no duplicates
	code.Const.Statements = append(code.Const.Statements, s)
	for _, id := range s.Name {
		code.Const.Map[id.Value] = s
	}
}

func (cp *CodeParser) addFuncStatement(code *ast.Code, s *ast.FuncStatement) {
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
	tmpMap := make(map[string]struct{})
	goodFunc := true
	for _, p := range s.Parameters {
		if _, ok := tmpMap[p.Value]; ok {
			msg := fmt.Sprintf("Function %s has duplicate parameter %s", s.Token.Literal, p.Value)
			ce := &token.CompileError{
				Token: p.Token,
				Msg:   msg,
			}
			cp.p.errors = append(cp.p.errors, ce)
			goodFunc = false
			continue
		}
		tmpMap[p.Value] = struct{}{}
	}
	if !goodFunc {
		return
	}

	code.Func.Statements = append(code.Func.Statements, s)
	code.Func.Map[fKey] = s
}
