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

		// Invalid: empty
		{"empty path", "", true, "cannot be empty"},

		// Invalid: uppercase
		{"uppercase letter", "MyPkg", true, "uppercase"},
		{"uppercase in domain", "GitHub.com/user/pkg", true, "uppercase"},
		{"uppercase in segment", "github.com/User/pkg", true, "uppercase"},

		// Invalid: double underscore
		{"double underscore", "my__pkg", true, "double underscore"},
		{"double underscore in segment", "github.com/my__user/pkg", true, "double underscore"},

		// Invalid: trailing underscore
		{"trailing underscore", "pkg_", true, "ends with underscore"},
		{"trailing underscore in segment", "github.com/user_/pkg", true, "ends with underscore"},

		// Invalid: empty segments
		{"double slash", "foo//bar", true, "empty segment"},
		{"double dot empty", "foo..bar", true, "empty segment"},
		{"leading slash", "/foo", true, "empty segment"},
		{"trailing slash", "foo/", true, "empty segment"},
		{"leading hyphen", "-foo", true, "empty segment"},
		{"trailing hyphen", "foo-", true, "empty segment"},
		{"double hyphen empty", "foo--bar", true, "empty segment"},

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

		// Invalid: Windows reserved names
		{"windows con", "con", true, "Windows reserved"},
		{"windows nul", "github.com/nul/pkg", true, "Windows reserved"},
		{"windows com1", "com1", true, "Windows reserved"},
		{"windows lpt1", "pkg/lpt1/sub", true, "Windows reserved"},
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
