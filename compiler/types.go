package compiler

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/thiremani/pluto/token"
)

type Kind int

const (
	UnresolvedKind Kind = iota
	EmptyKind
	IntKind
	UintKind
	FloatKind
	PtrKind
	StrKind // String type (StrG or StrH)
	RangeKind
	FuncKind
	ArrayKind
	TableKind
	ArrayRangeKind
	StructKind
)

// Type is the interface for all types in our language.
type Type interface {
	String() string
	Kind() Kind
	Mangle() string
	Key() Type
}

// Common concrete types (aliases) for readability.
// These are value-typed singletons; using them in maps/keys is safe since
// Int and Float are comparable by value.
var (
	I1  Type = Int{Width: 1}
	I64 Type = Int{Width: 64}
	F64 Type = Float{Width: 64}
)

// PrimitiveTypeNames lists all primitive type mangle names (for demangling).
// Update this when adding new primitive types.
// Sorted by descending length in init() for correct prefix matching.
var PrimitiveTypeNames = []string{
	"I64", "I32", "I16", "I8", "I1",
	"U64", "U32", "U16", "U8",
	"F64", "F32",
	"StrG", "StrH", // StrG = global/static (.rodata), StrH = heap
	"Empty",
	"X", // Unresolved placeholder
}

// CompoundTypePrefixes lists all compound type prefixes (for demangling).
// These are types that use the _tN_[types...] pattern.
// Update this when adding new compound types.
// Sorted by descending length in init() for correct prefix matching.
var CompoundTypePrefixes = []string{
	"ArrayRange",
	"Table",
	"Array",
	"Range",
	"Ptr",
	"Func",
}

func init() {
	// Sort by descending length to ensure longer prefixes match first
	// (e.g., "ArrayRange" before "Array", "I64" before "I6")
	sortByLengthDesc := func(slice []string) {
		sort.Slice(slice, func(i, j int) bool {
			return len(slice[i]) > len(slice[j])
		})
	}
	sortByLengthDesc(PrimitiveTypeNames)
	sortByLengthDesc(CompoundTypePrefixes)
}

type Unresolved struct{}

func (u Unresolved) Kind() Kind     { return UnresolvedKind }
func (u Unresolved) String() string { return "?" } // human-friendly
func (u Unresolved) Mangle() string { return "X" } // placeholder for unresolved
func (u Unresolved) Key() Type      { return u }

// Empty is the element type of an empty array whose concrete element type has
// not been established. Unlike Unresolved, it is a valid, fully resolved type.
type Empty struct{}

func (e Empty) Kind() Kind     { return EmptyKind }
func (e Empty) String() string { return "Empty" }
func (e Empty) Mangle() string { return "Empty" }
func (e Empty) Key() Type      { return e }

// Int represents an integer type with a given bit width.
type Int struct {
	Width uint32 // e.g. 8, 16, 32, 64
}

func (i Int) String() string {
	return fmt.Sprintf("I%d", i.Width)
}

func (i Int) Kind() Kind {
	return IntKind
}
func (i Int) Mangle() string { return i.String() }
func (i Int) Key() Type      { return i }

func IsI1(t Type) bool {
	intType, ok := t.(Int)
	return ok && intType.Width == 1
}

// Float represents a floating-point type with a given precision.
type Float struct {
	Width uint32 // e.g. 32, 64
}

func (f Float) String() string {
	return fmt.Sprintf("F%d", f.Width)
}

func (f Float) Kind() Kind {
	return FloatKind
}
func (f Float) Mangle() string { return f.String() }
func (f Float) Key() Type      { return f }

// Ptr represents a pointer type to some element type.
type Ptr struct {
	Elem Type // The type of the element being pointed to.
}

func (p Ptr) String() string { return "*" + p.Elem.String() }
func (p Ptr) Mangle() string { return "Ptr" + SEP + T + "1" + SEP + p.Elem.Mangle() }
func (p Ptr) Key() Type      { return p }

func (p Ptr) Kind() Kind {
	return PtrKind
}

// StrG represents a global/static string stored in .rodata.
// These strings are never freed and cannot be concatenated.
type StrG struct{}

