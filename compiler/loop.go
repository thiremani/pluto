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

// extractRangeSymbol loads a range aggregate from a symbol when needed.
func (c *Compiler) extractRangeSymbol(sym *Symbol, name string) (*Symbol, bool) {
	switch t := sym.Type.(type) {
	case Range:
		return sym, true
	case Ptr:
		rangeType, ok := t.Elem.(Range)
		if ok {
			return &Symbol{
				Val:      c.createLoad(sym.Val, rangeType, name+"_range"),
				Type:     rangeType,
				FuncArg:  sym.FuncArg,
				Borrowed: true,
				ReadOnly: sym.ReadOnly,
			}, true
		}
	}
	return nil, false
}

// extractArrayRangeSymbol loads an array-range aggregate from a symbol when needed.
func (c *Compiler) extractArrayRangeSymbol(sym *Symbol, name string) (*Symbol, bool) {
	switch t := sym.Type.(type) {
	case ArrayRange:
		return sym, true
	case Ptr:
		arrRangeType, ok := t.Elem.(ArrayRange)
		if ok {
			return &Symbol{
				Val:      c.createLoad(sym.Val, arrRangeType, name+"_arrrange"),
				Type:     arrRangeType,
				FuncArg:  sym.FuncArg,
				Borrowed: true,
				ReadOnly: sym.ReadOnly,
			}, true
		}
	}
	return nil, false
}

func (c *Compiler) rangeAggregateFromSymbol(sym *Symbol, name string) llvm.Value {
	if rangeSym, ok := c.extractRangeSymbol(sym, name); ok {
		return rangeSym.Val
	}

	if arrRangeSym, ok := c.extractArrayRangeSymbol(sym, name); ok {
		return c.builder.CreateExtractValue(arrRangeSym.Val, 1, name+"_range")
	}

	panic(fmt.Sprintf("internal: %q is not a Range or ArrayRange during lowering (got %s)", name, sym.Type.String()))
}

func (c *Compiler) iterOverDriverSymbol(sym *Symbol, name string, body func(*Symbol)) {
	if rangeSym, ok := c.extractRangeSymbol(sym, name); ok {
		c.iterOverRange(rangeSym.Type.(Range), rangeSym.Val, func(iter llvm.Value, iterType Type) {
			body(&Symbol{
				Val:      iter,
				Type:     iterType,
				FuncArg:  rangeSym.FuncArg,
				Borrowed: true,
			})
		})
		return
	}

	if arrRangeSym, ok := c.extractArrayRangeSymbol(sym, name); ok {
		c.iterOverArrayRange(arrRangeSym, func(iter llvm.Value, iterType Type) {
			body(&Symbol{
				Val:      iter,
				Type:     iterType,
				FuncArg:  arrRangeSym.FuncArg,
				Borrowed: true,
			})
		})
		return
	}

	panic(fmt.Sprintf("internal: %q is not a Range or ArrayRange during lowering (got %s)", name, sym.Type.String()))
}

// rangeAggregateForRI builds the {start,stop,step} aggregate for a driver.
// Named ArrayRange drivers contribute their underlying range component.
func (c *Compiler) rangeAggregateForRI(ri *RangeInfo) llvm.Value {
	if ri.RangeLit != nil {
		return c.ToRange(ri.RangeLit, Range{Iter: Int{Width: 64}})
	}

	sym, ok := c.getRawSymbol(ri.Name)
	if !ok {
		panic(fmt.Sprintf("internal: range driver %q not found in scope (should have been caught by type solver)", ri.Name))
	}
	return c.rangeAggregateFromSymbol(sym, ri.Name)
}

// iterOverRangeInfo iterates a single driver, binding its per-iteration scalar value.
func (c *Compiler) iterOverRangeInfo(ri *RangeInfo, body func(*Symbol)) {
	if ri.RangeLit != nil {
		rangeType := Range{Iter: Int{Width: 64}}
		rangeVal := c.ToRange(ri.RangeLit, rangeType)
		c.iterOverRange(rangeType, rangeVal, func(iter llvm.Value, iterType Type) {
			body(&Symbol{Val: iter, Type: iterType, Borrowed: true})
		})
		return
	}

	sym, ok := c.getRawSymbol(ri.Name)
	if !ok {
		panic(fmt.Sprintf("internal: range driver %q not found in scope (should have been caught by type solver)", ri.Name))
	}
	c.iterOverDriverSymbol(sym, ri.Name, body)
}

// Build a nested loop over specs; at each level shadow specs[i].Name with the scalar iter; run body at innermost.
func (c *Compiler) withLoopNest(ranges []*RangeInfo, body func()) {
	ranges = c.pendingLoopRanges(ranges)
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
		c.iterOverRangeInfo(ranges[i], func(iterSym *Symbol) {
			PushScope(&c.Scopes, BlockScope)
			Put(c.Scopes, ranges[i].Name, iterSym)
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

	guard, fastAccess, ok := c.affineVersioningGuard(probes, pending)
	if !ok {
		c.withLoopNest(ranges, body)
		return
	}

	fastBlock, checkedBlock, contBlock := c.createIfElseCont(guard, "loop_affine_fast", "loop_affine_checked", "loop_affine_cont")

	c.builder.SetInsertPointAtEnd(fastBlock)
	c.pushLoopBoundsMode(loopBoundsModeAffineFast, fastAccess)
	c.withLoopNest(ranges, body)
	c.popLoopBoundsMode()
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(checkedBlock)
	c.pushLoopBoundsMode(loopBoundsModeChecked, nil)
	c.withLoopNest(ranges, body)
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

func (c *Compiler) createLoopState(r llvm.Value, seed llvm.Value, stateType llvm.Type, bodyGen func(iter llvm.Value, current llvm.Value) llvm.Value) llvm.Value {
	return c.createLoopCore(r, seed, stateType, true, bodyGen)
}
