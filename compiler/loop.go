package compiler

import (
	"fmt"
	"maps"

	"github.com/thiremani/pluto/ast"
	"tinygo.org/x/go-llvm"
)

type Loop struct {
	Iter llvm.Value
	Body *llvm.BasicBlock
	Exit *llvm.BasicBlock
}

// rangeSymbol loads a range aggregate from a symbol when needed.
func (c *Compiler) rangeSymbol(sym *Symbol, name string) *Symbol {
	switch t := sym.Type.(type) {
	case Range:
		return sym
	case Ptr:
		rangeType, ok := t.Elem.(Range)
		if ok {
			return &Symbol{
				Val:      c.createLoad(sym.Val, rangeType, name+"_range"),
				Type:     rangeType,
				FuncArg:  sym.FuncArg,
				Borrowed: true,
				ReadOnly: sym.ReadOnly,
			}
		}
	}

	panic(fmt.Sprintf("internal: %q is not a Range during lowering (got %s)", name, sym.Type.String()))
}

// loopRangeValues builds each driver's range once, ahead of its loop nest. A
// range literal's bounds resolve there like any operands: when one fails to
// yield, its range is empty and its loop runs no iterations.
func (c *Compiler) loopRangeValues(ranges []*RangeInfo) []*Symbol {
	values := make([]*Symbol, len(ranges))
	for i, ri := range ranges {
		if ri.RangeLit != nil {
			values[i] = c.rangeLiteralValue(ri.RangeLit)
			continue
		}

		sym, ok := c.getRawSymbol(ri.Name)
		if !ok {
			panic(fmt.Sprintf("internal: range driver %q not found in scope (should have been caught by type solver)", ri.Name))
		}
		values[i] = c.rangeSymbol(sym, ri.Name)
	}
	return values
}

// rangeLiteralValue builds an iterated range literal's range: empty unless
// every bound yields, through its conditions and checked accesses alike.
func (c *Compiler) rangeLiteralValue(lit *ast.RangeLiteral) *Symbol {
	rangeType := Range{Iter: Int{Width: 64}}
	empty := c.makeZeroValue(rangeType).Val
	slot := c.createEntryBlockAlloca(c.mapToLLVMType(rangeType), "range_lit")
	c.createStore(empty, slot, rangeType)

	c.compileCondOperands(lit, func() {
		guardPtr := c.pushBoundsGuard("range_bounds_guard")
		rangeVal := c.ToRange(lit, rangeType)
		if c.stmtBoundsUsed() {
			inBounds := c.createLoad(guardPtr, Int{Width: 1}, "range_bounds_ok")
			rangeVal = c.builder.CreateSelect(inBounds, rangeVal, empty, "range_or_empty")
		}
		c.popBoundsGuard()
		c.createStore(rangeVal, slot, rangeType)
	})
	return &Symbol{Val: c.createLoad(slot, rangeType, "range_lit_val"), Type: rangeType}
}

// Build a nested loop over specs; at each level shadow specs[i].Name with the scalar iter; run body at innermost.
func (c *Compiler) withLoopNest(ranges []*RangeInfo, body func()) {
	ranges = c.pendingLoopRanges(ranges)
	c.withLoopNestOver(ranges, c.loopRangeValues(ranges), body)
}

// withLoopNestOver runs a loop nest over driver ranges built ahead of it.
func (c *Compiler) withLoopNestOver(ranges []*RangeInfo, values []*Symbol, body func()) {
	if len(ranges) == 0 {
		body()
		return
	}
	var rec func(i int)
	rec = func(i int) {
		if i == len(ranges) {
			body()
			return
		}
		rangeSym := values[i]
		c.iterOverRange(rangeSym.Type.(Range), rangeSym.Val, func(iter llvm.Value, iterType Type) {
			PushScope(&c.Scopes, BlockScope)
			Put(c.Scopes, ranges[i].Name, &Symbol{Val: iter, Type: iterType, FuncArg: rangeSym.FuncArg, Borrowed: true})
			rec(i + 1)
			c.popScope()
		})
	}
	rec(0)
}

