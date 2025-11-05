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

// Build the {start,stop,step} aggregate for either a literal or a named range.
func (c *Compiler) rangeAggregateForRI(ri *RangeInfo) llvm.Value {
	// Literal occurrence: synthesize the aggregate
	if ri.RangeLit != nil {
		return c.ToRange(ri.RangeLit, Range{Iter: Int{Width: 64}})
	}

	// Named occurrence: look it up in scope
	if sym, ok := Get(c.Scopes, ri.Name); ok {
		return sym.Val
	}
	if c.CodeCompiler != nil && c.CodeCompiler.Compiler != nil {
		if sym, ok := Get(c.CodeCompiler.Compiler.Scopes, ri.Name); ok {
			return sym.Val
		}
	}

	panic(fmt.Sprintf("range %q not found in scope", ri.Name))
}

// Build a nested loop over specs; at each level shadow specs[i].Name with the scalar iter; run body at innermost.
func (c *Compiler) withLoopNest(ranges []*RangeInfo, body func()) {
	ranges = c.pendingLoopRanges(ranges)
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
			PopScope(&c.Scopes)
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
		sym, ok := Get(c.Scopes, ri.Name)
		if ok && sym.Type.Kind() == IntKind {
			continue
		}
		filtered = append(filtered, ri)
	}
	return filtered
}

func (c *Compiler) createLoop(r llvm.Value, bodyGen func(iter llvm.Value)) {
	start, stop, step := c.rangeComponents(r)

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
