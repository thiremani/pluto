package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"pluto/ast"
	"pluto/compiler"
	"pluto/lexer"
	"pluto/parser"
	"runtime"
	"strings"

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

	OPT_LEVEL = "-O2" // Can be configured via flag
)

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

func compileCode(codeFiles []string, cacheDir, modPath string, ctx llvm.Context) (*compiler.Compiler, string, error) {
	if len(codeFiles) == 0 {
		return nil, "", nil
	}

	pkgCode := ast.NewCode()
	for _, codeFile := range codeFiles {
		source, err := os.ReadFile(codeFile)
		if err != nil {
			fmt.Printf("Error reading %s: %v\n", codeFile, err)
			continue
		}
		l := lexer.New(string(source))
		cp := parser.NewCodeParser(l)
		code := cp.Parse()

		if errs := cp.Errors(); len(errs) > 0 {
			for _, e := range errs {
				fmt.Printf("%s: %s\n", codeFile, e)
			}
			continue
		}
		pkgCode.Merge(code)
	}

	c := compiler.NewCompiler(ctx, modPath)
	c.CompileConst(pkgCode)
	ir := c.GenerateIR()

	pkg := filepath.Base(modPath)
	fmt.Println("Pkg name is", pkg)
	codeLL := filepath.Join(cacheDir, CODE_DIR, pkg+IR_SUFFIX)
	os.MkdirAll(filepath.Dir(codeLL), 0755)
	if err := os.WriteFile(codeLL, []byte(ir), 0644); err != nil {
		fmt.Printf("Error writing IR to %s: %v\n", codeLL, err)
		return nil, "", err
	}
	return c, codeLL, nil
}

func compileScript(scriptFile, script, cacheDir string, codeCompiler *compiler.Compiler, codeLL string, ctx llvm.Context) (string, error) {
	source, err := os.ReadFile(scriptFile)
	if err != nil {
		fmt.Printf("Error reading %s: %v\n", scriptFile, err)
		return "", err
	}
	l := lexer.New(string(source))
	sp := parser.NewScriptParser(l)
	ast := sp.Parse()
	c := compiler.NewCompiler(ctx, script)

	// Only link if code module has content
	if codeCompiler != nil && !codeCompiler.Module.IsNil() {
		buffer, err := llvm.NewMemoryBufferFromFile(codeLL)
		if err != nil {
			fmt.Printf("Error loading to memory buffer: %v\n", err)
			return "", err
		}
		clone, err := ctx.ParseIR(buffer)
		if err != nil {
			fmt.Printf("Error parsing IR: %v\n", err)
			return "", err
		}
		// Link code-mode module into script's module in-memory
		if err := llvm.LinkModules(c.Module, clone); err != nil {
			fmt.Printf("Error linking modules: %v\n", err)
			return "", err
		}
		c.ExtSymbols = codeCompiler.Symbols
	}

	c.CompileScript(ast)
	ir := c.GenerateIR()

	llName := script + IR_SUFFIX
	scriptLL := filepath.Join(cacheDir, SCRIPT_DIR, llName)
	os.MkdirAll(filepath.Dir(scriptLL), 0755)
	if err := os.WriteFile(scriptLL, []byte(ir), 0644); err != nil {
		fmt.Printf("Error writing IR to %s: %v\n", scriptLL, err)
		return "", err
	}
	return scriptLL, nil
}

func genBinary(scriptLL, bin, cacheDir, cwd string) error {
	// Create temp files
	optFile := filepath.Join(cacheDir, SCRIPT_DIR, bin+OPT_SUFFIX+IR_SUFFIX)
	objFile := filepath.Join(cacheDir, SCRIPT_DIR, bin+OBJ_SUFFIX)
	binFile := filepath.Join(cwd, bin)

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
		"-o",
		binFile,
	)

	// Link executable (with dead code elimination)
	clangCmd := exec.Command("clang", linkArgs...)
	if output, err := clangCmd.CombinedOutput(); err != nil {
		fmt.Printf("linking failed: %s\n%s\n", err, string(output))
		return err
	}

	return nil
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

// getFullModPath returns the full import path for cwd by combining the
// module name from pt.mod with the relative subpath under that root.
func getFullModPath(cwd string) (string, error) {
	root, err := findModRoot(cwd)
	if err != nil {
		return "", err
	}
	moduleName, err := parseModuleName(filepath.Join(root, MOD_FILE))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil {
		err = fmt.Errorf("rel %s -> %s: %w", root, cwd, err)
		return "", err
	}
	if rel == "." {
		return moduleName, nil
	}
	// ensure forward slashes
	rel = filepath.ToSlash(rel)
	return moduleName + "/" + rel, nil
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

	fmt.Println("Current working directory is", cwd)

	ptcache := defaultPTCache()
	fmt.Printf("Using PTCACHE: %s\n", ptcache)
	if err := os.MkdirAll(ptcache, 0755); err != nil {
		fmt.Printf("Error creating PTCACHE directory: %v\n", err)
		os.Exit(1)
	}

	// Determine module import path from pt.mod
	modPath, err := getFullModPath(cwd)
	if err != nil {
		os.Exit(1)
	}

	fmt.Printf("Resolved module path is %s\n", modPath)
	// Use module path (slashes) as unique cache key
	cacheDir := filepath.Join(ptcache, filepath.FromSlash(modPath))
	fmt.Printf("Cache dir is %s\n", cacheDir)

	dirEntries, err := os.ReadDir(cwd)
	if err != nil {
		fmt.Printf("Error reading current directory: %v\n", err)
		os.Exit(1)
	}

	codeFiles := []string{}
	scriptFiles := []string{}
	for _, entry := range dirEntries {
		if entry.IsDir() {
			// TODO compile within directories too
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, PT_SUFFIX) {
			codeFiles = append(codeFiles, filepath.Join(cwd, name))
		} else if strings.HasSuffix(name, SPT_SUFFIX) {
			scriptFiles = append(scriptFiles, filepath.Join(cwd, name))
		}
	}

	ctx := llvm.NewContext()
	codeCompiler, codeLL, err := compileCode(codeFiles, cacheDir, modPath, ctx)
	if err != nil {
		return
	}

	for _, scriptFile := range scriptFiles {
		script := strings.TrimSuffix(filepath.Base(scriptFile), SPT_SUFFIX)
		scriptLL, err := compileScript(scriptFile, script, cacheDir, codeCompiler, codeLL, ctx)
		if err != nil {
			continue
		}

		if err := genBinary(scriptLL, script, cacheDir, cwd); err != nil {
			fmt.Printf("⚠️ Binary generation failed for %s: %v\n", script, err)
		} else {
			fmt.Printf("✅ Successfully built binary for script: %s\n", script)
		}
	}
}
