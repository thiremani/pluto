package compiler

import (
	"fmt"
	"strconv"
	"unicode"
)

// UnmangleSignature decodes a mangled function name produced by mangle(func, args)
// into its function name and argument types. It expects the format:
//
//	$<funcName> { $<Type> }
//
// where each Type is encoded by Type.Mangle() using a per-type arity scheme.
func UnmangleSignature(s string) (name string, args []Type, err error) {
	if len(s) == 0 || s[0] != '$' {
		return "", nil, fmt.Errorf("invalid mangled string: missing leading '$'")
	}
	// read function name up to next '$' or end
	i := 1
	j := i
	for j < len(s) && s[j] != '$' {
		j++
	}
	name = s[i:j]
	pos := j
	// parse types until end of string
	for pos < len(s) {
		t, next, perr := parseTypeFrom(s, pos)
		if perr != nil {
			return "", nil, perr
		}
		args = append(args, t)
		pos = next
	}
	return name, args, nil
}

// parseTypeFrom parses a single Type starting at position pos, where s[pos] is expected to be '$'.
// It returns the parsed Type and the next index to continue parsing from.
func parseTypeFrom(s string, pos int) (Type, int, error) {
	if pos >= len(s) || s[pos] != '$' {
		return nil, pos, fmt.Errorf("parse error: expected '$' at %d", pos)
	}
	// token after '$'
	tok, next := readToken(s, pos)
	if tok == "" {
		return nil, next, fmt.Errorf("parse error: empty token at %d", pos)
	}
	switch tok {
	case "Str":
		return Str{}, next, nil
	case "Unresolved":
		return Unresolved{}, next, nil
	case "Ptr":
		// expect $1 then element type
		cnt, npos, err := readCount(s, next)
		if err != nil {
			return nil, npos, fmt.Errorf("Ptr missing count: %w", err)
		}
		if cnt != 1 {
			return nil, npos, fmt.Errorf("Ptr count must be 1, got %d", cnt)
		}
		elem, nnext, err := parseTypeFrom(s, npos)
		if err != nil {
			return nil, nnext, err
		}
		return Ptr{Elem: elem}, nnext, nil
	case "Range":
		cnt, npos, err := readCount(s, next)
		if err != nil {
			return nil, npos, fmt.Errorf("Range missing count: %w", err)
		}
		if cnt != 1 {
			return nil, npos, fmt.Errorf("Range count must be 1, got %d", cnt)
		}
		iter, nnext, err := parseTypeFrom(s, npos)
		if err != nil {
			return nil, nnext, err
		}
		return Range{Iter: iter}, nnext, nil
	case "Array":
		n, npos, err := readCount(s, next)
		if err != nil {
			return nil, npos, fmt.Errorf("Array missing count: %w", err)
		}
		cols := make([]Type, n)
		cur := npos
		for i := 0; i < n; i++ {
			ct, nn, err := parseTypeFrom(s, cur)
			if err != nil {
				return nil, nn, err
			}
			cols[i] = ct
			cur = nn
		}
		return Array{Headers: nil, ColTypes: cols, Length: 0}, cur, nil
	case "Fn":
		// $Fn $P<p> <p types> $O<r> <r types>
		p, posP, err := readTagCount(s, next, 'P')
		if err != nil {
			return nil, posP, fmt.Errorf("Fn missing P<count>: %w", err)
		}
		params := make([]Type, p)
		cur := posP
		for i := 0; i < p; i++ {
			pt, nn, err := parseTypeFrom(s, cur)
			if err != nil {
				return nil, nn, err
			}
			params[i] = pt
			cur = nn
		}
		r, posO, err := readTagCount(s, cur, 'O')
		if err != nil {
			return nil, posO, fmt.Errorf("Fn missing O<count>: %w", err)
		}
		outs := make([]Type, r)
		cur = posO
		for i := 0; i < r; i++ {
			ot, nn, err := parseTypeFrom(s, cur)
			if err != nil {
				return nil, nn, err
			}
			outs[i] = ot
			cur = nn
		}
		return Func{Name: "", Params: params, OutTypes: outs}, cur, nil
	default:
		// Scalars: I{digits} or F{digits}
		if tok[0] == 'I' || tok[0] == 'F' {
			if len(tok) == 1 {
				return nil, next, fmt.Errorf("missing width in scalar token: %q", tok)
			}
			w, err := strconv.Atoi(tok[1:])
			if err != nil {
				return nil, next, fmt.Errorf("invalid width in token %q: %v", tok, err)
			}
			if tok[0] == 'I' {
				return Int{Width: uint32(w)}, next, nil
			}
			return Float{Width: uint32(w)}, next, nil
		}
		return nil, next, fmt.Errorf("unknown type tag: %q", tok)
	}
}

// readToken reads the token that starts at s[pos], where s[pos] == '$'.
// It returns the token string and the index of the next '$' or end of string.
func readToken(s string, pos int) (string, int) {
	// precondition: s[pos] == '$'
	i := pos + 1
	j := i
	for j < len(s) && s[j] != '$' {
		j++
	}
	return s[i:j], j
}

// readCount reads a numeric token after position 'pos'. Expects s[pos] == '$'.
func readCount(s string, pos int) (int, int, error) {
	tok, next := readToken(s, pos)
	if tok == "" {
		return 0, next, fmt.Errorf("missing count token")
	}
	// Allow forms like "3" or "N3" where first char is not a digit
	start := 0
	if !unicode.IsDigit(rune(tok[0])) {
		start = 1
	}
	if start >= len(tok) {
		return 0, next, fmt.Errorf("malformed count token: %q", tok)
	}
	val, err := strconv.Atoi(tok[start:])
	if err != nil {
		return 0, next, fmt.Errorf("invalid count in %q: %v", tok, err)
	}
	return val, next, nil
}

// readTagCount expects a tag token like "P" or "O" possibly fused with digits ("P3").
// It returns the parsed integer count and next index.
func readTagCount(s string, pos int, tag rune) (int, int, error) {
	tok, next := readToken(s, pos)
	if tok == "" {
		return 0, next, fmt.Errorf("missing %c token", tag)
	}
	if rune(tok[0]) != tag {
		return 0, next, fmt.Errorf("expected %c token, got %q", tag, tok)
	}
	if len(tok) > 1 {
		val, err := strconv.Atoi(tok[1:])
		if err != nil {
			return 0, next, fmt.Errorf("invalid %c count in %q: %v", tag, tok, err)
		}
		return val, next, nil
	}
	// No fused digits; expect a separate numeric token
	return readCount(s, next)
}
