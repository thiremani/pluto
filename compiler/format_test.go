package compiler

import (
	"strings"
	"testing"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/parser"
	"github.com/thiremani/pluto/token"

	"tinygo.org/x/go-llvm"
)

func TestValidateSpecifierModifiers(t *testing.T) {
	tests := []struct {
		name         string
		flags        string
		length       string
		hasWidth     bool
		hasPrecision bool
		conversion   rune
		expectError  string
	}{
		{name: "SignedIntegerFlags", flags: "-+ 0", conversion: 'd'},
		{name: "HexFlags", flags: "-#0", conversion: 'x'},
		{name: "FloatFlags", flags: "-+ #0", conversion: 'A'},
		{name: "QuotedWidth", flags: "-", hasWidth: true, conversion: 'q'},
		{name: "CountLength", length: "ll", conversion: 'n'},
		{name: "RepeatedFlag", flags: "--", conversion: 'd'},
		{name: "SignedAlternateForm", flags: "#", conversion: 'd', expectError: `Format flag '#' is not supported for %d`},
		{name: "UnsignedSign", flags: "+", conversion: 'u', expectError: `Format flag '+' is not supported for %u`},
		{name: "StringLength", length: "l", conversion: 's', expectError: `Length modifier "l" is not supported for %s`},
		{name: "PointerWidth", hasWidth: true, conversion: 'p', expectError: `Width is not supported for %p`},
		{name: "CharacterPrecision", hasPrecision: true, conversion: 'c', expectError: `Precision is not supported for %c`},
		{name: "QuotedPrecision", hasPrecision: true, conversion: 'q', expectError: `Precision is not supported for %q because it can truncate the quoted string`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSpecifierModifiers(token.Token{}, "value", tc.flags, tc.length, tc.hasWidth, tc.hasPrecision, tc.conversion)
			if tc.expectError == "" {
				if err != nil {
					t.Fatalf("unexpected validation error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected validation error containing %q", tc.expectError)
			}
			if !strings.Contains(err.Msg, tc.expectError) {
				t.Fatalf("expected error containing %q, got %q", tc.expectError, err.Msg)
			}
		})
	}
}

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
			name: "SpecifierDoesNotEnd",
			input: `x = 10
"Value: -x%#-"`,
			expectError: `Invalid format specifier string: Format specifier "%#-" is incomplete`,
		},
		{
			name: "TrailingPercent",
			input: `x = 95
"Progress: -x%"`,
			expectError: `Invalid format specifier string: Format specifier "%" is incomplete. Use \% or %% for a literal percent after a value`,
		},
		{
			name: "IdentifierWithinSpecifierNotFound",
			input: `x = 10
"Value: -x%(-width)d"`,
			expectError: "Undefined variable width within specifier. String Literal is Value: -x%(-width)d",
		},
		{
			name: "MissingDynamicWidthClosingParen",
			input: `x = 10
"Value: -x%(-width"`,
			expectError: "Expected ) after the identifier width. Str: Value: -x%(-width",
		},
		{
			name: "InvalidDynamicPrecisionGroup",
			input: `x = 5
"Value: -x%.(-1)d"`,
			expectError: `Unexpected '(' in format specifier "%."`,
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
			name: "UnsupportedSpecifier",
			input: `x = 5
"Value: -x%v"`,
			expectError: `Unexpected 'v' in format specifier "%"`,
		},
		{
			name: "ParenthesizedTextAfterPercent",
			input: `n = 30
"Value: -n%(5)d."`,
			expectError: `Unexpected '(' in format specifier "%"`,
		},
		{
			name: "EscapedSpecifierConversion",
			input: `s = "hello"
"Value: -s%\x71"`,
			expectError: "Escape sequences cannot be used as format syntax",
		},
		{
			name: "EscapedSpecifierWidth",
			input: `s = "hello"
"Value: -s%\x31q"`,
			expectError: "Escape sequences cannot be used as format syntax",
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
			name: "LiteralTrailingPercent",
			input: `x = 95
"Progress: -x%%"`,
			expectOutput: "Progress: %lld%%",
		},
		{
			name: "PercentBeforeText",
			input: `x = 95
"Progress: -x%% complete"`,
			expectOutput: "Progress: %lld%% complete",
		},
		{
			name: "EscapedPercentBeforeText",
			input: `x = 95
"Progress: -x\% complete"`,
			expectOutput: "Progress: %lld%% complete",
		},
		{
			name: "SeparatedPercent",
			input: `n = 95
"Profit is -n %"`,
			expectOutput: "Profit is %lld %%",
		},
		{
			name: "PercentBeforeParentheticalText",
			input: `n = 95
"Profit is -n%%(Higher than last year"`,
			expectOutput: "Profit is %lld%%(Higher than last year",
		},
		{
			name: "ParenthesizedNumberIsText",
			input: `n = 30
"Value: -n%%(5)d."`,
			expectOutput: "Value: %lld%%(5)d.",
		},
		{
			name: "PercentBeforeSpacedParentheticalText",
			input: `n = 95
"Profit is -n%% (Higher than last year)"`,
			expectOutput: "Profit is %lld%% (Higher than last year)",
		},
		{
			name: "MarkerBeforeParentheticalText",
			input: `x = 5
"Value of x is -x(it's a new variable)"`,
			expectOutput: "Value of x is %lld(it's a new variable)",
		},
		{
			name: "MarkerBeforeSpacedParentheticalText",
			input: `x = 5
"Value of x is -x (it's a new variable)"`,
			expectOutput: "Value of x is %lld (it's a new variable)",
		},
		{
			name: "UnresolvedMarkerAllowsFollowingMarker",
			input: `width = 5
"Width: -width; literal: -missing%(-width)d"`,
			expectOutput: "Width: %lld; literal: -missing%%(%lld)d",
		},
		{
			name: "SpaceAfterVar",
			input: `x = 5
"x = -x %d"`,
			expectOutput: "x = %lld %%d",
		},
		{
			name: "SignedIntegerSpaceFlag",
			input: `x = 5
"x = -x% d"`,
			expectOutput: "x = % lld",
		},
		{
			name: "FloatSpaceFlag",
			input: `x = 5.
"x = -x% .2f"`,
			expectOutput: "x = % .2f",
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

func TestUnresolvedMarkerAllowsFollowingMarker(t *testing.T) {
	value := `-missing%(-width)d`
	widthOnly := func(name string) bool { return name == "width" }
	if !hasValidMarkers(value, widthOnly) {
		t.Fatal("a defined marker following an unresolved marker must still be recognized")
	}

	mainOnly := func(name string) bool { return name == "missing" }
	if !hasValidMarkers(value, mainOnly) {
		t.Fatal("defined main identifier should form a marker")
	}
}
