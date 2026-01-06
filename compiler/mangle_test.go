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
		{"simple", "foo", "3foo"},
		{"single char", "x", "1x"},
		{"longer", "calculateSum", "12calculateSum"},
		{"with underscore", "foo_bar", "7foo_bar"},
		{"private style", "_private", "8_private"},
		{"camelCase", "getValue", "8getValue"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MangleIdent(tt.ident)
			assert.Equal(t, tt.expected, result)
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
		// No relative path
		{
			name:     "simple function no args",
			modName:  "math",
			relPath:  "",
			funcName: "pi",
			args:     []Type{},
			expected: "Pt_4math_2pi_f0",
		},
		{
			name:     "function with I64 arg",
			modName:  "math",
			relPath:  "",
			funcName: "square",
			args:     []Type{I64},
			expected: "Pt_4math_6square_f1_I64",
		},
		{
			name:     "function with multiple args",
			modName:  "math",
			relPath:  "",
			funcName: "add",
			args:     []Type{I64, I64},
			expected: "Pt_4math_3add_f2_I64_I64",
		},
		{
			name:     "function with float args",
			modName:  "math",
			relPath:  "",
			funcName: "multiply",
			args:     []Type{F64, F64},
			expected: "Pt_4math_8multiply_f2_F64_F64",
		},
		{
			name:     "github module path",
			modName:  "github.com/user/math",
			relPath:  "",
			funcName: "Square",
			args:     []Type{I64},
			expected: "Pt_6github_d_3com_s_4user_s_4math_6Square_f1_I64",
		},

		// With relative path
		{
			name:     "with simple relPath",
			modName:  "github.com/user/math",
			relPath:  "stats",
			funcName: "Mean",
			args:     []Type{I64},
			expected: "Pt_6github_d_3com_s_4user_s_4math_p_5stats_4Mean_f1_I64",
		},
		{
			name:     "with nested relPath",
			modName:  "github.com/user/math",
			relPath:  "stats/integral",
			funcName: "Quad",
			args:     []Type{F64},
			expected: "Pt_6github_d_3com_s_4user_s_4math_p_5stats_s_8integral_4Quad_f1_F64",
		},

		// Mixed digit-prefix segments in paths
		{
			name:     "mixed digit-prefix in module path",
			modName:  "github.com/user/math/v1.2.34abc",
			relPath:  "",
			funcName: "Calc",
			args:     []Type{I64},
			expected: "Pt_6github_d_3com_s_4user_s_4math_s_2v1_d_n2_d_n34_3abc_4Calc_f1_I64",
		},
		{
			name:     "numeric then hyphen in module path",
			modName:  "github.com/user/stat/v2.45-abhijk",
			relPath:  "",
			funcName: "Mean",
			args:     []Type{F64},
			expected: "Pt_6github_d_3com_s_4user_s_4stat_s_2v2_d_n45_h_6abhijk_4Mean_f1_F64",
		},

		// Mixed types
		{
			name:     "mixed types",
			modName:  "mymod",
			relPath:  "",
			funcName: "process",
			args:     []Type{I64, F64, Str{}},
			expected: "Pt_5mymod_7process_f3_I64_F64_Str",
		},

		// Complex types
		{
			name:     "with pointer type",
			modName:  "mem",
			relPath:  "",
			funcName: "alloc",
			args:     []Type{Ptr{Elem: I64}},
			expected: "Pt_3mem_5alloc_f1_Ptr_t1_I64",
		},
		{
			name:     "with range type",
			modName:  "iter",
			relPath:  "",
			funcName: "sum",
			args:     []Type{Range{Iter: I64}},
			expected: "Pt_4iter_3sum_f1_Range_t1_I64",
		},
		{
			name:     "with array type",
			modName:  "arr",
			relPath:  "",
			funcName: "len",
			args:     []Type{Array{ColTypes: []Type{I64}}},
			expected: "Pt_3arr_3len_f1_Array_t1_I64",
		},
		{
			name:     "with func type",
			modName:  "hof",
			relPath:  "",
			funcName: "apply",
			args:     []Type{Func{Params: []Type{I64, F64}}},
			expected: "Pt_3hof_5apply_f1_Func_t2_I64_F64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Mangle(tt.modName, tt.relPath, tt.funcName, tt.args)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMangleDistinguishesModuleFromSubdir(t *testing.T) {
	// This is the key test: ensure module path vs module+subdir produce different results
	// Module: github.com/user/math/stats (as a single module)
	moduleOnly := Mangle("github.com/user/math/stats", "", "Mean", []Type{I64})

	// Module: github.com/user/math with subdir: stats
	moduleWithSubdir := Mangle("github.com/user/math", "stats", "Mean", []Type{I64})

	assert.NotEqual(t, moduleOnly, moduleWithSubdir,
		"Module path and module+subdir should produce different mangled names")

	// Verify the actual difference is the _p_ marker
	assert.Contains(t, moduleWithSubdir, "_p_",
		"Module with subdir should contain _p_ marker")
	assert.NotContains(t, moduleOnly, "_p_",
		"Module-only path should not contain _p_ marker before function name")
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
			result := separatorCode(tt.input)
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
		// Simple cases
		{
			name:     "simple function",
			mangled:  "Pt_4math_6square_f1_I64",
			expected: "math.square(I64)",
		},
		{
			name:     "function with multiple args",
			mangled:  "Pt_4math_3add_f2_I64_I64",
			expected: "math.add(I64, I64)",
		},
		{
			name:     "function no args",
			mangled:  "Pt_4math_2pi_f0",
			expected: "math.pi()",
		},

		// GitHub paths
		{
			name:     "github module path",
			mangled:  "Pt_6github_d_3com_s_4user_s_4math_6Square_f1_I64",
			expected: "github.com/user/math.Square(I64)",
		},

		// With relative path
		{
			name:     "with relPath",
			mangled:  "Pt_6github_d_3com_s_4user_s_4math_p_5stats_4Mean_f1_I64",
			expected: "github.com/user/math/stats.Mean(I64)",
		},
		{
			name:     "with nested relPath",
			mangled:  "Pt_6github_d_3com_s_4user_s_4math_p_5stats_s_8integral_4Quad_f1_F64",
			expected: "github.com/user/math/stats/integral.Quad(F64)",
		},

		// Mixed digit-prefix segments
		{
			name:     "mixed digit-prefix in path",
			mangled:  "Pt_6github_d_3com_s_4user_s_4math_s_2v1_d_n2_d_n34_3abc_4Calc_f1_I64",
			expected: "github.com/user/math/v1.2.34abc.Calc(I64)",
		},
		{
			name:     "numeric then hyphen in path",
			mangled:  "Pt_6github_d_3com_s_4user_s_4stat_s_2v2_d_n45_h_6abhijk_4Mean_f1_F64",
			expected: "github.com/user/stat/v2.45-abhijk.Mean(F64)",
		},

		// Consecutive separators
		{
			name:     "double dot",
			mangled:  "Pt_1a_dd_1b_1f_f0",
			expected: "a..b.f()",
		},
		{
			name:     "triple dot",
			mangled:  "Pt_1a_ddd_1b_1f_f0",
			expected: "a...b.f()",
		},
		{
			name:     "mixed consecutive separators",
			mangled:  "Pt_1a_dh_1b_1f_f0",
			expected: "a.-b.f()",
		},

		// Constants (no _f marker, just symbol name)
		{
			name:     "constant at module root",
			mangled:  "Pt_6github_d_3com_s_4user_s_4math_2pi",
			expected: "github.com/user/math.pi",
		},
		{
			name:     "constant with relPath",
			mangled:  "Pt_6github_d_3com_s_4user_s_4math_p_5stats_2pi",
			expected: "github.com/user/math/stats.pi",
		},

		// Complex types - passed through as-is (already readable)
		{
			name:     "with pointer type",
			mangled:  "Pt_3mem_5alloc_f1_Ptr_t1_I64",
			expected: "mem.alloc(Ptr_t1_I64)",
		},
		{
			name:     "with range type",
			mangled:  "Pt_4iter_3sum_f1_Range_t1_I64",
			expected: "iter.sum(Range_t1_I64)",
		},
		{
			name:     "with func type",
			mangled:  "Pt_3hof_5apply_f1_Func_t2_I64_F64",
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
			mangled := Mangle(tt.modName, tt.relPath, tt.funcName, tt.args)
			demangled := Demangle(mangled)
			assert.Equal(t, tt.expected, demangled)
		})
	}
}
