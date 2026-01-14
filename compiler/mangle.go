package compiler

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Mangling constants per Pluto C ABI Spec
const (
	PT  = "Pt" // Pluto symbol prefix
	P   = "p"  // Path marker (end of module path)
	R   = "r"  // Relpath end marker (for constants with relpath)
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
// Format: [MangledPath]_[Name]_f[N]_[Types...]
// mangledPath is pre-computed via MangleDirPath.
func Mangle(mangledPath, funcName string, args []Type) string {
	parts := []string{mangledPath, MangleIdent(funcName), F + strconv.Itoa(len(args))}
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
				parts = append(parts, MangleIdent(current.String()))
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
		parts = append(parts, MangleIdent(current.String()))
	}

	return strings.Join(parts, "_")
}

// MangleDirPath generates the mangled directory path prefix.
// Format: Pt_[ModPath]_p_[RelPath]_r (with relpath) or Pt_[ModPath]_p (without)
// Used for LLVM module naming and as base for symbol mangling.
func MangleDirPath(modName, relPath string) string {
	parts := []string{PT, ManglePath(modName), P}
	if relPath != "" {
		parts = append(parts, ManglePath(relPath), R)
	}
	return strings.Join(parts, SEP)
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

// MangleIdent converts an identifier or path segment to mangled form with Unicode support.
// Digit-starting: n<digits>[_<rest>] (e.g., "123" -> "n123", "2abc" -> "n2_3abc")
// ASCII-only: "<len><chars>" (e.g., "foo" -> "3foo")
// With Unicode: alternating ASCII and Unicode segments
//   - ASCII segment: <len><chars>
//   - Unicode segment: u<count>_<hex6><hex6>... (consecutive hex values after single _)
//   - After Unicode, if ASCII starts with digit: n<digits>[_<len><alpha>] to avoid ambiguity
//
// Example: "123" -> "n123"
// Example: "2abc" -> "n2_3abc"
// Example: "foo" -> "3foo"
// Example: "π" -> "u1_0003C0"
// Example: "foo_π" -> "4foo_u1_0003C0"
// Example: "aπb" -> "1au1_0003C01b"
// Example: "αβ" -> "u2_0003B10003B2"
// Example: "α2" -> "u1_0003B1n2" (digit after Unicode uses n-prefix)
// Example: "α2y" -> "u1_0003B1n2_1y" (mixed: digit-prefix + alpha)
func MangleIdent(ident string) string {
	if ident == "" {
		return ""
	}

	var result strings.Builder
	i := 0

	// Simple dispatch: at each position, route to ASCII or Unicode handler
	for i < len(ident) {
		if ident[i] < 128 {
			i = mangleASCIIOrDigits(&result, ident, i)
			continue
		}
		i = mangleUnicode(&result, ident, i)
	}

	return result.String()
}

// mangleASCIIOrDigits writes either n-prefixed digits or length-prefixed ASCII.
//
// For digit-starting segments after Unicode, we use n-prefix to avoid ambiguity.
// Without n-prefix, "α2" would mangle to "...12" where "12" is ambiguous
// (length=1 char='2', or length=12?). With n-prefix: "...n2" is unambiguous.
//
// The separator logic after n<digits>:
//   - Nothing if end of identifier (e.g., "α2" → "...n2")
//   - "_" if Unicode follows (e.g., "α2β" → "...n2_...", _ consumed before next Unicode)
//   - "_<len><chars>" if ASCII follows (e.g., "α2y" → "...n2_1y")
func mangleASCIIOrDigits(result *strings.Builder, s string, i int) int {
	hadDigits := false
	if s[i] >= '0' && s[i] <= '9' {
		i = mangleDigits(result, s, i)
		hadDigits = true
	}

	if i < len(s) {
		if hadDigits {
			result.WriteString(SEP)
		}
		if s[i] < 128 {
			i = mangleASCII(result, s, i)
		}
	}

	return i
}

// mangleDigits writes n<digits> and returns position after digits.
func mangleDigits(result *strings.Builder, s string, start int) int {
	i := start + 1
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	result.WriteString(N)
	result.WriteString(s[start:i])
	return i
}

// mangleASCII writes <len><chars> and returns new position.
func mangleASCII(result *strings.Builder, s string, start int) int {
	i := start
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= 128 {
			break
		}
		i += size
	}
	result.WriteString(strconv.Itoa(i - start))
	result.WriteString(s[start:i])
	return i
}

// mangleUnicode writes u<count>_<hex6>... and returns new position.
func mangleUnicode(result *strings.Builder, s string, start int) int {
	var runes []rune
	i := start
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r < 128 {
			break
		}
		runes = append(runes, r)
		i += size
	}
	if len(runes) > 0 {
		writeUnicodeSegment(result, runes)
	}
	return i
}

// writeUnicodeSegment writes a Unicode segment: u<count>_<hex6><hex6>...
// Each codepoint is encoded as exactly 6 uppercase hex digits.
func writeUnicodeSegment(w *strings.Builder, runes []rune) {
	w.WriteString("u")
	w.WriteString(strconv.Itoa(len(runes)))
	w.WriteString("_")
	for _, r := range runes {
		fmt.Fprintf(w, "%06X", r)
	}
}

