package compiler

import (
	"pluto/ast"
	"fmt"
	"strings"
	"tinygo.org/x/go-llvm"
)

type Compiler struct {
	context    llvm.Context
	module     llvm.Module
	builder    llvm.Builder
	symbols    map[string]llvm.Value
	funcParams map[string][]llvm.Value
	formatCounter int  // Track unique format strings
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
		formatCounter: 0,
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
		condVal, _ := c.compileExpression(expr)
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
		val, _ := c.compileExpression(stmt.Value[i])
		c.symbols[ident.Value] = val
	}
	c.builder.CreateBr(contBlock)

	// False block (no assignment)
	c.builder.SetInsertPoint(elseBlock, elseBlock.FirstInstruction())
	c.builder.CreateBr(contBlock)

	// Continue block
	c.builder.SetInsertPoint(contBlock, contBlock.FirstInstruction())
}

func (c *Compiler) compileExpression(expr ast.Expression) (llvm.Value, llvm.Type) {
    switch e := expr.(type) {
    case *ast.IntegerLiteral:
        val := llvm.ConstInt(c.context.Int64Type(), uint64(e.Value), false)
        return val, val.Type()
    case *ast.StringLiteral:
        // Create global string constant
        str := llvm.ConstString(e.Value, true)
        global := llvm.AddGlobal(c.module, str.Type(), "str_literal")
        global.SetInitializer(str)
        ptr := c.builder.CreateGEP(
            global.Type(),
            global,
            []llvm.Value{
                llvm.ConstInt(c.context.Int64Type(), 0, false),
                llvm.ConstInt(c.context.Int64Type(), 0, false),
            },
            "str_ptr",
        )
        return ptr, ptr.Type()
    case *ast.Identifier:
        val := c.compileIdentifier(e)
        return val, val.Type()
    case *ast.InfixExpression:
        val := c.compileInfixExpression(e)
        return val, val.Type()
    default:
        panic("unsupported expression type")
    }
}

func (c *Compiler) compileIdentifier(ident *ast.Identifier) llvm.Value {
	if val, ok := c.symbols[ident.Value]; ok {
		return val
	}
	panic(fmt.Sprintf("undefined variable: %s", ident.Value))
}

func (c *Compiler) compileInfixExpression(expr *ast.InfixExpression) llvm.Value {
	left, _ := c.compileExpression(expr.Left)
	right, _ := c.compileExpression(expr.Right)

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
		args[i], _ = c.compileExpression(arg)
	}

    funcType := fn.Type().ElementType() // Get function signature type
    return c.builder.CreateCall(funcType, fn, args, "call_tmp")
}

func (c *Compiler) compilePrintStatement(ps *ast.PrintStatement) {
    // Track format specifiers and arguments
    var formatStr string
    var args []llvm.Value

    // Generate format string based on expression types
    for _, expr := range ps.Expression {
        // Compile the expression and get its value and type
        val, typ := c.compileExpression(expr)

        // Append format specifier based on type
        switch typ.TypeKind() {
        case llvm.IntegerTypeKind:
            formatStr += "%d "
        case llvm.PointerTypeKind:
            if typ.ElementType().TypeKind() == llvm.IntegerTypeKind && typ.ElementType().IntTypeWidth() == 8 {
                formatStr += "%s "  // Assume it's a string
            } else {
                panic("unsupported pointer type in print statement")
            }
        default:
            panic("unsupported type in print statement")
        }

        args = append(args, val)
    }

    // Add newline and null terminator
	formatStr = strings.TrimSuffix(formatStr, " ") + "\n" // Remove trailing space
    formatConst := llvm.ConstString(formatStr, true)  // true = add \0

    // Create unique global variable for the format string
    globalName := fmt.Sprintf("printf_fmt_%d", c.formatCounter)
    c.formatCounter++

    // Define global array with exact length
    arrayLength := len(formatStr) + 1  // +1 for null terminator
    arrayType := llvm.ArrayType(c.context.Int8Type(), arrayLength)
    formatGlobal := llvm.AddGlobal(c.module, arrayType, globalName)
    formatGlobal.SetInitializer(formatConst)

    // Get pointer to the format string
    zero := llvm.ConstInt(c.context.Int64Type(), 0, false)
    formatPtr := c.builder.CreateGEP(
        arrayType,
        formatGlobal,
        []llvm.Value{zero, zero},
        "fmt_ptr",
    )

    // Declare printf (variadic function)
    printfType := llvm.FunctionType(
        c.context.Int32Type(),
        []llvm.Type{llvm.PointerType(c.context.Int8Type(), 0)},
        true,  // Variadic
    )
    printf := c.module.NamedFunction("printf")
    if printf.IsNil() {
        printf = llvm.AddFunction(c.module, "printf", printfType)
    }

    // Call printf with all arguments
    allArgs := append([]llvm.Value{formatPtr}, args...)
    c.builder.CreateCall(printfType, printf, allArgs, "printf_call")
}

// Helper function to generate final output
func (c *Compiler) GenerateIR() string {
	return c.module.String()
}