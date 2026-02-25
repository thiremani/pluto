package compiler

import (
	"math"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

type boundsGuardFrame struct {
	guard llvm.Value
	used  bool
}

type loopBoundsMode int

const (
	loopBoundsModeDefault loopBoundsMode = iota
	loopBoundsModeAffineFast
	loopBoundsModeChecked
)

type loopBoundsFrame struct {
	mode       loopBoundsMode
	fastAccess map[*ast.ArrayRangeExpression]struct{}
}

func (c *Compiler) stmtBoundsUsed() bool {
	ctx := c.currentStmtCtx()
	if ctx == nil || len(ctx.boundsStack) == 0 {
		return false
	}
	return ctx.boundsStack[len(ctx.boundsStack)-1].used
}

// pushBoundsGuard sets up a new bounds-check guard and returns its pointer.
// Callers must pop the guard with popBoundsGuard after the guarded region.
func (c *Compiler) pushBoundsGuard(name string) llvm.Value {
	ctx := c.currentStmtCtx()
	if ctx == nil {
		// Bounds checks can be compiled in expression-only paths with no
		// statement frame. In that case we skip statement-level guard tracking.
		return llvm.Value{}
	}

	guardPtr := c.createEntryBlockAlloca(c.Context.Int1Type(), name)
	c.createStore(llvm.ConstInt(c.Context.Int1Type(), 1, false), guardPtr, Int{Width: 1})
	ctx.boundsStack = append(ctx.boundsStack, boundsGuardFrame{guard: guardPtr, used: false})
	return guardPtr
}

func (c *Compiler) popBoundsGuard() {
	ctx := c.currentStmtCtx()
	if ctx == nil || len(ctx.boundsStack) == 0 {
		// No active statement/bounds frame: nothing to pop by design.
		return
	}
	ctx.boundsStack = ctx.boundsStack[:len(ctx.boundsStack)-1]
}

// recordStmtBoundsCheck ANDs one in-bounds predicate into the active assignment
// guard. A false guard means the current assignment should become a no-op.
func (c *Compiler) recordStmtBoundsCheck(inBounds llvm.Value) {
	ctx := c.currentStmtCtx()
	if ctx == nil || len(ctx.boundsStack) == 0 {
		// No active statement/bounds frame: skip statement-level guard updates.
		return
	}

	// Pointer into boundsStack is safe here because this function does not append
	// to boundsStack while frame is live.
	frame := &ctx.boundsStack[len(ctx.boundsStack)-1]
	if frame.guard.IsNil() {
		return
	}
	curr := c.createLoad(frame.guard, Int{Width: 1}, "stmt_bounds_curr")
	next := c.builder.CreateAnd(curr, inBounds, "stmt_bounds_and")
	c.createStore(next, frame.guard, Int{Width: 1})
	frame.used = true
}

// withGuardedBranch emits an if/else/cont branch structure and executes the
// provided callbacks in each branch.
func (c *Compiler) withGuardedBranch(
	guard llvm.Value,
	ifName, elseName, contName string,
	onIf func(),
	onElse func(),
) {
	ifBlock, elseBlock, contBlock := c.createIfElseCont(guard, ifName, elseName, contName)

	c.builder.SetInsertPointAtEnd(ifBlock)
	if onIf != nil {
		onIf()
	}
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(elseBlock)
	if onElse != nil {
		onElse()
	}
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
}

func (c *Compiler) withLoadedGuard(
	guardPtr llvm.Value,
	loadName, ifName, elseName, contName string,
	onIf func(),
	onElse func(),
) {
	guard := c.createLoad(guardPtr, Int{Width: 1}, loadName)
	c.withGuardedBranch(guard, ifName, elseName, contName, onIf, onElse)
}

func (c *Compiler) withStmtBoundsGuard(
	loadName, ifName, elseName, contName string,
	onIf func(),
	onElse func(),
) bool {
	if !c.stmtBoundsUsed() {
		return false
	}

	ctx := c.currentStmtCtx()
	frame := ctx.boundsStack[len(ctx.boundsStack)-1]
	c.withLoadedGuard(frame.guard, loadName, ifName, elseName, contName, onIf, onElse)
	return true
}

// arrayIndexInBounds checks idx against [0, len(arr)).
func (c *Compiler) arrayIndexInBounds(arr *Symbol, elem Type, idx llvm.Value) llvm.Value {
	length := c.ArrayLen(arr, elem)
	// Unsigned compare rejects negative indices as well (two's-complement wrap
	// makes them larger than any valid length).
	return c.builder.CreateICmp(llvm.IntULT, idx, length, "idx_in_bounds")
}

// checkedArrayGet loads arr[idx] when idx is in bounds; otherwise returns zero
// value for resultType. Assignment-level guards decide whether writes commit.
func (c *Compiler) checkedArrayGet(arr *Symbol, arrElem Type, resultType Type, idx llvm.Value, inBounds llvm.Value) llvm.Value {
	outPtr := c.createEntryBlockAlloca(c.mapToLLVMType(resultType), "arr_get_checked_mem")

	getBlock, missBlock, contBlock := c.createIfElseCont(inBounds, "arr_get_in_bounds", "arr_get_oob", "arr_get_cont")

	c.builder.SetInsertPointAtEnd(getBlock)
	value := c.ArrayGet(arr, arrElem, idx)
	c.createStore(value, outPtr, resultType)
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(missBlock)
	zero := c.makeZeroValue(resultType)
	c.createStore(zero.Val, outPtr, zero.Type)
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
	return c.createLoad(outPtr, resultType, "arr_get_checked")
}

type affineIndexForm struct {
	hasVar  bool
	varName string
	coeff   int64
	bias    int64
}

func addInt64(a, b int64) (int64, bool) {
	sum := a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, false
	}
	return sum, true
}

