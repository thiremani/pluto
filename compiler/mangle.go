package compiler

const (
	SEP = "$"
)

func mangle(funcName string, args []Symbol) string {
	mangledName := SEP + funcName
	for i := 0; i < len(args); i++ {
		mangledName += SEP + args[i].Type.String()
	}
	return mangledName
}
