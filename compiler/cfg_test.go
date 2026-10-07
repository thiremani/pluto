package compiler

import (
	"fmt"
	"maps"
	"slices"
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

// Flow checks run once per template, from its text, whether or not anything
// calls it.
func TestFunctionDataflowRunsOncePerTemplate(t *testing.T) {
	code := `r = Unreachable(x)
    temporary = x + 1
    temporary = x + 2
    r = x`

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "unreachableLocalDeadStore", "", mustParseCode(t, code))
	errs := cc.Compile()
	require.Len(t, errs, 3)
	assertHasExpectedError(t, errs, `unconditional assignment to "temporary" overwrites a previous value that was never used`)

	deadStores := 0
	for _, err := range errs {
		if strings.Contains(err.Msg, `value assigned to "temporary" is never used`) {
			deadStores++
		}
	}
	require.Equal(t, 2, deadStores)
}

// Every specialization must pass liveness with its inputs and outputs
// treated as unshared; sharing at a call cannot make an otherwise rejected
// body acceptable. A body that builds on its own write names the output.
func TestOutputWriteLivenessIgnoresSharing(t *testing.T) {
	tests := []cfgTestCase{
		{
			name: "Repeated Output Write Shared",
			code: `out = BumpTwice(current, item)
    out = current + item
    out = current + item`,
			input:         "value = 10\nvalue = BumpTwice(value, 5)\nvalue",
			errorContains: `unconditional assignment to "out" overwrites a previous value that was never used`,
		},
		{
			name: "Repeated Output Write Unshared",
			code: `out = BumpTwice(current, item)
    out = current + item
    out = current + item`,
			input:         "value = 10\nother = BumpTwice(value, 5)\nother",
			errorContains: `unconditional assignment to "out" overwrites a previous value that was never used`,
		},
		{
			name: "Repeated Output Write Through Wrapper",
			code: `out = BumpTwice(current, item)
    out = current + item
    out = current + item

out = Bump(current, item)
    out = BumpTwice(current, item)`,
			input:         "value = 10\nvalue = Bump(value, 5)\nvalue",
			errorContains: `unconditional assignment to "out" overwrites a previous value that was never used`,
		},
		{
			name: "Incompatible Input Output Storage",
			code: `out = Replaced(current)
    out = "first"
    current
    out = "second"`,
			input:         "value = Replaced(1)\nvalue",
			errorContains: `unconditional assignment to "out" overwrites a previous value that was never used`,
		},
		{
			// The second write reads the first through the output name, so
			// the body is valid for every call shape.
			name: "Second Write Reads Output",
			code: `out = BumpTwice(current, item)
    out = current + item
    out = out + item`,
			input: "value = 10\nvalue = BumpTwice(value, 5)\nother = BumpTwice(value, 5)\nvalue, other",
		},
		{
			name: "Second Write Reads Output And Input",
			code: `out = BumpTwice(current, item)
    out = current + item
    out = current + out`,
			input: "value = 10\nvalue = BumpTwice(10, value)\nvalue",
		},
		{
			name: "Output Read Between Writes Through Nested Call",
			code: `out, seen = Reset(current)
    out = current
    seen = out
    out = "second"

out, seen = Wrap(current)
    out, seen = Reset(current)`,
			input: `value = "hello" ⊕ "!"
value, seen = Wrap(value)
value, seen`,
		},
		{
			name: "Marker Read Of Output Between Writes",
			code: `out = Show(current)
    out = current
    "-out"
    out = 2`,
			input: "x = 5\nx = Show(x)\nx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runCFGTest(t, tt, tt.errorContains != "")
		})
	}
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
			// An output is readable once definitely assigned, as a value, a
			// condition, a call argument, a print, or a marker.
			name: "Output Read After Definite Write",
			code: `res = overwrite(x)
    res = x
    res = res + 1`,
			input: "x = overwrite(3)\nx",
		},
		{
			name: "Output Read In Condition",
			code: `res = gated(x)
    res = x
    res = res > 5 x * x`,
			input: "x = gated(3)\nx",
		},
		{
			name: "Output Read As Call Argument",
			code: `res = id(x)
    res = x

res = forwarded(x)
    res = x
    res = id(res)`,
			input: "x = forwarded(3)\nx",
		},
		{
			name: "Output Read By Print",
			code: `res = printed(x)
    res = x
    res`,
			input: "x = printed(3)\nx",
		},
		{
			name: "Output Read By Format Marker After Assignment",
			code: `res = marked(x)
    res = x
    "value -res"`,
			input: "x = marked(3)\nx",
		},
		{
			name: "Output Read By Dynamic Width",
			code: `res = widened(x)
    res = x
    "-x%(-res)d"`,
			input: "x = widened(3)\nx",
		},
		{
			name: "Output Feeds Sibling Output",
			code: `sq, cube = powers(x)
    sq = x * x
    cube = sq * x`,
			input: "p, q = powers(3)\np, q",
		},
		{
			// The repair the diagnostic names: the previous value arrives as
			// an input and initializes the output, for either call shape.
			name: "Output Initialized From Input Before Conditional Write",
			code: `res = maybeIncrement(current, x)
    res = current
    res = x > 0 x
    res = res + 1`,
			input: "a = 5\na = maybeIncrement(a, -1)\nb = maybeIncrement(5, 3)\na, b",
		},
		{
			// A later conditional write does not undo the assignment.
			name: "Output Read After Later Conditional Write",
			code: `res = refined(x)
    res = x
    res = x > 5 x * x
    res = res + 1`,
			input: "x = refined(3)\nx",
		},
		{
			// Once assigned, the simultaneous form reads the previous value.
			name: "Simultaneous Output Read After Assignment",
			code: `sq, cube = powers(x)
    sq = 1
    sq, cube = x * x, sq * x`,
			input: "a, b = powers(3)\na, b",
		},
		{
			// Empty data with an established element type is readable.
			name: "Concrete Empty Array Output Read",
			code: `out, n = shrink(x)
    out = []0
    n = out
    out = [x]`,
			input: "a, b = shrink(1)\na, b",
		},
		{
			// A sample reads its output, so a definite assignment comes first.
			name: "Output Named As Sample After Definite Assignment",
			code: `out, xs = sampled(n)
    out = n
    xs = []out`,
			input: "a, b = sampled(1)\na, b",
		},
		{
			// A read string output is solved as owned, so the local copied
			// from it and the call it feeds use heap storage.
			name: "Output Read Through Local Into Call",
			code: `seen = Identity(current)
    seen = current

out, kept, echo = ReadTwice(current)
    out = "first"
    saved = out
    kept = Identity(saved)
    out = "second"
    echo = current`,
			input: `value = "hello" ⊕ "!"
value, kept, echo = ReadTwice(value)
value, kept, echo`,
		},
		{
			// A parameter is in scope for a marker's specifier inside a body.
			name: "Parameter In Marker Specifier",
			code: `out = Pad(value, width)
    local = value
    out = value
    "-local%(-width)d"`,
			input: "y = Pad(7, 4)\ny",
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
			// The callee keeps the old value through an input, so the call
			// reads the earlier write.
			name: "Write then Call Keeping Old Value",
			code: `res = maybeWrite(prev, x)
    res = prev
    res = x > 0 42`,
			input: "x = 7\nx = maybeWrite(x, -1)\nx",
		},
		{
			// One body serves scalar and array arguments: the comparison counts
			// as possibly skipping for both, so the default stays live.
			name: "Default Before Comparison For Scalar And Array",
			code: `out = Pos(prev, x)
    out = prev
    out = x > 0`,
			input: "s = Pos(9, -1)\nm = Pos([]0, [1 -2 3])\ns, m",
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
		{
			// Writing an output twice never reads it, and a call may target it.
			name: "Output Rewritten And Targeted By Nested Call",
			code: `res = maybe(prev, x)
    res = prev
    res = x > 0 x

res = refine(x)
    res = x
    res = x > 5 x * x
    res = maybe(res, x)`,
			input: "x = refine(3)\nx",
		},
		{
			// Intermediate values live in locals; the caller may still reuse a
			// variable as both argument and destination.
			name: "Local Accumulator Feeds Output",
			code: `res = accumulate(a, x)
    total = a + x
    total = total * 2
    res = total`,
			input: "x = 7\nx = accumulate(x, 3)\nx",
		},
	}
}

