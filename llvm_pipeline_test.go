package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tinygo.org/x/go-llvm"
)

func parseTestIR(t *testing.T, ir string) llvm.Module {
	t.Helper()

	ctx := llvm.NewContext()
	t.Cleanup(ctx.Dispose)

	path := filepath.Join(t.TempDir(), "test.ll")
	if err := os.WriteFile(path, []byte(ir), 0644); err != nil {
		t.Fatalf("write test IR: %v", err)
	}
	buf, err := llvm.NewMemoryBufferFromFile(path)
	if err != nil {
		t.Fatalf("read test IR: %v", err)
	}

	mod, err := ctx.ParseIR(buf)
	if err != nil {
		t.Fatalf("parse test IR: %v", err)
	}
	t.Cleanup(mod.Dispose)
	return mod
}

func TestEmitObjectRejectsInvalidModuleBeforeOptimization(t *testing.T) {
	ctx := llvm.NewContext()
	codeModule := ctx.NewModule("code")
	codeGlobal := llvm.AddGlobal(codeModule, ctx.Int64Type(), "answer")
	codeGlobal.SetInitializer(llvm.ConstInt(ctx.Int64Type(), 42, false))

	scriptModule := ctx.NewModule("script")
	fnType := llvm.FunctionType(ctx.Int64Type(), nil, false)
	fn := llvm.AddFunction(scriptModule, "main", fnType)
	entry := ctx.AddBasicBlock(fn, "entry")
	builder := ctx.NewBuilder()
	t.Cleanup(func() {
		builder.Dispose()
		scriptModule.Dispose()
		codeModule.Dispose()
		ctx.Dispose()
	})
	builder.SetInsertPointAtEnd(entry)
	loaded := builder.CreateLoad(ctx.Int64Type(), codeGlobal, "answer_load")
	builder.CreateRet(loaded)

	p := &Pluto{}
	err := p.emitObject(scriptModule, filepath.Join(t.TempDir(), "invalid.o"))
	if err == nil {
		t.Fatal("expected invalid cross-module reference to fail verification")
	}
	if got := err.Error(); !strings.Contains(got, "compiler generated invalid LLVM IR before optimization") {
		t.Fatalf("unexpected verification error: %v", err)
	}
	if got := err.Error(); !strings.Contains(got, "Referencing global in another module") {
		t.Fatalf("verification error does not include LLVM diagnostic: %v", err)
	}
}

func TestAnnotateScalarUnrollLoopsMarksSmallScalarRecurrence(t *testing.T) {
	mod := parseTestIR(t, `
define i64 @fib_like(i64 %n) {
entry:
  br label %loop

loop:
  %a = phi i64 [ 0, %entry ], [ %b, %loop ]
  %b = phi i64 [ 1, %entry ], [ %sum, %loop ]
  %i = phi i64 [ %n, %entry ], [ %dec, %loop ]
  %dec = add i64 %i, -1
  %sum = add i64 %a, %b
  %done = icmp eq i64 %dec, 0
  br i1 %done, label %exit, label %loop

exit:
  ret i64 %b
}
`)

	if got := annotateScalarUnrollLoops(mod); got != 1 {
		t.Fatalf("annotateScalarUnrollLoops() = %d, want 1", got)
	}
	ir := mod.String()
	if !strings.Contains(ir, "llvm.loop.unroll.count") {
		t.Fatalf("expected unroll metadata in IR:\n%s", ir)
	}
}

func TestAnnotateScalarUnrollLoopsSkipsCallHeavyLoops(t *testing.T) {
	mod := parseTestIR(t, `
declare void @side_effect()

define void @call_loop(i64 %n) {
entry:
  br label %loop

loop:
  %i = phi i64 [ %n, %entry ], [ %dec, %loop ]
  call void @side_effect()
  %dec = add i64 %i, -1
  %done = icmp eq i64 %dec, 0
  br i1 %done, label %exit, label %loop

exit:
  ret void
}
`)

	if got := annotateScalarUnrollLoops(mod); got != 0 {
		t.Fatalf("annotateScalarUnrollLoops() = %d, want 0", got)
	}
}

