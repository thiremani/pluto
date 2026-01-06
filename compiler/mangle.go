package compiler

import (
	"fmt"
	"strconv"
	"strings"
)

// Mangling constants per Pluto C ABI Spec
const (
	PT  = "Pt" // Pluto symbol prefix
	P   = "p"  // Path marker (relpath follows)
	F   = "f"  // Function arity marker
	T   = "t"  // Generic type params marker
	M   = "m"  // Method separator
	OP  = "op" // Operator prefix
	N   = "n"  // Numeric segment prefix
	SEP = "_"  // General separator
)

// Path separator characters
const (
	DOT  = '.'
	SL   = '/'
	HYPH = '-'
)

// Mangled separator codes
const (
	DOT_CODE  = 'd'
	SL_CODE   = 's'
	HYPH_CODE = 'h'
)

// SymbolKind identifies the type of mangled symbol.
type SymbolKind int

const (
	SymbolConst    SymbolKind = iota // Constant value
	SymbolType                       // Type metadata (future)
	SymbolFunc                       // Standalone function
	SymbolMethod                     // Method on a type (future)
	SymbolOperator                   // Operator overload (future)
)

// Demangled holds the parsed components of a mangled symbol.
type Demangled struct {
	ModPath  string     // Module path (e.g., "github.com/user/math")
	RelPath  string     // Relative subdirectory path (e.g., "stats/integral")
	Name     string     // Symbol name (function or constant name)
	Kind     SymbolKind // Type of symbol
	Arity    int        // Number of arguments (for functions)
	ArgTypes []string   // Argument type names (for functions)
}

// FullPath returns the complete path (ModPath + RelPath).
func (d *Demangled) FullPath() string {
	if d.RelPath == "" {
		return d.ModPath
	}
	return d.ModPath + "/" + d.RelPath
}

// String returns a human-readable representation.
func (d *Demangled) String() string {
	// If no ModPath, this is not a Pluto symbol - return name as-is
	if d.ModPath == "" {
		return d.Name
	}

	var result strings.Builder
	result.WriteString(d.FullPath())
	result.WriteString(".")
	result.WriteString(d.Name)

	if d.Kind == SymbolFunc {
		result.WriteString("(")
		result.WriteString(strings.Join(d.ArgTypes, ", "))
		result.WriteString(")")
	}
	return result.String()
}

// Mangle generates C ABI-compliant function name per Pluto C ABI Spec.
// Format: Pt_[ModPath][_p_RelPath]_[Name]_f[N]_[Types...]
// where ModPath and RelPath are mangled per ManglePath, Name per MangleIdent.
// The _p_ marker indicates a relative path follows (implies / separator).
func Mangle(modName, relPath, funcName string, args []Type) string {
	parts := []string{PT, ManglePath(modName)}
	if relPath != "" {
		parts = append(parts, P, ManglePath(relPath))
	}
	parts = append(parts, MangleIdent(funcName))
	parts = append(parts, F+strconv.Itoa(len(args)))
	for _, arg := range args {
		parts = append(parts, arg.Mangle())
	}
	return strings.Join(parts, SEP)
}

// ManglePath converts a module path to its mangled form per Pluto C ABI Spec.
// Separators: . -> d, / -> s, - -> h
// Identifiers are length-prefixed.
// Numeric segments use n prefix.
// Example: "github.com/user/math" -> "6github_d_3com_s_4user_s_4math"
func ManglePath(path string) string {
	var parts []string
	var current strings.Builder
	var seps strings.Builder

	for _, r := range path {
		// Handle separators: flush current identifier, accumulate separator codes
		if sep := separatorCode(r); sep != 0 {
			if current.Len() > 0 {
				parts = append(parts, mangleSegment(current.String()))
				current.Reset()
			}
			seps.WriteRune(sep)
			continue
		}

		// Regular character: flush pending separators, then accumulate
		if seps.Len() > 0 {
			parts = append(parts, seps.String())
			seps.Reset()
		}
		current.WriteRune(r)
	}

	// Flush final identifier
	if current.Len() > 0 {
		parts = append(parts, mangleSegment(current.String()))
	}

	return strings.Join(parts, "_")
}

