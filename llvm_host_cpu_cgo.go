//go:build darwin || linux

package main

/*
#cgo darwin,amd64 CPPFLAGS: -I/usr/local/opt/llvm@21/include -D__STDC_CONSTANT_MACROS -D__STDC_FORMAT_MACROS -D__STDC_LIMIT_MACROS
#cgo darwin,arm64 CPPFLAGS: -I/opt/homebrew/opt/llvm@21/include -D__STDC_CONSTANT_MACROS -D__STDC_FORMAT_MACROS -D__STDC_LIMIT_MACROS
#cgo linux CPPFLAGS: -I/usr/include/llvm-21 -I/usr/include/llvm-c-21 -D_GNU_SOURCE -D__STDC_CONSTANT_MACROS -D__STDC_FORMAT_MACROS -D__STDC_LIMIT_MACROS
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
