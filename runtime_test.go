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
	t.Setenv(targetCPUEnv, "")

	flags := runtimeCompileFlags()

	if !slices.Contains(flags, OPT_LEVEL) {
		t.Fatalf("missing optimize flag %q in %v", OPT_LEVEL, flags)
	}
	if !slices.Contains(flags, C_STD) {
		t.Fatalf("missing C standard flag %q in %v", C_STD, flags)
	}
	wantTargetFlag := clangTargetFlag(runtime.GOARCH)
	if wantTargetFlag != "" && !slices.Contains(flags, wantTargetFlag) {
		t.Fatalf("expected host CPU default with %q, got %v", wantTargetFlag, flags)
	}
	otherPrefix := "-march="
	if strings.HasPrefix(wantTargetFlag, "-march=") {
		otherPrefix = "-mcpu="
	}
	if containsPrefix(flags, otherPrefix) {
		t.Fatalf("did not expect mismatched target flag family, got %v", flags)
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

func TestTargetCPUFlagDefault(t *testing.T) {
	t.Setenv(targetCPUEnv, "")

	require.Equal(t, "-mcpu=native", targetCPUFlag())
}

func TestTargetCPUFlagOverride(t *testing.T) {
	t.Setenv(targetCPUEnv, "apple-m1")

	require.Equal(t, "-mcpu=apple-m1", targetCPUFlag())
}

func TestTargetCPUFlagPassthroughMCPU(t *testing.T) {
	t.Setenv(targetCPUEnv, "-mcpu=apple-m1")

	require.Equal(t, "-mcpu=apple-m1", targetCPUFlag())
}

func TestTargetCPUFlagNormalizesMarchPrefix(t *testing.T) {
	t.Setenv(targetCPUEnv, "-march=x86-64-v3")

	require.Equal(t, "-mcpu=x86-64-v3", targetCPUFlag())
}

func TestTargetCPUFlagCaseInsensitivePrefix(t *testing.T) {
	t.Setenv(targetCPUEnv, "-MCPU=apple-m1")

	require.Equal(t, "-mcpu=apple-m1", targetCPUFlag())
}

func TestClangTargetFlagDefaultAMD64(t *testing.T) {
	t.Setenv(targetCPUEnv, "")

	require.Equal(t, "-march=native", clangTargetFlag("amd64"))
}

func TestClangTargetFlagDefaultARM64(t *testing.T) {
	t.Setenv(targetCPUEnv, "")

	require.Equal(t, "-mcpu=native", clangTargetFlag("arm64"))
}

func TestClangTargetFlagExplicitMarchPassthrough(t *testing.T) {
	t.Setenv(targetCPUEnv, "-march=x86-64-v3")

	require.Equal(t, "-march=x86-64-v3", clangTargetFlag("amd64"))
}

func TestClangTargetFlagExplicitMCPUPassthrough(t *testing.T) {
	t.Setenv(targetCPUEnv, "-mcpu=apple-m1")

	require.Equal(t, "-mcpu=apple-m1", clangTargetFlag("arm64"))
}

func TestClangTargetFlagExplicitMarchPreservedOnARM64(t *testing.T) {
	t.Setenv(targetCPUEnv, "-march=armv8.5-a")

	require.Equal(t, "-march=armv8.5-a", clangTargetFlag("arm64"))
}

func TestTargetCPUFlagDisable(t *testing.T) {
	t.Setenv(targetCPUEnv, "portable")

	require.Empty(t, targetCPUFlag())
	require.Empty(t, clangTargetFlag("amd64"))
}

func TestTargetCPUCacheSegmentDefault(t *testing.T) {
	t.Setenv(targetCPUEnv, "")

	segment, err := targetCPUCacheSegment()
	require.NoError(t, err)
	require.Empty(t, segment)
}

func TestTargetCPUCacheSegmentDisable(t *testing.T) {
	t.Setenv(targetCPUEnv, "portable")

	segment, err := targetCPUCacheSegment()
	require.NoError(t, err)
	require.Equal(t, "target_cpu-portable", segment)
}

func TestTargetCPUCacheSegmentEscapes(t *testing.T) {
	t.Setenv(targetCPUEnv, "-march=x86-64/v3")

	segment, err := targetCPUCacheSegment()
	require.NoError(t, err)
	require.Equal(t, "target_cpu-march-x86-64%2Fv3", segment)
}

func TestTargetCPUCacheSegmentExplicitMCPU(t *testing.T) {
	t.Setenv(targetCPUEnv, "-mcpu=apple-m1")

	segment, err := targetCPUCacheSegment()
	require.NoError(t, err)
	require.Equal(t, "target_cpu-mcpu-apple-m1", segment)
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
