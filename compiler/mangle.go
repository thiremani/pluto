package compiler

const (
	PREFIX  = "$"   // Prefix for function names and types
	BRACKET = "$_$" // for Array<I64> -> Array$_$$I64$_$
	ON      = "$.$" // for the on keyword that separates func args from attr arguments
)

func mangle(funcName string, args []Type) string {
	mangledName := PREFIX + funcName
	for i := 0; i < len(args); i++ {
		mangledName += PREFIX + encodeType(args[i])
	}
	return mangledName
}

// encodeType returns a collision-resistant encoding for types in mangled names.
// It avoids relying on Type.String() for composite types to prevent clashes with
// potential user-defined names like "Range_I64". We reserve a dot '.' to
// separate built-in type constructors from their parameters ('.' is not allowed
// in Pluto identifiers and is well-supported in LLVM symbol names).
// Examples:
//
//	I64        -> I64
//	F64        -> F64
//	Str        -> Str
//	Ptr{I64}   -> Ptr.I64
//	Range{I64} -> Range.I64
//	Array{I64} -> Array.I64 (multi-column: Array.I64.F64)
func encodeType(t Type) string {
	switch t.Kind() {
	case IntKind:
		return t.String() // I{width}
	case FloatKind:
		return t.String() // F{width}
	case StrKind:
		return t.String() // Str
	case PtrKind:
		p := t.(Ptr)
		return "Ptr." + encodeType(p.Elem)
	case RangeKind:
		r := t.(Range)
		return "Range." + encodeType(r.Iter)
	case ArrayKind:
		a := t.(Array)
		// Encode only schema; ignore headers/length
		if len(a.ColTypes) == 0 {
			return "Array" // degenerate, shouldn't happen
		}
		s := "Array"
		for _, ct := range a.ColTypes {
			s += "." + encodeType(ct)
		}
		return s
	case FuncKind:
		f := t.(Func)
		// Encode function types as Fn.<params>-><outs>
		s := "Fn"
		for _, p := range f.Params {
			s += "." + encodeType(p)
		}
		s += "->"
		for _, o := range f.OutTypes {
			s += "." + encodeType(o)
		}
		return s
	default:
		// Unresolved, etc. Fall back to String for diagnostics; avoid in stable mangle.
		return t.String()
	}
}