func TestAnnotateScalarUnrollLoopsSkipsExpensiveArithmetic(t *testing.T) {
	mod := parseTestIR(t, `
define i64 @rem_loop(i64 %n) {
entry:
  br label %loop

loop:
  %i = phi i64 [ %n, %entry ], [ %dec, %loop ]
  %acc = phi i64 [ 0, %entry ], [ %next, %loop ]
  %rem = srem i64 %i, 17
  %next = add i64 %acc, %rem
  %dec = add i64 %i, -1
  %done = icmp eq i64 %dec, 0
  br i1 %done, label %exit, label %loop

exit:
  ret i64 %next
}
`)

	if got := annotateScalarUnrollLoops(mod); got != 0 {
		t.Fatalf("annotateScalarUnrollLoops() = %d, want 0", got)
	}
}

func TestAnnotateScalarUnrollLoopsAllowsFloatingDivision(t *testing.T) {
	mod := parseTestIR(t, `
define double @harmonic_like(i64 %n) {
entry:
  br label %loop

loop:
  %i = phi i64 [ %n, %entry ], [ %dec, %loop ]
  %acc = phi double [ 0.0, %entry ], [ %next, %loop ]
  %as_float = sitofp i64 %i to double
  %term = fdiv double 1.0, %as_float
  %next = fadd double %acc, %term
  %dec = add i64 %i, -1
  %done = icmp eq i64 %dec, 0
  br i1 %done, label %exit, label %loop

exit:
  ret double %next
}
`)

	if got := annotateScalarUnrollLoops(mod); got != 1 {
		t.Fatalf("annotateScalarUnrollLoops() = %d, want 1", got)
	}
}

func TestAnnotateScalarUnrollLoopsSkipsVectorLoops(t *testing.T) {
	mod := parseTestIR(t, `
define void @vector_loop(i64 %n, <2 x i64> %v) {
entry:
  br label %loop

loop:
  %i = phi i64 [ %n, %entry ], [ %dec, %loop ]
  %acc = phi <2 x i64> [ %v, %entry ], [ %next, %loop ]
  %next = add <2 x i64> %acc, %v
  %dec = add i64 %i, -1
  %done = icmp eq i64 %dec, 0
  br i1 %done, label %exit, label %loop

exit:
  ret void
}
`)

	if got := annotateScalarUnrollLoops(mod); got != 0 {
		t.Fatalf("annotateScalarUnrollLoops() = %d, want 0", got)
	}
}

func TestAnnotateScalarUnrollLoopsUsesDominatorsForBackedges(t *testing.T) {
	mod := parseTestIR(t, `
define i64 @reordered_loop(i64 %n) {
entry:
  br label %loop

latch:
  %done = icmp eq i64 %dec, 0
  br i1 %done, label %exit, label %loop

loop:
  %i = phi i64 [ %n, %entry ], [ %dec, %latch ]
  %acc = phi i64 [ 0, %entry ], [ %next, %latch ]
  %next = add i64 %acc, %i
  %dec = add i64 %i, -1
  br label %latch

exit:
  ret i64 %next
}
`)

	if got := annotateScalarUnrollLoops(mod); got != 1 {
		t.Fatalf("annotateScalarUnrollLoops() = %d, want 1", got)
	}
}

