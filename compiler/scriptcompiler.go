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

func NewScriptCompiler(ctx llvm.Context, program *ast.Program, cc *CodeCompiler, funcCache map[string]*Func, exprCache map[ExprKey]*ExprInfo) *ScriptCompiler {
	compiler := NewCompiler(ctx, cc.Compiler.MangledPath, cc)
	compiler.FuncCache = funcCache
	compiler.ExprCache = exprCache
	return &ScriptCompiler{
		Compiler: compiler,
		Program:  program,
	}
}

// validateReservedNames rejects script variable names that collide with built-in type names.
func (sc *ScriptCompiler) validateReservedNames() {
	for _, stmt := range sc.Program.Statements {
		ls, ok := stmt.(*ast.LetStatement)
		if !ok {
			continue
		}
		for _, id := range ls.Name {
			sc.Compiler.rejectReservedName(id.Token, "variable")
		}
	}
}

func (sc *ScriptCompiler) Compile() []*token.CompileError {
	sc.validateReservedNames()
	if len(sc.Compiler.Errors) > 0 {
		return sc.Compiler.Errors
	}

	// get output types for all functions
	ts := NewTypeSolver(sc)
	ts.Solve()
	if len(ts.Errors) != 0 {
		return ts.Errors
	}

	cfg := NewCFG(sc, sc.Compiler.CodeCompiler)
	cfg.Analyze(sc.Program.Statements)
	if len(cfg.Errors) != 0 {
		// return any data‐flow errors (use‐before‐def, dead stores, etc.)
		return cfg.Errors
	}

	c := sc.Compiler
	// Create main function
	c.addMain()
	for _, stmt := range sc.Program.Statements {
		c.compileStatement(stmt)
	}
	// Clean up main scope before returning
	c.cleanupScope()
	// Add explicit return 0
	c.addRet()
	return c.Errors
}
