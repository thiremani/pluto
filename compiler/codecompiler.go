package compiler

import (
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

type CodeCompiler struct {
	Compiler *Compiler
	ModName  string // Module name from pt.mod (e.g., "github.com/user/math")
	RelPath  string // Relative path from module root (e.g., "stats/integral")
	Code     *ast.Code
}

func NewCodeCompiler(ctx llvm.Context, modName, relPath string, code *ast.Code) *CodeCompiler {
	// Combine for display, using "$" to distinguish relPath from module path.
	// This avoids ambiguity: "mod/sub$rel" vs "mod/sub/rel$" are distinct.
	// Note: "$" is valid in LLVM identifiers (unlike ":").
	displayName := modName + "$" + relPath
	return &CodeCompiler{
		Compiler: NewCompiler(ctx, displayName, nil),
		ModName:  modName,
		RelPath:  relPath,
		Code:     code,
	}
}

// compile compiles the constants in the AST and adds them to the compiler's symbol table.
func (cc *CodeCompiler) Compile() []*token.CompileError {
	// Compile constants
	for _, stmt := range cc.Code.Const.Statements {
		cc.Compiler.compileConstStatement(stmt)
	}

	cfg := NewCFG(nil, cc)
	cfg.AnalyzeFuncs()
	cc.Compiler.Errors = append(cc.Compiler.Errors, cfg.Errors...)

	return cc.Compiler.Errors
}
