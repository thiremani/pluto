package compiler

import (
	"strings"
	"testing"

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
			name:        "UndefinedVariable",
			input:       `"Value: -undefinedVar%d"`,
			expectError: "Identifier undefinedVar not found. Unexpected specifier %d after identifier",
		},
		{
			name: "InvalidFormatSpecifier",
			input: `x = 4
"x = -x%"`,
			expectError: "Invalid format specifier string: Format specifier is incomplete",
		},
		{
			name:        "UndefinedVariable",
			input:       `"Value: -x%s"`,
			expectError: "Identifier x not found. Unexpected specifier %s after identifier",
		},
		{
			name: "IdentifierWithinSpecifierNotFound",
			input: `x = 10
"Value: -x%(-var)d"`,
			expectError: "Identifier var not found within specifier. Specifier was after identifier x",
		},
		{
			name: "SpecifierDoesNotEnd",
			input: `x = 10
"Value: -x%#-"`,
			expectError: "Invalid format specifier string: Format specifier '%#-' is incomplete",
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

			sc := NewScriptCompiler(llvm.NewContext(), "TestFormatErrors", program, nil)
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
			expectOutput: "x = %ld",
		},
		{
			name: "Escape%%",
			input: `x = 5
"Value: %%-x%%`,
			expectOutput: "Value: %%%%%ld%%",
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
			expectOutput: "x = %ld %%d",
		},
		{
			name: "PercentAfterSpecifier",
			input: `x = 5.
"x = -x%4.2f%"`,
			expectOutput: "x = %4.2f%%",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := lexer.New("TestValidFormatString", tc.input)
			p := parser.New(l)
			program := p.ParseProgram()
			sc := NewScriptCompiler(llvm.NewContext(), "TestValidFormatString", program, nil)
			sc.Compile()
			ir := sc.Compiler.GenerateIR()
			if !strings.Contains(ir, tc.expectOutput) {
				t.Errorf("IR does not contain string constant.\nIR: %s\n, expected to contain: %s\n", ir, tc.expectOutput)
			}
		})
	}
}
