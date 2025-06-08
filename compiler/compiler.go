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
		FuncCache:     make(map[string]*Func),
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

// does conditional assignment
func (c *Compiler) doCondAssign(stmt *ast.LetStatement, cond llvm.Value, trueSymbols map[string]*Symbol) {
	// This map will hold the symbols before the conditional
	prevSymbols := make(map[string]*Symbol)

	// possibly promote values to memory if trueSym is a pointer
	for _, ident := range stmt.Name {
		name := ident.Value
		trueSym := trueSymbols[name]
		prevSym, ok := c.Get(ident.Value)
		if !ok {
			prevSym = &Symbol{
				Val:  llvm.ConstInt(c.Context.Int64Type(), 0, false), // Default to 0
				Type: Int{Width: 64},
			}
			c.Put(name, prevSym)
		}
		prevSymbols[name] = prevSym

		if trueSym.Type.Kind() == PointerKind {
			// promoteToMemory does nothing if it is already in memory
			c.promoteToMemory(name)
		}
	}

	fn := c.builder.GetInsertBlock().Parent()
	ifBlock := c.Context.AddBasicBlock(fn, "if_block")
	elseBlock := c.Context.AddBasicBlock(fn, "else_block")
	contBlock := c.Context.AddBasicBlock(fn, "cont_block")

	c.builder.CreateCondBr(cond, ifBlock, elseBlock)
	c.builder.SetInsertPointAtEnd(ifBlock)
	c.builder.CreateBr(contBlock)
	c.builder.SetInsertPointAtEnd(elseBlock)
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
	for _, ident := range stmt.Name {
		// Look up the existing symbol to get its custom type.
		name := ident.Value
		finalSym, _ := c.Get(name)
		prevSym := prevSymbols[name]
		trueSym := trueSymbols[name]

		if finalSym.Type.Kind() == PointerKind {
			addr := finalSym.Val
			elemType := finalSym.Type.(Pointer).Elem

			elseVal := prevSym.Val
			if prevSym.Type.Kind() == PointerKind {
				// it was always in memory
				elseVal = c.createLoad(prevSym.Val, elemType, name+"_prev")
			}

			ifVal := trueSym.Val
			if trueSym.Type.Kind() == PointerKind {
				ifVal = c.createLoad(trueSym.Val, elemType, name+"_true")
			}

			valToStorePhi := c.builder.CreatePHI(c.mapToLLVMType(elemType), name+"_val_phi")
			valToStorePhi.AddIncoming(
				[]llvm.Value{ifVal, elseVal},
				[]llvm.BasicBlock{ifBlock, elseBlock},
			)
			c.createStore(valToStorePhi, addr, elemType)
			continue
		}

		// This case only happens if both prevSym and trueSym were values (not in memory)
		phi := c.builder.CreatePHI(c.mapToLLVMType(trueSym.Type), name+"_phi")
		phi.AddIncoming(
			[]llvm.Value{
				trueSym.Val, // Value from true block
				prevSym.Val, // Value from before the LetStatement
			},
			[]llvm.BasicBlock{ifBlock, elseBlock},
		)
		c.Put(ident.Value, &Symbol{Val: phi, Type: trueSym.Type})
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

	trueValues := make(map[string]*Symbol)
	for i, ident := range stmt.Name {
		trueValues[ident.Value] = syms[i]
	}

	// wait for true values to be put in c.Scopes above so they have types
	// TODO check type of variable has not changed
	if !compile {
		c.PutBulk(trueValues)
		return
	}

	// TODO check type of variable has not changed
	if !hasConditions {
		for i, ident := range stmt.Name {
			name := ident.Value
			// This is the new symbol from the right-hand side. It should represent a pure value.
			newRhsSymbol := syms[i]

			// Check if the variable on the LEFT-hand side (`name`) already exists
			// and if it represents a memory location (a pointer).
			if lhsSymbol, ok := c.Get(name); ok && lhsSymbol.Type.Kind() == PointerKind {
				// CASE A: Storing to an existing memory location.
				// Example: `res = x * x` where `res` is a function output pointer.

				// `newRhsSymbol.Val` is the value to store (e.g., the result of `x * x`).
				// `lhsSymbol.Val` is the pointer to the memory location for `res`.
				// `newRhsSymbol.Type` is the type of the value being stored.
				c.createStore(newRhsSymbol.Val, lhsSymbol.Val, newRhsSymbol.Type)
				continue
			}

			c.Put(name, newRhsSymbol)
		}
		return
	}

	c.doCondAssign(stmt, cond, trueValues)
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
		// We assume Str is i8* or u8*
		inst.SetAlignment(8)
	case Pointer:
		inst.SetAlignment(8)
	default:
		panic("Unsupported type for alignment" + typ.String())
	}
}