// separatorCode returns the mangled separator code, or 0 if not a separator.
func separatorCode(r rune) rune {
	switch r {
	case DOT:
		return DOT_CODE
	case SL:
		return SL_CODE
	case HYPH:
		return HYPH_CODE
	default:
		return 0
	}
}

// mangleSegment mangles a path segment:
// - Pure numeric: n<digits>
// - Pure identifier: <len><payload>
// - Mixed (digit-prefix): n<digits>_<len><alpha>
func mangleSegment(s string) string {
	if s[0] < '0' || s[0] > '9' {
		return MangleIdent(s)
	}
	// Find where leading digits end
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == len(s) {
		return N + s // Pure numeric
	}
	return N + s[:i] + SEP + MangleIdent(s[i:]) // Mixed: digit-prefix
}

// MangleIdent converts an identifier to length-prefixed form.
// Example: "foo" -> "3foo"
func MangleIdent(ident string) string {
	return fmt.Sprintf("%d%s", len(ident), ident)
}

// Demangle converts a mangled symbol back to human-readable form.
// Example: "Pt_6github_d_3com_s_4user_s_4math_6Square_f1_I64" -> "github.com/user/math.Square(I64)"
func Demangle(mangled string) string {
	return DemangleParsed(mangled).String()
}

// DemangleParsed converts a mangled symbol to a structured Demangled.
//
// Symbol structure:
//   - Function: Pt_ModPath[_p_RelPath]_Name_fN[_Type]*
//   - Constant: Pt_ModPath[_p_RelPath]_Name (no _f marker)
//
// Path disambiguation:
//   - Path segments are connected by separator codes (d=., s=/, h=-)
//   - When we see sep followed by a digit (length-prefixed ident), path ends
//   - _p_ marker indicates relative path follows (implies / separator)
func DemangleParsed(mangled string) *Demangled {
	prefix := PT + SEP
	if !strings.HasPrefix(mangled, prefix) {
		return &Demangled{Name: mangled, Kind: SymbolConst}
	}

	rest := mangled[len(prefix):]
	result := &Demangled{Kind: SymbolConst} // Default to constant (no _f marker)

	// Parse module path (segments connected by separator codes)
	result.ModPath, rest = demanglePath(rest)

	// Check for relative path marker (_p_)
	pMarker := SEP + P + SEP
	if strings.HasPrefix(rest, pMarker) {
		rest = rest[len(pMarker):]
		result.RelPath, rest = demanglePath(rest)
	}

	// Parse symbol name (after SEP)
	rest = strings.TrimPrefix(rest, SEP)
	result.Name, rest = demangleIdent(rest)

	// Parse function arity marker (_fN) - if present, it's a function
	fMarker := SEP + F
	if strings.HasPrefix(rest, fMarker) {
		result.Kind = SymbolFunc
		rest = rest[len(fMarker):]
		// Parse arity number
		arityStr := ""
		for len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
			arityStr += string(rest[0])
			rest = rest[1:]
		}
		if arityStr != "" {
			result.Arity, _ = strconv.Atoi(arityStr)
		}
	}

	// Parse argument types (only for functions)
	for strings.HasPrefix(rest, SEP) {
		rest = rest[len(SEP):]
		typeName, remaining := demangleType(rest)
		if typeName == "" {
			break
		}
		result.ArgTypes = append(result.ArgTypes, typeName)
		rest = remaining
	}

	return result
}

// demanglePath converts a mangled path back to original form.
// Returns the demangled path and the remaining string.
//
// Path structure: ident (_separator_ ident)*
// - Separator codes: d=., s=/, h=-
// - Path ends when we see Sep followed by non-separator (function name, _p_, _f, etc.)
//
// Example: "6github_d_3com_s_4user" -> "github.com/user"
func demanglePath(s string) (string, string) {
	var result strings.Builder
	rest := s

	// Parse first segment
	rest = demanglePathSegment(&result, rest)

	// Parse subsequent segments: _separators_segment
	for strings.HasPrefix(rest, SEP) {
		afterSep := rest[len(SEP):]

		// Consume all consecutive separator codes (d, s, h)
		i := 0
		for i < len(afterSep) {
			sepChar := demangleSeparator(rune(afterSep[i]))
			if sepChar == 0 {
				break
			}
			result.WriteRune(sepChar)
			i++
		}
		if i == 0 {
			// No separator code - path ends here (function name, marker, etc.)
			break
		}

		rest = afterSep[i:]
		rest = strings.TrimPrefix(rest, SEP)
		rest = demanglePathSegment(&result, rest)
	}

	return result.String(), rest
}