func (c *Compiler) pendingLoopRanges(ranges []*RangeInfo) []*RangeInfo {
	if len(ranges) == 0 {
		return ranges
	}
	filtered := make([]*RangeInfo, 0, len(ranges))
	for _, ri := range ranges {
		sym, ok := c.getRawSymbol(ri.Name)
		if ok {
			driverType := sym.Type
			if ptrType, ok := sym.Type.(Ptr); ok {
				driverType = ptrType.Elem
			}
			if !isRangeDriverType(driverType) {
				continue
			}
		}
		filtered = append(filtered, ri)
	}
	return filtered
}

func (c *Compiler) pushLoopBoundsMode(mode loopBoundsMode, fast map[*ast.ArrayRangeExpression]struct{}) {
	ctx := c.currentStmtCtx()
	ctx.loopBoundsStack = append(ctx.loopBoundsStack, loopBoundsFrame{
		mode:       mode,
		fastAccess: fast,
		slotChecks: maps.Clone(ctx.slotChecks),
	})
}

// popLoopBoundsMode restores the slot checks: each version of a loop lowers the
// same expressions, and the checks one marks are values of its own blocks.
func (c *Compiler) popLoopBoundsMode() {
	ctx := c.currentStmtCtx()
	last := len(ctx.loopBoundsStack) - 1
	ctx.slotChecks = ctx.loopBoundsStack[last].slotChecks
	ctx.loopBoundsStack = ctx.loopBoundsStack[:last]
}

func (c *Compiler) currentLoopBoundsMode() loopBoundsMode {
	ctx := c.currentStmtCtx()
	if len(ctx.loopBoundsStack) == 0 {
		return loopBoundsModeDefault
	}
	return ctx.loopBoundsStack[len(ctx.loopBoundsStack)-1].mode
}

func (c *Compiler) isFastAffineAccess(expr *ast.ArrayRangeExpression) bool {
	ctx := c.currentStmtCtx()
	if len(ctx.loopBoundsStack) == 0 {
		return false
	}
	frame := ctx.loopBoundsStack[len(ctx.loopBoundsStack)-1]
	if frame.mode != loopBoundsModeAffineFast || len(frame.fastAccess) == 0 {
		return false
	}
	_, ok := frame.fastAccess[expr]
	return ok
}

func (c *Compiler) withLoopNestVersioned(ranges []*RangeInfo, probes []ast.Expression, body func()) {
	pending := c.pendingLoopRanges(ranges)
	if len(pending) == 0 {
		body()
		return
	}

	values := c.loopRangeValues(pending)
	guard, fastAccess, ok := c.affineVersioningGuard(probes, pending, values)
	if !ok {
		c.withLoopNestOver(pending, values, body)
		return
	}

	fastBlock, checkedBlock, contBlock := c.createIfElseCont(guard, "loop_affine_fast", "loop_affine_checked", "loop_affine_cont")

	c.builder.SetInsertPointAtEnd(fastBlock)
	c.pushLoopBoundsMode(loopBoundsModeAffineFast, fastAccess)
	c.withLoopNestOver(pending, values, body)
	c.popLoopBoundsMode()
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(checkedBlock)
	c.pushLoopBoundsMode(loopBoundsModeChecked, nil)
	c.withLoopNestOver(pending, values, body)
	c.popLoopBoundsMode()
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
}

// rangeLoop is one loop over a range's elements, threading an optional
// loop-carried state from seed through bodyGen.
type rangeLoop struct {
	start, stop, step llvm.Value
	seed              llvm.Value
	stateType         llvm.Type
	hasState          bool
	bodyGen           func(iter llvm.Value, current llvm.Value) llvm.Value
}

// createLoopCore runs bodyGen once per element of range r. The range ends
// where its next element would pass stop, even when computing that element
// would overflow (#156): a constant step of 1 or -1 cannot overflow before it
// passes stop, and any other step is tested before it is taken.
func (c *Compiler) createLoopCore(r llvm.Value, seed llvm.Value, stateType llvm.Type, hasState bool, bodyGen func(iter llvm.Value, current llvm.Value) llvm.Value) llvm.Value {
	start, stop, step := c.rangeComponents(r)
	if step.IsUndef() || start.IsUndef() || stop.IsUndef() {
		panic("range aggregate is undefined; likely received non-range value")
	}

	loop := rangeLoop{start: start, stop: stop, step: step, seed: seed, stateType: stateType, hasState: hasState, bodyGen: bodyGen}
	if unit, ok := unitStep(step); ok {
		return c.createUnitStepLoop(loop, unit < 0)
	}
	return c.createTestedStepLoop(loop)
}

