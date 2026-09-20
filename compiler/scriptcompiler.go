package compiler

import (
	"fmt"

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
	// Plans holds the statement plans the PIR router accepted, in source
	// order; -emit-pir renders them after a successful compile.
	Plans []*pir.AssignPlan
}

type Script struct {
	Name string
	Root *FuncInfo
}

type cfgDiagnosticKey struct {
	fileName string
	line     int
	column   int
	message  string
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
	cfg.Errors = replaySpecializationCFG(sc.Compiler, sc.ScriptMangled, sc.Program.Statements, cfg.Errors)
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
// assignments through PIR plans. Routing only this loop's statements makes
// the script-root context structural: function bodies never reach the router.
func (sc *ScriptCompiler) compileStatements() {
	c := sc.Compiler
	for _, stmt := range sc.Program.Statements {
		if let, isLet := stmt.(*ast.LetStatement); isLet {
			if plan, planned := c.planLetStatement(let); planned {
				pir.Elaborate(plan)
				// An invalid plan is a compiler bug. Panic to recoverICE:
				// skipping the statement would leave scope state inconsistent
				// with the solved program for everything after it.
				if err := pir.Validate(plan, planBindingCompatible); err != nil {
					panic(fmt.Sprintf("invalid plan for %q: %v", let.String(), err))
				}
				sc.Plans = append(sc.Plans, plan)
				c.lowerAssignPlan(plan)
				continue
			}
		}
		c.compileStatement(stmt)
	}
}

// replaySpecializationCFG reports the dataflow diagnostics of every
// specialization the script reaches, in the alias context each call reaches
// it with. A script call site fixes its own context from names; inside a
// callee, each nested call derives its context from the enclosing one by the
// same rule lowering applies, so a body is analyzed exactly as it is lowered.
// Each lowered variant is visited once, root-first and depth-first in source
// order, and diagnostics are deduplicated by location and message.
func replaySpecializationCFG(compiler *Compiler, scriptMangled string, statements []ast.Statement, errors []*token.CompileError) []*token.CompileError {
	walk := &cfgWalk{
		compiler: compiler,
		visited:  make(map[string]struct{}),
		reported: make(map[cfgDiagnosticKey]struct{}, len(errors)),
		errors:   errors,
	}
	for _, compileError := range errors {
		walk.reported[cfgDiagnosticKeyFor(compileError)] = struct{}{}
	}

	walk.visitSites(scriptMangled, statements, nil)
	return walk.errors
}

type cfgWalk struct {
	compiler *Compiler
	visited  map[string]struct{} // lowered variant symbols already walked
	reported map[cfgDiagnosticKey]struct{}
	errors   []*token.CompileError
}

// cfgCallSite is one call with the destinations it writes; a call nested in an
// expression, a condition, or a print writes none.
type cfgCallSite struct {
	call  *ast.CallExpression
	dests []*ast.Identifier
}

// bodyCallSites lists a body's calls in source order. A multi-valued sibling
// shifts a later call's destinations by its output count, as lowering does.
func bodyCallSites(compiler *Compiler, mangled string, statements []ast.Statement) []cfgCallSite {
	var sites []cfgCallSite
	for _, statement := range statements {
		switch stmt := statement.(type) {
		case *ast.LetStatement:
			for _, condition := range stmt.Condition {
				sites = appendNestedCallSites(sites, condition)
			}
			target := 0
			for _, value := range stmt.Value {
				if call, ok := value.(*ast.CallExpression); ok {
					sites = append(sites, cfgCallSite{call: call, dests: stmt.Name[target:]})
					for _, argument := range call.Arguments {
						sites = appendNestedCallSites(sites, argument)
					}
				} else {
					sites = appendNestedCallSites(sites, value)
				}
				target += len(compiler.ExprCache[key(mangled, value)].OutTypes)
			}
		case *ast.PrintStatement:
			for _, argument := range stmt.Expression.Arguments {
				sites = appendNestedCallSites(sites, argument)
			}
		}
	}
	return sites
}

func appendNestedCallSites(sites []cfgCallSite, expr ast.Expression) []cfgCallSite {
	for _, call := range collectExprCalls(expr) {
		sites = append(sites, cfgCallSite{call: call})
	}
	return sites
}

func (walk *cfgWalk) visitSites(callerMangled string, statements []ast.Statement, enclosing map[string]string) {
	for _, site := range bodyCallSites(walk.compiler, callerMangled, statements) {
		if _, builtin := Builtins[site.call.Function.Value]; builtin {
			continue
		}

		info := walk.compiler.ExprCache[key(callerMangled, site.call)]
		walk.visitCallee(callerMangled, site, info.CallParamTypes, enclosing)
		if info.ScalarCallVariantEnsured {
			walk.visitCallee(callerMangled, site, info.ScalarCallParamTypes, enclosing)
		}
	}
}

func (walk *cfgWalk) visitCallee(callerMangled string, site cfgCallSite, paramTypes []Type, enclosing map[string]string) {
	mangled := Mangle(walk.compiler.MangledPath, site.call.Function.Value, paramTypes)
	requireSpecializationCallTarget(walk.compiler, callerMangled, mangled)
	callee := walk.compiler.FuncCache[mangled]
	pattern := walk.sitePattern(callerMangled, site, paramTypes, callee.Sig.OutTypes, enclosing)

	variant := MangleVariant(mangled, pattern)
	if _, seen := walk.visited[variant]; seen {
		return
	}
	walk.visited[variant] = struct{}{}

	template, ok := walk.compiler.CodeCompiler.lookupFuncTemplate(callee.Sig.Name, len(callee.Sig.Params))
	if !ok {
		panic(fmt.Sprintf("internal: settled specialization %s has no template", mangled))
	}
	for _, compileError := range walk.contextErrors(template, callee, pattern, variant) {
		diagnostic := cfgDiagnosticKeyFor(compileError)
		if _, seen := walk.reported[diagnostic]; seen {
			continue
		}
		walk.reported[diagnostic] = struct{}{}
		walk.errors = append(walk.errors, compileError)
	}

	walk.visitSites(mangled, template.Body.Statements, sharedOutputs(template, pattern))
}

// sitePattern derives a call's alias pattern the way lowering will: one name
// per parameter position for plain identifier arguments, and the destinations
// by their source names.
func (walk *cfgWalk) sitePattern(callerMangled string, site cfgCallSite, paramTypes, outTypes []Type, enclosing map[string]string) []int {
	argNames := make([]string, len(paramTypes))
	position := 0
	for _, argument := range site.call.Arguments {
		if ident, ok := argument.(*ast.Identifier); ok {
			argNames[position] = ident.Value
			position++
			continue
		}
		position += len(walk.compiler.ExprCache[key(callerMangled, argument)].OutTypes)
	}
	return aliasPattern(argNames, identNames(site.dests), paramTypes, outTypes, enclosing)
}

// contextErrors returns the callee's diagnostics in one alias context,
// analyzing a shared context on first reach and caching it on the
// specialization under its variant symbol; the unshared context was
// analyzed at settlement.
func (walk *cfgWalk) contextErrors(template *ast.FuncStatement, callee *FuncInfo, pattern []int, variant string) []*token.CompileError {
	if pattern == nil {
		return callee.CFGResult.Errors
	}
	if cached, ok := callee.CFGResult.shared[variant]; ok {
		return cached
	}

	cfg := NewCFG(walk.compiler.CodeCompiler)
	cfg.AnalyzeSpecialization(template, callee, pattern)
	if callee.CFGResult.shared == nil {
		callee.CFGResult.shared = make(map[string][]*token.CompileError)
	}
	callee.CFGResult.shared[variant] = cfg.Errors
	return cfg.Errors
}

func cfgDiagnosticKeyFor(compileError *token.CompileError) cfgDiagnosticKey {
	return cfgDiagnosticKey{
		fileName: compileError.Token.FileName,
		line:     compileError.Token.Line,
		column:   compileError.Token.Column,
		message:  compileError.Msg,
	}
}
