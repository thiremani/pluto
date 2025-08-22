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
	testCases := []struct {
		name          string
		input         string
		code          string
		expectError   bool
		errorContains string
	}{
		// --- Valid Cases ---
		{
			name:        "Correct Simple Program",
			input:       "x = 1\ny = x + 1\ny",
			expectError: false,
		},
		{
			name:        "Allowed Write then ConditionalWrite",
			input:       "x = 100\nx = 1 > 2 99\nx",
			expectError: false,
		},
		{
			name:        "Allowed ConditionalWrite then ConditionalWrite",
			input:       "x = 1 > 3 1\nx = 2 > 1 2\nx",
			expectError: false,
		},
		{
			name:        "Read after ConditionalWrite",
			input:       "x = 5 > 2 1\ny = x + 1\ny",
			expectError: false,
		},
		{
			name:        "Write Read Write",
			input:       "x = 1\nx\nx = 2\nx",
			expectError: false,
		},
		{
			name:        "PrintOnly",
			input:       `"hello"`,
			expectError: false,
		},
		{
			name:        "EmptyProgram",
			input:       ``,
			expectError: false,
		},
		{
			name: "FormatMarker After Def",
			input: `x = 42
"Answer: -x"`, // x defined before marker
			expectError: false,
		},
		{
			name:        "Var Not Defined",
			input:       `"Value: -x%s"`,
			expectError: false,
		},
		// --- Error Cases ---
		{
			name:          "Use Before Definition",
			input:         "x = y + 1",
			expectError:   true,
			errorContains: `variable "y" has not been defined`,
		},
		{
			name:          "Use cond Before definition",
			input:         "a = b > 2 1",
			expectError:   true,
			errorContains: `variable "b" has not been defined`,
		},
		{
			name:          "Unconditional Write After Unconditional Write",
			input:         "x = 1\nx = 2\nx",
			expectError:   true,
			errorContains: `unconditional assignment to "x" overwrites a previous value that was never used. It was previously written at line 1:1`,
		},
		{
			name:          "Simple Dead Store (Unused Variable)",
			input:         "x = 1",
			expectError:   true,
			errorContains: `value assigned to "x" is never used`,
		},
		{
			name:          "Complex Dead Store",
			input:         "a = 1\nb = 2\nb",
			expectError:   true,
			errorContains: `value assigned to "a" is never used`,
		},
		{
			name:          "Conditional Write then Unconditional Write",
			input:         "x = 1 > 0 10\nx = 20\nx",
			expectError:   true,
			errorContains: `value assigned to "x" in conditional statement is never used`,
		},
		{
			name:          "Conditional Write then Unconditional Write (Dead Store)",
			input:         "a = 1\nx = a > 0 10\nx = 20",
			expectError:   true,
			errorContains: `value assigned to "x" is never used`,
		},
		{
			name:          "Read after write but still a dead store later",
			input:         "a = 1\nb = a\na = 2\nb", // The write 'a = 2' is a dead store
			expectError:   true,
			errorContains: `value assigned to "a" is never used`,
		},
		{
			name:          "Multi-variable Dead Store",
			input:         "a=1\nb=2\nc=3\na, b",
			expectError:   true,
			errorContains: `value assigned to "c" is never used`,
		},
		{
			name:          "Print Use Before Def",
			input:         `"x is", x`,
			expectError:   true,
			errorContains: `variable "x" has not been defined`,
		},
		{
			name: "Write To Constant",
			code: `a = 4`,
			input: `
x = a
a = 2`, // redeclaring/writing to const 'a'
			expectError:   true,
			errorContains: `cannot write to constant "a"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			prog := parseInput(t, tc.name, tc.input)
			cp := parser.NewCodeParser(lexer.New(tc.name, tc.code))
			ctx := llvm.NewContext()
			cc := NewCodeCompiler(ctx, "TestCFGAnalysis", cp.Parse())
			cc.Compile()
			cfg := NewCFG(nil, cc)
			cfg.Analyze(prog.Statements)

			if tc.expectError {
				// Assert that we have at least one error.
				assert.NotEmpty(t, cfg.Errors, "Expected an error, but got none.")

				// Optional: Check if we have *exactly* one error if that's expected.
				// assert.Len(t, cfg.Errors, 1)

				// Assert that the error message contains the expected substring.
				if len(cfg.Errors) > 0 {
					assert.Contains(t, cfg.Errors[0].Msg, tc.errorContains, "Error message mismatch")
				}
			} else {
				// Assert that the Errors slice is empty. This is much cleaner.
				assert.Empty(t, cfg.Errors, "Expected no errors, but got some.")
			}
		})
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

	tests := []struct {
		name     string
		code     string
		wantMsgs []string
	}{
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
				`variable "res" has not been defined`, // or your specific “use before definition” text
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

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// parse
			cp := parser.NewCodeParser(lexer.New(tc.name+".pt", tc.code))
			codeAST := cp.Parse()
			require.Empty(t, cp.Errors(), "parser errors in %s", tc.name)

			// compile & validate
			cc := NewCodeCompiler(ctx, tc.name, codeAST)
			errs := cc.Compile()

			// collect just the messages for easier comparison
			got := make([]string, len(errs))
			for i, e := range errs {
				got[i] = e.Msg
			}

			// each expected substring must appear
			for _, want := range tc.wantMsgs {
				assert.Condition(t, func() bool {
					for _, m := range got {
						if strings.Contains(m, want) {
							return true
						}
					}
					return false
				}, "expected an error containing %q, got: %v", want, got)
			}
		})
	}
}
