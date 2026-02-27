package types

var reservedTypeNames = []string{
	"Int",
	"Float",
	"Str",
	"StrG",
	"StrH",
	"StrS",
	"I1",
	"I8",
	"I16",
	"I32",
	"I64",
	"U8",
	"U16",
	"U32",
	"U64",
	"F32",
	"F64",
	"Ptr",
	"Range",
	"Array",
	"ArrayRange",
	"Func",
	"Struct",
}

var reservedTypeSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(reservedTypeNames))
	for _, t := range reservedTypeNames {
		m[t] = struct{}{}
	}
	return m
}()

// ReservedTypeNames returns a copy of source-level reserved type names.
func ReservedTypeNames() []string {
	return append([]string(nil), reservedTypeNames...)
}

// IsReservedTypeName reports whether name is reserved for built-in/compiler types.
func IsReservedTypeName(name string) bool {
	_, ok := reservedTypeSet[name]
	return ok
}
