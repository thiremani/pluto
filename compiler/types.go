package compiler

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type Kind int

const (
	UnresolvedKind Kind = iota
	IntKind
	UintKind
	FloatKind
	PtrKind
	StrKind // String type (StrG or StrH)
	RangeKind
	FuncKind
	ArrayKind
	ArrayRangeKind
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
	"X", // Unresolved placeholder
}

// CompoundTypePrefixes lists all compound type prefixes (for demangling).
// These are types that use the _tN_[types...] pattern.
// Update this when adding new compound types.
// Sorted by descending length in init() for correct prefix matching.
var CompoundTypePrefixes = []string{
	"ArrayRange",
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

func (s StrG) String() string { return "StrG" }
func (s StrG) Kind() Kind     { return StrKind }
func (s StrG) Mangle() string { return "StrG" }
func (s StrG) Key() Type      { return s }

// StrH represents a heap-allocated string.
// These strings must be freed and can be concatenated.
type StrH struct{}

func (s StrH) String() string { return "StrH" }
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
	case Int, Float, StrG, StrH:
		return true
	case Ptr:
		return IsFullyResolvedType(tt.Elem)
	case Range:
		return IsFullyResolvedType(tt.Iter)
	case Array:
		if len(tt.ColTypes) == 0 {
			return false
		}
		for _, col := range tt.ColTypes {
			if !IsFullyResolvedType(col) {
				return false
			}
		}
		return true
	case ArrayRange:
		return IsFullyResolvedType(tt.Array) && IsFullyResolvedType(tt.Range)
	case Func:
		return tt.AllTypesInferred()
	default:
		panic(fmt.Sprintf("unhandled type in IsFullyResolvedType: %T (%v)", t, t.Kind()))
	}
}

// Array represents a tabular array with optional headers and typed columns.
// Each column has a primitive element type (I64, F64, or Str). Length is the
// number of rows. Headers may be empty (for matrices without named columns).
type Array struct {
	Headers  []string // column headers (may be empty)
	ColTypes []Type   // element type per column (must be Int{64}, Float{64}, or Str)
	Length   int      // number of rows
}

func (a Array) String() string {
	// Type identity ignores headers and length; show schema only.
	if len(a.ColTypes) == 0 {
		return "[]"
	}
	var cols []string
	for _, ct := range a.ColTypes {
		cols = append(cols, ct.String())
	}
	return "[" + strings.Join(cols, " ") + "]"
}

func (a Array) Kind() Kind { return ArrayKind }
func (a Array) Mangle() string {
	s := "Array" + SEP + T + strconv.Itoa(len(a.ColTypes))
	for _, ct := range a.ColTypes {
		s += SEP + ct.Mangle()
	}
	return s
}
func (a Array) Key() Type {
	// Array keys ignore headers and length, using only column types
	keyColTypes := make([]Type, len(a.ColTypes))
	for i, ct := range a.ColTypes {
		keyColTypes[i] = ct.Key()
	}
	return Array{ColTypes: keyColTypes}
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
	s := "ArrayRange" + SEP + T + strconv.Itoa(len(ar.Array.ColTypes))
	for _, ct := range ar.Array.ColTypes {
		s += SEP + ct.Mangle()
	}
	return s
}
func (ar ArrayRange) Key() Type {
	return ArrayRange{
		Array: ar.Array.Key().(Array),
		Range: ar.Range.Key().(Range),
	}
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
	if oldType.Kind() == UnresolvedKind {
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
	default:
		// For other types, must be equal
		return TypeEqual(oldType, newType)
	}
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
	return canRefineTypes(oldArr.ColTypes, newArr.ColTypes)
}

func canRefineArrayRange(oldSlice, newSlice ArrayRange) bool {
	return CanRefineType(oldSlice.Array, newSlice.Array) && CanRefineType(oldSlice.Range, newSlice.Range)
}

func canRefineFunc(oldFunc, newFunc Func) bool {
	return canRefineTypes(oldFunc.Params, newFunc.Params) && canRefineTypes(oldFunc.OutTypes, newFunc.OutTypes)
}

func typeComparer(k Kind) func(a, b Type) bool {
	switch k {
	case UnresolvedKind:
		return eqUnresolved
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
	case ArrayRangeKind:
		return eqArrayRange
	default:
		return func(a, b Type) bool { panic(fmt.Sprintf("TypeEqual: unhandled kind %v", k)) }
	}
}

func eqUnresolved(a, b Type) bool { return true }

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
	if len(aa.ColTypes) != len(ba.ColTypes) {
		return false
	}
	return EqualTypes(aa.ColTypes, ba.ColTypes)
}

func eqArrayRange(a, b Type) bool {
	aar := a.(ArrayRange)
	bar := b.(ArrayRange)
	return eqArray(aar.Array, bar.Array) && eqRange(aar.Range, bar.Range)
}
