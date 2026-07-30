package compiler

import (
	"fmt"
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

func TestFunctionDiagnosticsFollowSourceOrder(t *testing.T) {
	code := `r = Hotel(x)
    th = x + 1
    r = x

r = Alpha(x)
    ta = x + 1
    r = x

r = Golf(x)
    tg = x + 1
    r = x

r = Bravo(x)
    tb = x + 1
    r = x

r = Foxtrot(x)
    tf = x + 1
    r = x

r = Charlie(x)
    tc = x + 1
    r = x

r = Echo(x)
    te = x + 1
    r = x

r = Delta(x)
    td = x + 1
    r = x`

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "deterministicDiagnostics", "", mustParseCode(t, code))
	errs := cc.Compile()
	require.Len(t, errs, 8)

	for i, name := range []string{"th", "ta", "tg", "tb", "tf", "tc", "te", "td"} {
		require.Contains(t, errs[i].Msg, fmt.Sprintf("%q", name))
	}
}

func TestFunctionDiagnosticsUseMergedMapDefinition(t *testing.T) {
	earlier := mustParseCode(t, `r = Duplicate(x)
    earlierUnused = x + 1
    r = x`)
	later := mustParseCode(t, `r = Duplicate(x)
    laterUnused = x + 1
    r = x`)
	code := ast.NewCode()
	code.Merge(earlier)
	code.Merge(later)

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "mergedFunctionDiagnostics", "", code)
	errs := cc.Compile()
	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Msg, `"laterUnused"`)
	require.NotContains(t, errs[0].Msg, `"earlierUnused"`)
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
			name: "Marker Following Unresolved Marker",
			input: `width = 5
"-missing%(-width)d"`,
		},
		{
			name:  "Var Not Defined",
			input: `"Value: -x%s"`,
		},
		{
			// The callee may skip its write, leaving the destination's previous
			// value in place, so that previous write is live.
			name: "Write then Skippable Call Root",
			code: `res = maybeWrite(x)
    res = x > 0 42`,
			input: "x = 7\nx = maybeWrite(-1)\nx",
		},
		{
			// A condition below the value root still leaves the whole RHS able
			// to yield nothing, so the earlier write stays live.
			name:  "Nested Condition Below Root",
			input: "x = 7\ny = 10\ny = (x < 5) + 5\ny",
		},
		{
			// An out-of-bounds read fails its lanes and preserves the target.
			name:  "Out Of Bounds Read Preserves Destination",
			input: "arr = [1]\ny = 10\ny = arr[9]\ny",
		},
		{
			// The failable expression suspends only its own destination, and
			// b is fresh, so nothing behind the unconditional sibling is dead.
			name:  "Failable Value Protects Only Its Own Destination",
			input: "x = 7\na = 10\na, b = x < 5, 30\na, b",
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
			// A call feeding an operator always contributes to a new value, so
			// the write stays unconditional and the earlier one is still dead.
			name: "Call Feeding Operator Stays Unconditional",
			code: `res = alwaysWrite(x)
    res = x * 2`,
			input:         "x = 7\nx = alwaysWrite(3) + 1\nx",
			errorContains: `unconditional assignment to "x" overwrites a previous value that was never used`,
		},
		{
			// A || yields whenever its final fallback does, so the resolver
			// boundary holds and this write is unconditional.
			name:          "Logical Or With Unconditional Fallback",
			input:         "x = 7\ny = 10\ny = (x < 5) || 99\ny",
			errorContains: `unconditional assignment to "y" overwrites a previous value that was never used`,
		},
		{
			// An array literal settles a failed cell locally, so the literal
			// always yields and the boundary holds.
			name:          "Array Literal Cell Stays Unconditional",
			input:         "x = 7\ny = [1]\ny = [x < 5]\ny",
			errorContains: `unconditional assignment to "y" overwrites a previous value that was never used`,
		},
		{
			// A failable sibling no longer suspends the whole statement, so
			// the dead store behind the unconditional literal is reported.
			name:          "Failable Sibling Does Not Protect Unconditional Write",
			input:         "x = 7\na = 10\nb = 20\na, b = x < 5, 30\na, b",
			errorContains: `unconditional assignment to "b" overwrites a previous value that was never used`,
		},
		{
			name:          "Print Use Before Def",
			input:         `"x is", x`,
			errorContains: `variable "x" has not been defined`,
		},
		{
			name: "Unresolved Dynamic Specifier",
			input: `x = 42
"Answer: -x%(-width)d"`,
			errorContains: "Undefined variable width within specifier",
		},
		{
			name: "Unresolved Dynamic Precision",
			input: `x = 5.
width = 2
"Value: -x%(-width).(-precision)f"`,
			errorContains: "Undefined variable precision within specifier",
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
	cc := NewCodeCompiler(ctx, "TestCFGAnalysis", "", cp.Parse())
	cc.Compile()
	cfg := NewCFG(nil, cc)
	cfg.PushBlock()
	defer cfg.PopBlock()
	PushScope(&cfg.Scopes, BlockScope)
	cfg.funcForwardPass(prog.Statements)
	cfg.backwardPass(make(map[string]struct{}))

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

func compileScriptForCFGTest(t *testing.T, name, input string) []*token.CompileError {
	t.Helper()

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, name, "", ast.NewCode())
	program := parseInput(t, name, input)
	sc := NewScriptCompiler(ctx, program, cc, make(map[string]*Func), cc.Compiler.ExprCache)
	return sc.Compile()
}

