package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"tinygo.org/x/go-llvm"
)

const llvmOptPipeline = "default<O3>"
const llvmScalarUnrollPipeline = "function(loop-unroll<O3>)"
const llvmScalarUnrollCount = 4

// Keep the forced unroll hint focused on small scalar recurrences. These limits
// fit the post-inline fib_tail recurrence with headroom; broader loops stay
// under LLVM's normal O3 cost model.
const (
	scalarUnrollMaxBlocks       = 5
	scalarUnrollMaxInstructions = 32
	scalarUnrollMaxMemoryOps    = 4
)

var (
	llvmCodegenInitOnce sync.Once
	llvmCodegenInitErr  error
)

func ensureLLVMCodegenInitialized() error {
	llvmCodegenInitOnce.Do(func() {
		if err := llvm.InitializeNativeTarget(); err != nil {
			llvmCodegenInitErr = fmt.Errorf("initialize native LLVM target: %w", err)
			return
		}
		if err := llvm.InitializeNativeAsmPrinter(); err != nil {
			llvmCodegenInitErr = fmt.Errorf("initialize native LLVM asm printer: %w", err)
		}
	})
	return llvmCodegenInitErr
}

func (cfg buildConfig) llvmTargetCPU() string {
	if cfg.targetCPU.disabled {
		return ""
	}
	if strings.EqualFold(cfg.targetCPU.bare, "native") {
		return llvmHostCPUName()
	}
	return cfg.targetCPU.bare
}

func (cfg buildConfig) llvmTargetFeatures() string {
	if cfg.targetCPU.disabled || !strings.EqualFold(cfg.targetCPU.bare, "native") {
		return ""
	}
	return llvmHostCPUFeatures()
}

func (cfg buildConfig) llvmRelocMode() llvm.RelocMode {
	if runtime.GOOS == OS_WINDOWS {
		return llvm.RelocDefault
	}
	return llvm.RelocPIC
}

func (cfg buildConfig) newTargetMachine() (llvm.TargetMachine, error) {
	if err := ensureLLVMCodegenInitialized(); err != nil {
		return llvm.TargetMachine{}, err
	}

	triple := llvm.DefaultTargetTriple()
	if triple == "" {
		return llvm.TargetMachine{}, fmt.Errorf("LLVM default target triple is empty")
	}

	target, err := llvm.GetTargetFromTriple(triple)
	if err != nil {
		return llvm.TargetMachine{}, fmt.Errorf("get LLVM target for %q: %w", triple, err)
	}

	tm := target.CreateTargetMachine(
		triple,
		cfg.llvmTargetCPU(),
		cfg.llvmTargetFeatures(),
		llvm.CodeGenLevelAggressive,
		cfg.llvmRelocMode(),
		llvm.CodeModelDefault,
	)
	if tm.C == nil {
		return llvm.TargetMachine{}, fmt.Errorf("create LLVM target machine for %q", triple)
	}
	return tm, nil
}

func (p *Pluto) emitObject(module llvm.Module, objFile string) error {
	tm, err := p.Config.newTargetMachine()
	if err != nil {
		return err
	}
	defer tm.Dispose()

	pbo := llvm.NewPassBuilderOptions()
	defer pbo.Dispose()
	// Match the tuning knobs enabled by the old external `opt -O3` pipeline.
	pbo.SetLoopInterleaving(true)
	pbo.SetLoopVectorization(true)
	pbo.SetSLPVectorization(true)
	pbo.SetLoopUnrolling(true)
	if err := module.RunPasses(llvmOptPipeline, tm, pbo); err != nil {
		return fmt.Errorf("optimize LLVM module with %s: %w", llvmOptPipeline, err)
	}
	if annotateScalarUnrollLoops(module) > 0 {
		if err := module.RunPasses(llvmScalarUnrollPipeline, tm, pbo); err != nil {
			return fmt.Errorf("optimize LLVM module with %s: %w", llvmScalarUnrollPipeline, err)
		}
	}

	obj, err := tm.EmitToMemoryBuffer(module, llvm.ObjectFile)
	if err != nil {
		return fmt.Errorf("emit object file: %w", err)
	}
	defer obj.Dispose()

	if err := os.MkdirAll(filepath.Dir(objFile), 0755); err != nil {
		return fmt.Errorf("create script cache dir: %w", err)
	}
	if err := os.WriteFile(objFile, obj.Bytes(), 0644); err != nil {
		return fmt.Errorf("write object file %s: %w", objFile, err)
	}
	return nil
}

type scalarUnrollLoop struct {
	header llvm.BasicBlock
	latch  llvm.BasicBlock
	term   llvm.Value
	blocks map[llvm.BasicBlock]struct{}
}

