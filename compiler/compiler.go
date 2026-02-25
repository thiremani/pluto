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
	FuncArg  bool // Symbol originates from function input/output argument context.
	Borrowed bool // Value/storage is borrowed from another owner (scope cleanup must skip).
	ReadOnly bool // Input parameter (cannot be written to).
}

// Borrowed-value ownership model:
//
// Function parameters are passed by reference (Ptr) to caller-owned storage.
// The caller retains ownership; the callee gets read/write access to slots.
//
//   - Input params (ReadOnly=true): read-only reference to caller's value
//   - Output params (ReadOnly=false): write reference to caller's storage
//
// Assignment semantics: when assigning a borrowed symbol to a local variable, the value
// is COPIED, just like `x = s` copies in regular scope. This ensures:
//   - No aliasing between caller's input and output variables
//   - Local variables get independent copies (with Borrowed=false)
//   - Consistent semantics: x = identity(s) behaves like x = s
//
// Memory management:
//
//   For function calls (x = f(y)):
//     - Function produces a new value and writes it to output slot
//     - This value is MOVED to the destination (ownership transferred)
//     - Old value in destination is NOT freed (see freeOldValues)
//     - Temps passed as inputs are freed by caller after call returns
//     - Function cleanup skips borrowed params/slots (caller owns them)
//
//   For other expressions (x = y + z):
//     - Old value in destination IS freed after store completes
//     - New value ownership transfers to destination
//
//   Scope cleanup:
//     - Final values are freed when scope ends (normal cleanup)
//     - Borrowed symbols are skipped (owner is outside current scope)
//
// Example: x = f(arr[0])
//   1. Caller: arr[0] returns owned copy, stored in temp alloca
//   2. Caller: passes Ptr to temp (input) and Ptr to x (output)
//   3. Function: computes result and writes to output slot
//   4. Function: cleanup skips borrowed params (caller owns slots)
//   5. Caller: frees temp; x now owns the function's result
//
// Flags:
//   - FuncArg tracks argument provenance (input/output params and iterator-derived values).
//   - Borrowed tracks lifetime/ownership (cleanup must skip when true).

type FuncArgs struct {
	Inputs      []*Symbol          // function.Param pointers for all params
	Outputs     []*Symbol          // retPtrs (pointers to sret slots)
	IterIndices []int              // Indices of iterator params
	Iters       map[string]*Symbol // Current iterator values during loop
}

func GetCopy(s *Symbol) (newSym *Symbol) {
	newSym = &Symbol{}
	newSym.Val = s.Val
	newSym.Type = s.Type
	newSym.FuncArg = s.FuncArg
	newSym.Borrowed = s.Borrowed
	newSym.ReadOnly = s.ReadOnly
	return newSym
}

type Compiler struct {
	Scopes          []Scope[*Symbol]
	Context         llvm.Context
	Module          llvm.Module
	builder         llvm.Builder
	formatCounter   int           // Track unique format strings
	tmpCounter      int           // Temporary variable names counter
	MangledPath     string        // pre-computed "Pt_[ModPath]_p_[RelPath]" or "Pt_[ModPath]_p"
	CodeCompiler    *CodeCompiler // Optional reference for script compilation
	FuncCache       map[string]*Func
	ExprCache       map[ExprKey]*ExprInfo
	FuncNameMangled string // current function's mangled name ("" for script level)
	Errors          []*token.CompileError
	stmtCtxStack    []stmtCtx
}

type stmtCtx struct {
	condStack       []map[ExprKey][]*Symbol // Cond-expr frames (one map per compileCondExprValue invocation)
	boundsStack     []boundsGuardFrame      // Nested bounds guards active within this statement
	loopBoundsStack []loopBoundsFrame       // Loop bounds mode stack active within this statement
}