func getErrorTestCases() []cfgTestCase {
	return []cfgTestCase{
		{
			name:          "Use Before Definition",
			input:         "x = y + 1",
			errorContains: `undefined identifier: y`,
		},
		{
			name:          "Use cond Before definition",
			input:         "a = b > 2 1",
			errorContains: `undefined identifier: b`,
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
			// The direct callee writes on every iteration of this proven-nonempty
			// domain, so it does not read the destination seed and overwrites it.
			name: "Proven Nonempty MustWrite Call Overwrites Prior Seed",
			code: `res = alwaysWrite(x)
    res = x * 2`,
			input:         "x = 7\nx = alwaysWrite(0:2)\nx",
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
			errorContains: `undefined identifier: x`,
		},
		{
			// A body that would compute with its incoming value is rejected at
			// the read, not silently resolved at the caller.
			name: "Output Read After Conditional Write",
			code: `res = maybeIncrement(x)
    res = x > 0 x
    res = res + 1`,
			input:         "x = maybeIncrement(-1)\nx",
			errorContains: `output "res" is read where it may still be unassigned`,
		},
		{
			// A body that can skip its write is rejected at its definition, so
			// a caller can count on the call writing its output.
			name: "Skippable Body Rejected At Definition",
			code: `res = maybe(x)
    res = x > 0 x

res = chained(x)
    res = maybe(x)
    res = res + 1`,
			input:         "x = chained(-1)\nx",
			errorContains: `output "res" may be left unassigned`,
		},
		{
			// A marker naming an output is a read even before any assignment,
			// where it would otherwise pass as literal text.
			name: "Output Read By Format Marker",
			code: `res = marked(x)
    "seed -res"
    res = x`,
			input:         "x = marked(3)\nx",
			errorContains: `output "res" is read before it is assigned`,
		},
		{
			// A sample names its output for the type alone, but it is still a
			// read, so a conditional assignment before it is not enough.
			name: "Output Named As Sample Before Definite Assignment",
			code: `out, xs = sampled(n)
    out = n > 0 1
    xs = []out`,
			input:         "a, b = sampled(1)\na, b",
			errorContains: `output "out" is read where it may still be unassigned`,
		},
		{
			// Reads in a simultaneous assignment precede its writes.
			name: "Simultaneous Output Read Before Assignment",
			code: `sq, cube = powers(x)
    sq, cube = x * x, sq * x`,
			input:         "a, b = powers(3)\na, b",
			errorContains: `output "sq" is read before it is assigned`,
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
	t.Helper()

	prog := parseInput(t, tc.name, tc.input)
	cp := parser.NewCodeParser(lexer.New(tc.name, tc.code))
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	code := cp.Parse()
	require.Empty(t, cp.Errors())

	cc := NewCodeCompiler(ctx, "TestCFGAnalysis", "", code)
	errors := cc.Compile()
	if len(errors) == 0 {
		sc := NewScriptCompiler(ctx, tc.name, prog, cc)
		errors = sc.Compile()
	}

	if expectError {
		assertHasExpectedError(t, errors, tc.errorContains)
	} else {
		assert.Empty(t, errors, "Expected no errors, but got some.")
	}
}

func assertHasExpectedError(t *testing.T, errors []*token.CompileError, expectedMessage string) {
	assert.NotEmpty(t, errors, "Expected an error, but got none.")

	for _, err := range errors {
		if strings.Contains(err.Msg, expectedMessage) {
			return
		}
	}

	assert.Fail(t, "Error message mismatch", "expected an error containing %q, got %v", expectedMessage, extractErrorMessages(errors))
}

func compileScriptForCFGTest(t *testing.T, name, input string) []*token.CompileError {
	t.Helper()

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, name, "", ast.NewCode())
	program := parseInput(t, name, input)
	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	return sc.Compile()
}

func TestScriptCFGAccumulatesAllErrors(t *testing.T) {
	errs := compileScriptForCFGTest(t, t.Name(), `value = 5.
unused = 1
"Value: -value%(-width).(-precision)f"`)

	require.Len(t, errs, 3)
	assertContainsExpectedMessages(t, errs, []string{
		"Undefined variable width within specifier",
		"Undefined variable precision within specifier",
		`value assigned to "unused" is never used`,
	})
}

func TestMissingSpecifiersReportPositions(t *testing.T) {
	errs := compileScriptForCFGTest(t, t.Name(), `value = 5.
"Value: -value%(-missing).(-missing)f"`)

	require.Len(t, errs, 2)
	for _, err := range errs {
		require.Contains(t, err.Msg, "Undefined variable missing within specifier")
		require.Equal(t, 2, err.Token.Line)
	}
	require.Equal(t, 18, errs[0].Token.Column, "width reference position")
	require.Equal(t, 29, errs[1].Token.Column, "precision reference position")
}

// Marker positions resume from a per-string cursor, so read collection over a
// marker-heavy literal must stay linear in the literal's length.
func BenchmarkCollectStringReadsManyMarkers(b *testing.B) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cp := parser.NewCodeParser(lexer.New(b.Name(), ""))
	cc := NewCodeCompiler(ctx, b.Name(), "", cp.Parse())
	require.Empty(b, cc.Compile())

	cfg := NewCFG(cc)
	Put(cfg.Scopes, "x", struct{}{})
	value := strings.Repeat("-x ", 10000)
	tok := token.Token{FileName: b.Name(), Line: 1, Column: 1}

	b.ResetTimer()
	for range b.N {
		cfg.collectStringReads(value, tok)
	}
}

