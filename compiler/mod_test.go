package compiler

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateModulePath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
		errMsg  string
	}{
		// Valid paths
		{"simple", "math", false, ""},
		{"with dot", "foo.bar", false, ""},
		{"with slash", "foo/bar", false, ""},
		{"with hyphen", "foo-bar", false, ""},
		{"github style", "github.com/user/pkg", false, ""},
		{"with underscore", "my_pkg", false, ""},
		{"numeric segment", "pkg/v2", false, ""},
		{"version style", "v1.2.3", false, ""},
		{"complex path", "github.com/user/my-pkg/v2", false, ""},
		{"mixed segment", "pkg/2foo", false, ""},
		{"pure numeric", "pkg/123", false, ""},
		// With new rules: . and - are valid in segments, so these are now valid
		{"hyphen in name", "foo-con", false, ""},         // foo-con is ONE segment
		{"hyphen separated", "my-con-pkg", false, ""},    // my-con-pkg is ONE segment
		{"leading hyphen in segment", "-foo", false, ""}, // -foo is valid
		{"leading dot in segment", ".hidden", false, ""}, // .hidden is valid (Unix hidden files)
		{"terminal hyphen", "foo-", true, "ends with hyphen"},
		{"hyphen before slash", "foo-/bar", false, ""},
		{"terminal hyphen after slash", "foo/bar-", true, "ends with hyphen"},
		{"double hyphen", "foo--bar", false, ""}, // foo--bar is valid (no __ rule for -)
		{"double dot", "foo..bar", false, ""},    // foo..bar is valid (no __ rule for .)

		// Invalid: empty
		{"empty path", "", true, "cannot be empty"},

		// Invalid: uppercase
		{"uppercase letter", "MyPkg", true, "uppercase"},
		{"uppercase in domain", "GitHub.com/user/pkg", true, "uppercase"},
		{"uppercase in segment", "github.com/User/pkg", true, "uppercase"},

		// Invalid: double underscore
		{"double underscore", "my__pkg", true, "double underscore"},
		{"double underscore in segment", "github.com/my__user/pkg", true, "double underscore"},
		{"underscore before dot", "foo_.bar", true, "underscore before separator"},
		{"underscore before hyphen", "foo_-bar", true, "underscore before separator"},

		// Invalid: trailing underscore
		{"trailing underscore", "pkg_", true, "ends with underscore"},
		{"trailing underscore in segment", "github.com/user_/pkg", true, "ends with underscore"},

		// Invalid: empty segments (only / creates segments now)
		{"double slash", "foo//bar", true, "empty segment"},
		{"leading slash", "/foo", true, "empty segment"},
		{"trailing slash", "foo/", true, "empty segment"},
		{"dot segment", "foo/./bar", true, "ends with dot"},

		// Invalid: underscore edge cases
		{"underscore only segment", "_", true, "ends with underscore"},
		{"underscore segment middle", "foo/_/bar", true, "ends with underscore"},
		{"leading underscore valid", "_foo", false, ""}, // leading _ is valid

		// Invalid: special characters
		{"at sign", "github.com/@user/pkg", true, "invalid character"},
		{"hash", "pkg#v2", true, "invalid character"},
		{"space", "my pkg", true, "invalid character"},
		{"tilde", "~/pkg", true, "invalid character"},
		{"asterisk", "pkg*", true, "invalid character"},

		// Invalid: non-ASCII
		{"unicode letter", "пакет", true, "invalid character"},
		{"unicode digit start", "pkg/٣foo", true, "invalid character"},
		{"unicode in middle", "foo٣bar", true, "invalid character"},

		// Invalid: Windows reserved names (base name before first dot)
		{"windows con", "con", true, "Windows reserved"},
		{"windows nul", "github.com/nul/pkg", true, "Windows reserved"},
		{"windows com1", "com1", true, "Windows reserved"},
		{"windows lpt1", "pkg/lpt1/sub", true, "Windows reserved"},
		{"windows con.txt", "con.txt", true, "Windows reserved"},
		{"windows nul.", "nul.", true, "ends with dot"}, // trailing dot rejected first
		{"windows prn.exe", "pkg/prn.exe", true, "Windows reserved"},
		{"windows con.txt.zip", "con.txt.zip", true, "Windows reserved"}, // multi-extension also rejected
		{"dot-prefixed .con valid", ".con", false, ""},                   // base=empty before dot, not reserved
		{"dot-prefixed .nul valid", ".nul", false, ""},                   // base=empty before dot, not reserved
		{"dot-prefixed ..con valid", "..con", false, ""},                 // base=empty before dot, not reserved
		{"foo.con valid", "foo.con", false, ""},                          // base=foo, not reserved

		// Invalid: trailing dot (invalid on Windows)
		{"trailing dot", "foo.", true, "ends with dot"},
		{"trailing dot in segment", "github.com/foo./bar", true, "ends with dot"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateModulePath(tt.path)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateScriptName(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		wantErr string
	}{
		{"simple", "report", ""},
		{"numeric", "1", ""},
		{"leading-zero numeric", "01", ""},
		{"version style", "1.2.3", ""},
		{"hyphenated", "daily-report", ""},
		{"leading dot", ".hidden", ""},
		{"empty", "", "cannot be empty"},
		{"path", "reports/daily", "invalid character"},
		{"backslash", `reports\daily`, "invalid character"},
		{"uppercase", "Report", ""},
		{"double underscore", "daily__report", "double underscore"},
		{"trailing underscore", "report_", "ends with underscore"},
		{"trailing dot", "report.", "ends with dot"},
		{"dot", ".", "ends with dot"},
		{"trailing hyphen", "report-", "ends with hyphen"},
		{"reserved", "con.txt", "Windows reserved"},
		{"unicode", "日本", ""},
		{"unicode position", "日本!", "position 2"},
		{"invalid UTF-8", string([]byte{0xff}), "not valid UTF-8"},
		{"underscore before dot", "daily_.report", "underscore before separator"},
		{"underscore before hyphen", "daily_-report", "underscore before separator"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateScriptName(tt.script)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateRelativePath(t *testing.T) {
	assert.NoError(t, ValidateRelativePath(""))
	assert.NoError(t, ValidateRelativePath("reports/v1.2.3"))
	assert.NoError(t, ValidateRelativePath("Reports/日本/v1.2.3"))
	assert.NoError(t, ValidateRelativePath("reports-/daily"))
	assert.ErrorContains(t, ValidateRelativePath("daily__reports"), "double underscore")
	assert.ErrorContains(t, ValidateRelativePath("daily_.reports"), "underscore before separator")
	assert.ErrorContains(t, ValidateRelativePath("reports/daily-"), "ends with hyphen")
	assert.ErrorContains(t, ValidateRelativePath("reports/./daily"), "ends with dot")
	assert.ErrorContains(t, ValidateRelativePath("."), "ends with dot")
	assert.ErrorContains(t, ValidateRelativePath(string([]byte{0xff})), "not valid UTF-8")
}
