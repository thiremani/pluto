package compiler

// Builtin function names
const Print = ""

// BuiltinFunc describes a builtin function's type signature
type BuiltinFunc struct {
	ReturnTypes []Type
}

// Builtins maps builtin function names to their type information
var Builtins = map[string]*BuiltinFunc{
	Print: {ReturnTypes: []Type{}},
}
