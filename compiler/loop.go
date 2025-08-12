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

// One loop dimension: either a named range (Name, LitNode=nil) or a literal occurrence (Name=temp, LitNode=orig node).
type loopSpec struct {
	Name string
	Node *ast.RangeLiteral
}

func (c *Compiler) freshIterName() string {
	n := c.tmpCounter
	c.tmpCounter++
	return fmt.Sprintf("tmpIter$%d", n)
}

func (c *Compiler) rewriteLitsUnder(e ast.Expression, out *[]loopSpec) ast.Expression {
	switch t := e.(type) {
	case *ast.RangeLiteral:
		nm := c.freshIterName()
		*out = append(*out, loopSpec{Name: nm, Node: t})
		return &ast.Identifier{Value: nm, Token: t.Tok()}
	case *ast.InfixExpression:
		l := c.rewriteLitsUnder(t.Left, out)
		r := c.rewriteLitsUnder(t.Right, out)
		if l == t.Left && r == t.Right {
			return e
		}
		cp := *t
		cp.Left, cp.Right = l, r
		return &cp
	case *ast.PrefixExpression:
		r := c.rewriteLitsUnder(t.Right, out)
		if r == t.Right {
			return e
		}
		cp := *t
		cp.Right = r
		return &cp
	case *ast.CallExpression:
		changed := false
		args := make([]ast.Expression, len(t.Arguments))
		for i, a := range t.Arguments {
			a2 := c.rewriteLitsUnder(a, out)
			args[i] = a2
			changed = changed || (a2 != a)
		}
		if !changed {
			return e
		}
		cp := *t
		cp.Arguments = args
		return &cp
	case *ast.Identifier:
		return t
	default:
		return e
	}
}

// Turn a loopSpec into its {start,stop,step} aggregate.
func (c *Compiler) rangeAggregateFor(sp loopSpec) llvm.Value {
	if sp.Node != nil {
		return c.toRange(sp.Node, Range{Iter: Int{Width: 64}})
	}
	if sym, ok := Get(c.Scopes, sp.Name); ok {
		return sym.Val
	}
	if c.CodeCompiler != nil && c.CodeCompiler.Compiler != nil {
		if sym, ok := Get(c.CodeCompiler.Compiler.Scopes, sp.Name); ok {
			return sym.Val
		}
	}
	panic(fmt.Sprintf("range '%s' not found in scope", sp.Name))
}

// Build a nested loop over specs; at each level shadow specs[i].Name with the scalar iter; run body at innermost.
func (c *Compiler) withLoopNest(specs []loopSpec, body func()) {
	var rec func(i int)
	rec = func(i int) {
		if i == len(specs) {
			body()
			return
		}
		sp := specs[i]
		r := c.rangeAggregateFor(sp)
		c.createLoop(r, func(iter llvm.Value) {
			PushScope(&c.Scopes, BlockScope)
			Put(c.Scopes, sp.Name, &Symbol{Type: Int{Width: 64}, Val: iter})
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
	c.builder.CreateStore(llvm.ConstInt(c.Context.Int1Type(), 0, false), f.setPtr)
	return f
}

func (c *Compiler) foldAccAdd(acc *foldAcc, op string, term *Symbol) {
	set := c.builder.CreateLoad(c.Context.Int1Type(), acc.setPtr, "acc_set_ld")

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
	c.builder.CreateStore(term.Val, acc.valPtr)
	c.builder.CreateStore(llvm.ConstInt(c.Context.Int1Type(), 1, false), acc.setPtr)
	c.builder.CreateBr(contB)

	// fold: acc = acc ⊗ term
	c.builder.SetInsertPointAtEnd(foldB)
	cur := c.builder.CreateLoad(acc.llvmTy, acc.valPtr, "acc_ld")
	left := &Symbol{Type: acc.outType, Val: cur}
	combKey := opKey{Operator: op, LeftType: left.Type.String(), RightType: term.Type.String()}
	comb := defaultOps[combKey](c, left, term, true)
	c.builder.CreateStore(comb.Val, acc.valPtr)
	c.builder.CreateBr(contB)

	c.builder.SetInsertPointAtEnd(contB)
}

func (c *Compiler) foldAccFinal(acc *foldAcc) *Symbol {
	if acc.valPtr.IsNil() {
		panic("empty range fold; provide an explicit seed")
	}
	v := c.builder.CreateLoad(acc.llvmTy, acc.valPtr, "acc_final")
	return &Symbol{Type: acc.outType, Val: v}
}

func (c *Compiler) makeLoopSpecs(names []string, lits []loopSpec) []loopSpec {
	specs := []loopSpec{}
	for _, n := range names {
		specs = append(specs, loopSpec{Name: n})
	}
	for _, lb := range lits {
		specs = append(specs, loopSpec{Name: lb.Name, Node: lb.Node})
	}
	return specs
}

func (c *Compiler) createLoop(r llvm.Value, bodyGen func(iter llvm.Value)) {
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
