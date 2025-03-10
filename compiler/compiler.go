package compiler

import (
	"fmt"
	"pluto/ast"
	"strings"
	"tinygo.org/x/go-llvm"
)

type Symbol struct {
	Val  llvm.Value
	Type Type
}

type Compiler struct {
	context       llvm.Context
	module        llvm.Module
	builder       llvm.Builder
	symbols       map[string]Symbol
	funcParams    map[string][]llvm.Value
	formatCounter int // Track unique format strings
}

func NewCompiler(moduleName string) *Compiler {
	context := llvm.NewContext()
	module := context.NewModule(moduleName)
	builder := context.NewBuilder()

	return &Compiler{
		context:       context,
		module:        module,
		builder:       builder,
		symbols:       make(map[string]Symbol),
		funcParams:    make(map[string][]llvm.Value),
		formatCounter: 0,
	}
}

func (c *Compiler) Compile(program *ast.Program) {
	// Create main function
	mainType := llvm.FunctionType(c.context.Int32Type(), []llvm.Type{}, false)
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

	// Add explicit return 0
	c.builder.CreateRet(llvm.ConstInt(c.context.Int32Type(), 0, false))
}

func (c *Compiler) mapToLLVMType(t Type) llvm.Type {
	switch t.Kind() {
	case IntKind:
		intType := t.(Int)
		switch intType.Width {
		case 8:
			return c.context.Int8Type()
		case 16:
			return c.context.Int16Type()
		case 32:
			return c.context.Int32Type()
		case 64:
			return c.context.Int64Type()
		default:
			panic(fmt.Sprintf("unsupported int width: %d", intType.Width))
		}
	case FloatKind:
		floatType := t.(Float)
		if floatType.Width == 32 {
			return c.context.FloatType()
		} else if floatType.Width == 64 {
			return c.context.DoubleType()
		} else {
			panic(fmt.Sprintf("unsupported float width: %d", floatType.Width))
		}
	case StringKind:
		// Represent a string as a pointer to an 8-bit integer.
		return llvm.PointerType(c.context.Int8Type(), 0)
	case PointerKind:
		ptrType := t.(Pointer)
		elemLLVM := c.mapToLLVMType(ptrType.Elem)
		return llvm.PointerType(elemLLVM, 0)
	case ArrayKind:
		arrType := t.(Array)
		elemLLVM := c.mapToLLVMType(arrType.Elem)
		return llvm.ArrayType(elemLLVM, arrType.Length)
	default:
		panic("unknown type in mapToLLVMType: " + t.String())
	}
}