func mulInt64(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if (a == math.MinInt64 && b == -1) || (b == math.MinInt64 && a == -1) {
		return 0, false
	}
	prod := a * b
	if prod/b != a {
		return 0, false
	}
	return prod, true
}

func negateInt64(v int64) (int64, bool) {
	if v == math.MinInt64 {
		return 0, false
	}
	return -v, true
}

func scaleAffineForm(form affineIndexForm, factor int64) (affineIndexForm, bool) {
	if factor == 0 {
		return affineIndexForm{}, true
	}

	coeff := int64(0)
	var ok bool
	if form.hasVar {
		coeff, ok = mulInt64(form.coeff, factor)
		if !ok {
			return affineIndexForm{}, false
		}
	}
	bias, ok := mulInt64(form.bias, factor)
	if !ok {
		return affineIndexForm{}, false
	}

	if coeff == 0 {
		return affineIndexForm{bias: bias}, true
	}
	return affineIndexForm{
		hasVar:  true,
		varName: form.varName,
		coeff:   coeff,
		bias:    bias,
	}, true
}

func combineAffineForms(left, right affineIndexForm, rightSign int64) (affineIndexForm, bool) {
	if rightSign != 1 && rightSign != -1 {
		return affineIndexForm{}, false
	}

	rightCoeff := right.coeff
	rightBias := right.bias
	if rightSign == -1 {
		var ok bool
		rightCoeff, ok = negateInt64(rightCoeff)
		if !ok {
			return affineIndexForm{}, false
		}
		rightBias, ok = negateInt64(rightBias)
		if !ok {
			return affineIndexForm{}, false
		}
	}

	if left.hasVar && right.hasVar && left.varName != right.varName {
		return affineIndexForm{}, false
	}

	coeff, ok := addInt64(left.coeff, rightCoeff)
	if !ok {
		return affineIndexForm{}, false
	}
	bias, ok := addInt64(left.bias, rightBias)
	if !ok {
		return affineIndexForm{}, false
	}

	if coeff == 0 {
		return affineIndexForm{bias: bias}, true
	}

	name := left.varName
	if name == "" {
		name = right.varName
	}

	return affineIndexForm{
		hasVar:  true,
		varName: name,
		coeff:   coeff,
		bias:    bias,
	}, true
}