func NewCompiler(ctx llvm.Context, mangledPath string, cc *CodeCompiler) *Compiler {
	module := ctx.NewModule(mangledPath)
	builder := ctx.NewBuilder()

	return &Compiler{
		Scopes:          []Scope[*Symbol]{NewScope[*Symbol](FuncScope)},
		Context:         ctx,
		Module:          module,
		builder:         builder,
		formatCounter:   0,
		tmpCounter:      0,
		MangledPath:     mangledPath,
		CodeCompiler:    cc,
		FuncCache:       make(map[string]*Func),
		ExprCache:       make(map[ExprKey]*ExprInfo),
		FuncNameMangled: "",
		Errors:          []*token.CompileError{},
		stmtCtxStack:    []stmtCtx{},
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

		// Mangle constant name for C ABI compliance
		mangledName := MangleConst(c.MangledPath, name)

		switch v := valueExpr.(type) {
		case *ast.IntegerLiteral:
			val = c.ConstI64(uint64(v.Value))
			sym.Type = Ptr{Elem: Int{Width: 64}}
			sym.Val = c.makeGlobalConst(c.Context.Int64Type(), mangledName, val, linkage)

		case *ast.FloatLiteral:
			val = c.ConstF64(v.Value)
			sym.Type = Ptr{Elem: Float{Width: 64}}
			sym.Val = c.makeGlobalConst(c.Context.DoubleType(), mangledName, val, linkage)

		case *ast.StringLiteral:
			sym.Val = c.createGlobalString(mangledName, v.Value, linkage)
			sym.Type = StrG{} // Global constants are static strings

		case *ast.HeapStringLiteral:
			c.Errors = append(c.Errors, &token.CompileError{
				Token: v.Token,
				Msg:   "heap string literals cannot be used as global constants",
			})
			return

		default:
			panic(fmt.Sprintf("unsupported constant type: %T", v))
		}
		Put(c.Scopes, name, sym)
	}
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
		// For StrH, allocate a heap empty string so callee can free it.
		s.Val = c.createGlobalString("zero_str", "", llvm.PrivateLinkage)
		if IsStrH(symType) {
			s.Val = c.copyString(s.Val)
		}
	case ArrayKind:
		// Zero value for arrays is null pointer (similar to static empty string for Str).
		// Runtime functions handle null gracefully: free(null) is no-op, len(null) returns 0.
		// This avoids heap allocation for zero values that may be immediately overwritten.
		s.Val = llvm.ConstPointerNull(llvm.PointerType(c.Context.Int8Type(), 0))
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

// writeTo stores compiled RHS symbols into destination identifiers.
// It delegates per-destination ownership and in-place pointer-slot updates to storeValue.
func (c *Compiler) writeTo(idents []*ast.Identifier, syms []*Symbol, needsCopy []bool) {
	for i, ident := range idents {
		c.storeValue(ident.Value, syms[i], needsCopy[i])
	}
}

// computeCopyRequirements determines whether each RHS value needs copying or can transfer ownership.
// It also returns movedSources - the set of RHS variable names whose ownership was transferred.
func (c *Compiler) computeCopyRequirements(idents []*ast.Identifier, syms []*Symbol, rhsNames []string) ([]bool, map[string]struct{}) {
	needsCopy := make([]bool, len(syms))
	movedSources := make(map[string]struct{})

	for i, rhsSym := range syms {
		// StrG (static strings): immutable, live forever - no copy needed.
		if IsStrG(rhsSym.Type) {
			continue
		}

		// Temporaries (array literals, function results, expressions): transfer ownership directly.
		// No copy needed - the temporary's memory becomes owned by the LHS variable.
		if rhsNames[i] == "" {
			continue
		}

		// Named variable on RHS - default to copying for safety
		needsCopy[i] = true

		// Check if RHS variable is being overwritten in LHS (enables ownership transfer).
		// Only allow transfer if this source hasn't already been moved.
		// This prevents double-free in cases like: a, b = a, a
		// where the second use of 'a' must copy, not transfer.
		if _, moved := movedSources[rhsNames[i]]; moved {
			continue
		}

		for _, lhsIdent := range idents {
			if lhsIdent.Value != rhsNames[i] {
				continue
			}
			needsCopy[i] = false
			movedSources[rhsNames[i]] = struct{}{}
			break
		}
	}
	return needsCopy, movedSources
}

// storeValue writes one RHS value into a named destination.
// If destination already has pointer storage, update that slot in place.
// Otherwise, bind/replace the scope symbol directly.
func (c *Compiler) storeValue(name string, rhsSym *Symbol, shouldCopy bool) {
	valueToStore := rhsSym
	if shouldCopy {
		valueToStore = c.deepCopyIfNeeded(rhsSym)
	}

	oldSym, exists := Get(c.Scopes, name)
	if !exists || oldSym.Type.Kind() != PtrKind {
		Put(c.Scopes, name, valueToStore)
		return
	}

	derefed := c.derefIfPointer(valueToStore, name+"_rhs_load")
	c.createStore(derefed.Val, oldSym.Val, derefed.Type)
}

// freeValue frees heap-allocated memory for the given value based on its type.
func (c *Compiler) freeValue(val llvm.Value, typ Type) {
	switch t := typ.(type) {
	case StrH:
		c.free([]llvm.Value{val})
	case StrG:
		// Static strings live forever, no free needed
	case Array:
		if len(t.ColTypes) > 0 && t.ColTypes[0].Kind() != UnresolvedKind {
			c.freeArray(val, t.ColTypes[0])
		}
	case ArrayRange:
		// Release the backing array payload. Borrowed views are skipped by callers.
		arrVal := c.builder.CreateExtractValue(val, 0, "arr_range_arr")
		c.freeValue(arrVal, t.Array)
	}
}

// freeSymbolValue frees a symbol's current value. If the symbol is Ptr-wrapped,
// loads the pointee first so the owned heap value is released.
func (c *Compiler) freeSymbolValue(sym *Symbol, loadName string) {
	if sym == nil {
		return
	}
	derefed := c.derefIfPointer(sym, loadName)
	c.freeValue(derefed.Val, derefed.Type)
}

// shouldSkipOldValueFree returns true when an expression delegates destination
// old-value cleanup to inner assignment logic, avoiding caller-side double-free.
//
// Cases:
//
//   - CallExpression:
//     The caller passes output pointers to the callee. The callee then applies
//     normal assignment cleanup when writing to those output params, so caller
//     freeOldValues must skip.
//
//   - InfixExpression/PrefixExpression/ArrayRangeExpression with pending ranges:
//     Range-lowered paths free previous output values per iteration inside the
//     loop body before storing the next value.
//
// All other expressions return false so freeOldValues handles cleanup with full
// assignment context (moved sources and borrowed/non-owning guards).
func (c *Compiler) shouldSkipOldValueFree(expr ast.Expression) bool {
	if _, isCall := expr.(*ast.CallExpression); isCall {
		return true
	}

	switch e := expr.(type) {
	case *ast.InfixExpression:
		info := c.ExprCache[key(c.FuncNameMangled, e)]
		return info != nil && len(c.pendingLoopRanges(info.Ranges)) > 0
	case *ast.PrefixExpression:
		info := c.ExprCache[key(c.FuncNameMangled, e)]
		return info != nil && len(c.pendingLoopRanges(info.Ranges)) > 0
	case *ast.ArrayRangeExpression:
		info := c.ExprCache[key(c.FuncNameMangled, e)]
		return info != nil && len(c.pendingLoopRanges(info.Ranges)) > 0
	default:
		return false
	}
}

// compileAssignments writes expression results into writeIdents while applying
// ownership/copy rules based on ownershipIdents.
//
// In simple assignments writeIdents == ownershipIdents. Conditional lowering can
// write through temporary output slots while still using destination identifiers
// for move/copy decisions.
//
// Invariant: if an ownership identifier is referenced on RHS (self-reference),
// that name must resolve to the corresponding write slot during RHS compilation.
// Conditional lowering guarantees this via compileCondAssignments.
func (c *Compiler) compileAssignments(writeIdents []*ast.Identifier, ownershipIdents []*ast.Identifier, exprs []ast.Expression) {
	// Capture old values BEFORE compiling RHS expressions.
	// This is critical for function calls with Ptr outputs: by the time RHS compilation
	// finishes, Ptrs already contain NEW values. We must capture old values first.
	oldValues := c.captureOldValues(writeIdents)

	// Collect bounds checks emitted while compiling RHS expressions. If any
	// check fails, skip this assignment and keep prior destination values.
	guardPtr := c.pushBoundsGuard("stmt_bounds_guard")
	defer c.popBoundsGuard()

	syms := []*Symbol{}
	rhsNames := []string{} // Track RHS variable names (or "" if not a variable)
	// Track result counts per expression to identify call destinations
	resCounts := []int{}
	i := 0
	for _, expr := range exprs {
		res := c.compileExpression(expr, writeIdents[i:])
		resCounts = append(resCounts, len(res))

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

	if !c.stmtBoundsUsed() {
		c.commitAssignments(writeIdents, ownershipIdents, syms, rhsNames, oldValues, exprs, resCounts)
		return
	}

	// Guarded assignments must converge through pointer-backed destinations so
	// runtime write/skip paths both feed subsequent reads correctly.
	c.promoteIdentifiersIfNeeded(writeIdents)
	c.withLoadedGuard(
		guardPtr,
		"stmt_bounds_ok",
		"stmt_bounds_write",
		"stmt_bounds_skip",
		"stmt_bounds_cont",
		func() {
			c.commitAssignments(writeIdents, ownershipIdents, syms, rhsNames, oldValues, exprs, resCounts)
		},
		func() {
			c.freeAssignmentTemps(exprs, syms, resCounts)
			c.restoreOldValues(writeIdents, oldValues)
		},
	)
}

func (c *Compiler) commitAssignments(
	writeIdents []*ast.Identifier,
	ownershipIdents []*ast.Identifier,
	syms []*Symbol,
	rhsNames []string,
	oldValues []*Symbol,
	exprs []ast.Expression,
	resCounts []int,
) {
	needsCopy, movedSources := c.computeCopyRequirements(ownershipIdents, syms, rhsNames)
	c.writeTo(writeIdents, syms, needsCopy)
	c.freeOldValues(ownershipIdents, oldValues, movedSources, exprs, resCounts)
}

func (c *Compiler) promoteExistingSym(name string) {
	if _, exists := Get(c.Scopes, name); !exists {
		return
	}
	c.promoteToMemory(name)
}

func (c *Compiler) promoteIdentifiersIfNeeded(idents []*ast.Identifier) {
	for _, ident := range idents {
		c.promoteExistingSym(ident.Value)
	}
}

// This returns a pointer into stmtCtxStack storage. Callers must not keep
// it across operations that can append to stmtCtxStack.
func (c *Compiler) currentStmtCtx() *stmtCtx {
	if len(c.stmtCtxStack) == 0 {
		return nil
	}
	return &c.stmtCtxStack[len(c.stmtCtxStack)-1]
}

func (c *Compiler) pushStmtCtx() {
	c.stmtCtxStack = append(c.stmtCtxStack, stmtCtx{})
}

func (c *Compiler) popStmtCtx() {
	c.stmtCtxStack = c.stmtCtxStack[:len(c.stmtCtxStack)-1]
}

func (c *Compiler) currentCondLHSFrame() map[ExprKey][]*Symbol {
	ctx := c.currentStmtCtx()
	if ctx == nil || len(ctx.condStack) == 0 {
		return nil
	}
	return ctx.condStack[len(ctx.condStack)-1]
}

func (c *Compiler) requireCondLHSFrame() map[ExprKey][]*Symbol {
	frame := c.currentCondLHSFrame()
	if frame == nil {
		panic("internal: missing condLHS frame (pushCondLHSFrame must be called before extraction)")
	}
	return frame
}

func (c *Compiler) pushCondLHSFrame() {
	ctx := c.currentStmtCtx()
	if ctx == nil {
		panic("internal: missing statement context for pushCondLHSFrame")
	}
	ctx.condStack = append(ctx.condStack, make(map[ExprKey][]*Symbol))
}

func (c *Compiler) popCondLHSFrame() {
	ctx := c.currentStmtCtx()
	if ctx == nil || len(ctx.condStack) == 0 {
		panic("internal: missing condLHS frame for popCondLHSFrame")
	}
	ctx.condStack = ctx.condStack[:len(ctx.condStack)-1]
}

// freeAssignmentTemps frees RHS temporaries when assignment writes are skipped.
func (c *Compiler) freeAssignmentTemps(exprs []ast.Expression, syms []*Symbol, resCounts []int) {
	offset := 0
	for exprIdx, expr := range exprs {
		count := resCounts[exprIdx]
		c.freeTemporary(expr, syms[offset:offset+count])
		offset += count
	}
}

// restoreOldValues writes captured destination values back after a skipped
// assignment path where RHS evaluation may have updated pointer-backed slots.
func (c *Compiler) restoreOldValues(writeIdents []*ast.Identifier, oldValues []*Symbol) {
	for i, ident := range writeIdents {
		oldVal := oldValues[i]
		if oldVal == nil {
			continue
		}
		sym, exists := Get(c.Scopes, ident.Value)
		if !exists {
			continue
		}
		if ptrType, ok := sym.Type.(Ptr); ok {
			c.createStore(oldVal.Val, sym.Val, ptrType.Elem)
			continue
		}
		// Fallback for non-pointer symbols (defensive; guarded assignment paths
		// normally promote existing destinations before branching).
		Put(c.Scopes, ident.Value, oldVal)
	}
}

// freeOldValues frees old values after stores complete.
// Skips: nil values (new variables), moved values, and expressions that
// manage old-value cleanup internally.
//
// Call results are skipped because function return values are moved (ownership
// transferred) to destination identifiers.
func (c *Compiler) freeOldValues(ownershipIdents []*ast.Identifier, oldValues []*Symbol, movedSources map[string]struct{}, exprs []ast.Expression, resCounts []int) {
	i := 0
	for exprIdx, expr := range exprs {
		// Skip expressions that handle destination old-value ownership themselves.
		if c.shouldSkipOldValueFree(expr) {
			i += resCounts[exprIdx]
			continue
		}
		for j := 0; j < resCounts[exprIdx]; j++ {
			idx := i + j
			if oldValues[idx] == nil {
				continue
			}
			if c.skipBorrowedOldValueFree(oldValues[idx]) {
				continue
			}
			if _, moved := movedSources[ownershipIdents[idx].Value]; moved {
				continue
			}
			c.freeSymbolValue(oldValues[idx], "old_assign")
		}
		i += resCounts[exprIdx]
	}
}

// skipBorrowedOldValueFree reports whether overwrite cleanup must skip freeing
// an old value because this scope does not own its storage.
func (c *Compiler) skipBorrowedOldValueFree(sym *Symbol) bool {
	if sym == nil || !sym.Borrowed {
		return false
	}

	// Read-only borrowed values are caller-owned inputs.
	if sym.ReadOnly {
		return true
	}

	// Borrowed array ranges are non-owning views into another array payload.
	return sym.Type.Kind() == ArrayRangeKind
}

// captureOldValues captures the current values of destination variables before RHS compilation.
// For Ptr variables, loads the actual value; for others, returns the symbol directly.
// Returns nil for variables that don't exist yet.
func (c *Compiler) captureOldValues(idents []*ast.Identifier) []*Symbol {
	result := make([]*Symbol, len(idents))
	for i, ident := range idents {
		sym, exists := Get(c.Scopes, ident.Value)
		if !exists {
			continue
		}
		// For Ptrs, load the actual value so we can free it later.
		// For non-Ptrs, use the symbol directly.
		result[i] = c.derefIfPointer(sym, ident.Value+"_old_load")
	}
	return result
}

func (c *Compiler) compileLetStatement(stmt *ast.LetStatement) {
	c.pushStmtCtx()
	defer c.popStmtCtx()

	cond, hasConditions := c.compileConditions(stmt)

	// Embedded conditional expressions (comparisons in value position)
	// take the most specialized path — they subsume statement conditions.
	for _, expr := range stmt.Value {
		if c.hasCondExprInTree(expr) {
			c.compileCondExprStatement(stmt, cond)
			return
		}
	}

	if hasConditions {
		c.compileCondStatement(stmt, cond)
		return
	}

	c.compileAssignments(stmt.Name, stmt.Name, stmt.Value)
}

func (c *Compiler) compileExpression(expr ast.Expression, dest []*ast.Identifier) (res []*Symbol) {
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
		res = []*Symbol{c.compileStringLiteral(e.Token, e.Value, false)}
	case *ast.HeapStringLiteral:
		res = []*Symbol{c.compileStringLiteral(e.Token, e.Value, true)}
	case *ast.RangeLiteral:
		res = c.compileRangeExpression(e)
		return
	case *ast.ArrayLiteral:
		return c.compileArrayExpression(e, dest)
	case *ast.ArrayRangeExpression:
		return c.compileArrayRangeExpression(e, dest)
	case *ast.Identifier:
		res = []*Symbol{c.compileIdentifier(e)}
	case *ast.InfixExpression:
		res = c.compileInfixExpression(e, dest)
	case *ast.PrefixExpression:
		res = c.compilePrefixExpression(e, dest)
	case *ast.CallExpression:
		res = c.compileCallExpression(e, dest)
	default:
		panic(fmt.Sprintf("unsupported expression type %T", e))
	}

	return
}

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
	case StrG, StrH:
		// Strings are i8* (char*)
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
		Val:      alloca,
		Type:     Ptr{Elem: s.Type},
		FuncArg:  s.FuncArg,
		Borrowed: s.Borrowed,
		ReadOnly: s.ReadOnly,
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
// representing the value loaded from that pointer with the given load name.
// Pass an empty string to use the default "_load" name.
// Otherwise, it returns the original symbol unmodified. It has NO side effects.
func (c *Compiler) derefIfPointer(s *Symbol, loadName string) *Symbol {
	var ptrType Ptr
	var ok bool
	if ptrType, ok = s.Type.(Ptr); !ok {
		return s
	}

	if loadName == "" {
		loadName = "_load"
	}

	loadedVal := c.createLoad(s.Val, ptrType.Elem, loadName)

	// Return a BRAND NEW symbol containing the result of the load.
	// Copy the symbol if we need other data like is it func arg, read only
	newS := GetCopy(s)
	newS.Val = loadedVal
	newS.Type = ptrType.Elem
	return newS
}

func (c *Compiler) ToRange(e *ast.RangeLiteral, typ Type) llvm.Value {
	start := c.compileExpression(e.Start, nil)[0].Val
	stop := c.compileExpression(e.Stop, nil)[0].Val
	var stepVal llvm.Value
	if e.Step != nil {
		stepVal = c.compileExpression(e.Step, nil)[0].Val
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

// compileStringLiteral compiles a string literal (regular or heap).
// If forceHeap is true, the string is always heap-allocated (for HeapStringLiteral).
// If forceHeap is false, only strings with format markers are heap-allocated.
func (c *Compiler) compileStringLiteral(tok token.Token, value string, forceHeap bool) *Symbol {
	formatted, args, toFree := c.formatString(tok, value)

	// No markers
	if len(args) == 0 {
		globalName := fmt.Sprintf("str_literal_%d", c.formatCounter)
		c.formatCounter++
		globalPtr := c.createGlobalString(globalName, value, llvm.PrivateLinkage)
		if forceHeap {
			// Heap string literal: strdup the static string
			return &Symbol{Type: StrH{}, Val: c.copyString(globalPtr)}
		}
		// Regular string literal: static storage
		return &Symbol{Type: StrG{}, Val: globalPtr}
	}

	// Has markers: build formatted string with sprintf_alloc (heap-allocated)
	formatPtr := c.createFormatStringGlobal(formatted)
	sprintfAllocArgs := append([]llvm.Value{formatPtr}, args...)
	fnType, fn := c.GetCFunc(SPRINTF_ALLOC)
	resultPtr := c.builder.CreateCall(fnType, fn, sprintfAllocArgs, "str_result")
	c.free(toFree)
	return &Symbol{Type: StrH{}, Val: resultPtr}
}

func (c *Compiler) compileIdentifier(ident *ast.Identifier) *Symbol {
	s, ok := Get(c.Scopes, ident.Value)
	if ok {
		return c.derefIfPointer(s, ident.Value+"_load")
	}

	cc := c.CodeCompiler.Compiler
	// no need to check ok as that is done in the typesolver
	s, _ = Get(cc.Scopes, ident.Value)
	return c.derefIfPointer(s, ident.Value+"_load")
}

// getRawSymbol looks up a symbol by name without dereferencing pointers.
// If it is a PtrKind, returns alloca and Type will be PtrKind.
func (c *Compiler) getRawSymbol(name string) (*Symbol, bool) {
	s, ok := Get(c.Scopes, name)
	if ok {
		return s, ok
	}
	s, ok = Get(c.CodeCompiler.Compiler.Scopes, name)
	return s, ok
}

func (c *Compiler) compileInfixExpression(expr *ast.InfixExpression, dest []*ast.Identifier) (res []*Symbol) {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]

	// Return pre-extracted LHS values for conditional expressions
	if condLHS := c.currentCondLHSFrame(); condLHS != nil {
		if lhs, ok := condLHS[key(c.FuncNameMangled, expr)]; ok {
			return lhs
		}
	}
	// Filter out ranges that are already bound (converted to scalar iterators in outer loops)
	pending := c.pendingLoopRanges(info.Ranges)
	if len(pending) == 0 {
		return c.compileInfixBasic(expr, info)
	}
	return c.compileInfixRanges(expr, info, dest)
}

// compileInfix compiles a single binary operation between two symbols.
// It handles pointer operands, array-scalar broadcasting, and delegates to
// the default operator table for scalar work.
func (c *Compiler) compileInfix(op string, left *Symbol, right *Symbol, expected Type) *Symbol {
	l := c.derefIfPointer(left, "")
	r := c.derefIfPointer(right, "")

	// Handle array operands early to avoid falling through to scalar op table
	if l.Type.Kind() == ArrayKind || r.Type.Kind() == ArrayKind {
		// Determine element type preference: use expected if it's an array, otherwise
		// use the available array operand's column type.
		var elem Type
		if expArr, ok := expected.(Array); ok && len(expArr.ColTypes) > 0 {
			elem = expArr.ColTypes[0]
		} else if l.Type.Kind() == ArrayKind {
			elem = l.Type.(Array).ColTypes[0]
		} else {
			elem = r.Type.(Array).ColTypes[0]
		}

		if l.Type.Kind() == ArrayKind && r.Type.Kind() == ArrayKind {
			return c.compileArrayArrayInfix(op, l, r, elem)
		}
		if l.Type.Kind() == ArrayKind {
			return c.compileArrayScalarInfix(op, l, r, elem, true)
		}
		return c.compileArrayScalarInfix(op, r, l, elem, false)
	}

	return defaultOps[opKey{
		Operator:  op,
		LeftType:  opType(l.Type.Key()),
		RightType: opType(r.Type.Key()),
	}](c, l, r, true)
}

// compareScalars derefs both operands and evaluates the comparison,
// returning the deref'd LHS and the i1 result.
func (c *Compiler) compareScalars(op string, left, right *Symbol) (*Symbol, llvm.Value) {
	lSym := c.derefIfPointer(left, "")
	rSym := c.derefIfPointer(right, "")
	result := defaultOps[opKey{
		Operator:  op,
		LeftType:  opType(lSym.Type.Key()),
		RightType: opType(rSym.Type.Key()),
	}](c, lSym, rSym, true)
	return lSym, result.Val
}

// canUseCondSelect reports whether cond-expr lowering can safely use a select
// without introducing heap allocations on the false arm.
func canUseCondSelect(t Type) bool {
	switch t.(type) {
	case Int, Float, StrG:
		return true
	default:
		return false
	}
}

// compileCondScalar lowers a scalar comparison in value position:
// returns LHS when comparison is true, otherwise zero value of LHS type.
func (c *Compiler) compileCondScalar(op string, left *Symbol, right *Symbol) *Symbol {
	lSym, cmpVal := c.compareScalars(op, left, right)

	if canUseCondSelect(lSym.Type) {
		zero := c.makeZeroValue(lSym.Type)
		val := c.builder.CreateSelect(cmpVal, lSym.Val, zero.Val, "cond_lhs")
		return &Symbol{Val: val, Type: lSym.Type}
	}

	// Heap-owning or composite types use explicit branching to avoid eager
	// false-arm materialization (select evaluates both operands).
	outPtr := c.createEntryBlockAlloca(c.mapToLLVMType(lSym.Type), "cond_lhs.mem")
	trueBlock, falseBlock, contBlock := c.createIfElseCont(cmpVal, "cond_lhs_true", "cond_lhs_false", "cond_lhs_cont")

	c.builder.SetInsertPointAtEnd(trueBlock)
	c.createStore(lSym.Val, outPtr, lSym.Type)
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(falseBlock)
	zero := c.makeZeroValue(lSym.Type)
	c.createStore(zero.Val, outPtr, zero.Type)
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
	val := c.createLoad(outPtr, lSym.Type, "cond_lhs")
	return &Symbol{Val: val, Type: lSym.Type}
}

func (c *Compiler) compileInfixBasic(expr *ast.InfixExpression, info *ExprInfo) (res []*Symbol) {
	// For infix operators, both operands are evaluated in non-root context
	// since they should not independently create loops
	left := c.compileExpression(expr.Left, nil)
	right := c.compileExpression(expr.Right, nil)

	for i := 0; i < len(left); i++ {
		mode := info.CompareModes[i]

		switch mode {
		case CondArray:
			res = append(res, c.compileArrayFilter(expr.Operator, left[i], right[i], info.OutTypes[i]))
		case CondScalar:
			// Usually pre-extracted via condLHS, but can still occur when range
			// comparisons are scalarized by an outer loop (e.g. call arg vectorization).
			res = append(res, c.compileCondScalar(expr.Operator, left[i], right[i]))
		default:
			res = append(res, c.compileInfix(expr.Operator, left[i], right[i], info.OutTypes[i]))
		}
	}

	// Free temporary array operands (literals used in expressions)
	// Variables are not freed here - they're managed by scope cleanup
	c.freeTemporary(expr.Left, left)
	c.freeTemporary(expr.Right, right)

	return res
}

// freeTemporary frees operands that are temporaries (not variables).
// If the expression is an identifier, it references a variable owned by scope - not a temporary.
// Everything else (literals, call results, infix/prefix results) produces a temporary
// that must be freed after use if it's a heap type (array or non-static string).
// Handles both direct values and pointer-wrapped values (loads value from pointer first).
func (c *Compiler) freeTemporary(expr ast.Expression, syms []*Symbol) {
	// Identifiers reference variables owned by scope - not temporaries
	if _, isIdent := expr.(*ast.Identifier); isIdent {
		return
	}

	// Everything else is a temporary - free heap types
	for _, sym := range syms {
		c.freeTemporarySymbol(sym, "temp_free")
	}
}

// freeTemporarySymbol frees one temporary symbol if it owns heap data.
func (c *Compiler) freeTemporarySymbol(sym *Symbol, loadName string) {
	if sym.Borrowed {
		return
	}
	derefed := c.derefIfPointer(sym, loadName)
	c.freeValue(derefed.Val, derefed.Type)
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

// Modified compileInfixRanges - cleaner with destinations
func (c *Compiler) compileInfixRanges(expr *ast.InfixExpression, info *ExprInfo, dest []*ast.Identifier) (res []*Symbol) {
	// Infix expressions never accumulate (Accumulates is always false for infix)
	// They loop and store the final value
	PushScope(&c.Scopes, BlockScope)
	defer c.popScope()

	// Setup outputs to store values across iterations.
	// Mark as borrowed so cleanupScope skips them - values are returned via out.
	outputs := c.makeOutputs(dest, info.OutTypes, true)

	leftRew := info.Rewrite.(*ast.InfixExpression).Left
	rightRew := info.Rewrite.(*ast.InfixExpression).Right
	_, leftIsIdent := leftRew.(*ast.Identifier)
	// CondScalar makes left-temp ownership branch-dependent (store on true,
	// drop on false), so handle left temp cleanup inline per slot.
	leftTempsHandledInline := info.HasCondScalar() && !leftIsIdent

	// Build nested loops, storing final value
	c.withLoopNestVersioned(info.Ranges, info.Rewrite.(*ast.InfixExpression), func() {
		c.pushBoundsGuard("infix_iter_bounds_guard")
		defer c.popBoundsGuard()

		left := c.compileExpression(leftRew, nil)
		right := c.compileExpression(rightRew, nil)

		for i := 0; i < len(left); i++ {
			c.compileRangeInfixSlot(
				expr.Operator,
				info.CompareModes[i],
				info.OutTypes[i],
				left[i],
				right[i],
				outputs[i],
				leftTempsHandledInline,
			)
		}

		c.cleanupRangeInfixTemps(leftRew, rightRew, left, right, leftTempsHandledInline)
	})

	// Load final values from outputs
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

func (c *Compiler) compileRangeInfixSlot(
	op string,
	mode CondMode,
	expected Type,
	leftSym *Symbol,
	rightSym *Symbol,
	output *Symbol,
	leftTempsHandledInline bool,
) {
	onSkip := func() {}
	if leftTempsHandledInline {
		onSkip = func() {
			c.freeTemporarySymbol(leftSym, "cond_lhs_drop")
		}
	}

	run := func() {
		if mode == CondScalar {
			c.storeRangeCondScalar(op, leftSym, rightSym, output, leftTempsHandledInline)
			return
		}

		computed := c.compileInfix(op, leftSym, rightSym, expected)
		// Free previous iteration's result before overwriting.
		c.freeSymbolValue(output, "old_output")
		c.createStore(computed.Val, output.Val, computed.Type)
		if leftTempsHandledInline {
			c.freeTemporarySymbol(leftSym, "temp_left")
		}
	}

	if !c.withStmtBoundsGuard(
		"infix_bounds_ok",
		"infix_bounds_run",
		"infix_bounds_skip",
		"infix_bounds_cont",
		run,
		onSkip,
	) {
		run()
	}
}

// storeRangeCondScalar updates output for a CondScalar slot inside range lowering.
// On true, store LHS. On false, keep previous output value.
func (c *Compiler) storeRangeCondScalar(op string, leftSym *Symbol, rightSym *Symbol, output *Symbol, leftTempsHandledInline bool) {
	// Range CondScalar is "keep previous output" on false, not "write zero".
	lSym, cmpVal := c.compareScalars(op, leftSym, rightSym)

	ifBlock, elseBlock, contBlock := c.createIfElseCont(cmpVal, "cond_store", "cond_drop_lhs", "cond_next")

	c.builder.SetInsertPointAtEnd(ifBlock)
	c.freeSymbolValue(output, "old_output")
	c.createStore(lSym.Val, output.Val, lSym.Type)
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(elseBlock)
	if leftTempsHandledInline {
		c.freeTemporarySymbol(leftSym, "cond_lhs_drop")
	}
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
}

func (c *Compiler) cleanupRangeInfixTemps(
	leftExpr ast.Expression,
	rightExpr ast.Expression,
	left []*Symbol,
	right []*Symbol,
	leftTempsHandledInline bool,
) {
	// Range-loop operands are temporary per iteration (except identifiers).
	// When left temps are handled inline, skip batch cleanup to avoid double-free.
	if !leftTempsHandledInline {
		c.freeTemporary(leftExpr, left)
	}
	c.freeTemporary(rightExpr, right)
}

func (c *Compiler) updateUnresolvedType(name string, sym *Symbol, resolved Type) {
	switch t := sym.Type.(type) {
	case Array:
		if t.ColTypes[0].Kind() == UnresolvedKind {
			sym.Type = resolved
			Put(c.Scopes, name, sym)
		}
	case ArrayRange:
		if t.Array.ColTypes[0].Kind() == UnresolvedKind {
			sym.Type = resolved
			Put(c.Scopes, name, sym)
		}
	case Ptr:
		if t.Elem.Kind() == UnresolvedKind {
			sym.Type = Ptr{Elem: resolved}
			Put(c.Scopes, name, sym)
		}
	default:
		// No action needed for other types
	}
}

func (c *Compiler) makeOutputs(dest []*ast.Identifier, outTypes []Type, borrowed bool) []*Symbol {
	outputs := make([]*Symbol, len(outTypes))

	for i, outType := range outTypes {
		// Determine the name for the alloca
		var name string
		if i < len(dest) {
			name = dest[i].Value
		} else {
			name = fmt.Sprintf("tmp_out_%d", c.tmpCounter)
			c.tmpCounter++
		}

		sym, exists := Get(c.Scopes, name)
		if exists {
			// Existing variable - update type if needed and promote to memory
			c.updateUnresolvedType(name, sym, outType)
			if sym.Type.Kind() == PtrKind {
				// Shadow existing pointer symbols in the current scope so temporary
				// ownership flags (e.g. Borrowed during range lowering) do not
				// mutate outer-scope symbols.
				sym = GetCopy(sym)
				Put(c.Scopes, name, sym)
			} else {
				sym = c.promoteToMemory(name)
			}
			// Preserve existing borrowed ownership and only add temporary borrowed semantics.
			// Example: function output params are already Borrowed=true (caller-owned slots).
			// A call path uses borrowed=false, and must not clear that existing ownership.
			sym.Borrowed = sym.Borrowed || borrowed
			outputs[i] = sym
			continue
		}

		// New variable or intermediate value - create temp alloca without adding to scope.
		// The permanent variable will be created by writeTo in FuncScope.
		ptr := c.createEntryBlockAlloca(c.mapToLLVMType(outType), name)
		// Initialize with zero value (important for empty ranges where loop body never runs).
		// Use zero value's type for Ptr.Elem to preserve Static flag for strings.
		zeroVal := c.makeZeroValue(outType)
		c.createStore(zeroVal.Val, ptr, zeroVal.Type)
		out := &Symbol{
			Val:      ptr,
			Type:     Ptr{Elem: zeroVal.Type},
			Borrowed: borrowed,
		}
		outputs[i] = out
	}
	return outputs
}

// Destination-aware prefix compilation,
// mirroring compileInfixExpression/compileInfixRanges.

func (c *Compiler) compilePrefixExpression(expr *ast.PrefixExpression, dest []*ast.Identifier) (res []*Symbol) {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	// Filter out ranges that are already bound (converted to scalar iterators in outer loops)
	pending := c.pendingLoopRanges(info.Ranges)
	// If the result is an array, let the operand handle any collection itself.
	if len(pending) == 0 {
		return c.compilePrefixBasic(expr, info)
	}
	return c.compilePrefixRanges(expr, info, dest)
}

// compilePrefix compiles a unary operation on a symbol, delegating array
// broadcasting to compileArrayUnaryPrefix and scalar lowering to defaultUnaryOps.
func (c *Compiler) compilePrefix(op string, operand *Symbol, expected Type) *Symbol {
	sym := c.derefIfPointer(operand, "")
	if expectedArr, ok := expected.(Array); ok {
		return c.compileArrayUnaryPrefix(op, sym, expectedArr)
	}
	key := unaryOpKey{Operator: op, OperandType: sym.Type}
	return defaultUnaryOps[key](c, sym, true)
}

func (c *Compiler) compilePrefixBasic(expr *ast.PrefixExpression, info *ExprInfo) (res []*Symbol) {
	operand := c.compileExpression(expr.Right, nil)
	for i, opSym := range operand {
		res = append(res, c.compilePrefix(expr.Operator, opSym, info.OutTypes[i]))
	}

	// Free temporary operand after use - the result is a new value
	c.freeTemporary(expr.Right, operand)

	return res
}

// compileArrayUnaryPrefix broadcasts a unary operator over a numeric array.

func (c *Compiler) compilePrefixRanges(expr *ast.PrefixExpression, info *ExprInfo, dest []*ast.Identifier) (res []*Symbol) {
	// New scope so we can temporarily shadow outputs like mini-functions do.
	PushScope(&c.Scopes, BlockScope)
	defer c.popScope()

	// Allocate/seed per-destination temps (seed from existing value or zero).
	// Mark as borrowed so cleanupScope skips them - the values are returned via out.
	outputs := c.makeOutputs(dest, info.OutTypes, true)

	// Rewritten operand under tmp iters.
	rightRew := info.Rewrite.(*ast.PrefixExpression).Right

	// Drive the loops and store into outputs each trip.
	c.withLoopNestVersioned(info.Ranges, info.Rewrite.(*ast.PrefixExpression), func() {
		c.pushBoundsGuard("prefix_iter_bounds_guard")
		defer c.popBoundsGuard()

		ops := c.compileExpression(rightRew, nil)

		for i := 0; i < len(ops); i++ {
			c.compileRangePrefixSlot(expr.Operator, ops[i], info.OutTypes[i], outputs[i])
		}

		// Range-loop operand is temporary per iteration (except identifiers).
		c.freeTemporary(rightRew, ops)
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

func (c *Compiler) compileRangePrefixSlot(op string, operand *Symbol, expected Type, output *Symbol) {
	run := func() {
		computed := c.compilePrefix(op, operand, expected)

		// Free previous iteration's result before overwriting
		c.freeSymbolValue(output, "old_output")
		c.createStore(computed.Val, output.Val, computed.Type)
	}

	if !c.withStmtBoundsGuard(
		"prefix_bounds_ok",
		"prefix_bounds_run",
		"prefix_bounds_skip",
		"prefix_bounds_cont",
		run,
		nil,
	) {
		run()
	}
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
		fields[i] = llvm.PointerType(c.mapToLLVMType(t), 0)
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

func (c *Compiler) compileFunc(template *ast.FuncStatement, mangled string, fnInfo *Func, paramTypes []Type, retStruct llvm.Type, funcType llvm.Type) llvm.Value {
	function := llvm.AddFunction(c.Module, mangled, funcType)

	sretAttr := c.Context.CreateTypeAttribute(llvm.AttributeKindID("sret"), retStruct)
	function.AddAttributeAtIndex(1, sretAttr) // Index 1 is the first parameter

	// Create entry block
	entry := c.Context.AddBasicBlock(function, "entry")
	savedBlock := c.builder.GetInsertBlock()
	c.builder.SetInsertPointAtEnd(entry)

	// Set FuncNameMangled so ExprCache entries are keyed to this function
	savedFuncNameMangled := c.FuncNameMangled
	c.FuncNameMangled = mangled
	c.compileFuncBlock(template, fnInfo, paramTypes, retStruct, function)
	c.FuncNameMangled = savedFuncNameMangled

	c.builder.CreateRetVoid()

	// Restore the builder to where it was before compiling this function
	if !savedBlock.IsNil() {
		c.builder.SetInsertPointAtEnd(savedBlock)
	}

	return function
}

func (c *Compiler) processOutputs(fn *ast.FuncStatement, retStruct llvm.Type, sretPtr llvm.Value, finalOutTypes []Type) []*Symbol {
	retPtrs := make([]*Symbol, len(fn.Outputs))
	for i, outIdent := range fn.Outputs {
		// GEP to get pointer to sret field (which holds a destination pointer)
		fieldPtr := c.builder.CreateStructGEP(retStruct, sretPtr, i, outIdent.Value+"_field")
		// Load the destination pointer from the sret field
		ptrType := llvm.PointerType(c.mapToLLVMType(finalOutTypes[i]), 0)
		destPtr := c.builder.CreateLoad(ptrType, fieldPtr, outIdent.Value+"_dest")

		// Use the type solver's output type. For strings, this includes the Static flag
		// which tells the callee whether the old value is heap-allocated.
		retPtrs[i] = &Symbol{
			Val:      destPtr,
			Type:     Ptr{Elem: finalOutTypes[i]},
			FuncArg:  true,
			Borrowed: true,
			ReadOnly: false,
		}
		// Bind output name to destination pointer so body writes to correct location
		Put(c.Scopes, outIdent.Value, retPtrs[i])
	}
	return retPtrs
}

func (c *Compiler) processParams(template *ast.FuncStatement, fnInfo *Func, paramTypes []Type, function llvm.Value) ([]*Symbol, []int) {
	inputs := make([]*Symbol, len(paramTypes))
	iterIndices := []int{}

	for i, param := range template.Parameters {
		name := param.Value
		// paramTypes has outer types (includes Range), fnInfo.Params has inner types (Range unwrapped)
		elemType := paramTypes[i]
		paramVal := function.Param(i + 1) // +1 because param 0 is sret

		inputs[i] = &Symbol{
			Val:      paramVal,
			Type:     Ptr{Elem: elemType},
			FuncArg:  true,
			Borrowed: true,
			ReadOnly: true,
		}

		kind := elemType.Kind()
		if kind == RangeKind || kind == ArrayRangeKind {
			iterIndices = append(iterIndices, i)
			continue
		}
		// Put non-iterator params in scope
		Put(c.Scopes, name, inputs[i])
	}
	return inputs, iterIndices
}

func (c *Compiler) compileFuncIter(template *ast.FuncStatement, inputs []*Symbol, iterIndices []int, retPtrs []*Symbol, function llvm.Value) {
	fa := &FuncArgs{
		Inputs:      inputs,
		Outputs:     retPtrs,
		IterIndices: iterIndices,
		Iters:       make(map[string]*Symbol),
	}
	c.funcLoopNest(template, fa, function, 0)
}

func (c *Compiler) compileFuncBlock(template *ast.FuncStatement, fnInfo *Func, paramTypes []Type, retStruct llvm.Type, function llvm.Value) {
	PushScope(&c.Scopes, FuncScope)
	defer c.popScope()

	sretPtr := function.Param(0)
	inputs, iterIndices := c.processParams(template, fnInfo, paramTypes, function)
	retPtrs := c.processOutputs(template, retStruct, sretPtr, fnInfo.OutTypes)

	if len(iterIndices) == 0 {
		c.compileBlockWithArgs(template, map[string]*Symbol{}, map[string]*Symbol{})
		return
	}

	c.compileFuncIter(template, inputs, iterIndices, retPtrs, function)
}

func (c *Compiler) iterOverRange(rangeType Range, rangeVal llvm.Value, body func(llvm.Value, Type)) {
	iterType := rangeType.Iter
	c.createLoop(rangeVal, func(iter llvm.Value) {
		body(iter, iterType)
	})
}

func (c *Compiler) iterOverArrayRange(arrRangeSym *Symbol, body func(llvm.Value, Type)) {
	arrRangeType := arrRangeSym.Type.(ArrayRange)
	arrPtr := c.builder.CreateExtractValue(arrRangeSym.Val, 0, "array_range_ptr")
	rangeVal := c.builder.CreateExtractValue(arrRangeSym.Val, 1, "array_range_bounds")
	arraySym := &Symbol{Val: arrPtr, Type: arrRangeType.Array}
	elemType := arrRangeType.Array.ColTypes[0]
	c.createLoop(rangeVal, func(iter llvm.Value) {
		inBounds := c.arrayIndexInBounds(arraySym, elemType, iter)
		iterBlock, skipBlock, contBlock := c.createIfElseCont(inBounds, "arr_iter_in_bounds", "arr_iter_oob", "arr_iter_cont")

		c.builder.SetInsertPointAtEnd(iterBlock)
		elemVal := c.ArrayGetBorrowed(arraySym, elemType, iter)
		body(elemVal, elemType)
		c.builder.CreateBr(contBlock)

		c.builder.SetInsertPointAtEnd(skipBlock)
		c.builder.CreateBr(contBlock)

		c.builder.SetInsertPointAtEnd(contBlock)
	})
}

func (c *Compiler) funcLoopNest(fn *ast.FuncStatement, fa *FuncArgs, function llvm.Value, level int) {
	if level == len(fa.IterIndices) {
		c.compileBlockWithArgs(fn, map[string]*Symbol{}, fa.Iters)
		return
	}

	paramIdx := fa.IterIndices[level]
	input := fa.Inputs[paramIdx]
	name := fn.Parameters[paramIdx].Value

	next := func(iterVal llvm.Value, iterType Type) {
		fa.Iters[name] = &Symbol{
			Val:      iterVal,
			Type:     iterType,
			FuncArg:  true,
			Borrowed: true,
			ReadOnly: false,
		}
		c.funcLoopNest(fn, fa, function, level+1)
	}

	// Inputs are pointers, extract element type and load the value
	elemType := input.Type.(Ptr).Elem
	paramPtr := input.Val

	switch elemType.Kind() {
	case RangeKind:
		rangeType := elemType.(Range)
		rangeVal := c.createLoad(paramPtr, elemType, name+"_range")
		c.iterOverRange(rangeType, rangeVal, next)
	case ArrayRangeKind:
		arrRangeVal := c.createLoad(paramPtr, elemType, name+"_arrrange")
		arrRangeSym := &Symbol{
			Val:      arrRangeVal,
			Type:     elemType,
			FuncArg:  true,
			Borrowed: true,
			ReadOnly: true,
		}
		c.iterOverArrayRange(arrRangeSym, next)
	default:
		panic("unsupported iterator kind in funcLoopNest")
	}
	delete(fa.Iters, name)
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

// createIfElseCont emits a conditional branch and creates if/else/cont blocks
// in the current function.
func (c *Compiler) createIfElseCont(cond llvm.Value, ifName, elseName, contName string) (llvm.BasicBlock, llvm.BasicBlock, llvm.BasicBlock) {
	fn := c.builder.GetInsertBlock().Parent()
	ifBlock := c.Context.AddBasicBlock(fn, ifName)
	elseBlock := c.Context.AddBasicBlock(fn, elseName)
	contBlock := c.Context.AddBasicBlock(fn, contName)
	c.builder.CreateCondBr(cond, ifBlock, elseBlock)
	return ifBlock, elseBlock, contBlock
}

// createIfCont emits a conditional branch and creates if/cont blocks
// in the current function.
func (c *Compiler) createIfCont(cond llvm.Value, ifName, contName string) (llvm.BasicBlock, llvm.BasicBlock) {
	fn := c.builder.GetInsertBlock().Parent()
	ifBlock := c.Context.AddBasicBlock(fn, ifName)
	contBlock := c.Context.AddBasicBlock(fn, contName)
	c.builder.CreateCondBr(cond, ifBlock, contBlock)
	return ifBlock, contBlock
}

func (c *Compiler) compileArgs(ce *ast.CallExpression) []*Symbol {
	funcName := ce.Function.Value
	var args []*Symbol
	for idx, callArg := range ce.Arguments {
		if ident, ok := callArg.(*ast.Identifier); ok {
			// Identifier: get raw symbol without dereferencing
			sym, _ := c.getRawSymbol(ident.Value)
			// If not already a pointer, promote to memory
			if sym.Type.Kind() != PtrKind {
				sym = c.promoteToMemory(ident.Value)
			}
			args = append(args, sym)
			continue
		}
		// Expression: evaluate, then wrap in pointer if needed
		res := c.compileExpression(callArg, nil)
		for _, r := range res {
			if r.Type.Kind() != PtrKind {
				name := fmt.Sprintf("%s_arg_%d", funcName, idx)
				r, _ = c.makePtr(name, r)
			}
			args = append(args, r)
		}
	}
	return args
}

func (c *Compiler) compileCallExpression(ce *ast.CallExpression, dest []*ast.Identifier) (res []*Symbol) {
	info := c.ExprCache[key(c.FuncNameMangled, ce)]

	outputs := c.makeOutputs(dest, info.OutTypes, false)

	// If LoopInside=false, wrap call in loops for all ranges
	if !info.LoopInside && len(info.Ranges) > 0 {
		rewCall := info.Rewrite.(*ast.CallExpression)
		c.withLoopNestVersioned(info.Ranges, rewCall, func() {
			// Scope bounds checks to this loop iteration: arguments can contain
			// multiple array reads, and the call should execute only when all are
			// in-bounds for this iteration.
			c.pushBoundsGuard("call_iter_bounds_guard")
			// Inside loop, ranges are shadowed as scalars. If call arguments contain
			// conditional expressions, execute the call only when they hold.
			c.compileCondExprValue(rewCall, llvm.Value{}, func() {
				c.compileCallInner(ce.Function.Value, rewCall, outputs)
			})
			c.popBoundsGuard()
		})

		// Loop path materializes final values from output slots after iteration.
		// Slots are seeded by makeOutputs (existing value or zero for new vars), so
		// empty ranges naturally preserve no-op semantics for existing destinations.
		out := make([]*Symbol, len(outputs))
		for i := range outputs {
			// Use info.OutTypes[i] for correct Static flag on strings, not outputs[i].Type
			// which may retain stale flags from existing variables.
			elemType := info.OutTypes[i]
			out[i] = &Symbol{
				Val:  c.createLoad(outputs[i].Val, elemType, "final"),
				Type: elemType,
			}
		}
		return out
	}

	// LoopInside=true or no ranges: direct call
	c.compileCallInner(ce.Function.Value, ce, outputs)

	// Update output types (e.g., Static flag for strings) when there are no ranges.
	// Old value freeing is handled by writeTo using captured old values.
	if !info.HasRanges {
		c.updateOutputTypes(outputs, info.OutTypes, dest)
	}

	return outputs
}

// updateOutputTypes updates the destination symbols in scope to reference the output values.
// With StrG/StrH types, the type is determined at type-solving time and doesn't change.
func (c *Compiler) updateOutputTypes(outputs []*Symbol, outTypes []Type, dest []*ast.Identifier) {
	for i, out := range outputs {
		if i >= len(outTypes) || dest == nil || i >= len(dest) {
			continue
		}
		// Update the symbol in scope to reference this output
		Put(c.Scopes, dest[i].Value, out)
	}
}

// compileCallInner compiles the actual function call
func (c *Compiler) compileCallInner(funcName string, ce *ast.CallExpression, outputs []*Symbol) {
	args := c.compileArgs(ce)

	// Extract element types from pointer args for mangling.
	// Functions are mangled separately for StrG/StrH, so no normalization needed.
	paramTypes := make([]Type, len(args))
	for i, arg := range args {
		paramTypes[i] = arg.Type.(Ptr).Elem
	}

	mangled := Mangle(c.MangledPath, funcName, paramTypes)
	fnInfo := c.FuncCache[mangled]
	if fnInfo == nil {
		c.Errors = append(c.Errors, &token.CompileError{
			Token: ce.Tok(),
			Msg:   fmt.Sprintf("function %s not found for argument types %v", funcName, paramTypes),
		})
		return
	}

	retStruct := c.getReturnStruct(mangled, fnInfo.OutTypes)
	sretPtr := c.createEntryBlockAlloca(retStruct, "sret_tmp")

	// Populate sret with output destination pointers
	for i, out := range outputs {
		fieldPtr := c.builder.CreateStructGEP(retStruct, sretPtr, i, fmt.Sprintf("sret_field_%d", i))
		c.builder.CreateStore(out.Val, fieldPtr)
	}

	// LLVM inputs are pointer types since we pass by reference
	llvmInputs := make([]llvm.Type, len(args))
	for i, arg := range args {
		llvmInputs[i] = c.mapToLLVMType(arg.Type)
	}
	funcType := c.getFuncType(retStruct, llvmInputs)
	fn := c.Module.NamedFunction(mangled)
	if fn.IsNil() {
		fk := ast.FuncKey{
			FuncName: funcName,
			Arity:    len(paramTypes),
		}
		template := c.CodeCompiler.Code.Func.Map[fk]
		savedBlock := c.builder.GetInsertBlock()

		fn = c.compileFunc(template, mangled, fnInfo, paramTypes, retStruct, funcType)
		c.builder.SetInsertPointAtEnd(savedBlock)
	}

	if !c.withStmtBoundsGuard(
		"call_bounds_ok",
		"call_bounds_run",
		"call_bounds_skip",
		"call_bounds_cont",
		func() {
			c.callFunctionDirect(fn, funcType, args, sretPtr, outputs)
		},
		nil,
	) {
		c.callFunctionDirect(fn, funcType, args, sretPtr, outputs)
	}

	// Free temporary arguments after the call. Function copies input values to outputs
	// (see computeCopyRequirements - no Borrowed skip), so temps can be safely freed.
	// Identifiers are skipped by freeTemporary since they're owned by caller's scope.
	for i, arg := range ce.Arguments {
		c.freeTemporary(arg, []*Symbol{args[i]})
	}
}

func (c *Compiler) callFunctionDirect(fn llvm.Value, funcType llvm.Type, args []*Symbol, sretPtr llvm.Value, outputs []*Symbol) []*Symbol {
	llvmArgs := []llvm.Value{sretPtr}
	for _, arg := range args {
		llvmArgs = append(llvmArgs, arg.Val)
	}

	c.builder.CreateCall(funcType, fn, llvmArgs, "")

	// Function writes directly through destination pointers in sret
	// Results are already in outputs, just return them
	return outputs
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
			Type:     StrH{}, // Copied strings are always heap-allocated
			FuncArg:  false,
			Borrowed: false,
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
				Borrowed: false,
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
	currentScope := c.Scopes[len(c.Scopes)-1]
	for _, sym := range currentScope.Elems {
		// Skip borrowed symbols - this scope does not own them.
		if sym.Borrowed {
			continue
		}
		c.freeSymbolValue(sym, "cleanup_load")
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
	c.pushStmtCtx()
	defer c.popStmtCtx()

	ce := ps.Expression
	info := c.ExprCache[key(c.FuncNameMangled, ce)]

	// If LoopInside=false, wrap print in loops for all ranges
	if !info.LoopInside && len(info.Ranges) > 0 {
		rewCall := info.Rewrite.(*ast.CallExpression)
		c.withLoopNestVersioned(info.Ranges, rewCall, func() {
			c.printAllExpressions(rewCall.Arguments)
		})
		return
	}

	// LoopInside=true or no ranges: direct print
	c.printAllExpressions(ce.Arguments)
}

// printAllExpressions prints all expressions on a single line
func (c *Compiler) printAllExpressions(exprs []ast.Expression) {
	var formatStr string
	var args []llvm.Value
	var toFree []llvm.Value

	for _, expr := range exprs {
		c.appendPrintExpression(expr, &formatStr, &args, &toFree)
	}

	formatPtr := c.createPrintFormatGlobal(formatStr)
	allArgs := append([]llvm.Value{formatPtr}, args...)
	c.printf(allArgs)
	c.free(toFree)
}

// asStringLiteral extracts token and value from string literal expressions (regular or heap).
func asStringLiteral(expr ast.Expression) (tok token.Token, value string, ok bool) {
	switch e := expr.(type) {
	case *ast.StringLiteral:
		return e.Token, e.Value, true
	case *ast.HeapStringLiteral:
		return e.Token, e.Value, true
	}
	return token.Token{}, "", false
}

// appendPrintExpression handles one print expression (string literal or compiled expression)
func (c *Compiler) appendPrintExpression(expr ast.Expression, formatStr *string, args *[]llvm.Value, toFree *[]llvm.Value) {
	// String literals (regular or heap) with markers are processed specially
	if tok, value, ok := asStringLiteral(expr); ok {
		processed, newArgs, toFreeArgs := c.formatString(tok, value)
		*formatStr += processed + " "
		*args = append(*args, newArgs...)
		*toFree = append(*toFree, toFreeArgs...)
		return
	}

	// Compile expression and process each resulting symbol
	syms := c.compileExpression(expr, nil)
	for _, s := range syms {
		c.appendPrintSymbol(s, expr, formatStr, args, toFree)
	}

	// String temporaries are consumed by printf directly, so defer freeing until
	// after print. Non-string temporaries can be released immediately.
	nonStringTemps := make([]*Symbol, 0, len(syms))
	for _, s := range syms {
		if s.Type.Kind() == StrKind {
			continue
		}
		if ptrType, ok := s.Type.(Ptr); ok && ptrType.Elem.Kind() == StrKind {
			continue
		}
		nonStringTemps = append(nonStringTemps, s)
	}
	c.freeTemporary(expr, nonStringTemps)
}

// appendPrintSymbol handles printing one symbol based on its type
func (c *Compiler) appendPrintSymbol(s *Symbol, expr ast.Expression, formatStr *string, args *[]llvm.Value, toFree *[]llvm.Value) {
	// Dereference pointers first - treat print args like function args
	if s.Type.Kind() == PtrKind {
		elemType := s.Type.(Ptr).Elem
		derefed := c.createLoad(s.Val, elemType, "print_deref")
		s = &Symbol{Val: derefed, Type: elemType}
	}

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
	case StrKind:
		*args = append(*args, s.Val)
		// Heap string temporaries must survive until printf executes.
		if IsStrH(s.Type) {
			if _, isIdent := expr.(*ast.Identifier); !isIdent {
				*toFree = append(*toFree, s.Val)
			}
		}
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