// MangleConst generates C ABI-compliant constant name per Pluto C ABI Spec.
// Format: [MangledPath]_[Name]
// mangledPath is pre-computed via MangleDirPath (includes _r suffix when relpath present).
func MangleConst(mangledPath, constName string) string {
	return mangledPath + SEP + MangleIdent(constName)
}

// Demangle converts a mangled symbol back to human-readable form.
// Example: "Pt_6github_d_3com_s_4user_s_4math_p_6Square_f1_I64" -> "github.com/user/math.Square(I64)"
// For malformed Pluto symbols, returns the error message.
func Demangle(mangled string) string {
	d, err := DemangleParsed(mangled)
	if err != nil {
		return fmt.Sprintf("<error: %s>", err)
	}
	return d.String()
}

// DemangleParsed converts a mangled symbol to a structured Demangled.
//
// Symbol structure (all symbols have _p_ after ModPath, _r_ marks end of relpath):
//   - Function (no relpath): Pt_ModPath_p_Name_fN[_Type]*
//   - Function (with relpath): Pt_ModPath_p_RelPath_r_Name_fN[_Type]*
//   - Constant (no relpath): Pt_ModPath_p_Name
//   - Constant (with relpath): Pt_ModPath_p_RelPath_r_Name
//
// Flow after _p_:
//  1. Parse path (could be relpath or name)
//  2. If _r_ follows: what we parsed was relpath, parse ident for name
//  3. If _f<digit> follows: it's a function, parse arity and types
//  4. Otherwise: it's a constant
//
// Returns error if symbol starts with Pt_ but is malformed (missing _p_ marker).
// Non-Pluto symbols (no Pt_ prefix) pass through without error.
func DemangleParsed(mangled string) (*Demangled, error) {
	prefix := PT + SEP
	if !strings.HasPrefix(mangled, prefix) {
		return &Demangled{Name: mangled, Kind: SymbolConst}, nil
	}

	rest := mangled[len(prefix):]
	result := &Demangled{Kind: SymbolConst}

	// Parse module path
	result.ModPath, rest = demanglePath(rest)

	// All symbols have _p_ after ModPath
	pMarker := SEP + P + SEP
	if !strings.HasPrefix(rest, pMarker) {
		return nil, fmt.Errorf("malformed Pluto symbol: missing _p_ marker in %q", mangled)
	}
	rest = rest[len(pMarker):]

	// Parse first path segment (could be relpath or name)
	firstPath, rest := demanglePath(rest)

	// Check for _r_ marker (relpath present)
	rMarker := SEP + R + SEP
	if strings.HasPrefix(rest, rMarker) {
		result.RelPath = firstPath
		rest = rest[len(rMarker):]
		result.Name, rest = demangleIdent(rest)
	} else {
		result.Name = firstPath
	}

	// Parse function signature if present
	demangleFunc(result, rest)

	return result, nil
}

// demangleFunc parses function signature: _fN[_Type]* and populates result.
// Sets Kind to SymbolFunc and parses arity and argument types.
func demangleFunc(result *Demangled, rest string) {
	fMarker := SEP + F
	if !strings.HasPrefix(rest, fMarker) {
		return
	}

	after := rest[len(fMarker):]
	if len(after) == 0 || after[0] < '0' || after[0] > '9' {
		return
	}

	result.Kind = SymbolFunc
	result.Arity, rest = parseArity(after)

	// Parse argument types
	for strings.HasPrefix(rest, SEP) {
		rest = rest[len(SEP):]
		typeName, remaining := demangleType(rest)
		if typeName == "" {
			break
		}
		result.ArgTypes = append(result.ArgTypes, typeName)
		rest = remaining
	}
}

// parseArity parses arity digits from s.
func parseArity(s string) (arity int, remaining string) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	arity, _ = strconv.Atoi(s[:i])
	return arity, s[i:]
}

// demanglePath converts a mangled path back to original form.
// Returns the demangled path and the remaining string.
//
// Path structure: [separator*] ident (_separator_ ident)*
// - Separator codes: d=., s=/, h=-
// - Path ends when we see Sep followed by non-separator (function name, _p_, _f, etc.)
//
// Example: "6github_d_3com_s_4user" -> "github.com/user"
// Example: "s_3foo" -> "/foo" (leading separator)
func demanglePath(s string) (string, string) {
	var result strings.Builder
	rest := s

	// Parse first segment (may have leading separators like "/foo")
	rest = demangleSepsAndSegment(&result, rest)

	// Parse subsequent segments: _separators_segment
	for strings.HasPrefix(rest, SEP) {
		afterSep := rest[len(SEP):]
		remaining := demangleSepsAndSegment(&result, afterSep)
		if remaining == afterSep {
			// Nothing consumed - path ends here (hit _p_, _f, etc.)
			break
		}
		rest = remaining
	}

	return result.String(), rest
}