// promoteToMemory takes the name of a variable that currently holds a value,
// converts it into a memory-backed variable, and updates the symbol table.
// This is a high-level operation with an intentional side effect on the compiler's state.
func (c *Compiler) promoteToMemory(name string) *Symbol {
	sym, ok := c.Get(name)
	if !ok {
		panic("Compiler error: trying to promote to memory an undefined variable: " + name)
	}

	if sym.Type.Kind() == PointerKind {
		return sym
	}

	// Create a memory slot (alloca) in the function's entry block.
	// The type of the memory slot is the type of the value we're storing.
	alloca := c.createEntryBlockAlloca(c.mapToLLVMType(sym.Type), name+".mem")
	c.createStore(sym.Val, alloca, sym.Type)

	// Create the new symbol that represents the pointer to this memory.
	newPtrSymbol := &Symbol{
		Val:  alloca,
		Type: Pointer{Elem: sym.Type},
	}

	// CRITICAL: Update the symbol table immediately. This is the intended side effect.
	// From now on, any reference to `name` in the current scope will resolve to this new pointer symbol.
	c.Put(name, newPtrSymbol)
	return newPtrSymbol
}

// createStore is a simple helper that creates an LLVM store instruction and sets its alignment.
// It has NO side effects on the Go compiler state or symbols.
func (c *Compiler) createStore(val llvm.Value, ptr llvm.Value, valType Type) llvm.Value {
	storeInst := c.builder.CreateStore(val, ptr)
	setInstAlignment(storeInst, valType)
	return storeInst
}

// createLoad is a simple helper that creates an LLVM load instruction and sets its alignment.
// It has NO side effects on the Go compiler state or symbols.
// It returns the LLVM value that results from the load.
func (c *Compiler) createLoad(ptr llvm.Value, elemType Type, name string) llvm.Value {
	loadInst := c.builder.CreateLoad(c.mapToLLVMType(elemType), ptr, name)
	// The alignment is based on the type of data being loaded from memory.
	setInstAlignment(loadInst, elemType)
	return loadInst
}

// derefIfPointer checks a symbol. If it's a pointer, it returns a NEW symbol
// representing the value loaded from that pointer. Otherwise, it returns the
// original symbol unmodified. It has NO side effects.
// if compile is false then it only returns symbol with type info.
// This is used for type inference.
func (c *Compiler) derefIfPointer(s *Symbol, compile bool) *Symbol {
	var ptrType Pointer
	var ok bool
	if ptrType, ok = s.Type.(Pointer); !ok {
		return s
	}

	if !compile {
		return &Symbol{Type: ptrType.Elem}
	}

	loadedVal := c.createLoad(s.Val, ptrType.Elem, "") // Use our new helper

	// Return a BRAND NEW symbol containing the result of the load.
	return &Symbol{
		Val:  loadedVal,
		Type: ptrType.Elem,
	}
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
		res = append(res, fn(c, c.derefIfPointer(op, compile), compile))
	}
	return
}

