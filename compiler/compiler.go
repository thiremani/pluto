package compiler

import (
	"fmt"
	"slices"
	"strings"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

type Symbol struct {
	Val       llvm.Value
	Type      Type
	FuncArg   bool       // Symbol originates from function input/output argument context.
	Borrowed  bool       // Value/storage is borrowed from another owner (scope cleanup must skip).
	ReadOnly  bool       // Input parameter (cannot be written to).
	WriteFlag llvm.Value // Optional i1* set when this logical output is actually written.
}

// Borrowed-value ownership model:
//
// Function lowering is ABI-classified. Indirect params/results use caller-owned
// storage as before; direct scalar params stay as SSA values in function scope
// until addressable storage is semantically required, and direct scalar outputs
// stay as seeded values in function scope until storage is semantically required.
// Direct scalar returns cross the call boundary as SSA values.
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
//     - Function writes into an independent, destination-seeded staging slot
//     - The staged result is MOVED to the real destination after all RHS values
//       have been evaluated
//     - The real destination's old value is then freed by freeExprOldValues
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
	Inputs      []*Symbol // lowered function inputs (range iterators remain pointer-backed)
	IterIndices []int     // Indices of iterator params
}

type callArg struct {
	Expr        ast.Expression
	Name        string
	Symbol      *Symbol
	Lowered     *Symbol
	OutputAlias int
}

type callSignature struct {
	FuncName   string
	Mangled    string
	ParamTypes []Type
	FnInfo     *Func
	ABI        FuncABI
}

type preparedCall struct {
	Args         []callArg
	AliasIndices []int
	Function     llvm.Value
	FuncType     llvm.Type
	RetStruct    llvm.Type
}

// BindingKey identifies a variable binding within a specific function variant.
// Script-level bindings use an empty FuncNameMangled.
type BindingKey struct {
	FuncNameMangled string
	Name            string
}

// paramAlias tracks an aliased direct scalar param binding for the active
// function body. The Base check prevents alias behavior from leaking onto a
// same-name binding introduced later in the scope tree.
type paramAlias struct {
	Base        *Symbol
	AliasIndex  llvm.Value
	OutputNames []string
}

type symbolSource int

const (
	symbolMissing symbolSource = iota
	symbolLocal
	symbolCode
)

func GetCopy(s *Symbol) (newSym *Symbol) {
	newSym = &Symbol{}
	newSym.Val = s.Val
	newSym.Type = s.Type
	newSym.FuncArg = s.FuncArg
	newSym.Borrowed = s.Borrowed
	newSym.ReadOnly = s.ReadOnly
	newSym.WriteFlag = s.WriteFlag
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
	StructCache     map[string]*Struct
	FuncCache       map[string]*Func
	BindingTypes    map[BindingKey]Type
	ExprCache       map[ExprKey]*ExprInfo
	FuncNameMangled string // current function's mangled name ("" for script level)
	Errors          []*token.CompileError
	paramAliasStack []map[string]*paramAlias
	stmtCtxStack    []stmtCtx
}

type stmtCtx struct {
	condStack         []map[ExprKey][]*Symbol // Cond-expr frames (one map per compileCondExprValue invocation)
	boundsStack       []boundsGuardFrame      // Nested bounds guards active within this statement
	loopBoundsStack   []loopBoundsFrame       // Loop bounds mode stack active within this statement
	arrayLitCellDepth int                     // Nested array-literal cell compilation frames active within this statement
}

func NewCompiler(ctx llvm.Context, mangledPath string, cc *CodeCompiler) *Compiler {
	module := ctx.NewModule(mangledPath)
	if triple, dataLayout := defaultModuleTargetMetadata(); triple != "" {
		module.SetTarget(triple)
		if dataLayout != "" {
			module.SetDataLayout(dataLayout)
		}
	}
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
		StructCache:     make(map[string]*Struct),
		FuncCache:       make(map[string]*Func),
		ExprCache:       make(map[ExprKey]*ExprInfo),
		FuncNameMangled: "",
		Errors:          []*token.CompileError{},
		paramAliasStack: []map[string]*paramAlias{},
		stmtCtxStack:    []stmtCtx{},
	}
}

func (c *Compiler) rejectReservedName(tok token.Token, kind string) {
	if _, reserved := reservedTypeNames[tok.Literal]; reserved {
		c.Errors = append(c.Errors, &token.CompileError{
			Token: tok,
			Msg:   fmt.Sprintf("%s name %q is a reserved name", kind, tok.Literal),
		})
	}
}

func (c *Compiler) bindingSlotType(name string, fallback Type) Type {
	typ, ok := c.BindingTypes[BindingKey{
		FuncNameMangled: c.FuncNameMangled,
		Name:            name,
	}]
	if !ok {
		return fallback
	}
	return typ
}

func (c *Compiler) currentParamAliases() map[string]*paramAlias {
	if len(c.paramAliasStack) == 0 {
		return nil
	}
	return c.paramAliasStack[len(c.paramAliasStack)-1]
}

func (c *Compiler) pushParamAliases() {
	c.paramAliasStack = append(c.paramAliasStack, make(map[string]*paramAlias))
}

func (c *Compiler) popParamAliases() {
	c.paramAliasStack = c.paramAliasStack[:len(c.paramAliasStack)-1]
}

func identNames(idents []*ast.Identifier) []string {
	names := make([]string, len(idents))
	for i, ident := range idents {
		names[i] = ident.Value
	}
	return names
}

// bindParamAlias records the output names eagerly, but the outputs themselves
// are resolved lazily from scope when the param is later read or promoted.
// This allows direct outputs to remain values, be replaced in scope, or be
// promoted to slots without invalidating the alias metadata.
func (c *Compiler) bindParamAlias(name string, sym *Symbol, aliasIndex llvm.Value, outputNames []string) {
	c.currentParamAliases()[name] = &paramAlias{
		Base:        sym,
		AliasIndex:  aliasIndex,
		OutputNames: append([]string(nil), outputNames...),
	}
}

func (c *Compiler) clearParamAlias(name string) {
	delete(c.currentParamAliases(), name)
}

func (c *Compiler) paramAliasFor(name string, sym *Symbol) (*paramAlias, bool) {
	alias, ok := c.currentParamAliases()[name]
	if !ok || alias.Base != sym {
		return nil, false
	}
	return alias, true
}

func (c *Compiler) resolvedDestTypes(dest []*ast.Identifier, outTypes []Type) []Type {
	resolved := make([]Type, len(outTypes))
	for i, outType := range outTypes {
		resolved[i] = outType
		if dest == nil || i >= len(dest) {
			continue
		}
		bindingType, ok := c.BindingTypes[BindingKey{
			FuncNameMangled: c.FuncNameMangled,
			Name:            dest[i].Value,
		}]
		if ok {
			resolved[i] = bindingType
			continue
		}
		// Conditional lowering writes through synthetic condtmp_* identifiers.
		// They have no solver binding entry, but their pointer element is the
		// authoritative slot flavor selected for the real destination.
		if sym, exists := Get(c.Scopes, dest[i].Value); exists {
			if ptrType, isPtr := sym.Type.(Ptr); isPtr {
				resolved[i] = ptrType.Elem
			}
		}
	}
	return resolved
}

func (c *Compiler) addCallTypeError(tok token.Token, msg string) bool {
	c.Errors = append(c.Errors, &token.CompileError{
		Token: tok,
		Msg:   msg,
	})
	return false
}

// inferCallParamTypes selects the solver-cached call variant to use at the
// current lowering site. Once outer loops have consumed all pending ranges, the
// scalarized param types become the right callee variant for code generation.
func (c *Compiler) inferCallParamTypes(info *ExprInfo) []Type {
	if len(c.pendingLoopRanges(info.Ranges)) == 0 {
		return info.ScalarCallParamTypes
	}
	return info.CallParamTypes
}

func (c *Compiler) resolveCallSignature(funcName string, ce *ast.CallExpression, info *ExprInfo) (*callSignature, bool) {
	paramTypes := c.inferCallParamTypes(info)
	mangled := Mangle(c.MangledPath, funcName, paramTypes)
	fnInfo := c.FuncCache[mangled]
	if fnInfo == nil {
		c.Errors = append(c.Errors, &token.CompileError{
			Token: ce.Tok(),
			Msg:   fmt.Sprintf("function %s not found for argument types %v", funcName, paramTypes),
		})
		return nil, false
	}

	return &callSignature{
		FuncName:   funcName,
		Mangled:    mangled,
		ParamTypes: paramTypes,
		FnInfo:     fnInfo,
		ABI:        classifyFuncABI(paramTypes, fnInfo.OutTypes),
	}, true
}

// buildCallParamAliasIndices records which direct scalar params alias caller
// destinations for range-bearing variants. The callee uses these indices to
// redirect a by-value param spill to the matching output slot so loop-carried
// accumulation keeps the same semantics as the legacy indirect ABI.
func (c *Compiler) buildCallParamAliasIndices(sig *callSignature, args []callArg, dest []*ast.Identifier) []int {
	aliasIndices := make([]int, sig.ABI.NumAliasSlots())
	if dest == nil {
		return aliasIndices
	}

	for i, arg := range args {
		aliasSlot := sig.ABI.Params[i].AliasSlot
		if aliasSlot < 0 || arg.Name == "" {
			continue
		}

		for j := range sig.FnInfo.OutTypes {
			if j >= len(dest) {
				break
			}
			if dest[j].Value != arg.Name {
				continue
			}
			aliasIndices[aliasSlot] = j + 1
			break
		}
	}

	return aliasIndices
}

// directReturnSeedForCall captures the caller's current destination value for a
// direct scalar return. Range-bearing variants thread this through a hidden ABI
// param so the callee can preserve empty-range and loop-carried accumulation
// semantics even though the LLVM return itself is by value.
func (c *Compiler) directReturnSeedForCall(outType Type, dest *ast.Identifier, output *Symbol) *Symbol {
	if output != nil {
		return c.derefIfPointer(output, "call_seed")
	}
	if dest != nil {
		return c.resolveDestSeed(dest, outType)
	}
	return c.makeZeroValue(outType)
}

func (c *Compiler) selectAliasedParamPtr(name string, spill llvm.Value, aliasIndex llvm.Value, outputs []*Symbol) llvm.Value {
	slotPtr := spill
	for i, output := range outputs {
		match := c.builder.CreateICmp(
			llvm.IntEQ,
			aliasIndex,
			llvm.ConstInt(c.Context.Int32Type(), uint64(i+1), false),
			fmt.Sprintf("%s_alias_%d", name, i),
		)
		slotPtr = c.builder.CreateSelect(match, output.Val, slotPtr, fmt.Sprintf("%s_slot_%d", name, i))
	}
	return slotPtr
}

func (c *Compiler) localValSymbol(name string, loadName string) (*Symbol, bool) {
	s, ok := Get(c.Scopes, name)
	if !ok {
		return nil, false
	}
	return c.valueSymbol(name, s, loadName), true
}

func (c *Compiler) lookupNamedSymbol(name string) (*Symbol, symbolSource) {
	if s, ok := Get(c.Scopes, name); ok {
		return s, symbolLocal
	}
	if c.CodeCompiler == nil {
		return nil, symbolMissing
	}
	s, ok := Get(c.CodeCompiler.Compiler.Scopes, name)
	if !ok {
		return nil, symbolMissing
	}
	return s, symbolCode
}

func (c *Compiler) directParamValue(name string, sym *Symbol, alias *paramAlias) *Symbol {
	if alias == nil || len(alias.OutputNames) == 0 {
		return sym
	}

	value := sym.Val
	for i, outputName := range alias.OutputNames {
		match := c.builder.CreateICmp(
			llvm.IntEQ,
			alias.AliasIndex,
			llvm.ConstInt(c.Context.Int32Type(), uint64(i+1), false),
			fmt.Sprintf("%s_alias_match_%d", name, i),
		)
		output, _ := c.localValSymbol(outputName, fmt.Sprintf("%s_alias_load_%d", outputName, i))
		aliasVal := c.coerceSymbolForType(output, sym.Type, fmt.Sprintf("%s_alias_value_%d", outputName, i))
		value = c.builder.CreateSelect(match, aliasVal.Val, value, fmt.Sprintf("%s_alias_value_%d", name, i))
	}

	resolved := GetCopy(sym)
	resolved.Val = value
	return resolved
}

func (c *Compiler) valueSymbol(name string, sym *Symbol, loadName string) *Symbol {
	if alias, ok := c.paramAliasFor(name, sym); ok {
		return c.directParamValue(name, sym, alias)
	}
	return c.derefIfPointer(sym, loadName)
}

