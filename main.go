package main

import (
	"bufio"
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

	// Compiler settings
	CC        = "clang"
	C_STD     = "-std=c11"
	OPT_LEVEL = "-O3"
)

// Pluto holds the state of a single pluto invocation.
// You can initialize it from the working directory and then
type Pluto struct {
	Cwd     string // Working directory (may be a subdir of RootDir)
	RootDir string // Absolute root of the project (where pt.mod lives)
	ModName string // The name of the module as written in the first non-commented line of pt.mod
	ModPath string // The module path declared in pt.mod + any relative subdirectory
	RelPath string // The path relative to the module path declared in pt.mod

	PtCache  string // Root cache directory (PTCACHE)
	CacheDir string // Project-specific cache directory (<PTCACHE>/<modulePath>)

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

func (p *Pluto) CompileScript(scriptFile, script string, cc *compiler.CodeCompiler, codeLL string, funcCache map[string]*compiler.Func, exprCache map[compiler.ExprKey]*compiler.ExprInfo) (string, error) {
	source, err := os.ReadFile(scriptFile)
	if err != nil {
		fmt.Printf("Error reading %s: %v\n", scriptFile, err)
		return "", err
	}
	l := lexer.New(p.RelPath+"/"+filepath.Base(scriptFile), string(source))
	sp := parser.NewScriptParser(l)
	program := sp.Parse()
	if errs := sp.Errors(); len(errs) > 0 {
		for _, err := range errs {
			fmt.Println(err)
		}
		fmt.Printf("error parsing scriptFile %s for script %s\n", scriptFile, script)
		return "", fmt.Errorf("parser errors for %s", scriptFile)
	}
	sc := compiler.NewScriptCompiler(p.Ctx, script, program, cc, funcCache, exprCache)

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

func (p *Pluto) GenBinary(scriptLL, bin string, rtObjs []string) error {
	optFile := filepath.Join(p.CacheDir, SCRIPT_DIR, bin+OPT_SUFFIX+IR_SUFFIX)
	// Use the default object suffix (".o") on all platforms, including
	// Windows when using the MinGW/UCRT toolchain.
	objExt := OBJ_SUFFIX
	objFile := filepath.Join(p.CacheDir, SCRIPT_DIR, bin+objExt)
	binFile := filepath.Join(p.Cwd, bin)
	if runtime.GOOS == "windows" {
		binFile = binFile + ".exe"
	}

	// 1) Optimize IR
	if out, err := exec.Command("opt", OPT_LEVEL, "-S", scriptLL, "-o", optFile).CombinedOutput(); err != nil {
		fmt.Printf("optimization failed: %v\n%s\n", err, out)
		return err
	}

	// 2) Lower to object
	llcArgs := []string{"-filetype=obj"}
	// PIC is ELF/Mach-O specific; avoid on Windows COFF
	if runtime.GOOS != "windows" {
		llcArgs = append(llcArgs, "-relocation-model=pic")
	}
	llcArgs = append(llcArgs, optFile, "-o", objFile)
	if out, err := exec.Command("llc", llcArgs...).CombinedOutput(); err != nil {
		fmt.Printf("llc compilation failed: %v\n%s\n", err, out)
		return err
	}

	// 3) Link everything
	linkArgs := []string{}

	switch runtime.GOOS {
	case "darwin":
		// Mach-O linker wants -dead_strip
		linkArgs = append(linkArgs, "-Wl,-dead_strip")
	case "windows":
		// MinGW/COFF linker flags
		linkArgs = append(linkArgs, "-Wl,--gc-sections")
	default:
		// ELF linkers (ld, lld) accept --gc-sections
		linkArgs = append(linkArgs, "-Wl,--gc-sections")
	}
	linkArgs = append(linkArgs, objFile)
	linkArgs = append(linkArgs, rtObjs...)
	linkArgs = append(linkArgs, "-o", binFile)
	// libm is only needed/available on ELF-based systems
	if runtime.GOOS != "windows" {
		linkArgs = append(linkArgs, "-lm")
	}

	if out, err := exec.Command(CC, linkArgs...).CombinedOutput(); err != nil {
		fmt.Printf("linking failed: %v\n%s\n", err, out)
		return err
	}

	return nil
}

func (p *Pluto) ScanPlutoFiles(specificScript string) ([]string, []string) {
	dirEntries, err := os.ReadDir(p.Cwd)
	if err != nil {
		fmt.Printf("Error reading current directory: %v\n", err)
		os.Exit(1)
	}

	codeFiles := []string{}
	scriptFiles := []string{}
	for _, entry := range dirEntries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, PT_SUFFIX) {
			codeFiles = append(codeFiles, filepath.Join(p.Cwd, name))
			continue
		}
		if !strings.HasSuffix(name, SPT_SUFFIX) {
			continue
		}
		// If a specific script is requested, only include that one
		if specificScript != "" && name != specificScript {
			continue
		}
		scriptFiles = append(scriptFiles, filepath.Join(p.Cwd, name))
	}
	return codeFiles, scriptFiles
}

