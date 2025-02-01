package main

import (
	"fmt"
	"os"
	"pluto/compiler"
	"pluto/lexer"
	"pluto/parser"
)

func main() {
	source, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Printf("Error reading file: %v\n", err)
		os.Exit(1)
	}

	// Parse and compile
	l := lexer.New(string(source))
	p := parser.New(l)
	ast := p.ParseProgram()
	comp := compiler.NewCompiler("pluto_module")
	comp.Compile(ast)
	fmt.Println(comp.GenerateIR())
}