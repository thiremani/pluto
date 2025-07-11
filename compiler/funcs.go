package compiler

import "tinygo.org/x/go-llvm"

const (
	PRINTF        = "printf"
	FREE          = "free"
	RANGE_I64_STR = "range_i64_str"
)

// GetFnType returns the LLVM FunctionType for a Pluto runtime helper
// name, like "printf", "free", or "range_i64_str".
func (c *Compiler) GetFnType(name string) llvm.Type {
	switch name {
	case PRINTF:
		return llvm.FunctionType(
			c.Context.Int32Type(),
			[]llvm.Type{llvm.PointerType(c.Context.Int8Type(), 0)},
			true, // Variadic
		)
	case FREE:
		return llvm.FunctionType(
			c.Context.VoidType(),
			[]llvm.Type{llvm.PointerType(c.Context.Int8Type(), 0)},
			false,
		)
	case RANGE_I64_STR:
		charPtrTy := llvm.PointerType(c.Context.Int8Type(), 0)
		i64 := c.Context.Int64Type()
		fnType := llvm.FunctionType(
			charPtrTy,
			[]llvm.Type{i64, i64, i64},
			false, // not variadic
		)
		return fnType
	default:
		panic("Unknown function name")
	}
}

func (c *Compiler) GetCFunc(name string) (llvm.Type, llvm.Value) {
	fnType := c.GetFnType(name)
	fn := c.Module.NamedFunction(name)
	if fn.IsNil() {
		fn = llvm.AddFunction(c.Module, name, fnType)
	}

	return fnType, fn
}
