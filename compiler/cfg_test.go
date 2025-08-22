package compiler

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tinygo.org/x/go-llvm"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/parser"
	"github.com/thiremani/pluto/token"
)

// The helper function now uses require to stop immediately if parsing fails.
func parseInput(t *testing.T, name, input string) *ast.Program {
	l := lexer.New(name, input)
	p := parser.NewScriptParser(l)
	prog := p.Parse()

	// require.Empty stops the test if the parser has errors.
	require.Empty(t, p.Errors(), "Parser errors found for input: %s", input)
	return prog
}

func TestCFGAnalysis(t *testing.T) {
	validCases := getValidTestCases()
	errorCases := getErrorTestCases()
	
	t.Run("ValidCases", func(t *testing.T) {
		for _, tc := range validCases {
			t.Run(tc.name, func(t *testing.T) {
				runCFGTest(t, tc, false)
			})
		}
	})
	
	t.Run("ErrorCases", func(t *testing.T) {
		for _, tc := range errorCases {
			t.Run(tc.name, func(t *testing.T) {
				runCFGTest(t, tc, true)
			})
		}
	})
}

func getValidTestCases() []cfgTestCase {
	return []cfgTestCase{
		{
			name:  "Correct Simple Program",
			input: "x = 1\ny = x + 1\ny",
		},
		{
			name:  "Allowed Write then ConditionalWrite",
			input: "x = 100\nx = 1 > 2 99\nx",
		},
		{
			name:  "Allowed ConditionalWrite then ConditionalWrite", 
			input: "x = 1 > 3 1\nx = 2 > 1 2\nx",
		},
		{
			name:  "Read after ConditionalWrite",
			input: "x = 5 > 2 1\ny = x + 1\ny",
		},
		{
			name:  "Write Read Write",
			input: "x = 1\nx\nx = 2\nx",
		},
		{
			name:  "PrintOnly",
			input: `"hello"`,
		},
		{
			name:  "EmptyProgram",
			input: ``,
		},
		{
			name: "FormatMarker After Def",
			input: `x = 42
"Answer: -x"`, // x defined before marker
		},
		{
			name:  "Var Not Defined",
			input: `"Value: -x%s"`,
		},
	}
}

func getErrorTestCases() []cfgTestCase {
	return []cfgTestCase{
		{
			name:          "Use Before Definition",
			input:         "x = y + 1",
			errorContains: `variable "y" has not been defined`,
		},
		{
			name:          "Use cond Before definition",
			input:         "a = b > 2 1",
			errorContains: `variable "b" has not been defined`,
		},
		{
			name:          "Unconditional Write After Unconditional Write",
			input:         "x = 1\nx = 2\nx",
			errorContains: `unconditional assignment to "x" overwrites a previous value that was never used. It was previously written at line 1:1`,
		},
		{
			name:          "Simple Dead Store (Unused Variable)",
			input:         "x = 1",
			errorContains: `value assigned to "x" is never used`,
		},
		{
			name:          "Complex Dead Store",
			input:         "a = 1\nb = 2\nb",
			errorContains: `value assigned to "a" is never used`,
		},
		{
			name:          "Conditional Write then Unconditional Write",
			input:         "x = 1 > 0 10\nx = 20\nx",
			errorContains: `value assigned to "x" in conditional statement is never used`,
		},
		{
			name:          "Conditional Write then Unconditional Write (Dead Store)",
			input:         "a = 1\nx = a > 0 10\nx = 20",
			errorContains: `value assigned to "x" is never used`,
		},
		{
			name:          "Read after write but still a dead store later",
			input:         "a = 1\nb = a\na = 2\nb", // The write 'a = 2' is a dead store
			errorContains: `value assigned to "a" is never used`,
		},
		{
			name:          "Multi-variable Dead Store",
			input:         "a=1\nb=2\nc=3\na, b",
			errorContains: `value assigned to "c" is never used`,
		},
		{
			name:          "Print Use Before Def",
			input:         `"x is", x`,
			errorContains: `variable "x" has not been defined`,
		},
		{
			name: "Write To Constant",
			code: `a = 4`,
			input: `
x = a
a = 2`, // redeclaring/writing to const 'a'
			errorContains: `cannot write to constant "a"`,
		},
	}
}

type cfgTestCase struct {
	name          string
	input         string
	code          string
	errorContains string
}

func runCFGTest(t *testing.T, tc cfgTestCase, expectError bool) {
	prog := parseInput(t, tc.name, tc.input)
	cp := parser.NewCodeParser(lexer.New(tc.name, tc.code))
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "TestCFGAnalysis", cp.Parse())
	cc.Compile()
	cfg := NewCFG(nil, cc)
	cfg.Analyze(prog.Statements)

	if expectError {
		assertHasExpectedError(t, cfg.Errors, tc.errorContains)
	} else {
		assert.Empty(t, cfg.Errors, "Expected no errors, but got some.")
	}
}

