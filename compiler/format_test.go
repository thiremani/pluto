package compiler

import (
	"strings"
	"testing"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/parser"

	"tinygo.org/x/go-llvm"
)

func TestFormatStringErrors(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectError string
	}{
		{
			name: "AsteiskNotAllowed",
			input: `x = 10
"Value: -x%*d"`,
			expectError: "TestFormatStringErrors:2:1:Using * not allowed in format specifier (after the % char). Instead use (-var) where var is an integer variable. Error str: Value: -x%*d",
		},
		{
			name: "MissingClosingParen",
			input: `x = 2
"Value: -x%(-var"`,
			expectError: "Expected ) after the identifier var",
		},
		{
			name: "InvalidFormatSpecifier",
			input: `x = 4
"x = -x%"`,
			expectError: "Invalid format specifier string: Format specifier is incomplete",
		},

		{
			name: "IdentifierWithinSpecifierNotFound",
			input: `x = 10
"Value: -x%(-var)d"`,
			expectError: "Undefined variable var within specifier. String Literal is Value: -x%(-var)d",
		},
		{
			name: "SpecifierDoesNotEnd",
			input: `x = 10
"Value: -x%#-"`,
			expectError: "Invalid format specifier string: Format specifier '%#-' is incomplete",
		},
		{
			name: "UnsupportedSpecifier",
			input: `x = 5
"Value: -x%q"`,
			expectError: "Invalid format specifier string: Format specifier '%q' is incomplete. Str: Value: -x%q",
		},
		{
			name: "IntWithFloatSpecifier",
			input: `x = 5
"x = -x%f"`,
			expectError: "Format specifier end 'f' is not correct for variable type. Variable identifier: x. Variable type: I64",
		},
		{
			name: "FloatWithIntSpecifier",
			input: `y = 3.14
"y = -y%d"`,
			expectError: "Format specifier end 'd' is not correct for variable type. Variable identifier: y. Variable type: F64",
		},
		{
			name: "PointerOnNonPointer",
			input: `x = 42
"Value: -x%p"`,
			expectError: "Format specifier end 'p' is not correct for variable type. Variable identifier: x. Variable type: I64",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := lexer.New("TestFormatStringErrors", tc.input)
			p := parser.New(l)
			program := p.ParseProgram()
			// Check for parser errors first, though these tests focus on compiler errors
			if len(p.Errors()) > 0 {
				t.Fatalf("Unexpected parser errors: %v", p.Errors())
			}

			ctx := llvm.NewContext()
			cc := NewCodeCompiler(ctx, "TestFormatStringErrors", ast.NewCode())

			funcCache := make(map[string]*Func)
			sc := NewScriptCompiler(ctx, "TestFormatErrors", program, cc, funcCache)
			errs := sc.Compile()

			if len(errs) == 0 {
				t.Fatal("Expected a compile error, but got none.")
			}
			if !strings.Contains(errs[0].Error(), tc.expectError) {
				t.Errorf("Expected error message to contain %q, but got %q", tc.expectError, errs[0].Error())
			}
		})
	}
}

func TestValidFormatString(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		expectOutput string
	}{
		{
			name: "ValidFormatString",
			input: `x = 5
"x = -x%ld"`,
        expectOutput: "x = %lld",
		},
		{
			name: "Escape%%",
			input: `x = 5
"Value: %%-x%%`,
        expectOutput: "Value: %%%%%lld%%",
		},
		{
			name:         "NoIdentifier",
			input:        `"Value: %d"`,
			expectOutput: "Value: %%d",
		},
		{
			name: "SpaceAfterVar",
			input: `x = 5
"x = -x %d"`,
        expectOutput: "x = %lld %%d",
		},
		{
			name: "PercentAfterSpecifier",
			input: `x = 5.
"x = -x%4.2f%"`,
			expectOutput: "x = %4.2f%%",
		},
		{
			name: "SpecifierArg",
			input: `x = 14
digits = 4
"x is -x%(-digits)d"`,
			expectOutput: "x is %*lld",
		},
		{
			name: "MixedBackToBack",
			input: `x = 3
y = 3.2
"x = -x%ld-y"`,
			// float default format is "%s"
        expectOutput: "x = %lld%s",
		},
		{
			name: "MixedBackToBackSpecifiers",
			input: `x = 3
y = 3.2
"x = -x%ld-y%f"`,
			// float default format is "%s"
        expectOutput: "x = %lld%f",
		},
		{
			name: "MixedBackToBackOneVar",
			input: `x = 3
"x = -x%ld-y%f"`,
			// float default format is "%s"
        expectOutput: "x = %lld-y%%f",
		},
		{
			name: "CountPointer",
			input: `x = 5
"x = -x%n"`,
        expectOutput: "x = %lln",
		},
		{
			name:         "VarNotDefined",
			input:        `"Value: -x%s"`,
			expectOutput: "Value: -x%%s",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := lexer.New("TestValidFormatString", tc.input)
			p := parser.New(l)
			program := p.ParseProgram()

			ctx := llvm.NewContext()
			cc := NewCodeCompiler(ctx, "TestValidFormatString", ast.NewCode())

			funcCache := make(map[string]*Func)
			sc := NewScriptCompiler(ctx, "TestValidFormatString", program, cc, funcCache)
			sc.Compile()
			ir := sc.Compiler.GenerateIR()
			if !strings.Contains(ir, tc.expectOutput) {
				t.Errorf("IR does not contain string constant.\nIR: %s\n, expected to contain: %s\n", ir, tc.expectOutput)
			}
		})
	}
}
