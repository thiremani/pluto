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
	FuncArg  bool // Is this a function parameter?
	ReadOnly bool // Can this be written to? (for output params vs input params)
}

type funcArgs struct {
	args        []*Symbol
	outputs     []*Symbol
	iterIndices []int
	iters       map[string]*Symbol
	arrayAccs   []*ArrayAccumulator
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
	case ArrayRangeKind:
		arrRange := t.(ArrayRange)
		arrayPtr := llvm.PointerType(c.Context.Int8Type(), 0)
		rangeTy := c.mapToLLVMType(arrRange.Range)
		return llvm.StructType([]llvm.Type{arrayPtr, rangeTy}, false)
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
	zero := c.ConstI64(0)
	arrayType := llvm.ArrayType(c.Context.Int8Type(), len(value)+1)
	return c.builder.CreateGEP(arrayType, global, []llvm.Value{zero, zero}, "static_str_ptr")
}

func (c *Compiler) createFormatStringGlobal(formatted string) llvm.Value {
	formatConst := llvm.ConstString(formatted, true)
	globalName := fmt.Sprintf("str_fmt_%d", c.formatCounter)
	c.formatCounter++

	arrayLength := len(formatted) + 1
	arrayType := llvm.ArrayType(c.Context.Int8Type(), arrayLength)
	formatGlobal := llvm.AddGlobal(c.Module, arrayType, globalName)
	formatGlobal.SetInitializer(formatConst)
	formatGlobal.SetGlobalConstant(true)

	zero := c.ConstI64(0)
	return c.builder.CreateGEP(arrayType, formatGlobal, []llvm.Value{zero, zero}, "fmt_ptr")
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
			val = c.ConstI64(uint64(v.Value))
			sym.Type = Ptr{Elem: Int{Width: 64}}
			sym.Val = c.makeGlobalConst(c.Context.Int64Type(), name, val, linkage)

		case *ast.FloatLiteral:
			val = c.ConstF64(v.Value)
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

	// --- Phase 2: Handle ownership and copying for PHI merges
	//
	// PHI nodes merge values from two branches. We need to ensure consistent ownership:
	//
	// For strings:
	// - Both static: result is static (no copy needed)
	// - Both heap: need to copy BOTH (only one executes, so copy for value semantics)
	// - Mixed: copy the static one to heap
	//
	// For arrays:
	// - Always copy BOTH branches (arrays always have value semantics, no ownership transfer)
	//
	// Only one branch executes at runtime, so we copy to ensure the result owns its value.
	for i := range stmt.Name {
		switch t := ifSyms[i].Type.(type) {
		case Str:
			ifStatic := t.Static
			elseStrType, _ := elseSyms[i].Type.(Str)
			elseStatic := elseStrType.Static

			// Copy based on storage class to ensure consistent ownership
			if ifStatic && !elseStatic {
				// If-branch is static, else-branch is heap - copy if-branch to heap
				terminator := ifEnd.LastInstruction()
				c.builder.SetInsertPointBefore(terminator)
				ifSyms[i] = c.deepCopyIfNeeded(ifSyms[i])
			} else if !ifStatic && elseStatic {
				// Else-branch is static, if-branch is heap - copy else-branch to heap
				terminator := elseEnd.LastInstruction()
				c.builder.SetInsertPointBefore(terminator)
				elseSyms[i] = c.deepCopyIfNeeded(elseSyms[i])
			} else if !ifStatic && !elseStatic {
				// Both heap strings - copy both to maintain value semantics
				terminator := ifEnd.LastInstruction()
				c.builder.SetInsertPointBefore(terminator)
				ifSyms[i] = c.deepCopyIfNeeded(ifSyms[i])

				terminator = elseEnd.LastInstruction()
				c.builder.SetInsertPointBefore(terminator)
				elseSyms[i] = c.deepCopyIfNeeded(elseSyms[i])
			}
			// If both static, no copy needed (static literals live forever)

		case Array:
			// Arrays always have value semantics - copy both branches
			terminator := ifEnd.LastInstruction()
			c.builder.SetInsertPointBefore(terminator)
			ifSyms[i] = c.deepCopyIfNeeded(ifSyms[i])

			terminator = elseEnd.LastInstruction()
			c.builder.SetInsertPointBefore(terminator)
			elseSyms[i] = c.deepCopyIfNeeded(elseSyms[i])
		}
	}

	// --- Phase 3: Hook up PHIs and do the stores/updates
	// Position builder at end of continue block
	c.builder.SetInsertPointAtEnd(contBlk)

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

		// Determine the result type, preserving Str.Static flag
		// After phase 2 normalization, both branches have the same storage class
		resultType := ifSyms[i].Type
		if strType, ok := ifSyms[i].Type.(Str); ok {
			elseStrType, _ := elseSyms[i].Type.(Str)
			// Both branches should have same Static flag after phase 2 normalization
			resultType = Str{Static: strType.Static && elseStrType.Static}
		}

		// if the variable was stack-allocated, store back
		orig, _ := Get(c.Scopes, name)
		if ptr, ok := orig.Type.(Ptr); ok {
			// orig.Val holds the alloca
			c.createStore(phi, orig.Val, ptr.Elem)
		} else {
			// pure SSA: update the symbol to the PHI
			Put(c.Scopes, name, &Symbol{
				Val:  phi,
				Type: resultType,
			})
		}
	}
}

