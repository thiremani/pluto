package main

import (
	"net/url"
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

func normalizedTargetCPUValue() string {
	value := targetCPUValue()
	switch strings.ToLower(value) {
	case "default", "off", "none", "portable":
		return ""
	}

	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "-mcpu=") || strings.HasPrefix(lower, "-march=") {
		if idx := strings.IndexByte(value, '='); idx >= 0 {
			return value[idx+1:]
		}
	}
	return value
}

func targetCPUFlag() string {
	value := normalizedTargetCPUValue()
	if value == "" {
		return ""
	}
	return "-mcpu=" + value
}

func clangTargetFlag(goarch string) string {
	value := normalizedTargetCPUValue()
	if value == "" {
		return ""
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

func targetCPUCacheSegment() string {
	value := normalizedTargetCPUValue()
	if value == "" {
		value = "portable"
	}
	return "cpu-" + url.PathEscape(value)
}
