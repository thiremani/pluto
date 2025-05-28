package compiler

import "fmt"

type Kind int

const (
	IntKind Kind = iota
	UintKind
	FloatKind
	PointerKind
	StrKind
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
	Length int // For fixed-length strings. Could be -1 for dynamic-length.
}

func (s Str) String() string {
	return "Str"
}

func (s Str) Kind() Kind {
	return StrKind
}

// Array represents an array type with a fixed length.
type Array struct {
	Elem   Type // Element type
	Length int  // Fixed length
}

func (a Array) String() string {
	return fmt.Sprintf("[%s] * %d", a.Elem.String(), a.Length)
}

func (a Array) Kind() Kind {
	return ArrayKind
}
