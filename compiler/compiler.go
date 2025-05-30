package compiler

import (
	"fmt"
	"github.com/thiremani/pluto/ast"
	"strings"
	"tinygo.org/x/go-llvm"
)

type Symbol struct {
	Val  llvm.Value
	Type Type
}

type Compiler struct {
	Symbols       map[string]Symbol
	Context       llvm.Context
	Module        llvm.Module
	builder       llvm.Builder
	formatCounter int           // Track unique format strings
	CodeCompiler  *CodeCompiler // Optional reference for script compilation
}

func NewCompiler(ctx llvm.Context, moduleName string, cc *CodeCompiler) *Compiler {
	module := ctx.NewModule(moduleName)
	builder := ctx.NewBuilder()

	return &Compiler{
		Symbols:       make(map[string]Symbol),
		Context:       ctx,
		Module:        module,
		builder:       builder,
		formatCounter: 0,
		CodeCompiler:  cc,
	}
}

func (c *Compiler) mapToLLVMType(t Type) llvm.Type {
	switch t.Kind() {
	case IntKind:
		intType := t.(Int)
		switch intType.Width {
		case 8:
			return c.Context.Int8Type()
		case 16:
			return c.Context.Int16Type()
		case 32:
			return c.Context.Int32Type()
		case 64:
			return c.Context.Int64Type()
		default:
			panic(fmt.Sprintf("unsupported int width: %d", intType.Width))
		}
	case FloatKind:
		floatType := t.(Float)
		if floatType.Width == 32 {
			return c.Context.FloatType()
		} else if floatType.Width == 64 {
			return c.Context.DoubleType()
		} else {
			panic(fmt.Sprintf("unsupported float width: %d", floatType.Width))
		}
	case StrKind:
		// Represent a string as a pointer to an 8-bit integer.
		return llvm.PointerType(c.Context.Int8Type(), 0)
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

// createGlobalString creates a global string constant in the LLVM module.
// The 'linkage' parameter allows you to specify the desired llvm.Linkage,
// such as llvm.ExternalLinkage for exported constants or llvm.PrivateLinkage for internal use.
func (c *Compiler) createGlobalString(name, value string, linkage llvm.Linkage) llvm.Value {
	strConst := llvm.ConstString(value, true)
	arrayLength := len(value) + 1
	arrType := llvm.ArrayType(c.Context.Int8Type(), arrayLength)

	return c.makeGlobalConst(arrType, name, strConst, linkage)
}

func (c *Compiler) makeGlobalConst(llvmType llvm.Type, name string, val llvm.Value, linkage llvm.Linkage) llvm.Value {
	// Create a global LLVM variable
	global := llvm.AddGlobal(c.Module, llvmType, name)
	global.SetInitializer(val)
	global.SetLinkage(linkage)
	global.SetUnnamedAddr(true)
	global.SetGlobalConstant(true)
	return global
}

func (c *Compiler) compileConstStatement(stmt *ast.ConstStatement) {
	for i := 0; i < len(stmt.Name); i++ {
		name := stmt.Name[i].Value
		valueExpr := stmt.Value[i]
		linkage := llvm.ExternalLinkage
		sym := Symbol{}
		var val llvm.Value

		switch v := valueExpr.(type) {
		case *ast.IntegerLiteral:
			val = llvm.ConstInt(c.Context.Int64Type(), uint64(v.Value), false)
			sym.Type = Pointer{Elem: Int{Width: 64}}
			sym.Val = c.makeGlobalConst(c.Context.Int64Type(), name, val, linkage)

		case *ast.FloatLiteral:
			val = llvm.ConstFloat(c.Context.DoubleType(), v.Value)
			sym.Type = Pointer{Elem: Float{Width: 64}}
			sym.Val = c.makeGlobalConst(c.Context.DoubleType(), name, val, linkage)

		case *ast.StringLiteral:
			sym.Val = c.createGlobalString(name, v.Value, linkage)
			sym.Type = Str{}

		default:
			panic(fmt.Sprintf("unsupported constant type: %T", v))
		}
		c.Symbols[name] = sym
	}
}

func (c *Compiler) compileLetStatement(stmt *ast.LetStatement, fn llvm.Value) {
	// Handle unconditional assignments (no conditions)
	if len(stmt.Condition) == 0 {
		// Directly assign values without branching
		for i, ident := range stmt.Name {
			c.Symbols[ident.Value] = c.compileExpression(stmt.Value[i])
		}
		return // Exit early; no PHI nodes or blocks needed
	}

	// Capture previous values BEFORE processing the LetStatement
	prevValues := make(map[string]Symbol)
	for _, ident := range stmt.Name {
		if val, exists := c.Symbols[ident.Value]; exists {
			prevValues[ident.Value] = val // Existing value
		} else {
			prevValues[ident.Value] = Symbol{
				Val:  llvm.ConstInt(c.Context.Int64Type(), 0, false), // Default to 0
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
	ifBlock := c.Context.AddBasicBlock(fn, "if_block")
	elseBlock := c.Context.AddBasicBlock(fn, "else_block")
	contBlock := c.Context.AddBasicBlock(fn, "cont_block")

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
		sym, ok := c.Symbols[ident.Value]
		if !ok {
			// Provide a default symbol if the variable hasn't been defined.
			sym = Symbol{
				Val:  llvm.ConstInt(c.Context.Int64Type(), 0, false),
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
		c.Symbols[ident.Value] = Symbol{
			Val:  phi, // Update symbol table
			Type: sym.Type,
		}
	}
}

func (c *Compiler) inferTypes(s *ast.LetStatement) []Type {
	outTypes := make([]Type, 0, len(s.Name))
	// for i, expr := range s.Value {

	// }

	return outTypes
}

func (c *Compiler) compileExpression(expr ast.Expression) (s Symbol) {
	switch e := expr.(type) {
	case *ast.IntegerLiteral:
		s.Val = llvm.ConstInt(c.Context.Int64Type(), uint64(e.Value), false)
		s.Type = Int{Width: 64}
		return
	case *ast.FloatLiteral:
		s.Val = llvm.ConstFloat(c.Context.DoubleType(), e.Value)
		s.Type = Float{Width: 64}
		return
	case *ast.StringLiteral:
		// create a global name for the literal
		globalName := fmt.Sprintf("str_literal_%d", c.formatCounter)
		c.formatCounter++
		s.Val = c.createGlobalString(globalName, e.Value, llvm.PrivateLinkage)
		s.Type = Str{}
		return
	case *ast.Identifier:
		s = c.compileIdentifier(e)
		return
	case *ast.InfixExpression:
		s = c.compileInfixExpression(e)
		return
	case *ast.PrefixExpression:
		s = c.compilePrefixExpression(e)
		return
	case *ast.CallExpression:
		s = c.compileCallExpression(e)
		return
	default:
		panic(fmt.Sprintln("unsupported expression type", e))
	}
}

func setInstAlignment(inst llvm.Value, t Type) {
	switch typ := t.(type) {
	case Int:
		// divide by 8 as we want num bytes
		inst.SetAlignment(int(typ.Width >> 3))
	case Float:
		// divide by 8 as we want num bytes
		inst.SetAlignment(int(typ.Width >> 3))
	case Str:
		// TODO not sure what this should be
	default:
		panic("Unsupported pointer element type")
	}
}

// PromoteToMemory converts a variable that’s currently in a register into one stored in memory.
// This is needed for supporting the %n specifier (which requires a pointer to the variable).
func (c *Compiler) promoteToMemory(id string) {
	var sym Symbol
	var ok bool
	if sym, ok = c.Symbols[id]; !ok {
		panic("Undefined variable: " + id)
	}

	// Get the current basic block and its function.
	currentBlock := c.builder.GetInsertBlock()
	fn := currentBlock.Parent()

	// Get the function’s entry block.
	entryBlock := fn.EntryBasicBlock()

	// Save the current insertion point by remembering the current block.
	// Now, set the insertion point to the beginning of the entry block.
	// We choose the first non-PHI instruction so that we don’t disturb PHI nodes.
	firstInst := entryBlock.FirstInstruction()
	if !firstInst.IsNil() {
		c.builder.SetInsertPointBefore(firstInst)
	} else {
		c.builder.SetInsertPointAtEnd(entryBlock)
	}

	// Create the alloca in the entry block. This will allocate space for the variable.
	alloca := c.builder.CreateAlloca(c.mapToLLVMType(sym.Type), id)

	// Restore the insertion point to the original basic block.
	c.builder.SetInsertPointAtEnd(currentBlock)

	// Store the current register value into the newly allocated memory.
	storeInst := c.builder.CreateStore(sym.Val, alloca)
	// Set the alignment on the store to match the alloca.
	setInstAlignment(storeInst, sym.Type)

	// Update your symbol table here if needed so that the variable now refers to "alloca".
	c.Symbols[id] = Symbol{
		Val:  alloca,
		Type: Pointer{Elem: sym.Type},
	}
}

func (c *Compiler) derefIfPointer(s Symbol) Symbol {
	if s.Type.Kind() == PointerKind {
		ptr := s.Type.(Pointer)
		// Load the pointer value
		loadInst := c.builder.CreateLoad(c.mapToLLVMType(ptr.Elem), s.Val, "")
		setInstAlignment(loadInst, ptr.Elem)
		s.Val = loadInst
		s.Type = ptr.Elem
		return s
	}
	return s
}

func (c *Compiler) compileIdentifier(ident *ast.Identifier) Symbol {
	if s, ok := c.Symbols[ident.Value]; ok {
		return c.derefIfPointer(s)
	}

	if c.CodeCompiler == nil || c.CodeCompiler.Compiler == nil {
		panic(fmt.Sprintf("undefined variable: %s", ident.Value))
	}
	codeSymbols := c.CodeCompiler.Compiler.Symbols
	if s, ok := codeSymbols[ident.Value]; ok {
		return c.derefIfPointer(s)
	}

	panic(fmt.Sprintf("undefined variable: %s", ident.Value))
}

func (c *Compiler) compileInfixExpression(expr *ast.InfixExpression) (s Symbol) {
	left := c.compileExpression(expr.Left)
	right := c.compileExpression(expr.Right)

	// Build a key based on the operator literal and the operand types.
	// We assume that the String() method for your custom Type returns "I64" for Int{Width: 64} and "F64" for Float{Width: 64}.
	key := opKey{
		Operator:  expr.Operator,
		LeftType:  left.Type.String(),
		RightType: right.Type.String(),
	}

	if fn, ok := defaultOps[key]; ok {
		s = fn(c, left, right, true)
		return
	}
	panic("unsupported operator: " + expr.Operator + " for types: " + left.Type.String() + ", " + right.Type.String())
}

// compilePrefixExpression handles unary operators (prefix expressions).
func (c *Compiler) compilePrefixExpression(expr *ast.PrefixExpression) Symbol {
	// First compile the operand
	operand := c.compileExpression(expr.Right)

	// Lookup in the defaultPrefixOps table
	key := unaryOpKey{
		Operator:    expr.Operator,
		OperandType: operand.Type.String(),
	}
	if fn, ok := defaultUnaryOps[key]; ok {
		return fn(c, operand, true)
	}
	panic("unsupported unary operator: " + expr.Operator + " for type: " + operand.Type.String())
}

func (c *Compiler) createReturnType(outTypes []Type) llvm.Type {
	fields := make([]llvm.Type, len(outTypes))
	for i, t := range outTypes {
		fields[i] = c.mapToLLVMType(t)
	}
	st := c.Context.StructCreateNamed("returnStruct")
	st.StructSetBody(fields, false) // false = not packed
	return st
}

func (c *Compiler) compileFuncStatement(fn *ast.FuncStatement, args []Symbol) (llvm.Value, []Type) {
	llvmInputs := make([]llvm.Type, len(args))
	for i, a := range args {
		llvmInputs[i] = c.mapToLLVMType(a.Type)
	}

	// Compile function body
	c.compileBlockStatement(fn.Body)
	// body should have compiled outputs into symbol table
	outTypes := c.inferOutTypes(fn)

	retStruct := c.createReturnType(outTypes)
	llvmParams := []llvm.Type{
		llvm.PointerType(retStruct, 0),
	}
	llvmParams = append(llvmParams, llvmInputs...)

	codeModule := c.CodeCompiler.Compiler.Module
	funcType := llvm.FunctionType(c.Context.VoidType(), llvmParams, false)
	function := llvm.AddFunction(codeModule, mangle(fn.Token.Literal, args), funcType)

	// Add sret and noalias attributes to first parameter
	function.AddAttributeAtIndex(0, c.Context.CreateEnumAttribute(llvm.AttributeKindID("sret"), 0))
	function.AddAttributeAtIndex(0, c.Context.CreateEnumAttribute(llvm.AttributeKindID("noalias"), 0))

	// Create entry block
	entry := c.Context.AddBasicBlock(function, "entry")
	c.builder.SetInsertPoint(entry, entry.FirstInstruction())

	// Store parameters
	for i, param := range fn.Parameters {
		alloca := c.createEntryBlockAlloca(function, param.Value)
		c.builder.CreateStore(function.Param(i+1), alloca)
		c.Symbols[param.Value] = Symbol{
			Val:  alloca,
			Type: args[i].Type,
		}
	}

	c.builder.CreateRetVoid()

	return function, outTypes
}

func (c *Compiler) inferOutTypes(fn *ast.FuncStatement) []Type {
	outTypes := make([]Type, 0, len(fn.Outputs))
	for _, stmt := range fn.Body.Statements {
		switch s := stmt.(type) {
		case *ast.LetStatement:
			c.inferTypes(s)
		default:
			continue
		}
	}
	return outTypes
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
	entryBlock := f.EntryBasicBlock()
	firstInst := entryBlock.FirstInstruction()

	// Handle empty entry block
	if firstInst.IsNil() {
		c.builder.SetInsertPointAtEnd(entryBlock)
	} else {
		c.builder.SetInsertPointBefore(firstInst)
	}

	alloca := c.builder.CreateAlloca(c.Context.Int64Type(), name)
	c.builder.SetInsertPointAtEnd(currentInsert)
	return alloca
}

func (c *Compiler) compileCallExpression(ce *ast.CallExpression) Symbol {
	args := make([]Symbol, len(ce.Arguments))
	argsV := make([]llvm.Value, len(ce.Arguments))
	for i, arg := range ce.Arguments {
		args[i] = c.compileExpression(arg)
		argsV[i] = args[i].Val
	}

	codeModule := c.CodeCompiler.Compiler.Module
	fn := codeModule.NamedFunction(mangle(ce.Function.Value, args))
	var outTypes []Type
	if fn.IsNil() {
		// attempt to compile existing function template from Code
		fk := ast.FuncKey{
			FuncName: ce.Function.Value,
			Arity:    len(args),
		}

		code := c.CodeCompiler.Code
		template, ok := code.Func.Map[fk]
		if !ok {
			panic(fmt.Sprintf("undefined function: %s", ce.Function.Value))
		}
		fn, outTypes = c.compileFuncStatement(template, args)
	}

	sretPtr := c.createEntryBlockAlloca(
		c.builder.GetInsertBlock().Parent(),
		"sret_tmp",
	)
	llvmArgs := []llvm.Value{sretPtr}
	llvmArgs = append(llvmArgs, argsV...)

	c.builder.CreateCall(fn.Type().ElementType(), fn, llvmArgs, "call_tmp")

	results := make([]Symbol, len(outTypes))
	for i, typ := range outTypes {
		gep := c.builder.CreateStructGEP(sretPtr.Type(), sretPtr, i, "ret_gep")
		load := c.builder.CreateLoad(c.mapToLLVMType(typ), gep, "ret_load")
		results[i] = Symbol{Val: load, Type: typ}
	}
	return results[0]
}

// defaultSpecifier returns the printf conversion specifier for a given type.
func defaultSpecifier(t Type) string {
	switch t.Kind() {
	case IntKind:
		return "%ld"
	case FloatKind:
		return "%.15g"
	case StrKind:
		return "%s"
	default:
		err := "unsupported type in print statement " + t.String()
		panic(err)
	}
}

func (c *Compiler) compilePrintStatement(ps *ast.PrintStatement) {
	// Track format specifiers and arguments
	var formatStr string
	var args []llvm.Value

	// Generate format string based on expression types
	for _, expr := range ps.Expression {
		// If the expression is a string literal, check for embedded markers.
		if strLit, ok := expr.(*ast.StringLiteral); ok {
			processed, newArgs := c.formatIdentifiers(strLit.Value)
			formatStr += processed + " " // separate expressions with a space
			args = append(args, newArgs...)
		} else {
			// Compile the expression and get its value and type
			s := c.compileExpression(expr)
			formatStr += defaultSpecifier(s.Type) + " "
			args = append(args, s.Val)
		}
	}

	// Add newline and null terminator
	formatStr = strings.TrimSuffix(formatStr, " ") + "\n" // Remove trailing space
	formatConst := llvm.ConstString(formatStr, true)      // true = add \0

	// Create unique global variable for the format string
	globalName := fmt.Sprintf("printf_fmt_%d", c.formatCounter)
	c.formatCounter++

	// Define global array with exact length
	arrayLength := len(formatStr) + 1 // +1 for null terminator
	arrayType := llvm.ArrayType(c.Context.Int8Type(), arrayLength)
	formatGlobal := llvm.AddGlobal(c.Module, arrayType, globalName)
	formatGlobal.SetInitializer(formatConst)
	// It is essential to mark this as constant. Else a printf like %n which writes to pointer will fail
	formatGlobal.SetGlobalConstant(true)

	// Get pointer to the format string
	zero := llvm.ConstInt(c.Context.Int64Type(), 0, false)
	formatPtr := c.builder.CreateGEP(arrayType, formatGlobal, []llvm.Value{zero, zero}, "fmt_ptr")

	// Declare printf (variadic function)
	printfType := llvm.FunctionType(
		c.Context.Int32Type(),
		[]llvm.Type{llvm.PointerType(c.Context.Int8Type(), 0)},
		true, // Variadic
	)
	printf := c.Module.NamedFunction("printf")
	if printf.IsNil() {
		printf = llvm.AddFunction(c.Module, "printf", printfType)
	}

	// Call printf with all arguments
	allArgs := append([]llvm.Value{formatPtr}, args...)
	c.builder.CreateCall(printfType, printf, allArgs, "printf_call")
}

// Helper function to generate final output
func (c *Compiler) GenerateIR() string {
	return c.Module.String()
}
