package compiler

import (
	"fmt"
	"strings"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

type Symbol struct {
	Val      llvm.Value
	Type     Type
	FuncArg  bool
	ReadOnly bool
}

func GetCopy(s *Symbol) (newSym *Symbol) {
	newSym = &Symbol{}
	newSym.Val = s.Val
	newSym.Type = s.Type
	newSym.FuncArg = s.FuncArg
	newSym.ReadOnly = s.ReadOnly
	return newSym
}

type Compiler struct {
	Scopes        []Scope[*Symbol]
	Context       llvm.Context
	Module        llvm.Module
	builder       llvm.Builder
	formatCounter int           // Track unique format strings
	CodeCompiler  *CodeCompiler // Optional reference for script compilation
	FuncCache     map[string]*Func
	Errors        []*token.CompileError
}

func NewCompiler(ctx llvm.Context, moduleName string, cc *CodeCompiler) *Compiler {
	module := ctx.NewModule(moduleName)
	builder := ctx.NewBuilder()

	return &Compiler{
		Scopes:        []Scope[*Symbol]{NewScope[*Symbol](FuncScope)},
		Context:       ctx,
		Module:        module,
		builder:       builder,
		formatCounter: 0,
		CodeCompiler:  cc,
		FuncCache:     make(map[string]*Func),
		Errors:        []*token.CompileError{},
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
		switch floatType.Width {
		case 32:
			return c.Context.FloatType()
		case 64:
			return c.Context.DoubleType()
		default:
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
		Put(c.Scopes, name, sym)
	}
}

func (c *Compiler) compileConditions(stmt *ast.LetStatement) (cond llvm.Value, hasConditions bool) {
	// Compile condition
	if len(stmt.Condition) == 0 {
		hasConditions = false
		return
	}

	hasConditions = true
	for i, expr := range stmt.Condition {
		condSyms := c.compileExpression(expr)
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

func (c *Compiler) compileStatement(stmt ast.Statement) {
	switch s := stmt.(type) {
	case *ast.LetStatement:
		c.compileLetStatement(s)
	case *ast.PrintStatement:
		c.compilePrintStatement(s)
	default:
		panic(fmt.Sprintf("Cannot handle statement type %T", s))
	}
}

// does conditional assignment
func (c *Compiler) compileCondStatement(stmt *ast.LetStatement, cond llvm.Value) {
	fn := c.builder.GetInsertBlock().Parent()
	ifBlock := c.Context.AddBasicBlock(fn, "if")
	elseBlock := c.Context.AddBasicBlock(fn, "else")
	contBlock := c.Context.AddBasicBlock(fn, "continue")
	c.builder.CreateCondBr(cond, ifBlock, elseBlock)

	// Create blocks and branch.
	c.builder.SetInsertPointAtEnd(ifBlock)
	ifValues, ifBlockEnd := c.compileIfCond(stmt)
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(elseBlock)
	elseValues, elseBlockEnd := c.compileElseCond(stmt, ifValues)
	c.builder.CreateBr(contBlock)

	// MERGE the branches.
	c.compileMergeBlock(stmt, contBlock, ifValues, elseValues, ifBlockEnd, elseBlockEnd)

	// Set the builder's position for the next statement.
	c.builder.SetInsertPointAtEnd(contBlock)
}

func (c *Compiler) compileIfCond(stmt *ast.LetStatement) ([]*Symbol, llvm.BasicBlock) {
	// Populate IF block.
	ifSymbols := []*Symbol{}

	// compileExpression already derefs any pointers
	// so don't worry about creating a load here
	for _, expr := range stmt.Value {
		ifSymbols = append(ifSymbols, c.compileExpression(expr)...)
	}

	return ifSymbols, c.builder.GetInsertBlock()
}

// ELSE block: pull "previous" symbols from scope and deref pointers
func (c *Compiler) compileElseCond(stmt *ast.LetStatement, trueSymbols []*Symbol) (elseSyms []*Symbol, elseEnd llvm.BasicBlock) {
	elseSyms = make([]*Symbol, len(stmt.Name))
	for i, ident := range stmt.Name {
		name := ident.Value

		prevSym, ok := Get(c.Scopes, name)
		if !ok {
			// first time: give a default zero
			prevSym = c.makeZeroValue(trueSymbols[i].Type)
			Put(c.Scopes, name, prevSym)
		}

		// Dereference pointers into raw values; if prevSym was an alloca, load it,
		// otherwise a no-op.
		elseSyms[i] = c.derefIfPointer(prevSym)
	}
	return elseSyms, c.builder.GetInsertBlock()
}

// MERGE block: build PHIs on the pure value type, then do final store if needed
func (c *Compiler) compileMergeBlock(
	stmt *ast.LetStatement,
	contBlk llvm.BasicBlock,
	ifSyms, elseSyms []*Symbol,
	ifEnd, elseEnd llvm.BasicBlock,
) {
	// position builder at top of contBlk
	c.builder.SetInsertPoint(contBlk, contBlk.FirstInstruction())

	// --- Phase 1: create all PHIs first on the value type
	// the phis MUST be at the top of the block
	phis := make([]llvm.Value, len(stmt.Name))
	for i, ident := range stmt.Name {
		ty := ifSyms[i].Type // both branches must agree
		phis[i] = c.builder.CreatePHI(c.mapToLLVMType(ty), ident.Value+"_phi")
	}

	// --- Phase 2: hook them up and do the stores/updates
	for i, ident := range stmt.Name {
		name := ident.Value
		phi := phis[i]
		ifVal := ifSyms[i].Val
		elseVal := elseSyms[i].Val

		// sanity
		if ifVal.IsNil() || elseVal.IsNil() {
			panic("nil incoming to PHI for " + name)
		}

		phi.AddIncoming(
			[]llvm.Value{ifVal, elseVal},
			[]llvm.BasicBlock{ifEnd, elseEnd},
		)

		// if the variable was stack-allocated, store back
		orig, _ := Get(c.Scopes, name)
		if ptr, ok := orig.Type.(Pointer); ok {
			// orig.Val holds the alloca
			c.createStore(phi, orig.Val, ptr.Elem)
		} else {
			// pure SSA: update the symbol to the PHI
			Put(c.Scopes, name, &Symbol{Val: phi, Type: ifSyms[i].Type})
		}
	}
}

func (c *Compiler) makeZeroValue(symType Type) *Symbol {
	s := &Symbol{
		Type: symType,
	}
	switch symType.Kind() {
	case IntKind:
		s.Val = llvm.ConstInt(c.Context.Int64Type(), 0, false)
	case FloatKind:
		s.Val = llvm.ConstFloat(c.Context.DoubleType(), 0.0)
	case StrKind:
		s.Val = c.createGlobalString("zero_str", "", llvm.PrivateLinkage)
	default:
		panic(fmt.Sprintf("unsupported type for zero value: %s", symType.String()))
	}
	return s
}

func (c *Compiler) compileSimpleStatement(stmt *ast.LetStatement) {
	syms := []*Symbol{}
	for _, expr := range stmt.Value {
		syms = append(syms, c.compileExpression(expr)...)
	}

	for i, ident := range stmt.Name {
		name := ident.Value
		// This is the new symbol from the right-hand side. It should represent a pure value.
		newRhsSymbol := syms[i]

		// Check if the variable on the LEFT-hand side (`name`) already exists
		// and if it represents a memory location (a pointer).
		if lhsSymbol, ok := Get(c.Scopes, name); ok && lhsSymbol.Type.Kind() == PointerKind {
			// CASE A: Storing to an existing memory location.
			// Example: `res = x * x` where `res` is a function output pointer.

			// `newRhsSymbol.Val` is the value to store (e.g., the result of `x * x`).
			// `lhsSymbol.Val` is the pointer to the memory location for `res`.
			// `newRhsSymbol.Type` is the type of the value being stored.
			c.createStore(newRhsSymbol.Val, lhsSymbol.Val, newRhsSymbol.Type)
			continue
		}

		Put(c.Scopes, name, newRhsSymbol)
	}
}

func (c *Compiler) compileLetStatement(stmt *ast.LetStatement) {
	cond, hasConditions := c.compileConditions(stmt)
	// TODO check type of variable has not changed
	if !hasConditions {
		c.compileSimpleStatement(stmt)
		return
	}

	c.compileCondStatement(stmt, cond)
}

func (c *Compiler) compileExpression(expr ast.Expression) (res []*Symbol) {
	s := &Symbol{}
	switch e := expr.(type) {
	case *ast.IntegerLiteral:
		s.Type = Int{Width: 64}
		s.Val = llvm.ConstInt(c.Context.Int64Type(), uint64(e.Value), false)
		res = []*Symbol{s}
	case *ast.FloatLiteral:
		s.Type = Float{Width: 64}
		s.Val = llvm.ConstFloat(c.Context.DoubleType(), e.Value)
		res = []*Symbol{s}
	case *ast.StringLiteral:
		s.Type = Str{}
		// create a global name for the literal
		globalName := fmt.Sprintf("str_literal_%d", c.formatCounter)
		c.formatCounter++
		s.Val = c.createGlobalString(globalName, e.Value, llvm.PrivateLinkage)
		res = []*Symbol{s}
	case *ast.Identifier:
		res = []*Symbol{c.compileIdentifier(e)}
	case *ast.InfixExpression:
		res = c.compileInfixExpression(e)
	case *ast.PrefixExpression:
		res = c.compilePrefixExpression(e)
	case *ast.CallExpression:
		res = c.compileCallExpression(e)
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
	sym, ok := Get(c.Scopes, name)
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
	Put(c.Scopes, name, newPtrSymbol)
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
func (c *Compiler) derefIfPointer(s *Symbol) *Symbol {
	var ptrType Pointer
	var ok bool
	if ptrType, ok = s.Type.(Pointer); !ok {
		return s
	}

	loadedVal := c.createLoad(s.Val, ptrType.Elem, "_load") // Use our new helper

	// Return a BRAND NEW symbol containing the result of the load.
	// Copy the symbol if we need other data like is it func arg, read only
	newS := GetCopy(s)
	newS.Val = loadedVal
	newS.Type = ptrType.Elem
	return newS
}

func (c *Compiler) compileIdentifier(ident *ast.Identifier) *Symbol {
	s, ok := Get(c.Scopes, ident.Value)
	if ok {
		return c.derefIfPointer(s)
	}

	cc := c.CodeCompiler.Compiler
	// no need to check ok as that is done in the typesolver
	s, _ = Get(cc.Scopes, ident.Value)
	return c.derefIfPointer(s)
}

func (c *Compiler) compileInfixExpression(expr *ast.InfixExpression) (res []*Symbol) {
	res = []*Symbol{}
	left := c.compileExpression(expr.Left)
	right := c.compileExpression(expr.Right)

	// Build a key based on the operator literal and the operand types.
	// We assume that the String() method for your custom Type returns "I64" for Int{Width: 64} and "F64" for Float{Width: 64}.
	for i := 0; i < len(left); i++ {
		l := c.derefIfPointer(left[i])

		r := c.derefIfPointer(right[i])

		key := opKey{
			Operator:  expr.Operator,
			LeftType:  l.Type.String(),
			RightType: r.Type.String(),
		}

		fn := defaultOps[key]
		res = append(res, fn(c, l, r, true))
	}
	return
}

// compilePrefixExpression handles unary operators (prefix expressions).
func (c *Compiler) compilePrefixExpression(expr *ast.PrefixExpression) (res []*Symbol) {
	// First compile the operand
	operand := c.compileExpression(expr.Right)

	for _, op := range operand {
		// Lookup in the defaultPrefixOps table
		key := unaryOpKey{
			Operator:    expr.Operator,
			OperandType: op.Type.String(),
		}

		fn := defaultUnaryOps[key]
		res = append(res, fn(c, c.derefIfPointer(op), true))
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

func (c *Compiler) compileFunc(fn *ast.FuncStatement, args []*Symbol, mangled string) (llvm.Value, []Type) {
	FuncType := c.FuncCache[mangled]

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
	PushScope(&c.Scopes, FuncScope)
	defer PopScope(&c.Scopes)

	sretPtr := function.Param(0)

	for i, outIdent := range fn.Outputs {
		outType := c.FuncCache[mangled].Outputs[i]

		fieldPtr := c.builder.CreateStructGEP(retStruct, sretPtr, i, outIdent.Value+"_ptr")

		Put(c.Scopes, outIdent.Value, &Symbol{
			Val:  fieldPtr,
			Type: Pointer{Elem: outType},
		})
	}

	for i, param := range fn.Parameters {
		arg := args[i]
		paramVal := function.Param(i + 1)
		paramVal.SetName(param.Value) // Good practice to name the LLVM value

		// Put the parameter directly into the symbol table as a VALUE, not a pointer.
		// Parameters in Pluto are assumed to be read-only, so it doesn't need a memory slot.
		Put(c.Scopes, param.Value, &Symbol{
			Val:  paramVal,
			Type: arg.Type,
		})
	}

	for _, stmt := range fn.Body.Statements {
		c.compileStatement(stmt)
	}
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

func (c *Compiler) compileCallExpression(ce *ast.CallExpression) (res []*Symbol) {
	args := []*Symbol{}
	argTypes := []Type{}
	for _, callArg := range ce.Arguments {
		args = append(args, c.compileExpression(callArg)...)
	}
	for _, arg := range args {
		argTypes = append(argTypes, arg.Type)
	}

	mangled := mangle(ce.Function.Value, argTypes)
	fn := c.Module.NamedFunction(mangled)
	var outTypes []Type
	if fn.IsNil() {
		// attempt to compile existing function template from Code
		fk := ast.FuncKey{
			FuncName: ce.Function.Value,
			Arity:    len(args),
		}

		code := c.CodeCompiler.Code
		// no need to check for ok as typesolver does that
		template := code.Func.Map[fk]

		savedBlock := c.builder.GetInsertBlock()
		fn, outTypes = c.compileFunc(template, args, mangled)
		c.builder.SetInsertPointAtEnd(savedBlock)
	} else {
		outTypes = c.FuncCache[mangled].Outputs
	}

	res = make([]*Symbol, len(outTypes))
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

func (c *Compiler) compilePrintStatement(ps *ast.PrintStatement) {
	// Track format specifiers and arguments
	var formatStr string
	var args []llvm.Value

	// Generate format string based on expression types
	for _, expr := range ps.Expression {
		// If the expression is a string literal, check for embedded markers.
		if strLit, ok := expr.(*ast.StringLiteral); ok {
			processed, newArgs := c.formatString(strLit)
			formatStr += processed + " " // separate expressions with a space
			args = append(args, newArgs...)
			continue
		}

		// Compile the expression and get its value and type
		syms := c.compileExpression(expr)
		for _, s := range syms {
			spec, err := defaultSpecifier(s.Type)
			if err != nil {
				cerr := &token.CompileError{
					Token: expr.Tok(),
					Msg:   err.Error(),
				}
				c.Errors = append(c.Errors, cerr)
				continue
			}
			formatStr += spec + " "
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
