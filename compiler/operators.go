package compiler

import (
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

// opKey is used as the key for operator functions.
type opKey struct {
	Operator  string
	LeftType  Type
	RightType Type
}

type unaryOpKey struct {
	Operator    string
	OperandType Type
}

// opFunc defines the function signature for an operator function.
// It takes two *Symbols and returns a new *Symbol.
type opFunc func(c *Compiler, left, right *Symbol, compile bool) *Symbol

// unaryOpFunc defines the function signature for a unary operator function.
// It takes one *Symbol and returns a new *Symbol
type unaryOpFunc func(c *Compiler, operand *Symbol, compile bool) *Symbol

// --- Multiplication helper functions (shared by * and ⋅) ---

var mulI64I64 = func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
	s = &Symbol{}
	s.Type = left.Type
	if !compile {
		return
	}
	s.Val = c.builder.CreateMul(left.Val, right.Val, "mul_tmp")
	return
}

var mulF64F64 = func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
	s = &Symbol{}
	s.Type = left.Type
	if !compile {
		return
	}
	s.Val = c.builder.CreateFMul(left.Val, right.Val, "fmul_tmp")
	return
}

var mulI64F64 = func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
	s = &Symbol{}
	s.Type = right.Type
	if !compile {
		return
	}
	s.Val = c.builder.CreateFMul(c.builder.CreateSIToFP(left.Val, c.Context.DoubleType(), "cast_to_float"), right.Val, "fmul_if_tmp")
	return
}

var mulF64I64 = func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
	s = &Symbol{}
	s.Type = left.Type
	if !compile {
		return
	}
	s.Val = c.builder.CreateFMul(left.Val, c.builder.CreateSIToFP(right.Val, c.Context.DoubleType(), "cast_to_float"), "fmul_fi_tmp")
	return
}

// strConcatOp concatenates two strings (StrG or StrH) and returns StrH
var strConcatOp = func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
	s = &Symbol{}
	s.Type = StrH{}
	if !compile {
		return
	}

	charPtrType := llvm.PointerType(c.Context.Int8Type(), 0)
	strConcatType := llvm.FunctionType(charPtrType, []llvm.Type{charPtrType, charPtrType}, false)
	strConcatFunc := c.Module.NamedFunction("str_concat")
	if strConcatFunc.IsNil() {
		strConcatFunc = llvm.AddFunction(c.Module, "str_concat", strConcatType)
	}

	s.Val = c.builder.CreateCall(strConcatType, strConcatFunc, []llvm.Value{left.Val, right.Val}, "str_concat_result")
	return
}

