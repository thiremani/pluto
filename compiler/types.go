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
	PointerKind
	StrKind
	RangeKind
	FuncKind
	ArrayKind
)

const (
	I64 = "I64"
	F64 = "F64"
)

// Type is the interface for all types in our language.
type Type interface {
	String() string
	Kind() Kind
}

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

// Pointer represents a pointer type to some element type.
type Pointer struct {
	Elem Type // The type of the element being pointed to.
}

func (p Pointer) String() string {
	return fmt.Sprintf("*%s", p.Elem.String())
}

func (p Pointer) Kind() Kind {
	return PointerKind
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
	Name    string
	Params  []Type
	Outputs []Type
}

func (f Func) String() string {
	return fmt.Sprintf("%s = %s(%s)", typesStr(f.Outputs), f.Name, typesStr(f.Params))
}

func (f Func) Kind() Kind {
	return FuncKind
}

func (f Func) AllTypesInferred() bool {
	for _, ot := range f.Outputs {
		if ot.Kind() == UnresolvedKind {
			return false
		}
	}
	return true
}

// Array represents an array type with a fixed length.
type Array struct {
	Elem   Type // Element type
	Length int  // Fixed length
}

func (a Array) String() string {
	return fmt.Sprintf("%d * [%s]", a.Length, a.Elem.String())
}

func (a Array) Kind() Kind {
	return ArrayKind
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
		if l.String() != right[i].String() {
			return false
		}
	}

	return true
}
