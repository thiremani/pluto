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

// opFunc defines the function signature for an operator function.
// It takes two Symbols and returns a new Symbol.
type opFunc func(left, right Symbol) Symbol

// initOpFuncs initializes the operator functions mapping.
// For simplicity, we assume that the Symbol type’s Type field's String() method returns
// "i64" for integers and "f64" for floats.
func (c *Compiler) initOpFuncs() {
	// Initialize the map.
	c.opFuncs = make(map[opKey]opFunc)

	// --- Arithmetic Operators ---
	// Addition:
	c.opFuncs[opKey{Operator: token.SYM_ADD, LeftType: "i64", RightType: "i64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateAdd(left.Val, right.Val, "add_tmp"),
			Type: left.Type,
		}
	}
	c.opFuncs[opKey{Operator: token.SYM_ADD, LeftType: "f64", RightType: "f64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFAdd(left.Val, right.Val, "fadd_tmp"),
			Type: left.Type,
		}
	}

	// Subtraction:
	c.opFuncs[opKey{Operator: token.SYM_SUB, LeftType: "i64", RightType: "i64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateSub(left.Val, right.Val, "sub_tmp"),
			Type: left.Type,
		}
	}
	c.opFuncs[opKey{Operator: token.SYM_SUB, LeftType: "f64", RightType: "f64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFSub(left.Val, right.Val, "fsub_tmp"),
			Type: left.Type,
		}
	}
	// Multiplication:
	c.opFuncs[opKey{Operator: token.SYM_MUL, LeftType: "i64", RightType: "i64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateMul(left.Val, right.Val, "mul_tmp"),
			Type: left.Type,
		}
	}
	c.opFuncs[opKey{Operator: token.SYM_MUL, LeftType: "f64", RightType: "f64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFMul(left.Val, right.Val, "fmul_tmp"),
			Type: left.Type,
		}
	}
	// Division:
	// For integers, this example uses integer division (CreateSDiv).
	// For ÷ operator
	c.opFuncs[opKey{Operator: token.SYM_QUO, LeftType: "i64", RightType: "i64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateSDiv(left.Val, right.Val, "div_tmp"),
			Type: left.Type,
		}
	}
	// For division, if both operands are integers, promote them to float and do float division.
	c.opFuncs[opKey{Operator: token.SYM_DIV, LeftType: "i64", RightType: "i64"}] = func(left, right Symbol) Symbol {
		leftFP := c.builder.CreateSIToFP(left.Val, c.context.DoubleType(), "cast_to_f64")
		rightFP := c.builder.CreateSIToFP(right.Val, c.context.DoubleType(), "cast_to_f64")
		return Symbol{
			Val:  c.builder.CreateFDiv(leftFP, rightFP, "fdiv_tmp"),
			Type: Float{Width: 64},
		}
	}
	c.opFuncs[opKey{Operator: token.SYM_DIV, LeftType: "f64", RightType: "f64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFDiv(left.Val, right.Val, "fdiv_tmp"),
			Type: left.Type,
		}
	}
	// Exponentiation (^):
	// Register exponentiation operator for float values.
	powType := llvm.FunctionType(c.context.DoubleType(), []llvm.Type{c.context.DoubleType(), c.context.DoubleType()}, false)
	powFunc := c.module.NamedFunction("llvm.pow.f64")
	if powFunc.IsNil() {
		powFunc = llvm.AddFunction(c.module, "llvm.pow.f64", powType)
	}
	c.opFuncs[opKey{Operator: "^", LeftType: "f64", RightType: "f64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateCall(powType, powFunc, []llvm.Value{left.Val, right.Val}, "pow_tmp"),
			Type: left.Type,
		}
	}

	// --- Comparison Operators ---
	// Equality (==)
	c.opFuncs[opKey{Operator: "==", LeftType: "i64", RightType: "i64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateICmp(llvm.IntEQ, left.Val, right.Val, "eq_i64"),
			Type: Int{Width: 64}, // Representing boolean as an int (0 or 1)
		}
	}
	c.opFuncs[opKey{Operator: "==", LeftType: "f64", RightType: "f64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFCmp(llvm.FloatUEQ, left.Val, right.Val, "eq_f64"),
			Type: Int{Width: 64},
		}
	}
	// Not Equal (!=)
	c.opFuncs[opKey{Operator: "!=", LeftType: "i64", RightType: "i64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateICmp(llvm.IntNE, left.Val, right.Val, "neq_i64"),
			Type: Int{Width: 64},
		}
	}
	c.opFuncs[opKey{Operator: "!=", LeftType: "f64", RightType: "f64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFCmp(llvm.FloatUNE, left.Val, right.Val, "neq_f64"),
			Type: Int{Width: 64},
		}
	}
	// Less Than (<)
	c.opFuncs[opKey{Operator: "<", LeftType: "i64", RightType: "i64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateICmp(llvm.IntSLT, left.Val, right.Val, "lt_i64"),
			Type: Int{Width: 64},
		}
	}
	c.opFuncs[opKey{Operator: "<", LeftType: "f64", RightType: "f64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFCmp(llvm.FloatULT, left.Val, right.Val, "lt_f64"),
			Type: Int{Width: 64},
		}
	}
	// Less Than or Equal (<=)
	c.opFuncs[opKey{Operator: "<=", LeftType: "i64", RightType: "i64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateICmp(llvm.IntSLE, left.Val, right.Val, "le_i64"),
			Type: Int{Width: 64},
		}
	}
	c.opFuncs[opKey{Operator: "<=", LeftType: "f64", RightType: "f64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFCmp(llvm.FloatULE, left.Val, right.Val, "le_f64"),
			Type: Int{Width: 64},
		}
	}
	// Greater Than (>)
	c.opFuncs[opKey{Operator: ">", LeftType: "i64", RightType: "i64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateICmp(llvm.IntSGT, left.Val, right.Val, "gt_i64"),
			Type: Int{Width: 64},
		}
	}
	c.opFuncs[opKey{Operator: ">", LeftType: "f64", RightType: "f64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFCmp(llvm.FloatUGT, left.Val, right.Val, "gt_f64"),
			Type: Int{Width: 64},
		}
	}
	// Greater Than or Equal (>=)
	c.opFuncs[opKey{Operator: ">=", LeftType: "i64", RightType: "i64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateICmp(llvm.IntSGE, left.Val, right.Val, "ge_i64"),
			Type: Int{Width: 64},
		}
	}
	c.opFuncs[opKey{Operator: ">=", LeftType: "f64", RightType: "f64"}] = func(left, right Symbol) Symbol {
		return Symbol{
			Val:  c.builder.CreateFCmp(llvm.FloatUGE, left.Val, right.Val, "ge_f64"),
			Type: Int{Width: 64},
		}
	}
}
