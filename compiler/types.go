package compiler

import "fmt"

type Kind int

const (
	IntKind Kind = iota
	FloatKind
	PointerKind
	StringKind
	ArrayKind
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
	return fmt.Sprintf("i%d", i.Width)
}

func (i Int) Kind() Kind {
	return IntKind
}

// Float represents a floating-point type with a given precision.
type Float struct {
	Width uint32 // e.g. 32, 64
}

func (f Float) String() string {
	return fmt.Sprintf("f%d", f.Width)
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

// String represents a string type.
// You can optionally store a maximum length if needed.
type String struct {
	Length int // For fixed-length strings. Could be -1 for dynamic-length.
}

func (s String) String() string {
	if s.Length >= 0 {
		return fmt.Sprintf("string[%d]", s.Length)
	}
	return "string"
}

func (s String) Kind() Kind {
	return StringKind
}

// Array represents an array type with a fixed length.
type Array struct {
	Elem   Type // Element type
	Length int  // Fixed length
}

func (a Array) String() string {
	return fmt.Sprintf("[%d]%s", a.Length, a.Elem.String())
}

func (a Array) Kind() Kind {
	return ArrayKind
}
