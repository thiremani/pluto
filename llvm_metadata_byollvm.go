//go:build byollvm

package main

/*
#include "llvm-c/Core.h"
*/
import "C"

import (
	"unsafe"

	"tinygo.org/x/go-llvm"
)

// go-LLVM exposes metadata handles but not these operand inspection APIs.
// The returned nodes are borrowed from the module's LLVM context.
func llvmMetadataOperands(node llvm.Value) []llvm.Value {
	if node.IsNil() {
		return nil
	}
	count := int(C.LLVMGetMDNodeNumOperands(C.LLVMValueRef(unsafe.Pointer(node.C))))
	if count == 0 {
		return nil
	}
	// Use a C pointer buffer without depending on llvm.Value's struct layout.
	refs := make([]C.LLVMValueRef, count)
	C.LLVMGetMDNodeOperands(C.LLVMValueRef(unsafe.Pointer(node.C)), &refs[0])

	operands := make([]llvm.Value, count)
	for i, ref := range refs {
		*(*unsafe.Pointer)(unsafe.Pointer(&operands[i].C)) = unsafe.Pointer(ref)
	}

	return operands
}

func llvmMetadataString(value llvm.Value) string {
	if value.IsNil() {
		return ""
	}
	var length C.unsigned
	str := C.LLVMGetMDString(C.LLVMValueRef(unsafe.Pointer(value.C)), &length)

	return C.GoStringN(str, C.int(length))
}

func llvmValueAsMetadata(value llvm.Value) llvm.Metadata {
	var metadata llvm.Metadata
	*(*unsafe.Pointer)(unsafe.Pointer(&metadata.C)) = unsafe.Pointer(C.LLVMValueAsMetadata(C.LLVMValueRef(unsafe.Pointer(value.C))))

	return metadata
}
