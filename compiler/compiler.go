package compiler

import (
	"fmt"
	"pluto/ast"
	"pluto/token"
	"strings"
	"tinygo.org/x/go-llvm"
)

// opKey represents the key for an operator function based on
// the operator literal and the operand type names.
type opKey struct {
	Operator  string
	LeftType  string
	RightType string
}

// opFunc is the type for functions that generate LLVM IR for an operator.
type opFunc func(left, right llvm.Value) llvm.Value

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
	opFuncs       map[opKey]opFunc // opFuncs maps an operator key to a function that generates the corresponding LLVM IR.
	formatCounter int              // Track unique format strings
}

func NewCompiler(moduleName string) *Compiler {
	context := llvm.NewContext()
	module := context.NewModule(moduleName)
	builder := context.NewBuilder()

	c := Compiler{
		context:       context,
		module:        module,
		builder:       builder,
		symbols:       make(map[string]Symbol),
		funcParams:    make(map[string][]llvm.Value),
		formatCounter: 0,
	}
	c.initOpFuncs()

	return &c
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

func (c *Compiler) initOpFuncs() {
	// Initialize the operator functions map with concise type names ("i64" and "f64").
	c.opFuncs = make(map[opKey]opFunc)

	// Integer addition
	c.opFuncs[opKey{Operator: token.SYM_ADD, LeftType: "i64", RightType: "i64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateAdd(left, right, "add_tmp")
	}
	// Float addition
	c.opFuncs[opKey{Operator: token.SYM_ADD, LeftType: "f64", RightType: "f64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateFAdd(left, right, "fadd_tmp")
	}
	// Integer subtraction
	c.opFuncs[opKey{Operator: token.SYM_SUB, LeftType: "i64", RightType: "i64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateSub(left, right, "sub_tmp")
	}
	// Float subtraction
	c.opFuncs[opKey{Operator: token.SYM_SUB, LeftType: "f64", RightType: "f64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateFSub(left, right, "fsub_tmp")
	}
	// Integer multiplication
	c.opFuncs[opKey{Operator: token.SYM_MUL, LeftType: "i64", RightType: "i64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateMul(left, right, "mul_tmp")
	}
	// Float multiplication
	c.opFuncs[opKey{Operator: token.SYM_MUL, LeftType: "f64", RightType: "f64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateFMul(left, right, "fmul_tmp")
	}
	// For division, if both operands are integers, promote them to float and do float division.
	c.opFuncs[opKey{Operator: token.SYM_DIV, LeftType: "i64", RightType: "i64"}] = func(left, right llvm.Value) llvm.Value {
		leftFP := c.builder.CreateSIToFP(left, c.context.DoubleType(), "cast_to_f64")
		rightFP := c.builder.CreateSIToFP(right, c.context.DoubleType(), "cast_to_f64")
		return c.builder.CreateFDiv(leftFP, rightFP, "fdiv_tmp")
	}
	// Float division
	c.opFuncs[opKey{Operator: token.SYM_DIV, LeftType: "f64", RightType: "f64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateFDiv(left, right, "fdiv_tmp")
	}

	//Comparisons
	// Integer comparisons
	c.opFuncs[opKey{Operator: token.SYM_EQL, LeftType: "i64", RightType: "i64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateICmp(llvm.IntEQ, left, right, "icmp_eq")
	}
	c.opFuncs[opKey{Operator: token.SYM_LSS, LeftType: "i64", RightType: "i64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateICmp(llvm.IntSLT, left, right, "icmp_lt")
	}
	c.opFuncs[opKey{Operator: token.SYM_GTR, LeftType: "i64", RightType: "i64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateICmp(llvm.IntSGT, left, right, "icmp_gt")
	}
	c.opFuncs[opKey{Operator: token.SYM_NEQ, LeftType: "i64", RightType: "i64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateICmp(llvm.IntNE, left, right, "icmp_ne")
	}
	c.opFuncs[opKey{Operator: token.SYM_LEQ, LeftType: "i64", RightType: "i64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateICmp(llvm.IntSLE, left, right, "icmp_le")
	}
	c.opFuncs[opKey{Operator: token.SYM_GEQ, LeftType: "i64", RightType: "i64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateICmp(llvm.IntSGE, left, right, "icmp_ge")
	}

	// Float comparisons
	c.opFuncs[opKey{Operator: token.SYM_EQL, LeftType: "f64", RightType: "f64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateFCmp(llvm.FloatOEQ, left, right, "fcmp_eq")
	}
	c.opFuncs[opKey{Operator: token.SYM_LSS, LeftType: "f64", RightType: "f64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateFCmp(llvm.FloatOLT, left, right, "fcmp_lt")
	}
	c.opFuncs[opKey{Operator: token.SYM_GTR, LeftType: "f64", RightType: "f64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateFCmp(llvm.FloatOGT, left, right, "fcmp_gt")
	}
	c.opFuncs[opKey{Operator: token.SYM_NEQ, LeftType: "f64", RightType: "f64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateFCmp(llvm.FloatONE, left, right, "fcmp_ne")
	}
	c.opFuncs[opKey{Operator: token.SYM_LEQ, LeftType: "f64", RightType: "f64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateFCmp(llvm.FloatOLE, left, right, "fcmp_le")
	}
	c.opFuncs[opKey{Operator: token.SYM_GEQ, LeftType: "f64", RightType: "f64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateFCmp(llvm.FloatOGE, left, right, "fcmp_ge")
	}

	// Register exponentiation operator for floating-point values.
	powType := llvm.FunctionType(c.context.DoubleType(), []llvm.Type{c.context.DoubleType(), c.context.DoubleType()}, false)
	powFunc := c.module.NamedFunction("llvm.pow.f64")
	if powFunc.IsNil() {
		powFunc = llvm.AddFunction(c.module, "llvm.pow.f64", powType)
	}
	c.opFuncs[opKey{Operator: "^", LeftType: "f64", RightType: "f64"}] = func(left, right llvm.Value) llvm.Value {
		return c.builder.CreateCall(powType, powFunc, []llvm.Value{left, right}, "pow_tmp")
	}
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
			left.Type = Float{Width: 64}
		}
		if !rightIsFloat {
			right.Val = c.builder.CreateSIToFP(right.Val, c.context.DoubleType(), "cast_to_float")
			right.Type = Float{Width: 64}
		}
	}

	// Build a key based on the operator literal and the operand types.
	// We assume that the String() method for your custom Type returns "i64" for Int{Width: 64} and "f64" for Float{Width: 64}.
	key := opKey{
		Operator:  expr.Operator,
		LeftType:  left.Type.String(),
		RightType: right.Type.String(),
	}

	if fn, ok := c.opFuncs[key]; ok {
		result := fn(left.Val, right.Val)
		var resultType Type
		if expr.Operator == token.SYM_DIV {
			resultType = Float{Width: 64}
		} else if opType == FloatKind {
			resultType = Float{Width: 64}
		} else {
			resultType = Int{Width: 64}
		}
		s = Symbol{
			Val:  result,
			Type: resultType,
		}
		return
	}
	panic("unsupported operator: " + expr.Operator + " for types: " + left.Type.String() + ", " + right.Type.String())
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
