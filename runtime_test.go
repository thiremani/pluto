package main

import (
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"tinygo.org/x/go-llvm"
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
	cfg := currentBuildConfig()

	flags := cfg.runtimeCompileFlags()

	if !slices.Contains(flags, OPT_LEVEL) {
		t.Fatalf("missing optimize flag %q in %v", OPT_LEVEL, flags)
	}
	if !slices.Contains(flags, C_STD) {
		t.Fatalf("missing C standard flag %q in %v", C_STD, flags)
	}
	wantTargetFlag := cfg.clangTargetFlag(runtime.GOARCH)
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
	cfg := currentBuildConfig()

	require.Equal(t, "-mcpu=native", cfg.targetCPUFlag())
}

func TestTargetCPUFlagOverride(t *testing.T) {
	t.Setenv(targetCPUEnv, "apple-m1")
	cfg := currentBuildConfig()

	require.Equal(t, "-mcpu=apple-m1", cfg.targetCPUFlag())
}

func TestTargetCPUFlagPassthroughMCPU(t *testing.T) {
	t.Setenv(targetCPUEnv, "-mcpu=apple-m1")
	cfg := currentBuildConfig()

	require.Equal(t, "-mcpu=apple-m1", cfg.targetCPUFlag())
}

func TestTargetCPUFlagNormalizesMarchPrefix(t *testing.T) {
	t.Setenv(targetCPUEnv, "-march=x86-64-v3")
	cfg := currentBuildConfig()

	require.Equal(t, "-mcpu=x86-64-v3", cfg.targetCPUFlag())
}

func TestTargetCPUFlagCaseInsensitivePrefix(t *testing.T) {
	t.Setenv(targetCPUEnv, "-MCPU=apple-m1")
	cfg := currentBuildConfig()

	require.Equal(t, "-mcpu=apple-m1", cfg.targetCPUFlag())
}

func TestClangTargetFlagDefaultAMD64(t *testing.T) {
	t.Setenv(targetCPUEnv, "")
	cfg := currentBuildConfig()

	require.Equal(t, "-march=native", cfg.clangTargetFlag("amd64"))
}

func TestClangTargetFlagDefaultARM64(t *testing.T) {
	t.Setenv(targetCPUEnv, "")
	cfg := currentBuildConfig()

	require.Equal(t, "-mcpu=native", cfg.clangTargetFlag("arm64"))
}

func TestClangTargetFlagExplicitMarchPassthrough(t *testing.T) {
	t.Setenv(targetCPUEnv, "-march=x86-64-v3")
	cfg := currentBuildConfig()

	require.Equal(t, "-march=x86-64-v3", cfg.clangTargetFlag("amd64"))
}

func TestClangTargetFlagExplicitMCPUPassthrough(t *testing.T) {
	t.Setenv(targetCPUEnv, "-mcpu=apple-m1")
	cfg := currentBuildConfig()

	require.Equal(t, "-mcpu=apple-m1", cfg.clangTargetFlag("arm64"))
}

func TestClangTargetFlagExplicitMarchPreservedOnARM64(t *testing.T) {
	t.Setenv(targetCPUEnv, "-march=armv8.5-a")
	cfg := currentBuildConfig()

	require.Equal(t, "-march=armv8.5-a", cfg.clangTargetFlag("arm64"))
}

func TestTargetCPUFlagDisable(t *testing.T) {
	t.Setenv(targetCPUEnv, "portable")
	cfg := currentBuildConfig()

	require.Empty(t, cfg.targetCPUFlag())
	require.Empty(t, cfg.clangTargetFlag("amd64"))
}

func TestTargetCPUCacheSegmentDefault(t *testing.T) {
	t.Setenv(targetCPUEnv, "")
	cfg := currentBuildConfig()

	segment, err := cfg.targetCPUCacheSegment()
	require.NoError(t, err)
	require.Empty(t, segment)
}