// unitStep returns a constant step of 1 or -1.
func unitStep(step llvm.Value) (int64, bool) {
	if step.IsAConstantInt().IsNil() {
		return 0, false
	}
	value := step.SExtValue()
	return value, value == 1 || value == -1
}

// createUnitStepLoop tests each element against stop before running it. An
// element runs only below stop <= INT64_MAX, so adding 1 to it cannot
// overflow; a step of -1 mirrors this above INT64_MIN.
func (c *Compiler) createUnitStepLoop(loop rangeLoop, descending bool) llvm.Value {
	preheader := c.builder.GetInsertBlock()
	fn := preheader.Parent()
	cond := c.Context.AddBasicBlock(fn, "loop_cond")
	body := c.Context.AddBasicBlock(fn, "loop_body")
	exit := c.Context.AddBasicBlock(fn, "loop_exit")
	predicate := llvm.IntSLT
	if descending {
		predicate = llvm.IntSGT
	}
	c.builder.CreateBr(cond)

	c.builder.SetInsertPointAtEnd(cond)
	iter, current := c.loopPhis(loop, preheader)
	c.builder.CreateCondBr(c.builder.CreateICmp(predicate, iter, loop.stop, "loop_in_range"), body, exit)

	c.builder.SetInsertPointAtEnd(body)
	nextState := loop.bodyGen(iter, current)
	latch := c.builder.GetInsertBlock()
	iterNext := c.builder.CreateAdd(iter, loop.step, "iter_next")
	c.builder.CreateBr(cond)
	c.continueLoop(loop, iter, current, iterNext, nextState, latch)

	c.builder.SetInsertPointAtEnd(exit)
	return current
}

// createTestedStepLoop steps only when another element follows: iter + step
// before stop, tested as iter against stop - step, which saturates where no
// element can follow. A zero step runs no iteration. The direction is a branch,
// not a select, so LLVM can unswitch a loop whose step sign it cannot see.
func (c *Compiler) createTestedStepLoop(loop rangeLoop) llvm.Value {
	preheader := c.builder.GetInsertBlock()
	fn := preheader.Parent()
	body := c.Context.AddBasicBlock(fn, "loop_body")
	testAsc := c.Context.AddBasicBlock(fn, "loop_test_asc")
	testDesc := c.Context.AddBasicBlock(fn, "loop_test_desc")
	latch := c.Context.AddBasicBlock(fn, "loop_latch")
	exit := c.Context.AddBasicBlock(fn, "loop_exit")
	descending, nonEmpty := c.rangeDirection(loop.start, loop.stop, loop.step)
	lastBound := c.saturatingSub(loop.stop, loop.step, "loop_last_bound")
	c.builder.CreateCondBr(nonEmpty, body, exit)

	c.builder.SetInsertPointAtEnd(body)
	iter, current := c.loopPhis(loop, preheader)
	nextState := loop.bodyGen(iter, current)
	c.builder.CreateCondBr(descending, testDesc, testAsc)

	c.builder.SetInsertPointAtEnd(testAsc)
	c.builder.CreateCondBr(c.builder.CreateICmp(llvm.IntSLT, iter, lastBound, "loop_more_asc"), latch, exit)

	c.builder.SetInsertPointAtEnd(testDesc)
	c.builder.CreateCondBr(c.builder.CreateICmp(llvm.IntSGT, iter, lastBound, "loop_more_desc"), latch, exit)

	// Another element follows, so this increment yields it and cannot overflow.
	c.builder.SetInsertPointAtEnd(latch)
	iterNext := c.builder.CreateNSWAdd(iter, loop.step, "iter_next")
	c.builder.CreateBr(body)
	c.continueLoop(loop, iter, current, iterNext, nextState, latch)

	c.builder.SetInsertPointAtEnd(exit)
	if !loop.hasState {
		return llvm.Value{}
	}
	final := c.builder.CreatePHI(loop.stateType, "loop_final")
	final.AddIncoming([]llvm.Value{loop.seed, nextState, nextState}, []llvm.BasicBlock{preheader, testAsc, testDesc})
	return final
}