func TestSpecifierPositionSpansLogicalLines(t *testing.T) {
	errs := compileScriptForCFGTest(t, t.Name(), "value = 5.\n\"head\r\nmid\n-value%(-missing)d\"")

	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Msg, "Undefined variable missing within specifier")
	require.Equal(t, 4, errs[0].Token.Line, "CRLF and LF breaks in the literal each advance one line")
	require.Equal(t, 10, errs[0].Token.Column)
}

// A collector materializes an array even over an empty domain, so its write is
// unconditional and the store behind it is dead. Range classification needs the
// solver, so this runs the full script pipeline.
func TestCollectorWriteIsUnconditional(t *testing.T) {
	errs := compileScriptForCFGTest(t, "collectorWrite", "i = 0:0\nc = [9]\nc = [i + 0]\nc")
	require.NotEmpty(t, errs, "the dead store behind the collector must be reported")
	assert.Contains(t, errs[0].Msg, `unconditional assignment to "c"`)
}

// Typed effects distinguish a scalar condition from an array mask. With
// infallible operands this mask materializes unconditionally.
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

func TestStructuralOutputAssignmentAccepted(t *testing.T) {
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
	assert.Empty(t, errs)
}

func TestValidateFuncFreshSelfReadIsUndefined(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	code := `
res = freshSelfRead(x)
    local = local + x
    res = x * 2
`
	cp := parser.NewCodeParser(lexer.New("freshSelfRead.pt", code))
	codeAST := cp.Parse()
	require.Empty(t, cp.Errors())

	cc := NewCodeCompiler(ctx, "freshSelfRead", "", codeAST)
	errs := cc.Compile()

	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Msg, `variable "local" has not been defined`)
}