func affineIndexFromExpr(expr ast.Expression) (affineIndexForm, bool) {
	switch e := expr.(type) {
	case *ast.IntegerLiteral:
		return affineIndexForm{bias: e.Value}, true
	case *ast.Identifier:
		return affineIndexForm{
			hasVar:  true,
			varName: e.Value,
			coeff:   1,
		}, true
	case *ast.PrefixExpression:
		if e.Operator != token.SYM_SUB {
			return affineIndexForm{}, false
		}
		right, ok := affineIndexFromExpr(e.Right)
		if !ok {
			return affineIndexForm{}, false
		}
		return scaleAffineForm(right, -1)
	case *ast.InfixExpression:
		left, ok := affineIndexFromExpr(e.Left)
		if !ok {
			return affineIndexForm{}, false
		}
		right, ok := affineIndexFromExpr(e.Right)
		if !ok {
			return affineIndexForm{}, false
		}

		switch e.Operator {
		case token.SYM_ADD:
			return combineAffineForms(left, right, 1)
		case token.SYM_SUB:
			return combineAffineForms(left, right, -1)
		case token.SYM_MUL, token.SYM_IMPL_MUL:
			leftConst := !left.hasVar
			rightConst := !right.hasVar
			switch {
			case leftConst && rightConst:
				bias, ok := mulInt64(left.bias, right.bias)
				if !ok {
					return affineIndexForm{}, false
				}
				return affineIndexForm{bias: bias}, true
			case leftConst:
				return scaleAffineForm(right, left.bias)
			case rightConst:
				return scaleAffineForm(left, right.bias)
			default:
				return affineIndexForm{}, false
			}
		default:
			return affineIndexForm{}, false
		}
	default:
		return affineIndexForm{}, false
	}
}

func (c *Compiler) loopIterMinMax(start, stop, step llvm.Value) (minIter, maxIter, hasAny llvm.Value) {
	zero := c.ConstI64(0)
	one := c.ConstI64(1)

	isNeg := c.builder.CreateICmp(llvm.IntSLT, step, zero, "iter_step_is_neg")
	isPos := c.builder.CreateICmp(llvm.IntSGT, step, zero, "iter_step_is_pos")
	posHasAny := c.builder.CreateAnd(
		isPos,
		c.builder.CreateICmp(llvm.IntSLT, start, stop, "iter_pos_has"),
		"iter_pos_has_any",
	)
	negHasAny := c.builder.CreateAnd(
		isNeg,
		c.builder.CreateICmp(llvm.IntSGT, start, stop, "iter_neg_has"),
		"iter_neg_has_any",
	)
	hasAny = c.builder.CreateOr(posHasAny, negHasAny, "iter_has_any")

	stopMinusOne := c.builder.CreateSub(stop, one, "iter_stop_minus_one")
	stopPlusOne := c.builder.CreateAdd(stop, one, "iter_stop_plus_one")
	minIter = c.builder.CreateSelect(isNeg, stopPlusOne, start, "iter_min")
	maxIter = c.builder.CreateSelect(isNeg, start, stopMinusOne, "iter_max")
	return
}

func (c *Compiler) affineIndexAt(coeff, bias int64, x llvm.Value, name string) llvm.Value {
	base := x
	switch coeff {
	case 0:
		base = c.ConstI64(0)
	case 1:
		// identity
	default:
		base = c.builder.CreateMul(x, c.ConstI64(uint64(coeff)), name+"_mul")
	}

	if bias == 0 {
		return base
	}
	return c.builder.CreateAdd(base, c.ConstI64(uint64(bias)), name+"_bias")
}

