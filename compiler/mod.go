package compiler

import (
	"fmt"
	"strings"
	"unicode/utf8"
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
//   - Segment separator: / only (maps to directory structure)
//   - Allowed in segments: ASCII lowercase letters, digits, underscore, dot, hyphen
//   - Lowercase letters only (no uppercase)
//   - No double underscores (__)
//   - No trailing underscores or dots in segments
//   - No terminal hyphen
//   - No empty segments (no // or leading/trailing /)
//   - No Windows reserved segment names
func ValidateModulePath(path string) error {
	return validatePath(path, "module path", true, true)
}

// ValidateRelativePath validates a module-relative directory path.
func ValidateRelativePath(path string) error {
	if path == "" {
		return nil
	}
	return validatePath(path, "relative path", true, false)
}

// ValidateScriptName validates a script filename without its .spt suffix.
func ValidateScriptName(name string) error {
	return validatePath(name, "script name", false, false)
}

func validatePath(path, subject string, allowSlash, lowercaseASCII bool) error {
	if path == "" {
		return fmt.Errorf("%s cannot be empty", subject)
	}
	if !utf8.ValidString(path) {
		return fmt.Errorf("%s is not valid UTF-8", subject)
	}

	segStart := 0
	position := 0

	for byteOffset, r := range path {
		if r == '/' {
			if !allowSlash {
				return fmt.Errorf("invalid character %q at position %d in %s", r, position, subject)
			}
			if err := checkPathSegment(path[segStart:byteOffset], subject, false); err != nil {
				return err
			}
			segStart = byteOffset + 1
			position++
			continue
		}

		switch {
		case r >= 'A' && r <= 'Z':
			if lowercaseASCII {
				return fmt.Errorf("uppercase letter %q at position %d: %s must be lowercase", r, position, subject)
			}
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r >= utf8.RuneSelf && !lowercaseASCII:
		case r == '_':
			if byteOffset > segStart && path[byteOffset-1] == '_' {
				return fmt.Errorf("double underscore at position %d", position)
			}
		case r == '.' || r == '-':
			if byteOffset > segStart && path[byteOffset-1] == '_' {
				return fmt.Errorf("underscore before separator at position %d", position-1)
			}
		default:
			return fmt.Errorf("invalid character %q at position %d in %s", r, position, subject)
		}
		position++
	}

	return checkPathSegment(path[segStart:], subject, true)
}

func checkPathSegment(seg, subject string, terminal bool) error {
	if seg == "" {
		return fmt.Errorf("empty segment in %s (consecutive separators)", subject)
	}
	if seg[len(seg)-1] == '_' {
		return fmt.Errorf("segment %q ends with underscore", seg)
	}
	if seg[len(seg)-1] == '.' {
		return fmt.Errorf("segment %q ends with dot", seg)
	}
	if terminal && seg[len(seg)-1] == '-' {
		return fmt.Errorf("segment %q ends with hyphen", seg)
	}
	// Windows treats "con.txt" as "CON" - check base name before first dot
	base := seg
	if i := strings.IndexByte(seg, '.'); i >= 0 {
		base = seg[:i]
	}
	if windowsReservedNames[strings.ToLower(base)] {
		return fmt.Errorf("segment %q has Windows reserved base name %q", seg, base)
	}
	return nil
}
