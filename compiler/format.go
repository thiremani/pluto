package compiler

import (
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

var specToKind = map[rune]Kind{
	'd': IntKind,
	'i': IntKind,
	'u': IntKind,
	'o': IntKind,
	'x': IntKind,
	'X': IntKind,
	'f': FloatKind,
	'F': FloatKind,
	'e': FloatKind,
	'E': FloatKind,
	'g': FloatKind,
	'G': FloatKind,
	'a': FloatKind,
	'c': IntKind, // maybe character code
	's': StrKind,
	'p': PointerKind, // if you introduce a PtrKind
	'n': IntKind,     // byte‐count pointer
}

// defaultSpecifier returns the printf conversion specifier for a given type.
func defaultSpecifier(t Type) (string, error) {
	switch t.Kind() {
	case IntKind:
		return "%ld", nil
	case FloatKind:
		// Floats are converted to char* via runtime helpers (f64_str/f32_str)
		return "%s", nil
	case StrKind:
		return "%s", nil
	case RangeKind:
		return "%s", nil
	case ArrayKind:
		// Arrays are converted to char* via runtime helpers
		return "%s", nil
	default:
		err := fmt.Errorf("unsupported type in print statement %s", t.String())
		return "", err
	}
}

// parseSpecifier assumes runes[start] == '%'
// for the * option the identifers are of the form (-identifier), enclosed wihin parentheses
// these are then returned as specIds. (-identifier) is replaced with *
// endIndex is one index after specifier ends, or after an invalid rune
func (c *Compiler) parseSpecifier(sl *ast.StringLiteral, runes []rune, start int) (specSyms []*Symbol, spec string, endIndex int, err *token.CompileError) {
	// Read until we encounter a conversion specifier end char (like d, f, etc.)
	err = nil
	specRunes := []rune{runes[start]}
	for it := start + 1; it < len(runes); it++ {
		if runes[it] == '*' {
			err = &token.CompileError{
				Token: sl.Token,
				Msg:   fmt.Sprintf("Using * not allowed in format specifier (after the %% char). Instead use (-var) where var is an integer variable. Error str: %s", sl.Value),
			}
			c.Errors = append(c.Errors, err)
			it += 1
			return
		}

		if specIdAhead(runes, it) {
			// cfg already checks that the ')' is after the identifier
			// and identifier is in the symbol table
			specId, end := parseIdentifier(runes, it+2)
			specSym, _ := c.getIdSym(specId)
			specSyms = append(specSyms, specSym)
			specRunes = append(specRunes, '*')
			// it must go past the )
			// it does so in the loop it++
			it = end
			continue
		}

		specRunes = append(specRunes, runes[it])
		if formatSpecifierEnd(runes[it]) {
			endIndex = it + 1
			spec = string(specRunes)
			return
		}
	}

	err = &token.CompileError{
		Token: sl.Token,
		Msg:   fmt.Sprintf("Invalid format specifier string: Format specifier '%s' is incomplete. Str: %s", string(specRunes), sl.Value),
	}
	c.Errors = append(c.Errors, err)
	endIndex = len(runes)
	return
}

// Assumes runes[start] is a valid start to the identifier (lexer.IsLetter)
// end is the index after identifier in runes
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
// marker should be of type -mainId%(-id1).(-id2).(-id3)d/f/...
// It returns the parsed symbols from symbol table (using ids), the custom specifier (if any), and the new index.
// If the main identifier is not in the symbol table, the bool markerSymValid is false
// If subsequent identifiers within the specifier are not found in symbol table, it gives an error
func (c *Compiler) parseMarker(sl *ast.StringLiteral, runes []rune, i int) (mainId string, syms []*Symbol, customSpec string, newIndex int, markerSymValid bool, err *token.CompileError) {
	// Assumes runes[i] == '-' and that runes[i+1] is a valid identifier start (isLetter).
	markerSymValid = false
	err = nil
	specSyms := []*Symbol{}
	var end int
	var mainSym *Symbol
	mainId, end = parseIdentifier(runes, i+1)
	mainSym, markerSymValid = c.getIdSym(mainId)
	if !markerSymValid {
		return mainId, []*Symbol{}, "", end, false, nil
	}

	if hasSpecifier(runes, end) {
		if end+1 == len(runes) {
			err = &token.CompileError{
				Token: sl.Token,
				Msg:   fmt.Sprintf("Invalid format specifier string: Format specifier is incomplete. Str: %s", sl.Value),
			}
			c.Errors = append(c.Errors, err)
			newIndex = end + 1
			return
		}
		specStart := end
		specSyms, customSpec, end, err = c.parseSpecifier(sl, runes, specStart)
	}

	syms = append(syms, mainSym)
	syms = append(syms, specSyms...)
	newIndex = end
	return
}

func (c *Compiler) getIdSym(id string) (*Symbol, bool) {
	s, ok := Get(c.Scopes, id)
	if ok {
		return c.derefIfPointer(s), ok
	}

	cc := c.CodeCompiler.Compiler
	s, ok = Get(cc.Scopes, id)
	if ok {
		return c.derefIfPointer(s), ok
	}
	return s, ok
}

// gets the raw symbol WITHOUT deref if pointer
// in case it is a PointerKind will return alloca and Type should be PointerKind
func (c *Compiler) getRawSym(id string) (*Symbol, bool) {
	s, ok := Get(c.Scopes, id)
	if ok {
		return s, ok
	}

	cc := c.CodeCompiler.Compiler
	s, ok = Get(cc.Scopes, id)
	return s, ok
}

// assumes we have at least one identifier in ids. CustomSpec is printf specifier %...
// if mainSym.Type does not match required type from specifier end, it returns a compileError and adds it to c.Errors
func (c *Compiler) parseFormatting(sl *ast.StringLiteral, mainId string, syms []*Symbol, customSpec string) (formattedStr string, valArgs []llvm.Value, toFree []llvm.Value, err *token.CompileError) {
	var builder strings.Builder
	mainSym := syms[0]
	valArgs = []llvm.Value{}
	toFree = []llvm.Value{}
	// Use the custom specifier if provided; otherwise, use the default.
	for _, specSym := range syms[1:] {
		valArgs = append(valArgs, specSym.Val)
	}

	// if customSpec is either "" or %% it must be written later anyway. %% must be written as is.
	var spec string
	var err1 error
	if customSpec == "" || customSpec == "%%" {
		spec, err1 = defaultSpecifier(mainSym.Type)
		if err1 != nil {
			err = &token.CompileError{
				Token: sl.Token,
				Msg:   fmt.Sprintf("Error formatting string. String: %s. Err: %s", sl.Value, err),
			}
			c.Errors = append(c.Errors, err)
			return
		}
		builder.WriteString(spec)
	}
	// customSpec %% is written here
	builder.WriteString(customSpec)

	finalSpec := customSpec
	if spec != "" {
		finalSpec = spec
	}
	specRune := rune(finalSpec[len(finalSpec)-1])
	if specRune == 'p' {
		mainSym, _ = c.getRawSym(mainId)
	}
	mainType := mainSym.Type

	formattedStr = builder.String()
	if specRune == 'n' {
		s := c.promoteToMemory(mainId)
		valArgs = append(valArgs, s.Val)
		return
	}

	// If we're using %s with a FloatKind, convert the float to a char* via runtime
	if specRune == 's' {
		switch mainType.Kind() {
		case FloatKind:
			strPtr := c.floatStrArg(mainSym)
			valArgs = append(valArgs, strPtr)
			toFree = append(toFree, strPtr)
			return
		case RangeKind:
			strPtr := c.rangeStrArg(mainSym)
			valArgs = append(valArgs, strPtr)
			toFree = append(toFree, strPtr)
			return
		}
	}

	if specToKind[specRune] != mainType.Kind() {
		err = &token.CompileError{
			Token: sl.Token,
			Msg:   fmt.Sprintf("Format specifier end %q is not correct for variable type. Variable identifier: %s. Variable type: %s", specRune, mainId, mainType),
		}
		c.Errors = append(c.Errors, err)
		return
	}

	valArgs = append(valArgs, mainSym.Val)
	return
}

// formatString scans the string literal for markers of the form "-identifier".
// For each such marker, it looks up the identifier in the symbol table and replaces the marker
// with the appropriate conversion specifier. It returns the new format string along with a slice
// of llvm.Value for each variable found.
// it additionally supports specifiers %d, %f etc as defined by the printf function
// the * option for the width and precision should be replaced by their corresponding variables within parentheses and with the marker
// eg: -a%(-w)d prints the integer variable a with width given by the variable w
func (c *Compiler) formatString(sl *ast.StringLiteral) (string, []llvm.Value, []llvm.Value) {
	var builder strings.Builder
	var args []llvm.Value
	var toFree []llvm.Value

	// Convert the input to a slice of runes so we can properly iterate over Unicode characters.
	runes := []rune(sl.Value)
	i := 0
	for i < len(runes) {
		if !(maybeMarker(runes, i)) {
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
		mainId, syms, customSpec, newIndex, markerSymValid, err := c.parseMarker(sl, runes, i)
		if !markerSymValid {
			builder.WriteRune('-')
			builder.WriteString(mainId)
			i = newIndex
			continue
		}
		if err != nil {
			return "", args, nil
		}
		formattedStr, idArgs, idToFree, err := c.parseFormatting(sl, mainId, syms, customSpec)
		if err != nil {
			return "", args, nil
		}
		builder.WriteString(formattedStr)
		args = append(args, idArgs...)
		toFree = append(toFree, idToFree...)
		// Advance past the marker.
		i = newIndex
	}

	st := builder.String()
	return st, args, toFree
}

func maybeMarker(runes []rune, i int) bool {
	if i+1 < len(runes) && runes[i] == '-' && lexer.IsLetter(runes[i+1]) {
		return true
	}
	return false
}

func hasSpecifier(runes []rune, i int) bool {
	if i < len(runes) && runes[i] == '%' {
		return true
	}
	return false
}

func specIdAhead(runes []rune, i int) bool {
	return i+2 < len(runes) && runes[i] == '(' && runes[i+1] == '-' && lexer.IsLetter(runes[i+2])
}