func annotateScalarUnrollLoops(module llvm.Module) int {
	ctx := module.Context()
	loopMDKind := ctx.MDKindID("llvm.loop")
	annotated := 0

	for fn := module.FirstFunction(); !fn.IsNil(); fn = llvm.NextFunction(fn) {
		loops := scalarUnrollCandidates(fn)
		for _, loop := range loops {
			if !loop.term.Metadata(loopMDKind).IsNil() {
				continue
			}
			loop.term.SetMetadata(loopMDKind, llvmUnrollCountMetadata(ctx, llvmScalarUnrollCount))
			annotated++
		}
	}

	return annotated
}

func scalarUnrollCandidates(fn llvm.Value) []scalarUnrollLoop {
	if fn.BasicBlocksCount() == 0 {
		return nil
	}
	blocks := fn.BasicBlocks()
	if len(blocks) == 0 {
		return nil
	}

	preds := make(map[llvm.BasicBlock][]llvm.BasicBlock, len(blocks))
	succs := make(map[llvm.BasicBlock][]llvm.BasicBlock, len(blocks))
	for _, bb := range blocks {
		term := bb.LastInstruction()
		for _, succ := range branchSuccessors(term) {
			succs[bb] = append(succs[bb], succ)
			preds[succ] = append(preds[succ], bb)
		}
	}
	entry := fn.EntryBasicBlock()
	if entry.IsNil() {
		return nil
	}
	doms := computeDominators(blocks, preds, entry)

	var loops []scalarUnrollLoop
	for _, latch := range blocks {
		term := latch.LastInstruction()
		if term.IsNil() || term.InstructionOpcode() != llvm.Br {
			continue
		}
		for _, header := range succs[latch] {
			if !dominates(doms, header, latch) {
				continue
			}
			loopBlocks := collectNaturalLoop(header, latch, preds)
			loop := scalarUnrollLoop{
				header: header,
				latch:  latch,
				term:   term,
				blocks: loopBlocks,
			}
			if loopIsWorthScalarUnroll(loop, preds, succs, doms) {
				loops = append(loops, loop)
			}
		}
	}

	return loops
}

func branchSuccessors(term llvm.Value) []llvm.BasicBlock {
	if term.IsNil() || term.InstructionOpcode() != llvm.Br {
		return nil
	}

	succs := make([]llvm.BasicBlock, 0, 2)
	for i := 0; i < term.OperandsCount(); i++ {
		op := term.Operand(i)
		if op.IsNil() || !op.IsBasicBlock() {
			continue
		}
		succ := op.AsBasicBlock()
		if !succ.IsNil() {
			succs = append(succs, succ)
		}
	}
	return succs
}

func computeDominators(blocks []llvm.BasicBlock, preds map[llvm.BasicBlock][]llvm.BasicBlock, entry llvm.BasicBlock) map[llvm.BasicBlock]map[llvm.BasicBlock]struct{} {
	doms := make(map[llvm.BasicBlock]map[llvm.BasicBlock]struct{}, len(blocks))
	if len(blocks) == 0 {
		return doms
	}

	allBlocks := blockSet(blocks...)
	for _, bb := range blocks {
		if bb == entry {
			doms[bb] = blockSet(bb)
			continue
		}
		doms[bb] = cloneBlockSet(allBlocks)
	}

	changed := true
	for changed {
		changed = false
		for _, bb := range blocks {
			if bb == entry {
				continue
			}
			predDoms := make([]map[llvm.BasicBlock]struct{}, 0, len(preds[bb]))
			for _, pred := range preds[bb] {
				if predDom, ok := doms[pred]; ok {
					predDoms = append(predDoms, predDom)
				}
			}

			next := intersectBlockSets(predDoms)
			next[bb] = struct{}{}
			if !sameBlockSet(doms[bb], next) {
				doms[bb] = next
				changed = true
			}
		}
	}

	return doms
}

func dominates(doms map[llvm.BasicBlock]map[llvm.BasicBlock]struct{}, dominator, bb llvm.BasicBlock) bool {
	bbDoms, ok := doms[bb]
	if !ok {
		return false
	}
	_, ok = bbDoms[dominator]
	return ok
}

func blockSet(blocks ...llvm.BasicBlock) map[llvm.BasicBlock]struct{} {
	set := make(map[llvm.BasicBlock]struct{}, len(blocks))
	for _, bb := range blocks {
		set[bb] = struct{}{}
	}
	return set
}

func cloneBlockSet(set map[llvm.BasicBlock]struct{}) map[llvm.BasicBlock]struct{} {
	clone := make(map[llvm.BasicBlock]struct{}, len(set))
	for bb := range set {
		clone[bb] = struct{}{}
	}
	return clone
}

func intersectBlockSets(sets []map[llvm.BasicBlock]struct{}) map[llvm.BasicBlock]struct{} {
	if len(sets) == 0 {
		return map[llvm.BasicBlock]struct{}{}
	}

	intersection := cloneBlockSet(sets[0])
	for _, set := range sets[1:] {
		for bb := range intersection {
			if _, ok := set[bb]; !ok {
				delete(intersection, bb)
			}
		}
	}
	return intersection
}

func sameBlockSet(a, b map[llvm.BasicBlock]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for bb := range a {
		if _, ok := b[bb]; !ok {
			return false
		}
	}
	return true
}