func (c *Compiler) namedValueSymbol(name string, loadName string) (*Symbol, bool) {
	s, source := c.lookupNamedSymbol(name)
	if source == symbolMissing {
		return nil, false
	}
	if source == symbolLocal {
		return c.valueSymbol(name, s, loadName), true
	}
	// CodeCompiler bindings are global code-scope values/slots, not active
	// function params, so they never participate in param-alias resolution.
	return c.derefIfPointer(s, loadName), true
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
		return llvm.StructType(
			[]llvm.Type{
				c.mapToLLVMType(arrRange.Array),
				c.mapToLLVMType(arrRange.Range),
			},
			false,
		)
	case PtrKind:
		ptrType := t.(Ptr)
		elemLLVM := c.mapToLLVMType(ptrType.Elem)
		return llvm.PointerType(elemLLVM, 0)
	case ArrayKind:
		arrayType := t.(Array)
		arrayPtr := llvm.PointerType(c.Context.Int8Type(), 0)
		if arrayType.Rank == 1 {
			return arrayPtr
		}
		i64 := c.Context.Int64Type()
		fields := make([]llvm.Type, arrayType.Rank+1)
		fields[0] = arrayPtr
		for i := 1; i < len(fields); i++ {
			fields[i] = i64
		}
		return llvm.StructType(fields, false)
	case TableKind:
		table := t.(Table)
		fields := make([]llvm.Type, len(table.Columns)+1)
		fields[0] = c.Context.Int64Type()
		arrayPtr := llvm.PointerType(c.Context.Int8Type(), 0)
		for i := range table.Columns {
			fields[i+1] = arrayPtr
		}
		return llvm.StructType(fields, false)
	case StructKind:
		return c.getOrCreateStructLLVMType(t.(Struct))
	default:
		panic("unknown type in mapToLLVMType: " + t.String())
	}
}

func (c *Compiler) structTypeName(typeName string) string {
	return c.MangledPath + SEP + MangleIdent(typeName)
}

func (c *Compiler) getOrCreateStructLLVMType(structType Struct) llvm.Type {
	name := c.structTypeName(structType.Name)
	if st := c.Module.GetTypeByName(name); !st.IsNil() {
		return st
	}

	st := c.Context.StructCreateNamed(name)
	fieldTypes := make([]llvm.Type, len(structType.Fields))
	for i, field := range structType.Fields {
		fieldTypes[i] = c.mapToLLVMType(field.Type)
	}
	st.StructSetBody(fieldTypes, false)
	return st
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

func structFieldTypeFromConstant(cell ast.Expression) (Type, bool) {
	switch cell.(type) {
	case *ast.IntegerLiteral:
		return I64, true
	case *ast.FloatLiteral:
		return F64, true
	case *ast.StringLiteral:
		return StrG{}, true
	default:
		return Unresolved{}, false
	}
}

// getStructSchema looks up a struct type's canonical schema from StructCache.
// StructCache is populated by validateStructDefs before compilation begins.
func (c *Compiler) getStructSchema(lit *ast.StructLiteral) (*Struct, bool) {
	typeName := lit.Token.Literal
	if schema, ok := c.StructCache[typeName]; ok {
		return schema, true
	}
	c.Errors = append(c.Errors, &token.CompileError{
		Token: lit.Token,
		Msg:   fmt.Sprintf("struct type %s is not defined", typeName),
	})
	return nil, false
}

func (c *Compiler) structFieldConstValue(typeName, fieldName string, fieldType Type, cell ast.Expression) (llvm.Value, bool) {
	switch cv := cell.(type) {
	case *ast.IntegerLiteral:
		switch fieldType.Kind() {
		case IntKind:
			return c.ConstI64(uint64(cv.Value)), true
		case FloatKind:
			return c.ConstF64(float64(cv.Value)), true
		}
	case *ast.FloatLiteral:
		if fieldType.Kind() == FloatKind {
			return c.ConstF64(cv.Value), true
		}
	case *ast.StringLiteral:
		if fieldType.Kind() == StrKind {
			fieldGlobalName := fmt.Sprintf("struct_%s_%s_%d", typeName, fieldName, c.tmpCounter)
			c.tmpCounter++
			fieldGlobal := c.createGlobalString(fieldGlobalName, lexer.DecodeStringLiteral(cv.Token.Literal), llvm.PrivateLinkage)
			return llvm.ConstBitCast(fieldGlobal, llvm.PointerType(c.Context.Int8Type(), 0)), true
		}
	}

	var expected string
	switch fieldType.Kind() {
	case IntKind:
		expected = "I64"
	case FloatKind:
		expected = "F64"
	case StrKind:
		expected = "Str"
	default:
		expected = fieldType.String()
	}
	c.Errors = append(c.Errors, &token.CompileError{
		Token: cell.Tok(),
		Msg:   fmt.Sprintf("struct field %q expects %s value", fieldName, expected),
	})
	return llvm.Value{}, false
}

func (c *Compiler) structFieldZeroValue(typeName, fieldName string, fieldType Type) (llvm.Value, bool) {
	switch fieldType.Kind() {
	case IntKind:
		return c.ConstI64(0), true
	case FloatKind:
		return c.ConstF64(0), true
	case StrKind:
		fieldGlobalName := fmt.Sprintf("struct_%s_%s_default_%d", typeName, fieldName, c.tmpCounter)
		c.tmpCounter++
		fieldGlobal := c.createGlobalString(fieldGlobalName, "", llvm.PrivateLinkage)
		return llvm.ConstBitCast(fieldGlobal, llvm.PointerType(c.Context.Int8Type(), 0)), true
	default:
		c.Errors = append(c.Errors, &token.CompileError{
			Msg: fmt.Sprintf("unsupported zero value for struct field type %s (%q)", fieldType.String(), fieldName),
		})
		return llvm.Value{}, false
	}
}

func (c *Compiler) compileConstBinding(name string, valueExpr ast.Expression) {
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
		sym.Val = c.createGlobalString(mangledName, lexer.DecodeStringLiteral(v.Token.Literal), linkage)
		sym.Type = StrG{} // Global constants are static strings

	case *ast.StructLiteral:
		if !c.compileStructConst(v, sym, mangledName, linkage) {
			return
		}

	default:
		panic(fmt.Sprintf("unsupported constant type: %T", v))
	}
	Put(c.Scopes, name, sym)
}

func (c *Compiler) compileStructConst(v *ast.StructLiteral, sym *Symbol, mangledName string, linkage llvm.Linkage) bool {
	if len(v.Row) != len(v.Headers) {
		c.Errors = append(c.Errors, &token.CompileError{
			Token: v.Token,
			Msg:   fmt.Sprintf("struct row width mismatch: got %d values for %d fields", len(v.Row), len(v.Headers)),
		})
		return false
	}

	schema, ok := c.getStructSchema(v)
	if !ok {
		return false
	}

	if _, err := validateStructFieldHeaders(schema, v.Headers); err != nil {
		c.Errors = append(c.Errors, err)
		return false
	}

	cellsByHeader := make(map[string]ast.Expression, len(v.Headers))
	for idx, headerTok := range v.Headers {
		cellsByHeader[headerTok.Literal] = v.Row[idx]
	}

	constVals := make([]llvm.Value, len(schema.Fields))
	for idx, field := range schema.Fields {
		cell, provided := cellsByHeader[field.Name]
		if provided {
			cv, ok := c.structFieldConstValue(schema.Name, field.Name, field.Type, cell)
			if !ok {
				return false
			}
			constVals[idx] = cv
			continue
		}
		zv, ok := c.structFieldZeroValue(schema.Name, field.Name, field.Type)
		if !ok {
			return false
		}
		constVals[idx] = zv
	}

	structLLVM := c.mapToLLVMType(*schema)
	sym.Type = Ptr{Elem: *schema}
	sym.Val = c.makeGlobalConst(structLLVM, mangledName, llvm.ConstNamedStruct(structLLVM, constVals), linkage)
	return true
}

func (c *Compiler) compileConstStatement(stmt *ast.ConstStatement) {
	for i := 0; i < len(stmt.Name); i++ {
		c.compileConstBinding(stmt.Name[i].Value, stmt.Value[i])
	}
}

func (c *Compiler) compileStructStatement(stmt *ast.StructStatement) {
	c.compileConstBinding(stmt.Name.Value, stmt.Value)
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
		arrayType := symType.(Array)
		dimensions := make([]llvm.Value, arrayType.Rank)
		for i := range dimensions {
			dimensions[i] = c.ConstI64(0)
		}
		s.Val = c.createArrayValue(llvm.Value{}, dimensions, arrayType)
	case TableKind:
		tableType := symType.(Table)
		columns := make([]llvm.Value, len(tableType.Columns))
		for i := range columns {
			columns[i] = llvm.ConstPointerNull(llvm.PointerType(c.Context.Int8Type(), 0))
		}
		s.Val = c.createTableValue(c.ConstI64(0), columns, tableType)
	case RangeKind:
		s.Val = c.CreateRange(c.ConstI64(0), c.ConstI64(0), c.ConstI64(1), symType)
	case StructKind:
		structType := symType.(Struct)
		fieldVals := make([]llvm.Value, len(structType.Fields))
		for i, field := range structType.Fields {
			zv, ok := c.structFieldZeroValue(structType.Name, field.Name, field.Type)
			if !ok {
				return s
			}
			fieldVals[i] = zv
		}
		s.Val = llvm.ConstNamedStruct(c.mapToLLVMType(structType), fieldVals)
	default:
		panic(fmt.Sprintf("unsupported type for zero value: %s", symType.String()))
	}
	return s
}

// writeTo stores each slot's compiled value into its destination.
// It delegates per-destination ownership and in-place pointer-slot updates to storeValue.
func (c *Compiler) writeTo(slots []slotAssign) {
	for _, slot := range slots {
		c.storeValue(slot.dest.Value, slot.value, slot.needsCopy)
	}
}

// markCopyRequirements decides whether each slot's store must deep-copy or can
// transfer ownership, setting slot.needsCopy. Returns movedSources — the RHS
// variable names whose ownership was transferred.
func (c *Compiler) markCopyRequirements(slots []slotAssign) map[string]struct{} {
	movedSources := make(map[string]struct{})

	for i := range slots {
		slot := &slots[i]
		// StrG (static strings): immutable, live forever - no copy needed.
		if IsStrG(slot.value.Type) {
			continue
		}

		// Temporaries (array literals, function results, expressions): transfer ownership directly.
		// No copy needed - the temporary's memory becomes owned by the LHS variable.
		if slot.rhsName == "" {
			continue
		}

		// Named variable on RHS - default to copying for safety
		slot.needsCopy = true

		// Check if RHS variable is being overwritten in LHS (enables ownership transfer).
		// Only allow transfer if this source hasn't already been moved.
		// This prevents double-free in cases like: a, b = a, a
		// where the second use of 'a' must copy, not transfer.
		if _, moved := movedSources[slot.rhsName]; moved {
			continue
		}

		for _, other := range slots {
			if other.owner.Value != slot.rhsName {
				continue
			}
			slot.needsCopy = false
			movedSources[slot.rhsName] = struct{}{}
			break
		}
	}
	return movedSources
}

func (c *Compiler) coerceSymbolForType(sym *Symbol, target Type, loadName string) *Symbol {
	derefed := c.derefIfPointer(sym, loadName)

	if target.Kind() == StrKind && derefed.Type.Kind() == StrKind {
		if IsStrH(target) && IsStrG(derefed.Type) {
			return &Symbol{
				Val:  c.copyString(derefed.Val),
				Type: StrH{},
			}
		}
		return &Symbol{
			Val: derefed.Val,
			// Keep the actual string flavor unless we explicitly widened StrG -> StrH.
			// This avoids silently dropping ownership metadata if a stale slot type slips through.
			Type:     derefed.Type,
			FuncArg:  derefed.FuncArg,
			Borrowed: derefed.Borrowed,
			ReadOnly: derefed.ReadOnly,
		}
	}

	targetArray, targetIsArray := target.(Array)
	sourceArray, sourceIsArray := derefed.Type.(Array)
	if targetIsArray && sourceIsArray && sourceArray.ElemType.Kind() == EmptyKind && sourceArray.Rank == targetArray.Rank {
		return &Symbol{
			Val:      derefed.Val,
			Type:     targetArray,
			FuncArg:  derefed.FuncArg,
			Borrowed: derefed.Borrowed,
			ReadOnly: derefed.ReadOnly,
		}
	}
	if targetIsArray && sourceIsArray && sourceArray.Rank == 1 && sourceArray.ElemType.Kind() == EmptyKind {
		zero := c.makeZeroValue(targetArray)
		return &Symbol{
			Val:      zero.Val,
			Type:     targetArray,
			FuncArg:  derefed.FuncArg,
			Borrowed: derefed.Borrowed,
			ReadOnly: derefed.ReadOnly,
		}
	}

	targetTable, targetIsTable := target.(Table)
	sourceTable, sourceIsTable := derefed.Type.(Table)
	if targetIsTable && sourceIsTable && isHeaderOnlyTableType(sourceTable) && CanRefineType(sourceTable, targetTable) {
		return &Symbol{
			Val:      derefed.Val,
			Type:     targetTable,
			FuncArg:  derefed.FuncArg,
			Borrowed: derefed.Borrowed,
			ReadOnly: derefed.ReadOnly,
		}
	}

	return derefed
}