func (c *Compiler) makeZeroValue(symType Type) *Symbol {
	s := &Symbol{
		Type: symType,
	}
	switch symType.Kind() {
	case IntKind:
		s.Val = c.ConstI64(0)
	case FloatKind:
		s.Val = c.ConstF64(0)
	case StrKind:
		s.Type = Str{Static: true} // Zero value is static empty string
		s.Val = c.createGlobalString("zero_str", "", llvm.PrivateLinkage)
	case ArrayKind:
		// Create an actual zero-length array (not null)
		// Arrays are always heap-allocated, even when empty
		arrayType := symType.(Array)
		if len(arrayType.ColTypes) > 0 && arrayType.ColTypes[0].Kind() != UnresolvedKind {
			elemType := arrayType.ColTypes[0]
			nConst := c.ConstI64(0)
			arrVal := c.CreateArrayForType(elemType, nConst)
			s.Val = c.builder.CreateBitCast(arrVal, llvm.PointerType(c.Context.Int8Type(), 0), "arr_zero")
			// No need for ReadOnly flag - arrays are always heap, always freed (unless FuncArg)
		} else {
			// If element type is unresolved, use null pointer temporarily
			// This should be rare and will be resolved later
			s.Val = llvm.ConstPointerNull(llvm.PointerType(c.Context.Int8Type(), 0))
		}
	case RangeKind:
		s.Val = c.CreateRange(c.ConstI64(0), c.ConstI64(0), c.ConstI64(1), symType)
	case ArrayRangeKind:
		arrRangeType := symType.(ArrayRange)
		// Create zero value for the array part
		arraySym := c.makeZeroValue(arrRangeType.Array)
		// Create zero value for the range part
		rangeSym := c.makeZeroValue(arrRangeType.Range)
		s.Val = c.CreateArrayRange(arraySym.Val, rangeSym.Val, arrRangeType)
	default:
		panic(fmt.Sprintf("unsupported type for zero value: %s", symType.String()))
	}
	return s
}

func (c *Compiler) writeTo(idents []*ast.Identifier, syms []*Symbol, rhsNames []string) {
	if len(idents) == 0 {
		return
	}

	// Determine copy requirements based on storage class and ownership
	needsCopy := make([]bool, len(syms))
	for i, rhsSym := range syms {
		// Check if this is a static string (no copy needed - immutable, lives forever)
		if strType, ok := rhsSym.Type.(Str); ok && strType.Static {
			needsCopy[i] = false
			continue
		}

		// Function arguments are borrowed references - no copy needed
		if rhsSym.FuncArg {
			needsCopy[i] = false
			continue
		}

		// Check if RHS is a variable that's being overwritten in LHS
		canTransfer := false
		if rhsNames[i] != "" {
			// RHS is a variable - check if it's in the LHS
			for _, lhsIdent := range idents {
				if lhsIdent.Value == rhsNames[i] {
					// This variable is being overwritten, can transfer ownership
					canTransfer = true
					break
				}
			}
		}

		// If RHS is not a variable, or variable is not in LHS, we need to copy
		// (because the original owner keeps the value)
		needsCopy[i] = !canTransfer
	}

	// Process assignments in two phases to avoid use-after-free
	for phase := 0; phase < 2; phase++ {
		for i, ident := range idents {
			shouldProcess := (phase == 0 && !needsCopy[i]) || (phase == 1 && needsCopy[i])
			if shouldProcess {
				c.performAssignment(ident.Value, syms[i], needsCopy[i])
			}
		}
	}
}

// performAssignment executes a single assignment with optional copying
func (c *Compiler) performAssignment(name string, rhsSym *Symbol, shouldCopy bool) {
	if lhsSymbol, ok := Get(c.Scopes, name); ok {
		c.assignToExisting(name, lhsSymbol, rhsSym, shouldCopy)
	} else {
		c.assignToNew(name, rhsSym, shouldCopy)
	}
}

// assignToExisting handles assignment to an existing variable
func (c *Compiler) assignToExisting(name string, lhsSym *Symbol, rhsSym *Symbol, shouldCopy bool) {
	// Free old value if different (freeSymbolValue checks Static flag internally)
	if lhsSym.Val != rhsSym.Val {
		c.freeSymbolValue(lhsSym)
	}

	// Handle type coercion
	valueToStore := c.coerceValueForAssignment(lhsSym.Type, rhsSym)

	// Copy only if needed
	if shouldCopy {
		valueToStore = c.deepCopyIfNeeded(valueToStore)
	}

	// Store
	if lhsSym.Type.Kind() == PtrKind {
		c.createStore(valueToStore.Val, lhsSym.Val, valueToStore.Type)
	} else {
		Put(c.Scopes, name, valueToStore)
	}
}

