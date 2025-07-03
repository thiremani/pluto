package compiler

import (
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
			cc := NewCodeCompiler(llvm.NewContext(), "TestCFGAnalysis", cp.Parse())
			cc.Compile()
			cfg := NewCFG(cc)
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
