package compiler

import (
	"pluto/token"
	"tinygo.org/x/go-llvm"
)

// opKey is used as the key for operator functions.
type opKey struct {
	Operator  string
	LeftType  string
	RightType string
}

// compiler/operators.go

type unaryOpKey struct {
	Operator    string
	OperandType string
}

// opFunc defines the function signature for an operator function.
// It takes two Symbols and returns a new Symbol.
type opFunc func(c *Compiler, left, right Symbol) Symbol

// unaryOpFunc defines the function signature for a unary operator function.
// It takes one Symbol and returns a new Symbol
type unaryOpFunc func(c *Compiler, operand Symbol) Symbol

// defaultOps is a map between operator, types and the corresponding operator function
// For simplicity, we assume that the Symbol type’s Type field's String() method returns
// "i64" for integers and "f64" for floats.
var defaultOps = map[opKey]opFunc{
	// --- Arithmetic Operators ---
	// Addition:
	{Operator: token.SYM_ADD, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateAdd(left.Val, right.Val, "add_tmp"),
			Type: left.Type,
		}
	},
	{Operator: token.SYM_ADD, LeftType: "f64", RightType: "f64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFAdd(left.Val, right.Val, "fadd_tmp"),
			Type: left.Type,
		}
	},

	// Subtraction:
	{Operator: token.SYM_SUB, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateSub(left.Val, right.Val, "sub_tmp"),
			Type: left.Type,
		}
	},
	{Operator: token.SYM_SUB, LeftType: "f64", RightType: "f64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFSub(left.Val, right.Val, "fsub_tmp"),
			Type: left.Type,
		}
	},

	// Multiplication:
	{Operator: token.SYM_MUL, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateMul(left.Val, right.Val, "mul_tmp"),
			Type: left.Type,
		}
	},
	{Operator: token.SYM_MUL, LeftType: "f64", RightType: "f64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFMul(left.Val, right.Val, "fmul_tmp"),
			Type: left.Type,
		}
	},

	// Division:
	// For integers, this example uses integer division (CreateSDiv).
	// For ÷ operator
	{Operator: token.SYM_QUO, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateSDiv(left.Val, right.Val, "div_tmp"),
			Type: left.Type,
		}
	},
	{Operator: token.SYM_QUO, LeftType: "f64", RightType: "f64"}: func(c *Compiler, left, right Symbol) Symbol {
		// float ÷ float → float via truncation
		div := c.builder.CreateFDiv(left.Val, right.Val, "fdiv_tmp")
		// truncate toward zero to 64-bit signed integer
		trunc := c.builder.CreateFPToSI(div, c.Context.Int64Type(), "fp_to_i")
		// cast back to double
		res := c.builder.CreateSIToFP(trunc, c.Context.DoubleType(), "i_to_fp")
		return Symbol{
			Val:  res,
			Type: left.Type,
		}
	},

	// For division, if both operands are integers, promote them to float and do float division.
	{Operator: token.SYM_DIV, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		leftFP := c.builder.CreateSIToFP(left.Val, c.Context.DoubleType(), "cast_to_f64")
		rightFP := c.builder.CreateSIToFP(right.Val, c.Context.DoubleType(), "cast_to_f64")
		return Symbol{
			Val:  c.builder.CreateFDiv(leftFP, rightFP, "fdiv_tmp"),
			Type: Float{Width: 64},
		}
	},
	{Operator: token.SYM_DIV, LeftType: "f64", RightType: "f64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFDiv(left.Val, right.Val, "fdiv_tmp"),
			Type: left.Type,
		}
	},

	// Exponentiation (^):
	{Operator: token.SYM_EXP, LeftType: "f64", RightType: "f64"}: func(c *Compiler, left, right Symbol) Symbol {
		// Register exponentiation operator for float values.
		powType := llvm.FunctionType(c.Context.DoubleType(), []llvm.Type{c.Context.DoubleType(), c.Context.DoubleType()}, false)
		powFunc := c.Module.NamedFunction("llvm.pow.f64")
		if powFunc.IsNil() {
			powFunc = llvm.AddFunction(c.Module, "llvm.pow.f64", powType)
		}
		return Symbol{
			Val:  c.builder.CreateCall(powType, powFunc, []llvm.Value{left.Val, right.Val}, "pow_tmp"),
			Type: left.Type,
		}
	},

	// --- Comparison Operators ---
	// Equality (==)
	{Operator: token.SYM_EQL, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateICmp(llvm.IntEQ, left.Val, right.Val, "eq_i64"),
			Type: Int{Width: 64}, // Representing boolean as an int (0 or 1)
		}
	},
	{Operator: token.SYM_EQL, LeftType: "f64", RightType: "f64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFCmp(llvm.FloatUEQ, left.Val, right.Val, "eq_f64"),
			Type: Int{Width: 64},
		}
	},

	// Not Equal (!=)
	{Operator: token.SYM_NEQ, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateICmp(llvm.IntNE, left.Val, right.Val, "neq_i64"),
			Type: Int{Width: 64},
		}
	},
	{Operator: token.SYM_NEQ, LeftType: "f64", RightType: "f64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFCmp(llvm.FloatUNE, left.Val, right.Val, "neq_f64"),
			Type: Int{Width: 64},
		}
	},

	// Less Than (<)
	{Operator: token.SYM_LSS, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateICmp(llvm.IntSLT, left.Val, right.Val, "lt_i64"),
			Type: Int{Width: 64},
		}
	},
	{Operator: token.SYM_LSS, LeftType: "f64", RightType: "f64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFCmp(llvm.FloatULT, left.Val, right.Val, "lt_f64"),
			Type: Int{Width: 64},
		}
	},

	// Less Than or Equal (<=)
	{Operator: token.SYM_LEQ, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateICmp(llvm.IntSLE, left.Val, right.Val, "le_i64"),
			Type: Int{Width: 64},
		}
	},
	{Operator: token.SYM_LEQ, LeftType: "f64", RightType: "f64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFCmp(llvm.FloatULE, left.Val, right.Val, "le_f64"),
			Type: Int{Width: 64},
		}
	},

	// Greater Than (>)
	{Operator: token.SYM_GTR, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateICmp(llvm.IntSGT, left.Val, right.Val, "gt_i64"),
			Type: Int{Width: 64},
		}
	},
	{Operator: token.SYM_GTR, LeftType: "f64", RightType: "f64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFCmp(llvm.FloatUGT, left.Val, right.Val, "gt_f64"),
			Type: Int{Width: 64},
		}
	},

	// Greater Than or Equal (>=)
	{Operator: token.SYM_GEQ, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateICmp(llvm.IntSGE, left.Val, right.Val, "ge_i64"),
			Type: Int{Width: 64},
		}
	},
	{Operator: token.SYM_GEQ, LeftType: "f64", RightType: "f64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFCmp(llvm.FloatUGE, left.Val, right.Val, "ge_f64"),
			Type: Int{Width: 64},
		}
	},

	// Bitwise AND
	{Operator: token.SYM_AND, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateAnd(left.Val, right.Val, "and_tmp"),
			Type: Int{Width: 64},
		}
	},

	// Bitwise OR
	{Operator: token.SYM_OR, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateOr(left.Val, right.Val, "or_tmp"),
			Type: Int{Width: 64},
		}
	},

	// Bitwise XOR
	{Operator: token.SYM_XOR, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateXor(left.Val, right.Val, "xor_tmp"),
			Type: Int{Width: 64},
		}
	},

	// Integer Modulo (Signed)
	{Operator: token.SYM_MOD, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateSRem(left.Val, right.Val, "srem_tmp"),
			Type: Int{Width: 64},
		}
	},
	// Floating-Point Modulo (via fmod)
	{Operator: token.SYM_MOD, LeftType: "f64", RightType: "f64"}: func(c *Compiler, left, right Symbol) Symbol {

		return Symbol{
			Val:  c.builder.CreateFRem(left.Val, right.Val, "frem_tmp"),
			Type: Float{Width: 64},
		}
	},

	// Left Shift
	{Operator: token.SYM_SHL, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateShl(left.Val, right.Val, "shl_tmp"),
			Type: Int{Width: 64},
		}
	},

	// Arithmetic Right Shift (Signed)
	{Operator: token.SYM_ASR, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateAShr(left.Val, right.Val, "ashr_tmp"),
			Type: Int{Width: 64},
		}
	},

	// Logical Right Shift
	{Operator: token.SYM_SHR, LeftType: "i64", RightType: "i64"}: func(c *Compiler, left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateLShr(left.Val, right.Val, "lshr_tmp"),
			Type: Int{Width: 64},
		}
	},
}