func (c *Compiler) getReturnStruct(mangled string, outputTypes []Type) llvm.Type {
	// Check if we've already made a "%<mangled>_ret" in this module:
	retName := mangled + "_ret"

	if named := c.Module.GetTypeByName(retName); !named.IsNil() {
		return named
	}
	// Otherwise, define it exactly once:
	st := c.Context.StructCreateNamed(retName)
	fields := make([]llvm.Type, len(outputTypes))
	for i, t := range outputTypes {
		fields[i] = c.mapToLLVMType(t)
	}
	st.StructSetBody(fields, false)
	return st
}

func (c *Compiler) getFuncType(retStruct llvm.Type, inputs []llvm.Type) llvm.Type {
	ptrToRet := llvm.PointerType(retStruct, 0)
	llvmParams := append([]llvm.Type{ptrToRet}, inputs...)

	funcType := llvm.FunctionType(c.Context.VoidType(), llvmParams, false)
	return funcType
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

	outputs := c.inferTypesInBlock(fn, args)
	// body should have compiled outputs into symbol table
	FuncType.Outputs = c.inferOutTypes(fn, outputs)
	c.FuncCache[mangled] = FuncType
	return FuncType.Outputs
}

func (c *Compiler) compileFunc(fn *ast.FuncStatement, args []*Symbol, mangled string) (llvm.Value, []Type) {
	FuncType, ok := c.FuncCache[mangled]
	if !ok {
		panic("FuncType not in function cache! Mangled name: " + mangled)
	}

	llvmInputs := make([]llvm.Type, len(args))
	for i, a := range args {
		llvmInputs[i] = c.mapToLLVMType(a.Type)
	}

	retStruct := c.getReturnStruct(mangled, FuncType.Outputs)
	funcType := c.getFuncType(retStruct, llvmInputs)
	function := llvm.AddFunction(c.Module, mangled, funcType)

	sretAttr := c.Context.CreateTypeAttribute(llvm.AttributeKindID("sret"), retStruct)
	function.AddAttributeAtIndex(1, sretAttr) // Index 1 is the first parameter
	function.AddAttributeAtIndex(1, c.Context.CreateEnumAttribute(llvm.AttributeKindID("noalias"), 0))

	// Create entry block
	entry := c.Context.AddBasicBlock(function, "entry")
	savedBlock := c.builder.GetInsertBlock()
	c.builder.SetInsertPointAtEnd(entry)

	c.compileFuncBlock(fn, args, mangled, retStruct, function)

	c.builder.CreateRetVoid()

	// Restore the builder to where it was before compiling this function
	if !savedBlock.IsNil() {
		c.builder.SetInsertPointAtEnd(savedBlock)
	}

	return function, FuncType.Outputs
}

func (c *Compiler) compileFuncBlock(fn *ast.FuncStatement, args []*Symbol, mangled string, retStruct llvm.Type, function llvm.Value) {
	c.PushScope(FuncScope)
	defer c.PopScope()

	sretPtr := function.Param(0)

	for i, outIdent := range fn.Outputs {
		outType := c.FuncCache[mangled].Outputs[i]

		fieldPtr := c.builder.CreateStructGEP(retStruct, sretPtr, i, outIdent.Value+"_ptr")

		c.Put(outIdent.Value, &Symbol{
			Val:  fieldPtr,
			Type: Pointer{Elem: outType},
		})
	}

	for i, param := range fn.Parameters {
		arg := args[i]
		paramVal := function.Param(i + 1)
		alloca := c.createEntryBlockAlloca(c.mapToLLVMType(arg.Type), param.Value)
		c.createStore(paramVal, alloca, arg.Type)

		c.Put(param.Value, &Symbol{
			Val:  alloca,
			Type: Pointer{Elem: arg.Type},
		})
	}

	for _, stmt := range fn.Body.Statements {
		c.doStatement(stmt, true)
	}
}

func (c *Compiler) doFuncStatement(fn *ast.FuncStatement, args []*Symbol, mangled string, compile bool) (llvm.Value, []Type) {
	if !compile {
		return llvm.Value{}, c.inferFuncTypes(fn, args, mangled)
	}
	return c.compileFunc(fn, args, mangled)

}

