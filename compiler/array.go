package compiler

import (
	"fmt"

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
