package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"pluto/compiler"
	"pluto/lexer"
	"pluto/parser"
	"strings"
)

var PT_SUFFIX = ".pt"
var IR_SUFFIX = ".ll"

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
	source, err := os.ReadFile(inputFile)
	if err != nil {
		fmt.Printf("Error reading file: %v\n", err)
		os.Exit(1)
	}

	// Parse and compile
	l := lexer.New(string(source))
	p := parser.New(l)
	ast := p.ParseProgram()
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
