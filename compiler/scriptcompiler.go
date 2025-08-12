package compiler

import (
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

type ScriptCompiler struct {
	Compiler *Compiler
	Program  *ast.Program
}

func NewScriptCompiler(ctx llvm.Context, moduleName string, program *ast.Program, cc *CodeCompiler, funcCache map[string]*Func) *ScriptCompiler {
	compiler := NewCompiler(ctx, moduleName, cc)
	compiler.FuncCache = funcCache
	return &ScriptCompiler{
		Compiler: compiler,
		Program:  program,
	}
}

func (sc *ScriptCompiler) Compile() []*token.CompileError {
	// get output types for all functions
	ts := NewTypeSolver(sc)
	ts.Solve()
	if len(ts.Errors) != 0 {
		return ts.Errors
	}

	cfg := NewCFG(sc.Compiler.CodeCompiler)
	cfg.Analyze(sc.Program.Statements)
	if len(cfg.Errors) != 0 {
		// return any data‐flow errors (use‐before‐def, dead stores, etc.)
		return cfg.Errors
	}

	c := sc.Compiler
	c.Ranges = ts.Ranges
	// Create main function
	c.addMain()
	for _, stmt := range sc.Program.Statements {
		c.compileStatement(stmt)
	}
	// Add explicit return 0
	c.addRet()
	return c.Errors
}
