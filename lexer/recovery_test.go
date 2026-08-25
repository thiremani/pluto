package lexer

import (
	"fmt"
	"strings"
	"testing"

	"github.com/thiremani/pluto/token"
)

// lexSignature renders the stream of token types, positions, and errors,
// deliberately omitting literals: raw string spelling differs by ending
// style while everything else must not.
func lexSignature(src string) string {
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
			break
		}
	}
	return b.String()
}

func TestStringRecoveryEndingIndependent(t *testing.T) {
	// Escape-recovery paths walk raw indexes returned by DecodeStringEscape
	// while the cursor advances by logical runes; these malformed templates
	// pin that the two index spaces stay reconciled: token types, positions,
	// and diagnostics are identical whichever physical ending E expands to,
	// including CRLF pairs adjacent to escape boundaries and EOF.
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
		ref := lexSignature(strings.ReplaceAll(tpl, "E", "\n"))
		if !strings.Contains(ref, "err=\"\"") && !strings.Contains(ref, "err=") {
			t.Fatalf("template %q produced no tokens", tpl)
		}
		for _, tc := range endings {
			if got := lexSignature(strings.ReplaceAll(tpl, "E", tc.ending)); got != ref {
				t.Errorf("template %q: %s stream differs from lf:\n  lf: %s\n  %s: %s", tpl, tc.name, ref, tc.name, got)
			}
		}
	}
}
