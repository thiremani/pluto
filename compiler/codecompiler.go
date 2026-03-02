package compiler

import (
	"fmt"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"github.com/thiremani/pluto/types"
	"tinygo.org/x/go-llvm"
)

type CodeCompiler struct {
	Compiler *Compiler
	Code     *ast.Code
}

func NewCodeCompiler(ctx llvm.Context, modName, relPath string, code *ast.Code) *CodeCompiler {
	mangledPath := MangleDirPath(modName, relPath)
	cc := &CodeCompiler{
		Compiler: NewCompiler(ctx, mangledPath, nil),
		Code:     code,
	}
	return cc
}

func (cc *CodeCompiler) validateStructDefs() []*token.CompileError {
	seenHeaders := make(map[string][]token.Token)
	headerMap := make(map[string]map[string]token.Token)
	errs := []*token.CompileError{}

	for _, stmt := range cc.Code.Struct.Statements {
		typeName := stmt.Value.Token.Literal
		if types.IsReservedTypeName(typeName) {
			errs = append(errs, &token.CompileError{
				Token: stmt.Value.Token,
				Msg:   fmt.Sprintf("struct type name %q is reserved", typeName),
			})
			continue
		}

		headers := stmt.Value.Headers
		schema, exists := headerMap[typeName]
		if !exists {
			if len(headers) == 0 {
				errs = append(errs, &token.CompileError{
					Token: stmt.Value.Token,
					Msg:   fmt.Sprintf("struct type %s used before definition", typeName),
				})
				continue
			}

			seenHeaders[typeName] = append([]token.Token(nil), headers...)
			schema = make(map[string]token.Token, len(headers))
			for _, header := range headers {
				schema[header.Literal] = header
			}
			headerMap[typeName] = schema
			continue
		}

		for _, header := range headers {
			if _, ok := schema[header.Literal]; ok {
				continue
			}
			errs = append(errs, &token.CompileError{
				Token: header,
				Msg:   fmt.Sprintf("unknown field %q in struct type %s", header.Literal, typeName),
			})
			break
		}
	}
	return errs
}

func (cc *CodeCompiler) validateFuncDefs() []*token.CompileError {
	errs := []*token.CompileError{}

	for _, fn := range cc.Code.Func.Statements {
		if !types.IsReservedTypeName(fn.Token.Literal) {
			continue
		}
		errs = append(errs, &token.CompileError{
			Token: fn.Token,
			Msg:   fmt.Sprintf("function name %q is reserved", fn.Token.Literal),
		})
	}

	return errs
}

// compile compiles the constants in the AST and adds them to the compiler's symbol table.
func (cc *CodeCompiler) Compile() []*token.CompileError {
	cc.Compiler.Errors = append(cc.Compiler.Errors, cc.validateStructDefs()...)
	cc.Compiler.Errors = append(cc.Compiler.Errors, cc.validateFuncDefs()...)
	if len(cc.Compiler.Errors) > 0 {
		return cc.Compiler.Errors
	}

	// Compile constants
	for _, stmt := range cc.Code.Const.Statements {
		cc.Compiler.compileConstStatement(stmt)
	}
	for _, stmt := range cc.Code.Struct.Statements {
		cc.Compiler.compileStructStatement(stmt)
	}

	cfg := NewCFG(nil, cc)
	cfg.AnalyzeFuncs()
	cc.Compiler.Errors = append(cc.Compiler.Errors, cfg.Errors...)

	return cc.Compiler.Errors
}