// assignToNew handles assignment to a new variable
func (c *Compiler) assignToNew(name string, rhsSym *Symbol, shouldCopy bool) {
	var newSymbol *Symbol
	if shouldCopy {
		newSymbol = c.deepCopyIfNeeded(rhsSym)
	} else {
		newSymbol = rhsSym
	}
	Put(c.Scopes, name, newSymbol)
}

// freeSymbolValue frees the memory owned by a symbol
func (c *Compiler) freeSymbolValue(sym *Symbol) {
	// Don't free function arguments - they're borrowed references
	if sym.FuncArg {
		return
	}

	switch t := sym.Type.(type) {
	case Str:
		// Only free heap-allocated strings, not static literals
		if !t.Static {
			c.free([]llvm.Value{sym.Val})
		}
	case Array:
		// Arrays are always heap-allocated (except null pointers for unresolved types)
		if len(t.ColTypes) > 0 && t.ColTypes[0].Kind() != UnresolvedKind {
			c.freeArray(sym.Val, t.ColTypes[0])
		}
	}
}

// coerceValueForAssignment handles type coercion when assigning values.
// Currently handles single-element array unwrapping for scalar accumulation in function iterations.
//
// When a function iterates over range/array parameters, output variables are allocated
// as scalar temporaries. If the function body assigns a single-element array like [i],
// we extract the scalar value for proper accumulation.
//
// Example:
//
//	res = ConstVec(i)
//	    res = [i]
//
// When called with ConstVec(0:5), each iteration assigns [0], [1], etc. to a scalar temp.
// This function extracts the scalar values 0, 1, 2, 3, 4 for accumulation.
func (c *Compiler) coerceValueForAssignment(targetType Type, value *Symbol) *Symbol {
	// Unwrap pointer type to get the actual target type
	actualTargetType := targetType
	if ptr, ok := targetType.(Ptr); ok {
		actualTargetType = ptr.Elem
	}

	// Check if we're assigning an array to a scalar
	if actualTargetType.Kind() != ArrayKind && value.Type.Kind() == ArrayKind {
		return c.unwrapSingleElementArray(value)
	}

	return value
}

// unwrapSingleElementArray extracts the first element from a single-element array.
// If the array is empty or has multiple columns, returns the array unchanged.
func (c *Compiler) unwrapSingleElementArray(arrSym *Symbol) *Symbol {
	arrType := arrSym.Type.(Array)

	// Safety: check array is non-empty (has at least one element)
	// Note: For dynamically sized arrays, Length may be 0 even if non-empty
	// We rely on the type system to ensure this is only called for non-empty arrays
	elemType := arrType.ColTypes[0]

	// Extract array[0]
	zero := c.ConstI64(0)
	elemVal := c.ArrayGet(arrSym, elemType, zero)

	return &Symbol{Val: elemVal, Type: elemType}
}