var defaultUnaryOps = map[unaryOpKey]unaryOpFunc{
	// Unary Minus (-)
	{Operator: token.SYM_SUB, OperandType: "i64"}: func(c *Compiler, operand Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateNeg(operand.Val, "neg_tmp"),
			Type: Int{Width: 64},
		}
	},
	{Operator: token.SYM_SUB, OperandType: "f64"}: func(c *Compiler, operand Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFNeg(operand.Val, "fneg_tmp"),
			Type: Float{Width: 64},
		}
	},

	// Bitwise NOT
	{Operator: token.SYM_TILDE, OperandType: "i64"}: func(c *Compiler, operand Symbol) Symbol {
		allOnes := llvm.ConstAllOnes(c.Context.Int64Type())
		return Symbol{
			Val:  c.builder.CreateXor(operand.Val, allOnes, "not_tmp"),
			Type: Int{Width: 64},
		}
	},

	// Logical NOT
	{Operator: token.SYM_BANG, OperandType: "i64"}: func(c *Compiler, operand Symbol) Symbol {
		zero := llvm.ConstInt(c.Context.Int64Type(), 0, false)
		cmp := c.builder.CreateICmp(llvm.IntEQ, operand.Val, zero, "not_cmp")
		return Symbol{
			Val:  c.builder.CreateZExt(cmp, c.Context.Int64Type(), "not_bool"),
			Type: Int{Width: 64},
		}
	},
}
