package lexer

import (
	"fmt"
	"strings"
	"testing"

	"github.com/thiremani/pluto/token"
)

// lexSignature renders token types, positions, and errors, omitting
// literals: raw string spelling differs by ending style, nothing else may.
func lexSignature(src string) (string, bool) {
	l := New("", src)
	var b strings.Builder
	for i := 0; i < 30; i++ {
		tok, err := l.NextToken()
		es := ""
		if err != nil {
			es = err.Error()
		}
		fmt.Fprintf(&b, "%s@%d:%d err=%q | ", tok.Type, tok.Line, tok.Column, es)
		if tok.Type == token.EOF {
			return b.String(), true
		}
	}
	return b.String(), false
}

func TestStringRecoveryEndingIndependent(t *testing.T) {
	// Escape recovery walks raw indexes while the cursor advances by logical
	// runes; these malformed templates pin that the streams stay identical
	// whichever physical ending E expands to.
	templates := []string{
		"\"a\\Eb\"Ec",   // backslash before line break
		"\"\\xEb\"Ec",   // \x with break as first digit
		"\"\\x1Eb\"Ec",  // \x with break as second digit
		"\"\\u12Eb\"Ec", // \u with break mid-digits
		"\"a\\E\"Ec",    // escape then immediate close quote
		"\"a\\EEb\"Ec",  // escape then blank line
		"\"a\\E",        // truncated at EOF after escape and break
		"\"\\x1E",       // truncated \x at EOF with break
		"\"\\E\"Ec",     // escape and break as entire content
		"\"aE\\Eb\"Ec",  // break, then escape and break
	}
	endings := []struct {
		name   string
		ending string
	}{
		{"crlf", "\r\n"},
		{"cr", "\r"},
	}
	for _, tpl := range templates {
		ref, ok := lexSignature(strings.ReplaceAll(tpl, "E", "\n"))
		if !ok {
			t.Fatalf("template %q: lf variant did not reach EOF within 30 tokens", tpl)
		}
		for _, tc := range endings {
			got, ok := lexSignature(strings.ReplaceAll(tpl, "E", tc.ending))
			if !ok {
				t.Errorf("template %q: %s variant did not reach EOF within 30 tokens", tpl, tc.name)
				continue
			}
			if got != ref {
				t.Errorf("template %q: %s stream differs from lf:\n  lf: %s\n  %s: %s", tpl, tc.name, ref, tc.name, got)
			}
		}
	}
}
