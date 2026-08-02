package compiler

import (
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

type ScriptCompiler struct {
	Compiler *Compiler
	Program  *ast.Program
	Script   *Script
}

// CompileInfo is shared by every script compiled with one CodeCompiler.
type CompileInfo struct {
	FuncCache map[string]*FuncInfo
	ExprCache map[ExprKey]*ExprInfo
}

type Script struct {
	Name        string
	MangledPath string
	Root        *FuncInfo
}

func (s *Script) Mangle() string {
	return MangleScript(s.MangledPath, s.Name)
}

func NewScriptCompiler(ctx llvm.Context, name string, program *ast.Program, cc *CodeCompiler) *ScriptCompiler {
	compiler := NewCompiler(ctx, cc.Compiler.MangledPath, cc)
	script := &Script{
		Name:        name,
		MangledPath: cc.Compiler.MangledPath,
		Root: &FuncInfo{
			Sig:  Func{Name: name},
			Vars: make(map[string]Type),
		},
	}
	mangled := script.Mangle()
	compiler.FuncNameMangled = mangled
	compiler.compileInfo.FuncCache[mangled] = script.Root
	return &ScriptCompiler{
		Compiler: compiler,
		Program:  program,
		Script:   script,
	}
}

func (sc *ScriptCompiler) Compile() []*token.CompileError {
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