// demangleSepsAndSegment consumes separator codes (d/s/h), writes corresponding chars,
// then parses the following segment. Returns s unchanged if nothing can be parsed.
func demangleSepsAndSegment(result *strings.Builder, s string) string {
	// Consume separator codes (d, s, h) and write chars (., /, -)
	i := 0
	for i < len(s) {
		sepChar := demangleSeparator(rune(s[i]))
		if sepChar == 0 {
			break
		}
		result.WriteRune(sepChar)
		i++
	}

	// Trim underscore after separators, then parse segment
	rest := s[i:]
	if i > 0 {
		rest = strings.TrimPrefix(rest, SEP)
	}
	return demanglePathSegment(result, rest)
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

// consumeDigits reads consecutive digits from s, writes them to result, and returns the rest.
func consumeDigits(result *strings.Builder, s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	result.WriteString(s[:i])
	return s[i:]
}

// demangleNumericSegment handles n<digits>[_<len><alpha>] patterns.
// rest should start after the 'n' prefix (i.e., starts with digits).
func demangleNumericSegment(result *strings.Builder, rest string) string {
	rest = consumeDigits(result, rest)

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

// demangleIdent extracts an identifier with Unicode support.
// ASCII-only: <len><chars>
// Unicode-only: u<count>_<hex6>...
// Mixed: alternating ASCII and Unicode segments
//   - ASCII segment: <len><chars>
//   - Unicode segment: u<count>_<hex6><hex6>... (consecutive hex values after single _)
//   - After Unicode, digit-starting ASCII uses: n<digits>[_<len><alpha>]
//
// Parsing algorithm:
//  1. If starts with `u` + digit, parse Unicode segment first
//  2. If starts with digit, parse ASCII segment
//  3. After any segment, check for more Unicode or ASCII
//  4. Identifier is complete when neither follows
func demangleIdent(s string) (string, string) {
	if len(s) == 0 {
		return "", s
	}

	var result strings.Builder
	rest := s

	// Parse segments in a loop: Unicode, n-prefixed numeric, or length-prefixed ASCII
	for len(rest) > 0 {
		switch {
		case rest[0] == 'u' && len(rest) > 1 && rest[1] >= '1' && rest[1] <= '9':
			// Unicode segment
			runes, remaining, ok := parseUnicodeSegment(rest)
			if !ok {
				return result.String(), rest
			}
			for _, r := range runes {
				result.WriteRune(r)
			}
			rest = remaining

		case rest[0] == 'n' && len(rest) > 1 && rest[1] >= '0' && rest[1] <= '9':
			// n-prefixed numeric (e.g., "123" -> "n123")
			rest = consumeDigits(&result, rest[1:])

		case rest[0] >= '0' && rest[0] <= '9':
			// Length-prefixed ASCII
			ascii, remaining, ok := parseASCIISegment(rest)
			if !ok {
				return result.String(), rest
			}
			result.WriteString(ascii)
			rest = remaining

		case rest[0] == '_' && len(rest) > 1 && isIdentContinuation(rest[1:]):
			// Separator within identifier (after n-prefix, before ASCII/Unicode)
			rest = rest[1:]

		default:
			// Unrecognized character - if nothing parsed yet, return failure
			if result.Len() == 0 {
				return "", s
			}
			return result.String(), rest
		}
	}

	return result.String(), rest
}

// isIdentContinuation checks if s starts with a valid identifier segment.
func isIdentContinuation(s string) bool {
	if len(s) == 0 {
		return false
	}
	// Digit: ASCII segment follows
	if s[0] >= '0' && s[0] <= '9' {
		return true
	}
	// Unicode segment follows
	if s[0] == 'u' && len(s) > 1 && s[1] >= '1' && s[1] <= '9' {
		return true
	}
	return false
}

// parseASCIISegment parses <len><chars> and returns the chars.
func parseASCIISegment(s string) (ascii, remaining string, ok bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return "", s, false
	}

	length, _ := strconv.Atoi(s[:i])
	s = s[i:]

	if length > len(s) {
		return "", s, false
	}
	return s[:length], s[length:], true
}

// parseUnicodeSegment parses u<count>_<hex6><hex6>... and returns the runes.
// Format: u<count>_<consecutive hex values> where each hex value is exactly 6 chars.
// Caller must verify s starts with 'u' followed by digit 1-9.
func parseUnicodeSegment(s string) (runes []rune, remaining string, ok bool) {
	// Find where count digits end (start at 1, skip 'u')
	j := 1
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j >= len(s) || s[j] != '_' {
		return nil, s, false
	}

	count, _ := strconv.Atoi(s[1:j])
	s = s[j+1:] // Skip past the underscore

	// Read count consecutive hex values (each 6 chars)
	needed := count * 6
	if len(s) < needed {
		return nil, s, false
	}
	for i := range count {
		codepoint, err := strconv.ParseInt(s[i*6:i*6+6], 16, 32)
		if err != nil {
			return nil, s, false
		}
		runes = append(runes, rune(codepoint))
	}

	return runes, s[needed:], true
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
