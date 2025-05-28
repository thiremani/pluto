package compiler

import (
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
	// Create main function
	c := sc.Compiler
	mainType := llvm.FunctionType(c.Context.Int32Type(), []llvm.Type{}, false)
	mainFunc := llvm.AddFunction(c.Module, "main", mainType)
	mainBlock := c.Context.AddBasicBlock(mainFunc, "entry")
	c.builder.SetInsertPoint(mainBlock, mainBlock.FirstInstruction())

	// Compile all statements
	for _, stmt := range sc.Program.Statements {
		switch s := stmt.(type) {
		case *ast.LetStatement:
			c.compileLetStatement(s, mainFunc)
		case *ast.PrintStatement:
			c.compilePrintStatement(s)
		}
	}

	// Add explicit return 0
	c.builder.CreateRet(llvm.ConstInt(c.Context.Int32Type(), 0, false))
}
