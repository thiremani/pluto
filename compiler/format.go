package compiler

import (
	"fmt"
	"strconv"
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

// directSpecToKind covers conversions lowered by the generic value path.
// Conversions requiring pointer, allocation, or vararg adaptation are handled separately.
var directSpecToKind = map[rune]Kind{
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
	's': StrKind,
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

func formatSpecifierFlag(ch rune) bool {
	switch ch {
	case '-', '+', ' ', '#', '0':
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

func formatSpecifierError(tok token.Token, value, msg string) *token.CompileError {
	return &token.CompileError{
		Token: tok,
		Msg:   fmt.Sprintf("Invalid format specifier string: %s. Str: %s", msg, value),
	}
}

func validateSpecifierModifiers(tok token.Token, value, flags, length string, hasWidth, hasPrecision bool, conversion rune) *token.CompileError {
	// Keep modifiers within Pluto's documented, portable printf subset.
	allowedFlags := ""
	switch conversion {
	case 'd', 'i':
		allowedFlags = "-+ 0"
	case 'u':
		allowedFlags = "-0"
	case 'o', 'x', 'X':
		allowedFlags = "-#0"
	case 'f', 'F', 'e', 'E', 'g', 'G', 'a', 'A':
		allowedFlags = "-+ #0"
	case 'c', 's', 'q', 'p':
		allowedFlags = "-"
	}

	for _, flag := range flags {
		if !strings.ContainsRune(allowedFlags, flag) {
			return formatSpecifierError(tok, value, fmt.Sprintf("Format flag %q is not supported for %%%c", flag, conversion))
		}
	}

	if length != "" && !strings.ContainsRune("diuoxXn", conversion) {
		return formatSpecifierError(tok, value, fmt.Sprintf("Length modifier %q is not supported for %%%c", length, conversion))
	}
	if hasWidth && strings.ContainsRune("n%", conversion) {
		return formatSpecifierError(tok, value, fmt.Sprintf("Width is not supported for %%%c", conversion))
	}
	if hasPrecision && strings.ContainsRune("cpn%", conversion) {
		return formatSpecifierError(tok, value, fmt.Sprintf("Precision is not supported for %%%c", conversion))
	}
	return nil
}

type specifierPrecision struct {
	present     bool
	dynamic     bool
	symbolIndex int
	literal     string
	start       int
	end         int
}

func (p specifierPrecision) removeFrom(spec string) string {
	if !p.present {
		return spec
	}
	return spec[:p.start] + spec[p.end:]
}

type parsedSpecifier struct {
	ids       []string
	text      string
	end       int
	precision specifierPrecision
}

type specifierParser struct {
	tok          token.Token
	value        string
	runes        []rune
	index        int
	specIDs      []string
	spec         strings.Builder
	flags        strings.Builder
	hasWidth     bool
	hasPrecision bool
	precision    specifierPrecision
}

func newSpecifierParser(tok token.Token, value string, runes []rune, start int) *specifierParser {
	p := &specifierParser{tok: tok, value: value, runes: runes, index: start + 1}
	p.spec.WriteRune('%')
	return p
}

func (p *specifierParser) parseFlags() {
	for p.index < len(p.runes) && formatSpecifierFlag(p.runes[p.index]) {
		p.flags.WriteRune(p.runes[p.index])
		p.spec.WriteRune(p.runes[p.index])
		p.index++
	}
}

func (p *specifierParser) rejectDirectAsterisk() *token.CompileError {
	if p.index >= len(p.runes) || p.runes[p.index] != '*' {
		return nil
	}
	p.index++
	return &token.CompileError{
		Token: p.tok,
		Msg:   fmt.Sprintf("Using * not allowed in format specifier (after the %% char). Instead use (-var) where var is an integer variable. Error str: %s", p.value),
	}
}

func (p *specifierParser) parseDynamic() (bool, *token.CompileError) {
	specID, next, complete := scanDynamicSpecifierID(p.runes, p.index)
	if !complete {
		if specID != "" {
			p.index = next
			return false, &token.CompileError{
				Token: p.tok,
				Msg:   fmt.Sprintf("Expected ) after the identifier %s. Str: %s", specID, p.value),
			}
		}
		return false, nil
	}
	p.specIDs = append(p.specIDs, specID)
	p.spec.WriteRune('*')
	p.index = next
	return true, nil
}

func (p *specifierParser) parseWidth() *token.CompileError {
	if p.index >= len(p.runes) {
		return nil
	}
	if '0' <= p.runes[p.index] && p.runes[p.index] <= '9' {
		p.hasWidth = true
		for p.index < len(p.runes) && '0' <= p.runes[p.index] && p.runes[p.index] <= '9' {
			p.spec.WriteRune(p.runes[p.index])
			p.index++
		}
		return nil
	}
	if p.runes[p.index] == '(' {
		matched, err := p.parseDynamic()
		p.hasWidth = matched
		return err
	}
	return nil
}

func (p *specifierParser) parsePrecision() *token.CompileError {
	if p.index >= len(p.runes) || p.runes[p.index] != '.' {
		return nil
	}
	p.hasPrecision = true
	p.precision.present = true
	p.precision.start = p.spec.Len()
	p.spec.WriteRune('.')
	p.index++
	if err := p.rejectDirectAsterisk(); err != nil {
		return err
	}
	if p.index < len(p.runes) && p.runes[p.index] == '(' {
		p.precision.symbolIndex = len(p.specIDs)
		matched, err := p.parseDynamic()
		p.precision.dynamic = matched
		p.precision.end = p.spec.Len()
		return err
	}
	literalStart := p.index
	for p.index < len(p.runes) && '0' <= p.runes[p.index] && p.runes[p.index] <= '9' {
		p.spec.WriteRune(p.runes[p.index])
		p.index++
	}
	p.precision.literal = string(p.runes[literalStart:p.index])
	if p.precision.literal == "" {
		p.precision.literal = "0"
	}
	p.precision.end = p.spec.Len()
	return nil
}

func (p *specifierParser) parseLength() (string, *token.CompileError) {
	if p.index >= len(p.runes) {
		return "", nil
	}
	if p.runes[p.index] == 'l' {
		length := "l"
		p.spec.WriteRune('l')
		p.index++
		if p.index < len(p.runes) && p.runes[p.index] == 'l' {
			length = "ll"
			p.spec.WriteRune('l')
			p.index++
		}
		return length, nil
	}
	if !formatSpecifierLengthStart(p.runes[p.index]) {
		return "", nil
	}
	length := p.runes[p.index]
	p.index++
	return "", formatSpecifierError(p.tok, p.value, fmt.Sprintf("Length modifier %q is not supported", length))
}

func (p *specifierParser) parseConversion(length string) *token.CompileError {
	if p.index >= len(p.runes) {
		spec := p.spec.String()
		msg := fmt.Sprintf("Format specifier %q is incomplete", spec)
		if spec == "%" {
			msg += `. Use \% or %% for a literal percent after a value`
		}
		return formatSpecifierError(p.tok, p.value, msg)
	}
	if p.runes[p.index] == '\\' {
		return formatSpecifierError(p.tok, p.value, "Escape sequences cannot be used as format syntax")
	}
	conversion := p.runes[p.index]
	if !formatSpecifierEnd(conversion) {
		p.index++
		return formatSpecifierError(p.tok, p.value, fmt.Sprintf("Unexpected %q in format specifier %q", conversion, p.spec.String()))
	}

	p.spec.WriteRune(conversion)
	p.index++
	return validateSpecifierModifiers(p.tok, p.value, p.flags.String(), length, p.hasWidth, p.hasPrecision, conversion)
}

func (p *specifierParser) result() parsedSpecifier {
	return parsedSpecifier{
		ids:       p.specIDs,
		text:      p.spec.String(),
		end:       p.index,
		precision: p.precision,
	}
}

// parseSpecifierSyntax parses a raw '%' attached to a resolved marker. Dynamic
// width and precision identifiers use (-identifier), which is returned as '*'.
func parseSpecifierSyntax(tok token.Token, value string, runes []rune, start int) (parsedSpecifier, *token.CompileError) {
	if start+1 < len(runes) && runes[start+1] == '%' {
		return parsedSpecifier{text: "%%", end: start + 2}, nil
	}

	p := newSpecifierParser(tok, value, runes, start)
	p.parseFlags()
	if err := p.rejectDirectAsterisk(); err != nil {
		return p.result(), err
	}
	if err := p.parseWidth(); err != nil {
		return p.result(), err
	}
	if err := p.parsePrecision(); err != nil {
		return p.result(), err
	}
	length, err := p.parseLength()
	if err != nil {
		return p.result(), err
	}
	if err := p.parseConversion(length); err != nil {
		return p.result(), err
	}
	return p.result(), nil
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
	mainID       string
	symbols      []*Symbol
	customSpec   string
	precision    specifierPrecision
	end          int
	mainResolved bool
}

func undefinedSpecifierVariableError(tok token.Token, value, specID string) *token.CompileError {
	return &token.CompileError{
		Token: tok,
		Msg:   fmt.Sprintf("Undefined variable %s within specifier. String Literal is %s", specID, value),
	}
}

func (c *Compiler) resolveDynamicSpecifierSymbols(tok token.Token, value string, specIDs []string) ([]*Symbol, *token.CompileError) {
	symbols := make([]*Symbol, 0, len(specIDs))
	for _, specID := range specIDs {
		specSym, found := c.getIdSym(specID)
		if !found {
			return nil, undefinedSpecifierVariableError(tok, value, specID)
		}
		if !TypeEqual(specSym.Type, I64) {
			return nil, &token.CompileError{
				Token: tok,
				Msg:   fmt.Sprintf("Format specifier variable %s must have type I64, got %s", specID, specSym.Type),
			}
		}
		symbols = append(symbols, specSym)
	}
	return symbols, nil
}

// parseMarker resolves a marker and any attached custom specifier.
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
		return result, nil
	}
	result.symbols = []*Symbol{mainSym}

	if end >= len(runes) || runes[end] != '%' {
		return result, nil
	}

	specStart := end
	spec, parseErr := parseSpecifierSyntax(tok, value, runes, specStart)
	result.end = spec.end
	if parseErr != nil {
		c.Errors = append(c.Errors, parseErr)
		return result, parseErr
	}

	specSymbols, resolveErr := c.resolveDynamicSpecifierSymbols(tok, value, spec.ids)
	if resolveErr != nil {
		c.Errors = append(c.Errors, resolveErr)
		return result, resolveErr
	}
	result.symbols = append(result.symbols, specSymbols...)
	result.customSpec = spec.text
	result.precision = spec.precision

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

type formattedMarker struct {
	text   string
	args   []llvm.Value
	toFree []llvm.Value
}

func (c *Compiler) quotePrecisionArg(tok token.Token, value string, precision specifierPrecision, syms []*Symbol) (*llvm.Value, *token.CompileError) {
	if !precision.present {
		return nil, nil
	}
	if precision.dynamic {
		symbolIndex := precision.symbolIndex + 1
		if symbolIndex >= len(syms) {
			panic("internal: dynamic format precision symbol is missing")
		}
		limitI32 := c.formatCIntArg(syms[symbolIndex], "quote_precision_i32")
		limit := c.builder.CreateSExt(limitI32, c.Context.Int64Type(), "quote_precision_i64")
		return &limit, nil
	}

	limit, err := strconv.ParseUint(precision.literal, 10, 63)
	if err != nil {
		return nil, formatSpecifierError(tok, value, fmt.Sprintf("Precision %q exceeds the I64 range", precision.literal))
	}
	limitValue := llvm.ConstInt(c.Context.Int64Type(), limit, false)
	return &limitValue, nil
}

func formatMarkerSpec(mainType Type, customSpec string) (string, rune, error) {
	defaultSpec := ""
	if customSpec == "" || customSpec == "%%" {
		var err error
		defaultSpec, err = defaultSpecifier(mainType)
		if err != nil {
			return "", 0, err
		}
	}

	customSpec = upgradeIntSpec(customSpec)
	activeSpec := customSpec
	if defaultSpec != "" {
		activeSpec = defaultSpec
	}
	return defaultSpec + customSpec, rune(activeSpec[len(activeSpec)-1]), nil
}

func (c *Compiler) formatAsString(mainSym *Symbol, result *formattedMarker) bool {
	switch mainSym.Type.Kind() {
	case FloatKind:
		strPtr := c.floatStrArg(mainSym)
		result.args = append(result.args, strPtr)
		result.toFree = append(result.toFree, strPtr)
	case RangeKind:
		strPtr := c.rangeStrArg(mainSym)
		result.args = append(result.args, strPtr)
		result.toFree = append(result.toFree, strPtr)
	case ArrayKind:
		strPtr := c.arrayStrArg(mainSym)
		result.args = append(result.args, strPtr)
		result.toFree = append(result.toFree, strPtr)
	case StructKind:
		fmtStr, fmtArgs, fmtFree := c.structFormatArgs(mainSym)
		result.text = fmtStr
		result.args = append(result.args, fmtArgs...)
		result.toFree = append(result.toFree, fmtFree...)
	case ArrayRangeKind:
		arrStr, rangeStr := c.arrayRangeStrArgs(mainSym)
		result.text += "[%s]"
		result.args = append(result.args, arrStr, rangeStr)
		result.toFree = append(result.toFree, arrStr, rangeStr)
	default:
		return false
	}
	return true
}

func (c *Compiler) formatSpecialValue(tok token.Token, mainID string, mainSym *Symbol, specRune rune, quoteLimit *llvm.Value, result *formattedMarker) (bool, *token.CompileError) {
	mainType := mainSym.Type
	switch specRune {
	case 'n':
		if !TypeEqual(mainType, I64) {
			return true, formatSpecifierTypeError(tok, specRune, mainID, mainType)
		}
		s := c.promoteToMemory(mainID)
		result.args = append(result.args, s.Val)
		return true, nil
	case 'q':
		if mainType.Kind() != StrKind {
			return true, formatSpecifierTypeError(tok, specRune, mainID, mainType)
		}
		var quoted llvm.Value
		if quoteLimit != nil {
			quoted = c.quotedStrPrefixArg(mainSym, *quoteLimit)
		} else {
			quoted = c.quotedStrArg(mainSym)
		}
		// Only a custom specifier can end in q; libc receives its width and alignment as %s.
		result.text = result.text[:len(result.text)-1] + "s"
		result.args = append(result.args, quoted)
		result.toFree = append(result.toFree, quoted)
		return true, nil
	case 's':
		return c.formatAsString(mainSym, result), nil
	case 'p':
		// Normalize pointer output as lowercase alternate-form hex while preserving width.
		rawSym, _ := c.getRawSymbol(mainID)
		if rawSym.Type.Kind() != PtrKind {
			return true, formatSpecifierTypeError(tok, specRune, mainID, rawSym.Type)
		}
		ptrAsInt := c.builder.CreatePtrToInt(rawSym.Val, c.Context.Int64Type(), "ptr_as_i64")
		result.text = "%#" + result.text[1:len(result.text)-1] + "llx"
		result.args = append(result.args, ptrAsInt)
		return true, nil
	case 'c':
		if mainType.Kind() != IntKind {
			return true, formatSpecifierTypeError(tok, specRune, mainID, mainType)
		}
		result.args = append(result.args, c.formatCIntArg(mainSym, "format_char_i32"))
		return true, nil
	default:
		return false, nil
	}
}

// parseFormatting lowers one resolved marker to compiler-owned printf text and
// matching arguments. syms contains the main value followed by dynamic sizes.
func (c *Compiler) parseFormatting(tok token.Token, value, mainID string, syms []*Symbol, customSpec string, precision specifierPrecision) (formattedMarker, *token.CompileError) {
	mainSym := syms[0]
	quotedPrecision := precision.present && strings.HasSuffix(customSpec, "q")
	var quoteLimit *llvm.Value
	if quotedPrecision {
		customSpec = precision.removeFrom(customSpec)
		var precisionErr *token.CompileError
		quoteLimit, precisionErr = c.quotePrecisionArg(tok, value, precision, syms)
		if precisionErr != nil {
			c.Errors = append(c.Errors, precisionErr)
			return formattedMarker{}, precisionErr
		}
	}
	text, specRune, buildErr := formatMarkerSpec(mainSym.Type, customSpec)
	if buildErr != nil {
		err := &token.CompileError{
			Token: tok,
			Msg:   fmt.Sprintf("Error formatting string. String: %s. Err: %s", value, buildErr),
		}
		c.Errors = append(c.Errors, err)
		return formattedMarker{}, err
	}

	result := formattedMarker{text: text}
	for i, specSym := range syms[1:] {
		if quotedPrecision && precision.dynamic && i == precision.symbolIndex {
			continue
		}
		result.args = append(result.args, c.formatCIntArg(specSym, "format_size_i32"))
	}
	handled, err := c.formatSpecialValue(tok, mainID, mainSym, specRune, quoteLimit, &result)
	if err != nil {
		c.Errors = append(c.Errors, err)
		return formattedMarker{}, err
	}
	if handled {
		return result, nil
	}
	if directSpecToKind[specRune] != mainSym.Type.Kind() {
		err := formatSpecifierTypeError(tok, specRune, mainID, mainSym.Type)
		c.Errors = append(c.Errors, err)
		return formattedMarker{}, err
	}
	result.args = append(result.args, mainSym.Val)
	return result, nil
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
			builder.WriteRune('-')
			builder.WriteString(marker.mainID)
			i = marker.end
			continue
		}
		if err != nil {
			return "", args, nil
		}
		formatted, err := c.parseFormatting(tok, value, marker.mainID, marker.symbols, marker.customSpec, marker.precision)
		if err != nil {
			return "", args, nil
		}
		builder.WriteString(formatted.text)
		args = append(args, formatted.args...)
		toFree = append(toFree, formatted.toFree...)
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
// This aligns with parseMarker/formatString semantics: each marker is resolved
// independently, including markers in text following an unresolved marker.
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
		mainId, _ := parseIdentifier(runes, i+1)
		// Only the main identifier matters - aligns with parseMarker behavior
		if isDefined(mainId) {
			return true
		}
	}
	return false
}