func TestTemplateBindingsDoNotLeakIntoTypedPass(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, t.Name(), "", ast.NewCode())
	program := mustParseScript(t, "value = 1")
	stmt := program.Statements[0].(*ast.LetStatement)
	effects := map[*ast.LetStatement]StatementEffect{
		stmt: {
			Writes:    []TargetWriteEffect{{TargetIndex: 0, Effect: MustWrite}},
			ReadsSeed: []int{0},
		},
	}
	cfg := NewCFG(cc)

	require.PanicsWithValue(t, `internal: CFG seed read targets undefined binding "value" in statement "value = 1"`, func() {
		cfg.AnalyzeScript(program.Statements, effects)
	})
}

func TestValidateFuncExistingSelfReadAndSwap(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	code := `
res = existingSelfReadAndSwap(x, y)
    left = x
    right = y
    left = left + 1
    left, right = right, left
    res = left + right
`
	cp := parser.NewCodeParser(lexer.New("existingSelfReadAndSwap.pt", code))
	codeAST := cp.Parse()
	require.Empty(t, cp.Errors())

	cc := NewCodeCompiler(ctx, "existingSelfReadAndSwap", "", codeAST)
	require.Empty(t, cc.Compile())
}

func TestDiscardCreatesNoBindingOrError(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	code := `
res = discardBinding(x)
    _ = x
    res = x + 1
`
	cp := parser.NewCodeParser(lexer.New("discardBinding.pt", code))
	codeAST := cp.Parse()
	require.Empty(t, cp.Errors())

	cc := NewCodeCompiler(ctx, "discardBinding", "", codeAST)
	require.Empty(t, cc.Compile())

	template := codeAST.Statements[0].(*ast.FuncStatement)
	discard := template.Body.Statements[0].(*ast.LetStatement)
	cfg := NewCFG(cc)
	cfg.declareTargets(discard.Name)
	_, exists := Get(cfg.Scopes, "_")
	assert.False(t, exists)
}

func TestFormattingReadsRemainStructural(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantError string
	}{
		{
			name: "unknown marker is literal",
			body: `res = unknownMarker(x)
    "-missing%#-"
    res = x`,
		},
		{
			name: "malformed resolved specifier",
			body: `res = malformedSpecifier(x)
    "-x%#-"
    res = x`,
			wantError: "Invalid format specifier string",
		},
		{
			name: "undefined dynamic width",
			body: `res = undefinedWidth(x)
    "-x%(-width)d"
    res = x`,
			wantError: "Undefined variable width within specifier",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := llvm.NewContext()
			defer ctx.Dispose()

			cc := NewCodeCompiler(ctx, test.name, "", mustParseCode(t, test.body))
			errs := cc.Compile()
			if test.wantError == "" {
				require.Empty(t, errs)
				return
			}

			assertHasExpectedError(t, errs, test.wantError)
		})
	}
}

func TestTemplatePrintReadKeepsLocalLive(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "printedTemplate", "", mustParseCode(t, `res = Printed(x)
    local = x + 1
    local
    res = x`))
	require.Empty(t, cc.Compile())
}

