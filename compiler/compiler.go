package compiler

import (
	"fmt"
	"strings"

	"github.com/thiremani/pluto/ast"
	"tinygo.org/x/go-llvm"
)

type Compiler struct {
	Scopes        []Scope
	Context       llvm.Context
	Module        llvm.Module
	builder       llvm.Builder
	formatCounter int           // Track unique format strings
	CodeCompiler  *CodeCompiler // Optional reference for script compilation
	FuncCache     map[string]*Func
}

func NewCompiler(ctx llvm.Context, moduleName string, cc *CodeCompiler) *Compiler {
	module := ctx.NewModule(moduleName)
	builder := ctx.NewBuilder()

	return &Compiler{
		Scopes:        []Scope{NewScope(FuncScope)},
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
		sym := &Symbol{}
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
		c.Put(name, sym)
	}
}

func (c *Compiler) doConditions(stmt *ast.LetStatement, compile bool) (cond llvm.Value, hasConditions bool) {
	// Compile condition
	if len(stmt.Condition) == 0 {
		hasConditions = false
		return
	}

	hasConditions = true
	for i, expr := range stmt.Condition {
		condSyms := c.doExpression(expr, compile)
		if !compile {
			continue
		}

		for idx, condSym := range condSyms {
			if i == 0 && idx == 0 {
				cond = condSym.Val
				continue
			}
			cond = c.builder.CreateAnd(cond, condSym.Val, "and_cond")
		}
	}
	return
}

func (c *Compiler) addMain() {
	mainType := llvm.FunctionType(c.Context.Int32Type(), []llvm.Type{}, false)
	mainFunc := llvm.AddFunction(c.Module, "main", mainType)
	mainBlock := c.Context.AddBasicBlock(mainFunc, "entry")
	c.builder.SetInsertPoint(mainBlock, mainBlock.FirstInstruction())
}

// adds final return for main exit
func (c *Compiler) addRet() {
	c.builder.CreateRet(llvm.ConstInt(c.Context.Int32Type(), 0, false))
}

func (c *Compiler) doStatement(stmt ast.Statement, compile bool) {
	switch s := stmt.(type) {
	case *ast.LetStatement:
		c.doLetStatement(s, compile)
	case *ast.PrintStatement:
		c.doPrintStatement(s, compile)
	default:
		panic(fmt.Sprintf("Cannot handle statement type %T", s))
	}
}

func (c *Compiler) doLetStatement(stmt *ast.LetStatement, compile bool) {
	cond, hasConditions := c.doConditions(stmt, compile)

	syms := []*Symbol{}
	for _, expr := range stmt.Value {
		syms = append(syms, c.doExpression(expr, compile)...)
	}

	if len(stmt.Name) != len(syms) {
		panic(fmt.Sprintf("Statement lhs identifiers are not equal to rhs values!!! lhs identifiers: %d. rhs values: %d", len(stmt.Name), len(syms)))
	}

	// Capture previous values
	prevValues := make(map[string]*Symbol)
	for _, ident := range stmt.Name {
		if val, exists := c.Get(ident.Value); exists {
			prevValues[ident.Value] = val // Existing value
		} else {
			// TODO set to default values for various types
			prevValues[ident.Value] = &Symbol{
				Val:  llvm.ConstInt(c.Context.Int64Type(), 0, false), // Default to 0
				Type: Int{Width: 64},
			}
		}
	}

	trueValues := make(map[string]*Symbol)
	for i, ident := range stmt.Name {
		trueValues[ident.Value] = syms[i]
	}

	// TODO check type of variable has not changed
	if !hasConditions {
		c.PutBulk(trueValues)
		return
	}

	// wait for true values to be put in c.Scopes above so they have types
	// TODO check type of variable has not changed
	if !compile {
		c.PutBulk(prevValues)
		return
	}

	fn := c.builder.GetInsertBlock().Parent()
	// Create blocks for conditional flow
	ifBlock := c.Context.AddBasicBlock(fn, "if_block")
	elseBlock := c.Context.AddBasicBlock(fn, "else_block")
	contBlock := c.Context.AddBasicBlock(fn, "cont_block")

	c.builder.CreateCondBr(cond, ifBlock, elseBlock)

	// True block: Assign new values if condition is true
	c.builder.SetInsertPoint(ifBlock, ifBlock.FirstInstruction())
	c.builder.CreateBr(contBlock)

	// False block: Do NOT assign anything (preserve previous values)
	c.builder.SetInsertPoint(elseBlock, elseBlock.FirstInstruction())
	c.builder.CreateBr(contBlock)

	// Continuation block: Merge new (true) or previous (false) values
	c.builder.SetInsertPoint(contBlock, contBlock.FirstInstruction())
	for _, ident := range stmt.Name {
		// Look up the existing symbol to get its custom type.
		sym := prevValues[ident.Value]
		llvmType := c.mapToLLVMType(sym.Type)
		phi := c.builder.CreatePHI(llvmType, ident.Value+"_phi")
		phi.AddIncoming(
			[]llvm.Value{
				trueValues[ident.Value].Val, // Value from true block
				sym.Val,                     // Value from before the LetStatement
			},
			[]llvm.BasicBlock{ifBlock, elseBlock},
		)
		sym.Val = phi
		c.Put(ident.Value, sym)
	}
}