// A collector materializes an array even over an empty domain, so its write is
// unconditional and the store behind it is dead. Range classification needs the
// solver, so this runs the full script pipeline.
func TestCollectorWriteIsUnconditional(t *testing.T) {
	errs := compileScriptForCFGTest(t, "collectorWrite", "i = 0:0\nc = [9]\nc = [i + 0]\nc")
	require.NotEmpty(t, errs, "the dead store behind the collector must be reported")
	assert.Contains(t, errs[0].Msg, `unconditional assignment to "c"`)
}

// Typed comparison metadata distinguishes a scalar condition, which may not
// yield, from an array mask, which always materializes an array. The untyped
// function-template fallback intentionally cannot make that distinction.
func TestArrayComparisonWriteIsUnconditional(t *testing.T) {
	errs := compileScriptForCFGTest(t, "arrayComparisonWrite", "a = [1 2]\nr = [9 9]\nr = a > 0\nr")
	require.NotEmpty(t, errs, "the dead store behind the array mask must be reported")
	assert.Contains(t, errs[0].Msg, `unconditional assignment to "r"`)
}

// A ranged gate can admit no iterations, so collector and scalar destinations
// both preserve their prior values and both writes stay conditional.
func TestRangedGateCollectorWriteIsConditional(t *testing.T) {
	errs := compileScriptForCFGTest(t, "rangedGateCollector", "i = 0:1\nc = [9]\ns = 42\nc, s = i < 0 [i], i + 7\nc, s")
	require.Empty(t, errs)
}

func TestGateArrayWriteKinds(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "empty ranged gate preserves collector",
			input: "c = [9]\nc = 0:0 [1]\nc",
		},
		{
			name:  "ranged block preserves destination",
			input: "c = [\n    9\n]\ni = 0:1\nc = i < 0 [\n    1\n]\nc",
		},
		{
			name:  "scalar collector preserves destination",
			input: "flag = 0\nc = [9]\nc = flag > 0 [1]\nc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := compileScriptForCFGTest(t, tt.name, tt.input)
			require.Empty(t, errs)
		})
	}
}

// A ranged expression suspends its own destination only: the sibling literal
// writes even when the domain is empty, so the store behind it is dead. Range
// classification needs the solver, so this runs the full script pipeline
// rather than the bare-CFG harness.
func TestEmptyDomainDoesNotProtectSiblingWrite(t *testing.T) {
	errs := compileScriptForCFGTest(t, "emptyDomainSibling", "i = 0:0\na = 1\nb = 2\na, b = i + 0, 30\na, b")
	require.NotEmpty(t, errs, "the dead store behind the sibling literal must be reported")

	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Msg
	}
	joined := strings.Join(msgs, "\n")
	assert.Contains(t, joined, `unconditional assignment to "b"`)
	assert.NotContains(t, joined, `to "a"`, "the ranged destination must stay protected")
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

	cc := NewCodeCompiler(ctx, "onlyOut", "", codeAST)
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

	cc := NewCodeCompiler(ctx, "withLocal", "", codeAST)
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

	cc := NewCodeCompiler(ctx, "noUse", "", codeAST)
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

	cc := NewCodeCompiler(ctx, "neverWrite", "", codeAST)
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
	cc := NewCodeCompiler(ctx, tc.name, "", codeAST)
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
