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
	if ri.Lit != nil {
		return c.toRange(ri.Lit, Range{Iter: Int{Width: 64}})
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

// Left-fold accumulator with "first term as seed".
type foldAcc struct {
	setPtr  llvm.Value // i1 flag: 0 unset, 1 set
	valPtr  llvm.Value // alloca for accumulator
	llvmTy  llvm.Type
	outType Type
}

func (c *Compiler) newFoldAcc() *foldAcc {
	f := &foldAcc{}
	f.setPtr = c.createEntryBlockAlloca(c.Context.Int1Type(), "acc_set")
	c.createStore(llvm.ConstInt(c.Context.Int1Type(), 0, false), f.setPtr, Int{Width: 1})
	return f
}

// init acc with an explicit seed
func (c *Compiler) foldAccInitWithSeed(acc *foldAcc, seed *Symbol) {
	if acc.valPtr.IsNil() {
		acc.llvmTy = c.mapToLLVMType(seed.Type)
		acc.valPtr = c.createEntryBlockAlloca(acc.llvmTy, "acc")
		acc.outType = seed.Type
	}
	c.createStore(seed.Val, acc.valPtr, seed.Type)
	c.createStore(llvm.ConstInt(c.Context.Int1Type(), 1, false), acc.setPtr, Int{Width: 1})
}

// acc = acc ⊗ term   (no control flow; used after seeded init)
func (c *Compiler) foldAccCombine(acc *foldAcc, op string, term *Symbol) {
	cur := c.createLoad(acc.valPtr, acc.outType, "acc_ld")
	left := &Symbol{Type: acc.outType, Val: cur}
	key := opKey{Operator: op, LeftType: left.Type.String(), RightType: term.Type.String()}
	comb := defaultOps[key](c, left, term, true)
	c.createStore(comb.Val, acc.valPtr, acc.outType)
}

func (c *Compiler) foldAccAdd(acc *foldAcc, op string, term *Symbol) {
	set := c.createLoad(acc.setPtr, Int{Width: 1}, "acc_set_ld")

	curr := c.builder.GetInsertBlock()
	fn := curr.Parent()
	initB := c.Context.AddBasicBlock(fn, "acc_init")
	foldB := c.Context.AddBasicBlock(fn, "acc_fold")
	contB := c.Context.AddBasicBlock(fn, "acc_cont")

	isUnset := c.builder.CreateICmp(llvm.IntEQ, set, llvm.ConstInt(c.Context.Int1Type(), 0, false), "is_unset")
	c.builder.CreateCondBr(isUnset, initB, foldB)

	// init with first term
	c.builder.SetInsertPointAtEnd(initB)
	if acc.valPtr.IsNil() {
		acc.llvmTy = c.mapToLLVMType(term.Type)
		acc.valPtr = c.createEntryBlockAlloca(acc.llvmTy, "acc")
		acc.outType = term.Type
	}
	c.createStore(term.Val, acc.valPtr, term.Type)
	c.createStore(llvm.ConstInt(c.Context.Int1Type(), 1, false), acc.setPtr, Int{Width: 1})
	c.builder.CreateBr(contB)

	// fold: acc = acc ⊗ term
	c.builder.SetInsertPointAtEnd(foldB)
	cur := c.createLoad(acc.valPtr, acc.outType, "acc_ld")
	left := &Symbol{Type: acc.outType, Val: cur}
	combKey := opKey{Operator: op, LeftType: left.Type.String(), RightType: term.Type.String()}
	comb := defaultOps[combKey](c, left, term, true)
	c.createStore(comb.Val, acc.valPtr, acc.outType)
	c.builder.CreateBr(contB)

	c.builder.SetInsertPointAtEnd(contB)
}

func (c *Compiler) foldAccFinal(acc *foldAcc) *Symbol {
	if acc.valPtr.IsNil() {
		panic("empty range fold; provide an explicit seed")
	}
	v := c.createLoad(acc.valPtr, acc.outType, "acc_final")
	return &Symbol{Type: acc.outType, Val: v}
}

func (c *Compiler) createLoop(r llvm.Value, bodyGen func(iter llvm.Value)) {
	start, stop, step := c.rangeComponents(r)

	preheader := c.builder.GetInsertBlock()
	fn := preheader.Parent()

	cond := c.Context.AddBasicBlock(fn, "loop_cond")
	body := c.Context.AddBasicBlock(fn, "loop_body")
	exit := c.Context.AddBasicBlock(fn, "loop_exit")

	// preheader -> cond
	c.builder.CreateBr(cond)
	c.builder.SetInsertPointAtEnd(cond)

	iter := c.builder.CreatePHI(c.Context.Int64Type(), "iter")
	iter.AddIncoming([]llvm.Value{start}, []llvm.BasicBlock{preheader})

	loopCond := c.builder.CreateICmp(llvm.IntSLT, iter, stop, "loop_cond")
	c.builder.CreateCondBr(loopCond, body, exit)

	// Call the provided function to generate the main body of the loop.
	// It uses the current value of the induction variable.
	c.builder.SetInsertPointAtEnd(body)
	bodyGen(iter)

	// IMPORTANT: use the block where bodyGen left us as the latch
	latch := c.builder.GetInsertBlock()

	// Now, at the end of the body, perform the increment.
	iterNext := c.builder.CreateAdd(iter, step, "iter_next")
	c.builder.CreateBr(cond)

	// Finally, add the incoming value from the body to the PHI node.
	// backedge is from latch, not necessarily 'body'
	iter.AddIncoming([]llvm.Value{iterNext}, []llvm.BasicBlock{latch})

	c.builder.SetInsertPointAtEnd(exit)
}