func (c *Compiler) compileLetStatement(stmt *ast.LetStatement, fn llvm.Value) {
	// Handle unconditional assignments (no conditions)
	if len(stmt.Condition) == 0 {
		// Directly assign values without branching
		for i, ident := range stmt.Name {
			c.symbols[ident.Value] = c.compileExpression(stmt.Value[i])
		}
		return // Exit early; no PHI nodes or blocks needed
	}

	// Capture previous values BEFORE processing the LetStatement
	prevValues := make(map[string]Symbol)
	for _, ident := range stmt.Name {
		if val, exists := c.symbols[ident.Value]; exists {
			prevValues[ident.Value] = val // Existing value
		} else {
			prevValues[ident.Value] = Symbol{
				Val:  llvm.ConstInt(c.context.Int64Type(), 0, false), // Default to 0
				Type: Int{Width: 64},
			}
		}
	}

	// Compile condition
	var cond llvm.Value
	for i, expr := range stmt.Condition {
		condVal := c.compileExpression(expr).Val
		if i == 0 {
			cond = condVal
		} else {
			cond = c.builder.CreateAnd(cond, condVal, "and_cond")
		}
	}

	// Create blocks for conditional flow
	ifBlock := c.context.AddBasicBlock(fn, "if_block")
	elseBlock := c.context.AddBasicBlock(fn, "else_block")
	contBlock := c.context.AddBasicBlock(fn, "cont_block")

	c.builder.CreateCondBr(cond, ifBlock, elseBlock)

	// True block: Assign new values if condition is true
	c.builder.SetInsertPoint(ifBlock, ifBlock.FirstInstruction())
	trueValues := make(map[string]Symbol)
	for i, ident := range stmt.Name {
		trueValues[ident.Value] = c.compileExpression(stmt.Value[i])
	}
	c.builder.CreateBr(contBlock)

	// False block: Do NOT assign anything (preserve previous values)
	c.builder.SetInsertPoint(elseBlock, elseBlock.FirstInstruction())
	c.builder.CreateBr(contBlock)

	// Continuation block: Merge new (true) or previous (false) values
	c.builder.SetInsertPoint(contBlock, contBlock.FirstInstruction())
	for _, ident := range stmt.Name {
		// Look up the existing symbol to get its custom type.
		sym, ok := c.symbols[ident.Value]
		if !ok {
			// Provide a default symbol if the variable hasn't been defined.
			sym = Symbol{
				Val:  llvm.ConstInt(c.context.Int64Type(), 0, false),
				Type: Int{Width: 64},
			}
		}
		llvmType := c.mapToLLVMType(sym.Type)
		phi := c.builder.CreatePHI(llvmType, ident.Value+"_phi")
		phi.AddIncoming(
			[]llvm.Value{
				trueValues[ident.Value].Val, // Value from true block
				prevValues[ident.Value].Val, // Value from before the LetStatement
			},
			[]llvm.BasicBlock{ifBlock, elseBlock},
		)
		c.symbols[ident.Value] = Symbol{
			Val:  phi, // Update symbol table
			Type: sym.Type,
		}
	}
}

func (c *Compiler) compileExpression(expr ast.Expression) (s Symbol) {
	switch e := expr.(type) {
	case *ast.IntegerLiteral:
		s.Val = llvm.ConstInt(c.context.Int64Type(), uint64(e.Value), false)
		s.Type = Int{Width: 64}
		return
	case *ast.FloatLiteral:
		s.Val = llvm.ConstFloat(c.context.DoubleType(), e.Value)
		s.Type = Float{Width: 64}
		return
	case *ast.StringLiteral:
		// Create global string constant
		str := llvm.ConstString(e.Value, true)
		// Compute the array length (including the null terminator).
		arrayLength := len(e.Value) + 1
		// Create an array type for [arrayLength x i8]
		arrType := llvm.ArrayType(c.module.Context().Int8Type(), arrayLength)

		globalName := fmt.Sprintf("str_literal_%d", c.formatCounter)
		c.formatCounter++

		global := llvm.AddGlobal(c.module, arrType, globalName)
		global.SetInitializer(str)
		global.SetLinkage(llvm.PrivateLinkage) // Make it private.
		global.SetUnnamedAddr(true)            // Mark as unnamed address.
		global.SetGlobalConstant(true)         // Mark it as constant.

		// Create the GEP with two 64-bit zero indices.
		zero := llvm.ConstInt(c.context.Int64Type(), 0, false)
		s.Val = llvm.ConstGEP(arrType, global, []llvm.Value{zero, zero})
		s.Type = String{Length: arrayLength}
		return
	case *ast.Identifier:
		s = c.compileIdentifier(e)
		return
	case *ast.InfixExpression:
		s = c.compileInfixExpression(e)
		return
	default:
		panic("unsupported expression type")
	}
}

func (c *Compiler) compileIdentifier(ident *ast.Identifier) Symbol {
	if val, ok := c.symbols[ident.Value]; ok {
		return val
	}
	panic(fmt.Sprintf("undefined variable: %s", ident.Value))
}