func (c *Compiler) compileSimpleStatement(stmt *ast.LetStatement) {
	syms := []*Symbol{}
	rhsNames := []string{} // Track RHS variable names (or "" if not a variable)
	i := 0
	for _, expr := range stmt.Value {
		res := c.compileExpression(expr, stmt.Name[i:], true)

		// For each result symbol, record the source variable name if it's an identifier
		var rhsName string
		if ident, ok := expr.(*ast.Identifier); ok {
			rhsName = ident.Value
		}
		for range res {
			rhsNames = append(rhsNames, rhsName)
		}

		syms = append(syms, res...)
		i += len(res)
	}
	c.writeTo(stmt.Name, syms, rhsNames)
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
		s.Val = c.ConstI64(uint64(e.Value))
		res = []*Symbol{s}
	case *ast.FloatLiteral:
		s.Type = Float{Width: 64}
		s.Val = c.ConstF64(e.Value)
		res = []*Symbol{s}
	case *ast.StringLiteral:
		// Process markers eagerly at string creation time
		formatted, args, toFree := c.formatString(e)

		// No markers, create a regular string literal (static storage)
		if len(args) == 0 {
			globalName := fmt.Sprintf("str_literal_%d", c.formatCounter)
			c.formatCounter++
			s.Type = Str{Static: true} // Static string literal in .rodata
			s.Val = c.createGlobalString(globalName, e.Value, llvm.PrivateLinkage)
			res = []*Symbol{s}
			return
		}

		// Build formatted string with sprintf_alloc (heap-allocated)
		formatPtr := c.createFormatStringGlobal(formatted)
		sprintfAllocArgs := append([]llvm.Value{formatPtr}, args...)
		fnType, fn := c.GetCFunc(SPRINTF_ALLOC)
		resultPtr := c.builder.CreateCall(fnType, fn, sprintfAllocArgs, "str_result")
		c.free(toFree)

		s.Type = Str{Static: false} // Heap-allocated formatted string
		s.Val = resultPtr
		res = []*Symbol{s}
	case *ast.RangeLiteral:
		if isRoot {
			res = c.compileRangeExpression(e)
			return
		}
		// Non-root range literal must be handled by the enclosing operator.
		panic("internal: unexpanded range literal in non-root position")
	case *ast.ArrayLiteral:
		return c.compileArrayExpression(e, dest, isRoot)
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
	case ArrayRange:
		// ArrayRange is a struct of { i8*, Range }, so align to the largest member, which is i8*
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

func (c *Compiler) ToRange(e *ast.RangeLiteral, typ Type) llvm.Value {
	start := c.compileExpression(e.Start, nil, false)[0].Val
	stop := c.compileExpression(e.Stop, nil, false)[0].Val
	var stepVal llvm.Value
	if e.Step != nil {
		stepVal = c.compileExpression(e.Step, nil, false)[0].Val
	} else {
		// default step = 1
		stepVal = c.ConstI64(1)
	}

	return c.CreateRange(start, stop, stepVal, typ)
}

func (c *Compiler) compileRangeExpression(e *ast.RangeLiteral) (res []*Symbol) {
	s := &Symbol{}
	s.Type = Range{Iter: Int{Width: 64}}
	s.Val = c.ToRange(e, s.Type)
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

		// Handle Array-Scalar operations (for element-wise ops, not concatenation)
		if l.Type.Kind() == ArrayKind {
			// Array on left: array op scalar
			return c.compileArrayScalarInfix(op, l, r, expectedArr.ColTypes[0], true)
		}
		if r.Type.Kind() == ArrayKind {
			// Array on right: scalar op array
			return c.compileArrayScalarInfix(op, r, l, expectedArr.ColTypes[0], false)
		}
	}

	// Normalize types for operator lookup (ignore Static flag for strings)
	leftType := l.Type
	if _, ok := leftType.(Str); ok {
		leftType = Str{} // Normalize to plain Str{} for lookup
	}
	rightType := r.Type
	if _, ok := rightType.(Str); ok {
		rightType = Str{} // Normalize to plain Str{} for lookup
	}

	key := opKey{
		Operator:  op,
		LeftType:  leftType,
		RightType: rightType,
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

func (c *Compiler) ConstI64(v uint64) llvm.Value {
	return llvm.ConstInt(c.Context.Int64Type(), v, false)
}

func (c *Compiler) ConstF64(v float64) llvm.Value {
	return llvm.ConstFloat(c.Context.DoubleType(), v)
}

// rangeZeroToN builds a Range{start:0, stop:n, step:1} aggregate
func (c *Compiler) rangeZeroToN(n llvm.Value) llvm.Value {
	return c.CreateRange(c.ConstI64(0), n, c.ConstI64(1), Range{Iter: Int{Width: 64}})
}

func (c *Compiler) CreateRange(start, stop, step llvm.Value, typ Type) llvm.Value {
	rType := c.mapToLLVMType(typ)
	agg := llvm.Undef(rType)
	agg = c.builder.CreateInsertValue(agg, start, 0, "start")
	agg = c.builder.CreateInsertValue(agg, stop, 1, "stop")
	agg = c.builder.CreateInsertValue(agg, step, 2, "step")
	return agg
}

func (c *Compiler) CreateArrayRange(arrayVal llvm.Value, rangeVal llvm.Value, arrRange ArrayRange) llvm.Value {
	llvmTy := c.mapToLLVMType(arrRange)
	agg := llvm.Undef(llvmTy)
	agg = c.builder.CreateInsertValue(agg, arrayVal, 0, "array_range_arr")
	agg = c.builder.CreateInsertValue(agg, rangeVal, 1, "array_range_rng")
	return agg
}

func (c *Compiler) initRangeArrayAccumulators(outTypes []Type) []*ArrayAccumulator {
	accs := make([]*ArrayAccumulator, len(outTypes))
	for i, outType := range outTypes {
		arrType, ok := outType.(Array)
		if !ok {
			continue
		}
		accs[i] = c.NewArrayAccumulator(arrType)
	}
	return accs
}

// Modified compileInfixRanges - cleaner with destinations
func (c *Compiler) compileInfixRanges(expr *ast.InfixExpression, info *ExprInfo, dest []*ast.Identifier) (res []*Symbol) {
	// Push a new scope for this expression block
	PushScope(&c.Scopes, BlockScope)
	defer c.popScope()

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
				c.PushVal(acc, computed)
				continue
			}

			c.createStore(computed.Val, outputs[i].Val, computed.Type)
		}
	})

	out := make([]*Symbol, len(outputs))
	for i := range outputs {
		if acc := arrayAccs[i]; acc != nil && acc.Used {
			out[i] = c.ArrayAccResult(acc)
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
		sym, ok := Get(c.Scopes, name)
		if ok {
			val = sym.Val
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
	info := c.ExprCache[expr]
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
	defer c.popScope()

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

func (c *Compiler) compileFunc(fn *ast.FuncStatement, args []*Symbol, mangled string, f *Func, retStruct llvm.Type, funcType llvm.Type) llvm.Value {
	function := llvm.AddFunction(c.Module, mangled, funcType)

	sretAttr := c.Context.CreateTypeAttribute(llvm.AttributeKindID("sret"), retStruct)
	function.AddAttributeAtIndex(1, sretAttr) // Index 1 is the first parameter
	function.AddAttributeAtIndex(1, c.Context.CreateEnumAttribute(llvm.AttributeKindID("noalias"), 0))

	// Create entry block
	entry := c.Context.AddBasicBlock(function, "entry")
	savedBlock := c.builder.GetInsertBlock()
	c.builder.SetInsertPointAtEnd(entry)

	c.compileFuncBlock(fn, f, args, retStruct, function)

	c.builder.CreateRetVoid()

	// Restore the builder to where it was before compiling this function
	if !savedBlock.IsNil() {
		c.builder.SetInsertPointAtEnd(savedBlock)
	}

	return function
}

func (c *Compiler) getLoopOutTypes(finalOutTypes []Type) []Type {
	loopOutTypes := make([]Type, len(finalOutTypes))
	for i, outType := range finalOutTypes {
		if arr, ok := outType.(Array); ok {
			loopOutTypes[i] = arr.ColTypes[0]
			continue
		}
		loopOutTypes[i] = outType
	}
	return loopOutTypes
}

func (c *Compiler) createRetPtrs(fn *ast.FuncStatement, retStruct llvm.Type, sretPtr llvm.Value, finalOutTypes []Type) []*Symbol {
	retPtrs := make([]*Symbol, len(fn.Outputs))
	for i, outIdent := range fn.Outputs {
		fieldPtr := c.builder.CreateStructGEP(retStruct, sretPtr, i, outIdent.Value+"_ptr")
		retPtrs[i] = &Symbol{
			Val:      fieldPtr,
			Type:     Ptr{Elem: finalOutTypes[i]},
			FuncArg:  true,
			ReadOnly: false,
		}
	}
	return retPtrs
}

func (c *Compiler) processParams(fn *ast.FuncStatement, args []*Symbol, function llvm.Value) []int {
	iterIndices := []int{}
	for i, arg := range args {
		name := fn.Parameters[i].Value
		kind := arg.Type.Kind()
		if kind == RangeKind || kind == ArrayKind || kind == ArrayRangeKind {
			iterIndices = append(iterIndices, i)
			continue
		}
		paramVal := function.Param(i + 1)
		Put(c.Scopes, name, &Symbol{
			Val:      paramVal,
			Type:     arg.Type,
			FuncArg:  true,
			ReadOnly: true,
		})
	}
	return iterIndices
}

func (c *Compiler) compileFuncNonIter(fn *ast.FuncStatement, retPtrs []*Symbol, finalOutTypes []Type) {
	// For each output, initialize retPtr with param value (if exists) then rebind to retPtr
	for i, outIdent := range fn.Outputs {
		if seed, ok := Get(c.Scopes, outIdent.Value); ok {
			c.createStore(seed.Val, retPtrs[i].Val, finalOutTypes[i])
		}
		Put(c.Scopes, outIdent.Value, retPtrs[i]) // Overwrites param binding
	}
	c.compileBlockWithArgs(fn, map[string]*Symbol{}, map[string]*Symbol{})
}

func (c *Compiler) compileFuncIter(fn *ast.FuncStatement, args []*Symbol, iterIndices []int, retPtrs []*Symbol, loopOutTypes []Type, finalOutTypes []Type, function llvm.Value) {
	// For iteration: setupRangeOutputs needs params in scope to initialize from matching names
	outputs := c.setupRangeOutputs(fn.Outputs, loopOutTypes)
	arrayAccs := c.initRangeArrayAccumulators(finalOutTypes)

	fa := &funcArgs{
		args:        args,
		outputs:     outputs,
		iterIndices: iterIndices,
		iters:       make(map[string]*Symbol),
		arrayAccs:   arrayAccs,
	}
	c.funcLoopNest(fn, fa, function, 0)

	for i := range retPtrs {
		if acc := arrayAccs[i]; acc != nil {
			arrSym := c.ArrayAccResult(acc)
			c.createStore(arrSym.Val, retPtrs[i].Val, arrSym.Type)
			continue
		}
		elemType := loopOutTypes[i]
		finalVal := c.createLoad(outputs[i].Val, elemType, fn.Outputs[i].Value+"_final")
		c.createStore(finalVal, retPtrs[i].Val, elemType)
	}
}

func (c *Compiler) compileFuncBlock(fn *ast.FuncStatement, f *Func, args []*Symbol, retStruct llvm.Type, function llvm.Value) {
	PushScope(&c.Scopes, FuncScope)
	defer c.popScope()

	sretPtr := function.Param(0)
	finalOutTypes := f.OutTypes
	loopOutTypes := c.getLoopOutTypes(finalOutTypes)
	retPtrs := c.createRetPtrs(fn, retStruct, sretPtr, finalOutTypes)
	iterIndices := c.processParams(fn, args, function)

	if len(iterIndices) == 0 {
		c.compileFuncNonIter(fn, retPtrs, finalOutTypes)
		return
	}

	c.compileFuncIter(fn, args, iterIndices, retPtrs, loopOutTypes, finalOutTypes, function)
}

func (c *Compiler) iterOverRange(rangeType Range, rangeVal llvm.Value, body func(llvm.Value, Type)) {
	iterType := rangeType.Iter
	c.createLoop(rangeVal, func(iter llvm.Value) {
		body(iter, iterType)
	})
}

func (c *Compiler) iterOverArray(arrSym *Symbol, body func(llvm.Value, Type)) {
	arrType := arrSym.Type.(Array)
	elemType := arrType.ColTypes[0]
	lenVal := c.ArrayLen(arrSym, elemType)
	r := c.rangeZeroToN(lenVal)
	c.createLoop(r, func(idx llvm.Value) {
		elemVal := c.ArrayGet(arrSym, elemType, idx)
		body(elemVal, elemType)
	})
}

func (c *Compiler) iterOverArrayRange(arrRangeSym *Symbol, body func(llvm.Value, Type)) {
	arrRangeType := arrRangeSym.Type.(ArrayRange)
	arrPtr := c.builder.CreateExtractValue(arrRangeSym.Val, 0, "array_range_ptr")
	rangeVal := c.builder.CreateExtractValue(arrRangeSym.Val, 1, "array_range_bounds")
	arraySym := &Symbol{Val: arrPtr, Type: arrRangeType.Array}
	elemType := arrRangeType.Array.ColTypes[0]
	c.createLoop(rangeVal, func(iter llvm.Value) {
		elemVal := c.ArrayGet(arraySym, elemType, iter)
		body(elemVal, elemType)
	})
}

func (c *Compiler) funcLoopNest(fn *ast.FuncStatement, fa *funcArgs, function llvm.Value, level int) {
	if level == len(fa.iterIndices) {
		c.compileBlockWithArgs(fn, map[string]*Symbol{}, fa.iters)
		for i, acc := range fa.arrayAccs {
			if acc == nil {
				continue
			}
			val := c.createLoad(fa.outputs[i].Val, acc.ElemType, fn.Outputs[i].Value+"_iter")
			c.PushVal(acc, &Symbol{Val: val, Type: acc.ElemType})
		}
		return
	}

	paramIdx := fa.iterIndices[level]
	arg := fa.args[paramIdx]
	name := fn.Parameters[paramIdx].Value

	next := func(iterVal llvm.Value, iterType Type) {
		fa.iters[name] = &Symbol{
			Val:      iterVal,
			Type:     iterType,
			FuncArg:  true,
			ReadOnly: false,
		}
		c.funcLoopNest(fn, fa, function, level+1)
	}

	switch arg.Type.Kind() {
	case RangeKind:
		rangeType := arg.Type.(Range)
		rangeVal := function.Param(paramIdx + 1)
		c.iterOverRange(rangeType, rangeVal, next)
	case ArrayKind:
		arrSym := &Symbol{
			Val:      function.Param(paramIdx + 1),
			Type:     arg.Type,
			FuncArg:  true,
			ReadOnly: true,
		}
		c.iterOverArray(arrSym, next)
	case ArrayRangeKind:
		arrRangeSym := &Symbol{
			Val:      function.Param(paramIdx + 1),
			Type:     arg.Type,
			FuncArg:  true,
			ReadOnly: true,
		}
		c.iterOverArrayRange(arrRangeSym, next)
	default:
		panic("unsupported iterator kind in funcLoopNest")
	}
	delete(fa.iters, name)
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

func (c *Compiler) compileArrayRangeArg(expr *ast.ArrayRangeExpression) *Symbol {
	info := c.ExprCache[expr]
	arrRange := info.OutTypes[0].(ArrayRange)

	arraySyms := c.compileExpression(expr.Array, nil, true)
	arraySym := arraySyms[0]

	rangeSyms := c.compileExpression(expr.Range, nil, true)
	rangeSym := rangeSyms[0]

	return &Symbol{
		Val:  c.CreateArrayRange(arraySym.Val, rangeSym.Val, arrRange),
		Type: arrRange,
	}
}

func (c *Compiler) compileCallExpression(ce *ast.CallExpression) (res []*Symbol) {
	args, _ := c.compileArgs(ce)

	paramTypes := make([]Type, len(args))
	for i, arg := range args {
		paramTypes[i] = arg.Type
	}

	mangled := mangle(ce.Function.Value, paramTypes)
	fnInfo := c.FuncCache[mangled]
	retStruct := c.getReturnStruct(mangled, fnInfo.OutTypes)
	sretPtr := c.createEntryBlockAlloca(retStruct, "sret_tmp")

	llvmInputs := make([]llvm.Type, len(paramTypes))
	for i, pt := range paramTypes {
		llvmInputs[i] = c.mapToLLVMType(pt)
	}
	funcType := c.getFuncType(retStruct, llvmInputs)
	fn := c.Module.NamedFunction(mangled)
	if fn.IsNil() {
		fk := ast.FuncKey{
			FuncName: ce.Function.Value,
			Arity:    len(paramTypes),
		}
		template := c.CodeCompiler.Code.Func.Map[fk]
		savedBlock := c.builder.GetInsertBlock()
		fn = c.compileFunc(template, args, mangled, fnInfo, retStruct, funcType)
		c.builder.SetInsertPointAtEnd(savedBlock)
	}

	return c.callFunctionDirect(fn, funcType, retStruct, fnInfo.OutTypes, args, sretPtr)
}

func (c *Compiler) callFunctionDirect(fn llvm.Value, funcType llvm.Type, retStruct llvm.Type, outTypes []Type, args []*Symbol, sretPtr llvm.Value) []*Symbol {
	llvmArgs := []llvm.Value{sretPtr}
	for _, arg := range args {
		llvmArgs = append(llvmArgs, arg.Val)
	}

	c.builder.CreateCall(funcType, fn, llvmArgs, "")

	res := make([]*Symbol, len(outTypes))
	for i, typ := range outTypes {
		ptr := c.builder.CreateStructGEP(retStruct, sretPtr, i, fmt.Sprintf("ret_gep_%d", i))
		res[i] = &Symbol{
			Val:  c.createLoad(ptr, typ, fmt.Sprintf("ret_val_%d", i)),
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

// copyString creates a deep copy of a string using strdup
func (c *Compiler) copyString(str llvm.Value) llvm.Value {
	fnType, fn := c.GetCFunc(STRDUP)
	return c.builder.CreateCall(fnType, fn, []llvm.Value{str}, "str_copy")
}

// copyArray creates a deep copy of an array
func (c *Compiler) copyArray(arr llvm.Value, elemType Type) llvm.Value {
	var fnType llvm.Type
	var fn llvm.Value
	switch elemType.Kind() {
	case IntKind:
		fnType, fn = c.GetCFunc(ARR_I64_COPY)
	case FloatKind:
		fnType, fn = c.GetCFunc(ARR_F64_COPY)
	case StrKind:
		fnType, fn = c.GetCFunc(ARR_STR_COPY)
	default:
		panic(fmt.Sprintf("unsupported array element type for copying: %s", elemType.String()))
	}
	return c.builder.CreateCall(fnType, fn, []llvm.Value{arr}, "arr_copy")
}

// deepCopyIfNeeded creates a deep copy if the symbol is a string or array
// This ensures value semantics for assignments
func (c *Compiler) deepCopyIfNeeded(sym *Symbol) *Symbol {
	switch sym.Type.Kind() {
	case StrKind:
		// Deep copy the string - result is always heap-allocated
		copiedStr := c.copyString(sym.Val)
		return &Symbol{
			Val:      copiedStr,
			Type:     Str{Static: false}, // Copied strings are always heap-allocated
			FuncArg:  false,
			ReadOnly: false,
		}
	case ArrayKind:
		// Deep copy the array
		arrayType := sym.Type.(Array)
		if len(arrayType.ColTypes) > 0 {
			// Skip copying if the element type is unresolved (will be resolved later)
			if arrayType.ColTypes[0].Kind() == UnresolvedKind {
				return sym
			}
			copiedArr := c.copyArray(sym.Val, arrayType.ColTypes[0])
			return &Symbol{
				Val:      copiedArr,
				Type:     sym.Type, // Arrays don't have Static flag
				FuncArg:  false,
				ReadOnly: false,
			}
		}
	}
	// For other types (int, float, range), just return as-is (they're value types)
	return sym
}

func (c *Compiler) freeArray(arr llvm.Value, elemType Type) {
	var fnType llvm.Type
	var fn llvm.Value
	switch elemType.Kind() {
	case IntKind:
		fnType, fn = c.GetCFunc(ARR_I64_FREE)
	case FloatKind:
		fnType, fn = c.GetCFunc(ARR_F64_FREE)
	case StrKind:
		fnType, fn = c.GetCFunc(ARR_STR_FREE)
	default:
		panic(fmt.Sprintf("unsupported array element type for cleanup: %s", elemType.String()))
	}
	c.builder.CreateCall(fnType, fn, []llvm.Value{arr}, "")
}

// cleanupScope generates cleanup code for all heap-allocated variables in the current scope
// This should be called before PopScope to free memory for strings and arrays
func (c *Compiler) cleanupScope() {
	if len(c.Scopes) == 0 {
		return
	}
	currentScope := c.Scopes[len(c.Scopes)-1]
	for _, sym := range currentScope.Elems {
		// Skip function arguments - they're borrowed references, not owned
		if sym.FuncArg {
			continue
		}

		// Check if this symbol needs cleanup based on type
		switch t := sym.Type.(type) {
		case Str:
			// Only free heap-allocated strings, not static literals
			if !t.Static {
				c.free([]llvm.Value{sym.Val})
			}
		case Array:
			// Arrays are always heap-allocated (unless unresolved null pointer)
			if !sym.Val.IsConstant() || !sym.Val.IsNull() {
				c.freeArray(sym.Val, t.ColTypes[0])
			}
		}
	}
}

// popScope is the compiler-specific scope pop that includes cleanup
// Use this instead of PopScope(&c.Scopes) to ensure memory is freed
func (c *Compiler) popScope() {
	c.cleanupScope()
	PopScope(&c.Scopes)
}

func (c *Compiler) printf(args []llvm.Value) {
	fnType, fn := c.GetCFunc(PRINTF)
	c.builder.CreateCall(fnType, fn, args, PRINTF)
}

func (c *Compiler) compilePrintStatement(ps *ast.PrintStatement) {
	var formatStr string
	var args []llvm.Value
	var toFree []llvm.Value

	// Process each expression and build format string + args
	for _, expr := range ps.Expression {
		c.appendPrintExpression(expr, &formatStr, &args, &toFree)
	}

	// Create format string global and call printf
	formatPtr := c.createPrintFormatGlobal(formatStr)
	allArgs := append([]llvm.Value{formatPtr}, args...)
	c.printf(allArgs)
	c.free(toFree)
}

// appendPrintExpression handles one print expression (string literal or compiled expression)
func (c *Compiler) appendPrintExpression(expr ast.Expression, formatStr *string, args *[]llvm.Value, toFree *[]llvm.Value) {
	// String literals with markers are processed specially
	if strLit, ok := expr.(*ast.StringLiteral); ok {
		processed, newArgs, toFreeArgs := c.formatString(strLit)
		*formatStr += processed + " "
		*args = append(*args, newArgs...)
		*toFree = append(*toFree, toFreeArgs...)
		return
	}

	// Compile expression and process each resulting symbol
	syms := c.compileExpression(expr, nil, true)
	for _, s := range syms {
		c.appendPrintSymbol(s, expr, formatStr, args, toFree)
	}
}

// appendPrintSymbol handles printing one symbol based on its type
func (c *Compiler) appendPrintSymbol(s *Symbol, expr ast.Expression, formatStr *string, args *[]llvm.Value, toFree *[]llvm.Value) {
	// ArrayRange needs special handling (two string args)
	if s.Type.Kind() == ArrayRangeKind {
		arrStr, rngStr := c.arrayRangeStrArgs(s)
		*formatStr += "%s[%s] "
		*args = append(*args, arrStr, rngStr)
		*toFree = append(*toFree, arrStr, rngStr)
		return
	}

	// Get format specifier for this type
	spec, err := defaultSpecifier(s.Type)
	if err != nil {
		c.Errors = append(c.Errors, &token.CompileError{
			Token: expr.Tok(),
			Msg:   err.Error(),
		})
		return
	}

	*formatStr += spec + " "

	// Handle types that need string conversion
	switch s.Type.Kind() {
	case RangeKind:
		strPtr := c.rangeStrArg(s)
		*args = append(*args, strPtr)
		*toFree = append(*toFree, strPtr)
	case FloatKind:
		strPtr := c.floatStrArg(s)
		*args = append(*args, strPtr)
		*toFree = append(*toFree, strPtr)
	case ArrayKind:
		arrType := s.Type.(Array)
		if len(arrType.ColTypes) == 0 || arrType.ColTypes[0].Kind() == UnresolvedKind {
			*args = append(*args, c.constCString("[]"))
			return
		}
		strPtr := c.arrayStrArg(s)
		*args = append(*args, strPtr)
		*toFree = append(*toFree, strPtr)
	default:
		*args = append(*args, s.Val)
	}
}

// createPrintFormatGlobal creates a global constant for the printf format string
func (c *Compiler) createPrintFormatGlobal(formatStr string) llvm.Value {
	// Add newline and create string constant
	formatStr = strings.TrimSuffix(formatStr, " ") + "\n"
	formatConst := llvm.ConstString(formatStr, true)

	// Create unique global variable
	globalName := fmt.Sprintf("printf_fmt_%d", c.formatCounter)
	c.formatCounter++

	arrayLength := len(formatStr) + 1
	arrayType := llvm.ArrayType(c.Context.Int8Type(), arrayLength)
	formatGlobal := llvm.AddGlobal(c.Module, arrayType, globalName)
	formatGlobal.SetInitializer(formatConst)
	formatGlobal.SetGlobalConstant(true)

	// Return pointer to the format string
	zero := c.ConstI64(0)
	return c.builder.CreateGEP(arrayType, formatGlobal, []llvm.Value{zero, zero}, "fmt_ptr")
}

// Helper function to generate final output
func (c *Compiler) GenerateIR() string {
	return c.Module.String()
}