func assertHasExpectedError(t *testing.T, errors []*token.CompileError, expectedMessage string) {
	assert.NotEmpty(t, errors, "Expected an error, but got none.")
	
	if len(errors) > 0 {
		assert.Contains(t, errors[0].Msg, expectedMessage, "Error message mismatch")
	}
}

func TestValidateFuncOutputsNotDeadStore(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	// A code-mode function that only writes to its output "res"
	code := `
res = onlyOut(x)
    res = x * 2
`
	cp := parser.NewCodeParser(lexer.New("onlyOut.pt", code))
	codeAST := cp.Parse()
	require.Empty(t, cp.Errors())

	cc := NewCodeCompiler(ctx, "onlyOut", codeAST)
	errs := cc.Compile()
	// No errors because "res" is an output and seeded live
	assert.Empty(t, errs, "output-only write should not trigger dead-store")
}

func TestValidateFuncLocalDeadStore(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	// A code-mode function with a local "tmp" that is never read
	code := `
res = withLocal(x)
    tmp = x + 1
    res = x * 2
`
	cp := parser.NewCodeParser(lexer.New("withLocal.pt", code))
	codeAST := cp.Parse()
	require.Empty(t, cp.Errors())

	cc := NewCodeCompiler(ctx, "withLocal", codeAST)
	errs := cc.Compile()

	// We expect exactly one dead-store error on "tmp"
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Msg, `value assigned to "tmp" is never used`)
}

func TestValidateFuncInputNotUsed(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	// define a function with one input “x” that is never read
	code := `
res = noUse(x)
    res = 42
`
	cp := parser.NewCodeParser(lexer.New("noUse.pt", code))
	codeAST := cp.Parse()
	require.Empty(t, cp.Errors())

	cc := NewCodeCompiler(ctx, "noUse", codeAST)
	errs := cc.Compile()

	// we expect exactly one error about the unused input parameter "x"
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Msg, `input parameter "x" is never read`)
}

func TestValidateFuncOutputNotWritten(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	// define a function with one output “res” but never assign to it
	code := `
res = neverWrite(x)
    x
    # (no body)
`
	cp := parser.NewCodeParser(lexer.New("neverWrite.pt", code))
	codeAST := cp.Parse()
	require.Empty(t, cp.Errors())

	cc := NewCodeCompiler(ctx, "neverWrite", codeAST)
	errs := cc.Compile()

	// we expect exactly one error about the output parameter “res” never being assigned
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Msg, `output parameter "res" is never assigned`)
}

func TestValidateFuncEdgeCases(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	tests := getFuncEdgeCaseTests()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runFuncEdgeCaseTest(t, ctx, tc)
		})
	}
}

func getFuncEdgeCaseTests() []funcEdgeCaseTest {
	return []funcEdgeCaseTest{
		{
			name: "WriteToInputParam",
			code: `
res = badWrite(x)
    x = 5
    res = x * 2
`,
			wantMsgs: []string{
				`cannot write to input parameter "x"`,
			},
		},
		{
			name: "ReadOutputBeforeWrite",
			code: `
res = readFirst(x)
    tmp = res + 1
    res = x * 2
`,
			wantMsgs: []string{
				`variable "res" has not been defined`, // or your specific "use before definition" text
			},
		},
		{
			name: "PartialOutputs",
			code: `
a, b = onlyA(x)
    a = x * 2
    # b is never written
`,
			wantMsgs: []string{
				`output parameter "b" is never assigned`,
			},
		},
		{
			name: "CombinedInputOutputErrors",
			code: `
a, b = bothBad(x)
    # neither input a is used nor output b is written
    a = 10
`,
			wantMsgs: []string{
				`input parameter "x" is never read`,
				`output parameter "b" is never assigned`,
			},
		},
	}
}

type funcEdgeCaseTest struct {
	name     string
	code     string
	wantMsgs []string
}

func runFuncEdgeCaseTest(t *testing.T, ctx llvm.Context, tc funcEdgeCaseTest) {
	// parse
	cp := parser.NewCodeParser(lexer.New(tc.name+".pt", tc.code))
	codeAST := cp.Parse()
	require.Empty(t, cp.Errors(), "parser errors in %s", tc.name)

	// compile & validate
	cc := NewCodeCompiler(ctx, tc.name, codeAST)
	errs := cc.Compile()

	// Verify expected error messages
	assertContainsExpectedMessages(t, errs, tc.wantMsgs)
}

func assertContainsExpectedMessages(t *testing.T, errs []*token.CompileError, expectedMsgs []string) {
	got := extractErrorMessages(errs)
	
	for _, want := range expectedMsgs {
		assertMessageFound(t, got, want)
	}
}

func extractErrorMessages(errs []*token.CompileError) []string {
	got := make([]string, len(errs))
	for i, e := range errs {
		got[i] = e.Msg
	}
	return got
}

func assertMessageFound(t *testing.T, messages []string, expectedMessage string) {
	found := false
	for _, m := range messages {
		if strings.Contains(m, expectedMessage) {
			found = true
			break
		}
	}
	assert.True(t, found, "expected an error containing %q, got: %v", expectedMessage, messages)
}
