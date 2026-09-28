package main

import (
	"bytes"
	"debug/elf"
	"debug/pe"
	"encoding/binary"
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
	writeELF(t, filepath.Join(dir, "elf_exec"), elf.ET_EXEC, 0)
	writeELF(t, filepath.Join(dir, "elf_pie"), elf.ET_DYN, elf.DF_1_PIE)
	writeELF(t, filepath.Join(dir, "libsample.so"), elf.ET_DYN, 0)
	writePE(t, filepath.Join(dir, "app.exe"), pe.IMAGE_FILE_EXECUTABLE_IMAGE)
	writePE(t, filepath.Join(dir, "sample.dll"), pe.IMAGE_FILE_EXECUTABLE_IMAGE|pe.IMAGE_FILE_DLL)
	writeCOFFObject(t, filepath.Join(dir, "object"), pe.IMAGE_FILE_EXECUTABLE_IMAGE)

	tests := []struct {
		name    string
		path    string
		allowed bool
	}{
		{"Missing", filepath.Join(dir, "missing"), true},
		{"Executable", os.Args[0], true},
		{"ELFExecutable", filepath.Join(dir, "elf_exec"), true},
		{"ELFPositionIndependentExecutable", filepath.Join(dir, "elf_pie"), true},
		{"PEExecutable", filepath.Join(dir, "app.exe"), true},
		{"ModFile", filepath.Join(dir, MOD_FILE), false},
		{"CodeFile", filepath.Join(dir, "lib.pt"), false},
		{"ScriptFile", filepath.Join(dir, "other.spt"), false},
		{"Directory", filepath.Join(dir, "sub"), false},
		{"ELFSharedLibrary", filepath.Join(dir, "libsample.so"), false},
		{"PEDLL", filepath.Join(dir, "sample.dll"), false},
		{"COFFObject", filepath.Join(dir, "object"), false},
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

// TestGenBinaryRefusesProjectFiles covers #80: script names may contain dots,
// so on Unix pt.mod.spt and lib.pt.spt name existing project files as their
// executables.
func TestGenBinaryRefusesProjectFiles(t *testing.T) {
	if runtime.GOOS == OS_WINDOWS {
		t.Skip("Windows appends .exe, so these scripts write pt.mod.exe and lib.pt.exe")
	}
	sources := map[string]string{
		MOD_FILE:     "module github.com/thiremani/pluto/output_path_test\n",
		"lib.pt":     "y = Twice(x)\n    y = x * 2\n",
		"pt.mod.spt": "Twice(1)\n",
		"lib.pt.spt": "Twice(2)\n",
	}
	p, cc, codeLL, rtObjs := compileTestProject(t, sources)

	for _, script := range []string{"pt.mod", "lib.pt"} {
		require.ErrorContains(t, buildScript(t, p, cc, codeLL, script, rtObjs), "refusing to replace")
	}
	for name, contents := range sources {
		got, err := os.ReadFile(filepath.Join(p.Cwd, name))
		require.NoError(t, err)
		require.Equal(t, contents, string(got), name)
	}
}

// TestGenBinaryReplacesItsExecutable checks that a rebuild replaces the
// executable an earlier build left, and that a failed link keeps it.
func TestGenBinaryReplacesItsExecutable(t *testing.T) {
	p, cc, codeLL, rtObjs := compileTestProject(t, map[string]string{
		MOD_FILE:   "module github.com/thiremani/pluto/output_path_test\n",
		"lib.pt":   "y = Twice(x)\n    y = x * 2\n",
		"main.spt": "Twice(3)\n",
	})
	mainBin := filepath.Join(p.Cwd, "main")
	if runtime.GOOS == OS_WINDOWS {
		mainBin += EXE_SUFFIX
	}

	require.NoError(t, buildScript(t, p, cc, codeLL, "main", rtObjs))
	require.NoError(t, buildScript(t, p, cc, codeLL, "main", rtObjs))
	out, err := exec.Command(mainBin).Output()
	require.NoError(t, err)
	require.Equal(t, "6", strings.TrimSpace(string(out)))

	built, err := os.ReadFile(mainBin)
	require.NoError(t, err)
	missingRuntime := []string{filepath.Join(p.Cwd, "missing.o")}
	require.Error(t, buildScript(t, p, cc, codeLL, "main", missingRuntime))
	after, err := os.ReadFile(mainBin)
	require.NoError(t, err)
	require.Equal(t, built, after)
	leftovers, err := filepath.Glob(filepath.Join(p.Cwd, linkDirPattern("main")))
	require.NoError(t, err)
	require.Empty(t, leftovers)
}

// fakePartialLinker stands in for clang: it writes part of an executable to
// its -o path, then fails as a crashed link would.
const fakePartialLinker = `#!/bin/sh
while [ "$#" -gt 0 ]; do
	if [ "$1" = "-o" ]; then
		printf partial > "$2"
	fi
	shift
done
exit 1
`

// TestGenBinaryKeepsExecutableWhenLinkFails puts a linker that writes before
// failing first on PATH: the previous executable must survive, which a link
// straight to the destination would break.
func TestGenBinaryKeepsExecutableWhenLinkFails(t *testing.T) {
	if runtime.GOOS == OS_WINDOWS {
		t.Skip("the fake linker is a shell script")
	}
	p, cc, codeLL, rtObjs := compileTestProject(t, map[string]string{
		MOD_FILE:   "module github.com/thiremani/pluto/output_path_test\n",
		"lib.pt":   "y = Twice(x)\n    y = x * 2\n",
		"main.spt": "Twice(3)\n",
	})
	require.NoError(t, buildScript(t, p, cc, codeLL, "main", rtObjs))
	mainBin := filepath.Join(p.Cwd, "main")
	built, err := os.ReadFile(mainBin)
	require.NoError(t, err)

	fakeDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fakeDir, CC), []byte(fakePartialLinker), 0755))
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	require.Error(t, buildScript(t, p, cc, codeLL, "main", rtObjs))

	after, err := os.ReadFile(mainBin)
	require.NoError(t, err)
	require.Equal(t, built, after)
	leftovers, err := filepath.Glob(filepath.Join(p.Cwd, linkDirPattern("main")))
	require.NoError(t, err)
	require.Empty(t, leftovers)
}

