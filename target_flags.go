package main

import (
	"os"
	"strings"
)

const targetCPUEnv = "PLUTO_TARGET_CPU"

func targetCPUValue() string {
	value := strings.TrimSpace(os.Getenv(targetCPUEnv))
	if value == "" {
		return "native"
	}
	return value
}

func targetCPUFlag() string {
	value := targetCPUValue()
	switch strings.ToLower(value) {
	case "default", "off", "none", "portable":
		return ""
	}

	if strings.HasPrefix(value, "-mcpu=") {
		return value
	}
	return "-mcpu=" + value
}

func clangTargetFlag(goarch string) string {
	value := targetCPUValue()
	switch strings.ToLower(value) {
	case "default", "off", "none", "portable":
		return ""
	}

	if strings.HasPrefix(value, "-mcpu=") || strings.HasPrefix(value, "-march=") {
		return value
	}

	switch goarch {
	case "386", "amd64":
		return "-march=" + value
	default:
		return "-mcpu=" + value
	}
}

func llvmCodegenFlags() []string {
	if cpu := targetCPUFlag(); cpu != "" {
		return []string{cpu}
	}
	return nil
}
