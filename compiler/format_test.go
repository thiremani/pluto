package compiler

import (
	"fmt"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/parser"
	"strings"
	"testing"

	"tinygo.org/x/go-llvm"
)

func TestFormatStringPanics(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectPanic string
	}{
		{
			name:        "NoIdentifier",
			input:       `"Value: %d"`,
			expectPanic: "specifier found without corresponding identifier for variable. The allowed format is -var%specifier Specifier is at index 7",
		},
		{
			name: "AsteiskNotAllowed",
			input: `x = 10
"Value: -x%*d"`,
			expectPanic: "Using * not allowed in format specifier. Instead use (-var), where var is an integer variable.",
		},
		{
			name: "MissingClosingParen",
			input: `x = 2
"Value: -x%(-var"`,
			expectPanic: "Expected ) after the identifier var",
		},
		{
			name:        "UndefinedVariable",
			input:       `"Value: -undefinedVar%d"`,
			expectPanic: "Identifier undefinedVar not found. Unexpected specifier %d after identifier",
		},
		{
			name: "InvalidFormatSpecifier",
			input: `x = 4
"x = -x%"`,
			expectPanic: "Invalid format specifier string: Format specifier is incomplete",
		},
		{
			name:        "UndefinedVariable",
			input:       `"Value: -x%s"`,
			expectPanic: "Identifier x not found. Unexpected specifier %s after identifier",
		},
		{
			name: "IdentifierWithinSpecifierNotFound",
			input: `x = 10
"Value: -x%(-var)d"`,
			expectPanic: "Identifier var not found within specifier. Specifier was after identifier x",
		},
		{
			name: "SpecifierDoesNotEnd",
			input: `x = 10
"Value: -x%#-"`,
			expectPanic: "Invalid format specifier string: Format specifier '%#-' is incomplete",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("Expected panic, got none")
				}
				if !strings.Contains(fmt.Sprint(r), tc.expectPanic) {
					t.Fatalf("Expected panic containing %q, got %q", tc.expectPanic, r)
				}
			}()

			l := lexer.New(tc.input)
			p := parser.New(l)
			program := p.ParseProgram()
			c := NewCompiler(llvm.NewContext(), "TestFormatPanics")
			c.CompileScript(program)
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
			expectOutput: "Value: %%%ld%%",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := lexer.New(tc.input)
			p := parser.New(l)
			program := p.ParseProgram()
			c := NewCompiler(llvm.NewContext(), "TestValidFormatString")
			c.CompileScript(program)
			ir := c.GenerateIR()
			if !strings.Contains(ir, tc.expectOutput) {
				t.Errorf("IR does not contain string constant.\nIR: %s\n, expected to contain: %s\n", ir, tc.expectOutput)
			}
		})
	}
}
