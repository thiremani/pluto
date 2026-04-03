package main

import (
	"os"
	"strings"
)

const targetCPUEnv = "PLUTO_TARGET_CPU"

func targetCPUFlag() string {
	value := strings.TrimSpace(os.Getenv(targetCPUEnv))
	if value == "" {
		value = "native"
	}

	switch strings.ToLower(value) {
	case "default", "off", "none", "portable":
		return ""
	}

	if strings.HasPrefix(value, "-mcpu=") {
		return value
	}
	return "-mcpu=" + value
}

func llvmCodegenFlags() []string {
	if cpu := targetCPUFlag(); cpu != "" {
		return []string{cpu}
	}
	return nil
}
