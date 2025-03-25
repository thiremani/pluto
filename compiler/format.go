package compiler

import (
	"fmt"
	"pluto/lexer"
	"strings"
	"tinygo.org/x/go-llvm"
)

func formatSpecifierEnd(ch rune) bool {
	return ch == 'd' ||
		ch == 'i' ||
		ch == 'u' ||
		ch == 'o' ||
		ch == 'x' ||
		ch == 'X' ||
		ch == 'f' ||
		ch == 'F' ||
		ch == 'e' ||
		ch == 'E' ||
		ch == 'g' ||
		ch == 'G' ||
		ch == 'a' ||
		ch == 'c' ||
		ch == 's' ||
		ch == 'p' ||
		ch == 'n' ||
		ch == '%'
}

// parseSpecifier assumes runes[start] == '%'
// for the * option the identifers are of the form (-identifier), enclosed wihin parentheses
// these are then returned as specIds. (-identifier) is replaced with *
func parseSpecifier(runes []rune, start int) (specIds []string, spec string, endIndex int) {
	// Read until we encounter a conversion specifier end char (like d, f, etc.)
	specRunes := []rune{runes[start]}
	it := start + 1
	for it < len(runes) && !formatSpecifierEnd(runes[it]) {
		if runes[it] == '*' {
			panic("Using * not allowed in format specifier. Instead use (-var), where var is an integer variable.")
		}
		if it+2 < len(runes) && runes[it] == '(' && runes[it+1] == '-' && lexer.IsLetter(runes[it+2]) {
			specId, end := parseIdentifier(runes, it+2)
			if end >= len(runes) || runes[end] != ')' {
				panic(fmt.Sprintf("Expected ) after the identifier %s", specId))
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
func parseMarker(runes []rune, i int) (idents []string, customSpec string, newIndex int) {
	// Assumes runes[i] == '-' and that runes[i+1] is a valid identifier start.
	specIds := make([]string, 0)
	identifier, end := parseIdentifier(runes, i+1)
	if end < len(runes) && runes[end] == '%' {
		if end+1 == len(runes) {
			panic("Invalid format specifier string: Format specifier is incomplete")
		}
		specStart := end
		specIds, customSpec, end = parseSpecifier(runes, specStart)
	}

	idents = append(idents, identifier)
	idents = append(idents, specIds...)
	newIndex = end
	return
}

// assumes we have at least one identifier in ids. CustomSpec is printf specifier %...
func (c *Compiler) parseIdsWithSpecifiers(ids []string, customSpec string) (formattedStr string, idArgs []llvm.Value) {
	var builder strings.Builder
	mainId := ids[0]
	// Look up the identifier in the symbol table.
	if sym, ok := c.symbols[mainId]; ok {
		sym = c.derefIfPointer(sym)
		// Use the custom specifier if provided; otherwise, use the default.
		for _, specId := range ids[1:] {
			var specSym Symbol
			var exists bool
			if specSym, exists = c.symbols[specId]; !exists {
				panic(fmt.Sprintf("Identifier %s not found within specifier. Specifier was after identifier %s", specId, mainId))
			}
			idArgs = append(idArgs, c.derefIfPointer(specSym).Val)
		}

		// if customSpec is either "" or %% it must be written later anyway. %% must be written as is.
		if customSpec == "" || customSpec == "%%" {
			builder.WriteString(defaultSpecifier(sym.Type))
		}
		builder.WriteString(customSpec)
		formattedStr = builder.String()
		if len(customSpec) > 0 && customSpec[len(customSpec)-1] == 'n' {
			c.promoteToMemory(mainId)
			idArgs = append(idArgs, c.symbols[mainId].Val)
			return
		}
		idArgs = append(idArgs, sym.Val)
		return
	}
	// If the symbol isn't found, you might want to error out or leave the marker intact.
	// Here, we simply output the marker as-is.
	if len(ids) > 1 {
		panic(fmt.Sprintf("Identifier %s not found. Unexpected specifier %s after identifier with identifier parameters %v", mainId, customSpec, ids[1:]))
	}
	builder.WriteRune('-')
	builder.WriteString(mainId)
	if customSpec != "" && customSpec != "%%" {
		panic(fmt.Sprintf("Identifier %s not found. Unexpected specifier %s after identifier", mainId, customSpec))
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
func (c *Compiler) formatIdentifiers(s string) (string, []llvm.Value) {
	var builder strings.Builder
	var args []llvm.Value

	// Convert the input to a slice of runes so we can properly iterate over Unicode characters.
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		if !(runes[i] == '-' && i+1 < len(runes) && lexer.IsLetter(runes[i+1])) {
			builder.WriteRune(runes[i])
			i++
			continue
		}
		// If we see a '-' and the next rune is a valid identifier start...
		// Parse the marker.
		identifiers, customSpec, newIndex := parseMarker(runes, i)
		formattedStr, idArgs := c.parseIdsWithSpecifiers(identifiers, customSpec)
		builder.WriteString(formattedStr)
		args = append(args, idArgs...)
		// Advance past the marker.
		i = newIndex
	}
	st := builder.String()
	return st, args
}
