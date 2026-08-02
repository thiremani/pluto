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

// ScriptCache keeps function and expression metadata on one cross-script lifetime.
type ScriptCache struct {
	FuncCache map[string]*Func
	ExprCache map[ExprKey]*ExprInfo
}

func NewScriptCache() *ScriptCache {
	return &ScriptCache{
		FuncCache: make(map[string]*Func),
		ExprCache: make(map[ExprKey]*ExprInfo),
	}
}

type Script struct {
	Name        string
	MangledPath string
	Root        *Func
}

func (s *Script) Mangle() string {
	return s.MangledPath + SEP + "script" + SEP + MangleIdent(s.Name)
}

func NewScriptCompiler(ctx llvm.Context, name string, program *ast.Program, cc *CodeCompiler, cache *ScriptCache) *ScriptCompiler {
	compiler := NewCompiler(ctx, cc.Compiler.MangledPath, cc)
	compiler.ScriptCache = cache
	script := &Script{
		Name:        name,
		MangledPath: cc.Compiler.MangledPath,
		Root: &Func{
			Name: name,
			Vars: make(map[string]Type),
		},
	}
	mangled := script.Mangle()
	compiler.FuncNameMangled = mangled
	cache.FuncCache[mangled] = script.Root
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
