package compiler

import (
	"fmt"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	ptypes "github.com/thiremani/pluto/types"
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
	seen := make(map[string]token.Token)
	errs := []*token.CompileError{}

	for _, def := range cc.Code.Struct.Definitions {
		typeName := def.Token.Literal
		if ptypes.IsReservedTypeName(typeName) {
			errs = append(errs, &token.CompileError{
				Token: def.Token,
				Msg:   fmt.Sprintf("struct type name %q is reserved", typeName),
			})
			continue
		}
		if _, exists := seen[typeName]; exists {
			errs = append(errs, &token.CompileError{
				Token: def.Token,
				Msg:   fmt.Sprintf("struct type %s has been previously defined", typeName),
			})
			continue
		}
		seen[typeName] = def.Token
	}
	return errs
}

func (cc *CodeCompiler) validateFuncDefs() []*token.CompileError {
	errs := []*token.CompileError{}

	for _, fn := range cc.Code.Func.Statements {
		if !ptypes.IsReservedTypeName(fn.Token.Literal) {
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

	cfg := NewCFG(nil, cc)
	cfg.AnalyzeFuncs()
	cc.Compiler.Errors = append(cc.Compiler.Errors, cfg.Errors...)

	return cc.Compiler.Errors
}
