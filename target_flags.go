package main

import (
	"os"
	"strings"
)

const targetCPUEnv = "PLUTO_TARGET_CPU"

type targetCPUSetting struct {
	bare         string
	explicitKind string
	disabled     bool
	defaulted    bool
}

func currentTargetCPUSetting() targetCPUSetting {
	value := strings.TrimSpace(os.Getenv(targetCPUEnv))
	defaulted := value == ""
	if defaulted {
		value = "native"
	}

	lower := strings.ToLower(value)
	switch lower {
	case "default", "off", "none", "portable":
		return targetCPUSetting{disabled: true, defaulted: defaulted}
	}

	setting := targetCPUSetting{
		bare:      value,
		defaulted: defaulted,
	}
	if strings.HasPrefix(lower, "-mcpu=") || strings.HasPrefix(lower, "-march=") {
		if idx := strings.IndexByte(value, '='); idx >= 0 {
			setting.bare = value[idx+1:]
		}
		if strings.HasPrefix(lower, "-march=") {
			setting.explicitKind = "march"
		} else {
			setting.explicitKind = "mcpu"
		}
	}
	return setting
}

func targetCPUFlag() string {
	setting := currentTargetCPUSetting()
	if setting.disabled {
		return ""
	}
	return "-mcpu=" + setting.bare
}

func clangTargetFlag(goarch string) string {
	setting := currentTargetCPUSetting()
	if setting.disabled {
		return ""
	}
	if setting.explicitKind != "" {
		return "-" + setting.explicitKind + "=" + setting.bare
	}

	switch goarch {
	case "386", "amd64":
		return "-march=" + setting.bare
	default:
		return "-mcpu=" + setting.bare
	}
}

func llvmCodegenFlags() []string {
	if cpu := targetCPUFlag(); cpu != "" {
		return []string{cpu}
	}
	return nil
}

func targetCPUCacheSegment() (string, error) {
	setting := currentTargetCPUSetting()
	if !setting.disabled && strings.EqualFold(setting.bare, "native") {
		return "", nil
	}

	key := "portable"
	if !setting.disabled {
		key = setting.bare
		if setting.explicitKind != "" {
			key = setting.explicitKind + "-" + key
		}
	}
	safeKey, err := sanitizeCacheComponent(key)
	if err != nil {
		return "", err
	}
	return "target_cpu-" + safeKey, nil
}
