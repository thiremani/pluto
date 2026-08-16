package compiler

import (
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

type ScriptCompiler struct {
	Compiler      *Compiler
	Program       *ast.Program
	Script        *Script
	ScriptMangled string // immutable script-root key (_e; function keys use _f<N>)
}

type Script struct {
	Name string
	Root *FuncInfo
}

func NewScriptCompiler(ctx llvm.Context, name string, program *ast.Program, cc *CodeCompiler) *ScriptCompiler {
	compiler := NewCompiler(ctx, cc.Compiler.MangledPath, cc)
	script := &Script{
		Name: name,
		Root: &FuncInfo{
			Sig:              Func{Name: name},
			Vars:             make(map[string]Type),
			StatementEffects: make(map[*ast.LetStatement]StatementEffect),
		},
	}
	scriptMangled := MangleScript(cc.Compiler.MangledPath, name)
	compiler.FuncNameMangled = scriptMangled
	compiler.FuncCache[scriptMangled] = script.Root
	return &ScriptCompiler{
		Compiler:      compiler,
		Program:       program,
		Script:        script,
		ScriptMangled: scriptMangled,
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
	cfg.AnalyzeScript(sc.Program.Statements, sc.Script.Root.StatementEffects)
	directCallees := collectDirectCallees(sc.Compiler, sc.ScriptMangled, sc.Program.Statements)
	cfg.Errors = replaySpecializationCFG(sc.Compiler, directCallees, cfg.Errors)
	if len(cfg.Errors) > 0 {
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

func replaySpecializationCFG(compiler *Compiler, roots []string, errors []*token.CompileError) []*token.CompileError {
	visited := make(map[string]struct{})

	for _, mangled := range roots {
		errors = replaySpecializationCFGNode(compiler, mangled, visited, errors)
	}

	return errors
}

func replaySpecializationCFGNode(compiler *Compiler, mangled string, visited map[string]struct{}, errors []*token.CompileError) []*token.CompileError {
	if _, seen := visited[mangled]; seen {
		return errors
	}
	visited[mangled] = struct{}{}

	info := compiler.FuncCache[mangled]
	if info == nil {
		panic("internal: reachable specialization missing from function cache: " + mangled)
	}
	if !info.Settled {
		panic("internal: reachable specialization is not settled: " + mangled)
	}
	if info.CFG == nil {
		panic("internal: settled specialization has no CFG result: " + mangled)
	}

	errors = append(errors, info.CFG.Errors...)
	for _, callee := range info.CFG.DirectCallees {
		errors = replaySpecializationCFGNode(compiler, callee, visited, errors)
	}

	return errors
}
