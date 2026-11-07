package compiler

import (
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

type stackedArrayAccumulator struct {
	array          *ArrayAccumulator
	arrayType      Array
	outerLenSlot   llvm.Value
	childDimSlots  []llvm.Value
	failedRowsSlot llvm.Value
}

// arrayValuedCell is one resolved array-valued cell. A failed cell whose operands
// show its shape yields zeros in that shape. One whose shape nothing shows didn't
// yield: its value is empty, and its row takes zeros in the shape of the rows
// that did.
type arrayValuedCell struct {
	value   *Symbol
	yielded llvm.Value
}

func (c *Compiler) compileFixedStackedArray(lit *ast.ArrayLiteral, arrayType Array) *Symbol {
	children := make([]ast.Expression, 0)
	for _, row := range lit.Rows {
		children = append(children, row...)
	}

	cells := make([]arrayValuedCell, 0, len(children))
	for _, child := range children {
		cells = append(cells, c.compileArrayValuedCell(child))
	}

	childDims := c.fixedRowShape(cells)

	dimensions := make([]llvm.Value, 0, arrayType.Rank)
	for _, dimension := range arrayLiteralLayoutShape(lit) {
		dimensions = append(dimensions, c.ConstI64(dimension))
	}
	dimensions = append(dimensions, childDims...)

	childLen := c.scaleByDimensions(c.ConstI64(1), childDims)
	totalLen := c.builder.CreateMul(c.ConstI64(uint64(len(cells))), childLen, "stacked_array_len")
	data := c.CreateArrayForType(arrayType.ElemType, totalLen)
	for i, cell := range cells {
		childElemType := cell.value.Type.(Array).ElemType
		offset := llvm.Value{}
		applyOffset := i > 0
		if applyOffset {
			offset = c.builder.CreateMul(c.ConstI64(uint64(i)), childLen, "stacked_array_offset")
		}
		c.CopyArrayInto(data, cell.value, childElemType, arrayType.ElemType, offset, applyOffset)
		c.freeSymbolValue(cell.value, "")
	}

	return &Symbol{Type: arrayType, Val: c.createArrayValue(data, dimensions, arrayType)}
}

// fixedRowShape returns the shape of the first cell that yielded, which every
// other yielded cell must match. A failed cell is empty, so it copies nothing and
// its row keeps the new array's zeros.
func (c *Compiler) fixedRowShape(cells []arrayValuedCell) []llvm.Value {
	childDims := c.arrayDimensions(cells[0].value)
	shaped := cells[0].yielded
	for _, cell := range cells[1:] {
		dims := c.arrayDimensions(cell.value)
		bothYielded := c.builder.CreateAnd(shaped, cell.yielded, "stacked_array_both_yielded")
		c.requireSameArrayShapeWhen(bothYielded, childDims, dims, "stacked_array_shape")
		for i := range childDims {
			childDims[i] = c.builder.CreateSelect(shaped, childDims[i], dims[i], "stacked_array_dimension")
		}
		shaped = c.builder.CreateOr(shaped, cell.yielded, "stacked_array_shaped")
	}
	return childDims
}

func (c *Compiler) scaleByDimensions(count llvm.Value, dimensions []llvm.Value) llvm.Value {
	for _, dimension := range dimensions {
		count = c.builder.CreateMul(count, dimension, "stacked_array_scaled")
	}
	return count
}

// compileArrayValuedCell resolves an array-valued cell through a seeded slot,
// as compileArrayLiteralCell does a scalar cell: the cell's conditions and
// bounds are its own, and the caller owns and frees the slot's value.
func (c *Compiler) compileArrayValuedCell(child ast.Expression) arrayValuedCell {
	info := c.ExprCache[key(c.FuncNameMangled, child)]
	cellSlot := c.newArrayCellSlot(info.OutTypes[0])
	yieldedSlot := c.createEntryBlockAlloca(c.Context.Int1Type(), "array_cell_yielded")
	c.setArrayCellYielded(yieldedSlot, 0)
	outerPassed, _ := c.activeBoundsGuardValue("array_cell_outer_guard")

	c.pushBoundsGuard("array_cell_bounds_guard")
	defer c.popBoundsGuard()

	c.withArrayLiteralCellMode(func() {
		c.compileCondExprValue(child, func() {
			c.storeArrayCellSlotWhenInBounds(cellSlot, c.compileExpression(child, nil), child, yieldedSlot)
		})
	})
	c.zeroFillFailedCell(child, cellSlot, yieldedSlot, outerPassed)

	return arrayValuedCell{
		value:   c.derefIfPointer(cellSlot, "stacked_array_child"),
		yielded: c.createLoad(yieldedSlot, Int{Width: 1}, "array_cell_yielded_value"),
	}
}

func (c *Compiler) setArrayCellYielded(yieldedSlot llvm.Value, bit uint64) {
	c.createStore(llvm.ConstInt(c.Context.Int1Type(), bit, false), yieldedSlot, Int{Width: 1})
}

// zeroFillFailedCell gives a failed cell zeros in the shape its operands show. A
// cell whose shape only running it would show stays failed, and so does one under
// a failed outer guard, since nothing will use its value.
func (c *Compiler) zeroFillFailedCell(cell ast.Expression, cellSlot *Symbol, yieldedSlot llvm.Value, outerPassed llvm.Value) {
	yielded := c.createLoad(yieldedSlot, Int{Width: 1}, "array_cell_yielded_value")
	failed := c.builder.CreateNot(yielded, "array_cell_failed")
	c.withCondBranch(c.andConds(failed, outerPassed, "array_cell_own_failure"), "array_cell_zero", func() {
		dimensions := c.dimensionsWithoutRunning(cell)
		if dimensions == nil {
			return
		}

		arrayType := cellSlot.Type.(Ptr).Elem.(Array)
		data := c.CreateArrayForType(arrayType.ElemType, c.scaleByDimensions(c.ConstI64(1), dimensions))
		c.storeSymbolToSlot(cellSlot, c.arrayValueSymbol(data, arrayType, dimensions), arrayType, "array_cell_zero_store")
		c.setArrayCellYielded(yieldedSlot, 1)
	}, nil)
}

// dimensionsWithoutRunning returns the dimensions expr's array would have, or nil
// when only running expr would show them, as with a call's result.
func (c *Compiler) dimensionsWithoutRunning(expr ast.Expression) []llvm.Value {
	switch e := expr.(type) {
	case *ast.Identifier:
		return c.arrayDimensions(c.compileIdentifier(e))
	case *ast.PrefixExpression:
		return c.dimensionsWithoutRunning(e.Right)
	case *ast.InfixExpression:
		return c.infixDimensionsWithoutRunning(e)
	case *ast.ArrayRangeExpression:
		dimensions := c.dimensionsWithoutRunning(e.Array)
		if dimensions == nil {
			return nil
		}
		return dimensions[1:]
	case *ast.ArrayLiteral:
		return c.literalDimensionsWithoutRunning(e)
	default:
		return nil
	}
}

func (c *Compiler) infixDimensionsWithoutRunning(e *ast.InfixExpression) []llvm.Value {
	switch {
	case e.IsLogicalAnd() || !c.isArrayExpression(e.Left):
		return c.dimensionsWithoutRunning(e.Right)
	case e.IsLogicalOr() || !c.isArrayExpression(e.Right):
		return c.dimensionsWithoutRunning(e.Left)
	case e.Operator == token.SYM_CONCAT:
		return nil
	}

	left := c.dimensionsWithoutRunning(e.Left)
	right := c.dimensionsWithoutRunning(e.Right)
	if left == nil || right == nil {
		return nil
	}
	return c.zipDimensions(left, right)
}

func (c *Compiler) literalDimensionsWithoutRunning(e *ast.ArrayLiteral) []llvm.Value {
	lit, info := c.resolveArrayLiteralRewrite(e)
	if len(info.CollectRanges) > 0 {
		return nil
	}

	dimensions := make([]llvm.Value, 0, info.OutTypes[0].(Array).Rank)
	for _, dimension := range arrayLiteralLayoutShape(lit) {
		dimensions = append(dimensions, c.ConstI64(dimension))
	}
	if !arrayLiteralHasArrayCells(lit, info.OutTypes[0].(Array)) {
		return dimensions
	}

	child := c.dimensionsWithoutRunning(ast.ExprChildren(lit)[0])
	if child == nil {
		return nil
	}
	return append(dimensions, child...)
}

func (c *Compiler) isArrayExpression(expr ast.Expression) bool {
	return c.ExprCache[key(c.FuncNameMangled, expr)].OutTypes[0].Kind() == ArrayKind
}

func (c *Compiler) compileStackedArray(
	lit *ast.ArrayLiteral,
	arrayType Array,
	ranges []*RangeInfo,
	condExprs []ast.Expression,
) *Symbol {
	if len(ranges) == 0 && len(condExprs) == 0 {
		return c.compileFixedStackedArray(lit, arrayType)
	}

	acc := c.newStackedArrayAccumulator(arrayType)
	c.withCollectorDomain(ranges, lit, condExprs, func() {
		for _, child := range ast.ExprChildren(lit) {
			c.appendStackedArrayChild(acc, child)
		}
	})
	return c.stackedArrayAccumulatorResult(acc)
}

func (c *Compiler) newStackedArrayAccumulator(arrayType Array) *stackedArrayAccumulator {
	// The runtime accumulator owns the flat leaf buffer. Shape is tracked
	// separately while lower-rank child arrays are appended.
	outerLenSlot := c.createEntryBlockAlloca(c.mapToLLVMType(I64), "stacked_array_outer_len_mem")
	c.createStore(c.ConstI64(0), outerLenSlot, I64)

	childDimSlots := make([]llvm.Value, arrayType.Rank-1)
	for i := range childDimSlots {
		childDimSlots[i] = c.createEntryBlockAlloca(c.mapToLLVMType(I64), "stacked_array_dimension_mem")
		c.createStore(c.ConstI64(0), childDimSlots[i], I64)
	}

	failedRowsSlot := c.createEntryBlockAlloca(c.mapToLLVMType(I64), "stacked_array_failed_rows_mem")
	c.createStore(c.ConstI64(0), failedRowsSlot, I64)

	return &stackedArrayAccumulator{
		array:          c.NewArrayAccumulator(Array{ElemType: arrayType.ElemType, Rank: 1}),
		arrayType:      arrayType,
		outerLenSlot:   outerLenSlot,
		childDimSlots:  childDimSlots,
		failedRowsSlot: failedRowsSlot,
	}
}

func (c *Compiler) appendStackedArrayCollector(acc *stackedArrayAccumulator, lit *ast.ArrayLiteral) {
	c.withPendingLiteralRanges(lit, func(resolved *ast.ArrayLiteral) {
		for _, child := range ast.ExprChildren(resolved) {
			c.appendStackedArrayChild(acc, child)
		}
	})
}

func (c *Compiler) stackedArrayAccumulatorResult(acc *stackedArrayAccumulator) *Symbol {
	c.flushFailedRows(acc)

	dimensions := make([]llvm.Value, 0, acc.arrayType.Rank)
	dimensions = append(dimensions, c.createLoad(acc.outerLenSlot, I64, "stacked_array_outer_len"))
	for _, slot := range acc.childDimSlots {
		dimensions = append(dimensions, c.createLoad(slot, I64, "stacked_array_dimension"))
	}

	return &Symbol{Type: acc.arrayType, Val: c.createArrayValue(acc.array.Vec, dimensions, acc.arrayType)}
}

func (c *Compiler) appendStackedArrayChild(
	acc *stackedArrayAccumulator,
	child ast.Expression,
) {
	cell := c.compileArrayValuedCell(child)
	c.withCondBranch(cell.yielded, "stacked_array_row", func() {
		c.appendStackedRow(acc, cell.value)
	}, func() {
		failed := c.createLoad(acc.failedRowsSlot, I64, "stacked_array_failed_rows")
		c.createStore(c.builder.CreateAdd(failed, c.ConstI64(1), "stacked_array_failed_rows_next"), acc.failedRowsSlot, I64)
	})
	c.freeSymbolValue(cell.value, "")
}

func (c *Compiler) appendStackedRow(acc *stackedArrayAccumulator, row *Symbol) {
	rowType := row.Type.(Array)
	rowDimensions := c.arrayDimensions(row)
	outerLen := c.createLoad(acc.outerLenSlot, I64, "stacked_array_outer_len")
	firstRow := c.builder.CreateICmp(llvm.IntEQ, outerLen, c.ConstI64(0), "stacked_array_first_child")

	for i, dimension := range rowDimensions {
		stored := c.createLoad(acc.childDimSlots[i], I64, "stacked_array_dimension")
		equal := c.builder.CreateICmp(llvm.IntEQ, stored, dimension, "stacked_array_shape")
		c.failShapeOnFalse(c.builder.CreateOr(firstRow, equal, "stacked_array_shape_compatible"), stored, dimension, "stacked_array_shape")
		c.createStore(c.builder.CreateSelect(firstRow, dimension, stored, "stacked_array_dimension_value"), acc.childDimSlots[i], I64)
	}
	c.flushFailedRows(acc)

	length := c.ArrayLen(row, rowType.ElemType)
	c.createLoop(c.rangeZeroToN(length), func(iter llvm.Value) {
		value := c.ArrayGetBorrowed(row, rowType.ElemType, iter)
		c.pushAccumCellValue(acc.array, &Symbol{Val: value, Type: rowType.ElemType}, false, acc.array.ElemType)
	})

	outerLen = c.createLoad(acc.outerLenSlot, I64, "stacked_array_outer_len")
	c.createStore(c.builder.CreateAdd(outerLen, c.ConstI64(1), "stacked_array_outer_len_next"), acc.outerLenSlot, I64)
}

// flushFailedRows appends the rows that failed since the last row to yield, as
// zeros in the shape set so far. The outer length stays 0 until a row yields, so
// the first one to yield still sets the shape; if none does, every dimension is
// 0 and the failed rows stay empty.
func (c *Compiler) flushFailedRows(acc *stackedArrayAccumulator) {
	failed := c.createLoad(acc.failedRowsSlot, I64, "stacked_array_failed_rows")
	dimensions := make([]llvm.Value, len(acc.childDimSlots))
	for i, slot := range acc.childDimSlots {
		dimensions[i] = c.createLoad(slot, I64, "stacked_array_dimension")
	}
	c.createLoop(c.rangeZeroToN(c.scaleByDimensions(failed, dimensions)), func(llvm.Value) {
		c.pushAccumCellValue(acc.array, c.makeZeroValue(acc.array.ElemType), true, acc.array.ElemType)
	})

	outerLen := c.createLoad(acc.outerLenSlot, I64, "stacked_array_outer_len")
	c.createStore(c.builder.CreateAdd(outerLen, failed, "stacked_array_outer_len_flushed"), acc.outerLenSlot, I64)
	c.createStore(c.ConstI64(0), acc.failedRowsSlot, I64)
}

func (c *Compiler) createArrayValue(data llvm.Value, dimensions []llvm.Value, arrayType Array) llvm.Value {
	i8p := llvm.PointerType(c.Context.Int8Type(), 0)
	if data.IsNil() {
		data = llvm.ConstPointerNull(i8p)
	} else {
		data = c.builder.CreateBitCast(data, i8p, "array_data")
	}
	if arrayType.Rank == 1 {
		return data
	}

	value := llvm.Undef(c.mapToLLVMType(arrayType))
	value = c.builder.CreateInsertValue(value, data, 0, "array_data_value")
	for i, dimension := range dimensions {
		value = c.builder.CreateInsertValue(value, dimension, i+1, "array_dimension")
	}
	return value
}

func (c *Compiler) arrayDataValue(value llvm.Value, arrayType Array) llvm.Value {
	if arrayType.Rank == 1 {
		return value
	}
	return c.builder.CreateExtractValue(value, 0, "array_data")
}

func (c *Compiler) arrayDimensions(symbol *Symbol) []llvm.Value {
	arrayType := symbol.Type.(Array)
	if arrayType.Rank == 1 {
		return []llvm.Value{c.ArrayLen(symbol, arrayType.ElemType)}
	}

	dimensions := make([]llvm.Value, arrayType.Rank)
	for i := range dimensions {
		dimensions[i] = c.builder.CreateExtractValue(symbol.Val, i+1, "array_dimension")
	}
	return dimensions
}

// arrayValueDimensions returns dimensions carried directly by an array value.
// Rank-1 length lives in the runtime vector rather than the LLVM array value.
func (c *Compiler) arrayValueDimensions(symbol *Symbol) []llvm.Value {
	if symbol.Type.(Array).Rank == 1 {
		return nil
	}
	return c.arrayDimensions(symbol)
}

func (c *Compiler) requireSameArrayShape(left, right []llvm.Value, name string) {
	for i := range left {
		equal := c.builder.CreateICmp(llvm.IntEQ, left[i], right[i], name)
		c.failShapeOnFalse(equal, left[i], right[i], name)
	}
}

func (c *Compiler) requireSameArrayShapeWhen(condition llvm.Value, left, right []llvm.Value, name string) {
	for i := range left {
		equal := c.builder.CreateICmp(llvm.IntEQ, left[i], right[i], name)
		compatible := c.builder.CreateOr(c.builder.CreateNot(condition, name+"_skip"), equal, name+"_compatible")
		c.failShapeOnFalse(compatible, left[i], right[i], name)
	}
}

// failShapeOnFalse aborts with a shape-mismatch diagnostic when ok is false.
// The fail path only executes on mismatch, so expected/got are always the
// offending dimension pair.
func (c *Compiler) failShapeOnFalse(ok llvm.Value, expected, got llvm.Value, name string) {
	c.abortOnFalse(ok, ARRAY_SHAPE_FAIL, expected, got, name)
}

// abortOnFalse calls the noreturn runtime function failName with the two
// dimensions when ok is false.
func (c *Compiler) abortOnFalse(ok llvm.Value, failName string, first, second llvm.Value, name string) {
	fn := c.builder.GetInsertBlock().Parent()
	failBlock := c.Context.AddBasicBlock(fn, name+"_fail")
	contBlock := c.Context.AddBasicBlock(fn, name+"_cont")
	c.builder.CreateCondBr(ok, contBlock, failBlock)

	c.builder.SetInsertPointAtEnd(failBlock)
	failTy, failFn := c.GetCFunc(failName)
	c.builder.CreateCall(failTy, failFn, []llvm.Value{first, second}, "")
	c.builder.CreateUnreachable()

	c.builder.SetInsertPointAtEnd(contBlock)
}

func (c *Compiler) copyArrayValue(value llvm.Value, arrayType Array) llvm.Value {
	data := c.copyArray(c.arrayDataValue(value, arrayType), arrayType.ElemType)
	if arrayType.Rank == 1 {
		return data
	}

	dimensions := make([]llvm.Value, arrayType.Rank)
	for i := range dimensions {
		dimensions[i] = c.builder.CreateExtractValue(value, i+1, "array_dimension")
	}
	return c.createArrayValue(data, dimensions, arrayType)
}

func (c *Compiler) arrayNDStrArg(s *Symbol) llvm.Value {
	arrayType := s.Type.(Array)
	dimensions := c.arrayDimensions(s)
	i64 := c.Context.Int64Type()
	dimensionsType := llvm.ArrayType(i64, arrayType.Rank)
	dimensionsValue := c.createEntryBlockAlloca(dimensionsType, "array_dimensions")
	zero := c.ConstI64(0)
	for i, dimension := range dimensions {
		slot := c.builder.CreateGEP(dimensionsType, dimensionsValue, []llvm.Value{zero, c.ConstI64(uint64(i))}, "array_dimension_slot")
		c.builder.CreateStore(dimension, slot)
	}

	kind := llvm.ConstInt(c.Context.Int32Type(), runtimeElementKinds[arrayType.ElemType.Kind()], false)
	fnType, fn := c.GetCFunc(ARRAY_ND_STR)
	return c.builder.CreateCall(fnType, fn, []llvm.Value{
		c.arrayDataValue(s.Val, arrayType),
		kind,
		c.ConstI64(uint64(arrayType.Rank)),
		c.builder.CreateBitCast(dimensionsValue, llvm.PointerType(i64, 0), "array_dimensions_ptr"),
	}, "array_nd_str")
}
