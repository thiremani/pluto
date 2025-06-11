package compiler

import (
	"github.com/thiremani/pluto/ast"
	"tinygo.org/x/go-llvm"
)

type ScriptCompiler struct {
	Compiler *Compiler
	Program  *ast.Program
}

func NewScriptCompiler(ctx llvm.Context, moduleName string, program *ast.Program, cc *CodeCompiler) *ScriptCompiler {
	return &ScriptCompiler{
		Compiler: NewCompiler(ctx, moduleName, cc),
		Program:  program,
	}
}

func (sc *ScriptCompiler) Compile() {
	c := sc.Compiler

	for _, stmt := range sc.Program.Statements {
		c.doStatement(stmt, false)
	}

	// reset symbol table
	c.Scopes = []Scope[*Symbol]{NewScope[*Symbol](FuncScope)}
	// Create main function
	c.addMain()
	for _, stmt := range sc.Program.Statements {
		c.doStatement(stmt, true)
	}

	// Add explicit return 0
	c.addRet()
}