func TestScalarUnrollPipelineUnrollsPostInlineRecurrence(t *testing.T) {
	mod := parseTestIR(t, `
define i64 @post_inline_recurrence(i64 %outer_n) {
entry:
  br label %outer

outer:
  %iter = phi i64 [ 0, %entry ], [ %iter_next, %inner_exit ]
  %sum = phi i64 [ 0, %entry ], [ %sum_next, %inner_exit ]
  %inner_n = or i64 %iter, 32
  br label %inner

inner:
  %b = phi i64 [ %fib_next, %inner ], [ 1, %outer ]
  %a = phi i64 [ %b, %inner ], [ 0, %outer ]
  %n = phi i64 [ %dec, %inner ], [ %inner_n, %outer ]
  %dec = add i64 %n, -1
  %fib_next = add i64 %a, %b
  %inner_done = icmp eq i64 %dec, 0
  br i1 %inner_done, label %inner_exit, label %inner

inner_exit:
  %sum_next = add i64 %sum, %b
  %iter_next = add i64 %iter, 1
  %outer_done = icmp eq i64 %iter_next, %outer_n
  br i1 %outer_done, label %exit, label %outer

exit:
  ret i64 %sum_next
}
`)

	if got := annotateScalarUnrollLoops(mod); got != 1 {
		t.Fatalf("annotateScalarUnrollLoops() = %d, want 1", got)
	}

	tm, err := currentBuildConfig().newTargetMachine()
	if err != nil {
		t.Fatalf("new target machine: %v", err)
	}
	defer tm.Dispose()

	pbo := llvm.NewPassBuilderOptions()
	defer pbo.Dispose()
	pbo.SetLoopUnrolling(true)
	if err := mod.RunPasses(llvmScalarUnrollPipeline, tm, pbo); err != nil {
		t.Fatalf("run scalar unroll pipeline: %v", err)
	}

	ir := mod.String()
	if !strings.Contains(ir, "fib_next.3") {
		t.Fatalf("expected inner recurrence to be unrolled:\n%s", ir)
	}
}

