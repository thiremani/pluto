package compiler

import (
	"fmt"
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
)

// Convenience, if needed in tests or messages, is handled by String() of types.

// Type is the interface for all types in our language.
type Type interface {
	String() string
	Kind() Kind
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
func (u Unresolved) String() string { return "?" } // or "Unresolved"

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

// Ptr represents a pointer type to some element type.
type Ptr struct {
	Elem Type // The type of the element being pointed to.
}

func (p Ptr) String() string {
	return fmt.Sprintf("Ptr_%s", p.Elem.String())
}

func (p Ptr) Kind() Kind {
	return PtrKind
}

// Str represents a string type.
// You can optionally store a maximum length if needed.
type Str struct {
	Length int
}

func (s Str) String() string {
	return "Str"
}

func (s Str) Kind() Kind {
	return StrKind
}

type Range struct {
	Iter Type
}

func (r Range) String() string {
	return "Range_" + r.Iter.String()
}

func (r Range) Kind() Kind {
	return RangeKind
}

type Func struct {
	Name     string
	Params   []Type
	OutTypes []Type // OutTypes are inferred in the type solver
}

func (f Func) String() string {
	return fmt.Sprintf("%s = %s(%s)", typesStr(f.OutTypes), f.Name, typesStr(f.Params))
}

func (f Func) Kind() Kind {
	return FuncKind
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
	if !equalTypeSlices(af.Params, bf.Params) {
		return false
	}
	return equalTypeSlices(af.OutTypes, bf.OutTypes)
}

func eqArray(a, b Type) bool {
	aa := a.(Array)
	ba := b.(Array)
	if len(aa.ColTypes) != len(ba.ColTypes) {
		return false
	}
	return equalTypeSlices(aa.ColTypes, ba.ColTypes)
}

func equalTypeSlices(xs, ys []Type) bool {
	if len(xs) != len(ys) {
		return false
	}
	for i := range xs {
		if !TypeEqual(xs[i], ys[i]) {
			return false
		}
	}
	return true
}
