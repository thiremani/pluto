package main

import (
	"bufio"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/compiler"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/parser"

	"tinygo.org/x/go-llvm"
)

const (
	PT_SUFFIX  = ".pt"
	SPT_SUFFIX = ".spt"
	IR_SUFFIX  = ".ll"
	OPT_SUFFIX = ".opt"
	OBJ_SUFFIX = ".o"

	SCRIPT_DIR = "script"
	CODE_DIR   = "code"

	MOD_FILE = "pt.mod"

	OPT_LEVEL = "-O3" // Can be configured via flag
)

//go:embed runtime/runtime.c
var runtimeCSource []byte

// Pluto holds the state of a single pluto invocation.
// You can initialize it from the working directory and then
type Pluto struct {
	Cwd     string // Working directory (may be a subdir of RootDir)
	RootDir string // Absolute root of the project (where pt.mod lives)
	ModName string // The name of the module as written in the first non-commented line of pt.mod
	ModPath string // The module path declared in pt.mod + any relative subdirectory
	RelPath string // The path relative to the module path declared in pt.mod

	CacheDir string // Cache directory (<PTCACHE>/<modulePath>)

	Ctx llvm.Context // LLVM context and code‐compiler for "code" files
}

// getDefaultPTCache gets env variable PTCACHE
// if it is not set sets it to default value for windows, mac, linux
func defaultPTCache() string {
	if env := os.Getenv("PTCACHE"); env != "" {
		return env
	}

	homeDir, _ := os.UserHomeDir()
	var ptcache string
	switch runtime.GOOS {
	case "windows":
		if localAppData := os.Getenv("LocalAppData"); localAppData != "" {
			ptcache = filepath.Join(localAppData, "pluto")
			return ptcache
		}
		ptcache = filepath.Join(homeDir, "AppData", "Local", "pluto")

	case "darwin":
		ptcache = filepath.Join(homeDir, "Library", "Caches", "pluto")

	default: // Linux and others
		if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
			ptcache = filepath.Join(xdg, "pluto")
			return ptcache
		}
		ptcache = filepath.Join(homeDir, ".cache", "pluto")
	}

	os.Setenv("PTCACHE", ptcache)
	return ptcache
}

// findModRoot walks up from startDir until it finds a directory containing pt.mod.
// It returns that directory (the module root) or an error if none is found.
func findModRoot(startDir string) (string, error) {
	dir := startDir
	for {
		if _, err := os.Stat(filepath.Join(dir, MOD_FILE)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	err := fmt.Errorf("%s not found from %s upward. The %s file should be present in the project file's root directory", MOD_FILE, startDir, MOD_FILE)
	fmt.Println(err)
	return "", err
}

// parseModuleName opens the given pt.mod file and returns the module path
// declared on the first non-blank, non-comment line.
func parseModuleName(modFile string) (string, error) {
	f, err := os.Open(modFile)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", modFile, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[0] == "module" {
			return parts[1], nil
		}
		err := fmt.Errorf("invalid module line in %s: %q. The module should begin with keyword module followed by module name", modFile, line)
		fmt.Println(err)
		return "", err
	}
	if err := scanner.Err(); err != nil {
		err := fmt.Errorf("reading %s: %w", modFile, err)
		fmt.Println(err)
		return "", err
	}
	err = fmt.Errorf("no module declaration in %s. The first line should begin with keyword module followed by module name", modFile)
	fmt.Println(err)
	return "", err
}

// resolveModPath does up to the directory in cwd that contains pt.mod file
// it takes module name from pt.mod with the relative subpath to cwd and sets it as modPath
// it also saves relPath, rootDir (the absolute path to the directory that contains pt.mod)
// all this is set in the Pluto struct
func (p *Pluto) resolveModPaths(cwd string) error {
	var err error
	p.RootDir, err = findModRoot(cwd)
	if err != nil {
		return err
	}
	fmt.Println("Root dir is", p.RootDir)

	p.ModName, err = parseModuleName(filepath.Join(p.RootDir, MOD_FILE))
	if err != nil {
		return err
	}

	p.RelPath, err = filepath.Rel(p.RootDir, cwd)
	if err != nil {
		err = fmt.Errorf("rel %s -> %s: %w", p.RootDir, cwd, err)
		return err
	}

	if p.RelPath == "." {
		p.RelPath = ""
		p.ModPath = p.ModName
	} else {
		// ensure forward slashes
		p.RelPath = filepath.ToSlash(p.RelPath)
		p.ModPath = p.ModName + "/" + p.RelPath
	}

	fmt.Printf("Mod path is %s\n", p.ModPath)
	fmt.Printf("Relative path of current directory to module root is %s\n", p.RelPath)

	return nil
}

