package compiler

const (
	PREFIX  = "$"   // Prefix for function names and types
	BRACKET = "$_$" // for Array<I64> -> Array$_$$I64$_$
	ON      = "$.$" // for the on keyword that separates func args from attr arguments
)

func mangle(funcName string, args []Type) string {
	mangledName := PREFIX + funcName
	for i := 0; i < len(args); i++ {
		mangledName += PREFIX + args[i].Mangle()
	}
	return mangledName
}
