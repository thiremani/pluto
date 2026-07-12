package compiler

import (
	"fmt"
	"strings"

	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

func formatSpecifierEnd(ch rune) bool {
	switch ch {
	case 'd', 'i', 'u', 'o', 'x', 'X', 'f', 'F', 'e', 'E', 'g', 'G', 'a', 'A', 'c', 's', 'q', 'p', 'n', '%':
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
	'A': FloatKind,
	'c': IntKind, // maybe character code
	's': StrKind,
	'p': PtrKind, // pointer kind
	'n': IntKind, // byte‐count pointer
}

// defaultSpecifier returns the printf conversion specifier for a given type.
func defaultSpecifier(t Type) (string, error) {
	switch t.Kind() {
	case IntKind:
		return "%lld", nil
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
	case ArrayRangeKind:
		return "%s", nil
	case StructKind:
		return "%s", nil
	default:
		err := fmt.Errorf("unsupported type in print statement %s", t.String())
		return "", err
	}
}

func writeFormatText(builder *strings.Builder, ch rune) {
	builder.WriteRune(ch)
	if ch == '%' {
		builder.WriteRune('%')
	}
}

func writeLiteralFormatText(builder *strings.Builder, runes []rune) {
	for i := 0; i < len(runes); {
		if runes[i] == '\\' {
			decoded, next, _ := lexer.DecodeStringEscape(runes, i)
			writeFormatText(builder, decoded)
			i = next
			continue
		}
		writeFormatText(builder, runes[i])
		i++
	}
}

func formatSpecifierFlag(ch rune) bool {
	switch ch {
	case '-', '+', '#', '0':
		return true
	}
	return false
}

func formatSpecifierLengthStart(ch rune) bool {
	return strings.ContainsRune("lhLjzt", ch)
}

func scanDynamicSpecifierID(runes []rune, start int) (id string, next int, complete bool) {
	if start < 0 || start >= len(runes) {
		return "", start, false
	}
	next = start + 1
	if start+2 >= len(runes) || runes[start] != '(' || runes[start+1] != '-' || !lexer.IsLetter(runes[start+2]) {
		return
	}

	id, next = parseIdentifier(runes, start+2)
	if next >= len(runes) || runes[next] != ')' {
		return id, next, false
	}
	return id, next + 1, true
}

func formatSpecifierStart(runes []rune, start int) bool {
	if start < 0 || start >= len(runes) {
		return false
	}
	if runes[start] == '(' {
		_, _, complete := scanDynamicSpecifierID(runes, start)
		return complete
	}
	ch := runes[start]
	return formatSpecifierEnd(ch) || formatSpecifierFlag(ch) ||
		('1' <= ch && ch <= '9') || ch == '.' || ch == '*' || formatSpecifierLengthStart(ch)
}

func formatSpecifierError(tok token.Token, value, msg string) *token.CompileError {
	return &token.CompileError{
		Token: tok,
		Msg:   fmt.Sprintf("Invalid format specifier string: %s. Str: %s", msg, value),
	}
}

func validateSpecifierModifiers(tok token.Token, value, flags, length string, hasWidth, hasPrecision bool, conversion rune) *token.CompileError {
	allowedFlags := ""
	switch conversion {
	case 'd', 'i':
		allowedFlags = "-+0"
	case 'u':
		allowedFlags = "-0"
	case 'o', 'x', 'X':
		allowedFlags = "-#0"
	case 'f', 'F', 'e', 'E', 'g', 'G', 'a', 'A':
		allowedFlags = "-+#0"
	case 'c', 's', 'q':
		allowedFlags = "-"
	}

	seenFlags := make(map[rune]struct{}, len(flags))
	for _, flag := range flags {
		if _, exists := seenFlags[flag]; exists {
			return formatSpecifierError(tok, value, fmt.Sprintf("Duplicate format flag %q", flag))
		}
		seenFlags[flag] = struct{}{}
		if !strings.ContainsRune(allowedFlags, flag) {
			return formatSpecifierError(tok, value, fmt.Sprintf("Format flag %q is not supported for %%%c", flag, conversion))
		}
	}

	if length != "" && !strings.ContainsRune("diuoxXn", conversion) {
		return formatSpecifierError(tok, value, fmt.Sprintf("Length modifier %q is not supported for %%%c", length, conversion))
	}
	if hasWidth && strings.ContainsRune("pn%", conversion) {
		return formatSpecifierError(tok, value, fmt.Sprintf("Width is not supported for %%%c", conversion))
	}
	if hasPrecision && strings.ContainsRune("cpn%", conversion) {
		return formatSpecifierError(tok, value, fmt.Sprintf("Precision is not supported for %%%c", conversion))
	}
	return nil
}

// parseSpecifierSyntax probes the raw source at start. matched is false when
// '%' begins literal text; once a supported prefix is recognized, malformed
// syntax is reported rather than silently passed to printf. Dynamic width and
// precision identifiers use (-identifier), which is returned as '*'.
func parseSpecifierSyntax(tok token.Token, value string, runes []rune, start int) (specIDs []string, spec string, endIndex int, matched bool, err *token.CompileError) {
	endIndex = start
	if start+1 >= len(runes) || !formatSpecifierStart(runes, start+1) {
		return
	}

	matched = true
	it := start + 1
	if runes[it] == '%' {
		return nil, "%%", it + 1, true, nil
	}

	var specBuilder strings.Builder
	specBuilder.WriteRune('%')
	var flagsBuilder strings.Builder
	for it < len(runes) && formatSpecifierFlag(runes[it]) {
		flagsBuilder.WriteRune(runes[it])
		specBuilder.WriteRune(runes[it])
		it++
	}

	if it < len(runes) && runes[it] == '*' {
		err = &token.CompileError{
			Token: tok,
			Msg:   fmt.Sprintf("Using * not allowed in format specifier (after the %% char). Instead use (-var) where var is an integer variable. Error str: %s", value),
		}
		endIndex = it + 1
		return
	}

	hasWidth := false
	if it < len(runes) && '0' <= runes[it] && runes[it] <= '9' {
		hasWidth = true
		for it < len(runes) && '0' <= runes[it] && runes[it] <= '9' {
			specBuilder.WriteRune(runes[it])
			it++
		}
	} else if it < len(runes) && runes[it] == '(' {
		specID, next, complete := scanDynamicSpecifierID(runes, it)
		if complete {
			hasWidth = true
			specIDs = append(specIDs, specID)
			it = next
			specBuilder.WriteRune('*')
		}
	}

	hasPrecision := false
	if it < len(runes) && runes[it] == '.' {
		hasPrecision = true
		specBuilder.WriteRune('.')
		it++
		if it < len(runes) && runes[it] == '*' {
			err = &token.CompileError{
				Token: tok,
				Msg:   fmt.Sprintf("Using * not allowed in format specifier (after the %% char). Instead use (-var) where var is an integer variable. Error str: %s", value),
			}
			endIndex = it + 1
			return
		}
		if it < len(runes) && runes[it] == '(' {
			specID, next, complete := scanDynamicSpecifierID(runes, it)
			if complete {
				specIDs = append(specIDs, specID)
				it = next
				specBuilder.WriteRune('*')
			}
		} else {
			for it < len(runes) && '0' <= runes[it] && runes[it] <= '9' {
				specBuilder.WriteRune(runes[it])
				it++
			}
		}
	}

	length := ""
	if it < len(runes) && runes[it] == 'l' {
		length = "l"
		specBuilder.WriteRune('l')
		it++
		if it < len(runes) && runes[it] == 'l' {
			length = "ll"
			specBuilder.WriteRune('l')
			it++
		}
	} else if it < len(runes) && formatSpecifierLengthStart(runes[it]) {
		err = formatSpecifierError(tok, value, fmt.Sprintf("Length modifier %q is not supported", runes[it]))
		endIndex = it + 1
		return
	}

	if it >= len(runes) {
		err = formatSpecifierError(tok, value, fmt.Sprintf("Format specifier %q is incomplete", specBuilder.String()))
		endIndex = it
		return
	}
	if runes[it] == '\\' {
		err = formatSpecifierError(tok, value, "Escape sequences cannot be used as format syntax")
		endIndex = it
		return
	}
	conversion := runes[it]
	if !formatSpecifierEnd(conversion) {
		err = formatSpecifierError(tok, value, fmt.Sprintf("Unexpected %q in format specifier %q", conversion, specBuilder.String()))
		endIndex = it + 1
		return
	}

	specBuilder.WriteRune(conversion)
	if modifierErr := validateSpecifierModifiers(tok, value, flagsBuilder.String(), length, hasWidth, hasPrecision, conversion); modifierErr != nil {
		err = modifierErr
		endIndex = it + 1
		return
	}
	return specIDs, specBuilder.String(), it + 1, true, nil
}

func unresolvedMarkerEnd(value string, runes []rune, identifierEnd int) int {
	if identifierEnd >= len(runes) || runes[identifierEnd] != '%' {
		return identifierEnd
	}
	// An unresolved main marker is literal, so probe only to keep its attached
	// pseudo-specifier together; syntax errors from that probe are not reported.
	_, _, specifierEnd, matched, _ := parseSpecifierSyntax(token.Token{}, value, runes, identifierEnd)
	if matched && specifierEnd > identifierEnd {
		return specifierEnd
	}
	return identifierEnd
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

type parsedMarker struct {
	mainID        string
	symbols       []*Symbol
	customSpec    string
	literalSuffix []rune
	end           int
	mainResolved  bool
}

// parseMarker resolves a marker and any attached custom specifier. A specifier
// containing an unresolved dynamic identifier is retained as one literal span.
func (c *Compiler) parseMarker(tok token.Token, value string, runes []rune, i int) (parsedMarker, *token.CompileError) {
	// Assumes runes[i] == '-' and that runes[i+1] is a valid identifier start (isLetter).
	mainID, end := parseIdentifier(runes, i+1)
	mainSym, found := c.getIdSym(mainID)
	result := parsedMarker{
		mainID:       mainID,
		end:          end,
		mainResolved: found,
	}
	if !found {
		result.end = unresolvedMarkerEnd(value, runes, end)
		return result, nil
	}
	result.symbols = []*Symbol{mainSym}

	if end < len(runes) && runes[end] == '%' {
		specStart := end
		specIDs, customSpec, specEnd, matched, err := parseSpecifierSyntax(tok, value, runes, specStart)
		if !matched {
			return result, nil
		}

		for _, specID := range specIDs {
			_, ok := c.getRawSymbol(specID)
			if !ok {
				result.literalSuffix = runes[specStart:specEnd]
				result.end = specEnd
				return result, nil
			}
		}

		if err != nil {
			c.Errors = append(c.Errors, err)
			result.end = specEnd
			return result, err
		}
		for _, specID := range specIDs {
			specSym, _ := c.getIdSym(specID)
			if !TypeEqual(specSym.Type, I64) {
				err := &token.CompileError{
					Token: tok,
					Msg:   fmt.Sprintf("Format specifier variable %s must have type I64, got %s", specID, specSym.Type),
				}
				c.Errors = append(c.Errors, err)
				result.end = specEnd
				return result, err
			}
			result.symbols = append(result.symbols, specSym)
		}
		result.customSpec = customSpec
		result.end = specEnd
	}

	return result, nil
}

func (c *Compiler) getIdSym(id string) (*Symbol, bool) {
	return c.namedValueSymbol(id, id+"_load")
}

func formatSpecifierTypeError(tok token.Token, specRune rune, mainID string, mainType Type) *token.CompileError {
	return &token.CompileError{
		Token: tok,
		Msg:   fmt.Sprintf("Format specifier end %q is not correct for variable type. Variable identifier: %s. Variable type: %s", specRune, mainID, mainType),
	}
}

func (c *Compiler) formatCIntArg(sym *Symbol, name string) llvm.Value {
	intType := sym.Type.(Int)
	switch {
	case intType.Width > 32:
		return c.builder.CreateTrunc(sym.Val, c.Context.Int32Type(), name)
	case intType.Width < 32:
		return c.builder.CreateSExt(sym.Val, c.Context.Int32Type(), name)
	default:
		return sym.Val
	}
}

// assumes we have at least one identifier in ids. CustomSpec is printf specifier %...
// if mainSym.Type does not match required type from specifier end, it returns a compileError and adds it to c.Errors
func (c *Compiler) parseFormatting(tok token.Token, value string, mainId string, syms []*Symbol, customSpec string) (formattedStr string, valArgs []llvm.Value, toFree []llvm.Value, err *token.CompileError) {
	var builder strings.Builder
	mainSym := syms[0]
	valArgs = []llvm.Value{}
	toFree = []llvm.Value{}
	// Use the custom specifier if provided; otherwise, use the default.
	for _, specSym := range syms[1:] {
		valArgs = append(valArgs, c.formatCIntArg(specSym, "format_size_i32"))
	}

	// if customSpec is either "" or %% it must be written later anyway. %% must be written as is.
	var spec string
	var err1 error
	if customSpec == "" || customSpec == "%%" {
		spec, err1 = defaultSpecifier(mainSym.Type)
		if err1 != nil {
			err = &token.CompileError{
				Token: tok,
				Msg:   fmt.Sprintf("Error formatting string. String: %s. Err: %s", value, err),
			}
			c.Errors = append(c.Errors, err)
			return
		}
		builder.WriteString(spec)
	}
	// customSpec %% is written here
	// any other customSpec we replce %d -> %lld etc
	customSpec = upgradeIntSpec(customSpec)
	builder.WriteString(customSpec)

	finalSpec := customSpec
	if spec != "" {
		finalSpec = spec
	}
	specRune := rune(finalSpec[len(finalSpec)-1])
	if specRune == 'p' {
		mainSym, _ = c.getRawSymbol(mainId)
	}
	mainType := mainSym.Type

	formattedStr = builder.String()
	if specRune == 'n' {
		if !TypeEqual(mainType, I64) {
			err = formatSpecifierTypeError(tok, specRune, mainId, mainType)
			c.Errors = append(c.Errors, err)
			return
		}
		s := c.promoteToMemory(mainId)
		valArgs = append(valArgs, s.Val)
		return
	}
	if specRune == 'q' {
		if mainType.Kind() != StrKind {
			err = formatSpecifierTypeError(tok, specRune, mainId, mainType)
			c.Errors = append(c.Errors, err)
			return
		}
		if strings.Contains(customSpec, ".") {
			err = &token.CompileError{
				Token: tok,
				Msg:   fmt.Sprintf("Precision is not supported for %%q because it can truncate the quoted string. Variable identifier: %s", mainId),
			}
			c.Errors = append(c.Errors, err)
			return
		}
		quoted := c.quotedStrArg(mainSym)
		// Only a custom specifier can end in q; libc receives the same specifier ending in s.
		formattedStr = formattedStr[:len(formattedStr)-1] + "s"
		valArgs = append(valArgs, quoted)
		toFree = append(toFree, quoted)
		return
	}

	// If we're using %s, convert non-string types to char* via runtime helpers
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
		case ArrayKind:
			strPtr := c.arrayStrArg(mainSym)
			valArgs = append(valArgs, strPtr)
			toFree = append(toFree, strPtr)
			return
		case StructKind:
			fmtStr, fmtArgs, fmtFree := c.structFormatArgs(mainSym)
			formattedStr = fmtStr
			valArgs = append(valArgs, fmtArgs...)
			toFree = append(toFree, fmtFree...)
			return
		case ArrayRangeKind:
			arrStr, rangeStr := c.arrayRangeStrArgs(mainSym)
			formattedStr = builder.String() + "[%s]"
			valArgs = append(valArgs, arrStr, rangeStr)
			toFree = append(toFree, arrStr, rangeStr)
			return
		}
	}

	// Make %p consistent across platforms: print as 0x<lowercase-hex>
	// by converting the pointer to an unsigned 64-bit integer and using %llx.
	if specRune == 'p' {
		// Validate type: %p requires a pointer.
		if mainType.Kind() != PtrKind {
			err = formatSpecifierTypeError(tok, specRune, mainId, mainType)
			c.Errors = append(c.Errors, err)
			return
		}
		// Use the raw symbol to ensure we have the pointer, not a dereferenced value.
		mainSym, _ = c.getRawSymbol(mainId)
		// Cast pointer to i64 and format with 0x%llx
		ptrAsInt := c.builder.CreatePtrToInt(mainSym.Val, c.Context.Int64Type(), "ptr_as_i64")
		formattedStr = "0x%llx"
		valArgs = append(valArgs, ptrAsInt)
		return
	}
	if specRune == 'c' {
		if mainType.Kind() != IntKind {
			err = formatSpecifierTypeError(tok, specRune, mainId, mainType)
			c.Errors = append(c.Errors, err)
			return
		}
		valArgs = append(valArgs, c.formatCIntArg(mainSym, "format_char_i32"))
		return
	}

	if specToKind[specRune] != mainType.Kind() {
		err = formatSpecifierTypeError(tok, specRune, mainId, mainType)
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
func (c *Compiler) formatString(tok token.Token, value string) (string, []llvm.Value, []llvm.Value) {
	var builder strings.Builder
	var args []llvm.Value
	var toFree []llvm.Value

	// Marker syntax is recognized from raw source. Escapes decode directly to
	// literal output and are never reconsidered as markers or specifiers.
	runes := []rune(value)
	i := 0
	for i < len(runes) {
		if runes[i] == '\\' {
			decoded, next, _ := lexer.DecodeStringEscape(runes, i)
			writeFormatText(&builder, decoded)
			i = next
			continue
		}
		if !maybeMarker(runes, i) {
			writeFormatText(&builder, runes[i])
			i++
			continue
		}
		// If we see a '-' and the next rune is a valid identifier start...
		// Parse the marker.
		marker, err := c.parseMarker(tok, value, runes, i)
		if !marker.mainResolved {
			writeLiteralFormatText(&builder, runes[i:marker.end])
			i = marker.end
			continue
		}
		if err != nil {
			return "", args, nil
		}
		formattedStr, idArgs, idToFree, err := c.parseFormatting(tok, value, marker.mainID, marker.symbols, marker.customSpec)
		if err != nil {
			return "", args, nil
		}
		builder.WriteString(formattedStr)
		writeLiteralFormatText(&builder, marker.literalSuffix)
		args = append(args, idArgs...)
		toFree = append(toFree, idToFree...)
		// Advance past the marker.
		i = marker.end
	}

	st := builder.String()
	return st, args, toFree
}

// upgradeIntSpec normalizes integer-like conversions to Pluto's I64 ABI while
// preserving flags, width, and precision.
// Examples:
//
//	%d   -> %lld
//	%*d  -> %*lld
//	%-08d -> %-08lld
//	%n   -> %lln
func upgradeIntSpec(spec string) string {
	if len(spec) < 2 || spec[0] != '%' {
		return spec
	}
	conv := spec[len(spec)-1]
	switch conv {
	case 'd', 'i', 'u', 'o', 'x', 'X', 'n':
		// eligible for upgrade
	default:
		return spec
	}
	body := spec[1 : len(spec)-1]
	if strings.HasSuffix(body, "ll") {
		return spec
	}
	if strings.HasSuffix(body, "l") {
		body = body[:len(body)-1]
	}
	return "%" + body + "ll" + string(conv)
}

func maybeMarker(runes []rune, i int) bool {
	if i+1 < len(runes) && runes[i] == '-' && lexer.IsLetter(runes[i+1]) {
		return true
	}
	return false
}

// structFormatArgs builds a printf format string and args for a struct value.
// Output format:
//
//	Point
//	    :x y
//	    1 2
func (c *Compiler) structFormatArgs(s *Symbol) (fmtStr string, args []llvm.Value, toFree []llvm.Value) {
	st := s.Type.(Struct)
	var headerParts []string
	var valueParts []string
	for i, field := range st.Fields {
		headerParts = append(headerParts, field.Name)
		fieldVal := c.builder.CreateExtractValue(s.Val, i, field.Name)
		spec, _ := defaultSpecifier(field.Type)
		switch field.Type.Kind() {
		case FloatKind:
			strPtr := c.floatStrArg(&Symbol{Type: field.Type, Val: fieldVal})
			args = append(args, strPtr)
			toFree = append(toFree, strPtr)
		case ArrayKind:
			strPtr := c.arrayStrArg(&Symbol{Type: field.Type, Val: fieldVal})
			args = append(args, strPtr)
			toFree = append(toFree, strPtr)
		default:
			args = append(args, fieldVal)
		}
		valueParts = append(valueParts, spec)
	}
	fmtStr = st.Name + "\n    :" + strings.Join(headerParts, " ") + "\n    " + strings.Join(valueParts, " ") + "\n"
	return
}

// hasValidMarkers checks if a format string contains any markers (-identifier)
// where the main identifier is defined according to the provided isDefined callback.
// This aligns with parseMarker/formatString semantics: a marker is only valid when
// its main identifier exists. Specifier-only matches (e.g., "-undef%(-width)d" where
// only width is defined) are not considered valid markers.
func hasValidMarkers(value string, isDefined func(string) bool) bool {
	runes := []rune(value)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' {
			_, next, _ := lexer.DecodeStringEscape(runes, i)
			i = next - 1
			continue
		}
		if !maybeMarker(runes, i) {
			continue
		}
		// Parse the identifier after the '-'
		mainId, end := parseIdentifier(runes, i+1)
		// Only the main identifier matters - aligns with parseMarker behavior
		if isDefined(mainId) {
			return true
		}
		i = unresolvedMarkerEnd(value, runes, end) - 1 // -1 because loop will increment
	}
	return false
}