// defaultOps maps (operator, left type, right type) to the lowering function.
// Keys use concrete Type values (e.g., I64, F64) instead of strings
// to avoid brittleness from relying on String() formatting.
var defaultOps = map[opKey]opFunc{
	// --- Arithmetic Operators ---
	// Addition:
	{Operator: token.SYM_ADD, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateAdd(left.Val, right.Val, "add_tmp")
		return
	},
	{Operator: token.SYM_ADD, LeftType: F64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateFAdd(left.Val, right.Val, "fadd_tmp")
		return
	},

	{Operator: token.SYM_ADD, LeftType: I64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = right.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateFAdd(c.builder.CreateSIToFP(left.Val, c.Context.DoubleType(), "cast_to_float"), right.Val, "fadd_if_tmp")
		return
	},
	{Operator: token.SYM_ADD, LeftType: F64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateFAdd(left.Val, c.builder.CreateSIToFP(right.Val, c.Context.DoubleType(), "cast_to_float"), "fadd_fi_tmp")
		return
	},

	// Subtraction:
	{Operator: token.SYM_SUB, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateSub(left.Val, right.Val, "sub_tmp")
		return
	},
	{Operator: token.SYM_SUB, LeftType: F64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateFSub(left.Val, right.Val, "fsub_tmp")
		return
	},
	{Operator: token.SYM_SUB, LeftType: I64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = right.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateFSub(c.builder.CreateSIToFP(left.Val, c.Context.DoubleType(), "cast_to_float"), right.Val, "fsub_if_tmp")
		return
	},
	{Operator: token.SYM_SUB, LeftType: F64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateFSub(left.Val, c.builder.CreateSIToFP(right.Val, c.Context.DoubleType(), "cast_to_float"), "fsub_fi_tmp")
		return
	},

	// Multiplication (* and ⋅): Both use the same implementation
	{Operator: token.SYM_MUL, LeftType: I64, RightType: I64}:      mulI64I64,
	{Operator: token.SYM_MUL, LeftType: F64, RightType: F64}:      mulF64F64,
	{Operator: token.SYM_MUL, LeftType: I64, RightType: F64}:      mulI64F64,
	{Operator: token.SYM_MUL, LeftType: F64, RightType: I64}:      mulF64I64,
	{Operator: token.SYM_IMPL_MUL, LeftType: I64, RightType: I64}: mulI64I64,
	{Operator: token.SYM_IMPL_MUL, LeftType: F64, RightType: F64}: mulF64F64,
	{Operator: token.SYM_IMPL_MUL, LeftType: I64, RightType: F64}: mulI64F64,
	{Operator: token.SYM_IMPL_MUL, LeftType: F64, RightType: I64}: mulF64I64,

	// Division:
	// For integers, this example uses integer division (CreateSDiv).
	// For ÷ operator
	{Operator: token.SYM_QUO, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateSDiv(left.Val, right.Val, "div_tmp")
		return
	},
	{Operator: token.SYM_QUO, LeftType: F64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = floatQuo(c, left.Val, right.Val)
		return
	},
	{Operator: token.SYM_QUO, LeftType: I64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = right.Type
		if !compile {
			return
		}

		s.Val = floatQuo(c, c.builder.CreateSIToFP(left.Val, c.Context.DoubleType(), "cast_to_float"), right.Val)
		return
	},
	{Operator: token.SYM_QUO, LeftType: F64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = floatQuo(c, left.Val, c.builder.CreateSIToFP(right.Val, c.Context.DoubleType(), "cast_to_float"))
		return
	},

	// For division, if both operands are integers, promote them to float and do float division.
	{Operator: token.SYM_DIV, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Float{Width: 64}
		if !compile {
			return
		}

		leftFP := c.builder.CreateSIToFP(left.Val, c.Context.DoubleType(), "cast_to_F64")
		rightFP := c.builder.CreateSIToFP(right.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFDiv(leftFP, rightFP, "fdiv_ii_tmp")
		return
	},
	{Operator: token.SYM_DIV, LeftType: F64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateFDiv(left.Val, right.Val, "fdiv_tmp")
		return
	},
	{Operator: token.SYM_DIV, LeftType: I64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = right.Type
		if !compile {
			return
		}

		leftFP := c.builder.CreateSIToFP(left.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFDiv(leftFP, right.Val, "fdiv_if_tmp")
		return
	},
	{Operator: token.SYM_DIV, LeftType: F64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		rightFP := c.builder.CreateSIToFP(right.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFDiv(left.Val, rightFP, "fdiv_fi_tmp")
		return
	},

	// Exponentiation (^):
	{Operator: token.SYM_EXP, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Float{Width: 64}
		if !compile {
			return
		}

		leftFP := c.builder.CreateSIToFP(left.Val, c.Context.DoubleType(), "cast_to_F64")
		rightFP := c.builder.CreateSIToFP(right.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = floatExp(c, leftFP, rightFP)
		return
	},
	{Operator: token.SYM_EXP, LeftType: F64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = floatExp(c, left.Val, right.Val)
		return
	},
	{Operator: token.SYM_EXP, LeftType: I64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = right.Type
		if !compile {
			return
		}

		leftFP := c.builder.CreateSIToFP(left.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = floatExp(c, leftFP, right.Val)
		return
	},
	{Operator: token.SYM_EXP, LeftType: F64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		rightFP := c.builder.CreateSIToFP(right.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = floatExp(c, left.Val, rightFP)
		return
	},

	// --- Comparison Operators ---
	// Equality (==)
	{Operator: token.SYM_EQL, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		s.Val = c.builder.CreateICmp(llvm.IntEQ, left.Val, right.Val, "eq_I64")
		return
	},
	{Operator: token.SYM_EQL, LeftType: F64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		s.Val = c.builder.CreateFCmp(llvm.FloatOEQ, left.Val, right.Val, "eq_F64")
		return
	},
	{Operator: token.SYM_EQL, LeftType: I64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		leftFP := c.builder.CreateSIToFP(left.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFCmp(llvm.FloatOEQ, leftFP, right.Val, "eq_I64_F64")
		return
	},
	{Operator: token.SYM_EQL, LeftType: F64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		rightFP := c.builder.CreateSIToFP(right.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFCmp(llvm.FloatOEQ, left.Val, rightFP, "eq_F64_I64")
		return
	},

	// Not Equal (!=)
	{Operator: token.SYM_NEQ, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		s.Val = c.builder.CreateICmp(llvm.IntNE, left.Val, right.Val, "neq_I64")
		return
	},
	{Operator: token.SYM_NEQ, LeftType: F64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		s.Val = c.builder.CreateFCmp(llvm.FloatONE, left.Val, right.Val, "neq_F64")
		return
	},
	{Operator: token.SYM_NEQ, LeftType: I64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		leftFP := c.builder.CreateSIToFP(left.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFCmp(llvm.FloatONE, leftFP, right.Val, "neq_I64_F64")
		return
	},
	{Operator: token.SYM_NEQ, LeftType: F64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		rightFP := c.builder.CreateSIToFP(right.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFCmp(llvm.FloatONE, left.Val, rightFP, "neq_F64_I64")
		return
	},

	// Less Than (<)
	{Operator: token.SYM_LSS, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}
		s.Val = c.builder.CreateICmp(llvm.IntSLT, left.Val, right.Val, "lt_I64")
		return
	},
	{Operator: token.SYM_LSS, LeftType: F64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}
		s.Val = c.builder.CreateFCmp(llvm.FloatOLT, left.Val, right.Val, "lt_F64")
		return
	},
	{Operator: token.SYM_LSS, LeftType: I64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		leftFP := c.builder.CreateSIToFP(left.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFCmp(llvm.FloatOLT, leftFP, right.Val, "lt_I64_F64")
		return
	},
	{Operator: token.SYM_LSS, LeftType: F64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		rightFP := c.builder.CreateSIToFP(right.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFCmp(llvm.FloatOLT, left.Val, rightFP, "lt_F64_I64")
		return
	},

	// Less Than or Equal (<=)
	{Operator: token.SYM_LEQ, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		s.Val = c.builder.CreateICmp(llvm.IntSLE, left.Val, right.Val, "le_I64")
		return
	},
	{Operator: token.SYM_LEQ, LeftType: F64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}
		s.Val = c.builder.CreateFCmp(llvm.FloatOLE, left.Val, right.Val, "le_F64")
		return
	},
	{Operator: token.SYM_LEQ, LeftType: I64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		leftFP := c.builder.CreateSIToFP(left.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFCmp(llvm.FloatOLE, leftFP, right.Val, "le_I64_F64")
		return
	},
	{Operator: token.SYM_LEQ, LeftType: F64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		rightFP := c.builder.CreateSIToFP(right.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFCmp(llvm.FloatOLE, left.Val, rightFP, "le_F64_I64")
		return
	},

	// Greater Than (>)
	{Operator: token.SYM_GTR, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		s.Val = c.builder.CreateICmp(llvm.IntSGT, left.Val, right.Val, "gt_I64")
		return
	},
	{Operator: token.SYM_GTR, LeftType: F64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}
		s.Val = c.builder.CreateFCmp(llvm.FloatOGT, left.Val, right.Val, "gt_F64")
		return
	},
	{Operator: token.SYM_GTR, LeftType: I64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		leftFP := c.builder.CreateSIToFP(left.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFCmp(llvm.FloatOGT, leftFP, right.Val, "gt_I64_F64")
		return
	},
	{Operator: token.SYM_GTR, LeftType: F64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		rightFP := c.builder.CreateSIToFP(right.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFCmp(llvm.FloatOGT, left.Val, rightFP, "gt_F64_I64")
		return
	},

	// Greater Than or Equal (>=)
	{Operator: token.SYM_GEQ, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		s.Val = c.builder.CreateICmp(llvm.IntSGE, left.Val, right.Val, "ge_I64")
		return
	},
	{Operator: token.SYM_GEQ, LeftType: F64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}
		s.Val = c.builder.CreateFCmp(llvm.FloatOGE, left.Val, right.Val, "ge_F64")
		return
	},
	{Operator: token.SYM_GEQ, LeftType: I64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		leftFP := c.builder.CreateSIToFP(left.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFCmp(llvm.FloatOGE, leftFP, right.Val, "ge_I64_F64")
		return
	},
	{Operator: token.SYM_GEQ, LeftType: F64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Int{Width: 1}
		if !compile {
			return
		}

		rightFP := c.builder.CreateSIToFP(right.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFCmp(llvm.FloatOGE, left.Val, rightFP, "ge_F64_I64")
		return
	},

	// Bitwise AND
	{Operator: token.SYM_AND, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateAnd(left.Val, right.Val, "and_tmp")
		return
	},

	// Bitwise OR
	{Operator: token.SYM_OR, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateOr(left.Val, right.Val, "or_tmp")
		return
	},

	// Bitwise XOR
	{Operator: token.SYM_XOR, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateXor(left.Val, right.Val, "xor_tmp")
		return
	},

	// Integer Modulo (Signed)
	// Note: Modulo by zero has undefined behavior (uses LLVM SRem)
	{Operator: token.SYM_MOD, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateSRem(left.Val, right.Val, "srem_tmp")
		return
	},

	// Floating-Point Modulo
	{Operator: token.SYM_MOD, LeftType: F64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateFRem(left.Val, right.Val, "frem_tmp")
		return
	},
	{Operator: token.SYM_MOD, LeftType: I64, RightType: F64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = right.Type
		if !compile {
			return
		}

		leftFP := c.builder.CreateSIToFP(left.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFRem(leftFP, right.Val, "frem_if_tmp")
		return
	},
	{Operator: token.SYM_MOD, LeftType: F64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		rightFP := c.builder.CreateSIToFP(right.Val, c.Context.DoubleType(), "cast_to_F64")
		s.Val = c.builder.CreateFRem(left.Val, rightFP, "frem_fi_tmp")
		return
	},

	// Left Shift
	{Operator: token.SYM_SHL, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateShl(left.Val, right.Val, "shl_tmp")
		return
	},

	// Arithmetic Right Shift (Signed)
	{Operator: token.SYM_ASR, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateAShr(left.Val, right.Val, "ashr_tmp")
		return
	},

	// Logical Right Shift
	{Operator: token.SYM_SHR, LeftType: I64, RightType: I64}: func(c *Compiler, left, right *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = left.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateLShr(left.Val, right.Val, "lshr_tmp")
		return
	},

	// --- String Concatenation (all combinations produce StrH) ---
	{Operator: token.SYM_CONCAT, LeftType: StrH{}, RightType: StrH{}}: strConcatOp,
	{Operator: token.SYM_CONCAT, LeftType: StrG{}, RightType: StrG{}}: strConcatOp,
	{Operator: token.SYM_CONCAT, LeftType: StrG{}, RightType: StrH{}}: strConcatOp,
	{Operator: token.SYM_CONCAT, LeftType: StrH{}, RightType: StrG{}}: strConcatOp,
}

var defaultUnaryOps = map[unaryOpKey]unaryOpFunc{
	// Unary Minus (-)
	{Operator: token.SYM_SUB, OperandType: I64}: func(c *Compiler, operand *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = operand.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateNeg(operand.Val, "neg_tmp")
		return
	},
	{Operator: token.SYM_SUB, OperandType: F64}: func(c *Compiler, operand *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = operand.Type
		if !compile {
			return
		}

		s.Val = c.builder.CreateFNeg(operand.Val, "fneg_tmp")
		return
	},

	// Bitwise NOT
	{Operator: token.SYM_TILDE, OperandType: I64}: func(c *Compiler, operand *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = operand.Type
		if !compile {
			return
		}

		allOnes := llvm.ConstAllOnes(c.Context.Int64Type())
		s.Val = c.builder.CreateXor(operand.Val, allOnes, "not_tmp")
		return
	},

	// Logical NOT
	{Operator: token.SYM_BANG, OperandType: I64}: func(c *Compiler, operand *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = operand.Type
		if !compile {
			return
		}

		zero := llvm.ConstInt(c.Context.Int64Type(), 0, false)
		cmp := c.builder.CreateICmp(llvm.IntEQ, operand.Val, zero, "not_cmp")
		s.Val = c.builder.CreateZExt(cmp, c.Context.Int64Type(), "not_bool")
		return
	},

	// Square root (√)
	{Operator: token.SYM_SQRT, OperandType: F64}: func(c *Compiler, operand *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = operand.Type
		if !compile {
			return
		}

		s.Val = floatSqrt(c, operand.Val)
		return
	},
	{Operator: token.SYM_SQRT, OperandType: I64}: func(c *Compiler, operand *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Float{Width: 64}
		if !compile {
			return
		}

		floatVal := c.builder.CreateSIToFP(operand.Val, c.Context.DoubleType(), "int_to_float")
		s.Val = floatSqrt(c, floatVal)
		return
	},

	// Cube root (∛)
	{Operator: token.SYM_CBRT, OperandType: F64}: func(c *Compiler, operand *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = operand.Type
		if !compile {
			return
		}

		s.Val = floatCbrt(c, operand.Val)
		return
	},
	{Operator: token.SYM_CBRT, OperandType: I64}: func(c *Compiler, operand *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Float{Width: 64}
		if !compile {
			return
		}

		floatVal := c.builder.CreateSIToFP(operand.Val, c.Context.DoubleType(), "int_to_float")
		s.Val = floatCbrt(c, floatVal)
		return
	},

	// Fourth root (∜)
	{Operator: token.SYM_FTHRT, OperandType: F64}: func(c *Compiler, operand *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = operand.Type
		if !compile {
			return
		}

		sqrt := floatSqrt(c, operand.Val)
		s.Val = floatSqrt(c, sqrt)
		return
	},
	{Operator: token.SYM_FTHRT, OperandType: I64}: func(c *Compiler, operand *Symbol, compile bool) (s *Symbol) {
		s = &Symbol{}
		s.Type = Float{Width: 64}
		if !compile {
			return
		}

		floatVal := c.builder.CreateSIToFP(operand.Val, c.Context.DoubleType(), "int_to_float")
		sqrt := floatSqrt(c, floatVal)
		s.Val = floatSqrt(c, sqrt)
		return
	},
}

// float = float ÷ float via truncation
// floatQuo assumes left and right are both of type F64
func floatQuo(c *Compiler, left, right llvm.Value) llvm.Value {
	div := c.builder.CreateFDiv(left, right, "fdiv_tmp")
	// get or declare the llvm.trunc.f64 intrinsic
	truncType := llvm.FunctionType(c.Context.DoubleType(), []llvm.Type{c.Context.DoubleType()}, false)
	truncFn := c.Module.NamedFunction("llvm.trunc.f64")
	if truncFn.IsNil() {
		truncFn = llvm.AddFunction(c.Module, "llvm.trunc.f64", truncType)
	}

	return c.builder.CreateCall(truncType, truncFn, []llvm.Value{div}, "trunc_tmp")
}

// floatExp assumes left and right are both of type F64
func floatExp(c *Compiler, left, right llvm.Value) llvm.Value {
	// Register exponentiation operator for float values.
	powType := llvm.FunctionType(c.Context.DoubleType(), []llvm.Type{c.Context.DoubleType(), c.Context.DoubleType()}, false)
	powFunc := c.Module.NamedFunction("llvm.pow.f64")
	if powFunc.IsNil() {
		powFunc = llvm.AddFunction(c.Module, "llvm.pow.f64", powType)
	}
	return c.builder.CreateCall(powType, powFunc, []llvm.Value{left, right}, "pow_tmp")
}

// floatSqrt assumes operand is of type F64
func floatSqrt(c *Compiler, x llvm.Value) llvm.Value {
	// Use LLVM sqrt intrinsic for best performance
	sqrtType := llvm.FunctionType(c.Context.DoubleType(), []llvm.Type{c.Context.DoubleType()}, false)
	sqrtFunc := c.Module.NamedFunction("llvm.sqrt.f64")
	if sqrtFunc.IsNil() {
		sqrtFunc = llvm.AddFunction(c.Module, "llvm.sqrt.f64", sqrtType)
	}
	return c.builder.CreateCall(sqrtType, sqrtFunc, []llvm.Value{x}, "sqrt_tmp")
}

func floatCbrt(c *Compiler, x llvm.Value) llvm.Value {
	cbrtType := llvm.FunctionType(c.Context.DoubleType(),
		[]llvm.Type{c.Context.DoubleType()}, false)
	cbrtFunc := c.Module.NamedFunction("cbrt")
	if cbrtFunc.IsNil() {
		cbrtFunc = llvm.AddFunction(c.Module, "cbrt", cbrtType)
	}
	// Optionally mark "nounwind", "readnone" if your toolchain’s libm allows.
	return c.builder.CreateCall(cbrtType, cbrtFunc, []llvm.Value{x}, "cbrt_tmp")
}
