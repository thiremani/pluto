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
	p := parser.New(l, true)
	program := p.ParseProgram()

	comp := NewCompiler("test")
	comp.Compile(program)
	ir := comp.GenerateIR()

	expectedIR := `@printf_fmt_0 = global [7 x i8] c"hello\0A\00"`
	if !strings.Contains(ir, expectedIR) {
		t.Errorf("IR does not contain string constant:\n%s", ir)
	}
}
