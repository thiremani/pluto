package compiler

import (
	"slices"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/pir"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

type ScriptCompiler struct {
	Compiler      *Compiler
	Program       *ast.Program
	Script        *Script
	ScriptMangled string // immutable script-root key (_e; function keys use _f<N>)
	// Plans holds the script-root statement plans the PIR router accepted, in
	// source order; -emit-pir renders them after a successful compile.
	Plans []*pir.AssignPlan
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
	if len(cfg.Errors) > 0 {
		return cfg.Errors
	}

	c := sc.Compiler
	// Create main function
	c.addMain()
	sc.compileStatements()
	// Clean up main scope before returning
	c.cleanupScope()
	// Add explicit return 0
	c.addRet()
	return c.Errors
}

// compileStatements lowers the script's statements, routing eligible
// assignments through PIR plans.
func (sc *ScriptCompiler) compileStatements() {
	sc.Plans = sc.Compiler.lowerRoutedBody(sc.Program.Statements, "")
}

// EmittedPlans is the -emit-pir order: the script root's plans, then each
// function specialization's, in the order the specializations were first
// lowered.
func (sc *ScriptCompiler) EmittedPlans() []*pir.AssignPlan {
	return slices.Concat(sc.Plans, slices.Concat(sc.Compiler.FuncPlans...))
}