func (c *Compiler) markOutputSlotWritten(dst *Symbol, mergedFrom llvm.Value) {
	if dst.WriteFlag.IsNil() {
		return
	}
	// Moving a value produced by a stage that tracks the same logical output
	// is a merge, not a new write. The stage already set the flag only on the
	// runtime paths that yielded a value.
	if !mergedFrom.IsNil() && mergedFrom == dst.WriteFlag {
		return
	}
	c.builder.CreateStore(llvm.ConstInt(c.Context.Int1Type(), 1, false), dst.WriteFlag)
}

func (c *Compiler) storeSymbolToSlot(dst *Symbol, src *Symbol, target Type, loadName string) *Symbol {
	ptrType, ok := dst.Type.(Ptr)
	if !ok {
		panic("internal: storeSymbolToSlot requires pointer destination")
	}
	sourceWriteFlag := src.WriteFlag
	if target.Kind() != ptrType.Elem.Kind() {
		target = ptrType.Elem
	}

	source := c.derefIfPointer(src, loadName)
	targetArray, targetIsArray := target.(Array)
	sourceArray, sourceIsArray := source.Type.(Array)
	if targetIsArray && sourceIsArray && sourceArray.Rank == 1 && sourceArray.ElemType.Kind() == EmptyKind && targetArray.Rank > 1 {
		currentValue := c.createLoad(dst.Val, targetArray, "array_reset_shape")
		current := &Symbol{Val: currentValue, Type: targetArray}
		dimensions := c.arrayDimensions(current)
		dimensions[0] = c.ConstI64(0)
		source = &Symbol{
			Val:      c.createArrayValue(llvm.Value{}, dimensions, targetArray),
			Type:     targetArray,
			FuncArg:  source.FuncArg,
			Borrowed: source.Borrowed,
			ReadOnly: source.ReadOnly,
		}
	}

	coerced := c.coerceSymbolForType(source, target, "")
	c.createStore(coerced.Val, dst.Val, coerced.Type)
	c.markOutputSlotWritten(dst, sourceWriteFlag)
	return coerced
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
		targetType := c.bindingSlotType(name, valueToStore.Type)
		valueToStore = c.coerceSymbolForType(valueToStore, targetType, name+"_rhs_load")
		Put(c.Scopes, name, valueToStore)
		return
	}

	targetType := c.bindingSlotType(name, oldSym.Type.(Ptr).Elem)
	stored := c.storeSymbolToSlot(oldSym, valueToStore, targetType, name+"_rhs_load")
	ptrType := oldSym.Type.(Ptr)
	if !TypeEqual(ptrType.Elem, stored.Type) {
		updated := GetCopy(oldSym)
		updated.Type = Ptr{Elem: stored.Type}
		// oldSym came from Get above and nothing since touched the scope stack, so
		// SetExisting always finds the binding — and it must update it in its own
		// scope (which may be an outer block), not shadow it via Put in the current.
		SetExisting(c.Scopes, name, updated)
	}
}

// freeValue frees heap-allocated memory for the given value based on its type.
func (c *Compiler) freeValue(val llvm.Value, typ Type) {
	switch t := typ.(type) {
	case StrH:
		c.free([]llvm.Value{val})
	case StrG:
		// Static strings live forever, no free needed
	case Array:
		if hasConcreteArrayElemType(t.ElemType) {
			c.freeArray(c.arrayDataValue(val, t), t.ElemType)
		}
	case Table:
		for i, column := range t.Columns {
			if hasConcreteArrayElemType(column.ElemType) {
				c.freeArray(c.tableColumnValue(val, i), column.ElemType)
			}
		}
	case ArrayRange:
		// Call-only ArrayRange descriptors own a temporary backing array when
		// Borrowed is false. The range metadata itself has no heap storage.
		arrayVal := c.builder.CreateExtractValue(val, 0, "array_range_arr")
		c.freeValue(arrayVal, t.Array)
	}
}

func typeNeedsCleanup(typ Type) bool {
	switch t := typ.(type) {
	case Ptr:
		return typeNeedsCleanup(t.Elem)
	case StrH:
		return true
	case Array:
		return hasConcreteArrayElemType(t.ElemType)
	case Table:
		return true
	case ArrayRange:
		return true
	default:
		// Scalar values, including Range, do not own heap data.
		return false
	}
}

// freeSymbolValue frees a symbol's current value. If the symbol is Ptr-wrapped,
// loads the pointee first so the owned heap value is released.
func (c *Compiler) freeSymbolValue(sym *Symbol, loadName string) {
	if sym == nil || !typeNeedsCleanup(sym.Type) {
		return
	}
	derefed := c.derefIfPointer(sym, loadName)
	c.freeValue(derefed.Val, derefed.Type)
}

// slotAssign is one destination slot of an assignment: where the value is
// written, which identifier owns move/copy decisions (the real destination,
// even when writing through a temp), the compiled value and the RHS variable
// it came from ("" for a temporary), whether the store must deep-copy, the
// value it replaces, and whether the value is the destination's own storage.
type slotAssign struct {
	dest       *ast.Identifier
	owner      *ast.Identifier
	value      *Symbol
	rhsName    string
	needsCopy  bool
	oldValue   *Symbol
	destBacked bool
}

// exprAssign is one right-hand-side expression's assignment: its compiled
// destination slots and the bounds bit its evaluation recorded (nil = clean).
type exprAssign struct {
	expr  ast.Expression
	bit   llvm.Value
	slots []slotAssign
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
	assigns := c.compileExprAssigns(writeIdents, ownershipIdents, exprs)
	guarded := slices.ContainsFunc(assigns, func(e exprAssign) bool { return !e.bit.IsNil() })
	if !guarded {
		c.commitAssignments(assigns)
		return
	}
	c.commitAssignmentsPerExpr(assigns)
}

// compileExprAssigns captures old destination values, then evaluates each
// expression under its own bounds guard: a failed check is a failed condition
// on that expression's lanes only, so sibling expressions in the same
// statement still commit (a, b = oarr[10], 5 keeps a, sets b).
func (c *Compiler) compileExprAssigns(writeIdents []*ast.Identifier, ownershipIdents []*ast.Identifier, exprs []ast.Expression) []exprAssign {
	// Capture old values BEFORE compiling RHS expressions. This is critical
	// for function calls with Ptr outputs: by the time RHS compilation
	// finishes, Ptrs already contain NEW values.
	oldValues := c.captureOldValues(writeIdents)

	assigns := make([]exprAssign, 0, len(exprs))
	i := 0
	for _, expr := range exprs {
		guardPtr := c.pushBoundsGuard("assign_bounds_guard")
		res := c.compileExpression(expr, writeIdents[i:])
		var bit llvm.Value
		if c.stmtBoundsUsed() {
			bit = c.createLoad(guardPtr, Int{Width: 1}, "assign_bounds_ok")
		}
		c.popBoundsGuard()

		assigns = append(assigns, c.newExprAssign(expr, bit, res, writeIdents[i:], ownershipIdents[i:], oldValues[i:]))
		i += len(res)
	}
	return assigns
}

// newExprAssign zips one expression's compiled results with its destination
// slots; the ident and old-value slices start at the expression's first slot.
func (c *Compiler) newExprAssign(expr ast.Expression, bit llvm.Value, res []*Symbol, dests, owners []*ast.Identifier, olds []*Symbol) exprAssign {
	var rhsName string
	if ident, ok := expr.(*ast.Identifier); ok {
		rhsName = ident.Value
	}
	slots := make([]slotAssign, len(res))
	for j, sym := range res {
		slots[j] = slotAssign{
			dest:       dests[j],
			owner:      owners[j],
			value:      sym,
			rhsName:    rhsName,
			needsCopy:  sym.Borrowed,
			oldValue:   olds[j],
			destBacked: c.aliasesDestSlot(dests[j], sym),
		}
	}
	return exprAssign{expr: expr, bit: bit, slots: slots}
}

// aliasesDestSlot reports whether a compiled value is the destination's own
// storage: an indirect-return call writes through existing destination slots
// and returns them, so a skipped commit must not free through such a value —
// the slot still holds the old value the skip is keeping.
func (c *Compiler) aliasesDestSlot(ident *ast.Identifier, sym *Symbol) bool {
	if sym == nil || sym.Type.Kind() != PtrKind {
		return false
	}
	destSym, ok := Get(c.Scopes, ident.Value)
	return ok && destSym.Type.Kind() == PtrKind && destSym.Val == sym.Val
}

// commitAssignmentsPerExpr commits each expression's destinations under that
// expression's bounds bit (nil = unconditional), preserving the statement's
// simultaneous-assignment order: all writes land before any old value is
// freed, so a swap (a, b = b, a) never reads a freed payload. A skipped
// expression frees its temporaries and restores destinations its evaluation
// may have written through (call outputs), keeping prior values.
func (c *Compiler) commitAssignmentsPerExpr(assigns []exprAssign) {
	// Guarded destinations must be pointer-backed so the write and skip paths
	// converge; a fresh one gets a zero seed to keep. The seed is what a
	// commit replaces, so it becomes the slot's oldValue — the commit path
	// frees an owned heap seed (StrH zero is a live copy) instead of leaking
	// it, and the skip path keeps it.
	for _, e := range assigns {
		if e.bit.IsNil() {
			continue
		}
		for j := range e.slots {
			if seed := c.ensureSeededDest(e.slots[j].dest, e.slots[j].value); seed != nil {
				e.slots[j].oldValue = seed
			}
		}
	}

	moved := make([]map[string]struct{}, len(assigns))
	for k, e := range assigns {
		moved[k] = c.markCopyRequirements(e.slots)
		c.withCondBranch(e.bit, "assign_write", func() {
			c.writeTo(e.slots)
		}, func() {
			c.keepPriorOnSkip(e)
		})
	}

	for k, e := range assigns {
		c.withCondBranch(e.bit, "assign_free", func() {
			c.freeExprOldValues(e, moved[k])
		}, nil)
	}
}

// keepPriorOnSkip is the skip path for one expression's assignment (guard
// false or bounds failure): free the temporaries its evaluation produced and
// restore its destinations to their captured prior values, so the skipped
// assignment leaves everything as it was.
func (c *Compiler) keepPriorOnSkip(e exprAssign) {
	c.freeSkippedTemps(e)
	c.restoreOldValues(e.slots)
}

// freeSkippedTemps frees a skipped expression's temporaries, except values
// backed by destination storage: a skipped call never wrote those slots, so
// they still hold the old value the skip is keeping.
func (c *Compiler) freeSkippedTemps(e exprAssign) {
	if _, isIdent := e.expr.(*ast.Identifier); isIdent {
		return
	}
	for _, slot := range e.slots {
		if slot.destBacked {
			continue
		}
		c.freeTemporarySymbol(slot.value, "temp_free")
	}
}

// ensureSeededDest makes a guarded destination pointer-backed so conditional
// commit paths converge; a fresh destination gets a zero seed as its keep-old
// value. Returns the seed (nil for an existing destination) so the caller can
// record it as the value a commit replaces.
func (c *Compiler) ensureSeededDest(ident *ast.Identifier, valSym *Symbol) *Symbol {
	if _, exists := Get(c.Scopes, ident.Value); exists {
		c.promoteExistingSym(ident.Value)
		return nil
	}
	t := valSym.Type
	if p, ok := t.(Ptr); ok {
		t = p.Elem
	}
	t = c.bindingSlotType(ident.Value, t)
	ptr := c.createEntryBlockAlloca(c.mapToLLVMType(t), ident.Value+".mem")
	zero := c.makeZeroValue(t)
	c.createStore(zero.Val, ptr, t)
	Put(c.Scopes, ident.Value, &Symbol{Val: ptr, Type: Ptr{Elem: t}})
	return zero
}

func (c *Compiler) finishAssignmentsWithGuard(assigns []exprAssign, guardPtr llvm.Value) {
	if !c.stmtBoundsUsed() {
		c.commitAssignments(assigns)
		return
	}

	// Guarded assignments must converge through pointer-backed destinations so
	// runtime write/skip paths both feed subsequent reads correctly.
	for _, e := range assigns {
		for _, slot := range e.slots {
			c.promoteExistingSym(slot.dest.Value)
		}
	}
	c.withGuardedBranch(
		guardPtr,
		"stmt_bounds_ok",
		"stmt_bounds_write",
		"stmt_bounds_skip",
		"stmt_bounds_cont",
		func() {
			c.commitAssignments(assigns)
		},
		func() {
			for _, e := range assigns {
				c.keepPriorOnSkip(e)
			}
		},
	)
}

