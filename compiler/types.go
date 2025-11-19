package compiler

import (
	"fmt"
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
	StrKind
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
}

// Common concrete types (aliases) for readability.
// These are value-typed singletons; using them in maps/keys is safe since
// Int and Float are comparable by value.
var (
	I1  Type = Int{Width: 1}
	I64 Type = Int{Width: 64}
	F64 Type = Float{Width: 64}
)

type Unresolved struct{}

func (u Unresolved) Kind() Kind     { return UnresolvedKind }
func (u Unresolved) String() string { return "?" } // human-friendly
func (u Unresolved) Mangle() string { return PREFIX + "Unresolved" }

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
func (i Int) Mangle() string { return PREFIX + i.String() }

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
func (f Float) Mangle() string { return PREFIX + f.String() }

// Ptr represents a pointer type to some element type.
type Ptr struct {
	Elem Type // The type of the element being pointed to.
}

func (p Ptr) String() string { return "*" + p.Elem.String() }
func (p Ptr) Mangle() string { return PREFIX + "Ptr" + PREFIX + "1" + p.Elem.Mangle() }

func (p Ptr) Kind() Kind {
	return PtrKind
}

// Str represents a string type.
// Static indicates whether this string is static storage (string literal)
// or heap-allocated (from strdup, sprintf_alloc, etc.)
type Str struct {
	Length int
	Static bool // true for string literals, false for heap strings
}

func (s Str) String() string {
	return "Str"
}

func (s Str) Kind() Kind {
	return StrKind
}
func (s Str) Mangle() string { return PREFIX + "Str" }

type Range struct {
	Iter Type
}

func (r Range) String() string {
	// Represent a range's schema as T:T:T (start:stop:step)
	t := r.Iter.String()
	return t + ":" + t + ":" + t
}
func (r Range) Mangle() string { return PREFIX + "Range" + PREFIX + "1" + r.Iter.Mangle() }

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
	s := PREFIX + "Fn"
	// Params
	s += PREFIX + "P" + strconv.Itoa(len(f.Params))
	for _, p := range f.Params {
		s += p.Mangle()
	}
	// Outputs
	s += PREFIX + "O" + strconv.Itoa(len(f.OutTypes))
	for _, o := range f.OutTypes {
		s += o.Mangle()
	}
	return s
}

func (f Func) AllTypesInferred() bool {
	for _, ot := range f.OutTypes {
		if ot.Kind() == UnresolvedKind {
			return false
		}
	}
	return true
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
	s := PREFIX + "Array"
	s += PREFIX + strconv.Itoa(len(a.ColTypes))
	for _, ct := range a.ColTypes {
		s += ct.Mangle()
	}
	return s
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
	s := PREFIX + "ArrayRange"
	s += PREFIX + strconv.Itoa(len(ar.Array.ColTypes))
	for _, ct := range ar.Array.ColTypes {
		s += ct.Mangle()
	}
	return s
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
func CanRefineType(oldType, newType Type) bool {
	// Completely unresolved type can be refined to anything
	if oldType.Kind() == UnresolvedKind {
		return true
	}

	// If kinds differ, can't refine
	if oldType.Kind() != newType.Kind() {
		return false
	}

	// Check type-specific refinement
	switch oldType.Kind() {
	case ArrayKind:
		oldArr := oldType.(Array)
		// Can refine if old has unresolved element type
		return oldArr.ColTypes[0].Kind() == UnresolvedKind || TypeEqual(oldType, newType)
	case ArrayRangeKind:
		oldSlice := oldType.(ArrayRange)
		newSlice := newType.(ArrayRange)
		return CanRefineType(oldSlice.Array, newSlice.Array) && CanRefineType(oldSlice.Range, newSlice.Range)
	case PtrKind:
		oldPtr := oldType.(Ptr)
		newPtr := newType.(Ptr)
		return CanRefineType(oldPtr.Elem, newPtr.Elem)
	default:
		// For other types, must be equal
		return TypeEqual(oldType, newType)
	}
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

func eqStr(a, b Type) bool { return true }

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
