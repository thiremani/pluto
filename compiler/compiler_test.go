package compiler

import (
	"strings"
    "testing"

    "pluto/lexer"
    "pluto/parser"
)

func TestStringCompile(t *testing.T) {
    input := `"hello"`
    l := lexer.New(input)
    p := parser.New(l)
    program := p.ParseProgram()
    
    comp := NewCompiler("test")
    comp.Compile(program)
    ir := comp.GenerateIR()
    
    expectedIR := `@str_literal_0 = private unnamed_addr constant [6 x i8] c"hello\00"`
    if !strings.Contains(ir, expectedIR) {
        t.Errorf("IR does not contain string constant:\n%s", ir)
    }
}