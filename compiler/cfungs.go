package compiler

import "tinygo.org/x/go-llvm"

const (
    PRINTF        = "printf"
    FREE          = "free"
    RANGE_I64_STR = "range_i64_str"
    F64_STR       = "f64_str"
    F32_STR       = "f32_str"
    PT_I64_NEW    = "pt_i64_new"
    PT_I64_RESIZE = "pt_i64_resize"
    PT_I64_SET    = "pt_i64_set"
    PT_F64_NEW    = "pt_f64_new"
    PT_F64_RESIZE = "pt_f64_resize"
    PT_F64_SET    = "pt_f64_set"
    PT_STR_NEW    = "pt_str_new"
    PT_STR_RESIZE = "pt_str_resize"
    PT_STR_SET    = "pt_str_set"
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
	case F64_STR:
		{
			charPtrTy := llvm.PointerType(c.Context.Int8Type(), 0) // char*
			return llvm.FunctionType(
				charPtrTy,
				[]llvm.Type{c.Context.DoubleType()}, // double
				false,
			)
		}
    case F32_STR:
        {
            charPtrTy := llvm.PointerType(c.Context.Int8Type(), 0) // char*
            return llvm.FunctionType(
                charPtrTy,
                []llvm.Type{c.Context.FloatType()}, // float
                false,
            )
        }
    case PT_I64_NEW:
        return llvm.FunctionType(
            c.namedOpaquePtr("struct.PtArrayI64"),
            []llvm.Type{},
            false,
        )
    case PT_I64_RESIZE:
        return llvm.FunctionType(
            c.Context.Int32Type(),
            []llvm.Type{c.namedOpaquePtr("struct.PtArrayI64"), c.Context.Int64Type(), c.Context.Int64Type()},
            false,
        )
    case PT_I64_SET:
        return llvm.FunctionType(
            c.Context.Int32Type(),
            []llvm.Type{c.namedOpaquePtr("struct.PtArrayI64"), c.Context.Int64Type(), c.Context.Int64Type()},
            false,
        )
    case PT_F64_NEW:
        return llvm.FunctionType(
            c.namedOpaquePtr("struct.PtArrayF64"),
            []llvm.Type{},
            false,
        )
    case PT_F64_RESIZE:
        return llvm.FunctionType(
            c.Context.Int32Type(),
            []llvm.Type{c.namedOpaquePtr("struct.PtArrayF64"), c.Context.Int64Type(), c.Context.DoubleType()},
            false,
        )
    case PT_F64_SET:
        return llvm.FunctionType(
            c.Context.Int32Type(),
            []llvm.Type{c.namedOpaquePtr("struct.PtArrayF64"), c.Context.Int64Type(), c.Context.DoubleType()},
            false,
        )
    case PT_STR_NEW:
        return llvm.FunctionType(
            c.namedOpaquePtr("struct.PtArrayStr"),
            []llvm.Type{},
            false,
        )
    case PT_STR_RESIZE:
        return llvm.FunctionType(
            c.Context.Int32Type(),
            []llvm.Type{c.namedOpaquePtr("struct.PtArrayStr"), c.Context.Int64Type()},
            false,
        )
    case PT_STR_SET:
        return llvm.FunctionType(
            c.Context.Int32Type(),
            []llvm.Type{c.namedOpaquePtr("struct.PtArrayStr"), c.Context.Int64Type(), llvm.PointerType(c.Context.Int8Type(), 0)},
            false,
        )
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
