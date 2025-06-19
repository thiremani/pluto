package compiler

import (
	"maps"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
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

func (sc *ScriptCompiler) Compile() []*token.CompileError {
	// get output types for all functions
	ts := NewTypeSolver(sc.Program, sc.Compiler.CodeCompiler)
	errs := ts.Solve()
	if len(errs) != 0 {
		return errs
	}

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
	return c.Errors
}
