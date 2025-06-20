package compiler

import (
	"errors"
	"fmt"
	"strings"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

func formatSpecifierEnd(ch rune) bool {
	switch ch {
	case 'd', 'i', 'u', 'o', 'x', 'X', 'f', 'F', 'e', 'E', 'g', 'G', 'a', 'c', 's', 'p', 'n', '%':
		return true
	}
	return false
}

// defaultSpecifier returns the printf conversion specifier for a given type.
func defaultSpecifier(t Type) (string, error) {
	switch t.Kind() {
	case IntKind:
		return "%ld", nil
	case FloatKind:
		return "%.15g", nil
	case StrKind:
		return "%s", nil
	default:
		err := fmt.Errorf("unsupported type in print statement %s", t.String())
		return "", err
	}
}

// parseSpecifier assumes runes[start] == '%'
// for the * option the identifers are of the form (-identifier), enclosed wihin parentheses
// these are then returned as specIds. (-identifier) is replaced with *
func (c *Compiler) parseSpecifier(sl *ast.StringLiteral, runes []rune, start int) (specIds []string, spec string, endIndex int) {
	// Read until we encounter a conversion specifier end char (like d, f, etc.)
	specRunes := []rune{runes[start]}
	it := start + 1
	for it < len(runes) && !formatSpecifierEnd(runes[it]) {
		if runes[it] == '*' {
			cerr := &token.CompileError{
				Token: sl.Token,
				Msg:   fmt.Sprintf("Using * not allowed in format specifier (after the %% char). Instead use (-var) where var is an integer variable. Error str: %s", sl.Value),
			}
			c.Errors = append(c.Errors, cerr)
			it += 1
			continue
		}
		if it+2 < len(runes) && runes[it] == '(' && runes[it+1] == '-' && lexer.IsLetter(runes[it+2]) {
			specId, end := parseIdentifier(runes, it+2)
			if end >= len(runes) || runes[end] != ')' {
				cerr := &token.CompileError{
					Token: sl.Token,
					Msg:   fmt.Sprintf("Expected ) after the identifier %s. Str: %s", specId, sl.Value),
				}
				c.Errors = append(c.Errors, cerr)
				it = end + 1
				continue
			}
			specIds = append(specIds, specId)
			specRunes = append(specRunes, '*')
			it = end + 1
			continue
		}
		specRunes = append(specRunes, runes[it])
		it++
	}

	// if we don't hit string end, add one to include the last character in return string
	if it < len(runes) {
		specRunes = append(specRunes, runes[it])
		it++
	}
	if !formatSpecifierEnd(specRunes[len(specRunes)-1]) {
		cerr := &token.CompileError{
			Token: sl.Token,
			Msg:   fmt.Sprintf("Invalid format specifier string: Format specifier '%s' is incomplete. Str: %s", string(specRunes), sl.Value),
		}
		c.Errors = append(c.Errors, cerr)
	}

	endIndex = it
	spec = string(specRunes)
	return
}

// Assumes runes[start] is a valid start to the identifier (lexer.IsLetter)
func parseIdentifier(runes []rune, start int) (identifier string, end int) {
	j := start + 1
	for j < len(runes) && lexer.IsLetterOrDigit(runes[j]) {
		j++
	}
	end = j
	identifier = string(runes[start:end])
	return
}

// parseMarker attempts to parse a marker starting at index i in runes.
// It returns the parsed identifier, the custom specifier (if any), and the new index.
func (c *Compiler) parseMarker(sl *ast.StringLiteral, runes []rune, i int) (idents []string, customSpec string, newIndex int, err error) {
	// Assumes runes[i] == '-' and that runes[i+1] is a valid identifier start.
	specIds := make([]string, 0)
	identifier, end := parseIdentifier(runes, i+1)
	if end < len(runes) && runes[end] == '%' {
		if end+1 == len(runes) {
			cerr := &token.CompileError{
				Token: sl.Token,
				Msg:   fmt.Sprintf("Invalid format specifier string: Format specifier is incomplete. Str: %s", sl.Value),
			}
			c.Errors = append(c.Errors, cerr)
			newIndex = end + 1
			err = errors.New(cerr.Msg)
			return
		}
		specStart := end
		specIds, customSpec, end = c.parseSpecifier(sl, runes, specStart)
	}

	idents = append(idents, identifier)
	idents = append(idents, specIds...)
	newIndex = end
	return
}

// assumes we have at least one identifier in ids. CustomSpec is printf specifier %...
func (c *Compiler) parseIdsWithSpecifiers(sl *ast.StringLiteral, ids []string, customSpec string) (formattedStr string, idArgs []llvm.Value) {
	var builder strings.Builder
	mainId := ids[0]
	// Look up the identifier in the symbol table.
	if sym, ok := Get(c.Scopes, mainId); ok {
		sym = c.derefIfPointer(sym)
		// Use the custom specifier if provided; otherwise, use the default.
		for _, specId := range ids[1:] {
			var specSym *Symbol
			var exists bool
			if specSym, exists = Get(c.Scopes, specId); !exists {
				cerr := &token.CompileError{
					Token: sl.Token,
					Msg:   fmt.Sprintf("Identifier %s not found within specifier. Specifier was after identifier %s. Str: %s", specId, mainId, sl.Value),
				}
				c.Errors = append(c.Errors, cerr)
				continue
			}
			idArgs = append(idArgs, c.derefIfPointer(specSym).Val)
		}

		// if customSpec is either "" or %% it must be written later anyway. %% must be written as is.
		if customSpec == "" || customSpec == "%%" {
			spec, err := defaultSpecifier(sym.Type)
			if err != nil {
				cerr := &token.CompileError{
					Token: sl.Token,
					Msg:   fmt.Sprintf("Error parsing ids with specifiers. String: %s. Err: %s", sl.Value, err),
				}
				c.Errors = append(c.Errors, cerr)
				return
			}
			builder.WriteString(spec)
		}
		builder.WriteString(customSpec)
		formattedStr = builder.String()
		if len(customSpec) > 0 && customSpec[len(customSpec)-1] == 'n' {
			s := c.promoteToMemory(mainId)
			idArgs = append(idArgs, s.Val)
			return
		}
		idArgs = append(idArgs, sym.Val)
		return
	}
	// If the symbol isn't found, you might want to error out or leave the marker intact.
	// Here, we simply output the marker as-is.
	if len(ids) > 1 {
		cerr := &token.CompileError{
			Token: sl.Token,
			Msg:   fmt.Sprintf("Identifier %s not found. Unexpected specifier %s after identifier with identifier parameters %v. Str: %s", mainId, customSpec, ids[1:], sl.Value),
		}
		c.Errors = append(c.Errors, cerr)
	}
	builder.WriteRune('-')
	builder.WriteString(mainId)
	if customSpec != "" && customSpec != "%%" {
		cerr := &token.CompileError{
			Token: sl.Token,
			Msg:   fmt.Sprintf("Identifier %s not found. Unexpected specifier %s after identifier. Str: %s", mainId, customSpec, sl.Value),
		}
		c.Errors = append(c.Errors, cerr)
	}
	builder.WriteString(customSpec)
	formattedStr = builder.String()
	return
}

// formatIdentifiers scans the string literal for markers of the form "-identifier".
// For each such marker, it looks up the identifier in the symbol table and replaces the marker
// with the appropriate conversion specifier. It returns the new format string along with a slice
// of llvm.Value for each variable found.
// it additionally supports specifiers %d, %f etc as defined by the printf function
// the * option for the width and precision should be replaced by their corresponding variables within parentheses and with the marker
// eg: -a%(-w)d prints the integer variable a with width given by the variable w
func (c *Compiler) formatIdentifiers(sl *ast.StringLiteral) (string, []llvm.Value) {
	var builder strings.Builder
	var args []llvm.Value

	// Convert the input to a slice of runes so we can properly iterate over Unicode characters.
	runes := []rune(sl.String())
	i := 0
	for i < len(runes) {
		if !(runes[i] == '-' && i+1 < len(runes) && lexer.IsLetter(runes[i+1])) {
			builder.WriteRune(runes[i])
			if runes[i] == '%' {
				// % is not after -var. so we allow lone %
				// for printf we need to write it twice
				builder.WriteRune(runes[i])
			}
			i++
			continue
		}
		// If we see a '-' and the next rune is a valid identifier start...
		// Parse the marker.
		identifiers, customSpec, newIndex, err := c.parseMarker(sl, runes, i)
		if err != nil {
			i = newIndex
			continue
		}
		formattedStr, idArgs := c.parseIdsWithSpecifiers(sl, identifiers, customSpec)
		builder.WriteString(formattedStr)
		args = append(args, idArgs...)
		// Advance past the marker.
		i = newIndex
	}

	st := builder.String()
	return st, args
}