func New(cwd string) *Pluto {
	fmt.Println("Current working directory is", cwd)

	ptcache := defaultPTCache()
	// Include version in cache path to isolate different compiler versions
	versionedCache := filepath.Join(ptcache, Version)
	fmt.Printf("Using PTCACHE: %s\n", versionedCache)
	if err := os.MkdirAll(versionedCache, 0755); err != nil {
		fmt.Printf("Error creating PTCACHE directory: %v\n", err)
		os.Exit(1)
	}

	p := &Pluto{
		Cwd:     cwd,
		PtCache: versionedCache,
		Ctx:     llvm.NewContext(),
	}

	err := p.resolveModPaths(cwd)
	if err != nil {
		os.Exit(1)
	}

	// Use module path (slashes) as unique cache key
	p.CacheDir = filepath.Join(p.PtCache, filepath.FromSlash(p.ModPath))
	fmt.Printf("Cache dir is %s\n", p.CacheDir)
	fmt.Println()

	return p
}

func main() {
	// Handle subcommands
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			printVersion()
			return
		case "clean":
			runClean()
			return
		}
	}

	// Default: compile
	runCompile()
}

// runClean removes the cache directory for the current version.
func runClean() {
	ptcache := defaultPTCache()
	versionCache := filepath.Join(ptcache, Version)

	info, err := os.Stat(versionCache)
	if os.IsNotExist(err) {
		fmt.Printf("Cache directory does not exist: %s\n", versionCache)
		return
	}
	if err != nil {
		fmt.Printf("Error accessing cache: %v\n", err)
		os.Exit(1)
	}
	if !info.IsDir() {
		fmt.Printf("Cache path is not a directory: %s\n", versionCache)
		os.Exit(1)
	}

	if err := os.RemoveAll(versionCache); err != nil {
		fmt.Printf("Error removing cache: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Removed cache for %s: %s\n", Version, versionCache)
}

// runCompile is the main compilation workflow.
func runCompile() {
	// Determine target path (file or directory)
	target, err := os.Getwd()
	if err != nil {
		fmt.Printf("Error getting current working directory: %v\n", err)
		os.Exit(1)
	}
	if len(os.Args) > 1 {
		target = os.Args[1]
	}

	// Resolve target to cwd and optional specific script
	cwd, specificScript, err := resolveTarget(target)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	p := New(cwd)
	codeFiles, scriptFiles := p.ScanPlutoFiles(specificScript)
	codeCompiler, codeLL, err := p.CompileCode(codeFiles)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	if len(scriptFiles) == 0 {
		fmt.Println("😱 No script file to compile!")
		return
	}

	// Prepare runtime once (in PtCache root, shared across all projects)
	rtObjs, err := prepareRuntime(p.PtCache)
	if err != nil {
		fmt.Printf("Error preparing runtime: %v\n", err)
		os.Exit(1)
	}

	compileErr := 0
	binErr := 0
	funcCache := make(map[string]*compiler.Func)
	exprCache := make(map[compiler.ExprKey]*compiler.ExprInfo)
	for _, scriptFile := range scriptFiles {
		script := strings.TrimSuffix(filepath.Base(scriptFile), SPT_SUFFIX)
		fmt.Println("🛠️ Starting compile for script: " + script)
		scriptLL, err := p.CompileScript(scriptFile, script, codeCompiler, codeLL, funcCache, exprCache)
		if err != nil {
			fmt.Println(err)
			fmt.Printf("⛓️‍💥 Error while trying to compile %s\n", script)
			compileErr++
			continue
		}

		if err := p.GenBinary(scriptLL, script, rtObjs); err != nil {
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

// resolveTarget determines if target is a file or directory
// Returns (cwd, specificScript, error)
func resolveTarget(target string) (string, string, error) {
	// Convert to absolute path first
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", "", fmt.Errorf("error resolving absolute path for %s: %w", target, err)
	}

	info, err := os.Stat(absTarget)
	if err != nil {
		return "", "", fmt.Errorf("error accessing %s: %w", absTarget, err)
	}

	if info.IsDir() {
		return absTarget, "", nil
	}

	// Target is a file - must be a .spt file
	if !strings.HasSuffix(absTarget, SPT_SUFFIX) {
		return "", "", fmt.Errorf("error: %s is not a .spt script file", absTarget)
	}

	return filepath.Dir(absTarget), filepath.Base(absTarget), nil
}
