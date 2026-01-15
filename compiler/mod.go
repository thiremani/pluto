package compiler

import (
	"fmt"
	"strings"
)

// Windows reserved names (case-insensitive)
var windowsReservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidateModulePath validates a module path from pt.mod.
// Rules:
//   - Only separators: . / -
//   - ASCII lowercase letters, digits, and underscore only
//   - Lowercase letters only (no uppercase)
//   - No double underscores (__)
//   - No trailing underscores
//   - No empty segments
//   - No Windows reserved names
func ValidateModulePath(path string) error {
	if path == "" {
		return fmt.Errorf("module path cannot be empty")
	}

	segStart := 0 // Start index of current segment

	for i, r := range path {
		if SeparatorCode(r) != 0 {
			if err := checkSegment(path[segStart:i]); err != nil {
				return err
			}
			segStart = i + 1
			continue
		}

		switch {
		case r >= 'A' && r <= 'Z':
			return fmt.Errorf("uppercase letter %q at position %d: module paths must be lowercase", r, i)
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			// valid
		case r == '_':
			if i > segStart && path[i-1] == '_' {
				return fmt.Errorf("double underscore at position %d", i)
			}
		default:
			return fmt.Errorf("invalid character %q at position %d in module path", r, i)
		}
	}

	return checkSegment(path[segStart:])
}

// checkSegment validates a single path segment.
func checkSegment(seg string) error {
	if seg == "" {
		return fmt.Errorf("empty segment in module path (consecutive separators)")
	}
	if seg[len(seg)-1] == '_' {
		return fmt.Errorf("segment %q ends with underscore", seg)
	}
	if windowsReservedNames[strings.ToLower(seg)] {
		return fmt.Errorf("segment %q is a Windows reserved name", seg)
	}
	return nil
}
