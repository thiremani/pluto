package compiler

import (
	"fmt"

	"tinygo.org/x/go-llvm"
)

type Loop struct {
	Iter llvm.Value
	Body *llvm.BasicBlock
	Exit *llvm.BasicBlock
}

// extractRangeValue extracts the range aggregate from a symbol.
// Returns the range value and true if successful, or zero value and false if not a range type.
func (c *Compiler) extractRangeValue(sym *Symbol, name string) (llvm.Value, bool) {
	switch t := sym.Type.(type) {
	case Range:
		return sym.Val, true
	case Ptr:
		if t.Elem.Kind() == RangeKind {
			return c.createLoad(sym.Val, t.Elem, name+"_range"), true
		}
	}
	return llvm.Value{}, false
}

// rangeAggregateForRI builds the {start,stop,step} aggregate for either a literal or a named range.
func (c *Compiler) rangeAggregateForRI(ri *RangeInfo) llvm.Value {
	if ri.RangeLit != nil {
		return c.ToRange(ri.RangeLit, Range{Iter: Int{Width: 64}})
	}

	sym, ok := c.getRawSymbol(ri.Name)
	if !ok {
		panic(fmt.Sprintf("internal: range %q not found in scope (should have been caught by type solver)", ri.Name))
	}

	if val, ok := c.extractRangeValue(sym, ri.Name); ok {
		return val
	}
	panic(fmt.Sprintf("internal: range %q expected Range kind, got %s (should have been caught by type solver)", ri.Name, sym.Type.String()))
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
		r := c.rangeAggregateForRI(ranges[i])
		c.createLoop(r, func(iter llvm.Value) {
			PushScope(&c.Scopes, BlockScope)
			Put(c.Scopes, ranges[i].Name, &Symbol{Type: Int{Width: 64}, Val: iter})
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
			switch t := sym.Type.(type) {
			case Int:
				continue
			case Ptr:
				if t.Elem.Kind() == IntKind {
					continue
				}
			}
		}
		filtered = append(filtered, ri)
	}
	return filtered
}

func (c *Compiler) createLoop(r llvm.Value, bodyGen func(iter llvm.Value)) {
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
	cmpPos := c.builder.CreateICmp(llvm.IntSLT, iterPos, stop, "loop_cond_pos")
	c.builder.CreateCondBr(cmpPos, body, exit)

	// condNeg: iter > stop
	c.builder.SetInsertPointAtEnd(condNeg)
	iterNeg := c.builder.CreatePHI(c.Context.Int64Type(), "iter_neg")
	iterNeg.AddIncoming([]llvm.Value{start}, []llvm.BasicBlock{preheader})
	cmpNeg := c.builder.CreateICmp(llvm.IntSGT, iterNeg, stop, "loop_cond_neg")
	c.builder.CreateCondBr(cmpNeg, body, exit)

	// Body
	c.builder.SetInsertPointAtEnd(body)
	iter := c.builder.CreatePHI(c.Context.Int64Type(), "iter")
	iter.AddIncoming([]llvm.Value{iterPos}, []llvm.BasicBlock{condPos})
	iter.AddIncoming([]llvm.Value{iterNeg}, []llvm.BasicBlock{condNeg})

	bodyGen(iter)

	latch := c.builder.GetInsertBlock()
	iterNext := c.builder.CreateAdd(iter, step, "iter_next")
	c.builder.CreateCondBr(isNeg, condNeg, condPos)

	iterPos.AddIncoming([]llvm.Value{iterNext}, []llvm.BasicBlock{latch})
	iterNeg.AddIncoming([]llvm.Value{iterNext}, []llvm.BasicBlock{latch})

	c.builder.SetInsertPointAtEnd(exit)
}
