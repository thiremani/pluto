package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thiremani/pluto/compiler"
)

const invalidRelativePathHelper = "PLUTO_TEST_INVALID_RELATIVE_PATH"

func TestNewReportsInvalidRelativePathOnce(t *testing.T) {
	if os.Getenv(invalidRelativePathHelper) == "1" {
		New(os.Getenv("PLUTO_TEST_CWD"), cliOptions{})
		return
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, MOD_FILE), []byte("module example.com/math\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "daily__reports")
	if err := os.Mkdir(cwd, 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestNewReportsInvalidRelativePathOnce$")
	cmd.Env = append(os.Environ(),
		invalidRelativePathHelper+"=1",
		"PLUTO_TEST_CWD="+cwd,
		"PTCACHE="+filepath.Join(root, "cache"),
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("New() succeeded, want invalid relative path failure\n%s", output)
	}
	if count := strings.Count(string(output), "invalid relative path"); count != 1 {
		t.Fatalf("invalid relative path diagnostic appeared %d times, want once\n%s", count, output)
	}
}

func TestResolveModPathsRejectsInvalidRelativePath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, MOD_FILE), []byte("module example.com/math\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "daily__reports")
	if err := os.Mkdir(cwd, 0755); err != nil {
		t.Fatal(err)
	}

	err := (&Pluto{}).resolveModPaths(cwd)
	if err == nil || !strings.Contains(err.Error(), "invalid relative path") {
		t.Fatalf("resolveModPaths() error = %v, want invalid relative path", err)
	}
}

func TestCompileScriptRejectsInvalidScriptName(t *testing.T) {
	_, err := (&Pluto{}).CompileScript("daily__report.spt", "daily__report", nil, "")
	if err == nil || !strings.Contains(err.Error(), "invalid script name") {
		t.Fatalf("CompileScript() error = %v, want invalid script name", err)
	}
}

func TestCheckBinaryDestination(t *testing.T) {
	dir := t.TempDir()
	writeProjectFiles(t, dir, map[string]string{
		MOD_FILE:    "module example.com/app\n",
		"lib.pt":    "y = Twice(x)\n    y = x * 2\n",
		"other.spt": "Twice(1)\n",
	})
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0755))

	tests := []struct {
		name    string
		path    string
		allowed bool
	}{
		{"Missing", filepath.Join(dir, "missing"), true},
		{"Executable", os.Args[0], true},
		{"ModFile", filepath.Join(dir, MOD_FILE), false},
		{"CodeFile", filepath.Join(dir, "lib.pt"), false},
		{"ScriptFile", filepath.Join(dir, "other.spt"), false},
		{"Directory", filepath.Join(dir, "sub"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkBinaryDestination(tt.path)
			if tt.allowed {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "refusing to replace")
		})
	}
}

func TestCheckBinaryDestinationRefusesSymlink(t *testing.T) {
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(os.Args[0], link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	require.ErrorContains(t, checkBinaryDestination(link), "refusing to replace")
}

// TestGenBinaryProtectsProjectFiles covers #80: script names may contain
// dots, so pt.mod.spt and lib.pt.spt name existing project files as their
// executables.
func TestGenBinaryProtectsProjectFiles(t *testing.T) {
	projectDir := t.TempDir()
	t.Setenv("PTCACHE", filepath.Join(t.TempDir(), "cache"))
	sources := map[string]string{
		MOD_FILE:     "module github.com/thiremani/pluto/output_path_test\n",
		"lib.pt":     "y = Twice(x)\n    y = x * 2\n",
		"pt.mod.spt": "Twice(1)\n",
		"lib.pt.spt": "Twice(2)\n",
		"main.spt":   "Twice(3)\n",
	}
	writeProjectFiles(t, projectDir, sources)

	p := New(projectDir, cliOptions{})
	defer p.Ctx.Dispose()
	rtObjs, err := prepareRuntime(p.PtCache, p.Config)
	require.NoError(t, err)
	codeFiles, _ := p.ScanPlutoFiles("")
	cc, codeLL, err := p.CompileCode(codeFiles)
	require.NoError(t, err)

	for _, script := range []string{"pt.mod", "lib.pt"} {
		require.ErrorContains(t, buildScript(t, p, cc, codeLL, script, rtObjs), "refusing to replace")
	}
	for name, contents := range sources {
		got, err := os.ReadFile(filepath.Join(projectDir, name))
		require.NoError(t, err)
		require.Equal(t, contents, string(got), name)
	}

	// A rebuild replaces the executable that the first build left.
	mainBin := filepath.Join(projectDir, "main")
	if runtime.GOOS == OS_WINDOWS {
		mainBin += EXE_SUFFIX
	}
	require.NoError(t, buildScript(t, p, cc, codeLL, "main", rtObjs))
	require.NoError(t, buildScript(t, p, cc, codeLL, "main", rtObjs))
	out, err := exec.Command(mainBin).Output()
	require.NoError(t, err)
	require.Equal(t, "6", strings.TrimSpace(string(out)))

	// A failed link keeps the previous executable and leaves no link directory.
	built, err := os.ReadFile(mainBin)
	require.NoError(t, err)
	missingRuntime := []string{filepath.Join(projectDir, "missing.o")}
	require.Error(t, buildScript(t, p, cc, codeLL, "main", missingRuntime))
	after, err := os.ReadFile(mainBin)
	require.NoError(t, err)
	require.Equal(t, built, after)
	leftovers, err := filepath.Glob(filepath.Join(projectDir, LINK_DIR_PATTERN))
	require.NoError(t, err)
	require.Empty(t, leftovers)
}

func writeProjectFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, contents := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(contents), 0644))
	}
}

// buildScript compiles one script of p's project and links it with rtObjs.
func buildScript(t *testing.T, p *Pluto, cc *compiler.CodeCompiler, codeLL, script string, rtObjs []string) error {
	t.Helper()
	module, err := p.CompileScript(filepath.Join(p.Cwd, script+SPT_SUFFIX), script, cc, codeLL)
	require.NoError(t, err)
	defer module.Dispose()
	return p.GenBinary(module, script, rtObjs)
}
