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
	tmpCounter    int           // Temporary variable names counter
	CodeCompiler  *CodeCompiler // Optional reference for script compilation
	FuncCache     map[string]*Func
	ExprCache     map[ast.Expression]*ExprInfo
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
		tmpCounter:    0,
		CodeCompiler:  cc,
		FuncCache:     make(map[string]*Func),
		ExprCache:     make(map[ast.Expression]*ExprInfo),
		Errors:        []*token.CompileError{},
	}
}

func (c *Compiler) mapToLLVMType(t Type) llvm.Type {
	switch t.Kind() {
	case IntKind:
		intType := t.(Int)
		switch intType.Width {
		case 1:
			return c.Context.Int1Type()
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
	case RangeKind:
		r := t.(Range)
		// Lower the element type (e.g. Int{64} → I64)
		elemLLVM := c.mapToLLVMType(r.Iter)
		// Build a { i64, i64, i64 }-style struct type
		// false means “not packed”
		return llvm.StructType(
			[]llvm.Type{elemLLVM, elemLLVM, elemLLVM},
			false,
		)
	case PtrKind:
		ptrType := t.(Ptr)
		elemLLVM := c.mapToLLVMType(ptrType.Elem)
		return llvm.PointerType(elemLLVM, 0)
	case ArrayKind:
		// Arrays are backed by runtime dynamic vectors (opaque C structs).
		// Model them as opaque pointers here to interop cleanly with the C runtime.
		return llvm.PointerType(c.Context.Int8Type(), 0)
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

func (c *Compiler) constCString(value string) llvm.Value {
	globalName := fmt.Sprintf("static_str_%d", c.formatCounter)
	c.formatCounter++
	global := c.createGlobalString(globalName, value, llvm.PrivateLinkage)
	zero := llvm.ConstInt(c.Context.Int64Type(), 0, false)
	arrayType := llvm.ArrayType(c.Context.Int8Type(), len(value)+1)
	return c.builder.CreateGEP(arrayType, global, []llvm.Value{zero, zero}, "static_str_ptr")
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
			sym.Type = Ptr{Elem: Int{Width: 64}}
			sym.Val = c.makeGlobalConst(c.Context.Int64Type(), name, val, linkage)

		case *ast.FloatLiteral:
			val = llvm.ConstFloat(c.Context.DoubleType(), v.Value)
			sym.Type = Ptr{Elem: Float{Width: 64}}
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
		condSyms := c.compileExpression(expr, nil, false)
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
	i := 0
	for _, expr := range stmt.Value {
		res := c.compileExpression(expr, stmt.Name[i:], true)
		ifSymbols = append(ifSymbols, res...)
		i += len(res)
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
		if ptr, ok := orig.Type.(Ptr); ok {
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
	case ArrayKind:
		// Arrays are modeled as opaque pointers; null is a valid zero.
		s.Val = llvm.ConstPointerNull(llvm.PointerType(c.Context.Int8Type(), 0))
	default:
		panic(fmt.Sprintf("unsupported type for zero value: %s", symType.String()))
	}
	return s
}

func (c *Compiler) writeTo(idents []*ast.Identifier, syms []*Symbol) {
	if len(idents) == 0 {
		return
	}
	for i, newRhsSymbol := range syms {
		name := idents[i].Value

		// Check if the variable on the LEFT-hand side (`name`) already exists
		// and if it represents a memory location (a pointer).
		if lhsSymbol, ok := Get(c.Scopes, name); ok && lhsSymbol.Type.Kind() == PtrKind {
			c.createStore(newRhsSymbol.Val, lhsSymbol.Val, newRhsSymbol.Type)
			continue
		}

		Put(c.Scopes, name, newRhsSymbol)
	}
}

func (c *Compiler) compileSimpleStatement(stmt *ast.LetStatement) {
	syms := []*Symbol{}
	i := 0
	for _, expr := range stmt.Value {
		res := c.compileExpression(expr, stmt.Name[i:], true)
		syms = append(syms, res...)
		i += len(res)
	}
	c.writeTo(stmt.Name, syms)
}

func (c *Compiler) compileLetStatement(stmt *ast.LetStatement) {
	cond, hasConditions := c.compileConditions(stmt)
	if !hasConditions {
		c.compileSimpleStatement(stmt)
		return
	}

	c.compileCondStatement(stmt, cond)
}

func (c *Compiler) compileExpression(expr ast.Expression, dest []*ast.Identifier, isRoot bool) (res []*Symbol) {
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
	case *ast.RangeLiteral:
		if isRoot {
			res = c.compileRangeExpression(e)
			return
		}
		// Non-root range literal must be handled by the enclosing operator.
		panic("internal: unexpanded range literal in non-root position")
	case *ast.ArrayLiteral:
		return c.compileArrayExpression(e, isRoot)
	case *ast.ArrayRangeExpression:
		return c.compileArrayRangeExpression(e, dest, isRoot)
	case *ast.Identifier:
		res = []*Symbol{c.compileIdentifier(e)}
	case *ast.InfixExpression:
		res = c.compileInfixExpression(e, dest)
	case *ast.PrefixExpression:
		res = c.compilePrefixExpression(e, dest)
	case *ast.CallExpression:
		res = c.compileCallExpression(e)
	default:
		panic(fmt.Sprintf("unsupported expression type %T", e))
	}

	return
}

// compileArrayExpression materializes simple array literals into runtime vectors.
// Currently supports only a single row with no headers, e.g. [1 2 3 4].

func setInstAlignment(inst llvm.Value, t Type) {
	switch typ := t.(type) {
	case Int:
		// I1 is a special case
		if typ.Width == 1 {
			inst.SetAlignment(int(typ.Width))
			return
		}
		// divide by 8 as we want num bytes
		inst.SetAlignment(int(typ.Width >> 3))
	case Float:
		// divide by 8 as we want num bytes
		inst.SetAlignment(int(typ.Width >> 3))
	case Str:
		// We assume Str is i8* or u8*
		inst.SetAlignment(8)
	case Ptr:
		inst.SetAlignment(8)
	case Range:
		setInstAlignment(inst, typ.Iter)
	case Array:
		// Arrays are represented as opaque pointers to runtime vectors
		inst.SetAlignment(8)
	default:
		panic("Unsupported type for alignment" + typ.String())
	}
}

func (c *Compiler) makePtr(name string, s *Symbol) (ptr *Symbol, alreadyPtr bool) {
	if s.Type.Kind() == PtrKind {
		return s, true
	}

	// Create a memory slot (alloca) in the function's entry block.
	// The type of the memory slot is the type of the value we're storing.
	alloca := c.createEntryBlockAlloca(c.mapToLLVMType(s.Type), name+".mem")
	c.createStore(s.Val, alloca, s.Type)

	// Create the new symbol that represents the pointer to this memory.
	ptr = &Symbol{
		Val:  alloca,
		Type: Ptr{Elem: s.Type},
	}

	return ptr, false
}

// promoteToMemory takes the name of a variable that currently holds a value,
// converts it into a memory-backed variable, and updates the symbol table.
// This is a high-level operation with an intentional side effect on the compiler's state.
func (c *Compiler) promoteToMemory(name string) *Symbol {
	sym, ok := Get(c.Scopes, name)
	if !ok {
		panic("Compiler error: trying to promote to memory an undefined variable: " + name)
	}

	ptr, alreadyPtr := c.makePtr(name, sym)
	if alreadyPtr {
		return ptr
	}

	// CRITICAL: Update the symbol table immediately. This is the intended side effect.
	// From now on, any reference to `name` in the current scope will resolve to this new pointer symbol.
	Put(c.Scopes, name, ptr)
	return ptr
}

// createStore is a simple helper that creates an LLVM store instruction and sets its alignment.
// It has NO side effects on the Go compiler state or symbols.
// the val is the value to be stored and the ptr is the memory location it is to be stored to
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
	var ptrType Ptr
	var ok bool
	if ptrType, ok = s.Type.(Ptr); !ok {
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

func (c *Compiler) toRange(e *ast.RangeLiteral, typ Type) llvm.Value {
	rType := c.mapToLLVMType(typ)
	agg := llvm.Undef(rType)
	agg = c.builder.CreateInsertValue(agg, c.compileExpression(e.Start, nil, false)[0].Val, 0, "start")
	agg = c.builder.CreateInsertValue(agg, c.compileExpression(e.Stop, nil, false)[0].Val, 1, "stop")
	// insert step, defaulting to 1 if no step was provided
	var stepVal llvm.Value
	if e.Step != nil {
		stepVal = c.compileExpression(e.Step, nil, false)[0].Val
	} else {
		// default step = 1
		stepVal = llvm.ConstInt(c.Context.Int64Type(), 1, false)
	}
	agg = c.builder.CreateInsertValue(agg, stepVal, 2, "step")
	return agg
}

func (c *Compiler) compileRangeExpression(e *ast.RangeLiteral) (res []*Symbol) {
	s := &Symbol{}
	s.Type = Range{Iter: Int{Width: 64}}
	s.Val = c.toRange(e, s.Type)
	res = []*Symbol{s}
	return res
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

func (c *Compiler) compileInfixExpression(expr *ast.InfixExpression, dest []*ast.Identifier) (res []*Symbol) {
	info := c.ExprCache[expr]
	if len(info.Ranges) == 0 {
		return c.compileInfixBasic(expr, info)
	}
	return c.compileInfixRanges(expr, info, dest)
}

// compileInfix compiles a single binary operation between two symbols.
// It handles pointer operands, array-scalar broadcasting, and delegates to
// the default operator table for scalar work.
func (c *Compiler) compileInfix(op string, left *Symbol, right *Symbol, expected Type) *Symbol {
	l := c.derefIfPointer(left)
	r := c.derefIfPointer(right)

	if expectedArr, ok := expected.(Array); ok {
		if l.Type.Kind() == ArrayKind && r.Type.Kind() == ArrayKind {
			return c.compileArrayArrayInfix(op, l, r, expectedArr.ColTypes[0])
		}

		// Handle Array-Scalar operations
		arr := l
		scalar := r
		if arr.Type.Kind() != ArrayKind {
			arr, scalar = scalar, arr
		}
		return c.compileArrayScalarInfix(op, arr, scalar, expectedArr.ColTypes[0])
	}

	key := opKey{
		Operator:  op,
		LeftType:  l.Type,
		RightType: r.Type,
	}
	return defaultOps[key](c, l, r, true)
}

func (c *Compiler) compileInfixBasic(expr *ast.InfixExpression, info *ExprInfo) (res []*Symbol) {
	left := c.compileExpression(expr.Left, nil, false)
	right := c.compileExpression(expr.Right, nil, false)

	for i := 0; i < len(left); i++ {
		res = append(res, c.compileInfix(expr.Operator, left[i], right[i], info.OutTypes[i]))
	}
	return res
}

// compileArrayScalarInfix lowers an infix op between an array and a scalar by
// broadcasting the scalar over the array and applying the scalar op element-wise.

func (c *Compiler) constI64(v int64) llvm.Value {
	return llvm.ConstInt(c.Context.Int64Type(), uint64(v), false)
}

// rangeZeroToN builds a Range{start:0, stop:n, step:1} aggregate
func (c *Compiler) rangeZeroToN(n llvm.Value) llvm.Value {
	rType := c.mapToLLVMType(Range{Iter: Int{Width: 64}})
	agg := llvm.Undef(rType)
	agg = c.builder.CreateInsertValue(agg, c.constI64(0), 0, "start")
	agg = c.builder.CreateInsertValue(agg, n, 1, "stop")
	agg = c.builder.CreateInsertValue(agg, c.constI64(1), 2, "step")
	return agg
}

func (c *Compiler) initRangeArrayAccumulators(outTypes []Type) []*arrayRangeAccumulator {
	accs := make([]*arrayRangeAccumulator, len(outTypes))
	for i, outType := range outTypes {
		arrType, ok := outType.(Array)
		if !ok {
			continue
		}
		accs[i] = c.newArrayRangeAccumulator(arrType)
	}
	return accs
}

// Modified compileInfixRanges - cleaner with destinations
func (c *Compiler) compileInfixRanges(expr *ast.InfixExpression, info *ExprInfo, dest []*ast.Identifier) (res []*Symbol) {
	// Push a new scope for this expression block
	PushScope(&c.Scopes, BlockScope)
	defer PopScope(&c.Scopes)

	// Setup outputs (like function outputs)
	outputs := c.setupRangeOutputs(dest, info.OutTypes)
	arrayAccs := c.initRangeArrayAccumulators(info.OutTypes)

	leftRew := info.Rewrite.(*ast.InfixExpression).Left
	rightRew := info.Rewrite.(*ast.InfixExpression).Right

	// Build nested loops
	c.withLoopNest(info.Ranges, func() {
		left := c.compileExpression(leftRew, nil, false)
		right := c.compileExpression(rightRew, nil, false)

		for i := 0; i < len(left); i++ {
			expected := info.OutTypes[i]
			computed := c.compileInfix(expr.Operator, left[i], right[i], expected)

			if acc := arrayAccs[i]; acc != nil && computed.Type.Kind() != ArrayKind {
				c.pushArrayRangeValue(acc, computed)
				continue
			}

			c.createStore(computed.Val, outputs[i].Val, computed.Type)
		}
	})

	out := make([]*Symbol, len(outputs))
	for i := range outputs {
		if acc := arrayAccs[i]; acc != nil && acc.used {
			out[i] = c.arrayRangeResult(acc)
			continue
		}
		elemType := outputs[i].Type.(Ptr).Elem
		out[i] = &Symbol{
			Val:  c.createLoad(outputs[i].Val, elemType, "final"),
			Type: elemType,
		}
	}
	return out
}

func (c *Compiler) setupRangeOutputs(dest []*ast.Identifier, outTypes []Type) []*Symbol {
	outputs := make([]*Symbol, len(dest))

	for i, outType := range outTypes {
		name := dest[i].Value

		// Create temp storage for the loop
		ptr := c.createEntryBlockAlloca(c.mapToLLVMType(outType), name+"_tmp")
		var val llvm.Value
		if sym, ok := Get(c.Scopes, name); ok {
			val = c.derefIfPointer(sym).Val
		} else {
			val = c.makeZeroValue(outType).Val
		}
		c.createStore(val, ptr, outType)

		// Shadow the variable in current scope
		// Now any reference to 'name' in the expression uses our temp
		outputs[i] = &Symbol{
			Val:  ptr,
			Type: Ptr{Elem: outType},
		}
		Put(c.Scopes, name, outputs[i])
	}

	return outputs
}

// Destination-aware prefix compilation,
// mirroring compileInfixExpression/compileInfixRanges.

func (c *Compiler) compilePrefixExpression(expr *ast.PrefixExpression, dest []*ast.Identifier) (res []*Symbol) {
	info, ok := c.ExprCache[expr]
	if !ok {
		info = &ExprInfo{Ranges: []*RangeInfo{}}
	}
	if len(info.Ranges) == 0 {
		return c.compilePrefixBasic(expr, info)
	}
	return c.compilePrefixRanges(expr, info, dest)
}

// compilePrefix compiles a unary operation on a symbol, delegating array
// broadcasting to compileArrayUnaryPrefix and scalar lowering to defaultUnaryOps.
func (c *Compiler) compilePrefix(op string, operand *Symbol, expected Type) *Symbol {
	sym := c.derefIfPointer(operand)
	if expectedArr, ok := expected.(Array); ok {
		return c.compileArrayUnaryPrefix(op, sym, expectedArr)
	}
	key := unaryOpKey{Operator: op, OperandType: sym.Type}
	return defaultUnaryOps[key](c, sym, true)
}

func (c *Compiler) compilePrefixBasic(expr *ast.PrefixExpression, info *ExprInfo) (res []*Symbol) {
	operand := c.compileExpression(expr.Right, nil, false)
	for i, opSym := range operand {
		res = append(res, c.compilePrefix(expr.Operator, opSym, info.OutTypes[i]))
	}
	return res
}

// compileArrayUnaryPrefix broadcasts a unary operator over a numeric array.

func (c *Compiler) compilePrefixRanges(expr *ast.PrefixExpression, info *ExprInfo, dest []*ast.Identifier) (res []*Symbol) {
	// New scope so we can temporarily shadow outputs like mini-functions do.
	PushScope(&c.Scopes, BlockScope)
	defer PopScope(&c.Scopes)

	// Allocate/seed per-destination temps (seed from existing value or zero).
	outputs := c.setupRangeOutputs(dest, info.OutTypes)

	// Rewritten operand under tmp iters.
	rightRew := info.Rewrite.(*ast.PrefixExpression).Right

	// Drive the loops and store into outputs each trip.
	c.withLoopNest(info.Ranges, func() {
		ops := c.compileExpression(rightRew, nil, false)

		for i := 0; i < len(ops); i++ {
			computed := c.compilePrefix(expr.Operator, ops[i], info.OutTypes[i])
			c.createStore(computed.Val, outputs[i].Val, computed.Type)
		}
	})

	// Materialize final values
	out := make([]*Symbol, len(outputs))
	for i := range outputs {
		elemType := outputs[i].Type.(Ptr).Elem
		out[i] = &Symbol{
			Val:  c.createLoad(outputs[i].Val, elemType, "final"),
			Type: elemType,
		}
	}
	return out
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

func (c *Compiler) compileFunc(fn *ast.FuncStatement, args []*Symbol, mangled string, outTypes []Type, retStruct llvm.Type, funcType llvm.Type) llvm.Value {
	function := llvm.AddFunction(c.Module, mangled, funcType)

	sretAttr := c.Context.CreateTypeAttribute(llvm.AttributeKindID("sret"), retStruct)
	function.AddAttributeAtIndex(1, sretAttr) // Index 1 is the first parameter
	function.AddAttributeAtIndex(1, c.Context.CreateEnumAttribute(llvm.AttributeKindID("noalias"), 0))

	// Create entry block
	entry := c.Context.AddBasicBlock(function, "entry")
	savedBlock := c.builder.GetInsertBlock()
	c.builder.SetInsertPointAtEnd(entry)

	c.compileFuncBlock(fn, outTypes, args, retStruct, function)

	c.builder.CreateRetVoid()

	// Restore the builder to where it was before compiling this function
	if !savedBlock.IsNil() {
		c.builder.SetInsertPointAtEnd(savedBlock)
	}

	return function
}

func (c *Compiler) compileFuncBlock(fn *ast.FuncStatement, outTypes []Type, args []*Symbol, retStruct llvm.Type, function llvm.Value) {
	PushScope(&c.Scopes, FuncScope)
	defer PopScope(&c.Scopes)

	sretPtr := function.Param(0)

	// 1) Install outputs into scope and remember their storage
	outSyms := make(map[string]*Symbol, len(fn.Outputs))
	for i, outIdent := range fn.Outputs {
		outType := outTypes[i]
		fieldPtr := c.builder.CreateStructGEP(retStruct, sretPtr, i, outIdent.Value+"_ptr")

		sym := &Symbol{
			Val:      fieldPtr,
			Type:     Ptr{Elem: outType},
			FuncArg:  true,
			ReadOnly: false,
		}
		Put(c.Scopes, outIdent.Value, sym)
		outSyms[outIdent.Value] = sym
	}

	// collect which param‐indices are ranges
	rangeIdxs := []int{}
	// recursive nester: for N ranges, make N loops via createLoop
	scalars := make(map[string]*Symbol)
	for i, arg := range args {
		name := fn.Parameters[i].Value

		if arg.Type.Kind() == RangeKind {
			rangeIdxs = append(rangeIdxs, i)
			// do nothing as arg will become an iter in createLoop
			continue
		}

		// Scalar param value (after sret)
		paramVal := function.Param(i + 1)

		// If this param is also an output, seed the output storage and don't add a scalar
		if outSym, isOut := outSyms[name]; isOut {
			elemT := outSym.Type.(Ptr).Elem
			c.createStore(paramVal, outSym.Val, elemT)
			continue
		}

		scalars[fn.Parameters[i].Value] = &Symbol{
			Val:      function.Param(i + 1), // Get the scalar value from the function's params
			Type:     arg.Type,
			FuncArg:  true,
			ReadOnly: true,
		}
	}

	c.nest(fn, scalars, rangeIdxs, function, 0, make(map[string]*Symbol))
}

func (c *Compiler) nest(fn *ast.FuncStatement, scalars map[string]*Symbol, rangeIdxs []int, function llvm.Value, level int, iters map[string]*Symbol) {
	if level == len(rangeIdxs) {
		c.compileBlockWithArgs(fn, scalars, iters)
		return
	}

	// pick the next range literal & its Symbol
	idx := rangeIdxs[level]
	c.createLoop(function.Param(idx+1), func(iter llvm.Value) {
		iters[fn.Parameters[idx].Value] = &Symbol{
			Val:      iter,
			Type:     Int{Width: 64}, // for now assume range has type I64
			FuncArg:  true,
			ReadOnly: false,
		}
		c.nest(fn, scalars, rangeIdxs, function, level+1, iters)
	})
}

func (c *Compiler) compileBlockWithArgs(fn *ast.FuncStatement, scalars map[string]*Symbol, iters map[string]*Symbol) {
	PutBulk(c.Scopes, scalars)
	PutBulk(c.Scopes, iters)

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

func (c *Compiler) compileArgs(ce *ast.CallExpression) (args []*Symbol, argTypes []Type) {
	for _, callArg := range ce.Arguments {
		res := c.compileExpression(callArg, nil, true) // isRoot is true as we want range expressions to be sent as is
		for _, r := range res {
			args = append(args, r)
			argTypes = append(argTypes, r.Type)
		}
	}
	return
}

func (c *Compiler) compileCallExpression(ce *ast.CallExpression) (res []*Symbol) {
	args, argTypes := c.compileArgs(ce)
	mangled := mangle(ce.Function.Value, argTypes)
	Func := c.FuncCache[mangled]
	retStruct := c.getReturnStruct(mangled, Func.OutTypes)
	sretPtr := c.createEntryBlockAlloca(retStruct, "sret_tmp")

	llvmInputs := []llvm.Type{}
	llvmArgs := []llvm.Value{sretPtr}
	for _, a := range args {
		llvmInputs = append(llvmInputs, c.mapToLLVMType(a.Type))
		llvmArgs = append(llvmArgs, a.Val)
	}

	fn := c.Module.NamedFunction(mangled)
	funcType := c.getFuncType(retStruct, llvmInputs)
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
		fn = c.compileFunc(template, args, mangled, Func.OutTypes, retStruct, funcType)
		c.builder.SetInsertPointAtEnd(savedBlock)
	}

	c.builder.CreateCall(funcType, fn, llvmArgs, "") // don't name the call as it returns void. those instructions cannot be named as per llvm

	res = make([]*Symbol, len(Func.OutTypes))
	for i, typ := range Func.OutTypes {
		res[i] = &Symbol{
			Val:  c.createLoad(c.builder.CreateStructGEP(retStruct, sretPtr, i, "ret_gep"), typ, ""),
			Type: typ,
		}
	}
	return res
}

// extract numeric fields of a range struct
func (c *Compiler) rangeComponents(r llvm.Value) (start, stop, step llvm.Value) {
	start = c.builder.CreateExtractValue(r, 0, "start")
	stop = c.builder.CreateExtractValue(r, 1, "stop")
	step = c.builder.CreateExtractValue(r, 2, "step")
	return
}

func (c *Compiler) rangeStrArg(s *Symbol) (arg llvm.Value) {
	start, stop, step := c.rangeComponents(s.Val)

	// call range_i64_str
	fnType, fn := c.GetCFunc(RANGE_I64_STR)
	arg = c.builder.CreateCall(
		fnType,
		fn,
		[]llvm.Value{start, stop, step},
		RANGE_I64_STR,
	)
	return
}

func (c *Compiler) floatStrArg(s *Symbol) llvm.Value {
	if s.Type.(Float).Width == 32 {
		fnTy, fn := c.GetCFunc(F32_STR) // char* f32_str(float)
		return c.builder.CreateCall(fnTy, fn, []llvm.Value{s.Val}, "f32_str")
	}
	// default to 64-bit path
	fnTy, fn := c.GetCFunc(F64_STR) // char* f64_str(double)
	return c.builder.CreateCall(fnTy, fn, []llvm.Value{s.Val}, "f64_str")
}

func (c *Compiler) free(ptrs []llvm.Value) {
	fnType, fn := c.GetCFunc(FREE)
	for _, ptr := range ptrs {
		c.builder.CreateCall(fnType, fn, []llvm.Value{ptr}, "") // name should be empty as it returns a void
	}
}

func (c *Compiler) printf(args []llvm.Value) {
	fnType, fn := c.GetCFunc(PRINTF)
	c.builder.CreateCall(fnType, fn, args, PRINTF)
}

func (c *Compiler) compilePrintStatement(ps *ast.PrintStatement) {
	// Track format specifiers and arguments
	var formatStr string
	var args []llvm.Value
	var toFree []llvm.Value

	// Generate format string based on expression types
	for _, expr := range ps.Expression {
		// If the expression is a string literal, check for embedded markers.
		if strLit, ok := expr.(*ast.StringLiteral); ok {
			processed, newArgs, toFreeArgs := c.formatString(strLit)
			formatStr += processed + " " // separate expressions with a space
			args = append(args, newArgs...)
			toFree = append(toFree, toFreeArgs...)
			continue
		}

		// Compile the expression and get its value and type
		syms := c.compileExpression(expr, nil, true)
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
			switch s.Type.Kind() {
			case RangeKind:
				strPtr := c.rangeStrArg(s) // returns i8*
				args = append(args, strPtr)
				toFree = append(toFree, strPtr)
			case FloatKind:
				strPtr := c.floatStrArg(s) // returns i8*
				args = append(args, strPtr)
				toFree = append(toFree, strPtr)
			case ArrayKind:
				arrType := s.Type.(Array)
				if len(arrType.ColTypes) == 0 || arrType.ColTypes[0].Kind() == UnresolvedKind {
					args = append(args, c.constCString("[]"))
					continue
				}
				strPtr := c.arrayStrArg(s)
				args = append(args, strPtr)
				toFree = append(toFree, strPtr)
			default:
				args = append(args, s.Val)
			}
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

	// Call printf with all arguments
	allArgs := append([]llvm.Value{formatPtr}, args...)
	c.printf(allArgs)
	c.free(toFree)
}

// Helper function to generate final output
func (c *Compiler) GenerateIR() string {
	return c.Module.String()
}
