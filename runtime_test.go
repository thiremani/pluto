package main

import (
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func containsPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func TestRuntimeCompileFlagsDefaultHostCPU(t *testing.T) {
	t.Setenv(runtimeMarchEnv, "")
	t.Setenv(targetCPUEnv, "")

	flags := runtimeCompileFlags()

	if !slices.Contains(flags, OPT_LEVEL) {
		t.Fatalf("missing optimize flag %q in %v", OPT_LEVEL, flags)
	}
	if !slices.Contains(flags, C_STD) {
		t.Fatalf("missing C standard flag %q in %v", C_STD, flags)
	}
	if !slices.Contains(flags, "-mcpu=native") {
		t.Fatalf("expected host CPU default with -mcpu=native, got %v", flags)
	}
	if containsPrefix(flags, "-march=") {
		t.Fatalf("did not expect legacy -march flag by default, got %v", flags)
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
	t.Setenv(targetCPUEnv, "")
	t.Setenv(runtimeMarchEnv, "x86-64")

	flags := runtimeCompileFlags()

	if !slices.Contains(flags, "-march=x86-64") {
		t.Fatalf("expected -march override in flags, got %v", flags)
	}
	if containsPrefix(flags, "-mcpu=") {
		t.Fatalf("did not expect -mcpu when legacy -march override is set, got %v", flags)
	}
}

func TestRuntimeCompileFlagsMarchFlagPassthrough(t *testing.T) {
	t.Setenv(targetCPUEnv, "")
	t.Setenv(runtimeMarchEnv, "-march=native")

	flags := runtimeCompileFlags()

	if !slices.Contains(flags, "-march=native") {
		t.Fatalf("expected explicit -march flag passthrough, got %v", flags)
	}
}

func TestTargetCPUFlagDefault(t *testing.T) {
	t.Setenv(targetCPUEnv, "")

	require.Equal(t, "-mcpu=native", targetCPUFlag())
}

func TestTargetCPUFlagOverride(t *testing.T) {
	t.Setenv(targetCPUEnv, "apple-m1")

	require.Equal(t, "-mcpu=apple-m1", targetCPUFlag())
}

func TestTargetCPUFlagDisable(t *testing.T) {
	t.Setenv(targetCPUEnv, "portable")

	require.Empty(t, targetCPUFlag())
}

func TestOptCommandArgsIncludeCPU(t *testing.T) {
	t.Setenv(targetCPUEnv, "")

	require.Contains(t, optCommandArgs("in.ll", "out.ll"), "-mcpu=native")
}

func TestLLCCommandArgsIncludeCPU(t *testing.T) {
	t.Setenv(targetCPUEnv, "")

	args := llcCommandArgs("in.ll", "out.o")
	require.Contains(t, args, "-mcpu=native")
	require.Contains(t, args, FILETYPE_OBJ)
	if runtime.GOOS == OS_WINDOWS {
		require.NotContains(t, args, RELOC_PIC)
		return
	}
	require.Contains(t, args, RELOC_PIC)
}

func TestCLICommand(t *testing.T) {
	tests := []struct {
		arg  string
		want string
	}{
		{arg: "-v", want: "version"},
		{arg: "-version", want: "version"},
		{arg: "-c", want: "clean"},
		{arg: "-clean", want: "clean"},
		{arg: "tests", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			require.Equal(t, tt.want, cliCommand(tt.arg))
		})
	}
}
