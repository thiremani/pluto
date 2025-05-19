package main

import (
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

	SCRIPT_DIR = "script"
	CODE_DIR   = "code"

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

func compileCode(codeFiles []string, pkgDir, pkg string, ctx llvm.Context) (*compiler.Compiler, string, error) {
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

	c := compiler.NewCompiler(ctx, pkg)
	c.CompileConst(pkgCode)
	ir := c.GenerateIR()

	outPath := filepath.Join(pkgDir, CODE_DIR, pkg+IR_SUFFIX)
	os.MkdirAll(filepath.Dir(outPath), 0755)
	if err := os.WriteFile(outPath, []byte(ir), 0644); err != nil {
		fmt.Printf("Error writing IR to %s: %v\n", outPath, err)
		return c, outPath, err
	}
	return c, outPath, nil
}

func compileScript(script, pkgDir, scriptFile string, codeCompiler *compiler.Compiler, codePath string, ctx llvm.Context) (string, error) {
	source, err := os.ReadFile(scriptFile)
	if err != nil {
		fmt.Printf("Error reading %s: %v\n", scriptFile, err)
		return "", err
	}
	l := lexer.New(string(source))
	sp := parser.NewScriptParser(l)
	ast := sp.Parse()
	c := compiler.NewCompiler(ctx, script)

	buffer, err := llvm.NewMemoryBufferFromFile(codePath)
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
		fmt.Println(err)
		return "", err
	}

	c.ExtSymbols = codeCompiler.Symbols
	c.CompileScript(ast)
	ir := c.GenerateIR()

	llName := script + IR_SUFFIX
	outPath := filepath.Join(pkgDir, SCRIPT_DIR, llName)
	os.MkdirAll(filepath.Dir(outPath), 0755)
	if err := os.WriteFile(outPath, []byte(ir), 0644); err != nil {
		fmt.Printf("Error writing IR to %s: %v\n", outPath, err)
		return "", err
	}
	return outPath, nil
}

func genBinary(bin, scriptLL, pkgDir, cwd string) error {
	// Create temp files
	optFile := filepath.Join(pkgDir, SCRIPT_DIR, bin+".opt.ll")
	objFile := filepath.Join(pkgDir, SCRIPT_DIR, bin+".o")
	binFile := filepath.Join(cwd, bin)

	// Optimization pass
	optCmd := exec.Command("opt", OPT_LEVEL, "-S", scriptLL, "-o", optFile)
	if output, err := optCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("optimization failed: %s\n%s", err, string(output))
	}

	// Compile to object file
	llcCmd := exec.Command("llc", "-filetype=obj", "-relocation-model=pic", optFile, "-o", objFile)
	if output, err := llcCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("llc compilation failed: %s\n%s", err, string(output))
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
		return fmt.Errorf("linking failed: %s\n%s", err, string(output))
	}

	return nil
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

	// TODO change pkgName to what is given by pt.mod
	pkg := filepath.Base(cwd)
	fmt.Println("Extracted pkg name is", pkg)

	pkgDir := filepath.Join(ptcache, pkg)

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
	codeCompiler, codePath, err := compileCode(codeFiles, pkgDir, pkg, ctx)
	if err != nil {
		return
	}

	for _, scriptFile := range scriptFiles {
		script := strings.TrimSuffix(filepath.Base(scriptFile), SPT_SUFFIX)
		scriptLL, err := compileScript(script, pkgDir, scriptFile, codeCompiler, codePath, ctx)
		if err != nil {
			continue
		}

		if err := genBinary(script, scriptLL, pkgDir, cwd); err != nil {
			fmt.Printf("⚠️ Binary generation failed for %s: %v\n", script, err)
		} else {
			fmt.Printf("✅ Successfully built binary for script: %s\n", script)
		}
	}
}