func TestTypedEventsUseSparseTargetIndices(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	code := mustParseCode(t, `a, b = Sparse(x)
    a, _, b = x, x, x`)
	template := code.Statements[0].(*ast.FuncStatement)
	statement := template.Body.Statements[0].(*ast.LetStatement)
	effects := map[*ast.LetStatement]StatementEffect{
		statement: {
			Writes: []TargetWriteEffect{
				{TargetIndex: 0, Effect: MustWrite},
				{TargetIndex: 2, Effect: MustWrite},
			},
		},
	}
	cc := NewCodeCompiler(ctx, "sparseSpecialization", "", code)
	cfg := NewCFG(cc)

	events := cfg.typedStatementEvents(statement, nil, effects)

	require.Equal(t, []VarEvent{
		{Name: "a", Kind: Write, Token: statement.Name[0].Tok()},
		{Name: "b", Kind: Write, Token: statement.Name[2].Tok()},
	}, events)
}

func TestCFGRejectsMissingStatementEffects(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, t.Name(), "", ast.NewCode())
	program := mustParseScript(t, "value = 1\nvalue")
	cfg := NewCFG(cc)

	require.PanicsWithValue(t, `internal: missing CFG effects for statement "value = 1"`, func() {
		cfg.AnalyzeScript(program.Statements, make(map[*ast.LetStatement]StatementEffect))
	})
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
				`output "res" is read before it is assigned`,
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

func unassignedOutputMessage(name string) string {
	return fmt.Sprintf("output %q may be left unassigned; assign it unconditionally first, or pass the previous value as an input and initialize from it (%s = prev), with each caller passing its destination as that input", name, name)
}

// Every body that runs writes every output. The template check reads the
// text: a statement gate, a value that can fail, or a range that may be empty
// can skip a write, for every argument type.
func TestOutputsMustBeDefinitelyAssigned(t *testing.T) {
	tests := []struct {
		name       string
		code       string
		unassigned []string
	}{
		{
			name: "Gated",
			code: `out = Maybe(x)
    out = x > 0 x`,
			unassigned: []string{"out"},
		},
		{
			// A comparison counts as possibly skipping even where an array
			// argument would make it a mask.
			name: "Comparison",
			code: `out = Less(x, y)
    out = x < y`,
			unassigned: []string{"out"},
		},
		{
			name: "CheckedAccess",
			code: `out = At(arr, i)
    out = arr[i]`,
			unassigned: []string{"out"},
		},
		{
			// Complementary conditions are not recognized: for floats, a NaN
			// fails both.
			name: "ComplementaryGates",
			code: `y = Fib(n)
    y = n <= 1 n
    y = n > 1 Fib(n - 1) + Fib(n - 2)`,
			unassigned: []string{"y"},
		},
		{
			name: "DefaultThenOverride",
			code: `y = Fib(n)
    y = n
    y = n > 1 Fib(n - 1) + Fib(n - 2)`,
		},
		{
			name: "PrevThenComparison",
			code: `out = Pos(prev, x)
    out = prev
    out = x > 0`,
		},
		{
			name: "LocalAbsorbsCheckedAccess",
			code: `out = At(arr, i)
    t = arr[i]
    out = t`,
		},
		{
			// A caller counts a call as writing every output, so only the
			// body that can skip is reported.
			name: "CallerIsNotReportedAgain",
			code: `res = maybe(x)
    res = x > 0 x

out = chained(x)
    out = maybe(x)
    out = out + 1`,
			unassigned: []string{"res"},
		},
		{
			name: "EveryOutputIsChecked",
			code: `x, y = IsEven(n)
    x, y = n != 0 IsOdd(n - 1)
    x = n == 0 "yes"

x, y = IsOdd(n)
    x, y = "no", "yes"
    x, y = n != 0 IsEven(n - 1)`,
			unassigned: []string{"x", "y"},
		},
		{
			name: "LocalRangeMayBeEmpty",
			code: `out = Tail(n)
    j = 0:n
    out = j * 2`,
			unassigned: []string{"out"},
		},
		{
			name: "InlineRangeMayBeEmpty",
			code: `out = Shift(n)
    out = n + (0:n)`,
			unassigned: []string{"out"},
		},
		{
			name: "NonemptyRangeLiteralAlwaysRuns",
			code: `out = Last(x)
    out = x + (0:3)`,
		},
		{
			name: "LocalRangeWithDefault",
			code: `out = F(prev, n)
    r = 0:n
    out = prev
    out = r * 2`,
		},
		{
			name: "ScalarCallOutput",
			code: `y = Helper(x)
    y = x + 1

out = F(x)
    y = Helper(x)
    out = y + 1`,
		},
		{
			// A gate that names a range iterates it, so the value reads the
			// element and the binding holds a scalar.
			name: "GateIteratesItsRange",
			code: `out = F(n)
    stream = 0:n
    last = stream > 2 stream
    out = last + 1`,
		},
		{
			name: "GateOnAnotherRangeKeepsDescriptor",
			code: `out = F(n)
    other = 0:3
    stream = 0:n
    kept = other > 1 stream
    out = kept * 2`,
			unassigned: []string{"out"},
		},
		{
			// A literal or binding under && is iterated.
			name: "AndIteratesLiteral",
			code: `out = F(n)
    last = n > 0 && 0:5
    out = last + 1`,
		},
		{
			name: "AndIteratesBinding",
			code: `out = F(n)
    stream = 0:n
    last = n > 0 && stream
    out = last + 1`,
		},
		{
			// A width or precision consumes a range as numbers.
			name: "SpecifierRangeIterates",
			code: `out = F(n)
    w = 1:n
    out = "-n%(-w)d"`,
			unassigned: []string{"out"},
		},
		{
			// A && fills its right operand's targets.
			name: "AndTakesRightOperandSlots",
			code: `a, b = Pair(x)
    a, b = x, x

a, b, c = F(x)
    a, b, c = x > 0 && Pair(x), x`,
			unassigned: []string{"a", "b"},
		},
		{
			// A call's arity counts every value its arguments yield, so the
			// targets line up with Count's outputs and only c can be skipped.
			name: "TargetsFollowCallOutputs",
			code: `left, right = Tags(n)
    left, right = n, n + 1

out, tag = Count(left, right, n)
    out = n + 1
    tag = left + right

a, b, c = F(arr, n)
    a, b, c = Count(Tags(n), n), arr[0]`,
			unassigned: []string{"c"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := llvm.NewContext()
			defer ctx.Dispose()

			cc := NewCodeCompiler(ctx, test.name, "", mustParseCode(t, test.code))
			want := make([]string, len(test.unassigned))
			for i, name := range test.unassigned {
				want[i] = unassignedOutputMessage(name)
			}

			require.Equal(t, want, extractErrorMessages(cc.Compile()))
		})
	}
}