// demanglePathSegment parses a single path segment (identifier or numeric).
func demanglePathSegment(result *strings.Builder, rest string) string {
	// Numeric segment: n<digits>[_<len><alpha>]
	if len(rest) > 1 && rest[0] == 'n' && rest[1] >= '0' && rest[1] <= '9' {
		return demangleNumericSegment(result, rest[1:])
	}

	// Identifier: length-prefixed (e.g., 4math)
	// demangleIdent handles invalid cases (non-digit, '0' prefix) by returning ""
	ident, remaining := demangleIdent(rest)
	if ident != "" {
		result.WriteString(ident)
		return remaining
	}

	return rest
}

// demangleNumericSegment handles n<digits>[_<len><alpha>] patterns.
// rest should start after the 'n' prefix (i.e., starts with digits).
func demangleNumericSegment(result *strings.Builder, rest string) string {
	// Consume digits
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	result.WriteString(rest[:i])
	rest = rest[i:]

	// Check for mixed segment continuation: _<len><alpha>
	if !strings.HasPrefix(rest, SEP) {
		return rest
	}
	ident, remaining := demangleIdent(rest[len(SEP):])
	if ident == "" {
		return rest
	}
	result.WriteString(ident)
	return remaining
}

// demangleIdent extracts a length-prefixed identifier.
// Callers must verify s starts with a digit 1-9 before calling.
func demangleIdent(s string) (string, string) {
	// Parse length prefix
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}

	length, _ := strconv.Atoi(s[:i])
	if i+length > len(s) {
		return "", s // Truncated input
	}

	return s[i : i+length], s[i+length:]
}

// demangleType extracts a type string, passing it through as-is.
// Handles primitives directly, and compound types by consuming N sub-types.
func demangleType(s string) (string, string) {
	// Check primitives
	for _, p := range PrimitiveTypeNames {
		if strings.HasPrefix(s, p) {
			return p, s[len(p):]
		}
	}
	// Check compound types
	if typeName, rest, ok := demangleCompoundType(s); ok {
		return typeName, rest
	}
	return "", s
}

// demangleCompoundType handles types with _tN_[types...] structure.
func demangleCompoundType(s string) (string, string, bool) {
	tMarker := SEP + T
	for _, prefix := range CompoundTypePrefixes {
		full := prefix + tMarker
		if !strings.HasPrefix(s, full) {
			continue
		}
		rest := s[len(full):]
		count, countLen := parseTypeCount(rest)
		if countLen == 0 {
			continue
		}
		consumed := len(full) + countLen
		consumed += consumeSubTypes(rest[countLen:], count)
		return s[:consumed], s[consumed:], true
	}
	return "", s, false
}

// parseTypeCount extracts the digit count after _t prefix.
func parseTypeCount(s string) (count, length int) {
	for length < len(s) && s[length] >= '0' && s[length] <= '9' {
		length++
	}
	if length > 0 {
		count, _ = strconv.Atoi(s[:length])
	}
	return
}

// consumeSubTypes consumes N sub-types separated by SEP, returns total bytes consumed.
func consumeSubTypes(s string, count int) int {
	consumed := 0
	for i := 0; i < count && strings.HasPrefix(s, SEP); i++ {
		s = s[len(SEP):]
		subType, remaining := demangleType(s)
		if subType == "" {
			break
		}
		consumed += len(SEP) + len(subType)
		s = remaining
	}
	return consumed
}

// demangleSeparator converts separator code back to original character.
func demangleSeparator(code rune) rune {
	switch code {
	case DOT_CODE:
		return DOT
	case SL_CODE:
		return SL
	case HYPH_CODE:
		return HYPH
	default:
		return 0
	}
}
