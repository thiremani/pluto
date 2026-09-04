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
			Label:  "assign_x",
			Source: "x = 5",
			Evals: []*pir.Eval{{
				Result: 0,
				Expr:   &ast.IntegerLiteral{Token: token.Token{Type: token.INT, Literal: "5"}, Value: 5},
				Slots:  []pir.Slot{{Type: i64}},
			}},
			Commit: []pir.Mapping{{
				Target: pir.Target{Kind: pir.LocalTarget, Name: "x", Type: i64},
			}},
		},
		{
			Label:  "assign_y",
			Source: "y = x",
			Evals: []*pir.Eval{{
				Result: 0,
				Expr:   &ast.Identifier{Token: token.Token{Type: token.IDENT, Literal: "x"}, Value: "x"},
				Slots:  []pir.Slot{{Type: i64}},
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

// failingWriter accepts writesLeft writes and then fails every one, standing
// in for a redirected stdout that goes away mid-stream.
type failingWriter struct{ writesLeft int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.writesLeft > 0 {
		w.writesLeft--
		return len(p), nil
	}
	return 0, errors.New("output sink closed")
}

// Labels are not unique, so the diagnostic must identify the failed plan by
// its one-based source-order number and source text.
func TestEmitPIRWriteFailure(t *testing.T) {
	plans := []*pir.AssignPlan{emitPIRTestPlans()[0], emitPIRTestPlans()[0]}
	plans[1].Source = "x = 6"
	err := emitPIR(&failingWriter{writesLeft: 1}, plans, parsedEmitPIRMode(t, "-emit-pir"))
	require.ErrorContains(t, err, `write PIR plan 2 assign_x "x = 6"`)
}

func TestEmitPIRDisabled(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, emitPIR(&out, emitPIRTestPlans(), parsedEmitPIRMode(t, "dir")))
	require.Empty(t, out.String())
}

// Plan §12: one emission group per plan, blank-line separated.
func TestEmitPIRConcise(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, emitPIR(&out, emitPIRTestPlans(), parsedEmitPIRMode(t, "-emit-pir")))
	require.Equal(t, `statement assign_x
    source "x = 5"

    execute
        %t0 = eval I64 5

    commit
        x <- %t0

statement assign_y
    source "y = x"

    execute
        %t0 = eval I64 x

    commit
        y <- %t0

`, out.String())
}

// Plan §12: expanded view adds shapes, ownership, and target types.
func TestEmitPIRExpanded(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, emitPIR(&out, emitPIRTestPlans()[:1], parsedEmitPIRMode(t, "-emit-pir=expanded")))
	require.Equal(t, `statement assign_x
    source "x = 5"

    execute
        %t0 = eval I64 5 [shape=scalar] [yield=always] [unmanaged]

    commit
        x : I64 <- %t0

`, out.String())
}