func (c *Compiler) compileInfixExpression(expr *ast.InfixExpression) (s Symbol) {
	left := c.compileExpression(expr.Left)
	right := c.compileExpression(expr.Right)

	// Determine types and promote to float if needed
	leftIsFloat := left.Type.Kind() == FloatKind
	rightIsFloat := right.Type.Kind() == FloatKind

	opType := IntKind

	if leftIsFloat || rightIsFloat {
		opType = FloatKind
		if !leftIsFloat {
			left.Val = c.builder.CreateSIToFP(left.Val, c.context.DoubleType(), "cast_to_float")
		}
		if !rightIsFloat {
			right.Val = c.builder.CreateSIToFP(right.Val, c.context.DoubleType(), "cast_to_float")
		}
	}

	switch expr.Operator {
	case "+":
		if opType == FloatKind {
			s.Val = c.builder.CreateFAdd(left.Val, right.Val, "fadd_tmp")
			s.Type = Float{Width: 64}
			return
		}
		s.Val = c.builder.CreateAdd(left.Val, right.Val, "add_tmp")
		s.Type = Int{Width: 64}
		return
	case "-":
		if opType == FloatKind {
			s.Val = c.builder.CreateFSub(left.Val, right.Val, "fsub_tmp")
			s.Type = Float{Width: 64}
			return
		}
		s.Val = c.builder.CreateSub(left.Val, right.Val, "sub_tmp")
		s.Type = Int{Width: 64}
		return
	case "*":
		if opType == FloatKind {
			s.Val = c.builder.CreateFMul(left.Val, right.Val, "fmul_tmp")
			s.Type = Float{Width: 64}
			return
		}
		s.Val = c.builder.CreateMul(left.Val, right.Val, "mul_tmp")
		s.Type = Int{Width: 64}
		return
	case "/":
		if left.Type.Kind() != FloatKind {
			left.Val = c.builder.CreateSIToFP(left.Val, c.context.DoubleType(), "cast_to_float")
		}
		if right.Type.Kind() != FloatKind {
			right.Val = c.builder.CreateSIToFP(right.Val, c.context.DoubleType(), "cast_to_float")
		}
		s.Val = c.builder.CreateFDiv(left.Val, right.Val, "fdiv_tmp")
		s.Type = Float{Width: 64}
		return
	case ">":
		s.Val = c.builder.CreateICmp(llvm.IntSGT, left.Val, right.Val, "cmp_tmp")
		s.Type = Int{Width: 64}
		return
	case "<":
		s.Val = c.builder.CreateICmp(llvm.IntSLT, left.Val, right.Val, "cmp_tmp")
		s.Type = Int{Width: 64}
		return
	case "==":
		s.Val = c.builder.CreateICmp(llvm.IntEQ, left.Val, right.Val, "eq_tmp")
		s.Type = Int{Width: 64}
		return
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
		c.symbols[param.Value] = Symbol{
			Val:  alloca,
			Type: Int{Width: 64},
		}
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
			c.compileLetStatement(s, c.builder.GetInsertBlock().Parent()) // Current function
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
		args[i] = c.compileExpression(arg).Val
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
		s := c.compileExpression(expr)

		// Append format specifier based on type
		switch s.Type.Kind() {
		case IntKind:
			// %ld for 64-bit integers
			formatStr += "%ld "
		case FloatKind:
			formatStr += "%.15g "
		case StringKind:
			formatStr += "%s "
		default:
			err := "unsupported type in print statement " + s.Type.String()
			panic(err)
		}

		args = append(args, s.Val)
	}

	// Add newline and null terminator
	formatStr = strings.TrimSuffix(formatStr, " ") + "\n" // Remove trailing space
	formatConst := llvm.ConstString(formatStr, true)      // true = add \0

	// Create unique global variable for the format string
	globalName := fmt.Sprintf("printf_fmt_%d", c.formatCounter)
	c.formatCounter++

	// Define global array with exact length
	arrayLength := len(formatStr) + 1 // +1 for null terminator
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
		true, // Variadic
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
