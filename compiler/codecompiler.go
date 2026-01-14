package compiler

import (
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
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

// compile compiles the constants in the AST and adds them to the compiler's symbol table.
func (cc *CodeCompiler) Compile() []*token.CompileError {
	// Compile constants
	for _, stmt := range cc.Code.Const.Statements {
		cc.Compiler.compileConstStatement(stmt)
	}

	cfg := NewCFG(nil, cc)
	cfg.AnalyzeFuncs()
	cc.Compiler.Errors = append(cc.Compiler.Errors, cfg.Errors...)

	return cc.Compiler.Errors
}
