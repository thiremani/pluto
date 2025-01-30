package compiler

import (
	"pluto/ast"
	"fmt"
	"tinygo.org/x/go-llvm"
)

type Compiler struct {
	context    llvm.Context
	module     llvm.Module
	builder    llvm.Builder
	symbols    map[string]llvm.Value
	funcParams map[string][]llvm.Value
}

func NewCompiler(moduleName string) *Compiler {
	context := llvm.NewContext()
	module := context.NewModule(moduleName)
	builder := context.NewBuilder()
	
	return &Compiler{
		context:    context,
		module:     module,
		builder:    builder,
		symbols:    make(map[string]llvm.Value),
		funcParams: make(map[string][]llvm.Value),
	}
}

func (c *Compiler) Compile(program *ast.Program) {
	// Create main function
	voidType := c.context.VoidType()
	mainType := llvm.FunctionType(voidType, []llvm.Type{}, false)
	mainFunc := llvm.AddFunction(c.module, "main", mainType)
	mainBlock := c.context.AddBasicBlock(mainFunc, "entry")
	c.builder.SetInsertPoint(mainBlock, mainBlock.FirstInstruction())

	// Compile all statements
	for _, stmt := range program.Statements {
		switch s := stmt.(type) {
		case *ast.LetStatement:
			c.compileLetStatement(s, mainFunc)
		case *ast.PrintStatement:
			c.compilePrintStatement(s)
		}
	}

	// Add return instruction
	c.builder.CreateRetVoid()
}

func (c *Compiler) compileLetStatement(stmt *ast.LetStatement, fn llvm.Value) {
	// Handle conditions
	var cond llvm.Value
	for i, expr := range stmt.Condition {
		condVal := c.compileExpression(expr)
		if i == 0 {
			cond = condVal
		} else {
			cond = c.builder.CreateAnd(cond, condVal, "and_cond")
		}
	}

	// Create basic blocks for conditional flow
    ifBlock := c.context.AddBasicBlock(fn, "if_block")
    elseBlock := c.context.AddBasicBlock(fn, "else_block")
    contBlock := c.context.AddBasicBlock(fn, "cont_block")

	c.builder.CreateCondBr(cond, ifBlock, elseBlock)

	// True block (assign values)
	c.builder.SetInsertPoint(ifBlock, ifBlock.FirstInstruction())
	for i, ident := range stmt.Name {
		val := c.compileExpression(stmt.Value[i])
		c.symbols[ident.Value] = val
	}
	c.builder.CreateBr(contBlock)

	// False block (no assignment)
	c.builder.SetInsertPoint(elseBlock, elseBlock.FirstInstruction())
	c.builder.CreateBr(contBlock)

	// Continue block
	c.builder.SetInsertPoint(contBlock, contBlock.FirstInstruction())
}

func (c *Compiler) compileExpression(expr ast.Expression) llvm.Value {
	switch e := expr.(type) {
	case *ast.Identifier:
		return c.compileIdentifier(e)
	case *ast.IntegerLiteral:
		return llvm.ConstInt(c.context.Int64Type(), uint64(e.Value), false)
	case *ast.InfixExpression:
		return c.compileInfixExpression(e)
	case *ast.FunctionLiteral:
		return c.compileFunctionLiteral(e)
	case *ast.CallExpression:
		return c.compileCallExpression(e)
	}
	panic("unknown expression type")
}

func (c *Compiler) compileIdentifier(ident *ast.Identifier) llvm.Value {
	if val, ok := c.symbols[ident.Value]; ok {
		return val
	}
	panic(fmt.Sprintf("undefined variable: %s", ident.Value))
}

func (c *Compiler) compileInfixExpression(expr *ast.InfixExpression) llvm.Value {
	left := c.compileExpression(expr.Left)
	right := c.compileExpression(expr.Right)

	switch expr.Operator {
	case "+":
		return c.builder.CreateAdd(left, right, "add_tmp")
	case "-":
		return c.builder.CreateSub(left, right, "sub_tmp")
	case "*":
		return c.builder.CreateMul(left, right, "mul_tmp")
	case "/":
		return c.builder.CreateSDiv(left, right, "div_tmp")
	case ">":
		return c.builder.CreateICmp(llvm.IntSGT, left, right, "cmp_tmp")
	case "<":
		return c.builder.CreateICmp(llvm.IntSLT, left, right, "cmp_tmp")
	case "==":
		return c.builder.CreateICmp(llvm.IntEQ, left, right, "eq_tmp")
	}
	panic("unknown operator")
}

