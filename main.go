package main

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
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
	OBJ_SUFFIX = ".o"
	EXE_SUFFIX = ".exe"

	SCRIPT_DIR  = "script"
	CODE_DIR    = "code"
	RUNTIME_DIR = "runtime"

	MOD_FILE = "pt.mod"

	// Platform
	OS_DARWIN  = "darwin"
	OS_WINDOWS = "windows"

	// Compiler binaries
	CC = "clang"

	// Compiler flags
	C_STD     = "-std=c11"
	OPT_LEVEL = "-O3"
	FPIC      = "-fPIC"

	// Linker flags
	LINK_DEAD_STRIP  = "-Wl,-dead_strip"
	LINK_GC_SECTIONS = "-Wl,--gc-sections"
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
	Config   buildConfig
	EmitIR   bool

	Ctx llvm.Context // LLVM context and code‐compiler for "code" files
}

type cliOptions struct {
	command string
	target  string
	emitIR  bool
}

// sanitizeCacheComponent returns a filesystem-safe cache path component.
// Returns error for path traversal attempts or empty values.
func sanitizeCacheComponent(v string) (string, error) {
	if v == "" {
		return "", fmt.Errorf("invalid cache component: empty string")
	}
	escaped := url.PathEscape(v)
	if escaped == "." || escaped == ".." {
		return "", fmt.Errorf("invalid cache component: %q", v)
	}
	return escaped, nil
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
	case OS_WINDOWS:
		if localAppData := os.Getenv("LocalAppData"); localAppData != "" {
			ptcache = filepath.Join(localAppData, "pluto")
			return ptcache
		}
		ptcache = filepath.Join(homeDir, "AppData", "Local", "pluto")

	case OS_DARWIN:
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
			modPath := parts[1]
			if err := compiler.ValidateModulePath(modPath); err != nil {
				err = fmt.Errorf("invalid module path in %s: %w", modFile, err)
				fmt.Println(err)
				return "", err
			}
			return modPath, nil
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
	cc := compiler.NewCodeCompiler(p.Ctx, p.ModName, p.RelPath, pkgCode)
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

// CompileScript returns an owned LLVM module. The caller must dispose it.
func (p *Pluto) CompileScript(scriptFile, script string, cc *compiler.CodeCompiler, codeLL string, funcCache map[string]*compiler.Func, exprCache map[compiler.ExprKey]*compiler.ExprInfo) (llvm.Module, error) {
	source, err := os.ReadFile(scriptFile)
	if err != nil {
		fmt.Printf("Error reading %s: %v\n", scriptFile, err)
		return llvm.Module{}, err
	}
	l := lexer.New(p.RelPath+"/"+filepath.Base(scriptFile), string(source))
	sp := parser.NewScriptParser(l)
	program := sp.Parse()
	if errs := sp.Errors(); len(errs) > 0 {
		for _, err := range errs {
			fmt.Println(err)
		}
		fmt.Printf("error parsing scriptFile %s for script %s\n", scriptFile, script)
		return llvm.Module{}, fmt.Errorf("parser errors for %s", scriptFile)
	}
	sc := compiler.NewScriptCompiler(p.Ctx, program, cc, funcCache, exprCache)
	scriptModule := sc.Compiler.Module
	moduleReturned := false
	defer func() {
		if !moduleReturned {
			scriptModule.Dispose()
		}
	}()

	// Only link if code module has content
	if codeLL != "" {
		buffer, err := llvm.NewMemoryBufferFromFile(codeLL)
		if err != nil {
			fmt.Printf("Error loading to memory buffer: %v\n", err)
			return llvm.Module{}, err
		}
		clone, err := p.Ctx.ParseIR(buffer)
		if err != nil {
			fmt.Printf("Error parsing IR: %v\n", err)
			return llvm.Module{}, err
		}
		// Link code-mode module into script's module in-memory
		if err := llvm.LinkModules(sc.Compiler.Module, clone); err != nil {
			fmt.Printf("Error linking modules: %v\n", err)
			return llvm.Module{}, err
		}
	}

	errs := sc.Compile()
	if len(errs) != 0 {
		for _, err := range errs {
			fmt.Println(err)
		}
		return llvm.Module{}, fmt.Errorf("error compiling scriptFile %s for script %s", scriptFile, script)
	}
	if p.EmitIR {
		if err := p.writeScriptIR(script, scriptModule); err != nil {
			return llvm.Module{}, err
		}
	}
	moduleReturned = true
	return scriptModule, nil
}

func (p *Pluto) writeScriptIR(script string, module llvm.Module) error {
	irFile := filepath.Join(p.CacheDir, SCRIPT_DIR, script+IR_SUFFIX)
	if err := os.MkdirAll(filepath.Dir(irFile), 0755); err != nil {
		return fmt.Errorf("create script IR cache dir: %w", err)
	}
	if err := os.WriteFile(irFile, []byte(module.String()), 0644); err != nil {
		return fmt.Errorf("write script IR %s: %w", irFile, err)
	}
	return nil
}

func (p *Pluto) GenBinary(scriptModule llvm.Module, bin string, rtObjs []string) error {
	// Use the default object suffix (".o") on all platforms, including
	// Windows when using the MinGW/UCRT toolchain.
	objExt := OBJ_SUFFIX
	objFile := filepath.Join(p.CacheDir, SCRIPT_DIR, bin+objExt)
	binFile := filepath.Join(p.Cwd, bin)
	if runtime.GOOS == OS_WINDOWS {
		binFile = binFile + EXE_SUFFIX
	}

	if err := p.emitObject(scriptModule, objFile); err != nil {
		fmt.Printf("object emission failed: %v\n", err)
		return err
	}

	linkArgs := []string{}

	switch runtime.GOOS {
	case OS_DARWIN:
		// Mach-O linker wants -dead_strip
		linkArgs = append(linkArgs, LINK_DEAD_STRIP)
	case OS_WINDOWS:
		// MinGW/COFF linker flags
		linkArgs = append(linkArgs, LINK_GC_SECTIONS)
	default:
		// ELF linkers (ld, lld) accept --gc-sections
		linkArgs = append(linkArgs, LINK_GC_SECTIONS)
	}
	linkArgs = append(linkArgs, objFile)
	linkArgs = append(linkArgs, rtObjs...)
	linkArgs = append(linkArgs, "-o", binFile)
	// libm is only needed/available on ELF-based systems
	if runtime.GOOS != OS_WINDOWS {
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

func New(cwd string, opts cliOptions) *Pluto {
	fmt.Println("Current working directory is", cwd)

	ptcache := defaultPTCache()
	cfg := currentBuildConfig()
	// Include version in cache path to isolate different compiler versions
	safeVersion, err := sanitizeCacheComponent(Version)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	versionedCache := filepath.Join(ptcache, safeVersion)
	fmt.Printf("Using PTCACHE: %s\n", versionedCache)
	if err := os.MkdirAll(versionedCache, 0755); err != nil {
		fmt.Printf("Error creating PTCACHE directory: %v\n", err)
		os.Exit(1)
	}

	p := &Pluto{
		Cwd:     cwd,
		PtCache: versionedCache,
		Config:  cfg,
		EmitIR:  opts.emitIR,
		Ctx:     llvm.NewContext(),
	}

	err = p.resolveModPaths(cwd)
	if err != nil {
		os.Exit(1)
	}

	// Use module path (slashes) as unique cache key
	targetSegment, err := p.Config.targetCPUCacheSegment()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	p.CacheDir = filepath.Join(p.PtCache, filepath.FromSlash(p.ModPath))
	if targetSegment != "" {
		p.CacheDir = filepath.Join(p.PtCache, targetSegment, filepath.FromSlash(p.ModPath))
	}
	fmt.Printf("Cache dir is %s\n", p.CacheDir)
	fmt.Println()

	return p
}

func cliCommand(arg string) string {
	switch arg {
	case "-v", "-version":
		return "version"
	case "-c", "-clean":
		return "clean"
	default:
		return ""
	}
}

func parseCLIArgs(args []string) (cliOptions, error) {
	var opts cliOptions
	var commandArg string
	for _, arg := range args {
		if command := cliCommand(arg); command != "" {
			if opts.command != "" {
				return cliOptions{}, fmt.Errorf("%s cannot be combined with %s", arg, commandArg)
			}
			opts.command = command
			commandArg = arg
			continue
		}

		switch arg {
		case "-emit-ir":
			opts.emitIR = true
		default:
			if strings.HasPrefix(arg, "-") {
				return cliOptions{}, fmt.Errorf("unknown flag: %s", arg)
			}
			if opts.target != "" {
				return cliOptions{}, fmt.Errorf("multiple compile targets provided: %s and %s", opts.target, arg)
			}
			opts.target = arg
		}
	}
	if opts.command != "" && (opts.target != "" || opts.emitIR) {
		return cliOptions{}, fmt.Errorf("%s cannot be combined with other arguments", commandArg)
	}
	return opts, nil
}

func main() {
	opts, err := parseCLIArgs(os.Args[1:])
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	switch opts.command {
	case "version":
		printVersion()
		return
	case "clean":
		runClean()
		return
	}

	// Default: compile
	runCompile(opts)
}

// runClean removes the cache directory for the current version.
func runClean() {
	ptcache := defaultPTCache()
	safeVersion, err := sanitizeCacheComponent(Version)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	versionCache := filepath.Join(ptcache, safeVersion)

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
func runCompile(opts cliOptions) {
	// Determine target path (file or directory)
	target, err := os.Getwd()
	if err != nil {
		fmt.Printf("Error getting current working directory: %v\n", err)
		os.Exit(1)
	}
	if opts.target != "" {
		target = opts.target
	}

	// Resolve target to cwd and optional specific script
	cwd, specificScript, err := resolveTarget(target)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	p := New(cwd, opts)

	// Prepare runtime once (in PtCache root, shared across all projects)
	rtObjs, err := prepareRuntime(p.PtCache, p.Config)
	if err != nil {
		fmt.Printf("Error preparing runtime: %v\n", err)
		os.Exit(1)
	}

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

	compileErr := 0
	binErr := 0
	funcCache := make(map[string]*compiler.Func)
	exprCache := make(map[compiler.ExprKey]*compiler.ExprInfo)
	for _, scriptFile := range scriptFiles {
		script := strings.TrimSuffix(filepath.Base(scriptFile), SPT_SUFFIX)
		fmt.Println("🛠️ Starting compile for script: " + script)
		scriptModule, err := p.CompileScript(scriptFile, script, codeCompiler, codeLL, funcCache, exprCache)
		if err != nil {
			fmt.Println(err)
			fmt.Printf("⛓️‍💥 Error while trying to compile %s\n", script)
			compileErr++
			continue
		}

		err = func() error {
			defer scriptModule.Dispose()
			return p.GenBinary(scriptModule, script, rtObjs)
		}()
		if err != nil {
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