func collectNaturalLoop(header, latch llvm.BasicBlock, preds map[llvm.BasicBlock][]llvm.BasicBlock) map[llvm.BasicBlock]struct{} {
	loopBlocks := map[llvm.BasicBlock]struct{}{
		header: {},
		latch:  {},
	}
	worklist := []llvm.BasicBlock{latch}

	for len(worklist) > 0 {
		bb := worklist[len(worklist)-1]
		worklist = worklist[:len(worklist)-1]
		if bb == header {
			continue
		}
		for _, pred := range preds[bb] {
			if _, ok := loopBlocks[pred]; ok {
				continue
			}
			loopBlocks[pred] = struct{}{}
			worklist = append(worklist, pred)
		}
	}

	return loopBlocks
}

func loopIsWorthScalarUnroll(loop scalarUnrollLoop, preds, succs map[llvm.BasicBlock][]llvm.BasicBlock, doms map[llvm.BasicBlock]map[llvm.BasicBlock]struct{}) bool {
	if len(loop.blocks) == 0 || len(loop.blocks) > scalarUnrollMaxBlocks {
		return false
	}

	stats := scalarLoopStats{}
	for bb := range loop.blocks {
		for inst := bb.FirstInstruction(); !inst.IsNil(); inst = llvm.NextInstruction(inst) {
			stats.observe(inst)
			if stats.instructions > scalarUnrollMaxInstructions ||
				stats.memoryOps > scalarUnrollMaxMemoryOps ||
				stats.calls > 0 ||
				stats.expensiveOps > 0 ||
				stats.vectorOps > 0 ||
				stats.unsupported > 0 {
				return false
			}
		}
		for _, succ := range succs[bb] {
			if _, inLoop := loop.blocks[succ]; inLoop {
				// In a single-block loop the latch-to-header backedge is a
				// self-edge; dominance alone cannot distinguish it from an
				// extra in-loop backedge.
				if dominates(doms, succ, bb) && !(bb == loop.latch && succ == loop.header) {
					return false
				}
				continue
			}
			stats.exits++
		}
		for _, pred := range preds[bb] {
			if _, inLoop := loop.blocks[pred]; !inLoop && bb != loop.header {
				return false
			}
		}
	}

	return stats.phis > 0 && stats.exits > 0 && stats.exits <= 2
}

type scalarLoopStats struct {
	instructions int
	phis         int
	calls        int
	memoryOps    int
	expensiveOps int
	vectorOps    int
	unsupported  int
	exits        int
}

func (s *scalarLoopStats) observe(inst llvm.Value) {
	s.instructions++
	if valueUsesVectorType(inst) {
		s.vectorOps++
	}

	switch inst.InstructionOpcode() {
	case llvm.PHI:
		s.phis++
	case llvm.Call:
		if !isAllowedScalarUnrollCall(inst) {
			s.calls++
		}
	case llvm.Load, llvm.Store, llvm.GetElementPtr:
		s.memoryOps++
	case llvm.UDiv, llvm.SDiv, llvm.URem, llvm.SRem, llvm.FRem:
		s.expensiveOps++
	case llvm.Invoke, llvm.IndirectBr, llvm.Switch, llvm.Ret, llvm.Resume, llvm.LandingPad,
		llvm.CleanupRet, llvm.CatchRet, llvm.CatchPad, llvm.CleanupPad, llvm.CatchSwitch,
		llvm.VAArg, llvm.ExtractElement, llvm.InsertElement, llvm.ShuffleVector:
		s.unsupported++
	}
}

func isAllowedScalarUnrollCall(inst llvm.Value) bool {
	called := inst.CalledValue()
	if called.IsNil() {
		return false
	}
	name := called.Name()
	// Keep this whitelist narrow until Pluto emits more side-effect-free
	// intrinsics in scalar loops.
	return strings.HasPrefix(name, "llvm.assume") || strings.HasPrefix(name, "llvm.lifetime.")
}

func valueUsesVectorType(v llvm.Value) bool {
	if !v.Type().IsNil() && v.Type().TypeKind() == llvm.VectorTypeKind {
		return true
	}
	for i := 0; i < v.OperandsCount(); i++ {
		op := v.Operand(i)
		if !op.IsNil() && !op.Type().IsNil() && op.Type().TypeKind() == llvm.VectorTypeKind {
			return true
		}
	}
	return false
}

func llvmUnrollCountMetadata(ctx llvm.Context, count int) llvm.Metadata {
	temp := ctx.TemporaryMDNode(nil)
	countMD := llvm.ConstInt(ctx.Int32Type(), uint64(count), false).ConstantAsMetadata()
	countNode := ctx.MDNode([]llvm.Metadata{
		ctx.MDString("llvm.loop.unroll.count"),
		countMD,
	})
	loopID := ctx.MDNode([]llvm.Metadata{temp, countNode})
	temp.ReplaceAllUsesWith(loopID)
	return loopID
}