func TestTargetCPUCacheSegmentDisable(t *testing.T) {
	t.Setenv(targetCPUEnv, "portable")
	cfg := currentBuildConfig()

	segment, err := cfg.targetCPUCacheSegment()
	require.NoError(t, err)
	require.Equal(t, "target_cpu-portable", segment)
}

func TestTargetCPUCacheSegmentEscapes(t *testing.T) {
	t.Setenv(targetCPUEnv, "-march=x86-64/v3")
	cfg := currentBuildConfig()

	segment, err := cfg.targetCPUCacheSegment()
	require.NoError(t, err)
	require.Equal(t, "target_cpu-march-x86-64%2Fv3", segment)
}

func TestTargetCPUCacheSegmentExplicitMCPU(t *testing.T) {
	t.Setenv(targetCPUEnv, "-mcpu=apple-m1")
	cfg := currentBuildConfig()

	segment, err := cfg.targetCPUCacheSegment()
	require.NoError(t, err)
	require.Equal(t, "target_cpu-mcpu-apple-m1", segment)
}

func TestLLVMTargetCPUDefault(t *testing.T) {
	t.Setenv(targetCPUEnv, "")
	cfg := currentBuildConfig()

	if runtime.GOOS != OS_DARWIN && runtime.GOOS != "linux" {
		require.Empty(t, cfg.llvmTargetCPU())
		return
	}
	require.NotEmpty(t, cfg.llvmTargetCPU())
	require.NotEqual(t, "native", cfg.llvmTargetCPU())
}

func TestLLVMTargetCPUOverride(t *testing.T) {
	t.Setenv(targetCPUEnv, "-march=x86-64-v3")
	cfg := currentBuildConfig()

	require.Equal(t, "x86-64-v3", cfg.llvmTargetCPU())
}

func TestLLVMTargetCPUDisable(t *testing.T) {
	t.Setenv(targetCPUEnv, "portable")
	cfg := currentBuildConfig()

	require.Empty(t, cfg.llvmTargetCPU())
}

func TestLLVMRelocMode(t *testing.T) {
	t.Setenv(targetCPUEnv, "")
	cfg := currentBuildConfig()

	if runtime.GOOS == OS_WINDOWS {
		require.Equal(t, llvm.RelocDefault, cfg.llvmRelocMode())
		return
	}
	require.Equal(t, llvm.RelocPIC, cfg.llvmRelocMode())
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

func TestParseCLIArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want cliOptions
	}{
		{
			name: "default compile",
			want: cliOptions{},
		},
		{
			name: "compile target",
			args: []string{"tests"},
			want: cliOptions{target: "tests"},
		},
		{
			name: "emit ir",
			args: []string{"-emit-ir", "tests"},
			want: cliOptions{target: "tests", emitIR: true},
		},
		{
			name: "version",
			args: []string{"-version"},
			want: cliOptions{command: "version"},
		},
		{
			name: "short version",
			args: []string{"-v"},
			want: cliOptions{command: "version"},
		},
		{
			name: "clean",
			args: []string{"-clean"},
			want: cliOptions{command: "clean"},
		},
		{
			name: "short clean",
			args: []string{"-c"},
			want: cliOptions{command: "clean"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCLIArgs(tt.args)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseCLIArgsRejectsInvalidCombinations(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "unknown flag", args: []string{"-unknown"}, wantErr: "unknown flag: -unknown"},
		{name: "double dash emit ir", args: []string{"--emit-ir"}, wantErr: "unknown flag: --emit-ir"},
		{name: "version with emit ir", args: []string{"-emit-ir", "-version"}, wantErr: "-version cannot be combined with other arguments"},
		{name: "short version with target", args: []string{"-v", "tests"}, wantErr: "-v cannot be combined with other arguments"},
		{name: "clean with target", args: []string{"-clean", "tests"}, wantErr: "-clean cannot be combined with other arguments"},
		{name: "multiple targets", args: []string{"tests", "other"}, wantErr: "multiple compile targets provided: tests and other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseCLIArgs(tt.args)
			require.Error(t, err)
			require.EqualError(t, err, tt.wantErr)
		})
	}
}