// commitAssignments commits a whole statement unconditionally. Copy/move
// decisions span the statement — the slots are marked as one group, so
// a, b = b, a moves both sides — and all writes land before any old value is
// freed, so a swap never reads a freed payload.
func (c *Compiler) commitAssignments(assigns []exprAssign) {
	var slots []slotAssign
	for _, e := range assigns {
		slots = append(slots, e.slots...)
	}

	movedSources := c.markCopyRequirements(slots)
	c.writeTo(slots)
	for _, e := range assigns {
		c.freeExprOldValues(e, movedSources)
	}
}

// freeExprOldValues frees the destination values one expression's commit
// replaced, skipping fresh destinations, moved sources, and borrowed storage.
// Function calls and ranged expressions return independent staged values, so
// their real destination cleanup also happens here.
func (c *Compiler) freeExprOldValues(e exprAssign, movedSources map[string]struct{}) {
	for _, slot := range e.slots {
		if slot.oldValue == nil || c.skipBorrowedOldValueFree(slot.oldValue) {
			continue
		}
		if _, isMoved := movedSources[slot.owner.Value]; isMoved {
			continue
		}
		c.freeSymbolValue(slot.oldValue, "old_assign")
	}
}

func (c *Compiler) promoteExistingSym(name string) {
	if _, exists := Get(c.Scopes, name); !exists {
		return
	}
	c.promoteToMemory(name)
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

func (c *Compiler) inArrayLiteralCellMode() bool {
	ctx := c.currentStmtCtx()
	return ctx != nil && ctx.arrayLitCellDepth > 0
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

// restoreOldValues writes captured destination values back after a skipped
// assignment path where RHS evaluation may have updated pointer-backed slots.
func (c *Compiler) restoreOldValues(slots []slotAssign) {
	for _, slot := range slots {
		if slot.oldValue == nil {
			continue
		}
		sym, exists := Get(c.Scopes, slot.dest.Value)
		if !exists {
			continue
		}
		if ptrType, ok := sym.Type.(Ptr); ok {
			c.createStore(slot.oldValue.Val, sym.Val, ptrType.Elem)
			continue
		}
		// Fallback for non-pointer symbols (defensive; guarded assignment paths
		// normally promote existing destinations before branching).
		Put(c.Scopes, slot.dest.Value, slot.oldValue)
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

	return false
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
		result[i] = c.valueSymbol(ident.Value, sym, ident.Value+"_old_load")
	}
	return result
}

func (c *Compiler) compileLetStatement(stmt *ast.LetStatement) {
	c.pushStmtCtx()
	defer c.popStmtCtx()

	// Ranged conditions must be checked before compileConditions so ranges
	// are not prematurely lowered into a single final boolean.
	if condRanges, condExprs := c.splitCondRanges(stmt.Condition); condRanges != nil {
		c.compileCondRangedStatement(stmt, condRanges, condExprs)
		return
	}

	cond, hasConditions := c.compileConditions(stmt)

	// Embedded conditional expressions (comparisons in value position)
	// take the most specialized path — they subsume statement conditions.
	if c.valuesHaveCondExpr(stmt.Value) {
		c.compileCondExprStatement(stmt, cond)
		return
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
		res = c.compileStringLiteralExpression(e, dest)
	case *ast.RangeLiteral:
		info := c.ExprCache[key(c.FuncNameMangled, e)]
		// Root bare range literals can be scalarized by an outer ranged context.
		// When all of this literal's ranges are already bound, compile the rewrite
		// iterator identifier instead of materializing a Range aggregate again.
		if rewIdent, ok := info.Rewrite.(*ast.Identifier); ok && len(c.pendingLoopRanges(info.Ranges)) == 0 {
			return []*Symbol{c.compileIdentifier(rewIdent)}
		}
		res = c.compileRangeExpression(e)
		return
	case *ast.ArrayLiteral:
		return c.compileArrayExpression(e, dest)
	case *ast.ArrayRangeExpression:
		return c.compileArrayRangeExpression(e, dest)
	case *ast.DotExpression:
		return c.compileDotExpression(e)
	case *ast.Identifier:
		res = c.compileIdentifierExpression(e, dest)
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
	case Array, Table, ArrayRange:
		inst.SetAlignment(8)
	case Struct:
		// Struct alignment follows max-field ABI alignment; Phase 1 fields are scalar/ptr.
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
		Val:       alloca,
		Type:      Ptr{Elem: s.Type},
		FuncArg:   s.FuncArg,
		Borrowed:  s.Borrowed,
		ReadOnly:  s.ReadOnly,
		WriteFlag: s.WriteFlag,
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

	if alias, ok := c.paramAliasFor(name, sym); ok {
		return c.promoteAlias(name, sym, alias)
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

func (c *Compiler) promoteAlias(name string, sym *Symbol, alias *paramAlias) *Symbol {
	paramPtr := c.createEntryBlockAlloca(c.mapToLLVMType(sym.Type), name)
	c.createStore(sym.Val, paramPtr, sym.Type)

	slotPtr := paramPtr
	if len(alias.OutputNames) > 0 {
		outputPtrs := make([]*Symbol, len(alias.OutputNames))
		for i, outputName := range alias.OutputNames {
			outputSym, _ := Get(c.Scopes, outputName)
			if outputSym.Type.Kind() != PtrKind {
				// Only params carry alias bindings, so promoting an output here
				// cannot recurse through another param-alias entry.
				outputSym = c.promoteToMemory(outputName)
			}
			outputPtrs[i] = outputSym
		}
		slotPtr = c.selectAliasedParamPtr(name, paramPtr, alias.AliasIndex, outputPtrs)
	}

	ptr := &Symbol{
		Val:       slotPtr,
		Type:      Ptr{Elem: sym.Type},
		FuncArg:   sym.FuncArg,
		Borrowed:  sym.Borrowed,
		ReadOnly:  sym.ReadOnly,
		WriteFlag: sym.WriteFlag,
	}
	Put(c.Scopes, name, ptr)
	c.clearParamAlias(name)
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
	// A write flag belongs to the storage slot, not to an ordinary value read.
	newS.WriteFlag = llvm.Value{}
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

// compileStringLiteral compiles a string literal.
// Strings with format markers are heap-allocated; other literals stay static.
func (c *Compiler) compileStringLiteral(tok token.Token) *Symbol {
	formatted, args, toFree := c.formatString(tok, tok.Literal)

	// No markers
	if len(args) == 0 {
		globalName := fmt.Sprintf("str_literal_%d", c.formatCounter)
		c.formatCounter++
		globalPtr := c.createGlobalString(globalName, lexer.DecodeStringLiteral(tok.Literal), llvm.PrivateLinkage)
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
	s, source := c.lookupNamedSymbol(ident.Value)
	if source == symbolLocal {
		return c.valueSymbol(ident.Value, s, ident.Value+"_load")
	}
	return c.derefIfPointer(s, ident.Value+"_load")
}

// compileStringLiteralExpression lowers formatting markers that reference
// named Range drivers once per yield and retains the final formatted string.
func (c *Compiler) compileStringLiteralExpression(lit *ast.StringLiteral, dest []*ast.Identifier) []*Symbol {
	info := c.ExprCache[key(c.FuncNameMangled, lit)]
	if info == nil || len(c.pendingLoopRanges(info.Ranges)) == 0 {
		return []*Symbol{c.compileStringLiteral(lit.Token)}
	}

	PushScope(&c.Scopes, BlockScope)
	defer c.popScope()

	outputs := c.makeSeededTempOutputs(dest, info.OutTypes)
	c.bindRangedTempOutputs(dest, outputs)
	output := outputs[0]
	c.withLoopNest(info.Ranges, func() {
		value := c.compileStringLiteral(lit.Token)
		c.storeRangedOutput(output, value.Val, value.Type)
	})

	return c.loadOutputValues(outputs, "format_range_final")
}

// compileIdentifierExpression closes a bare named Range driver to its final
// yielded iterator value. When an outer loop has already shadowed the Range
// with a scalar, the identifier compiles directly.
func (c *Compiler) compileIdentifierExpression(ident *ast.Identifier, dest []*ast.Identifier) []*Symbol {
	info := c.ExprCache[key(c.FuncNameMangled, ident)]
	if info == nil || len(c.pendingLoopRanges(info.Ranges)) == 0 {
		return []*Symbol{c.compileIdentifier(ident)}
	}

	PushScope(&c.Scopes, BlockScope)
	defer c.popScope()

	outputs := c.makeSeededTempOutputs(dest, info.OutTypes)
	c.bindRangedTempOutputs(dest, outputs)
	output := outputs[0]
	c.withLoopNest(info.Ranges, func() {
		value := c.compileIdentifier(ident)
		c.storeRangedOutput(output, value.Val, value.Type)
	})

	return c.loadOutputValues(outputs, "range_final")
}

func (c *Compiler) compileDotExpression(expr *ast.DotExpression) []*Symbol {
	leftSym := c.compileExpression(expr.Left, nil)[0]
	leftSym = c.derefIfPointer(leftSym, "dot_left")

	switch leftType := leftSym.Type.(type) {
	case Struct:
		for i, field := range leftType.Fields {
			if field.Name == expr.Field {
				return []*Symbol{{
					Type: field.Type,
					Val:  c.builder.CreateExtractValue(leftSym.Val, i, expr.Field),
				}}
			}
		}
		c.Errors = append(c.Errors, &token.CompileError{
			Token: expr.Token,
			Msg:   fmt.Sprintf("unknown struct field %q on %s", expr.Field, leftType.Name),
		})
	case Table:
		for i, column := range leftType.Columns {
			if column.Name != expr.Field {
				continue
			}

			columnType := Array{ElemType: column.ElemType, Rank: 1}
			if info := c.ExprCache[key(c.FuncNameMangled, expr)]; info != nil && len(info.OutTypes) > 0 {
				if resolved, ok := info.OutTypes[0].(Array); ok {
					columnType = resolved
				}
			}

			columnValue := c.tableColumnValue(leftSym.Val, i)
			if hasConcreteArrayElemType(column.ElemType) {
				columnValue = c.copyArray(columnValue, column.ElemType)
			}
			c.freeConsumedTemporary(expr.Left, []*Symbol{leftSym})

			return []*Symbol{{
				Type: columnType,
				Val:  columnValue,
			}}
		}
		c.Errors = append(c.Errors, &token.CompileError{
			Token: expr.Token,
			Msg:   fmt.Sprintf("unknown table column %q on %s", expr.Field, leftType.String()),
		})
	default:
		c.Errors = append(c.Errors, &token.CompileError{
			Token: expr.Token,
			Msg:   fmt.Sprintf("field access expects a struct or table value, got %s", leftSym.Type.String()),
		})
	}
	return []*Symbol{{Type: I64, Val: c.ConstI64(0)}}
}

// getRawSymbol looks up a symbol by name without dereferencing pointers.
// If it is a PtrKind, returns alloca and Type will be PtrKind.
func (c *Compiler) getRawSymbol(name string) (*Symbol, bool) {
	s, source := c.lookupNamedSymbol(name)
	return s, source != symbolMissing
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
		// Use rewritten expression if ranges were consumed by an outer loop.
		if rew, ok := info.Rewrite.(*ast.InfixExpression); ok {
			expr = rew
			info = c.ExprCache[key(c.FuncNameMangled, expr)]
		}
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
		if expArr, ok := expected.(Array); ok {
			elem = expArr.ElemType
		} else if l.Type.Kind() == ArrayKind {
			elem = l.Type.(Array).ElemType
		} else {
			elem = r.Type.(Array).ElemType
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
			res = append(res, c.compileArrayMask(expr.Operator, left[i], right[i], info.OutTypes[i]))
		case CondScalar:
			// Usually pre-extracted via condLHS, but can still occur when range
			// comparisons are scalarized by an outer loop (e.g. call arg vectorization).
			res = append(res, c.compileCondScalar(expr.Operator, left[i], right[i]))
		case CondOr, CondAnd:
			panic("internal: value-position logical OR/AND must be lowered through conditional expression branching")
		case CondNone:
			res = append(res, c.compileInfix(expr.Operator, left[i], right[i], info.OutTypes[i]))
		default:
			// A new CondMode must be classified here deliberately: falling
			// through would lower a conditional slot as plain arithmetic.
			panic(fmt.Sprintf("internal: unhandled CondMode %d in infix lowering", mode))
		}
	}

	// Free temporary array operands (literals used in expressions)
	// Variables are not freed here - they're managed by scope cleanup
	c.freeTemporary(expr.Left, left)
	c.freeTemporary(expr.Right, right)

	return res
}

// compileShortCircuitAnd emits a lazy i1 conjunction. compileRight is invoked
// with the builder in the true branch, so its instructions execute only when
// left is true.
func (c *Compiler) compileShortCircuitAnd(left llvm.Value, compileRight func() llvm.Value, name string) llvm.Value {
	rhsBlock, falseBlock, contBlock := c.createIfElseCont(left, name+"_rhs", name+"_false", name+"_cont")

	c.builder.SetInsertPointAtEnd(rhsBlock)
	right := compileRight()
	c.builder.CreateBr(contBlock)
	rhsEnd := c.builder.GetInsertBlock()

	c.builder.SetInsertPointAtEnd(falseBlock)
	c.builder.CreateBr(contBlock)
	falseEnd := c.builder.GetInsertBlock()

	c.builder.SetInsertPointAtEnd(contBlock)
	result := c.builder.CreatePHI(c.Context.Int1Type(), name)
	result.AddIncoming(
		[]llvm.Value{right, llvm.ConstInt(c.Context.Int1Type(), 0, false)},
		[]llvm.BasicBlock{rhsEnd, falseEnd},
	)
	return result
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

// freeConsumedTemporary frees expr's temporaries on the evaluation's straight
// line and records the free against expr's condLHS frame entry, so the
// extraction cleanups — the unmoved-mask sweep and the retained-temp
// releases, which track these same symbols — skip the released payload. Only
// valid where the free dominates the extraction's branch arms: a free inside
// one arm of a runtime branch (a collector's cell push, a per-iteration
// release) must use freeTemporary, or the compile-time mark would poison the
// other arm.
func (c *Compiler) freeConsumedTemporary(expr ast.Expression, syms []*Symbol) {
	c.freeTemporary(expr, syms)
	c.markFreedFrameValues(expr, syms)
}

// markFreedFrameValues marks expr's frame-stashed values borrowed after a
// consumer freed them. Only slots whose value identity matches what was freed
// are marked: a consumer freeing a value merely derived from a frame slot (a
// cond_lhs phi, an LHS-or-zero select) did not release the retained payload.
func (c *Compiler) markFreedFrameValues(expr ast.Expression, syms []*Symbol) {
	frame := c.currentCondLHSFrame()
	if frame == nil {
		return
	}
	lhsSyms, ok := frame[key(c.FuncNameMangled, expr)]
	if !ok {
		return
	}
	for _, freed := range syms {
		for _, sym := range lhsSyms {
			if sym.Val == freed.Val {
				sym.Borrowed = true
			}
		}
	}
}

// freeTemporarySymbol frees one temporary symbol if it owns heap data.
func (c *Compiler) freeTemporarySymbol(sym *Symbol, loadName string) {
	if sym.Borrowed || !typeNeedsCleanup(sym.Type) {
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

func (c *Compiler) CreateArrayRange(arrayVal, rangeVal llvm.Value, typ ArrayRange) llvm.Value {
	llvmType := c.mapToLLVMType(typ)
	agg := llvm.Undef(llvmType)
	agg = c.builder.CreateInsertValue(agg, arrayVal, 0, "array_range_arr")
	agg = c.builder.CreateInsertValue(agg, rangeVal, 1, "array_range_rng")
	return agg
}

// compileInfixRanges evaluates an infix expression once per driver yield and
// retains one loop-carried result per destination.
func (c *Compiler) compileInfixRanges(expr *ast.InfixExpression, info *ExprInfo, dest []*ast.Identifier) (res []*Symbol) {
	PushScope(&c.Scopes, BlockScope)
	defer c.popScope()

	// Setup outputs to store values across iterations.
	// Mark as borrowed so cleanupScope skips them - values are returned via out.
	outputs := c.makeSeededTempOutputs(dest, info.OutTypes)
	c.bindRangedTempOutputs(dest, outputs)

	rew := info.Rewrite.(*ast.InfixExpression)
	withCollectorPreparedLoopNest(c, rew, info.Ranges, nil, nil, func(prepared *ast.InfixExpression) {
		leftRew := prepared.Left
		rightRew := prepared.Right
		_, leftIsIdent := leftRew.(*ast.Identifier)
		// CondScalar makes left-temp ownership branch-dependent (store on true,
		// drop on false), so handle left temp cleanup inline per slot.
		leftTempsHandledInline := info.HasCondScalar() && !leftIsIdent

		c.pushBoundsGuard("infix_iter_bounds_guard")
		defer c.popBoundsGuard()

		c.compileCondOperands(prepared, llvm.Value{}, func() {
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
	})

	return c.loadOutputValues(outputs, "final")
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
	var onSkip func()
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
		c.storeSymbolToSlot(output, computed, output.Type.(Ptr).Elem, "range_infix_store")
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
	toStore := lSym
	if !leftTempsHandledInline {
		// Identifier LHS values are borrowed from live bindings. Copy before
		// releasing the loop-carried output, especially for self-reference
		// where both values can currently point at the same payload.
		toStore = c.deepCopyIfNeeded(lSym)
	}
	c.freeSymbolValue(output, "old_output")
	c.storeSymbolToSlot(output, toStore, output.Type.(Ptr).Elem, "range_cond_store")
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
		if !hasConcreteArrayElemType(t.ElemType) {
			sym.Type = resolved
			Put(c.Scopes, name, sym)
		}
	case Table:
		if !IsFullyResolvedType(t) {
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

func (c *Compiler) makeTempOutput(name string, outType Type, borrowed bool, seed *Symbol) *Symbol {
	ptr := c.createEntryBlockAlloca(c.mapToLLVMType(outType), name)
	ptrElem := outType
	if seed == nil {
		// Zero initialization preserves empty-range no-op behavior for fresh slots.
		// Use the zero value's type to keep string Static flags accurate.
		seed = c.makeZeroValue(outType)
		ptrElem = seed.Type
	}

	output := &Symbol{
		Val:      ptr,
		Type:     Ptr{Elem: ptrElem},
		Borrowed: borrowed,
	}
	c.storeSymbolToSlot(output, seed, outType, name+"_seed")
	return output
}

// makeSeededTempOutputs creates independent result slots. Existing destination
// values seed output-parameter and empty-range behavior, but heap-backed seeds
// are copied so evaluating one RHS cannot mutate or free a destination before
// sibling RHS expressions have read the statement's original values.
func (c *Compiler) makeSeededTempOutputs(dest []*ast.Identifier, outTypes []Type) []*Symbol {
	resolved := c.resolvedDestTypes(dest, outTypes)
	outputs := make([]*Symbol, len(resolved))
	for i, outType := range resolved {
		name := fmt.Sprintf("calltmp_%d", c.tmpCounter)
		c.tmpCounter++

		var existing *Symbol
		var exists bool
		if dest != nil && i < len(dest) {
			existing, exists = Get(c.Scopes, dest[i].Value)
		}

		var seed *Symbol
		if exists {
			seed = c.deepCopyIfNeeded(c.resolveDestSeed(dest[i], outType))
		} else {
			seed = c.makeZeroValue(outType)
		}
		outputs[i] = c.makeTempOutput(name, outType, true, seed)
		if exists {
			outputs[i].WriteFlag = existing.WriteFlag
		}
	}
	return outputs
}

type callOutputAdapter struct {
	abiOutput *Symbol
	bridged   bool
}

// makeCallOutputAdapters adapts destination-typed result slots to an indirect
// callee's declared output types. Matching slots pass through directly. A
// mismatched slot gets an ABI-typed zero seed; the callee's explicit per-output
// write flag decides whether that value commits into the destination-typed
// stage. This prevents one static ownership/shape flavor from masquerading as
// another inside the callee.
func (c *Compiler) makeCallOutputAdapters(running []*Symbol, outTypes []Type) []callOutputAdapter {
	adapters := make([]callOutputAdapter, len(running))
	for i, output := range running {
		targetType := output.Type.(Ptr).Elem
		if TypeEqual(targetType, outTypes[i]) {
			adapters[i].abiOutput = output
			continue
		}

		name := fmt.Sprintf("calladapter_%d", c.tmpCounter)
		c.tmpCounter++
		adapters[i].abiOutput = c.makeTempOutput(name, outTypes[i], true, nil)
		adapters[i].bridged = true
	}
	return adapters
}

func callAdapterOutputs(adapters []callOutputAdapter) []*Symbol {
	outputs := make([]*Symbol, len(adapters))
	for i := range adapters {
		outputs[i] = adapters[i].abiOutput
	}
	return outputs
}

func (c *Compiler) propagateOutputWriteFlag(output *Symbol, didWrite llvm.Value, index int) {
	if output.WriteFlag.IsNil() {
		return
	}
	previous := c.builder.CreateLoad(c.Context.Int1Type(), output.WriteFlag, fmt.Sprintf("output_written_%d", index))
	merged := c.builder.CreateOr(previous, didWrite, fmt.Sprintf("output_written_merge_%d", index))
	c.builder.CreateStore(merged, output.WriteFlag)
}

// commitCallOutputAdapters commits ABI-flavor temporaries into the independent
// result slots after (and only after) a call executes. Store/coercion happens
// before old-value cleanup so shape resets and StrG -> StrH copies can inspect
// the previous destination safely.
func (c *Compiler) commitCallOutputAdapters(running []*Symbol, adapters []callOutputAdapter, writeFlags []llvm.Value) {
	for i, adapter := range adapters {
		didWrite := c.builder.CreateLoad(c.Context.Int1Type(), writeFlags[i], fmt.Sprintf("call_output_written_%d", i))
		if !adapter.bridged {
			c.propagateOutputWriteFlag(running[i], didWrite, i)
			continue
		}

		commit := func() {
			targetType := running[i].Type.(Ptr).Elem
			oldValue := c.derefIfPointer(running[i], fmt.Sprintf("calladapter_old_%d", i))
			source := c.derefIfPointer(adapter.abiOutput, fmt.Sprintf("calladapter_result_%d", i))
			stored := c.storeSymbolToSlot(running[i], source, targetType, fmt.Sprintf("calladapter_store_%d", i))
			c.freeSymbolValue(oldValue, "")
			if stored.Val != source.Val {
				c.freeSymbolValue(source, "")
			}
		}

		c.withCondBranch(didWrite, "calladapter", commit, func() {
			c.freeSymbolValue(adapter.abiOutput, fmt.Sprintf("calladapter_unwritten_%d", i))
		})
	}
}

func (c *Compiler) cleanupSkippedCallOutputAdapters(adapters []callOutputAdapter) {
	for i, adapter := range adapters {
		if adapter.bridged {
			c.freeSymbolValue(adapter.abiOutput, fmt.Sprintf("calladapter_skip_%d", i))
		}
	}
}

// bindRangedTempOutputs makes each destination name resolve to its staged slot
// while that one ranged expression is compiled. Conditional lowering can make
// the real destination and a condtmp_* write name alias the same slot, so bind
// every visible name for that slot as well. This preserves loop-carried
// self-reference (res = res + i) without exposing the staged value to sibling
// right-hand sides in a simultaneous assignment; the caller's BlockScope is
// popped before the next expression is compiled.
func (c *Compiler) bindRangedTempOutputs(dest []*ast.Identifier, outputs []*Symbol) {
	for i := 0; i < len(dest) && i < len(outputs); i++ {
		names := []string{dest[i].Value}
		if current, ok := Get(c.Scopes, dest[i].Value); ok && current.Type.Kind() == PtrKind {
			seen := make(map[string]struct{})
			for scopeIdx := len(c.Scopes) - 1; scopeIdx >= 0; scopeIdx-- {
				scope := c.Scopes[scopeIdx]
				for name, sym := range scope.Elems {
					if _, visited := seen[name]; visited {
						continue
					}
					seen[name] = struct{}{}
					if sym.Type.Kind() == PtrKind && sym.Val == current.Val {
						names = append(names, name)
					}
				}
				if scope.ScopeKind == FuncScope {
					break
				}
			}
		}
		for _, name := range names {
			Put(c.Scopes, name, outputs[i])
		}
	}
}

// Destination-aware prefix compilation,
// mirroring compileInfixExpression/compileInfixRanges.

func (c *Compiler) compilePrefixExpression(expr *ast.PrefixExpression, dest []*ast.Identifier) (res []*Symbol) {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	// Filter out ranges that are already bound (converted to scalar iterators in outer loops)
	pending := c.pendingLoopRanges(info.Ranges)
	// If the result is an array, let the operand handle any collection itself.
	if len(pending) == 0 {
		// Use rewritten expression if ranges were consumed by an outer loop.
		if rew, ok := info.Rewrite.(*ast.PrefixExpression); ok {
			expr = rew
			info = c.ExprCache[key(c.FuncNameMangled, expr)]
		}
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
	outputs := c.makeSeededTempOutputs(dest, info.OutTypes)
	c.bindRangedTempOutputs(dest, outputs)

	withCollectorPreparedLoopNest(c, info.Rewrite.(*ast.PrefixExpression), info.Ranges, nil, nil, func(prepared *ast.PrefixExpression) {
		rightRew := prepared.Right

		c.pushBoundsGuard("prefix_iter_bounds_guard")
		defer c.popBoundsGuard()

		c.compileCondOperands(prepared, llvm.Value{}, func() {
			ops := c.compileExpression(rightRew, nil)

			for i := 0; i < len(ops); i++ {
				c.compileRangePrefixSlot(expr.Operator, ops[i], info.OutTypes[i], outputs[i])
			}

			// Range-loop operand is temporary per iteration (except identifiers).
			c.freeTemporary(rightRew, ops)
		})
	})

	return c.loadOutputValues(outputs, "final")
}

func (c *Compiler) compileRangePrefixSlot(op string, operand *Symbol, expected Type, output *Symbol) {
	run := func() {
		computed := c.compilePrefix(op, operand, expected)

		// Free previous iteration's result before overwriting
		c.freeSymbolValue(output, "old_output")
		c.storeSymbolToSlot(output, computed, output.Type.(Ptr).Elem, "range_prefix_store")
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
	fields := make([]llvm.Type, len(outputTypes)*2)
	for i, t := range outputTypes {
		fields[i] = llvm.PointerType(c.mapToLLVMType(t), 0)
		fields[len(outputTypes)+i] = llvm.PointerType(c.Context.Int1Type(), 0)
	}
	st.StructSetBody(fields, false)
	return st
}

func (c *Compiler) getFuncType(mangled string, abi FuncABI) (llvm.Type, llvm.Type) {
	llvmParams := make([]llvm.Type, 0, len(abi.Params)+1)
	retType := c.Context.VoidType()
	retStruct := llvm.Type{}

	if abi.UsesIndirectReturn() {
		retStruct = c.getReturnStruct(mangled, abi.Return.OutTypes)
		llvmParams = append(llvmParams, llvm.PointerType(retStruct, 0))
	} else {
		retType = c.mapToLLVMType(abi.Return.DirectType)
	}

	for _, param := range abi.Params {
		llvmParams = append(llvmParams, c.mapToLLVMType(param.Lowered))
	}
	for i := 0; i < abi.NumAliasSlots(); i++ {
		llvmParams = append(llvmParams, c.Context.Int32Type())
	}
	if abi.Return.HasSeedParam {
		llvmParams = append(llvmParams, c.mapToLLVMType(abi.Return.DirectType))
	}

	return llvm.FunctionType(retType, llvmParams, false), retStruct
}

func (c *Compiler) addEnumAttribute(function llvm.Value, index int, name string) {
	attr := c.Context.CreateEnumAttribute(llvm.AttributeKindID(name), 0)
	function.AddAttributeAtIndex(index, attr)
}

func (c *Compiler) addStringAttribute(function llvm.Value, index int, name string, value string) {
	attr := c.Context.CreateStringAttribute(name, value)
	function.AddAttributeAtIndex(index, attr)
}

func (c *Compiler) addNoundefAttribute(function llvm.Value, index int) {
	c.addEnumAttribute(function, index, "noundef")
}

func (c *Compiler) addPointerParamAttributes(function llvm.Value, index int) {
	c.addNoundefAttribute(function, index)
	c.addEnumAttribute(function, index, "nonnull")
	c.addStringAttribute(function, index, "captures", "none")
}

func (c *Compiler) compileFunc(template *ast.FuncStatement, sig *callSignature, funcType llvm.Type, retStruct llvm.Type) llvm.Value {
	function := llvm.AddFunction(c.Module, sig.Mangled, funcType)

	if sig.ABI.UsesIndirectReturn() {
		sretAttr := c.Context.CreateTypeAttribute(llvm.AttributeKindID("sret"), retStruct)
		function.AddAttributeAtIndex(1, sretAttr) // Index 1 is the first parameter
		c.addPointerParamAttributes(function, 1)
	} else {
		c.addNoundefAttribute(function, 0)
	}

	for i, paramABI := range sig.ABI.Params {
		paramIndex := sig.ABI.SourceFunctionParamIndex(i) + 1
		if paramABI.Mode == ABIParamDirect {
			c.addNoundefAttribute(function, paramIndex)
			continue
		}
		c.addPointerParamAttributes(function, paramIndex)
	}

	for i := 0; i < sig.ABI.NumAliasSlots(); i++ {
		c.addNoundefAttribute(function, sig.ABI.AliasParamBaseIndex()+i+1)
	}
	if seedParamIndex := sig.ABI.DirectReturnSeedParamIndex(); seedParamIndex >= 0 {
		c.addNoundefAttribute(function, seedParamIndex+1)
	}

	// Create entry block
	entry := c.Context.AddBasicBlock(function, "entry")
	savedBlock := c.builder.GetInsertBlock()
	c.builder.SetInsertPointAtEnd(entry)

	// Set FuncNameMangled so ExprCache entries are keyed to this function
	savedFuncNameMangled := c.FuncNameMangled
	c.FuncNameMangled = sig.Mangled
	c.pushParamAliases()
	retVal, hasDirectRet := c.compileFuncBlock(template, sig, retStruct, function)
	c.popParamAliases()
	c.FuncNameMangled = savedFuncNameMangled

	if hasDirectRet {
		c.builder.CreateRet(retVal)
	} else {
		c.builder.CreateRetVoid()
	}

	// Restore the builder to where it was before compiling this function
	if !savedBlock.IsNil() {
		c.builder.SetInsertPointAtEnd(savedBlock)
	}

	return function
}

func (c *Compiler) processIndirectOutputs(fn *ast.FuncStatement, retStruct llvm.Type, sretPtr llvm.Value, finalOutTypes []Type) []*Symbol {
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
		flagField := c.builder.CreateStructGEP(retStruct, sretPtr, len(fn.Outputs)+i, outIdent.Value+"_written_field")
		flagPtrType := llvm.PointerType(c.Context.Int1Type(), 0)
		retPtrs[i].WriteFlag = c.builder.CreateLoad(flagPtrType, flagField, outIdent.Value+"_written")
	}
	return retPtrs
}

func (c *Compiler) directOutputSeed(index int, outType Type, sig *callSignature, function llvm.Value) *Symbol {
	seedParamIndex := sig.ABI.DirectReturnSeedParamIndex()
	if index != 0 || seedParamIndex < 0 {
		return c.makeZeroValue(outType)
	}

	return &Symbol{
		Val:      function.Param(seedParamIndex),
		Type:     sig.ABI.Return.DirectType,
		FuncArg:  true,
		Borrowed: true,
		ReadOnly: true,
	}
}

func (c *Compiler) processDirectOutputValues(fn *ast.FuncStatement, sig *callSignature, function llvm.Value) []*Symbol {
	outputs := make([]*Symbol, len(fn.Outputs))
	for i := range fn.Outputs {
		output := GetCopy(c.directOutputSeed(i, sig.FnInfo.OutTypes[i], sig, function))
		output.FuncArg = true
		output.Borrowed = false
		output.ReadOnly = false
		outputs[i] = output
	}
	return outputs
}

func (c *Compiler) bindFuncOutputs(fn *ast.FuncStatement, outputs []*Symbol) {
	for i, outIdent := range fn.Outputs {
		Put(c.Scopes, outIdent.Value, outputs[i])
	}
}

func (c *Compiler) processParams(template *ast.FuncStatement, sig *callSignature, function llvm.Value, outputNames []string) ([]*Symbol, []int) {
	inputs := make([]*Symbol, len(sig.ParamTypes))
	iterIndices := []int{}

	for i, param := range template.Parameters {
		name := param.Value
		elemType := sig.ParamTypes[i]
		paramVal := function.Param(sig.ABI.SourceFunctionParamIndex(i))

		if sig.ABI.Params[i].Mode == ABIParamDirect {
			inputs[i] = &Symbol{
				Val:      paramVal,
				Type:     elemType,
				FuncArg:  true,
				ReadOnly: true,
			}
			if aliasParamIndex := sig.ABI.AliasFunctionParamIndex(i); aliasParamIndex >= 0 {
				c.bindParamAlias(name, inputs[i], function.Param(aliasParamIndex), outputNames)
			}
		} else {
			inputs[i] = &Symbol{
				Val:      paramVal,
				Type:     Ptr{Elem: elemType},
				FuncArg:  true,
				Borrowed: true,
				ReadOnly: true,
			}
		}

		if elemType.Kind() == RangeKind || elemType.Kind() == ArrayRangeKind {
			iterIndices = append(iterIndices, i)
			continue
		}
		// Put non-iterator params in scope
		Put(c.Scopes, name, inputs[i])
	}
	return inputs, iterIndices
}

func (c *Compiler) compileFuncIter(template *ast.FuncStatement, inputs []*Symbol, iterIndices []int, currentOutput *Symbol) *Symbol {
	fa := &FuncArgs{
		Inputs:      inputs,
		IterIndices: iterIndices,
	}
	return c.funcLoopNest(template, fa, 0, currentOutput)
}

func (c *Compiler) compileFuncBlock(template *ast.FuncStatement, sig *callSignature, retStruct llvm.Type, function llvm.Value) (llvm.Value, bool) {
	PushScope(&c.Scopes, FuncScope)
	defer c.popScope()

	outputNames := identNames(template.Outputs)
	inputs, iterIndices := c.processParams(template, sig, function, outputNames)

	var outputs []*Symbol
	if sig.ABI.UsesIndirectReturn() {
		sretPtr := function.Param(0)
		outputs = c.processIndirectOutputs(template, retStruct, sretPtr, sig.FnInfo.OutTypes)
	} else {
		outputs = c.processDirectOutputValues(template, sig, function)
	}
	c.bindFuncOutputs(template, outputs)

	if len(iterIndices) == 0 {
		c.compileBlockWithArgs(template, nil)
	} else if sig.ABI.Return.Mode == ABIReturnDirect {
		// ABIReturnDirect is currently a single scalar output, so the seeded
		// binding above becomes the initial loop-carried SSA state here.
		finalOutput := c.compileFuncIter(template, inputs, iterIndices, outputs[0])
		// Rebind the function output name to the final loop-carried SSA value.
		Put(c.Scopes, template.Outputs[0].Value, finalOutput)
	} else {
		c.compileFuncIter(template, inputs, iterIndices, nil)
	}

	if sig.ABI.Return.Mode == ABIReturnDirect {
		retSym, _ := c.localValSymbol(template.Outputs[0].Value, template.Outputs[0].Value+"_ret")
		return retSym.Val, true
	}

	return llvm.Value{}, false
}

func (c *Compiler) iterOverRange(rangeType Range, rangeVal llvm.Value, body func(llvm.Value, Type)) {
	c.iterOverRangeState(rangeType, rangeVal, nil, func(iter llvm.Value, iterType Type, _ *Symbol) *Symbol {
		body(iter, iterType)
		return nil
	})
}

func (c *Compiler) iterOverRangeState(rangeType Range, rangeVal llvm.Value, currentOutput *Symbol, body func(llvm.Value, Type, *Symbol) *Symbol) *Symbol {
	iterType := rangeType.Iter
	hasState := currentOutput != nil
	stateType := llvm.Type{}
	seed := llvm.Value{}
	if hasState {
		stateType = c.mapToLLVMType(currentOutput.Type)
		seed = currentOutput.Val
	}

	finalVal := c.createLoopCore(rangeVal, seed, stateType, hasState, func(iter llvm.Value, current llvm.Value) llvm.Value {
		var state *Symbol
		if hasState {
			state = GetCopy(currentOutput)
			state.Val = current
		}
		next := body(iter, iterType, state)
		if !hasState {
			return llvm.Value{}
		}
		return next.Val
	})

	if !hasState {
		return nil
	}
	finalOutput := GetCopy(currentOutput)
	finalOutput.Val = finalVal
	return finalOutput
}

func (c *Compiler) iterOverArrayRangeState(arrRangeSym *Symbol, currentOutput *Symbol, body func(llvm.Value, Type, *Symbol) *Symbol) *Symbol {
	arrRangeType := arrRangeSym.Type.(ArrayRange)
	arrayVal := c.builder.CreateExtractValue(arrRangeSym.Val, 0, "array_range_arr")
	rangeVal := c.builder.CreateExtractValue(arrRangeSym.Val, 1, "array_range_bounds")
	arraySym := &Symbol{
		Val:      arrayVal,
		Type:     arrRangeType.Array,
		Borrowed: true,
		ReadOnly: true,
	}
	resultType := arrayIndexResultType(arrRangeType.Array)

	hasState := currentOutput != nil
	stateType := llvm.Type{}
	seed := llvm.Value{}
	if hasState {
		stateType = c.mapToLLVMType(currentOutput.Type)
		seed = currentOutput.Val
	}

	finalVal := c.createLoopCore(rangeVal, seed, stateType, hasState, func(iter llvm.Value, current llvm.Value) llvm.Value {
		inBounds := c.arrayIndexInBounds(arraySym, arrRangeType.Array.ElemType, iter)
		preCheck := c.builder.GetInsertBlock()
		iterBlock, contBlock := c.createIfCont(inBounds, "arr_iter_in_bounds", "arr_iter_cont")

		c.builder.SetInsertPointAtEnd(iterBlock)
		var yielded *Symbol
		if arrRangeType.Array.Rank == 1 {
			yielded = &Symbol{
				Val:      c.ArrayGetBorrowed(arraySym, arrRangeType.Array.ElemType, iter),
				Type:     resultType,
				Borrowed: true,
				ReadOnly: true,
			}
		} else {
			yielded = c.compileArraySubarray(arraySym, iter)
		}

		var state *Symbol
		if hasState {
			state = GetCopy(currentOutput)
			state.Val = current
		}
		next := body(yielded.Val, yielded.Type, state)
		if arrRangeType.Array.Rank > 1 {
			c.freeSymbolValue(yielded, "array_range_subarray")
		}
		c.builder.CreateBr(contBlock)
		iterEnd := c.builder.GetInsertBlock()

		c.builder.SetInsertPointAtEnd(contBlock)
		if !hasState {
			return llvm.Value{}
		}
		// Out-of-bounds indices skip the function body, so preserve the
		// incoming direct-return state on that edge.
		merged := c.builder.CreatePHI(stateType, "arr_iter_state")
		merged.AddIncoming([]llvm.Value{current}, []llvm.BasicBlock{preCheck})
		merged.AddIncoming([]llvm.Value{next.Val}, []llvm.BasicBlock{iterEnd})
		return merged
	})

	if !hasState {
		return nil
	}
	finalOutput := GetCopy(currentOutput)
	finalOutput.Val = finalVal
	return finalOutput
}

func (c *Compiler) funcLoopNest(fn *ast.FuncStatement, fa *FuncArgs, level int, currentOutput *Symbol) *Symbol {
	if level == len(fa.IterIndices) {
		PushScope(&c.Scopes, BlockScope)
		defer c.popScope()
		if currentOutput == nil {
			c.compileBlockWithArgs(fn, nil)
			return nil
		}
		return c.compileDirectOutputIterBody(fn, currentOutput)
	}

	paramIdx := fa.IterIndices[level]
	input := fa.Inputs[paramIdx]
	name := fn.Parameters[paramIdx].Value

	next := func(iterVal llvm.Value, iterType Type, current *Symbol) *Symbol {
		iterSym := &Symbol{
			Val:      iterVal,
			Type:     iterType,
			FuncArg:  true,
			Borrowed: true,
			ReadOnly: false,
		}
		PushScope(&c.Scopes, BlockScope)
		Put(c.Scopes, name, iterSym)
		defer c.popScope()
		return c.funcLoopNest(fn, fa, level+1, current)
	}

	// Inputs are pointers, extract element type and load the value
	elemType := input.Type.(Ptr).Elem
	paramPtr := input.Val
	var result *Symbol

	switch elemType.Kind() {
	case RangeKind:
		rangeType := elemType.(Range)
		rangeVal := c.createLoad(paramPtr, elemType, name+"_range")
		result = c.iterOverRangeState(rangeType, rangeVal, currentOutput, next)
	case ArrayRangeKind:
		arrRangeVal := c.createLoad(paramPtr, elemType, name+"_array_range")
		arrRangeSym := &Symbol{
			Val:      arrRangeVal,
			Type:     elemType,
			FuncArg:  true,
			Borrowed: true,
			ReadOnly: true,
		}
		result = c.iterOverArrayRangeState(arrRangeSym, currentOutput, next)
	default:
		panic("unsupported iterator kind in funcLoopNest")
	}
	return result
}

func (c *Compiler) compileDirectOutputIterBody(fn *ast.FuncStatement, currentOutput *Symbol) *Symbol {
	// Direct-return ABI is single-output today, so the loop body only needs the
	// current scalar output binding for fn.Outputs[0].
	c.compileBlockWithArgs(fn, map[string]*Symbol{fn.Outputs[0].Value: currentOutput})

	output, _ := c.localValSymbol(fn.Outputs[0].Value, fn.Outputs[0].Value+"_iter_out")
	return output
}

// compileBlockWithArgs executes a function body in the writable scope prepared
// by the caller, seeding any extra scalar bindings needed for that body entry.
func (c *Compiler) compileBlockWithArgs(fn *ast.FuncStatement, scalars map[string]*Symbol) {
	PutBulk(c.Scopes, scalars)

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

func (c *Compiler) compileCallArgs(sig *callSignature, ce *ast.CallExpression) []callArg {
	args := []callArg{}
	paramIndex := 0
	for _, callArgExpr := range ce.Arguments {
		if paramIndex >= len(sig.ParamTypes) {
			panic("internal: call argument count exceeds resolved signature")
		}
		if arrayRangeType, ok := sig.ParamTypes[paramIndex].(ArrayRange); ok {
			arrayRangeExpr, ok := callArgExpr.(*ast.ArrayRangeExpression)
			if !ok {
				panic(fmt.Sprintf("internal: ArrayRange parameter received %T", callArgExpr))
			}
			args = append(args, callArg{
				Expr:        callArgExpr,
				Symbol:      c.compileArrayRangeCallArg(arrayRangeExpr, arrayRangeType),
				OutputAlias: -1,
			})
			paramIndex++
			continue
		}

		if ident, ok := callArgExpr.(*ast.Identifier); ok {
			args = append(args, callArg{
				Expr:        callArgExpr,
				Name:        ident.Value,
				OutputAlias: -1,
			})
			paramIndex++
			continue
		}

		res := c.compileExpression(callArgExpr, nil)
		for _, r := range res {
			if paramIndex >= len(sig.ParamTypes) {
				panic("internal: compiled call argument count exceeds resolved signature")
			}
			args = append(args, callArg{
				Expr:        callArgExpr,
				Symbol:      r,
				OutputAlias: -1,
			})
			paramIndex++
		}
	}
	if paramIndex != len(sig.ParamTypes) {
		panic(fmt.Sprintf("internal: compiled %d call arguments for %d parameters", paramIndex, len(sig.ParamTypes)))
	}
	return args
}

func (c *Compiler) indirectCallOutputAlias(sig *callSignature, paramIndex int, arg callArg, dest []*ast.Identifier) int {
	if !sig.ABI.HasRangeParams || sig.ABI.Params[paramIndex].Mode != ABIParamIndirect || arg.Name == "" {
		return -1
	}
	for outputIndex, output := range dest {
		if output.Value != arg.Name || outputIndex >= len(sig.ABI.Return.OutTypes) {
			continue
		}
		if TypeEqual(sig.ParamTypes[paramIndex], sig.ABI.Return.OutTypes[outputIndex]) {
			return outputIndex
		}
	}
	return -1
}

func (c *Compiler) lowerCallArgs(funcName string, args []callArg, sig *callSignature, dest []*ast.Identifier) []int {
	aliasIndices := c.buildCallParamAliasIndices(sig, args, dest)
	for i, arg := range args {
		args[i].OutputAlias = c.indirectCallOutputAlias(sig, i, arg, dest)
		sym := arg.Symbol
		if sig.ABI.Params[i].Mode != ABIParamIndirect {
			if arg.Name != "" {
				lowered, _ := c.namedValueSymbol(arg.Name, fmt.Sprintf("%s_arg_%d_load", funcName, i))
				args[i].Lowered = lowered
				continue
			}
			args[i].Lowered = c.derefIfPointer(sym, fmt.Sprintf("%s_arg_%d_load", funcName, i))
			continue
		}

		if arg.Name == "" {
			sym, _ = c.makePtr(fmt.Sprintf("%s_arg_%d", funcName, i), sym)
			args[i].Lowered = sym
			continue
		}

		sym, _ = c.getRawSymbol(arg.Name)
		if sym.Type.Kind() != PtrKind {
			sym = c.promoteToMemory(arg.Name)
		}
		args[i].Lowered = sym
	}
	return aliasIndices
}

func (c *Compiler) freeCallArgTemps(callArgs []callArg) {
	offset := 0
	for offset < len(callArgs) {
		expr := callArgs[offset].Expr
		end := offset + 1
		for end < len(callArgs) && callArgs[end].Expr == expr {
			end++
		}

		// Free through the compiled symbols, not the ABI-lowered wrappers, so
		// a frame-substituted argument keeps its value identity and the free
		// is recorded against the frame entry.
		compiled := make([]*Symbol, 0, end-offset)
		for i := offset; i < end; i++ {
			compiled = append(compiled, callArgs[i].Symbol)
		}
		c.freeConsumedTemporary(expr, compiled)
		offset = end
	}
}

func (c *Compiler) prepareCall(sig *callSignature, ce *ast.CallExpression, dest []*ast.Identifier) preparedCall {
	callArgs := c.compileCallArgs(sig, ce)
	aliasIndices := c.lowerCallArgs(sig.FuncName, callArgs, sig, dest)
	fn, funcType, retStruct := c.getOrCompileCallFunction(sig)
	return preparedCall{
		Args:         callArgs,
		AliasIndices: aliasIndices,
		Function:     fn,
		FuncType:     funcType,
		RetStruct:    retStruct,
	}
}

func (c *Compiler) withPreparedCall(sig *callSignature, ce *ast.CallExpression, dest []*ast.Identifier, body func(preparedCall)) {
	call := c.prepareCall(sig, ce, dest)
	defer c.freeCallArgTemps(call.Args)
	body(call)
}

func (c *Compiler) runCallWithBounds(run func()) {
	c.runCallWithBoundsElse(run, nil)
}

func (c *Compiler) runCallWithBoundsElse(run func(), onSkip func()) {
	if !c.withStmtBoundsGuard(
		"call_bounds_ok",
		"call_bounds_run",
		"call_bounds_skip",
		"call_bounds_cont",
		run,
		onSkip,
	) {
		run()
	}
}

func (c *Compiler) loadOutputValues(outputs []*Symbol, name string) []*Symbol {
	out := make([]*Symbol, len(outputs))
	for i := range outputs {
		elemType := outputs[i].Type.(Ptr).Elem
		loadName := name
		if len(outputs) > 1 {
			loadName = fmt.Sprintf("%s_%d", name, i)
		}
		out[i] = &Symbol{
			Val:       c.createLoad(outputs[i].Val, elemType, loadName),
			Type:      elemType,
			WriteFlag: outputs[i].WriteFlag,
		}
	}
	return out
}

func (c *Compiler) compileDirectCallWithRanges(sig *callSignature, info *ExprInfo, dest []*ast.Identifier) []*Symbol {
	PushScope(&c.Scopes, BlockScope)
	defer c.popScope()

	outputs := c.makeSeededTempOutputs(dest, info.OutTypes)
	c.bindRangedTempOutputs(dest, outputs)
	withCollectorPreparedLoopNest(c, info.Rewrite.(*ast.CallExpression), info.Ranges, nil, nil, func(rewCall *ast.CallExpression) {
		c.pushBoundsGuard("call_iter_bounds_guard")
		c.compileCondExprValue(rewCall, llvm.Value{}, func() {
			c.compileDirectCallIntoOutput(sig, rewCall, dest, outputs[0])
		})
		c.popBoundsGuard()
	})

	return c.loadOutputValues(outputs, "final")
}

func (c *Compiler) compileIndirectCallWithRanges(sig *callSignature, info *ExprInfo, dest []*ast.Identifier) []*Symbol {
	PushScope(&c.Scopes, BlockScope)
	defer c.popScope()

	outputs := c.makeSeededTempOutputs(dest, info.OutTypes)
	c.bindRangedTempOutputs(dest, outputs)

	withCollectorPreparedLoopNest(c, info.Rewrite.(*ast.CallExpression), info.Ranges, nil, nil, func(rewCall *ast.CallExpression) {
		// Scope bounds checks to this loop iteration: arguments can contain
		// multiple array reads, and the call should execute only when all are
		// in-bounds for this iteration.
		c.pushBoundsGuard("call_iter_bounds_guard")
		// Inside loop, ranges are shadowed as scalars. If call arguments contain
		// conditional expressions, execute the call only when they hold.
		c.compileCondExprValue(rewCall, llvm.Value{}, func() {
			c.compileIndirectCallIntoStagedOutputs(sig, rewCall, dest, outputs)
		})
		c.popBoundsGuard()
	})

	// Loop path materializes final values from output slots after iteration.
	// Slots are seeded by makeSeededTempOutputs (existing value or zero for new vars), so
	// empty ranges naturally preserve no-op semantics for existing destinations.
	return c.loadOutputValues(outputs, "final")
}

func (c *Compiler) compileCallExpression(ce *ast.CallExpression, dest []*ast.Identifier) (res []*Symbol) {
	info := c.ExprCache[key(c.FuncNameMangled, ce)]
	if rew, ok := info.Rewrite.(*ast.CallExpression); ok && len(c.pendingLoopRanges(info.Ranges)) == 0 {
		ce = rew
		if rewInfo := c.ExprCache[key(c.FuncNameMangled, ce)]; rewInfo != nil {
			info = rewInfo
		}
	}
	sig, ok := c.resolveCallSignature(ce.Function.Value, ce, info)
	if !ok {
		return nil
	}
	// Mutual-recursion convergence can leave this call site's cache partially
	// unresolved even though the selected function variant is now concrete.
	// Keep every lowering path aligned with the authoritative ABI types.
	info.OutTypes = append([]Type(nil), sig.ABI.Return.OutTypes...)
	info.ExprLen = len(info.OutTypes)

	if sig.ABI.Return.Mode == ABIReturnDirect {
		if !info.LoopInside && len(info.Ranges) > 0 {
			// LoopInside=false still needs a temp slot so empty ranges preserve the
			// existing destination value instead of forcing zero.
			return c.compileDirectCallWithRanges(sig, info, dest)
		}

		return c.compileCallInner(sig, ce, dest)
	}

	if len(info.Ranges) > 0 && !info.LoopInside {
		return c.compileIndirectCallWithRanges(sig, info, dest)
	}

	// Indirect-return callees write through their output pointers. Always point
	// them at independent, destination-seeded slots so a call in one RHS cannot
	// mutate a real destination before sibling RHS expressions have read the
	// statement-start values. The outer assignment owns the eventual commit and
	// cleanup. ABI-flavor adapters handle established slots such as StrH when a
	// callee declares StrG.
	outputs := c.makeSeededTempOutputs(dest, info.OutTypes)
	c.compileIndirectCallIntoStagedOutputs(sig, ce, dest, outputs)
	return c.loadOutputValues(outputs, "call_final")
}

func (c *Compiler) getOrCompileCallFunction(sig *callSignature) (llvm.Value, llvm.Type, llvm.Type) {
	funcType, retStruct := c.getFuncType(sig.Mangled, sig.ABI)
	fn := c.Module.NamedFunction(sig.Mangled)
	if !fn.IsNil() {
		return fn, funcType, retStruct
	}

	fk := ast.FuncKey{
		FuncName: sig.FuncName,
		Arity:    len(sig.ParamTypes),
	}
	template := c.CodeCompiler.Code.Func.Map[fk]
	savedBlock := c.builder.GetInsertBlock()

	fn = c.compileFunc(template, sig, funcType, retStruct)
	c.builder.SetInsertPointAtEnd(savedBlock)
	return fn, funcType, retStruct
}

func (c *Compiler) compileDirectCallIntoOutput(sig *callSignature, ce *ast.CallExpression, dest []*ast.Identifier, output *Symbol) {
	c.withPreparedCall(sig, ce, dest, func(call preparedCall) {
		seed := c.makeZeroValue(sig.ABI.Return.DirectType)
		if output != nil {
			seed = c.directReturnSeedForCall(sig.ABI.Return.DirectType, nil, output)
		}
		c.runCallWithBounds(func() {
			result := c.callDirect(call.Function, call.FuncType, sig, call, seed)
			c.storeSymbolToSlot(output, result, output.Type.(Ptr).Elem, "call_direct_store")
		})
	})
}

func (c *Compiler) compileIndirectCallIntoOutputs(
	sig *callSignature,
	ce *ast.CallExpression,
	dest []*ast.Identifier,
	outputs []*Symbol,
	afterCall func([]llvm.Value),
	onSkip func(),
) {
	c.withPreparedCall(sig, ce, dest, func(call preparedCall) {
		c.runCallWithBoundsElse(func() {
			writeFlags := c.makeCallOutputWriteFlags(len(outputs))
			c.builder.CreateCall(
				call.FuncType,
				call.Function,
				c.callArgs(sig, call, call.RetStruct, outputs, writeFlags, nil),
				"",
			)
			if afterCall != nil {
				afterCall(writeFlags)
			}
		}, onSkip)
	})
}

func (c *Compiler) compileIndirectCallIntoStagedOutputs(
	sig *callSignature,
	ce *ast.CallExpression,
	dest []*ast.Identifier,
	staged []*Symbol,
) {
	adapters := c.makeCallOutputAdapters(staged, sig.ABI.Return.OutTypes)
	callOutputs := callAdapterOutputs(adapters)
	c.compileIndirectCallIntoOutputs(
		sig,
		ce,
		dest,
		callOutputs,
		func(writeFlags []llvm.Value) { c.commitCallOutputAdapters(staged, adapters, writeFlags) },
		func() { c.cleanupSkippedCallOutputAdapters(adapters) },
	)
}

func (c *Compiler) makeCallOutputWriteFlags(count int) []llvm.Value {
	flags := make([]llvm.Value, count)
	for i := range flags {
		name := fmt.Sprintf("call_output_written_%d", c.tmpCounter)
		c.tmpCounter++
		flags[i] = c.createEntryBlockAlloca(c.Context.Int1Type(), name)
		c.builder.CreateStore(llvm.ConstInt(c.Context.Int1Type(), 0, false), flags[i])
	}
	return flags
}

// compileCallInner compiles the actual function call
func (c *Compiler) compileCallInner(sig *callSignature, ce *ast.CallExpression, dest []*ast.Identifier) []*Symbol {
	var results []*Symbol
	c.withPreparedCall(sig, ce, dest, func(call preparedCall) {
		seed := c.makeZeroValue(sig.ABI.Return.DirectType)
		if len(dest) > 0 {
			seed = c.directReturnSeedForCall(sig.ABI.Return.DirectType, dest[0], nil)
		}
		resultPtr := c.createEntryBlockAlloca(c.mapToLLVMType(sig.ABI.Return.DirectType), sig.FuncName+"_call_tmp")
		c.createStore(seed.Val, resultPtr, seed.Type)

		c.runCallWithBounds(func() {
			callResult := c.callDirect(call.Function, call.FuncType, sig, call, seed)
			c.createStore(callResult.Val, resultPtr, callResult.Type)
		})

		results = []*Symbol{{
			Val:  c.createLoad(resultPtr, sig.ABI.Return.DirectType, sig.FuncName+"_call_ret"),
			Type: sig.ABI.Return.DirectType,
		}}
	})

	return results
}

func (c *Compiler) callArgs(
	sig *callSignature,
	call preparedCall,
	retStruct llvm.Type,
	outputs []*Symbol,
	writeFlags []llvm.Value,
	directSeed *Symbol,
) []llvm.Value {
	llvmArgs := []llvm.Value{}
	if sig.ABI.UsesIndirectReturn() {
		sretPtr := c.createEntryBlockAlloca(retStruct, "sret_tmp")
		for i, out := range outputs {
			fieldPtr := c.builder.CreateStructGEP(retStruct, sretPtr, i, fmt.Sprintf("sret_field_%d", i))
			c.builder.CreateStore(out.Val, fieldPtr)
		}
		for i, flag := range writeFlags {
			fieldPtr := c.builder.CreateStructGEP(retStruct, sretPtr, len(outputs)+i, fmt.Sprintf("sret_written_field_%d", i))
			c.builder.CreateStore(flag, fieldPtr)
		}
		llvmArgs = append(llvmArgs, sretPtr)
	}
	for _, arg := range call.Args {
		argVal := arg.Lowered.Val
		if arg.OutputAlias >= 0 && arg.OutputAlias < len(outputs) {
			argVal = outputs[arg.OutputAlias].Val
		}
		llvmArgs = append(llvmArgs, argVal)
	}
	for _, aliasIndex := range call.AliasIndices {
		llvmArgs = append(llvmArgs, llvm.ConstInt(c.Context.Int32Type(), uint64(aliasIndex), false))
	}
	if sig.ABI.Return.HasSeedParam {
		seed := c.coerceSymbolForType(directSeed, sig.ABI.Return.DirectType, sig.FuncName+"_seed")
		llvmArgs = append(llvmArgs, seed.Val)
	}
	return llvmArgs
}

func (c *Compiler) callDirect(fn llvm.Value, funcType llvm.Type, sig *callSignature, call preparedCall, directSeed *Symbol) *Symbol {
	callVal := c.builder.CreateCall(funcType, fn, c.callArgs(sig, call, llvm.Type{}, nil, nil, directSeed), sig.FuncName+"_ret")
	return &Symbol{
		Val:  callVal,
		Type: sig.ABI.Return.DirectType,
	}
}

// extract numeric fields of a range struct
func (c *Compiler) rangeComponents(r llvm.Value) (start, stop, step llvm.Value) {
	start = c.builder.CreateExtractValue(r, 0, "start")
	stop = c.builder.CreateExtractValue(r, 1, "stop")
	step = c.builder.CreateExtractValue(r, 2, "step")
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

func (c *Compiler) quotedStrArg(s *Symbol) llvm.Value {
	fnType, fn := c.GetCFunc(STR_QUOTE)
	return c.builder.CreateCall(fnType, fn, []llvm.Value{s.Val}, "str_quote")
}

func (c *Compiler) quotedStrPrefixArg(s *Symbol, byteLimit llvm.Value) llvm.Value {
	fnType, fn := c.GetCFunc(STR_QUOTE_PREFIX)
	return c.builder.CreateCall(fnType, fn, []llvm.Value{s.Val, byteLimit}, "str_quote_prefix")
}

func (c *Compiler) hexStrArg(s *Symbol, byteLimit *llvm.Value, uppercase, alternate, spaced bool) llvm.Value {
	limit := llvm.ConstAllOnes(c.Context.Int64Type())
	if byteLimit != nil {
		limit = *byteLimit
	}
	boolArg := func(value bool) llvm.Value {
		if value {
			return llvm.ConstInt(c.Context.Int32Type(), 1, false)
		}
		return llvm.ConstInt(c.Context.Int32Type(), 0, false)
	}
	fnType, fn := c.GetCFunc(STR_HEX)
	return c.builder.CreateCall(fnType, fn, []llvm.Value{
		s.Val,
		limit,
		boolArg(uppercase),
		boolArg(alternate),
		boolArg(spaced),
	}, "str_hex")
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
		// A static string is immortal, so it never needs an owned copy — return
		// it as-is. A store into a heap-string slot makes the StrH copy through
		// the store's coercion (coerceSymbolForType); a store into a StrG slot
		// keeps it static. Only heap strings are deep-copied here.
		if IsStrG(sym.Type) {
			return sym
		}
		copiedStr := c.copyString(sym.Val)
		return &Symbol{
			Val:      copiedStr,
			Type:     StrH{}, // Copied strings are always heap-allocated
			FuncArg:  false,
			Borrowed: false,
			ReadOnly: false,
		}
	case ArrayKind:
		arrayType := sym.Type.(Array)
		if arrayType.ElemType != nil {
			if !hasConcreteArrayElemType(arrayType.ElemType) {
				return sym
			}
			return &Symbol{
				Val:      c.copyArrayValue(sym.Val, arrayType),
				Type:     sym.Type,
				FuncArg:  false,
				Borrowed: false,
				ReadOnly: false,
			}
		}
	case TableKind:
		tableType := sym.Type.(Table)
		if !IsFullyResolvedType(tableType) {
			return sym
		}
		return &Symbol{
			Val:      c.copyTableValue(sym.Val, tableType),
			Type:     tableType,
			Borrowed: false,
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
		// Collector preparation can bind collecttmp_* before the print loop opens.
		// Keep those temporaries scoped to this print statement.
		PushScope(&c.Scopes, BlockScope)
		defer c.popScope()

		withCollectorPreparedLoopNest(c, info.Rewrite.(*ast.CallExpression), info.Ranges, nil, nil, func(rewCall *ast.CallExpression) {
			// Per-iteration gate: a failing conditional skips that
			// iteration's line, so `i > 2` prints only admitted elements.
			c.compileCondOperands(rewCall, llvm.Value{}, func() {
				c.printAllExpressions(rewCall.Arguments)
			})
		})
		return
	}

	// LoopInside=true or no ranges: direct print, gated on every conditional
	// argument yielding — a failed condition prints nothing (the target-less
	// case of propagation).
	c.compileCondOperands(ce, llvm.Value{}, func() {
		c.printAllExpressions(ce.Arguments)
	})
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

// asStringLiteral extracts token and value from string literal expressions.
func asStringLiteral(expr ast.Expression) (tok token.Token, value string, ok bool) {
	switch e := expr.(type) {
	case *ast.StringLiteral:
		return e.Token, e.Token.Literal, true
	}
	return token.Token{}, "", false
}

// appendPrintExpression handles one print expression (string literal or compiled expression)
func (c *Compiler) appendPrintExpression(expr ast.Expression, formatStr *string, args *[]llvm.Value, toFree *[]llvm.Value) {
	// String literals with markers are processed specially.
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

	// Structs produce multi-line output with a trailing \n separator.
	if s.Type.Kind() == StructKind {
		fmtStr, fmtArgs, fmtFree := c.structFormatArgs(s)
		*formatStr += fmtStr
		*args = append(*args, fmtArgs...)
		*toFree = append(*toFree, fmtFree...)
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
	case FloatKind:
		strPtr := c.floatStrArg(s)
		*args = append(*args, strPtr)
		*toFree = append(*toFree, strPtr)
	case ArrayKind:
		arrType := s.Type.(Array)
		if arrType.Rank == 1 && !hasConcreteArrayElemType(arrType.ElemType) {
			*args = append(*args, c.constCString("[]"))
			return
		}
		strPtr := c.arrayStrArg(s)
		*args = append(*args, strPtr)
		*toFree = append(*toFree, strPtr)
	case TableKind:
		strPtr := c.tableStrArg(s)
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
