package compiler

import (
	"strings"
	"testing"

	"pluto/lexer"
	"pluto/parser"
	"tinygo.org/x/go-llvm"
)

func TestStringCompile(t *testing.T) {
	input := `"hello"`
	l := lexer.New(input)
	sp := parser.NewScriptParser(l)
	program := sp.Parse()

	comp := NewCompiler("test")
	comp.CompileScript(program)
	ir := comp.GenerateIR()

	expectedIR := `@printf_fmt_0 = constant [7 x i8] c"hello\0A\00"`
	if !strings.Contains(ir, expectedIR) {
		t.Errorf("IR does not contain string constant:\n%s", ir)
	}
}

func TestFormatIdentifiers(t *testing.T) {
	input := `x = 5
six = 6`

	l := lexer.New(input)
	sp := parser.NewScriptParser(l)
	program := sp.Parse()

	c := NewCompiler("TestFormatIdentifiers")
	c.CompileScript(program)
	res, vals := c.formatIdentifiers("x = -x, six = -six")
	expStr := "x = %ld, six = %ld"
	if res != expStr {
		t.Errorf("formattedStr does not match expected. got: %s, expected: %s", res, expStr)
	}
	if len(vals) != 2 {
		t.Errorf("len(vals) does not match expected. got: %d, expected: 2", len(vals))
	}
	expVals := []llvm.Value{llvm.ConstInt(c.context.Int64Type(), 5, false), llvm.ConstInt(c.context.Int64Type(), 6, false)}
	for i, val := range vals {
		if val != expVals[i] {
			t.Errorf("vals[%d] does not match expected.", i)
			t.Error("got", val, "expected", expVals[i])
		}
	}
}