// inferOutTypes assumes the all block LetStatements have been walked
// and assumes types are in Types SymTable
func (c *Compiler) inferOutTypes(fn *ast.FuncStatement, outputs map[string]*Symbol) []Type {
	outTypes := make([]Type, len(fn.Outputs))
	for i, o := range fn.Outputs {
		s := outputs[o.Value]
		if s == nil || s.Type == nil {
			panic(fmt.Sprintf("Could not infer output type for function %s. Output Arg %d, Name %s", fn.Token.Literal, i, o.Value))
		}
		outTypes[i] = s.Type
	}
	return outTypes
}

func (c *Compiler) inferTypesInBlock(fn *ast.FuncStatement, args []*Symbol) map[string]*Symbol {
	outputs := make(map[string]*Symbol)
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
	defer c.PopScope()

	for i, id := range fn.Parameters {
		readArg := GetCopy(args[i])
		readArg.FuncArg = true
		readArg.ReadOnly = true
		c.Put(id.Value, readArg)
	}
	c.PutBulk(outputs)

	for _, stmt := range fn.Body.Statements {
		c.doStatement(stmt, false)
	}

	for i, id := range fn.Outputs {
		s, ok := c.Get(id.Value)
		if !ok {
			panic(fmt.Sprintf("Could not infer type of output for function %s. Output arg index: %d. arg name: %s", fn.Token.Literal, i, id.Value))
		}
		o, exists := outputs[id.Value]
		if !exists {
			outputs[id.Value] = s
			continue

		}
		if s.Type != o.Type {
			panic(fmt.Sprintf("Cannot change type of variable! Type before entering function %s: %s. Type after entering: %s. Variable name: %s", fn.Token.Literal, s.Type, o.Type, id.Value))
		}
	}

	return outputs
}

func (c *Compiler) createEntryBlockAlloca(ty llvm.Type, name string) llvm.Value {
	current := c.builder.GetInsertBlock()
	fn := current.Parent()
	entry := fn.EntryBasicBlock()
	first := entry.FirstInstruction()

	if first.IsNil() {
		c.builder.SetInsertPointAtEnd(entry)
	} else {
		c.builder.SetInsertPointBefore(first)
	}

	alloca := c.builder.CreateAlloca(ty, name)
	c.builder.SetInsertPointAtEnd(current)
	return alloca
}

func (c *Compiler) doCallExpression(ce *ast.CallExpression, compile bool) (res []*Symbol) {
	args := []*Symbol{}
	for _, arg := range ce.Arguments {
		args = append(args, c.doExpression(arg, compile)...)
	}

	mangled := mangle(ce.Function.Value, args)
	fn := c.Module.NamedFunction(mangled)
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
		if compile {
			savedBlock := c.builder.GetInsertBlock()
			fn, outTypes = c.doFuncStatement(template, args, mangled, compile)
			c.builder.SetInsertPointAtEnd(savedBlock)

		} else {
			fn, outTypes = c.doFuncStatement(template, args, mangled, compile)
		}
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

	llvmInputs := make([]llvm.Type, len(args))
	for i, a := range args {
		llvmInputs[i] = c.mapToLLVMType(a.Type)
	}

	retStruct := c.getReturnStruct(mangled, outTypes)
	funcType := c.getFuncType(retStruct, llvmInputs)
	sretPtr := c.createEntryBlockAlloca(retStruct, "sret_tmp")
	llvmArgs := []llvm.Value{sretPtr}
	for _, arg := range args {
		llvmArgs = append(llvmArgs, arg.Val)
	}

	c.builder.CreateCall(funcType, fn, llvmArgs, "")

	for i, typ := range outTypes {
		res[i] = &Symbol{
			Val:  c.createLoad(c.builder.CreateStructGEP(retStruct, sretPtr, i, "ret_gep"), typ, ""),
			Type: typ,
		}
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
