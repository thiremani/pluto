package compiler

import (
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

// parseMarker attempts to parse a marker starting at index i in runes.
// It returns the parsed identifier, the custom specifier (if any), and the new index.
func parseMarker(runes []rune, i int) (identifier string, customSpec string, newIndex int) {
	// Assumes runes[i] == '-' and that runes[i+1] is a valid identifier start.
	j := i + 2
	for j < len(runes) && lexer.IsLetterOrDigit(runes[j]) {
		j++
	}
	identifier = string(runes[i+1 : j])
	customSpec = ""
	// Check if a custom specifier follows, starting with '%'
	if j < len(runes) && runes[j] == '%' {
		specStart := j
		j++ // consume '%'
		// Read until we encounter a conversion specifier (like d, f, etc.)
		for j < len(runes) && !formatSpecifierEnd(runes[j]) {
			j++
		}
		// Optionally include the conversion specifier character:
		if j < len(runes) {
			j++ // include the conversion specifier
		}
		customSpec = string(runes[specStart:j])
	}
	return identifier, customSpec, j
}

// formatIdentifiers scans the string literal for markers of the form "-identifier".
// For each such marker, it looks up the identifier in the symbol table and replaces the marker
// with the appropriate conversion specifier. It returns the new format string along with a slice
// of llvm.Value for each variable found.
func (c *Compiler) formatIdentifiers(s string) (string, []llvm.Value) {
	var builder strings.Builder
	var args []llvm.Value

	// Convert the input to a slice of runes so we can properly iterate over Unicode characters.
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		// If we see a '-' and the next rune is a valid identifier start...
		if runes[i] == '-' && i+1 < len(runes) && lexer.IsLetter(runes[i+1]) {
			// Parse the identifier.
			identifier, customSpec, newIndex := parseMarker(runes, i)
			// Look up the identifier in the symbol table.
			if sym, ok := c.symbols[identifier]; ok {
				// Use the custom specifier if provided; otherwise, use the default.
				spec := customSpec
				if spec == "" || spec == "%%" {
					spec = defaultSpecifier(sym.Type)
				}
				builder.WriteString(spec)
				if customSpec == "%%" {
					builder.WriteString(customSpec)
				}
				args = append(args, sym.Val)
			} else {
				// If the symbol isn't found, you might want to error out or leave the marker intact.
				// Here, we simply output the marker as-is.
				builder.WriteRune('-')
				builder.WriteString(identifier)
				builder.WriteString(customSpec)
			}
			// Advance past the marker.
			i = newIndex
			continue
		}
		// Otherwise, simply output the rune.
		builder.WriteRune(runes[i])
		i++
	}
	return builder.String(), args
}
