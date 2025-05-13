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
)

var PT_SUFFIX = ".pt"
var SPT_SUFFIX = ".spt"
var IR_SUFFIX = ".ll"

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

func compileCode(pkg, ptcache string, codeFiles []string) {
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

	outPath := filepath.Join(ptcache, pkg, "code", pkg+".ll")
	os.MkdirAll(filepath.Dir(outPath), 0755)
	if err := os.WriteFile(outPath, []byte(ir), 0644); err != nil {
		fmt.Printf("Error writing IR to %s: %v\n", outPath, err)
	}

	return c.symbols
}

func compileScript(pkg, ptcache string, scriptFiles []string) {
	for _, scriptFile := range scriptFiles {
		source, err := os.ReadFile(scriptFile)
		if err != nil {
			fmt.Printf("Error reading %s: %v\n", scriptFile, err)
			continue
		}
		l := lexer.New(string(source))
		sp := parser.NewScriptParser(l)
		ast := sp.Parse()
		modName := strings.TrimSuffix(filepath.Base(scriptFile), SPT_SUFFIX)
		c := compiler.NewCompiler(modName)
		c.CompileScript(ast)
		ir := c.GenerateIR()

		llName := strings.TrimSuffix(filepath.Base(scriptFile), SPT_SUFFIX) + ".ll"
		outPath := filepath.Join(ptcache, pkg, "script", llName)
		os.MkdirAll(filepath.Dir(outPath), 0755)
		if err := os.WriteFile(outPath, []byte(ir), 0644); err != nil {
			fmt.Printf("Error writing IR to %s: %v\n", outPath, err)
		}
	}
}

// linkIR locates the one package under ptcache, then for each
// script IR in ptcache/<pkg>/script/*.ll it runs:
//
//	llvm-link ptcache/<pkg>/code/<pkg>.ll script.ll -o script_combined.ll
//
// if there is no .../code/<pkg>.ll then the function is a no-op
func linkIR(ptcache, pkg string) {
	pkgDir := filepath.Join(ptcache, pkg)
	if _, err := os.Stat(pkgDir); err != nil {
		fmt.Println(err)
		return
	}

	// Check if code IR exists
	codeLL := filepath.Join(pkgDir, "code", pkg+".ll")
	if _, err := os.Stat(codeLL); err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("⚠️ Error checking code IR %q: %v\n", codeLL, err)
		}
		return
	}

	scriptDir := filepath.Join(pkgDir, "script")
	scripts, err := os.ReadDir(scriptDir)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("⚠️ Error reading scripts dir %q: %v\n", scriptDir, err)
		}
		return
	}

	for _, s := range scripts {
		if s.IsDir() || !strings.HasSuffix(s.Name(), ".ll") {
			continue
		}

		scriptLL := filepath.Join(scriptDir, s.Name())
		base := strings.TrimSuffix(s.Name(), ".ll")
		outLL := filepath.Join(scriptDir, base+"_combined.ll")

		cmd := exec.Command("llvm-link", codeLL, scriptLL, "-o", outLL)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Printf("⚠️ Failed linking %q + %q → %q: %v\n", codeLL, scriptLL, outLL, err)
			continue
		}
		fmt.Printf("↪ Successfully linked %q + %q → %q\n", codeLL, scriptLL, outLL)
	}
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Printf("Error getting current working directory: %v\n", err)
		os.Exit(1)
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

	compileCode(pkg, ptcache, codeFiles)
	compileScript(pkg, ptcache, scriptFiles)
	linkIR(ptcache, pkg)
}
