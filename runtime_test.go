package main

import (
	"runtime"
	"slices"
	"strings"
	"testing"
)

func containsPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func TestRuntimeCompileFlagsDefaultPortable(t *testing.T) {
	t.Setenv(runtimeMarchEnv, "")

	flags := runtimeCompileFlags()

	if !slices.Contains(flags, OPT_LEVEL) {
		t.Fatalf("missing optimize flag %q in %v", OPT_LEVEL, flags)
	}
	if !slices.Contains(flags, C_STD) {
		t.Fatalf("missing C standard flag %q in %v", C_STD, flags)
	}
	if containsPrefix(flags, "-march=") {
		t.Fatalf("expected portable default without -march, got %v", flags)
	}

	if runtime.GOOS == OS_WINDOWS {
		if slices.Contains(flags, FPIC) {
			t.Fatalf("did not expect %q on windows, got %v", FPIC, flags)
		}
		return
	}
	if !slices.Contains(flags, FPIC) {
		t.Fatalf("expected %q on non-windows, got %v", FPIC, flags)
	}
}

func TestRuntimeCompileFlagsMarchOverride(t *testing.T) {
	t.Setenv(runtimeMarchEnv, "x86-64")

	flags := runtimeCompileFlags()

	if !slices.Contains(flags, "-march=x86-64") {
		t.Fatalf("expected -march override in flags, got %v", flags)
	}
}

func TestRuntimeCompileFlagsMarchFlagPassthrough(t *testing.T) {
	t.Setenv(runtimeMarchEnv, "-march=native")

	flags := runtimeCompileFlags()

	if !slices.Contains(flags, "-march=native") {
		t.Fatalf("expected explicit -march flag passthrough, got %v", flags)
	}
}
