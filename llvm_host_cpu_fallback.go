//go:build !darwin && !linux

package main

func llvmHostCPUName() string {
	return ""
}

func llvmHostCPUFeatures() string {
	return ""
}
