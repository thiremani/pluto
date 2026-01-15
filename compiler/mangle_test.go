package compiler

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMangleIdent(t *testing.T) {
	tests := []struct {
		name     string
		ident    string
		expected string
	}{
		// ASCII-only identifiers
		{"simple", "foo", "3foo"},
		{"single char", "x", "1x"},
		{"longer", "calculateSum", "12calculateSum"},
		{"with underscore", "foo_bar", "7foo_bar"},
		{"private style", "_private", "8_private"},
		{"camelCase", "getValue", "8getValue"},

		// Unicode identifiers - no 0 prefix when starting with unicode
		{"pure unicode single", "π", "u1_0003C0"},
		{"pure unicode multi", "αβ", "u2_0003B10003B2"},
		{"ascii then unicode", "foo_π", "4foo_u1_0003C0"},
		{"unicode then ascii", "πbar", "u1_0003C03bar"},
		{"mixed aπb", "aπb", "1au1_0003C01b"},
		{"emoji", "😀", "u1_01F600"},
		{"ascii with emoji", "foo😀bar", "3foou1_01F6003bar"},
		{"operator symbol", "⊕", "u1_002295"},
		{"multiple unicode segments", "a🔥b🌟c", "1au1_01F5251bu1_01F31F1c"},
		{"complex alternating", "xαβyγδz", "1xu2_0003B10003B21yu2_0003B30003B41z"},
		{"ascii with numbers and unicode", "calc123πend", "7calc123u1_0003C03end"},
		{"numbers in alternating", "x1αy2βz3", "2x1u1_0003B12y2u1_0003B22z3"},

		// Digit after Unicode - uses n-prefix format
		// n<digits> if end, n<digits>_ if unicode follows, n<digits>_<len><chars> if ASCII follows
		{"digit after unicode", "α2", "u1_0003B1n2"},
		{"digit+alpha after unicode", "α2y", "u1_0003B1n2_1y"},
		{"multi digit after unicode", "α23", "u1_0003B1n23"},
		{"complex digit after unicode", "x1α2βz3", "2x1u1_0003B1n2_u1_0003B22z3"},
		{"digit then alpha after unicode", "x1α2yβz3", "2x1u1_0003B1n2_1yu1_0003B22z3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MangleIdent(tt.ident)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDemangleIdent(t *testing.T) {
	tests := []struct {
		name     string
		mangled  string
		expected string
		rest     string
	}{
		// ASCII-only
		{"simple", "3foo", "foo", ""},
		{"with rest", "3foo_bar", "foo", "_bar"},
		{"underscore in ident", "7foo_bar", "foo_bar", ""},

		// Unicode - no 0 prefix when starting with unicode
		{"pure unicode", "u1_0003C0", "π", ""},
		{"pure unicode multi", "u2_0003B10003B2", "αβ", ""},
		{"ascii then unicode", "4foo_u1_0003C0", "foo_π", ""},
		{"mixed aπb", "1au1_0003C01b", "aπb", ""},
		{"emoji", "u1_01F600", "😀", ""},
		{"ascii emoji ascii", "3foou1_01F6003bar", "foo😀bar", ""},
		{"with trailing content", "u1_0003C0_rest", "π", "_rest"},
		{"multiple unicode segments", "1au1_01F5251bu1_01F31F1c", "a🔥b🌟c", ""},
		{"complex alternating", "1xu2_0003B10003B21yu2_0003B30003B41z", "xαβyγδz", ""},
		{"ascii with numbers and unicode", "7calc123u1_0003C03end", "calc123πend", ""},
		{"numbers in alternating", "2x1u1_0003B12y2u1_0003B22z3", "x1αy2βz3", ""},

		// n-prefix format for digits after Unicode
		// n<digits> if end, n<digits>_ if unicode follows, n<digits>_<len><chars> if ASCII follows
		{"digit after unicode", "u1_0003B1n2", "α2", ""},
		{"digit+alpha after unicode", "u1_0003B1n2_1y", "α2y", ""},
		{"multi digit after unicode", "u1_0003B1n23", "α23", ""},
		{"complex digit after unicode", "2x1u1_0003B1n2_u1_0003B22z3", "x1α2βz3", ""},
		{"digit then alpha after unicode", "2x1u1_0003B1n2_1yu1_0003B22z3", "x1α2yβz3", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, rest := demangleIdent(tt.mangled)
			assert.Equal(t, tt.expected, result)
			assert.Equal(t, tt.rest, rest)
		})
	}
}

func TestMangleDemangleIdentRoundTrip(t *testing.T) {
	// Test that demangleIdent(MangleIdent(x)) == x for various identifiers
	tests := []string{
		"foo",
		"foo_bar",
		"_private",
		"π",
		"αβγ",
		"foo_π",
		"πbar",
		"aπb",
		"😀",
		"foo😀bar",
		"⊕",
		"a🔥b🌟c",
		"calculate_αβ_sum",
		"xαβyγδz",
		"calc123πend",
		"x1αy2βz3",
		// Digit-after-Unicode cases (now supported with n-prefix)
		"α2",
		"α2y",
		"α23",
		"x1α2βz3",
		"x1α2yβz3",
	}

	for _, ident := range tests {
		t.Run(ident, func(t *testing.T) {
			mangled := MangleIdent(ident)
			demangled, rest := demangleIdent(mangled)
			assert.Equal(t, ident, demangled, "round-trip failed for %q -> %q", ident, mangled)
			assert.Empty(t, rest, "unexpected rest after demangling %q", mangled)
		})
	}
}

func TestManglePath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		// Simple paths
		{"simple module", "math", "4math"},
		{"two parts with dot", "foo.bar", "3foo_d_3bar"},
		{"two parts with slash", "foo/bar", "3foo_s_3bar"},

		// GitHub-style paths
		{"github path", "github.com/user/math", "6github_d_3com_s_4user_s_4math"},
		{"github with org", "github.com/thiremani/pluto", "6github_d_3com_s_9thiremani_s_5pluto"},

		// Numeric segments (version numbers)
		{"version number", "v1.2.3", "2v1_d_n2_d_n3"},
		{"mixed numeric", "pkg/v2", "3pkg_s_2v2"},
		{"pure numeric segment", "lib/123/util", "3lib_s_n123_s_4util"},
		{"mixed digit-prefix segment", "v1.2.34abc", "2v1_d_n2_d_n34_3abc"},
		{"numeric then hyphen", "v2.45-abhijk", "2v2_d_n45_h_6abhijk"},

		// Hyphenated names
		{"with hyphen", "my-package", "2my_h_7package"},
		{"hyphen and dot", "my-pkg.com", "2my_h_3pkg_d_3com"},

		// Consecutive separators
		{"double dot", "a..b", "1a_dd_1b"},
		{"triple dot", "a...b", "1a_ddd_1b"},
		{"dot slash", "a./b", "1a_ds_1b"},
		{"slash dot", "a/.b", "1a_sd_1b"},

		// Edge cases
		{"trailing separator handled", "foo/", "3foo"},
		{"leading separator", "/foo", "s_3foo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ManglePath(tt.path)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMangle(t *testing.T) {
	tests := []struct {
		name     string
		modName  string
		relPath  string
		funcName string
		args     []Type
		expected string
	}{
		// No relative path (all functions now have _p_ after ModPath)
		{
			name:     "simple function no args",
			modName:  "math",
			relPath:  "",
			funcName: "pi",
			args:     []Type{},
			expected: "Pt_4math_p_2pi_f0",
		},
		{
			name:     "function with I64 arg",
			modName:  "math",
			relPath:  "",
			funcName: "square",
			args:     []Type{I64},
			expected: "Pt_4math_p_6square_f1_I64",
		},
		{
			name:     "function with multiple args",
			modName:  "math",
			relPath:  "",
			funcName: "add",
			args:     []Type{I64, I64},
			expected: "Pt_4math_p_3add_f2_I64_I64",
		},
		{
			name:     "function with float args",
			modName:  "math",
			relPath:  "",
			funcName: "multiply",
			args:     []Type{F64, F64},
			expected: "Pt_4math_p_8multiply_f2_F64_F64",
		},
		{
			name:     "github module path",
			modName:  "github.com/user/math",
			relPath:  "",
			funcName: "Square",
			args:     []Type{I64},
			expected: "Pt_6github_d_3com_s_4user_s_4math_p_6Square_f1_I64",
		},

		// With relative path
		{
			name:     "with simple relPath",
			modName:  "github.com/user/math",
			relPath:  "stats",
			funcName: "Mean",
			args:     []Type{I64},
			expected: "Pt_6github_d_3com_s_4user_s_4math_p_5stats_r_4Mean_f1_I64",
		},
		{
			name:     "with nested relPath",
			modName:  "github.com/user/math",
			relPath:  "stats/integral",
			funcName: "Quad",
			args:     []Type{F64},
			expected: "Pt_6github_d_3com_s_4user_s_4math_p_5stats_s_8integral_r_4Quad_f1_F64",
		},

		// Mixed digit-prefix segments in paths
		{
			name:     "mixed digit-prefix in module path",
			modName:  "github.com/user/math/v1.2.34abc",
			relPath:  "",
			funcName: "Calc",
			args:     []Type{I64},
			expected: "Pt_6github_d_3com_s_4user_s_4math_s_2v1_d_n2_d_n34_3abc_p_4Calc_f1_I64",
		},
		{
			name:     "numeric then hyphen in module path",
			modName:  "github.com/user/stat/v2.45-abhijk",
			relPath:  "",
			funcName: "Mean",
			args:     []Type{F64},
			expected: "Pt_6github_d_3com_s_4user_s_4stat_s_2v2_d_n45_h_6abhijk_p_4Mean_f1_F64",
		},

		// Mixed types
		{
			name:     "mixed types",
			modName:  "mymod",
			relPath:  "",
			funcName: "process",
			args:     []Type{I64, F64, Str{}},
			expected: "Pt_5mymod_p_7process_f3_I64_F64_Str",
		},

		// Complex types
		{
			name:     "with pointer type",
			modName:  "mem",
			relPath:  "",
			funcName: "alloc",
			args:     []Type{Ptr{Elem: I64}},
			expected: "Pt_3mem_p_5alloc_f1_Ptr_t1_I64",
		},
		{
			name:     "with range type",
			modName:  "iter",
			relPath:  "",
			funcName: "sum",
			args:     []Type{Range{Iter: I64}},
			expected: "Pt_4iter_p_3sum_f1_Range_t1_I64",
		},
		{
			name:     "with array type",
			modName:  "arr",
			relPath:  "",
			funcName: "len",
			args:     []Type{Array{ColTypes: []Type{I64}}},
			expected: "Pt_3arr_p_3len_f1_Array_t1_I64",
		},
		{
			name:     "with func type",
			modName:  "hof",
			relPath:  "",
			funcName: "apply",
			args:     []Type{Func{Params: []Type{I64, F64}}},
			expected: "Pt_3hof_p_5apply_f1_Func_t2_I64_F64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mangledPath := MangleDirPath(tt.modName, tt.relPath)
			result := Mangle(mangledPath, tt.funcName, tt.args)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMangleDistinguishesModuleFromSubdir(t *testing.T) {
	// This is the key test: ensure module path vs module+subdir produce different results
	// Module: github.com/user/math/stats (as a single module)
	moduleOnly := Mangle(MangleDirPath("github.com/user/math/stats", ""), "Mean", []Type{I64})

	// Module: github.com/user/math with subdir: stats
	moduleWithSubdir := Mangle(MangleDirPath("github.com/user/math", "stats"), "Mean", []Type{I64})

	assert.NotEqual(t, moduleOnly, moduleWithSubdir,
		"Module path and module+subdir should produce different mangled names")

	// Both now have _p_ (always present after ModPath)
	// moduleWithSubdir also has _r_ (marks end of relpath)
	assert.Contains(t, moduleOnly, "_p_",
		"Module-only should contain _p_ marker")
	assert.Contains(t, moduleWithSubdir, "_p_",
		"Module with subdir should contain _p_ marker")
	assert.Contains(t, moduleWithSubdir, "_r_",
		"Module with subdir should contain _r_ marker")

	// moduleOnly: ...s_5stats_p_4Mean_f1...  (stats is part of modpath)
	// moduleWithSubdir: ...s_4math_p_5stats_r_4Mean_f1... (stats is after _p_, _r_ marks end)
	assert.Contains(t, moduleOnly, "s_5stats_p_4Mean",
		"Module-only should have stats before _p_")
	assert.Contains(t, moduleWithSubdir, "s_4math_p_5stats_r_4Mean",
		"Module with subdir should have stats after _p_ with _r_ marker")
}

func TestSeparatorCode(t *testing.T) {
	tests := []struct {
		input    rune
		expected rune
	}{
		{'.', 'd'},
		{'/', 's'},
		{'-', 'h'},
		{'a', 0},
		{'_', 0},
		{'1', 0},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			result := SeparatorCode(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDemangle(t *testing.T) {
	tests := []struct {
		name     string
		mangled  string
		expected string
	}{
		// Simple cases (all functions now have _p_ after ModPath)
		{
			name:     "simple function",
			mangled:  "Pt_4math_p_6square_f1_I64",
			expected: "math.square(I64)",
		},
		{
			name:     "function with multiple args",
			mangled:  "Pt_4math_p_3add_f2_I64_I64",
			expected: "math.add(I64, I64)",
		},
		{
			name:     "function no args",
			mangled:  "Pt_4math_p_2pi_f0",
			expected: "math.pi()",
		},
		{
			name:     "function with 11 args",
			mangled:  "Pt_4math_p_6bigfun_f11_I64_F64_Str_I32_U8_F32_I16_U64_I8_U32_U16",
			expected: "math.bigfun(I64, F64, Str, I32, U8, F32, I16, U64, I8, U32, U16)",
		},

		// GitHub paths
		{
			name:     "github module path",
			mangled:  "Pt_6github_d_3com_s_4user_s_4math_p_6Square_f1_I64",
			expected: "github.com/user/math.Square(I64)",
		},

		// With relative path
		{
			name:     "with relPath",
			mangled:  "Pt_6github_d_3com_s_4user_s_4math_p_5stats_r_4Mean_f1_I64",
			expected: "github.com/user/math/stats.Mean(I64)",
		},
		{
			name:     "with nested relPath",
			mangled:  "Pt_6github_d_3com_s_4user_s_4math_p_5stats_s_8integral_r_4Quad_f1_F64",
			expected: "github.com/user/math/stats/integral.Quad(F64)",
		},

		// Mixed digit-prefix segments
		{
			name:     "mixed digit-prefix in path",
			mangled:  "Pt_6github_d_3com_s_4user_s_4math_s_2v1_d_n2_d_n34_3abc_p_4Calc_f1_I64",
			expected: "github.com/user/math/v1.2.34abc.Calc(I64)",
		},
		{
			name:     "numeric then hyphen in path",
			mangled:  "Pt_6github_d_3com_s_4user_s_4stat_s_2v2_d_n45_h_6abhijk_p_4Mean_f1_F64",
			expected: "github.com/user/stat/v2.45-abhijk.Mean(F64)",
		},

		// Consecutive separators
		{
			name:     "double dot",
			mangled:  "Pt_1a_dd_1b_p_1f_f0",
			expected: "a..b.f()",
		},
		{
			name:     "triple dot",
			mangled:  "Pt_1a_ddd_1b_p_1f_f0",
			expected: "a...b.f()",
		},
		{
			name:     "mixed consecutive separators",
			mangled:  "Pt_1a_dh_1b_p_1f_f0",
			expected: "a.-b.f()",
		},

		// Constants (_p_ marks end of modpath, _r_ marks end of relpath)
		{
			name:     "constant at module root",
			mangled:  "Pt_6github_d_3com_s_4user_s_4math_p_2pi",
			expected: "github.com/user/math.pi",
		},
		{
			name:     "constant with relPath",
			mangled:  "Pt_6github_d_3com_s_4user_s_4math_p_5stats_r_2pi",
			expected: "github.com/user/math/stats.pi",
		},

		// Complex types - passed through as-is (already readable)
		{
			name:     "with pointer type",
			mangled:  "Pt_3mem_p_5alloc_f1_Ptr_t1_I64",
			expected: "mem.alloc(Ptr_t1_I64)",
		},
		{
			name:     "with range type",
			mangled:  "Pt_4iter_p_3sum_f1_Range_t1_I64",
			expected: "iter.sum(Range_t1_I64)",
		},
		{
			name:     "with func type",
			mangled:  "Pt_3hof_p_5apply_f1_Func_t2_I64_F64",
			expected: "hof.apply(Func_t2_I64_F64)",
		},

		// Not a Pluto symbol
		{
			name:     "not pluto symbol",
			mangled:  "printf",
			expected: "printf",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Demangle(tt.mangled)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDemangleParsedError(t *testing.T) {
	tests := []struct {
		name    string
		mangled string
		wantErr bool
	}{
		{
			name:    "valid pluto symbol",
			mangled: "Pt_4math_p_3add_f2_I64_I64",
			wantErr: false,
		},
		{
			name:    "non-pluto symbol passes through",
			mangled: "printf",
			wantErr: false,
		},
		{
			name:    "malformed - missing _p_ marker",
			mangled: "Pt_4math_3add",
			wantErr: true,
		},
		{
			name:    "malformed - Pt_ prefix but no marker",
			mangled: "Pt_6github_d_3com",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DemangleParsed(tt.mangled)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "malformed Pluto symbol")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMangleDemangleRoundTrip(t *testing.T) {
	// Test that Demangle(Mangle(...)) produces readable output
	tests := []struct {
		modName  string
		relPath  string
		funcName string
		args     []Type
		expected string
	}{
		{"math", "", "add", []Type{I64, I64}, "math.add(I64, I64)"},
		{"github.com/user/pkg", "", "Run", []Type{}, "github.com/user/pkg.Run()"},
		{"github.com/user/pkg", "sub", "Run", []Type{F64}, "github.com/user/pkg/sub.Run(F64)"},
		{"github.com/user/math/v1.2.34abc", "", "Calc", []Type{I64}, "github.com/user/math/v1.2.34abc.Calc(I64)"},
		{"github.com/user/stat/v2.45-abhijk", "", "Mean", []Type{F64}, "github.com/user/stat/v2.45-abhijk.Mean(F64)"},
		// Consecutive separators
		{"a..b", "", "f", []Type{}, "a..b.f()"},
		{"x/1.2..-3/y", "", "g", []Type{I64}, "x/1.2..-3/y.g(I64)"},
	}

	for _, tt := range tests {
		name := tt.modName + "/" + tt.relPath + "." + tt.funcName
		t.Run(name, func(t *testing.T) {
			mangledPath := MangleDirPath(tt.modName, tt.relPath)
			mangled := Mangle(mangledPath, tt.funcName, tt.args)
			demangled := Demangle(mangled)
			assert.Equal(t, tt.expected, demangled)
		})
	}
}

func TestMangleConst(t *testing.T) {
	tests := []struct {
		name      string
		modName   string
		relPath   string
		constName string
		expected  string
	}{
		// No relative path
		{
			name:      "simple constant at module root",
			modName:   "math",
			relPath:   "",
			constName: "pi",
			expected:  "Pt_4math_p_2pi",
		},
		{
			name:      "github module constant",
			modName:   "github.com/user/math",
			relPath:   "",
			constName: "pi",
			expected:  "Pt_6github_d_3com_s_4user_s_4math_p_2pi",
		},

		// With relative path
		{
			name:      "constant with relPath",
			modName:   "github.com/user/math",
			relPath:   "stats",
			constName: "pi",
			expected:  "Pt_6github_d_3com_s_4user_s_4math_p_5stats_r_2pi",
		},
		{
			name:      "constant with nested relPath",
			modName:   "github.com/user/math",
			relPath:   "stats/integral",
			constName: "epsilon",
			expected:  "Pt_6github_d_3com_s_4user_s_4math_p_5stats_s_8integral_r_7epsilon",
		},

		// Edge cases: names containing _f or _r_
		{
			name:      "constant name containing _f",
			modName:   "pkg",
			relPath:   "",
			constName: "my_func",
			expected:  "Pt_3pkg_p_7my_func",
		},
		{
			name:      "constant name containing _r_",
			modName:   "pkg",
			relPath:   "",
			constName: "my_r_var",
			expected:  "Pt_3pkg_p_8my_r_var",
		},

		// Edge cases: relpaths containing . or -
		{
			name:      "relpath with dot (version folder)",
			modName:   "pkg",
			relPath:   "v1.2",
			constName: "pi",
			expected:  "Pt_3pkg_p_2v1_d_n2_r_2pi",
		},
		{
			name:      "relpath with dot and segment starting with digit",
			modName:   "pkg",
			relPath:   "stats.v2",
			constName: "pi",
			expected:  "Pt_3pkg_p_5stats_d_2v2_r_2pi",
		},
		{
			name:      "relpath with hyphen",
			modName:   "pkg",
			relPath:   "my-sub",
			constName: "val",
			expected:  "Pt_3pkg_p_2my_h_3sub_r_3val",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mangledPath := MangleDirPath(tt.modName, tt.relPath)
			result := MangleConst(mangledPath, tt.constName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConstDemangleRoundTrip(t *testing.T) {
	// Test that Demangle(MangleConst(...)) produces readable output
	tests := []struct {
		modName   string
		relPath   string
		constName string
		expected  string
	}{
		{"math", "", "pi", "math.pi"},
		{"github.com/user/pkg", "", "VERSION", "github.com/user/pkg.VERSION"},
		{"github.com/user/pkg", "sub", "MAX", "github.com/user/pkg/sub.MAX"},
		// Names containing _f or _r_ (should not confuse demangler)
		{"pkg", "", "my_func", "pkg.my_func"},
		{"pkg", "", "my_r_var", "pkg.my_r_var"},
		{"pkg", "sub", "my_func", "pkg/sub.my_func"},
		// Relpaths with . or - (should demangle correctly)
		{"pkg", "v1.2", "pi", "pkg/v1.2.pi"},
		{"pkg", "stats.v2", "pi", "pkg/stats.v2.pi"},
		{"pkg", "my-sub", "val", "pkg/my-sub.val"},
	}

	for _, tt := range tests {
		name := tt.modName + "/" + tt.relPath + "." + tt.constName
		t.Run(name, func(t *testing.T) {
			mangledPath := MangleDirPath(tt.modName, tt.relPath)
			mangled := MangleConst(mangledPath, tt.constName)
			demangled := Demangle(mangled)
			assert.Equal(t, tt.expected, demangled)
		})
	}
}