func (c *Compiler) compileFunctionLiteral(fn *ast.FunctionLiteral) llvm.Value {
	// Create function type
	returnType := c.context.VoidType()
	paramTypes := make([]llvm.Type, len(fn.Parameters))
	for i := range fn.Parameters {
		paramTypes[i] = c.context.Int64Type()
	}
	
	funcType := llvm.FunctionType(returnType, paramTypes, false)
	function := llvm.AddFunction(c.module, fn.Token.Literal, funcType)

	// Create entry block
	entry := c.context.AddBasicBlock(function, "entry")
	c.builder.SetInsertPoint(entry, entry.FirstInstruction())

	// Store parameters
	for i, param := range fn.Parameters {
		alloca := c.createEntryBlockAlloca(function, param.Value)
		c.builder.CreateStore(function.Params()[i], alloca)
		c.symbols[param.Value] = alloca
	}

	// Compile function body
	c.compileBlockStatement(fn.Body)
	c.builder.CreateRetVoid()
	
	return function
}

func (c *Compiler) compileBlockStatement(bs *ast.BlockStatement) {
    for _, stmt := range bs.Statements {
        switch s := stmt.(type) {
        case *ast.LetStatement:
            c.compileLetStatement(s, c.builder.GetInsertBlock().Parent())  // Current function
        case *ast.PrintStatement:
            c.compilePrintStatement(s)
        }
    }
}

func (c *Compiler) createEntryBlockAlloca(f llvm.Value, name string) llvm.Value {
	currentInsert := c.builder.GetInsertBlock()
	c.builder.SetInsertPointBefore(f.EntryBasicBlock().FirstInstruction())

	alloca := c.builder.CreateAlloca(c.context.Int64Type(), name)
	c.builder.SetInsertPointAtEnd(currentInsert)
	return alloca
}

func (c *Compiler) compileCallExpression(ce *ast.CallExpression) llvm.Value {
	fn := c.module.NamedFunction(ce.Function.Value)
	if fn.IsNil() {
		panic("undefined function: " + ce.Function.Value)
	}

	args := make([]llvm.Value, len(ce.Arguments))
	for i, arg := range ce.Arguments {
		args[i] = c.compileExpression(arg)
	}

    funcType := fn.Type().ElementType() // Get function signature type
    return c.builder.CreateCall(funcType, fn, args, "call_tmp")
}

func (c *Compiler) compilePrintStatement(ps *ast.PrintStatement) {
	// Declare printf function if not already declared
	printfType := llvm.FunctionType(c.context.Int64Type(), []llvm.Type{llvm.PointerType(c.context.Int8Type(), 0)}, true)
	printf := c.module.NamedFunction("printf")
	if printf.IsNil() {
		printf = llvm.AddFunction(c.module, "printf", printfType)
	}

	// Prepare format string and arguments
	formatStr := llvm.ConstString(" %d\n", false)
	formatGlobal := llvm.AddGlobal(c.module, llvm.ArrayType(c.context.Int8Type(), len(formatStr.String())+1), "printf_format")
	formatGlobal.SetInitializer(formatStr)
	
	int64Type := c.context.IntType(64)
	arrayType := formatGlobal.Type().ElementType() // Get array type

	formatPtr := c.builder.CreateGEP(arrayType, formatGlobal, []llvm.Value{
		llvm.ConstInt(int64Type, 0, false),
		llvm.ConstInt(int64Type, 0, false),
	}, "format_ptr")

	// Compile each expression and create call
	for _, expr := range ps.Expression {
		val := c.compileExpression(expr)
		c.builder.CreateCall(printfType, printf, []llvm.Value{formatPtr, val}, "printf_call")
	}
}

// Helper function to generate final output
func (c *Compiler) GenerateIR() string {
	return c.module.String()
}