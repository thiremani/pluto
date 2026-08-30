package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/pir"
	"github.com/thiremani/pluto/token"
)

func TestParseCLIArgsEmitPIR(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		emitPIR string
		wantErr bool
	}{
		{"Concise", []string{"-emit-pir"}, "concise", false},
		{"Expanded", []string{"-emit-pir=expanded"}, "expanded", false},
		{"ConciseWithTarget", []string{"-emit-pir", "dir"}, "concise", false},
		{"WithEmitIR", []string{"-emit-ir", "-emit-pir=expanded"}, "expanded", false},
		{"UnknownVariant", []string{"-emit-pir=verbose"}, "", true},
		{"CleanConflict", []string{"-clean", "-emit-pir"}, "", true},
		{"VersionConflict", []string{"-emit-pir=expanded", "-version"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseCLIArgs(tc.args)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.emitPIR, opts.emitPIR)
		})
	}
}

type pirTestType string

func (t pirTestType) String() string { return string(t) }

func emitPIRTestPlans() []*pir.AssignPlan {
	i64 := pirTestType("I64")
	return []*pir.AssignPlan{
		{
			Name:   "assign_x",
			Source: "x = 5",
			Evals: []*pir.Eval{{
				Result: 0,
				Expr:   &ast.IntegerLiteral{Token: token.Token{Literal: "5"}},
				Types:  []pir.Type{i64},
			}},
			Commit: []pir.Mapping{{
				Target: pir.Target{Kind: pir.LocalTarget, Name: "x", Type: i64},
			}},
		},
		{
			Name:   "assign_y",
			Source: "y = x",
			Evals: []*pir.Eval{{
				Result: 0,
				Expr:   &ast.Identifier{Token: token.Token{Literal: "x"}, Value: "x"},
				Types:  []pir.Type{i64},
			}},
			Commit: []pir.Mapping{{
				Target: pir.Target{Kind: pir.LocalTarget, Name: "y", Type: i64},
			}},
		},
	}
}

// parsedEmitPIRMode runs the real CLI parser and returns the -emit-pir mode
// it produced, so the emitPIR goldens exercise the parse-to-output bridge.
func parsedEmitPIRMode(t *testing.T, args ...string) string {
	t.Helper()
	opts, err := parseCLIArgs(args)
	require.NoError(t, err)
	return opts.emitPIR
}

// brokenWriter fails every write, standing in for a redirected stdout that
// has gone away.
type brokenWriter struct{}

func (brokenWriter) Write(p []byte) (int, error) {
	return 0, errors.New("output sink closed")
}

func TestEmitPIRWriteFailure(t *testing.T) {
	err := emitPIR(brokenWriter{}, emitPIRTestPlans(), parsedEmitPIRMode(t, "-emit-pir"))
	require.ErrorContains(t, err, "write PIR plan assign_x")
}

func TestEmitPIRDisabled(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, emitPIR(&out, emitPIRTestPlans(), parsedEmitPIRMode(t, "dir")))
	require.Empty(t, out.String())
}

func TestEmitPIRConcise(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, emitPIR(&out, emitPIRTestPlans(), parsedEmitPIRMode(t, "-emit-pir")))
	require.Equal(t, `pir.statement @assign_x
    source "x = 5"

    execute
        %t0 = eval 5 : I64

    commit simultaneous
        @x <- %t0

pir.statement @assign_y
    source "y = x"

    execute
        %t0 = eval @x : I64

    commit simultaneous
        @y <- %t0

`, out.String())
}

func TestEmitPIRExpanded(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, emitPIR(&out, emitPIRTestPlans()[:1], parsedEmitPIRMode(t, "-emit-pir=expanded")))
	require.Equal(t, `pir.statement @assign_x
    source "x = 5"

    execute
        %t0 = eval 5 : I64 [shape=scalar] [yield=always] [unmanaged]

    commit simultaneous
        @x : I64 <- %t0

`, out.String())
}