func (p *Pluto) CompileCode(codeFiles []string) (*compiler.CodeCompiler, string, error) {
	pkgCode := ast.NewCode()
	cc := compiler.NewCodeCompiler(p.Ctx, p.ModPath, pkgCode)
	if len(codeFiles) == 0 {
		return cc, "", nil
	}

	var errStr string
	for _, codeFile := range codeFiles {
		source, err := os.ReadFile(codeFile)
		if err != nil {
			err := fmt.Errorf("error reading %s: %v", codeFile, err)
			return cc, "", err
		}
		l := lexer.New(p.RelPath+"/"+filepath.Base(codeFile), string(source))
		cp := parser.NewCodeParser(l)
		code := cp.Parse()

		if errs := cp.Errors(); len(errs) > 0 {
			for _, err := range errs {
				errStr += fmt.Sprintln(err)
			}
			errStr += fmt.Sprintf("error parsing code file %s\n", codeFile)
			continue
		}
		pkgCode.Merge(code)
	}

	if errStr != "" {
		return nil, "", errors.New(errStr)
	}

	errs := cc.Compile()
	if len(errs) != 0 {
		for _, err := range errs {
			errStr += fmt.Sprintln(err)
		}
		return nil, "", fmt.Errorf("%s\nerror while compiling code module %s", errStr, p.ModPath)
	}
	ir := cc.Compiler.GenerateIR()

	pkg := filepath.Base(p.ModPath)
	fmt.Println("Pkg name is", pkg)
	codeLL := filepath.Join(p.CacheDir, CODE_DIR, pkg+IR_SUFFIX)
	os.MkdirAll(filepath.Dir(codeLL), 0755)
	if err := os.WriteFile(codeLL, []byte(ir), 0644); err != nil {
		fmt.Printf("Error writing IR to %s: %v\n", codeLL, err)
		return nil, "", err
	}
	return cc, codeLL, nil
}

func (p *Pluto) CompileScript(scriptFile, script string, cc *compiler.CodeCompiler, codeLL string, funcCache map[string]*compiler.Func) (string, error) {
	source, err := os.ReadFile(scriptFile)
	if err != nil {
		fmt.Printf("Error reading %s: %v\n", scriptFile, err)
		return "", err
	}
	fmt.Println("THe file name is", p.RelPath+"/"+filepath.Base(scriptFile))
	l := lexer.New(p.RelPath+"/"+filepath.Base(scriptFile), string(source))
	sp := parser.NewScriptParser(l)
	program := sp.Parse()
	sc := compiler.NewScriptCompiler(p.Ctx, script, program, cc, funcCache)

	// Only link if code module has content
	if codeLL != "" {
		buffer, err := llvm.NewMemoryBufferFromFile(codeLL)
		if err != nil {
			fmt.Printf("Error loading to memory buffer: %v\n", err)
			return "", err
		}
		clone, err := p.Ctx.ParseIR(buffer)
		if err != nil {
			fmt.Printf("Error parsing IR: %v\n", err)
			return "", err
		}
		// Link code-mode module into script's module in-memory
		if err := llvm.LinkModules(sc.Compiler.Module, clone); err != nil {
			fmt.Printf("Error linking modules: %v\n", err)
			return "", err
		}
	}

	errs := sc.Compile()
	if len(errs) != 0 {
		for _, err := range errs {
			fmt.Println(err)
		}
		return "", fmt.Errorf("error compiling scriptFile %s for script %s", scriptFile, script)
	}
	ir := sc.Compiler.GenerateIR()

	llName := script + IR_SUFFIX
	scriptLL := filepath.Join(p.CacheDir, SCRIPT_DIR, llName)
	os.MkdirAll(filepath.Dir(scriptLL), 0755)
	if err := os.WriteFile(scriptLL, []byte(ir), 0644); err != nil {
		fmt.Printf("Error writing IR to %s: %v\n", scriptLL, err)
		return "", err
	}
	return scriptLL, nil
}

