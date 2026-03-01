package parser

import (
	"fmt"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/token"
	"github.com/thiremani/pluto/types"
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
			cp.addStructStatement(code, s)
		}
		cp.p.nextToken()
	}

	if len(cp.p.errors) > 0 {
		return nil
	}
	return code
}

func (cp *CodeParser) validateConstBindings(code *ast.Code, names []*ast.Identifier) int {
	prevLen := len(cp.p.errors)
	cp.p.checkNoDuplicates(names)

	// Check for global redeclarations against all constant bindings.
	for _, id := range names {
		if _, ok := code.ConstNames[id.Value]; ok {
			msg := fmt.Sprintf("global redeclaration of constant %s", id.Value)
			ce := &token.CompileError{
				Token: id.Token,
				Msg:   msg,
			}
			cp.p.errors = append(cp.p.errors, ce)
		}
	}
	return prevLen
}

func (cp *CodeParser) addConstBinding(code *ast.Code, s *ast.ConstStatement) {
	code.Const.Statements = append(code.Const.Statements, s)
	for _, id := range s.Name {
		code.Const.Map[id.Value] = s
		code.ConstNames[id.Value] = id.Token
	}
}

func (cp *CodeParser) addStructConstBinding(code *ast.Code, s *ast.StructStatement) {
	// Keep struct-bound names in global const names so shared global-const checks
	// (writes/reads in CFG) stay consistent with regular constants.
	code.ConstNames[s.Name.Value] = s.Name.Token
}

func (cp *CodeParser) addConstStatement(code *ast.Code, s *ast.ConstStatement) {
	prevLen := cp.validateConstBindings(code, s.Name)

	if len(cp.p.errors) > prevLen {
		return
	}

	cp.addConstBinding(code, s)
}

func (cp *CodeParser) addStructStatement(code *ast.Code, s *ast.StructStatement) {
	prevLen := cp.validateConstBindings(code, []*ast.Identifier{s.Name})
	typeName := s.Value.Token.Literal

	if types.IsReservedTypeName(typeName) {
		cp.p.errors = append(cp.p.errors, &token.CompileError{
			Token: s.Value.Token,
			Msg:   fmt.Sprintf("struct type name %q is reserved", typeName),
		})
	}

	if existing, exists := code.Struct.Map[typeName]; exists && !sameStructHeaders(existing.Value.Headers, s.Value.Headers) {
		cp.p.errors = append(cp.p.errors, &token.CompileError{
			Token: s.Value.Token,
			Msg:   fmt.Sprintf("struct type %s has conflicting field headers", typeName),
		})
	}

	if len(cp.p.errors) > prevLen {
		return
	}

	code.Struct.Statements = append(code.Struct.Statements, s)
	if _, exists := code.Struct.Map[typeName]; !exists {
		code.Struct.Map[typeName] = s
	}
	cp.addStructConstBinding(code, s)
}

func sameStructHeaders(a, b []token.Token) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Literal != b[i].Literal {
			return false
		}
	}
	return true
}

func (cp *CodeParser) addFuncStatement(code *ast.Code, s *ast.FuncStatement) {
	prevLen := len(cp.p.errors)

	if types.IsReservedTypeName(s.Token.Literal) {
		ce := &token.CompileError{
			Token: s.Token,
			Msg:   fmt.Sprintf("function name %q is reserved", s.Token.Literal),
		}
		cp.p.errors = append(cp.p.errors, ce)
	}

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
	// Check no duplicates among parameters and outputs
	// The same name CAN appear in both outputs and parameters when we call the function.
	// Blanks ("_") are not allowed in function definitions.
	cp.p.checkNoDuplicates(append(s.Parameters, s.Outputs...))

	if len(cp.p.errors) > prevLen {
		// If there are errors, we don't add the statement to the code.
		return
	}

	code.Func.Statements = append(code.Func.Statements, s)
	code.Func.Map[fKey] = s
}