func TestUnassignedOutputIsReportedAtTheHeader(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, t.Name(), "", mustParseCode(t, `out = Maybe(x)
    out = x > 0 x`))
	errs := cc.Compile()

	require.Len(t, errs, 1)
	require.Equal(t, unassignedOutputMessage("out"), errs[0].Msg)
	require.Equal(t, 1, errs[0].Token.Line)
	require.Equal(t, 1, errs[0].Token.Column)
}

// The template checks classify each write from the text, so they first report
// text the classification cannot read, which the solver rejects in any
// specialization, and check no flow over it.
func TestUnclassifiableTemplateTextIsReported(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		errors []string
	}{
		{
			// The value that can fail does not mark the targets as possibly
			// skipped.
			name: "ValueCountMismatch",
			code: `a, b = Pair(x)
    a, b = x, x

a, b, c = F(arr, x)
    a, b, c = Pair(x), arr[0], x`,
			errors: []string{"assignment mismatch: 3 targets but 4 values"},
		},
		{
			name: "OneValueForTwoTargets",
			code: `a, b = F(x)
    a, b = x`,
			errors: []string{"assignment mismatch: 2 targets but 1 value"},
		},
		{
			name: "UndefinedFunction",
			code: `y = F(x)
    y = Missing(x)`,
			errors: []string{"undefined function: Missing"},
		},
		{
			// A call names a template by the number of values its arguments
			// yield.
			name: "WrongArgumentCount",
			code: `a, b = Pair(x)
    a, b = x, x

y = F(x)
    y = Pair(x, x)`,
			errors: []string{"undefined function: Pair"},
		},
		{
			name: "UndefinedFunctionInConditionAndArgument",
			code: `y = F(x)
    y = x
    y = Check(x) > 0 Inc(Next(x))

out = Inc(n)
    out = n + 1`,
			errors: []string{"undefined function: Check", "undefined function: Next"},
		},
		{
			name: "UndefinedFunctionInPrint",
			code: `y = F(x)
    y = x
    Missing(x)`,
			errors: []string{"undefined function: Missing"},
		},
		{
			// The solver counts an unknown call as one value and reports both.
			name: "UndefinedFunctionInTuple",
			code: `a, b = F(x)
    a, b = Missing(x)`,
			errors: []string{"undefined function: Missing", "assignment mismatch: 2 targets but 1 value"},
		},
		{
			// An operator whose sides do not line up yields one value, as the
			// solver counts it, so the assignment does not line up either.
			name: "OperandCountMismatch",
			code: `p, q = Pair(n)
    p, q = n, n + 1

a, b = F(n)
    a, b = Pair(n) * 2`,
			errors: []string{`operand mismatch: "*" has 2 values on its left but 1 value on its right`, "assignment mismatch: 2 targets but 1 value"},
		},
		{
			name: "OrAlternativesCountMismatch",
			code: `p, q = Pair(n)
    p, q = n, n + 1

y = Inc(n)
    y = n + 1

a, b = F(n)
    a, b = Pair(n > 0) || Inc(3)`,
			errors: []string{`operand mismatch: "||" has 2 values on its left but 1 value on its right`, "assignment mismatch: 2 targets but 1 value"},
		},
		{
			name: "NestedMismatchCountsOneValue",
			code: `p, q = Pair(n)
    p, q = n, n + 1

y = F(n)
    y = (Pair(n) * 2) + 1`,
			errors: []string{`operand mismatch: "*" has 2 values on its left but 1 value on its right`},
		},
		{
			// A && condition folds onto one value or broadcasts to several,
			// but two conditions cannot gate three values.
			name: "AndArityMismatch",
			code: `p, q = Pair(n)
    p, q = n, n + 1

a, b, c = Three(n)
    a, b, c = n, n, n

a, b, c = F(n)
    a, b, c = Pair(n > 0) && Three(n)`,
			errors: []string{"logical AND condition arity must match the value's, fold to one, or broadcast from one — got 2 and 3", "assignment mismatch: 3 targets but 1 value"},
		},
		{
			name: "RangeReassignedNonRange",
			code: `y = G(x)
    r = 1:3
    s = [r]
    r = x
    y = r + s`,
			errors: []string{`cannot reassign "r" from a range to a non-range value`},
		},
		{
			name: "NonRangeReassignedRange",
			code: `y = G(x)
    r = x
    s = r + 1
    r = 1:3
    y = r + s`,
			errors: []string{`cannot reassign "r" from a non-range value to a range`},
		},
		{
			// As in the solver, the first assignment fixes the kind.
			name: "KindFollowsFirstAssignment",
			code: `y = G(n)
    r = 0:n
    r = n
    r = 1:n
    y = [r]`,
			errors: []string{`cannot reassign "r" from a range to a non-range value`},
		},
		{
			// A range the statement's condition iterates reads as an element.
			name: "GateIteratedRangeReassigned",
			code: `y = G(n)
    r = 0:n
    s = [r]
    r = r > 1 r
    y = s`,
			errors: []string{`cannot reassign "r" from a range to a non-range value`},
		},
		{
			// y's gated write waits until the template's text classifies.
			name: "NoFlowChecksOverUnclassifiableText",
			code: `y, z = F(x)
    y = x > 0 x
    z = Missing(x)`,
			errors: []string{"undefined function: Missing"},
		},
		{
			name: "OtherTemplatesStillChecked",
			code: `y = F(x)
    y = Missing(x)

out = Maybe(x)
    out = x > 0 x`,
			errors: []string{"undefined function: Missing", unassignedOutputMessage("out")},
		},
		{
			// The text is read even when the structure is invalid; the flow
			// is not.
			name: "ReportedWithStructuralErrors",
			code: `y = F(x, unused)
    y = Missing(x)`,
			errors: []string{`input parameter "unused" is never read`, "undefined function: Missing"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := llvm.NewContext()
			defer ctx.Dispose()

			cc := NewCodeCompiler(ctx, test.name, "", mustParseCode(t, test.code))
			require.Equal(t, test.errors, extractErrorMessages(cc.Compile()))
		})
	}
}

