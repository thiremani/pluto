package compiler

import (
	"fmt"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

type ArrayInfo struct {
	PtrName string
	LenName string
	GetName string
	SetName string
	Create  func(*Compiler, llvm.Value) llvm.Value
}

var arrayInfos = map[Kind]ArrayInfo{
	IntKind: {
		PtrName: "PtArrayI64",
		LenName: ARR_I64_LEN,
		GetName: ARR_I64_GET,
		SetName: ARR_I64_SET,
		Create:  (*Compiler).createArrayI64,
	},
	FloatKind: {
		PtrName: "PtArrayF64",
		LenName: ARR_F64_LEN,
		GetName: ARR_F64_GET,
		SetName: ARR_F64_SET,
		Create:  (*Compiler).createArrayF64,
	},
	StrKind: {
		PtrName: "PtArrayStr",
		LenName: ARR_STR_LEN,
		GetName: ARR_STR_GET,
		SetName: ARR_STR_SET,
		Create:  (*Compiler).createArrayStr,
	},
}

func arrayInfoForType(t Type) (ArrayInfo, bool) {
	info, ok := arrayInfos[t.Kind()]
	return info, ok
}

func (c *Compiler) createArrayForType(elem Type, length llvm.Value) llvm.Value {
	info, ok := arrayInfoForType(elem)
	if !ok {
		panic(fmt.Sprintf("unsupported array element type: %s", elem.String()))
	}

	return info.Create(c, length)
}

func (c *Compiler) arraySetForType(elem Type, vec llvm.Value, idx llvm.Value, value llvm.Value) {
	info, ok := arrayInfoForType(elem)
	if !ok {
		panic(fmt.Sprintf("unsupported array element type: %s", elem.String()))
	}

	fnTy, fn := c.GetCFunc(info.SetName)
	c.builder.CreateCall(fnTy, fn, []llvm.Value{vec, idx, value}, info.SetName)
}

func (c *Compiler) arrayLen(arr *Symbol, elem Type) llvm.Value {
	info, ok := arrayInfoForType(elem)
	if !ok {
		panic(fmt.Sprintf("unsupported array element type: %s", elem.String()))
	}

	pt := c.namedOpaquePtr(info.PtrName)
	cast := c.builder.CreateBitCast(arr.Val, pt, "arrp")
	fnTy, fn := c.GetCFunc(info.LenName)
	return c.builder.CreateCall(fnTy, fn, []llvm.Value{cast}, "len")
}

func (c *Compiler) arrayGet(arr *Symbol, elem Type, idx llvm.Value) llvm.Value {
	info, ok := arrayInfoForType(elem)
	if !ok {
		panic(fmt.Sprintf("unsupported array element type: %s", elem.String()))
	}

	pt := c.namedOpaquePtr(info.PtrName)
	cast := c.builder.CreateBitCast(arr.Val, pt, "arrp")
	fnTy, fn := c.GetCFunc(info.GetName)
	return c.builder.CreateCall(fnTy, fn, []llvm.Value{cast, idx}, "get")
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
	kind := arr.ColTypes[0].Kind()

	n := len(row)
	nConst := llvm.ConstInt(c.Context.Int64Type(), uint64(n), false)

	// The solver has already fixed the array schema; assign type once.
	s.Type = arr
	switch kind {
	case StrKind:
		arrVal := c.createArrayStr(nConst)
		c.setArrayStr(arrVal, cells)
		s.Val = c.builder.CreateBitCast(arrVal, llvm.PointerType(c.Context.Int8Type(), 0), "arr_i8p")
	case FloatKind:
		arrVal := c.createArrayF64(nConst)
		c.setArrayF64(arrVal, cells)
		s.Val = c.builder.CreateBitCast(arrVal, llvm.PointerType(c.Context.Int8Type(), 0), "arr_i8p")
	case IntKind:
		arrVal := c.createArrayI64(nConst)
		c.setArrayI64(arrVal, cells)
		s.Val = c.builder.CreateBitCast(arrVal, llvm.PointerType(c.Context.Int8Type(), 0), "arr_i8p")
	default:
		panic("internal: unresolved array element type in compileArrayExpression")
	}

	return []*Symbol{s}
}

// Array creation and manipulation functions

func (c *Compiler) createArrayI64(n llvm.Value) llvm.Value {
	_, newFn := c.GetCFunc(ARR_I64_NEW)
	vec := c.builder.CreateCall(c.GetFnType(ARR_I64_NEW), newFn, nil, "arr_i64_new")
	_, rezFn := c.GetCFunc(ARR_I64_RESIZE)
	zero := llvm.ConstInt(c.Context.Int64Type(), 0, false)
	c.builder.CreateCall(c.GetFnType(ARR_I64_RESIZE), rezFn, []llvm.Value{vec, n, zero}, "arr_i64_resize")
	return vec
}

func (c *Compiler) setArrayI64(vec llvm.Value, cells []*Symbol) {
	_, setFn := c.GetCFunc(ARR_I64_SET)
	for i, cs := range cells {
		idx := llvm.ConstInt(c.Context.Int64Type(), uint64(i), false)
		val := cs.Val
		if cs.Type.Kind() == FloatKind {
			val = c.builder.CreateFPToSI(cs.Val, c.Context.Int64Type(), "f64_to_i64")
		}
		c.builder.CreateCall(c.GetFnType(ARR_I64_SET), setFn, []llvm.Value{vec, idx, val}, "arr_i64_set")
	}
}

func (c *Compiler) createArrayF64(n llvm.Value) llvm.Value {
	_, newFn := c.GetCFunc(ARR_F64_NEW)
	vec := c.builder.CreateCall(c.GetFnType(ARR_F64_NEW), newFn, nil, "arr_f64_new")
	_, rezFn := c.GetCFunc(ARR_F64_RESIZE)
	c.builder.CreateCall(c.GetFnType(ARR_F64_RESIZE), rezFn, []llvm.Value{vec, n, llvm.ConstFloat(c.Context.DoubleType(), 0.0)}, "arr_f64_resize")
	return vec
}

func (c *Compiler) setArrayF64(vec llvm.Value, cells []*Symbol) {
	_, setFn := c.GetCFunc(ARR_F64_SET)
	for i, cs := range cells {
		idx := llvm.ConstInt(c.Context.Int64Type(), uint64(i), false)
		val := cs.Val
		if cs.Type.Kind() == IntKind {
			val = c.builder.CreateSIToFP(cs.Val, c.Context.DoubleType(), "i64_to_f64")
		}
		c.builder.CreateCall(c.GetFnType(ARR_F64_SET), setFn, []llvm.Value{vec, idx, val}, "arr_f64_set")
	}
}

func (c *Compiler) createArrayStr(n llvm.Value) llvm.Value {
	_, newFn := c.GetCFunc(ARR_STR_NEW)
	vec := c.builder.CreateCall(c.GetFnType(ARR_STR_NEW), newFn, nil, "arr_str_new")
	_, rezFn := c.GetCFunc(ARR_STR_RESIZE)
	c.builder.CreateCall(c.GetFnType(ARR_STR_RESIZE), rezFn, []llvm.Value{vec, n}, "arr_str_resize")
	return vec
}

func (c *Compiler) setArrayStr(vec llvm.Value, cells []*Symbol) {
	_, setFn := c.GetCFunc(ARR_STR_SET)
	for i, cs := range cells {
		idx := llvm.ConstInt(c.Context.Int64Type(), uint64(i), false)
		c.builder.CreateCall(c.GetFnType(ARR_STR_SET), setFn, []llvm.Value{vec, idx, cs.Val}, "arr_str_set")
	}
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
	// Bitcast the opaque i8* to the appropriate named opaque pointer
	switch arr.ColTypes[0].Kind() {
	case IntKind:
		pt := c.namedOpaquePtr("PtArrayI64")
		cast := c.builder.CreateBitCast(s.Val, pt, "arr_i64p")
		fnTy, fn := c.GetCFunc(ARR_I64_STR)
		return c.builder.CreateCall(fnTy, fn, []llvm.Value{cast}, "arr_i64_str")
	case FloatKind:
		pt := c.namedOpaquePtr("PtArrayF64")
		cast := c.builder.CreateBitCast(s.Val, pt, "arr_f64p")
		fnTy, fn := c.GetCFunc(ARR_F64_STR)
		return c.builder.CreateCall(fnTy, fn, []llvm.Value{cast}, "arr_f64_str")
	case StrKind:
		pt := c.namedOpaquePtr("PtArrayStr")
		cast := c.builder.CreateBitCast(s.Val, pt, "arr_strp")
		fnTy, fn := c.GetCFunc(ARR_STR_STR)
		return c.builder.CreateCall(fnTy, fn, []llvm.Value{cast}, "arr_str_str")
	default:
		panic("internal: unsupported array element kind for printing")
	}
}
