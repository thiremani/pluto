package compiler

import (
	"fmt"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

const (
	unsupportedArrayTypeMsg = "unsupported array element type: %s"
)

type ArrayInfo struct {
	PtrName    string
	NewName    string
	ResizeName string
	SetName    string
	GetName    string
	LenName    string
	StrName    string
}

var arrayInfos = map[Kind]ArrayInfo{
	IntKind: {
		PtrName:    "PtArrayI64",
		NewName:    ARR_I64_NEW,
		ResizeName: ARR_I64_RESIZE,
		SetName:    ARR_I64_SET,
		GetName:    ARR_I64_GET,
		LenName:    ARR_I64_LEN,
		StrName:    ARR_I64_STR,
	},
	FloatKind: {
		PtrName:    "PtArrayF64",
		NewName:    ARR_F64_NEW,
		ResizeName: ARR_F64_RESIZE,
		SetName:    ARR_F64_SET,
		GetName:    ARR_F64_GET,
		LenName:    ARR_F64_LEN,
		StrName:    ARR_F64_STR,
	},
	StrKind: {
		PtrName:    "PtArrayStr",
		NewName:    ARR_STR_NEW,
		ResizeName: ARR_STR_RESIZE,
		SetName:    ARR_STR_SET,
		GetName:    ARR_STR_GET,
		LenName:    ARR_STR_LEN,
		StrName:    ARR_STR_STR,
	},
}

func arrayInfoForType(t Type) (ArrayInfo, bool) {
	info, ok := arrayInfos[t.Kind()]
	return info, ok
}

// arrayBitCast casts an array value to the appropriate named opaque pointer type
func (c *Compiler) arrayBitCast(arr llvm.Value, info ArrayInfo, name string) llvm.Value {
	pt := c.namedOpaquePtr(info.PtrName)
	return c.builder.CreateBitCast(arr, pt, name)
}

func (c *Compiler) createArrayForType(elem Type, length llvm.Value) llvm.Value {
	info, ok := arrayInfoForType(elem)
	if !ok {
		panic(fmt.Sprintf(unsupportedArrayTypeMsg, elem.String()))
	}

	// Create new array
	_, newFn := c.GetCFunc(info.NewName)
	vec := c.builder.CreateCall(c.GetFnType(info.NewName), newFn, nil, "arr_new")

	// Resize array
	_, rezFn := c.GetCFunc(info.ResizeName)
	switch elem.Kind() {
	case IntKind:
		zero := llvm.ConstInt(c.Context.Int64Type(), 0, false)
		c.builder.CreateCall(c.GetFnType(info.ResizeName), rezFn, []llvm.Value{vec, length, zero}, "arr_resize")
	case FloatKind:
		zero := llvm.ConstFloat(c.Context.DoubleType(), 0.0)
		c.builder.CreateCall(c.GetFnType(info.ResizeName), rezFn, []llvm.Value{vec, length, zero}, "arr_resize")
	case StrKind:
		c.builder.CreateCall(c.GetFnType(info.ResizeName), rezFn, []llvm.Value{vec, length}, "arr_resize")
	}

	return vec
}

func (c *Compiler) arraySetForType(elem Type, vec llvm.Value, idx llvm.Value, value llvm.Value) {
	info, ok := arrayInfoForType(elem)
	if !ok {
		panic(fmt.Sprintf(unsupportedArrayTypeMsg, elem.String()))
	}

	fnTy, fn := c.GetCFunc(info.SetName)
	c.builder.CreateCall(fnTy, fn, []llvm.Value{vec, idx, value}, info.SetName)
}

func (c *Compiler) arrayLen(arr *Symbol, elem Type) llvm.Value {
	info, ok := arrayInfoForType(elem)
	if !ok {
		panic(fmt.Sprintf(unsupportedArrayTypeMsg, elem.String()))
	}

	cast := c.arrayBitCast(arr.Val, info, "arrp")
	fnTy, fn := c.GetCFunc(info.LenName)
	return c.builder.CreateCall(fnTy, fn, []llvm.Value{cast}, "len")
}

func (c *Compiler) arrayGet(arr *Symbol, elem Type, idx llvm.Value) llvm.Value {
	info, ok := arrayInfoForType(elem)
	if !ok {
		panic(fmt.Sprintf(unsupportedArrayTypeMsg, elem.String()))
	}

	cast := c.arrayBitCast(arr.Val, info, "arrp")
	fnTy, fn := c.GetCFunc(info.GetName)
	return c.builder.CreateCall(fnTy, fn, []llvm.Value{cast, idx}, "get")
}

// arraySetCells populates an array with cell values, handling type conversions
func (c *Compiler) arraySetCells(vec llvm.Value, cells []*Symbol, elemType Type) {
	info, ok := arrayInfoForType(elemType)
	if !ok {
		panic(fmt.Sprintf(unsupportedArrayTypeMsg, elemType.String()))
	}

	_, setFn := c.GetCFunc(info.SetName)
	for i, cs := range cells {
		idx := llvm.ConstInt(c.Context.Int64Type(), uint64(i), false)
		val := cs.Val

		// Handle type conversions
		switch elemType.Kind() {
		case IntKind:
			if cs.Type.Kind() == FloatKind {
				val = c.builder.CreateFPToSI(cs.Val, c.Context.Int64Type(), "f64_to_i64")
			}
		case FloatKind:
			if cs.Type.Kind() == IntKind {
				val = c.builder.CreateSIToFP(cs.Val, c.Context.DoubleType(), "i64_to_f64")
			}
		}

		c.builder.CreateCall(c.GetFnType(info.SetName), setFn, []llvm.Value{vec, idx, val}, "arr_set")
	}
}

// Array compilation functions

func (c *Compiler) compileArrayExpression(e *ast.ArrayLiteral) (res []*Symbol) {
	s := &Symbol{}

	// Only support vector form for now
	if !(len(e.Headers) == 0 && len(e.Rows) == 1) {
		c.Errors = append(c.Errors, &token.CompileError{Token: e.Tok(), Msg: "2D arrays/tables not implemented yet"})
		return nil
	}

	row := e.Rows[0]
	// Compile cells
	cells := make([]*Symbol, len(row))
	for i, cell := range row {
		vals := c.compileExpression(cell, nil, false)
		// type solver ensures exactly one value per cell
		cells[i] = c.derefIfPointer(vals[0])
	}

	// Use the solver's inferred array schema
	info := c.ExprCache[e]
	arr := info.OutTypes[0].(Array)
	elemType := arr.ColTypes[0]

	n := len(row)
	nConst := llvm.ConstInt(c.Context.Int64Type(), uint64(n), false)

	// Create array and populate it
	arrVal := c.createArrayForType(elemType, nConst)
	c.arraySetCells(arrVal, cells, elemType)

	// Set symbol type and value
	s.Type = arr
	s.Val = c.builder.CreateBitCast(arrVal, llvm.PointerType(c.Context.Int8Type(), 0), "arr_i8p")

	return []*Symbol{s}
}

// Array operation functions

func (c *Compiler) compileArrayScalarInfix(op string, arr *Symbol, scalar *Symbol, resElem Type) *Symbol {
	arrType := arr.Type.(Array)
	arrElem := arrType.ColTypes[0]

	lenVal := c.arrayLen(arr, arrElem)
	resVec := c.createArrayForType(resElem, lenVal)

	scalarSym := c.derefIfPointer(scalar)

	r := c.rangeZeroToN(lenVal)
	c.createLoop(r, func(iter llvm.Value) {
		idx := iter
		val := c.arrayGet(arr, arrElem, idx)
		elemSym := &Symbol{Val: val, Type: arrElem}

		computed := c.compileInfix(op, elemSym, scalarSym, resElem)
		c.arraySetForType(resElem, resVec, idx, computed.Val)
	})

	i8p := llvm.PointerType(c.Context.Int8Type(), 0)
	resSym := &Symbol{Type: Array{Headers: nil, ColTypes: []Type{resElem}, Length: 0}}
	resSym.Val = c.builder.CreateBitCast(resVec, i8p, "arr_i8p")
	return resSym
}

func (c *Compiler) compileArrayUnaryPrefix(op string, arr *Symbol, result Array) *Symbol {
	arrType := arr.Type.(Array)
	elem := arrType.ColTypes[0]
	resElem := result.ColTypes[0]
	n := c.arrayLen(arr, elem)
	resVec := c.createArrayForType(resElem, n)

	r := c.rangeZeroToN(n)
	c.createLoop(r, func(iter llvm.Value) {
		idx := iter
		v := c.arrayGet(arr, elem, idx)
		opSym := &Symbol{Val: v, Type: elem}
		computed := c.compilePrefix(op, opSym, resElem)
		c.arraySetForType(resElem, resVec, idx, computed.Val)
	})

	i8p := llvm.PointerType(c.Context.Int8Type(), 0)
	resSym := &Symbol{Type: result}
	resSym.Val = c.builder.CreateBitCast(resVec, i8p, "arr_i8p")
	return resSym
}

// Array string conversion function

func (c *Compiler) arrayStrArg(s *Symbol) llvm.Value {
	arr := s.Type.(Array)
	if len(arr.ColTypes) != 1 {
		panic("internal: arrayStrArg supports only single-column vectors")
	}

	elemType := arr.ColTypes[0]
	info, ok := arrayInfoForType(elemType)
	if !ok {
		panic("internal: unsupported array element kind for printing")
	}

	cast := c.arrayBitCast(s.Val, info, "arr_cast")
	fnTy, fn := c.GetCFunc(info.StrName)
	return c.builder.CreateCall(fnTy, fn, []llvm.Value{cast}, "arr_str")
}