// String is the user-facing name; the global/heap flavor is an internal
// detail carried only by Mangle (for specialization identity), not shown
// in diagnostics.
func (s StrG) String() string { return "Str" }
func (s StrG) Kind() Kind     { return StrKind }
func (s StrG) Mangle() string { return "StrG" }
func (s StrG) Key() Type      { return s }

// StrH represents a heap-allocated string.
// These strings must be freed and can be concatenated.
type StrH struct{}

// String is the user-facing name; see StrG.String. Mangle keeps the flavor.
func (s StrH) String() string { return "Str" }
func (s StrH) Kind() Kind     { return StrKind }
func (s StrH) Mangle() string { return "StrH" }
func (s StrH) Key() Type      { return s }

// IsStrG returns true if the type is a global/static string.
func IsStrG(t Type) bool {
	_, ok := t.(StrG)
	return ok
}

// IsStrH returns true if the type is a heap-allocated string.
func IsStrH(t Type) bool {
	_, ok := t.(StrH)
	return ok
}

// Str is the canonical string type used as the operator lookup key.
// All string types (StrG, StrH, etc.) are char* at the LLVM level,
// so operators only need one registration using Str as the key type.
type Str struct{}

func (s Str) String() string { return "Str" }
func (s Str) Kind() Kind     { return StrKind }
func (s Str) Mangle() string { return "Str" }
func (s Str) Key() Type      { return s }

type Range struct {
	Iter Type
}

func (r Range) String() string {
	// Represent a range's schema as T:T:T (start:stop:step)
	t := r.Iter.String()
	return t + ":" + t + ":" + t
}
func (r Range) Mangle() string { return "Range" + SEP + T + "1" + SEP + r.Iter.Mangle() }
func (r Range) Key() Type      { return r }

func (r Range) Kind() Kind {
	return RangeKind
}

type Func struct {
	Name     string
	Params   []Type
	OutTypes []Type // Final function output types (after wrapping for array types)
}

func (f Func) String() string {
	return fmt.Sprintf("%s = %s(%s)", typesStr(f.OutTypes), f.Name, typesStr(f.Params))
}

func (f Func) Kind() Kind {
	return FuncKind
}
func (f Func) Mangle() string {
	// Follow _tN_ pattern like other compound types (Ptr, Range, Array)
	// Per spec: return types are NOT mangled in function symbols.
	// This means function types differing only by return type will have
	// the same mangled form. This is intentional: Pluto disallows function
	// overloading by return type alone, so collisions cannot occur.
	// Note: Key() includes output types for internal type equality checks.
	s := "Func" + SEP + T + strconv.Itoa(len(f.Params))
	for _, p := range f.Params {
		s += SEP + p.Mangle()
	}
	return s
}
func (f Func) Key() Type {
	keyParams := make([]Type, len(f.Params))
	for i, p := range f.Params {
		keyParams[i] = p.Key()
	}
	keyOutTypes := make([]Type, len(f.OutTypes))
	for i, o := range f.OutTypes {
		keyOutTypes[i] = o.Key()
	}
	return Func{Params: keyParams, OutTypes: keyOutTypes}
}

func (f Func) AllTypesInferred() bool {
	for _, p := range f.Params {
		if !IsFullyResolvedType(p) {
			return false
		}
	}
	return f.OutputTypesInferred()
}

// OutputTypesInferred reports whether all function outputs are fully resolved.
func (f Func) OutputTypesInferred() bool {
	for _, ot := range f.OutTypes {
		if !IsFullyResolvedType(ot) {
			return false
		}
	}
	return true
}

