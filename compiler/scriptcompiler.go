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
	// Create main function
	c := sc.Compiler
	// first run to get types, run checks etc.
	for _, stmt := range sc.Program.Statements {
		c.doStatement(stmt, false)
	}

	// reset symbol table
	c.Scopes = []Scope{NewScope(FuncScope)}
	// Compile all statements
	c.addMain()
	for _, stmt := range sc.Program.Statements {
		c.doStatement(stmt, true)
	}

	// Add explicit return 0
	c.addRet()
}
