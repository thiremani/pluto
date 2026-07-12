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
			name: "AsteriskNotAllowed",
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
			name: "IdentifierWithinSpecifierNotFound",
			input: `x = 10
"Value: -x%(-var)d"`,
			expectError: "Undefined variable var within specifier. String Literal is Value: -x%(-var)d",
		},
		{
			name: "SpecifierDoesNotEnd",
			input: `x = 10
"Value: -x%#-"`,
			expectError: `Invalid format specifier string: Format specifier "%#-" is incomplete`,
		},
		{
			name: "InvalidDynamicIdentifier",
			input: `x = 5
"Value: -x%(-1)d"`,
			expectError: "Expected an identifier of the form (-name)",
		},
		{
			name: "DynamicSpecifierWrongType",
			input: `x = 5
width = 3.5
"Value: -x%(-width)d"`,
			expectError: "Format specifier variable width must have type I64, got F64",
		},
		{
			name: "UnexpectedSpecifierRune",
			input: `x = 5
"Value: -x%0vd"`,
			expectError: `Unexpected 'v' in format specifier "%0"`,
		},
		{
			name: "InvalidLengthForConversion",
			input: `s = "hello"
"Value: -s%ls"`,
			expectError: `Length modifier "l" is not supported for %s`,
		},
		{
			name: "UnsupportedLengthAtStart",
			input: `x = 5
"Value: -x%hd"`,
			expectError: "Length modifier 'h' is not supported",
		},
		{
			name: "QuotedSpecifierOnNonString",
			input: `x = 5
"Value: -x%q"`,
			expectError: "Format specifier end 'q' is not correct for variable type. Variable identifier: x. Variable type: I64",
		},
		{
			name: "QuotedSpecifierPrecision",
			input: `s = "hello"
"Value: -s%.3q"`,
			expectError: "Precision is not supported for %q because it can truncate the quoted string",
		},
		{
			name: "EscapedSpecifierClosingParen",
			input: `s = "hello"
width = 10
"Value: -s%(-width\x29q"`,
			expectError: "Expected ) after the identifier width",
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
		{
			name: "CountOnNonInteger",
			input: `s = "hello"
"Value: -s%n"`,
			expectError: "Format specifier end 'n' is not correct for variable type. Variable identifier: s. Variable type: Str",
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
			cc := NewCodeCompiler(ctx, "TestFormatStringErrors", "", ast.NewCode())

			funcCache := make(map[string]*Func)
			sc := NewScriptCompiler(ctx, program, cc, funcCache, cc.Compiler.ExprCache)
			errs := sc.Compile()

			if len(errs) == 0 {
				t.Fatal("Expected a compile error, but got none.")
			}
			if len(errs) != 1 {
				t.Fatalf("Expected one compile error, but got %d: %v", len(errs), errs)
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
		expectIR     string
		rejectIR     string
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
			name: "TrailingPercent",
			input: `x = 95
"Progress: -x%"`,
			expectOutput: "Progress: %lld%%",
		},
		{
			name: "PercentBeforeText",
			input: `x = 95
"Progress: -x% complete"`,
			expectOutput: "Progress: %lld%% complete",
		},
		{
			name: "UnsupportedSpecifierIsText",
			input: `x = 5
"Value: -x%v"`,
			expectOutput: "Value: %lld%%v",
		},
		{
			name: "UnresolvedMarkerKeepsSpecifierLiteral",
			input: `width = 5
"Width: -width; literal: -missing%(-width)d"`,
			expectOutput: "Width: %lld; literal: -missing%%(-width)d",
		},
		{
			name: "EscapedSpecifierConversionIsText",
			input: `s = "hello"
"Value: -s%\x71"`,
			expectOutput: "Value: %s%%q",
		},
		{
			name: "EscapedSpecifierWidthIsText",
			input: `s = "hello"
"Value: -s%\x31q"`,
			expectOutput: "Value: %s%%1q",
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
			name: "UpperHexFloat",
			input: `x = 5.
"x = -x%A"`,
			expectOutput: "x = %A",
		},
		{
			name: "SpecifierArg",
			input: `x = 14
digits = 4
"x is -x%(-digits)d"`,
			expectOutput: "x is %*lld",
			expectIR:     "i32 4, i64 14",
		},
		{
			name: "CharacterArg",
			input: `x = 65
"x is -x%c"`,
			expectOutput: "x is %c",
			expectIR:     "i32 65)",
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
		{
			name: "EscapedMarker",
			input: `x = 5
"Literal: \x2dx and value -x"`,
			expectOutput: "Literal: -x and value %lld",
		},
		{
			name: "UnicodeEscapedMarker",
			input: `x = 5
"Literal: \u002dx and value -x"`,
			expectOutput: "Literal: -x and value %lld",
		},
		{
			name: "WideUnicodeEscapedMarker",
			input: `x = 5
"Literal: \U0000002dx and value -x"`,
			expectOutput: "Literal: -x and value %lld",
		},
		{
			name: "EscapedMarkerIdentifierStart",
			input: `x = 5
"Literal: -\x78 and value -x"`,
			expectOutput: "Literal: -x and value %lld",
		},
		{
			name: "EscapedMarkerIdentifierContinuation",
			input: `x = 5
xy = 6
"Value: -x\x79; xy: -xy"`,
			expectOutput: "Value: %lldy; xy: %lld",
		},
		{
			name: "EscapedSpecifier",
			input: `s = "hello"
"Value: -s\x25q"`,
			expectOutput: "Value: %s%%q",
		},
		{
			name: "UnicodeEscapedSpecifier",
			input: `s = "hello"
"Value: -s\u0025q"`,
			expectOutput: "Value: %s%%q",
		},
		{
			name: "WideUnicodeEscapedSpecifier",
			input: `s = "hello"
"Value: -s\U00000025q"`,
			expectOutput: "Value: %s%%q",
		},
		{
			name: "QuotedSpecifierWidth",
			input: `s = "hello"
"Value: -s%10q"`,
			expectOutput: "Value: %10s",
			expectIR:     "call ptr @str_quote",
		},
		{
			name: "QuotedSpecifierDynamicWidth",
			input: `s = "hello"
width = 10
"Value: -s%(-width)q"`,
			expectOutput: "Value: %*s",
			expectIR:     "call ptr @str_quote",
		},
		{
			name: "StringSpecifierDynamicWidth",
			input: `s = "hello"
width = 10
"Value: -s%(-width)s"`,
			expectOutput: "Value: %*s",
			rejectIR:     "call ptr @str_quote",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := lexer.New("TestValidFormatString", tc.input)
			p := parser.New(l)
			program := p.ParseProgram()

			ctx := llvm.NewContext()
			cc := NewCodeCompiler(ctx, "TestValidFormatString", "", ast.NewCode())

			funcCache := make(map[string]*Func)
			sc := NewScriptCompiler(ctx, program, cc, funcCache, cc.Compiler.ExprCache)
			if errs := sc.Compile(); len(errs) > 0 {
				t.Fatalf("unexpected compile errors: %v", errs)
			}
			ir := sc.Compiler.GenerateIR()
			if !strings.Contains(ir, tc.expectOutput) {
				t.Errorf("IR does not contain string constant.\nIR: %s\n, expected to contain: %s\n", ir, tc.expectOutput)
			}
			if tc.expectIR != "" && !strings.Contains(ir, tc.expectIR) {
				t.Errorf("IR does not contain %q.\nIR: %s", tc.expectIR, ir)
			}
			if tc.rejectIR != "" && strings.Contains(ir, tc.rejectIR) {
				t.Errorf("IR unexpectedly contains %q.\nIR: %s", tc.rejectIR, ir)
			}
		})
	}
}

func TestUnresolvedMarkerSpecifierDoesNotCreateMarker(t *testing.T) {
	value := `-missing%(-width)d`
	widthOnly := func(name string) bool { return name == "width" }
	if hasValidMarkers(value, widthOnly) {
		t.Fatal("dynamic identifier of an unresolved marker must remain literal")
	}

	mainOnly := func(name string) bool { return name == "missing" }
	if !hasValidMarkers(value, mainOnly) {
		t.Fatal("defined main identifier should form a marker")
	}
}
