package compiler

import (
	"strings"
	"testing"

	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/parser"
	"tinygo.org/x/go-llvm"
)

func TestStringCompile(t *testing.T) {
	input := `"hello"`
	l := lexer.New("TestStringCompile", input)
	sp := parser.NewScriptParser(l)
	program := sp.Parse()

	sc := NewScriptCompiler(llvm.NewContext(), "test", program, nil)
	sc.Compile()
	ir := sc.Compiler.GenerateIR()

	expectedIR := `@printf_fmt_0 = constant [7 x i8] c"hello\0A\00"`
	if !strings.Contains(ir, expectedIR) {
		t.Errorf("IR does not contain string constant:\n%s", ir)
	}
}

func TestFormatIdentifiers(t *testing.T) {
	input := `x = 5
six = 6`

	l := lexer.New("TestFormatIdentifiers", input)
	sp := parser.NewScriptParser(l)
	program := sp.Parse()

	sc := NewScriptCompiler(llvm.NewContext(), "TestFormatIdentifiers", program, nil)
	sc.Compile()
	res, vals := sc.Compiler.formatIdentifiers("x = -x, six = -six")
	expStr := "x = %ld, six = %ld"
	if res != expStr {
		t.Errorf("formattedStr does not match expected. got: %s, expected: %s", res, expStr)
	}
	if len(vals) != 2 {
		t.Errorf("len(vals) does not match expected. got: %d, expected: 2", len(vals))
	}
	expVals := []llvm.Value{llvm.ConstInt(sc.Compiler.Context.Int64Type(), 5, false), llvm.ConstInt(sc.Compiler.Context.Int64Type(), 6, false)}
	for i, val := range vals {
		if val != expVals[i] {
			t.Errorf("vals[%d] does not match expected.", i)
			t.Error("got", val, "expected", expVals[i])
		}
	}
}

func TestConstCompile(t *testing.T) {
	input := `pi = 3.1415926535
answer = 42
greeting = "hello"`

	l := lexer.New("TestConstCompile", input)
	cp := parser.NewCodeParser(l)
	code := cp.Parse()

	c := NewCodeCompiler(llvm.NewContext(), "testConst", code)
	c.Compile()
	ir := c.Compiler.GenerateIR()

	expPi := "@pi = unnamed_addr constant double 0x400921FB54411744"
	if !strings.Contains(ir, expPi) {
		t.Errorf("IR does not contain global constant for pi. Exp: %s, ir: \n%s", expPi, ir)
	}

	expAns := "@answer = unnamed_addr constant i64 42"
	if !strings.Contains(ir, expAns) {
		t.Errorf("IR does not contain global constant for answer. Exp: %s, ir: \n%s", expAns, ir)
	}

	expGreeting := `@greeting = unnamed_addr constant [6 x i8] c"hello\00"`

	if !strings.Contains(ir, expGreeting) {
		t.Errorf("IR does not contain global constant for greeting. Exp: %s, ir: \n%s", expGreeting, ir)
	}
}
