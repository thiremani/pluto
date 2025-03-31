package main

import (
	"flag"
	"fmt"
	"os"
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

var allowedExts = []string{PT_SUFFIX, SPT_SUFFIX}

func isValidExtension(fileName string) (bool, bool) {
	ext := filepath.Ext(fileName) // Extract file extension
	for _, expectedExt := range allowedExts {
		if runtime.GOOS == "windows" {
			if strings.EqualFold(ext, expectedExt) {
				return true, expectedExt == SPT_SUFFIX
			}
		} else {
			if ext == expectedExt {
				return true, expectedExt == SPT_SUFFIX
			}
		}
	}
	return false, false
}

func main() {
	var outputFile string
	flag.StringVar(&outputFile, "o", "", "Output file for the generated IR")
	flag.StringVar(&outputFile, "output", "", "Output file for the generated IR (long form)")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Println("Error: no input file specified")
		os.Exit(1)
	}

	inputFile := args[0]
	isValidExt, isScript := isValidExtension(inputFile)
	if !isValidExt {
		fmt.Printf("Error: Pluto files must use %s (code) or %s (script) extensions\n",
			PT_SUFFIX, SPT_SUFFIX)
		os.Exit(1)
	}

	source, err := os.ReadFile(inputFile)
	if err != nil {
		fmt.Printf("Error reading file: %v\n", err)
		os.Exit(1)
	}

	// Parse and compile
	l := lexer.New(string(source))
	var ast *ast.Program
	if isScript {
		sp := parser.NewScriptParser(l)
		ast = sp.Parse()
	} else {
		cp := parser.NewCodeParser(l)
		ast = cp.Parse()
	}
	c := compiler.NewCompiler("pluto_module")
	c.Compile(ast)
	ir := c.GenerateIR()

	if outputFile == "" {
		outputFile = strings.TrimSuffix(inputFile, PT_SUFFIX)
		outputFile += IR_SUFFIX
	}

	// Create parent directories if they don't exist
	dir := filepath.Dir(outputFile)
	if dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Printf("Error creating directories: %v\n", err)
			os.Exit(1)
		}
	}

	err = os.WriteFile(outputFile, []byte(ir), 0644)
	if err != nil {
		fmt.Printf("Error writing IR to file: %v\n", err)
		os.Exit(1)
	}
}