// if compile bool is false, we only return symbol with type info. This is used for type inference
func (c *Compiler) doExpression(expr ast.Expression, compile bool) (res []*Symbol) {
	s := &Symbol{}
	switch e := expr.(type) {
	case *ast.IntegerLiteral:
		s.Type = Int{Width: 64}
		if !compile {
			res = []*Symbol{s}
			return
		}
		s.Val = llvm.ConstInt(c.Context.Int64Type(), uint64(e.Value), false)
		res = []*Symbol{s}
	case *ast.FloatLiteral:
		s.Type = Float{Width: 64}
		if !compile {
			res = []*Symbol{s}
			return
		}
		s.Val = llvm.ConstFloat(c.Context.DoubleType(), e.Value)
		res = []*Symbol{s}
	case *ast.StringLiteral:
		s.Type = Str{}
		if !compile {
			res = []*Symbol{s}
			return
		}
		// create a global name for the literal
		globalName := fmt.Sprintf("str_literal_%d", c.formatCounter)
		c.formatCounter++
		s.Val = c.createGlobalString(globalName, e.Value, llvm.PrivateLinkage)
		res = []*Symbol{s}
	case *ast.Identifier:
		res = []*Symbol{c.doIdentifier(e, compile)}
	case *ast.InfixExpression:
		res = c.doInfixExpression(e, compile)
	case *ast.PrefixExpression:
		res = c.doPrefixExpression(e, compile)
	case *ast.CallExpression:
		res = c.doCallExpression(e, compile)
	default:
		panic(fmt.Sprintf("unsupported expression type %T", e))
	}

	return
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
func (c *Compiler) promoteToMemory(id string) *Symbol {
	var sym *Symbol
	var ok bool
	if sym, ok = c.Get(id); !ok {
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
	sym.Val = alloca
	sym.Type = Pointer{Elem: sym.Type}
	return sym
}

// if compile is false, we only return symbol with type info. This is used for type inference
func (c *Compiler) derefIfPointer(s *Symbol, compile bool) *Symbol {
	if s.Type.Kind() == PointerKind {
		ptr := s.Type.(Pointer)
		newS := &Symbol{
			Type: ptr.Elem,
		}
		// Load the pointer value
		if !compile {
			return newS
		}
		loadInst := c.builder.CreateLoad(c.mapToLLVMType(ptr.Elem), s.Val, "")
		setInstAlignment(loadInst, ptr.Elem)
		newS.Val = loadInst
		return newS
	}
	return s
}

// if compile is false, we only return symbol with type info. This is used for type inference
func (c *Compiler) doIdentifier(ident *ast.Identifier, compile bool) *Symbol {
	if s, ok := c.Get(ident.Value); ok {
		return c.derefIfPointer(s, compile)
	}

	if c.CodeCompiler == nil || c.CodeCompiler.Compiler == nil {
		panic(fmt.Sprintf("undefined variable: %s", ident.Value))
	}
	cc := c.CodeCompiler.Compiler
	if s, ok := cc.Get(ident.Value); ok {
		return c.derefIfPointer(s, compile)
	}

	panic(fmt.Sprintf("undefined variable: %s. Not found in script or code files", ident.Value))
}

func (c *Compiler) doInfixExpression(expr *ast.InfixExpression, compile bool) (res []*Symbol) {
	res = []*Symbol{}
	left := c.doExpression(expr.Left, compile)
	right := c.doExpression(expr.Right, compile)
	if len(left) != len(right) {
		panic(fmt.Sprintf("Left expression and right expression have unequal lengths! Left: %d. Right: %d", len(left), len(right)))
	}

	// Build a key based on the operator literal and the operand types.
	// We assume that the String() method for your custom Type returns "I64" for Int{Width: 64} and "F64" for Float{Width: 64}.
	for i := 0; i < len(left); i++ {
		l := c.derefIfPointer(left[i], compile)

		r := c.derefIfPointer(right[i], compile)

		key := opKey{
			Operator:  expr.Operator,
			LeftType:  l.Type.String(),
			RightType: r.Type.String(),
		}

		var fn opFunc
		var ok bool
		if fn, ok = defaultOps[key]; !ok {
			panic("unsupported operator: " + expr.Operator + " for types: " + l.Type.String() + ", " + r.Type.String())
		}
		res = append(res, fn(c, l, r, compile))
	}
	return
}

// compilePrefixExpression handles unary operators (prefix expressions).
func (c *Compiler) doPrefixExpression(expr *ast.PrefixExpression, compile bool) (res []*Symbol) {
	// First compile the operand
	operand := c.doExpression(expr.Right, compile)

	for _, op := range operand {
		// Lookup in the defaultPrefixOps table
		key := unaryOpKey{
			Operator:    expr.Operator,
			OperandType: op.Type.String(),
		}

		var fn unaryOpFunc
		var ok bool
		if fn, ok = defaultUnaryOps[key]; !ok {
			panic("unsupported unary operator: " + expr.Operator + " for type: " + op.Type.String())

		}
		res = append(res, fn(c, op, compile))
	}
	return
}

func (c *Compiler) createReturnType(outTypes []Type) llvm.Type {
	fields := make([]llvm.Type, len(outTypes))
	for i, t := range outTypes {
		fields[i] = c.mapToLLVMType(t)
	}
	st := c.Context.StructCreateNamed("retStruct")
	st.StructSetBody(fields, false) // false = not packed
	return st
}

func (c *Compiler) inferFuncTypes(fn *ast.FuncStatement, args []*Symbol, mangled string) []Type {
	// if func is already in cache then skip as it is a recursive call
	if _, ok := c.FuncCache[mangled]; ok {
		return nil
	}

	// add func to cache
	FuncType := &Func{
		Name: fn.Token.Literal,
	}
	for _, arg := range args {
		FuncType.Params = append(FuncType.Params, arg.Type)
	}
	c.FuncCache[mangled] = FuncType

	outputs := c.doFuncBlock(fn, args, mangled, false)
	// body should have compiled outputs into symbol table
	FuncType.Outputs = c.inferOutTypes(fn, outputs)
	c.FuncCache[mangled] = FuncType

	return FuncType.Outputs
}

func (c *Compiler) doFuncStatement(fn *ast.FuncStatement, args []*Symbol, mangled string, compile bool) (llvm.Value, []Type) {
	if !compile {
		return llvm.Value{}, c.inferFuncTypes(fn, args, mangled)
	}

	llvmInputs := make([]llvm.Type, len(args))
	for i, a := range args {
		llvmInputs[i] = c.mapToLLVMType(a.Type)
	}

	FuncType, ok := c.FuncCache[mangled]
	if !ok {
		panic("FuncType not in function cache! Mangled name: " + mangled)
	}
	retStruct := c.createReturnType(FuncType.Outputs)
	llvmParams := []llvm.Type{
		llvm.PointerType(retStruct, 0),
	}
	llvmParams = append(llvmParams, llvmInputs...)

	codeModule := c.CodeCompiler.Compiler.Module
	funcType := llvm.FunctionType(c.Context.VoidType(), llvmParams, false)
	function := llvm.AddFunction(codeModule, mangled, funcType)

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
		c.Put(param.Value, &Symbol{
			Val:  alloca,
			Type: args[i].Type,
		})
	}

	c.builder.CreateRetVoid()

	return function, FuncType.Outputs
}

// inferOutTypes assumes the all block LetStatements have been walked
// and assumes types are in Types SymTable
func (c *Compiler) inferOutTypes(fn *ast.FuncStatement, outputs map[string]*Symbol) []Type {
	outTypes := make([]Type, 0, len(fn.Outputs))
	for i, o := range fn.Outputs {
		s := outputs[o.Value]
		if s.Type == nil {
			panic(fmt.Sprintf("Could not infer output type for function %s. Output Arg %d, Name %s", fn.Token.Literal, i, o.Value))
		}
		outTypes[i] = s.Type
	}
	return outTypes
}

func (c *Compiler) doFuncBlock(fn *ast.FuncStatement, args []*Symbol, mangled string, compile bool) map[string]*Symbol {
	outputs := make(map[string]*Symbol)
	if fn != nil {
		// see if there are any write args already in outer scope
		for _, id := range fn.Outputs {
			if s, ok := c.Get(id.Value); ok {
				writeArg := GetCopy(s)
				writeArg.FuncArg = true
				writeArg.ReadOnly = false
				outputs[id.Value] = writeArg
			}
		}
		c.PushScope(FuncScope)
		for i, id := range fn.Parameters {
			readArg := GetCopy(args[i])
			readArg.FuncArg = true
			readArg.ReadOnly = true
			c.Put(id.Value, readArg)
		}
		c.PutBulk(outputs)
	} else {
		c.PushScope(BlockScope)
	}
	defer c.PopScope()

	if compile {
		// get types from funcCache
		ft := c.FuncCache[mangled]
		for i, id := range fn.Outputs {
			o, ok := outputs[id.Value]
			if ok {
				if o.Type != ft.Outputs[i] {
					panic(fmt.Sprintf("Type of output argument before function does not match inferred type! Name: %s. Type before function: %s. Inferred Type: %s", id.Value, o.Type, ft.Outputs[i]))
				}
			} else {
				outputs[id.Value] = &Symbol{Type: ft.Outputs[i]}
			}
		}
	}
	for _, stmt := range fn.Body.Statements {
		c.doStatement(stmt, compile)
	}

	return outputs
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

func (c *Compiler) doCallExpression(ce *ast.CallExpression, compile bool) (res []*Symbol) {
	args := []*Symbol{}
	for _, arg := range ce.Arguments {
		args = append(args, c.doExpression(arg, compile)...)
	}

	codeModule := c.CodeCompiler.Compiler.Module
	mangled := mangle(ce.Function.Value, args)
	fn := codeModule.NamedFunction(mangled)
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
		// set compile flag to false to get outTypes
		fn, outTypes = c.doFuncStatement(template, args, mangled, compile)
	} else {
		outTypes = c.FuncCache[mangled].Outputs
	}

	res = make([]*Symbol, len(outTypes))
	if !compile {
		for i := range outTypes {
			res[i] = &Symbol{Type: outTypes[i]}
		}
		return
	}

	sretPtr := c.createEntryBlockAlloca(
		c.builder.GetInsertBlock().Parent(),
		"sret_tmp",
	)
	llvmArgs := []llvm.Value{sretPtr}
	for _, arg := range args {
		llvmArgs = append(llvmArgs, arg.Val)
	}

	c.builder.CreateCall(fn.Type().ElementType(), fn, llvmArgs, "call_tmp")

	for i, typ := range outTypes {
		gep := c.builder.CreateStructGEP(sretPtr.Type(), sretPtr, i, "ret_gep")
		load := c.builder.CreateLoad(c.mapToLLVMType(typ), gep, "ret_load")
		res[i] = &Symbol{Val: load, Type: typ}
	}
	return res
}

func (c *Compiler) doPrintStatement(ps *ast.PrintStatement, compile bool) {
	// Track format specifiers and arguments
	var formatStr string
	var args []llvm.Value

	// Generate format string based on expression types
	for _, expr := range ps.Expression {
		// If the expression is a string literal, check for embedded markers.
		if strLit, ok := expr.(*ast.StringLiteral); ok && compile {
			processed, newArgs := c.formatIdentifiers(strLit.Value)
			formatStr += processed + " " // separate expressions with a space
			args = append(args, newArgs...)
			continue
		}
		// Compile the expression and get its value and type
		syms := c.doExpression(expr, compile)
		if !compile {
			continue
		}
		for _, s := range syms {
			formatStr += defaultSpecifier(s.Type) + " "
			args = append(args, s.Val)
		}
	}
	if !compile {
		return
	}

	// Add newline and null terminator
	formatStr = strings.TrimSuffix(formatStr, " ") + "\n" // Remove trailing space
	formatConst := llvm.ConstString(formatStr, compile)   // true = add \0

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
