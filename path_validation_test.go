package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
