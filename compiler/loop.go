package compiler

import (
	"fmt"

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
	})
}

func (c *Compiler) popLoopBoundsMode() {
	ctx := c.currentStmtCtx()
	ctx.loopBoundsStack = ctx.loopBoundsStack[:len(ctx.loopBoundsStack)-1]
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

func (c *Compiler) createLoopCore(r llvm.Value, seed llvm.Value, stateType llvm.Type, hasState bool, bodyGen func(iter llvm.Value, current llvm.Value) llvm.Value) llvm.Value {
	start, stop, step := c.rangeComponents(r)
	if step.IsUndef() || start.IsUndef() || stop.IsUndef() {
		panic("range aggregate is undefined; likely received non-range value")
	}
	if step.IsConstant() && step.ZExtValue() == 0 {
		panic("range step is zero; would loop forever")
	}

	preheader := c.builder.GetInsertBlock()
	fn := preheader.Parent()

	condPos := c.Context.AddBasicBlock(fn, "loop_cond_pos")
	condNeg := c.Context.AddBasicBlock(fn, "loop_cond_neg")
	body := c.Context.AddBasicBlock(fn, "loop_body")
	exit := c.Context.AddBasicBlock(fn, "loop_exit")

	// Preheader: compute sign and dispatch once
	zero := llvm.ConstInt(c.Context.Int64Type(), 0, false)
	isNeg := c.builder.CreateICmp(llvm.IntSLT, step, zero, "step_is_neg")
	c.builder.CreateCondBr(isNeg, condNeg, condPos)

	// condPos: iter < stop
	c.builder.SetInsertPointAtEnd(condPos)
	iterPos := c.builder.CreatePHI(c.Context.Int64Type(), "iter_pos")
	iterPos.AddIncoming([]llvm.Value{start}, []llvm.BasicBlock{preheader})
	var statePos llvm.Value
	if hasState {
		statePos = c.builder.CreatePHI(stateType, "state_pos")
		statePos.AddIncoming([]llvm.Value{seed}, []llvm.BasicBlock{preheader})
	}
	cmpPos := c.builder.CreateICmp(llvm.IntSLT, iterPos, stop, "loop_cond_pos")
	c.builder.CreateCondBr(cmpPos, body, exit)

	// condNeg: iter > stop
	c.builder.SetInsertPointAtEnd(condNeg)
	iterNeg := c.builder.CreatePHI(c.Context.Int64Type(), "iter_neg")
	iterNeg.AddIncoming([]llvm.Value{start}, []llvm.BasicBlock{preheader})
	var stateNeg llvm.Value
	if hasState {
		stateNeg = c.builder.CreatePHI(stateType, "state_neg")
		stateNeg.AddIncoming([]llvm.Value{seed}, []llvm.BasicBlock{preheader})
	}
	cmpNeg := c.builder.CreateICmp(llvm.IntSGT, iterNeg, stop, "loop_cond_neg")
	c.builder.CreateCondBr(cmpNeg, body, exit)

	// Body
	c.builder.SetInsertPointAtEnd(body)
	iter := c.builder.CreatePHI(c.Context.Int64Type(), "iter")
	iter.AddIncoming([]llvm.Value{iterPos}, []llvm.BasicBlock{condPos})
	iter.AddIncoming([]llvm.Value{iterNeg}, []llvm.BasicBlock{condNeg})
	var current llvm.Value
	if hasState {
		current = c.builder.CreatePHI(stateType, "loop_state")
		current.AddIncoming([]llvm.Value{statePos}, []llvm.BasicBlock{condPos})
		current.AddIncoming([]llvm.Value{stateNeg}, []llvm.BasicBlock{condNeg})
	}

	nextState := bodyGen(iter, current)

	latch := c.builder.GetInsertBlock()
	iterNext := c.builder.CreateAdd(iter, step, "iter_next")
	c.builder.CreateCondBr(isNeg, condNeg, condPos)

	iterPos.AddIncoming([]llvm.Value{iterNext}, []llvm.BasicBlock{latch})
	iterNeg.AddIncoming([]llvm.Value{iterNext}, []llvm.BasicBlock{latch})
	if hasState {
		statePos.AddIncoming([]llvm.Value{nextState}, []llvm.BasicBlock{latch})
		stateNeg.AddIncoming([]llvm.Value{nextState}, []llvm.BasicBlock{latch})
	}

	c.builder.SetInsertPointAtEnd(exit)
	if !hasState {
		return llvm.Value{}
	}
	finalState := c.builder.CreatePHI(stateType, "loop_final")
	finalState.AddIncoming([]llvm.Value{statePos}, []llvm.BasicBlock{condPos})
	finalState.AddIncoming([]llvm.Value{stateNeg}, []llvm.BasicBlock{condNeg})
	return finalState
}

func (c *Compiler) createLoop(r llvm.Value, bodyGen func(iter llvm.Value)) {
	c.createLoopCore(r, llvm.Value{}, llvm.Type{}, false, func(iter llvm.Value, _ llvm.Value) llvm.Value {
		bodyGen(iter)
		return llvm.Value{}
	})
}
