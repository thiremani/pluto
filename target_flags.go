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

type buildConfig struct {
	targetCPU targetCPUSetting
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

func currentBuildConfig() buildConfig {
	return buildConfig{targetCPU: currentTargetCPUSetting()}
}

func (cfg buildConfig) targetCPUFlag() string {
	if cfg.targetCPU.disabled {
		return ""
	}
	return "-mcpu=" + cfg.targetCPU.bare
}

func (cfg buildConfig) clangTargetFlag(goarch string) string {
	if cfg.targetCPU.disabled {
		return ""
	}
	if cfg.targetCPU.explicitKind != "" {
		return "-" + cfg.targetCPU.explicitKind + "=" + cfg.targetCPU.bare
	}

	switch goarch {
	case "386", "amd64":
		return "-march=" + cfg.targetCPU.bare
	default:
		return "-mcpu=" + cfg.targetCPU.bare
	}
}

func (cfg buildConfig) targetCPUCacheSegment() (string, error) {
	if !cfg.targetCPU.disabled && strings.EqualFold(cfg.targetCPU.bare, "native") {
		return "", nil
	}

	key := "portable"
	if !cfg.targetCPU.disabled {
		key = cfg.targetCPU.bare
		if cfg.targetCPU.explicitKind != "" {
			key = cfg.targetCPU.explicitKind + "-" + key
		}
	}
	safeKey, err := sanitizeCacheComponent(key)
	if err != nil {
		return "", err
	}
	return "target_cpu-" + safeKey, nil
}