// Unclassifiable text is reported where the solver reports it: an assignment
// at its =, a call at its parenthesis, and a reassignment at its target.
func TestUnclassifiableTemplateTextPositions(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, t.Name(), "", mustParseCode(t, `a, b = F(x)
    a, b = Missing(x)

y = G(x)
    r = 1:3
    s = [r]
    r = x
    y = r + s`))
	errs := cc.Compile()

	require.Len(t, errs, 3)
	positions := make([][2]int, len(errs))
	for i, err := range errs {
		positions[i] = [2]int{err.Token.Line, err.Token.Column}
	}
	require.Equal(t, [][2]int{{2, 19}, {2, 10}, {7, 5}}, positions)
}

func rangeOutputMessage(name string) string {
	return fmt.Sprintf("output %q cannot hold a range; return its bounds and build the range where it is used", name)
}

// A function cannot return a range: it returns the bounds, and its caller
// builds the range.
func TestFunctionsCannotReturnRanges(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		errors []string
	}{
		{
			name: "RangeLiteralOutput",
			code: `r = MakeRange(n)
    r = 0:n`,
			errors: []string{rangeOutputMessage("r")},
		},
		{
			name: "OneOutputOfSeveral",
			code: `r, k = Two(n)
    r, k = 0:n, n`,
			errors: []string{rangeOutputMessage("r")},
		},
		{
			name: "GatedRangeOutput",
			code: `out = F(n)
    out = n > 0 0:n`,
			errors: []string{rangeOutputMessage("out")},
		},
		{
			name: "CopiedRangeOutput",
			code: `out = F(n)
    r = 0:n
    out = r`,
			errors: []string{rangeOutputMessage("out")},
		},
		{
			name: "IteratedRangeOutput",
			code: `out = F(prev, n)
    r = 0:n
    out = prev
    out = r * 2`,
		},
		{
			name: "CollectedRangeOutput",
			code: `out = F(n)
    out = [0:n]`,
		},
		{
			name: "BoundsOutputs",
			code: `lo, hi = Bounds(n)
    lo, hi = 0, n

out = F(n)
    lo, hi = Bounds(n)
    r = lo:hi
    out = [r]`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := llvm.NewContext()
			defer ctx.Dispose()

			cc := NewCodeCompiler(ctx, test.name, "", mustParseCode(t, test.code))
			want := test.errors
			if want == nil {
				want = []string{}
			}
			require.Equal(t, want, extractErrorMessages(cc.Compile()))
		})
	}
}