func TestScalarUnrollAnnotatesRealPlutoFibTailLoop(t *testing.T) {
	projectDir := t.TempDir()
	t.Setenv("PTCACHE", filepath.Join(t.TempDir(), "cache"))

	files := map[string]string{
		MOD_FILE: "module github.com/thiremani/pluto/scalar_unroll_test\n",
		"support.pt": `
y = Fib(n)
    y = FibAux(n, 0, 1)

y = FibAux(n, a, b)
    y = n == 0 a
    y = n != 0 FibAux(n - 1, b, a + b)
`,
		"main.spt": `
i = 0:1000000
res = 0
res = res + Fib(32 + i % 2)
res
`,
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(projectDir, name), []byte(contents), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	p := New(projectDir, cliOptions{})
	defer p.Ctx.Dispose()

	codeFiles, scriptFiles := p.ScanPlutoFiles("")
	if len(scriptFiles) != 1 {
		t.Fatalf("scriptFiles = %v, want one script", scriptFiles)
	}
	cc, codeLL, err := p.CompileCode(codeFiles)
	if err != nil {
		t.Fatalf("compile code: %v", err)
	}
	scriptModule, err := p.CompileScript(scriptFiles[0], "main", cc, codeLL)
	if err != nil {
		t.Fatalf("compile script: %v", err)
	}
	defer scriptModule.Dispose()

	tm, err := currentBuildConfig().newTargetMachine()
	if err != nil {
		t.Fatalf("new target machine: %v", err)
	}
	defer tm.Dispose()

	pbo := llvm.NewPassBuilderOptions()
	defer pbo.Dispose()
	pbo.SetLoopInterleaving(true)
	pbo.SetLoopVectorization(true)
	pbo.SetSLPVectorization(true)
	pbo.SetLoopUnrolling(true)
	if err := scriptModule.RunPasses(llvmOptPipeline, tm, pbo); err != nil {
		t.Fatalf("run O3 pipeline: %v", err)
	}
	loopMDKind := scriptModule.Context().MDKindID("llvm.loop")
	// Loops the O3 run already marked must contribute no add chains; that
	// makes any post-unroll chain growth attributable to the loops annotated
	// below rather than to pre-existing loop metadata.
	if got := maxChainedAddsInMarkedLatch(scriptModule); got != 0 {
		t.Fatalf("expected no add chains in pre-marked loop latches, got %d:\n%s", got, scriptModule.String())
	}
	// Record the exact latches the annotator is about to mark (the inlined
	// copies of the Fib tail recurrence) and the longest add chain they hold.
	candidates := 0
	preUnrollChain := 0
	for fn := scriptModule.FirstFunction(); !fn.IsNil(); fn = llvm.NextFunction(fn) {
		for _, loop := range scalarUnrollCandidates(fn) {
			if !loop.term.Metadata(loopMDKind).IsNil() {
				continue
			}
			candidates++
			if chained := chainedAddsInBlock(loop.latch); chained > preUnrollChain {
				preUnrollChain = chained
			}
		}
	}
	if candidates == 0 {
		t.Fatalf("expected the Fib tail recurrence as a scalar unroll candidate:\n%s", scriptModule.String())
	}
	if got := annotateScalarUnrollLoops(scriptModule); got != candidates {
		t.Fatalf("annotateScalarUnrollLoops() = %d, want %d annotated candidates", got, candidates)
	}
	if !strings.Contains(scriptModule.String(), "llvm.loop.unroll.count") {
		t.Fatalf("expected scalar unroll metadata in real Pluto post-O3 IR:\n%s", scriptModule.String())
	}

	if err := scriptModule.RunPasses(llvmScalarUnrollPipeline, tm, pbo); err != nil {
		t.Fatalf("run scalar unroll pipeline: %v", err)
	}
	postUnrollIR := scriptModule.String()
	if !strings.Contains(postUnrollIR, "llvm.loop.unroll.disable") {
		t.Fatalf("expected unroll-disable metadata in real Pluto post-unroll IR:\n%s", postUnrollIR)
	}
	// The 4x unroll replicates the recurrence add inside the metadata-marked
	// latch, so the same-block add chain must grow past its pre-unroll length.
	// Checking structure instead of SSA names keeps the assertion independent
	// of how each target inlines Fib/FibAux.
	postUnrollChain := maxChainedAddsInMarkedLatch(scriptModule)
	if postUnrollChain < 3 || postUnrollChain <= preUnrollChain {
		t.Fatalf("expected unrolled recurrence in real Pluto post-unroll IR: chained adds %d -> %d:\n%s", preUnrollChain, postUnrollChain, postUnrollIR)
	}
}

// maxChainedAddsInMarkedLatch returns, across all loop latches that carry
// llvm.loop metadata, the largest number of add instructions that consume
// another add from the same block. An unrolled scalar recurrence leaves its
// replicated adds chained together inside the marked latch, so this grows
// when the annotated loop is actually unrolled.
func maxChainedAddsInMarkedLatch(module llvm.Module) int {
	loopMDKind := module.Context().MDKindID("llvm.loop")
	best := 0
	for fn := module.FirstFunction(); !fn.IsNil(); fn = llvm.NextFunction(fn) {
		for bb := fn.FirstBasicBlock(); !bb.IsNil(); bb = llvm.NextBasicBlock(bb) {
			term := bb.LastInstruction()
			if term.IsNil() || term.Metadata(loopMDKind).IsNil() {
				continue
			}
			if chained := chainedAddsInBlock(bb); chained > best {
				best = chained
			}
		}
	}
	return best
}

// chainedAddsInBlock counts the add instructions in bb that consume another
// add from the same block. Unrolling a scalar recurrence replicates its add
// and chains the copies together, so this grows with the unroll factor.
func chainedAddsInBlock(bb llvm.BasicBlock) int {
	adds := make(map[llvm.Value]bool)
	chained := 0
	for inst := bb.FirstInstruction(); !inst.IsNil(); inst = llvm.NextInstruction(inst) {
		if inst.InstructionOpcode() != llvm.Add {
			continue
		}
		for i := 0; i < inst.OperandsCount(); i++ {
			if adds[inst.Operand(i)] {
				chained++
				break
			}
		}
		adds[inst] = true
	}
	return chained
}