// loopPhis opens a loop header with the element and the carried state.
func (c *Compiler) loopPhis(loop rangeLoop, preheader llvm.BasicBlock) (iter, current llvm.Value) {
	iter = c.builder.CreatePHI(c.Context.Int64Type(), "iter")
	iter.AddIncoming([]llvm.Value{loop.start}, []llvm.BasicBlock{preheader})
	if loop.hasState {
		current = c.builder.CreatePHI(loop.stateType, "loop_state")
		current.AddIncoming([]llvm.Value{loop.seed}, []llvm.BasicBlock{preheader})
	}
	return iter, current
}

// continueLoop feeds the next element and carried state back from latch.
func (c *Compiler) continueLoop(loop rangeLoop, iter, current, iterNext, nextState llvm.Value, latch llvm.BasicBlock) {
	iter.AddIncoming([]llvm.Value{iterNext}, []llvm.BasicBlock{latch})
	if loop.hasState {
		current.AddIncoming([]llvm.Value{nextState}, []llvm.BasicBlock{latch})
	}
}

// rangeDirection reports whether a range steps downward, and whether it holds
// an element; a zero step holds none.
func (c *Compiler) rangeDirection(start, stop, step llvm.Value) (descending, nonEmpty llvm.Value) {
	zero := c.ConstI64(0)
	descending = c.builder.CreateICmp(llvm.IntSLT, step, zero, "step_is_neg")
	ascends := c.builder.CreateAnd(
		c.builder.CreateICmp(llvm.IntSGT, step, zero, "step_is_pos"),
		c.builder.CreateICmp(llvm.IntSLT, start, stop, "start_below_stop"),
		"range_ascends",
	)
	descends := c.builder.CreateAnd(
		descending,
		c.builder.CreateICmp(llvm.IntSGT, start, stop, "start_above_stop"),
		"range_descends",
	)
	nonEmpty = c.builder.CreateOr(ascends, descends, "range_nonempty")
	return descending, nonEmpty
}

// saturatingSub emits a - b clamped to the I64 range.
func (c *Compiler) saturatingSub(a, b llvm.Value, name string) llvm.Value {
	i64 := c.Context.Int64Type()
	fnType := llvm.FunctionType(i64, []llvm.Type{i64, i64}, false)
	return c.builder.CreateCall(fnType, c.intrinsic("llvm.ssub.sat.i64", fnType), []llvm.Value{a, b}, name)
}

// checkedArith emits an I64 overflow intrinsic, such as
// llvm.smul.with.overflow.i64, and returns its result and overflow bit.
func (c *Compiler) checkedArith(intrinsic string, a, b llvm.Value, name string) (result, overflow llvm.Value) {
	i64 := c.Context.Int64Type()
	pairType := c.Context.StructType([]llvm.Type{i64, c.Context.Int1Type()}, false)
	fnType := llvm.FunctionType(pairType, []llvm.Type{i64, i64}, false)
	pair := c.builder.CreateCall(fnType, c.intrinsic(intrinsic, fnType), []llvm.Value{a, b}, name)
	return c.builder.CreateExtractValue(pair, 0, name+"_value"), c.builder.CreateExtractValue(pair, 1, name+"_overflow")
}

// intrinsic declares an LLVM intrinsic in the module once.
func (c *Compiler) intrinsic(name string, fnType llvm.Type) llvm.Value {
	fn := c.Module.NamedFunction(name)
	if fn.IsNil() {
		fn = llvm.AddFunction(c.Module, name, fnType)
	}
	return fn
}

func (c *Compiler) createLoop(r llvm.Value, bodyGen func(iter llvm.Value)) {
	c.createLoopCore(r, llvm.Value{}, llvm.Type{}, false, func(iter llvm.Value, _ llvm.Value) llvm.Value {
		bodyGen(iter)
		return llvm.Value{}
	})
}