// IsFullyResolvedType reports whether a type contains no unresolved components.
// It recursively checks composite/container types.
func IsFullyResolvedType(t Type) bool {
	switch tt := t.(type) {
	case Unresolved:
		return false
	case Empty, Int, Float, StrG, StrH:
		return true
	case Ptr:
		return IsFullyResolvedType(tt.Elem)
	case Range:
		return IsFullyResolvedType(tt.Iter)
	case Array:
		return tt.Rank > 0 && tt.ElemType != nil && IsFullyResolvedType(tt.ElemType)
	case Table:
		for _, column := range tt.Columns {
			if column.ElemType == nil || !IsFullyResolvedType(column.ElemType) {
				return false
			}
		}
		return true
	case ArrayRange:
		return IsFullyResolvedType(tt.Array) && IsFullyResolvedType(tt.Range)
	case Struct:
		for _, field := range tt.Fields {
			if !IsFullyResolvedType(field.Type) {
				return false
			}
		}
		return true
	case Func:
		return tt.AllTypesInferred()
	default:
		panic(fmt.Sprintf("unhandled type in IsFullyResolvedType: %T (%v)", t, t.Kind()))
	}
}

// isUntypedEmptyCollection reports whether t is a header-only table, or a
// column projected from one, whose element types have not been established.
func isUntypedEmptyCollection(t Type) bool {
	switch tt := t.(type) {
	case Array:
		return tt.ElemType != nil && tt.ElemType.Kind() == UnresolvedKind
	case Table:
		if len(tt.Columns) == 0 {
			return false
		}
		for _, column := range tt.Columns {
			if column.ElemType == nil || column.ElemType.Kind() != UnresolvedKind {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// Array is a homogeneous rectangular value. Rank is part of the type while
// dimension lengths are runtime properties.
type Array struct {
	ElemType Type
	Rank     int
}

func (a Array) String() string {
	if a.ElemType == nil || a.Rank < 1 {
		return "[]"
	}
	result := a.ElemType.String()
	for range a.Rank {
		result = "[" + result + "]"
	}
	return result
}

func (a Array) Kind() Kind { return ArrayKind }
func (a Array) Mangle() string {
	result := a.ElemType.Mangle()
	for range a.Rank {
		result = "Array" + SEP + T + "1" + SEP + result
	}
	return result
}
func (a Array) Key() Type {
	return Array{ElemType: a.ElemType.Key(), Rank: a.Rank}
}

func hasConcreteArrayElemType(elem Type) bool {
	return elem != nil && elem.Kind() != EmptyKind && elem.Kind() != UnresolvedKind
}

func arrayIndexResultType(array Array) Type {
	if array.Rank > 1 {
		return Array{ElemType: array.ElemType, Rank: array.Rank - 1}
	}
	if array.ElemType.Kind() == StrKind {
		return StrH{}
	}
	return array.ElemType
}

// TableColumn pairs an optional source-level name with a homogeneous column
// element type. Name is empty for positional, unnamed columns.
type TableColumn struct {
	Name     string
	ElemType Type
}

// Table is an ordered, columnar schema. Row count is a runtime property.
type Table struct {
	Columns []TableColumn
}

func (t Table) String() string {
	columns := make([]string, len(t.Columns))
	for i, column := range t.Columns {
		columns[i] = column.ElemType.String()
		if column.Name != "" {
			columns[i] = column.Name + ":" + columns[i]
		}
	}
	return "Table[" + strings.Join(columns, " ") + "]"
}

func (t Table) Kind() Kind { return TableKind }
func (t Table) Mangle() string {
	parts := make([]string, 0, len(t.Columns)*2)
	for _, column := range t.Columns {
		encodedName := "u"
		if column.Name != "" {
			encodedName = "n" + column.Name
		}
		parts = append(parts, MangleIdent(encodedName), column.ElemType.Mangle())
	}
	mangled := "Table" + SEP + T + strconv.Itoa(len(parts))
	if len(parts) > 0 {
		mangled += SEP + strings.Join(parts, SEP)
	}
	return mangled
}
func (t Table) Key() Type {
	columns := make([]TableColumn, len(t.Columns))
	for i, column := range t.Columns {
		columns[i] = TableColumn{Name: column.Name, ElemType: column.ElemType.Key()}
	}
	return Table{Columns: columns}
}

// ArrayRange represents an iteration over a range of an array.
// It carries the underlying array schema so type comparisons and mangling
// can remain structural; the actual range bounds are runtime values.
type ArrayRange struct {
	Array Array
	Range Range
}

func (ar ArrayRange) String() string {
	return fmt.Sprintf("%s[%s]", ar.Array.String(), ar.Range.String())
}

func (ar ArrayRange) Kind() Kind { return ArrayRangeKind }

func (ar ArrayRange) Mangle() string {
	return "ArrayRange" + SEP + T + "1" + SEP + ar.Array.ElemType.Mangle()
}
func (ar ArrayRange) Key() Type {
	return ArrayRange{
		Array: ar.Array.Key().(Array),
		Range: ar.Range.Key().(Range),
	}
}

func isRangeDriverType(t Type) bool {
	if t == nil {
		return false
	}

	switch t.Kind() {
	case RangeKind, ArrayRangeKind:
		return true
	default:
		return false
	}
}

type StructField struct {
	Name string
	Type Type
}

type Struct struct {
	// Struct is the canonical schema for a nominal struct type.
	// Source literals may provide only a subset of fields; omitted fields remain
	// represented in the AST and are filled during codegen, not in the type.
	Name     string
	Fields   []StructField
	FieldSet map[string]struct{}
}

func (s Struct) String() string {
	parts := make([]string, len(s.Fields))
	for i, field := range s.Fields {
		parts[i] = field.Name + ":" + field.Type.String()
	}
	return s.Name + "{" + strings.Join(parts, " ") + "}"
}

func (s Struct) Kind() Kind { return StructKind }

// FieldIndex returns the index of the named field, or -1 if not found.
func (s Struct) FieldIndex(name string) int {
	for i, f := range s.Fields {
		if f.Name == name {
			return i
		}
	}
	return -1
}

// Structs are nominal in Phase 1 and mangle as nominal type names.
func (s Struct) Mangle() string { return MangleIdent(s.Name) }

func (s Struct) Key() Type {
	keyFields := make([]StructField, len(s.Fields))
	for i, field := range s.Fields {
		keyFields[i] = StructField{
			Name: field.Name,
			Type: field.Type.Key(),
		}
	}
	return Struct{
		Name:   s.Name,
		Fields: keyFields,
	}
}

func validateStructFieldHeaders(schema *Struct, headers []token.Token) (map[string]int, *token.CompileError) {
	fieldIdxByHeader := make(map[string]int, len(headers))
	for _, headerTok := range headers {
		header := headerTok.Literal
		if _, exists := fieldIdxByHeader[header]; exists {
			return nil, &token.CompileError{
				Token: headerTok,
				Msg:   fmt.Sprintf("duplicate struct field header: %s", header),
			}
		}
		fieldIdx := schema.FieldIndex(header)
		if fieldIdx < 0 {
			return nil, &token.CompileError{
				Token: headerTok,
				Msg:   fmt.Sprintf("field %q not in struct type %s", header, schema.Name),
			}
		}
		fieldIdxByHeader[header] = fieldIdx
	}
	return fieldIdxByHeader, nil
}

func mergeStringFlavor(leftType, rightType Type) Type {
	if IsStrH(leftType) || IsStrH(rightType) {
		return StrH{}
	}
	return StrG{}
}

func resolvedStructFieldType(fieldType, cellType Type) (Type, bool) {
	if cellType.Kind() == StrKind && fieldType.Kind() == StrKind {
		return mergeStringFlavor(fieldType, cellType), true
	}
	if cellType.Kind() == IntKind && fieldType.Kind() == FloatKind {
		return fieldType, true
	}
	if TypeEqual(cellType, fieldType) {
		return fieldType, true
	}
	return Unresolved{}, false
}

func typesStr(types []Type) string {
	if len(types) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, t := range types {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(t.String())
	}
	return sb.String()
}

// Checks if two type arrays are equal
// Be careful with Unresolved types
func EqualTypes(left []Type, right []Type) bool {
	if len(left) != len(right) {
		return false
	}

	for i, l := range left {
		if !TypeEqual(l, right[i]) {
			return false
		}
	}

	return true
}

// TypeEqual performs structural equality on types, avoiding brittle String() comparison.
// TypeEqual performs structural equality on types with a dispatcher by Kind.
func TypeEqual(a, b Type) bool {
	if a.Kind() != b.Kind() {
		return false
	}
	cmp := typeComparer(a.Kind())
	return cmp(a, b)
}

// CanRefineType checks if oldType can be refined to newType by replacing
// unresolved components with concrete types.
// Returns true if refinement is possible (including when types are already equal).
// Composite/container types are checked recursively.
func CanRefineType(oldType, newType Type) bool {
	// Completely unresolved type can be refined to anything
	if oldType.Kind() == UnresolvedKind || oldType.Kind() == EmptyKind {
		return true
	}

	// If kinds differ, can't refine
	if oldType.Kind() != newType.Kind() {
		return false
	}

	switch old := oldType.(type) {
	case Array:
		newArr, ok := newType.(Array)
		return ok && canRefineArray(old, newArr)
	case Table:
		newTable, ok := newType.(Table)
		return ok && canRefineTable(old, newTable)
	case ArrayRange:
		newSlice, ok := newType.(ArrayRange)
		return ok && canRefineArrayRange(old, newSlice)
	case Ptr:
		newPtr, ok := newType.(Ptr)
		return ok && CanRefineType(old.Elem, newPtr.Elem)
	case Range:
		newRange, ok := newType.(Range)
		return ok && CanRefineType(old.Iter, newRange.Iter)
	case Func:
		newFunc, ok := newType.(Func)
		return ok && canRefineFunc(old, newFunc)
	case Struct:
		newStruct, ok := newType.(Struct)
		return ok && canRefineStruct(old, newStruct)
	default:
		// For other types, must be equal
		return TypeEqual(oldType, newType)
	}
}

func bindingSlotCompatible(oldType, newType Type) bool {
	if oldType.Kind() == StrKind && newType.Kind() == StrKind {
		return true
	}
	oldArray, oldIsArray := oldType.(Array)
	newArray, newIsArray := newType.(Array)
	if oldIsArray && newIsArray {
		// [] resets an existing array without changing its established type or rank.
		if newArray.ElemType.Kind() == EmptyKind {
			return true
		}
		if oldArray.ElemType.Kind() == EmptyKind {
			return oldArray.Rank == newArray.Rank
		}
	}
	return CanRefineType(oldType, newType)
}

// mergeBindingSlotType assumes bindingSlotCompatible(oldType, newType) is true.
// For strings it widens to the owning flavor; for all other types callers have
// already established that newType is a valid refinement of the existing slot.
func mergeBindingSlotType(oldType, newType Type) Type {
	if oldType.Kind() == StrKind && newType.Kind() == StrKind {
		return mergeStringFlavor(oldType, newType)
	}
	oldArray, oldIsArray := oldType.(Array)
	newArray, newIsArray := newType.(Array)
	if oldIsArray && newIsArray {
		if newArray.ElemType.Kind() == EmptyKind {
			return oldType
		}
		if oldArray.ElemType.Kind() == EmptyKind {
			return newType
		}
	}
	return newType
}

func canRefineTypes(oldTypes, newTypes []Type) bool {
	if len(oldTypes) != len(newTypes) {
		return false
	}
	for i := range oldTypes {
		if !CanRefineType(oldTypes[i], newTypes[i]) {
			return false
		}
	}
	return true
}

func canRefineArray(oldArr, newArr Array) bool {
	return oldArr.Rank == newArr.Rank && CanRefineType(oldArr.ElemType, newArr.ElemType)
}

func canRefineTable(oldTable, newTable Table) bool {
	if len(oldTable.Columns) != len(newTable.Columns) {
		return false
	}
	for i, oldColumn := range oldTable.Columns {
		newColumn := newTable.Columns[i]
		if oldColumn.Name != newColumn.Name || !CanRefineType(oldColumn.ElemType, newColumn.ElemType) {
			return false
		}
	}
	return true
}

func canRefineArrayRange(oldSlice, newSlice ArrayRange) bool {
	return CanRefineType(oldSlice.Array, newSlice.Array) && CanRefineType(oldSlice.Range, newSlice.Range)
}

func canRefineFunc(oldFunc, newFunc Func) bool {
	return canRefineTypes(oldFunc.Params, newFunc.Params) && canRefineTypes(oldFunc.OutTypes, newFunc.OutTypes)
}

func canRefineStruct(oldStruct, newStruct Struct) bool {
	// Struct refinement is nominal: solver-side Struct values are canonicalized
	// through StructCache before they participate in assignment/refinement.
	return oldStruct.Name == newStruct.Name
}

func typeComparer(k Kind) func(a, b Type) bool {
	switch k {
	case UnresolvedKind:
		return eqUnresolved
	case EmptyKind:
		return eqEmpty
	case IntKind:
		return eqInt
	case FloatKind:
		return eqFloat
	case StrKind:
		return eqStr
	case PtrKind:
		return eqPointer
	case RangeKind:
		return eqRange
	case FuncKind:
		return eqFunc
	case ArrayKind:
		return eqArray
	case TableKind:
		return eqTable
	case ArrayRangeKind:
		return eqArrayRange
	case StructKind:
		return eqStruct
	default:
		return func(a, b Type) bool { panic(fmt.Sprintf("TypeEqual: unhandled kind %v", k)) }
	}
}

func eqUnresolved(a, b Type) bool { return true }
func eqEmpty(a, b Type) bool      { return true }

func eqInt(a, b Type) bool {
	ai := a.(Int)
	bi := b.(Int)
	return ai.Width == bi.Width
}

func eqFloat(a, b Type) bool {
	af := a.(Float)
	bf := b.(Float)
	return af.Width == bf.Width
}

// eqStr checks if both string types are the same (both StrG or both StrH)
func eqStr(a, b Type) bool {
	return (IsStrG(a) && IsStrG(b)) || (IsStrH(a) && IsStrH(b))
}

func eqPointer(a, b Type) bool {
	ap := a.(Ptr)
	bp := b.(Ptr)
	return TypeEqual(ap.Elem, bp.Elem)
}

func eqRange(a, b Type) bool {
	ar := a.(Range)
	br := b.(Range)
	return TypeEqual(ar.Iter, br.Iter)
}

func eqFunc(a, b Type) bool {
	af := a.(Func)
	bf := b.(Func)
	if len(af.Params) != len(bf.Params) || len(af.OutTypes) != len(bf.OutTypes) {
		return false
	}
	if !EqualTypes(af.Params, bf.Params) {
		return false
	}
	return EqualTypes(af.OutTypes, bf.OutTypes)
}

func eqArray(a, b Type) bool {
	aa := a.(Array)
	ba := b.(Array)
	return aa.Rank == ba.Rank && TypeEqual(aa.ElemType, ba.ElemType)
}

func eqTable(a, b Type) bool {
	at := a.(Table)
	bt := b.(Table)
	if len(at.Columns) != len(bt.Columns) {
		return false
	}
	for i, column := range at.Columns {
		other := bt.Columns[i]
		if column.Name != other.Name || !TypeEqual(column.ElemType, other.ElemType) {
			return false
		}
	}
	return true
}

func eqArrayRange(a, b Type) bool {
	aar := a.(ArrayRange)
	bar := b.(ArrayRange)
	return eqArray(aar.Array, bar.Array) && eqRange(aar.Range, bar.Range)
}

func eqStruct(a, b Type) bool {
	as := a.(Struct)
	bs := b.(Struct)
	if as.Name != bs.Name {
		return false
	}
	if len(as.Fields) != len(bs.Fields) {
		return false
	}
	for i := range as.Fields {
		af := as.Fields[i]
		bf := bs.Fields[i]
		if af.Name != bf.Name {
			return false
		}
		if !TypeEqual(af.Type, bf.Type) {
			return false
		}
	}
	return true
}

var reservedTypeNames = map[string]struct{}{
	"Empty": {}, "Int": {}, "Float": {}, "Str": {}, "StrG": {}, "StrH": {}, "StrS": {},
	"I1": {}, "I8": {}, "I16": {}, "I32": {}, "I64": {},
	"U8": {}, "U16": {}, "U32": {}, "U64": {},
	"F32": {}, "F64": {},
	"Ptr": {}, "Range": {}, "Array": {}, "Table": {}, "ArrayRange": {},
	"Func": {}, "Struct": {},
}
