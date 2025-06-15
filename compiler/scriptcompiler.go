package compiler

import (
	"maps"

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
	// get output types for all functions
	ts := NewTypeSolver(sc.Program, sc.Compiler.CodeCompiler)
	ts.Solve()

	c := sc.Compiler
	// copy the output types we got into compile func cache
	maps.Copy(c.FuncCache, ts.FuncCache)

	// Create main function
	c.addMain()
	for _, stmt := range sc.Program.Statements {
		c.compileStatement(stmt)
	}
	// Add explicit return 0
	c.addRet()
}
