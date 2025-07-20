package compiler

import (
	"tinygo.org/x/go-llvm"
)

type Loop struct {
	Iter llvm.Value
	Body *llvm.BasicBlock
	Exit *llvm.BasicBlock
}

func (c *Compiler) createLoop(r llvm.Value, bodyGen func(indVar llvm.Value)) {
	start, stop, step := c.rangeComponents(r)

	curr := c.builder.GetInsertBlock()
	fn := curr.Parent()

	cond := c.Context.AddBasicBlock(fn, "loop_cond")
	body := c.Context.AddBasicBlock(fn, "loop_body")
	exit := c.Context.AddBasicBlock(fn, "loop_exit")

	c.builder.CreateBr(cond)
	c.builder.SetInsertPointAtEnd(cond)

	iter := c.builder.CreatePHI(c.Context.Int64Type(), "iter")
	iter.AddIncoming([]llvm.Value{start}, []llvm.BasicBlock{curr})

	loopCond := c.builder.CreateICmp(llvm.IntSLT, iter, stop, "loop_cond")
	c.builder.CreateCondBr(loopCond, body, exit)

	// Call the provided function to generate the main body of the loop.
	// It uses the current value of the induction variable.
	c.builder.SetInsertPointAtEnd(body)
	bodyGen(iter)

	// Now, at the end of the body, perform the increment.
	iterNext := c.builder.CreateAdd(iter, step, "iter_next")
	c.builder.CreateBr(cond)

	// Finally, add the incoming value from the body to the PHI node.
	iter.AddIncoming([]llvm.Value{iterNext}, []llvm.BasicBlock{body})

	c.builder.SetInsertPointAtEnd(exit)
}
