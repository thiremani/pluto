package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"tinygo.org/x/go-llvm"
)

type cleanupCheckingWriter struct {
	bytes.Buffer
	cleaned            *bool
	wroteBeforeCleanup bool
}

func (w *cleanupCheckingWriter) Write(p []byte) (int, error) {
	if !*w.cleaned {
		w.wroteBeforeCleanup = true
	}
	return w.Buffer.Write(p)
}

func panicAsICE(stderr io.Writer, cleanup func()) (err error) {
	released := false
	defer recoverICE("panic.spt", &err, stderr)
	defer cleanupUnlessReleased(&released, cleanup)
	panic("test invariant failed")
}

func TestRecoverICEReportsTypedErrorAfterModuleCleanup(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()
	mod := ctx.NewModule("ice_cleanup")

	cleaned := false
	stderr := &cleanupCheckingWriter{cleaned: &cleaned}
	err := panicAsICE(stderr, func() {
		mod.Dispose()
		cleaned = true
	})

	var ice *internalCompilerError
	if !errors.As(err, &ice) {
		t.Fatalf("error = %T %v, want *internalCompilerError", err, err)
	}
	if ice.unit != "panic.spt" {
		t.Fatalf("ICE unit = %q, want panic.spt", ice.unit)
	}
	if !isInternalCompilerError(err) {
		t.Fatal("isInternalCompilerError returned false for recovered panic")
	}
	if isInternalCompilerError(errors.New("ordinary compile error")) {
		t.Fatal("ordinary compile error was classified as an ICE")
	}
	if !cleaned {
		t.Fatal("owned LLVM module was not disposed during panic unwinding")
	}
	if stderr.wroteBeforeCleanup {
		t.Fatal("ICE report was written before owned LLVM module cleanup")
	}

	report := stderr.String()
	for _, want := range []string{
		"internal compiler error: test invariant failed",
		"This is a bug in the Pluto compiler",
		"https://github.com/thiremani/pluto/issues",
		"panicAsICE",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("ICE report missing %q:\n%s", want, report)
		}
	}
}

func TestCleanupUnlessReleasedPreservesReturnedModule(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()
	mod := ctx.NewModule("returned_module")
	defer mod.Dispose()

	released := true
	cleaned := false
	cleanupUnlessReleased(&released, func() {
		cleaned = true
	})
	if cleaned {
		t.Fatal("cleanup ran after module ownership was returned to the caller")
	}
}