func (c *Compiler) affineLoopBoundsForComponents(
	form affineIndexForm,
	arr *Symbol,
	arrElem Type,
	start llvm.Value,
	stop llvm.Value,
	step llvm.Value,
) llvm.Value {
	minIter, maxIter, hasAny := c.loopIterMinMax(start, stop, step)
	minSrc := minIter
	maxSrc := maxIter
	if form.coeff < 0 {
		minSrc, maxSrc = maxSrc, minSrc
	}

	minIdx := c.affineIndexAt(form.coeff, form.bias, minSrc, "idx_affine_min")
	maxIdx := c.affineIndexAt(form.coeff, form.bias, maxSrc, "idx_affine_max")

	length := c.ArrayLen(arr, arrElem)
	zero := c.ConstI64(0)
	lowOK := c.builder.CreateICmp(llvm.IntSGE, minIdx, zero, "idx_affine_low_ok")
	highOK := c.builder.CreateICmp(llvm.IntSLT, maxIdx, length, "idx_affine_high_ok")
	nonEmptySafe := c.builder.CreateAnd(lowOK, highOK, "idx_affine_safe_nonempty")
	return c.builder.CreateOr(
		c.builder.CreateNot(hasAny, "idx_affine_empty"),
		nonEmptySafe,
		"idx_affine_all_safe",
	)
}

type loopRangeBounds struct {
	start llvm.Value
	stop  llvm.Value
	step  llvm.Value
}

func collectArrayIndexAccesses(expr ast.Expression, out *[]*ast.ArrayRangeExpression) {
	if expr == nil {
		return
	}
	if access, ok := expr.(*ast.ArrayRangeExpression); ok {
		*out = append(*out, access)
	}
	for _, child := range ast.ExprChildren(expr) {
		collectArrayIndexAccesses(child, out)
	}
}

func (c *Compiler) loopRangeBoundsByName(ranges []*RangeInfo) map[string]loopRangeBounds {
	bounds := make(map[string]loopRangeBounds, len(ranges))
	for _, ri := range ranges {
		rangeVal := c.rangeAggregateForRI(ri)
		start, stop, step := c.rangeComponents(rangeVal)
		if start.Type() != c.Context.Int64Type() || stop.Type() != c.Context.Int64Type() || step.Type() != c.Context.Int64Type() {
			continue
		}
		bounds[ri.Name] = loopRangeBounds{
			start: start,
			stop:  stop,
			step:  step,
		}
	}
	return bounds
}

func (c *Compiler) arraySymbolForAffineGuard(arrayExpr ast.Expression) (*Symbol, Type, bool) {
	ident, ok := arrayExpr.(*ast.Identifier)
	if !ok {
		return nil, nil, false
	}
	raw, ok := c.getRawSymbol(ident.Value)
	if !ok {
		return nil, nil, false
	}
	arraySym := c.derefIfPointer(raw, ident.Value+"_affine_arr")
	arrType, ok := arraySym.Type.(Array)
	if !ok || len(arrType.ColTypes) == 0 {
		return nil, nil, false
	}
	return arraySym, arrType.ColTypes[0], true
}

func (c *Compiler) affineVersioningGuard(expr ast.Expression, ranges []*RangeInfo) (llvm.Value, map[*ast.ArrayRangeExpression]struct{}, bool) {
	if expr == nil || len(ranges) == 0 {
		return llvm.Value{}, nil, false
	}

	var accesses []*ast.ArrayRangeExpression
	collectArrayIndexAccesses(expr, &accesses)
	if len(accesses) == 0 {
		return llvm.Value{}, nil, false
	}

	boundsByName := c.loopRangeBoundsByName(ranges)
	fastAccess := make(map[*ast.ArrayRangeExpression]struct{})
	guard := llvm.Value{}
	hasGuard := false

	for _, access := range accesses {
		form, ok := affineIndexFromExpr(access.Range)
		if !ok || !form.hasVar {
			continue
		}

		bounds, ok := boundsByName[form.varName]
		if !ok {
			continue
		}

		arraySym, arrElem, ok := c.arraySymbolForAffineGuard(access.Array)
		if !ok {
			continue
		}

		safe := c.affineLoopBoundsForComponents(form, arraySym, arrElem, bounds.start, bounds.stop, bounds.step)
		if !hasGuard {
			guard = safe
			hasGuard = true
		} else {
			guard = c.builder.CreateAnd(guard, safe, "loop_affine_guard")
		}
		fastAccess[access] = struct{}{}
	}

	if !hasGuard {
		return llvm.Value{}, nil, false
	}
	return guard, fastAccess, true
}