func (p *Pluto) GenBinary(scriptLL, bin string) error {
	// Create temp files
	optFile := filepath.Join(p.CacheDir, SCRIPT_DIR, bin+OPT_SUFFIX+IR_SUFFIX)
	objFile := filepath.Join(p.CacheDir, SCRIPT_DIR, bin+OBJ_SUFFIX)
	binFile := filepath.Join(p.Cwd, bin)

	runtimeC := filepath.Join(p.CacheDir, SCRIPT_DIR, "runtime.c")
	if err := os.WriteFile(runtimeC, runtimeCSource, 0644); err != nil {
		return err
	}

	runtimeObj := filepath.Join(p.CacheDir, SCRIPT_DIR, "runtime.o")
	rtCmd := exec.Command("clang", OPT_LEVEL, "-march=native", "-c", runtimeC, "-o", runtimeObj)
	if out, err := rtCmd.CombinedOutput(); err != nil {
		fmt.Printf("runtime compile failed: %s\n%s\n", err, string(out))
		return err
	}

	// Optimization pass
	optCmd := exec.Command("opt", OPT_LEVEL, "-S", scriptLL, "-o", optFile)
	if output, err := optCmd.CombinedOutput(); err != nil {
		fmt.Printf("optimization failed: %s\n%s\n", err, string(output))
		return err
	}

	// Compile to object file
	llcCmd := exec.Command("llc", "-filetype=obj", "-relocation-model=pic", optFile, "-o", objFile)
	if output, err := llcCmd.CombinedOutput(); err != nil {
		fmt.Printf("llc compilation failed: %s\n%s\n", err, string(output))
		return err
	}

	linkArgs := []string{"-flto", "-fuse-ld=lld"}

	if runtime.GOOS == "darwin" {
		// Mach-O linker (ld64.lld) wants -dead_strip
		linkArgs = append(linkArgs, "-Wl,-dead_strip")
	} else {
		// ELF linkers (ld, lld) accept --gc-sections
		linkArgs = append(linkArgs, "-Wl,--gc-sections")
	}

	linkArgs = append(linkArgs,
		objFile,
		runtimeObj,
		"-o",
		binFile,
		"-lm", // link against the standard math library
	)

	// Link executable (with dead code elimination)
	clangCmd := exec.Command("clang", linkArgs...)
	if output, err := clangCmd.CombinedOutput(); err != nil {
		fmt.Printf("linking failed: %s\n%s\n", err, string(output))
		return err
	}

	return nil
}

func (p *Pluto) ScanPlutoFiles() ([]string, []string) {
	dirEntries, err := os.ReadDir(p.Cwd)
	if err != nil {
		fmt.Printf("Error reading current directory: %v\n", err)
		os.Exit(1)
	}

	codeFiles := []string{}
	scriptFiles := []string{}
	for _, entry := range dirEntries {
		if entry.IsDir() {
			// TODO we can check if this dir is in pt.mod. If so then compile the directory also
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, PT_SUFFIX) {
			codeFiles = append(codeFiles, filepath.Join(p.Cwd, name))
		} else if strings.HasSuffix(name, SPT_SUFFIX) {
			scriptFiles = append(scriptFiles, filepath.Join(p.Cwd, name))
		}
	}
	return codeFiles, scriptFiles
}

func New(cwd string) *Pluto {
	fmt.Println("Current working directory is", cwd)

	ptcache := defaultPTCache()
	fmt.Printf("Using PTCACHE: %s\n", ptcache)
	if err := os.MkdirAll(ptcache, 0755); err != nil {
		fmt.Printf("Error creating PTCACHE directory: %v\n", err)
		os.Exit(1)
	}

	p := &Pluto{
		Cwd: cwd,
		Ctx: llvm.NewContext(),
	}

	err := p.resolveModPaths(cwd)
	if err != nil {
		os.Exit(1)
	}

	// Use module path (slashes) as unique cache key
	p.CacheDir = filepath.Join(ptcache, filepath.FromSlash(p.ModPath))
	fmt.Printf("Cache dir is %s\n", p.CacheDir)
	fmt.Println()

	return p
}

func main() {
	var cwd string
	var err error
	if len(os.Args) > 1 {
		cwd = os.Args[1]
	} else {
		cwd, err = os.Getwd()
		if err != nil {
			fmt.Printf("Error getting current working directory: %v\n", err)
			os.Exit(1)
		}
	}

	p := New(cwd)
	codeFiles, scriptFiles := p.ScanPlutoFiles()
	codeCompiler, codeLL, err := p.CompileCode(codeFiles)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	if len(scriptFiles) == 0 {
		fmt.Println("😱 No script file to compile!")
		return
	}

	compileErr := 0
	binErr := 0
	funcCache := make(map[string]*compiler.Func)
	for _, scriptFile := range scriptFiles {
		script := strings.TrimSuffix(filepath.Base(scriptFile), SPT_SUFFIX)
		fmt.Println("🛠️ Starting compile for script: " + script)
		scriptLL, err := p.CompileScript(scriptFile, script, codeCompiler, codeLL, funcCache)
		if err != nil {
			fmt.Println(err)
			fmt.Printf("⛓️‍💥 Error while trying to compile %s\n", script)
			compileErr++
			continue
		}

		if err := p.GenBinary(scriptLL, script); err != nil {
			fmt.Printf("⚠️ Binary generation failed for %s: %v\n", script, err)
			binErr++
		} else {
			fmt.Printf("✅ Successfully built binary for script: %s\n", script)
		}
	}
	if compileErr > 0 || binErr > 0 {
		os.Exit(1)
	}
}