// compileTestProject writes files into a new project, prepares the runtime in
// a temporary cache and compiles the project's code files.
func compileTestProject(t *testing.T, files map[string]string) (*Pluto, *compiler.CodeCompiler, string, []string) {
	t.Helper()
	projectDir := t.TempDir()
	t.Setenv("PTCACHE", filepath.Join(t.TempDir(), "cache"))
	writeProjectFiles(t, projectDir, files)

	p := New(projectDir, cliOptions{})
	t.Cleanup(p.Ctx.Dispose)
	rtObjs, err := prepareRuntime(p.PtCache, p.Config)
	require.NoError(t, err)
	codeFiles, _ := p.ScanPlutoFiles("")
	cc, codeLL, err := p.CompileCode(codeFiles)
	require.NoError(t, err)
	return p, cc, codeLL, rtObjs
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

// writeELF writes a minimal little-endian ELF64 image of type typ whose only
// sections are a dynamic section holding DT_FLAGS_1 = flags1 and the section
// name table, so tests can classify shared objects without an ELF linker.
func writeELF(t *testing.T, path string, typ elf.Type, flags1 elf.DynFlag1) {
	t.Helper()
	const (
		dynamicOff = 64
		namesOff   = dynamicOff + 2*16
		sectionOff = 128
	)
	names := "\x00.dynamic\x00.shstrtab\x00"
	header := elf.Header64{
		Type:      uint16(typ),
		Machine:   uint16(elf.EM_X86_64),
		Version:   uint32(elf.EV_CURRENT),
		Shoff:     sectionOff,
		Ehsize:    64,
		Shentsize: 64,
		Shnum:     3,
		Shstrndx:  2,
	}
	copy(header.Ident[:], elf.ELFMAG)
	header.Ident[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	header.Ident[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	header.Ident[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	dynamic := []elf.Dyn64{{Tag: int64(elf.DT_FLAGS_1), Val: uint64(flags1)}, {Tag: int64(elf.DT_NULL)}}
	sections := []elf.Section64{
		{},
		{Name: 1, Type: uint32(elf.SHT_DYNAMIC), Off: dynamicOff, Size: 2 * 16, Addralign: 8, Entsize: 16},
		{Name: 10, Type: uint32(elf.SHT_STRTAB), Off: namesOff, Size: uint64(len(names)), Addralign: 1},
	}

	var image bytes.Buffer
	require.NoError(t, binary.Write(&image, binary.LittleEndian, header))
	require.NoError(t, binary.Write(&image, binary.LittleEndian, dynamic))
	image.WriteString(names)
	image.Write(make([]byte, sectionOff-image.Len()))
	require.NoError(t, binary.Write(&image, binary.LittleEndian, sections))
	require.NoError(t, os.WriteFile(path, image.Bytes(), 0755))
}

// writePE writes a minimal PE32+ image with the given file characteristics.
func writePE(t *testing.T, path string, characteristics uint16) {
	t.Helper()
	const signatureOff = 64
	dosHeader := make([]byte, signatureOff)
	copy(dosHeader, "MZ")
	binary.LittleEndian.PutUint32(dosHeader[0x3c:], signatureOff)
	optional := pe.OptionalHeader64{Magic: 0x20b, NumberOfRvaAndSizes: 16}
	header := pe.FileHeader{
		Machine:              pe.IMAGE_FILE_MACHINE_AMD64,
		SizeOfOptionalHeader: uint16(binary.Size(optional)),
		Characteristics:      characteristics,
	}

	var image bytes.Buffer
	image.Write(dosHeader)
	image.WriteString("PE\x00\x00")
	require.NoError(t, binary.Write(&image, binary.LittleEndian, header))
	require.NoError(t, binary.Write(&image, binary.LittleEndian, optional))
	require.NoError(t, os.WriteFile(path, image.Bytes(), 0755))
}

// writeCOFFObject writes a bare COFF file header, which debug/pe parses like a
// PE image that has neither an MS-DOS header nor an optional header.
func writeCOFFObject(t *testing.T, path string, characteristics uint16) {
	t.Helper()
	header := pe.FileHeader{Machine: pe.IMAGE_FILE_MACHINE_AMD64, Characteristics: characteristics}
	var image bytes.Buffer
	require.NoError(t, binary.Write(&image, binary.LittleEndian, header))
	image.Write(make([]byte, 96))
	require.NoError(t, os.WriteFile(path, image.Bytes(), 0644))
}
