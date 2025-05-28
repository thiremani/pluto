package compiler

import (
	"github.com/thiremani/pluto/ast"
	"tinygo.org/x/go-llvm"
)

type CodeCompiler struct {
	Compiler *Compiler
	Code     *ast.Code
}

func NewCodeCompiler(ctx llvm.Context, moduleName string, code *ast.Code) *CodeCompiler {
	return &CodeCompiler{
		Compiler: NewCompiler(ctx, moduleName, nil),
		Code:     code,
	}
}

// compile compiles the constants in the AST and adds them to the compiler's symbol table.
func (cc *CodeCompiler) Compile() {
	// Compile constants
	for _, stmt := range cc.Code.Const.Statements {
		cc.Compiler.compileConstStatement(stmt)
	}
}
