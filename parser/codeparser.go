package parser

import (
	"fmt"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/token"
	ptypes "github.com/thiremani/pluto/types"
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
			if lit, ok := cp.structLiteralOf(s); ok {
				cp.addStructStatement(code, s, lit)
			} else {
				cp.addConstStatement(code, s)
			}
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

func (cp *CodeParser) structLiteralOf(s *ast.ConstStatement) (*ast.StructLiteral, bool) {
	if len(s.Value) != 1 {
		return nil, false
	}
	lit, ok := s.Value[0].(*ast.StructLiteral)
	return lit, ok
}

func (cp *CodeParser) validateConstBindings(code *ast.Code, s *ast.ConstStatement) int {
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
	return prevLen
}

func (cp *CodeParser) addConstBinding(code *ast.Code, s *ast.ConstStatement) {
	code.Const.Statements = append(code.Const.Statements, s)
	for _, id := range s.Name {
		code.Const.Map[id.Value] = s
	}
}

func (cp *CodeParser) addConstStatement(code *ast.Code, s *ast.ConstStatement) {
	prevLen := cp.validateConstBindings(code, s)

	if len(cp.p.errors) > prevLen {
		return
	}

	cp.addConstBinding(code, s)
}

func (cp *CodeParser) addStructStatement(code *ast.Code, s *ast.ConstStatement, lit *ast.StructLiteral) {
	prevLen := cp.validateConstBindings(code, s)
	typeName := lit.Token.Literal

	if ptypes.IsReservedTypeName(typeName) {
		cp.p.errors = append(cp.p.errors, &token.CompileError{
			Token: lit.Token,
			Msg:   fmt.Sprintf("struct type name %q is reserved", typeName),
		})
	}

	if _, exists := code.Struct.Map[typeName]; exists {
		cp.p.errors = append(cp.p.errors, &token.CompileError{
			Token: lit.Token,
			Msg:   fmt.Sprintf("struct type %s has been previously defined", typeName),
		})
	}

	if len(cp.p.errors) > prevLen {
		return
	}

	structDef := &ast.StructDef{
		Token:       lit.Token,
		Fields:      make([]string, len(lit.Headers)),
		FieldTokens: append([]token.Token(nil), lit.Headers...),
	}
	for i, h := range lit.Headers {
		structDef.Fields[i] = h.Literal
	}
	code.Struct.Map[typeName] = structDef
	code.Struct.Definitions = append(code.Struct.Definitions, structDef)
	cp.addConstBinding(code, s)
}

func (cp *CodeParser) addFuncStatement(code *ast.Code, s *ast.FuncStatement) {
	prevLen := len(cp.p.errors)

	if ptypes.IsReservedTypeName(s.Token.Literal) {
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