func TestRangeOutputIsReportedAtItsAssignment(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, t.Name(), "", mustParseCode(t, `r, k = Two(n)
    k = n
    r = 0:n`))
	errs := cc.Compile()

	require.Len(t, errs, 1)
	require.Equal(t, 3, errs[0].Token.Line)
	require.Equal(t, 5, errs[0].Token.Column)
}

// The template checks read from the text which bindings hold a Range, and
// settlement checks that the solved types agree, so each way a value holds
// or iterates a range is pinned here against the solver.
func TestTextRangeSummaryAgreesWithSolver(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		ranges []string
	}{
		{
			name: "DescriptorCopy",
			code: `out = F(n)
    stream = 0:n
    kept = stream
    out = [kept]`,
			ranges: []string{"kept", "stream"},
		},
		{
			name: "OperationIterates",
			code: `out = F(n)
    stream = 0:n
    out = n
    out = stream + 0`,
			ranges: []string{"stream"},
		},
		{
			name: "GateIteratesItsRange",
			code: `out = F(n)
    stream = 0:n
    out = n
    out = stream > 2 stream`,
			ranges: []string{"stream"},
		},
		{
			name: "GateOnAnotherRangeKeepsDescriptor",
			code: `out = F(n)
    other = 0:3
    stream = 0:n
    kept = other > 1 stream
    out = [kept]`,
			ranges: []string{"kept", "other", "stream"},
		},
		{
			name: "WholeLiteralUnderGateKeepsDescriptor",
			code: `out = F(n)
    stream = 0:n
    kept = stream > 1 0:9
    out = [kept]`,
			ranges: []string{"kept", "stream"},
		},
		{
			// A range inside a collector in the condition is collected, so
			// the gate does not iterate it.
			name: "CollectorInGateKeepsDescriptor",
			code: `n = Size(xs)
    t = xs[0]
    n = t + 2

out = F(n)
    stream = 0:n
    kept = Size([stream]) > 1 stream
    out = [kept]`,
			ranges: []string{"kept", "stream"},
		},
		{
			name: "SpecifierInGateIterates",
			code: `out = F(n)
    w = 0:n
    kept = "-n%(-w)d" > "" w
    out = [kept]`,
			ranges: []string{"w"},
		},
		{
			name: "AndIteratesLiteral",
			code: `out = F(n)
    last = n > 0 && 0:5
    out = [last]`,
		},
		{
			name: "AndIteratesBinding",
			code: `out = F(n)
    stream = 0:n
    last = n > 0 && stream
    out = [last]`,
			ranges: []string{"stream"},
		},
		{
			// A range in one value of a tuple holds only that target.
			name: "RangeInOneSlotOfATuple",
			code: `out = F(n)
    kept, k = 0:n, n
    out = [kept] ⊕ [k]`,
			ranges: []string{"kept"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := llvm.NewContext()
			defer ctx.Dispose()

			cc := NewCodeCompiler(ctx, test.name, "", mustParseCode(t, test.code))
			require.Empty(t, cc.Compile())

			ranges := slices.Sorted(maps.Keys(cc.rangeBindings[funcKey{name: "F", arity: 1}]))
			if test.ranges == nil {
				require.Empty(t, ranges)
			} else {
				require.Equal(t, test.ranges, ranges)
			}

			sc := NewScriptCompiler(ctx, t.Name(), mustParseScript(t, "v = F(5)\nv"), cc)
			require.Empty(t, sc.Compile())
		})
	}
}

func TestSettledRangeDisagreementIsInternalError(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, t.Name(), "", mustParseCode(t, `out = F(n)
    stream = 0:n
    out = [stream]`))
	require.Empty(t, cc.Compile())
	delete(cc.rangeBindings[funcKey{name: "F", arity: 1}], "stream")

	ts := NewTypeSolver(NewScriptCompiler(ctx, t.Name(), mustParseScript(t, "v = F(5)\nv"), cc))
	require.PanicsWithValue(t,
		fmt.Sprintf("internal: F solves %q as %s, which disagrees with its template's text range summary", "stream", Range{Iter: I64}),
		ts.Solve,
	)
}
