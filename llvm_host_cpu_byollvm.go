//go:build byollvm

package main

/*
#include "llvm-c/Core.h"
#include "llvm-c/TargetMachine.h"
*/
import "C"

func llvmHostCPUName() string {
	cpu := C.LLVMGetHostCPUName()
	if cpu == nil {
		return ""
	}
	defer C.LLVMDisposeMessage(cpu)
	return C.GoString(cpu)
}

func llvmHostCPUFeatures() string {
	features := C.LLVMGetHostCPUFeatures()
	if features == nil {
		return ""
	}
	defer C.LLVMDisposeMessage(features)
	return C.GoString(features)
}
