package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"pluto/ast"
	"pluto/compiler"
	"pluto/lexer"
	"pluto/parser"
	"runtime"
	"strings"
)

var PT_SUFFIX = ".pt"
var SPT_SUFFIX = ".spt"
var IR_SUFFIX = ".ll"

var CODE_DIR = "code"
var SCRIPT_DIR = "script"
var LINKED_DIR = "linked"

var OPT_LEVEL = "-O2" // Can be configured via flag

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

func compileCode(codeFiles []string, pkgDir, pkg string) {
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

		if len(cp.Errors()) > 0 {
			for _, e := range cp.Errors() {
				fmt.Printf("%s: %s\n", codeFile, e)
			}
			continue
		}
		pkgCode.Merge(code)
	}

	c := compiler.NewCompiler(pkg)
	c.CompileConst(pkgCode)
	ir := c.GenerateIR()

	outPath := filepath.Join(pkgDir, CODE_DIR, pkg+IR_SUFFIX)
	os.MkdirAll(filepath.Dir(outPath), 0755)
	if err := os.WriteFile(outPath, []byte(ir), 0644); err != nil {
		fmt.Printf("Error writing IR to %s: %v\n", outPath, err)
	}
}

func compileScript(script, scriptFile, pkgDir, pkg string) string {
	source, err := os.ReadFile(scriptFile)
	if err != nil {
		fmt.Printf("Error reading %s: %v\n", scriptFile, err)
		return ""
	}
	l := lexer.New(string(source))
	sp := parser.NewScriptParser(l)
	ast := sp.Parse()
	c := compiler.NewCompiler(script)
	c.CompileScript(ast)
	ir := c.GenerateIR()

	llName := script + ".ll"
	outPath := filepath.Join(pkgDir, SCRIPT_DIR, llName)
	os.MkdirAll(filepath.Dir(outPath), 0755)
	if err := os.WriteFile(outPath, []byte(ir), 0644); err != nil {
		fmt.Printf("Error writing IR to %s: %v\n", outPath, err)
		return ""
	}
	return outPath
}

// Copy copies the contents of the file at srcpath to a regular file
// at dstpath. If the file named by dstpath already exists, it is
// truncated. The function does not copy the file mode, file
// permission bits, or file attributes.
func Copy(srcpath, dstpath string) (err error) {
	r, err := os.Open(srcpath)
	if err != nil {
		fmt.Println(err)
		return err
	}
	defer r.Close() // ignore error: file was opened read-only.

	w, err := os.Create(dstpath)
	if err != nil {
		fmt.Println(err)
		return err
	}

	defer func() {
		// Report the error, if any, from Close, but do so
		// only if there isn't already an outgoing error.
		c := w.Close()
		if c != nil {
			fmt.Println(c)
		}
		if err == nil {
			err = c
		}
	}()

	_, err = io.Copy(w, r)
	if err != nil {
		fmt.Println(err)
	}
	return err
}

// runLLVMLink links scriptLL and codeLL files using llvm-link command into outLL
// It creates the directory for outLL if it does not exist
// In case codeLL = "" it just copies scriptLL into outLL
func runLLVMLink(pkgDir, script, scriptLL, codeLL string) (string, error) {
	outLL := filepath.Join(pkgDir, LINKED_DIR, script+IR_SUFFIX)
	os.MkdirAll(filepath.Dir(outLL), 0755)
	if codeLL == "" {
		// nothing to link
		return outLL, Copy(scriptLL, outLL)
	}
	args := []string{"-S", "-o", outLL, codeLL, scriptLL}
	cmd := exec.Command("llvm-link", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("⚠️ Failed linking to output file %q. Err: %v\n", outLL, err)
		fmt.Printf("Input script: %q. Input code file: %q", scriptLL, codeLL)
		return "", err
	}
	return outLL, nil
}

// build locates the one package under ptcache, then for each
// script IR in ptcache/<pkg>/script/*.ll it runs:
//
//	llvm-link ptcache/<pkg>/code/<pkg>.ll script.ll -o ptcache/<pkg>/linked/script.ll
// if there is no <pkg>.ll in code folder to link then it simply copies the script.ll to linked folder
// it then builds binary for the linked script.ll

func buildScript(script, scriptLL, pkgDir, pkg, cwd string) {
	codeLL := filepath.Join(pkgDir, CODE_DIR, pkg+IR_SUFFIX)
	if _, err := os.Stat(codeLL); err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("⚠️ Error checking code IR %q: %v. Build failed.\n", codeLL, err)
			return
		}
		codeLL = ""
	}

	outLL, err := runLLVMLink(pkgDir, script, scriptLL, codeLL)
	if err != nil {
		return
	}

	if err := genBinary(script, outLL, pkgDir, cwd); err != nil {
		fmt.Printf("⚠️ Binary generation failed for %s: %v\n", script, err)
	} else {
		fmt.Printf("✅ Successfully built binary for script: %s\n", script)
	}
}

func genBinary(bin, linkedLL, pkgDir, cwd string) error {
	// Create temp files
	optFile := filepath.Join(pkgDir, SCRIPT_DIR, bin+".opt.ll")
	objFile := filepath.Join(pkgDir, SCRIPT_DIR, bin+".o")
	binFile := filepath.Join(cwd, bin)

	// Optimization pass
	optCmd := exec.Command("opt", OPT_LEVEL, "-S", linkedLL, "-o", optFile)
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

	compileCode(codeFiles, pkgDir, pkg)
	for _, scriptFile := range scriptFiles {
		script := strings.TrimSuffix(filepath.Base(scriptFile), SPT_SUFFIX)
		scriptLL := compileScript(script, scriptFile, pkgDir, pkg)
		buildScript(script, scriptLL, pkgDir, pkg, cwd)
	}
}
