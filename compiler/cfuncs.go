package compiler

import "tinygo.org/x/go-llvm"

const (
	PRINTF        = "printf"
	FREE          = "free"
	RANGE_I64_STR = "range_i64_str"
	F64_STR       = "f64_str"
	F32_STR       = "f32_str"
	ARR_I64_STR   = "arr_i64_str"
	ARR_F64_STR   = "arr_f64_str"
	ARR_STR_STR   = "arr_str_str"
	ARR_I64_NEW    = "arr_i64_new"
	ARR_I64_RESIZE = "arr_i64_resize"
	ARR_I64_SET    = "arr_i64_set"
	ARR_F64_NEW    = "arr_f64_new"
	ARR_F64_RESIZE = "arr_f64_resize"
	ARR_F64_SET    = "arr_f64_set"
	ARR_STR_NEW    = "arr_str_new"
	ARR_STR_RESIZE = "arr_str_resize"
	ARR_STR_SET    = "arr_str_set"
)

// GetFnType returns the LLVM FunctionType for a Pluto runtime helper
// name, like "printf", "free", or "range_i64_str".
func (c *Compiler) GetFnType(name string) llvm.Type {
	// Short helpers to reduce duplication
	charPtr := llvm.PointerType(c.Context.Int8Type(), 0)
	i64 := c.Context.Int64Type()
	f64 := c.Context.DoubleType()

	switch name {
	case PRINTF:
		return llvm.FunctionType(c.Context.Int32Type(), []llvm.Type{charPtr}, true)
	case FREE:
		return llvm.FunctionType(c.Context.VoidType(), []llvm.Type{charPtr}, false)
	case RANGE_I64_STR:
		return llvm.FunctionType(charPtr, []llvm.Type{i64, i64, i64}, false)
	case F64_STR:
		return llvm.FunctionType(charPtr, []llvm.Type{f64}, false)
	case F32_STR:
		return llvm.FunctionType(charPtr, []llvm.Type{c.Context.FloatType()}, false)
	case ARR_I64_NEW:
		return llvm.FunctionType(c.namedOpaquePtr("PtArrayI64"), nil, false)
	case ARR_I64_RESIZE, ARR_I64_SET:
		return llvm.FunctionType(c.Context.Int32Type(), []llvm.Type{c.namedOpaquePtr("PtArrayI64"), i64, i64}, false)
	case ARR_F64_NEW:
		return llvm.FunctionType(c.namedOpaquePtr("PtArrayF64"), nil, false)
	case ARR_F64_RESIZE, ARR_F64_SET:
		return llvm.FunctionType(c.Context.Int32Type(), []llvm.Type{c.namedOpaquePtr("PtArrayF64"), i64, f64}, false)
	case ARR_STR_NEW:
		return llvm.FunctionType(c.namedOpaquePtr("PtArrayStr"), nil, false)
	case ARR_STR_RESIZE:
		return llvm.FunctionType(c.Context.Int32Type(), []llvm.Type{c.namedOpaquePtr("PtArrayStr"), i64}, false)
	case ARR_STR_SET:
		return llvm.FunctionType(c.Context.Int32Type(), []llvm.Type{c.namedOpaquePtr("PtArrayStr"), i64, charPtr}, false)
	case ARR_I64_STR:
		return llvm.FunctionType(charPtr, []llvm.Type{c.namedOpaquePtr("PtArrayI64")}, false)
	case ARR_F64_STR:
		return llvm.FunctionType(charPtr, []llvm.Type{c.namedOpaquePtr("PtArrayF64")}, false)
	case ARR_STR_STR:
		return llvm.FunctionType(charPtr, []llvm.Type{c.namedOpaquePtr("PtArrayStr")}, false)
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

// namedOpaquePtr returns a pointer type to a named opaque struct, creating it if needed.
func (c *Compiler) namedOpaquePtr(name string) llvm.Type {
	st := c.Module.GetTypeByName(name)
	if st.IsNil() {
		st = c.Context.StructCreateNamed(name)
	}
	return llvm.PointerType(st, 0)
}
