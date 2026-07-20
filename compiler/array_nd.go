package compiler

import (
	"fmt"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

func (c *Compiler) compileStackedArrayLiteral(lit *ast.ArrayLiteral, arrayType Array) *Symbol {
	children := make([]ast.Expression, 0)
	for _, row := range lit.Rows {
		children = append(children, row...)
	}

	childSymbols := make([]*Symbol, 0, len(children))
	for _, child := range children {
		compiled := c.compileArrayValuedCell(child)
		if len(compiled) != 1 {
			c.Errors = append(c.Errors, &token.CompileError{
				Token: child.Tok(),
				Msg:   fmt.Sprintf("array cell produced %d values; expected 1", len(compiled)),
			})
			return c.makeZeroValue(arrayType)
		}
		childSymbols = append(childSymbols, c.derefIfPointer(compiled[0], "stacked_array_child"))
	}

	childType := childSymbols[0].Type.(Array)
	childDims := c.arrayDimensions(childSymbols[0])
	for i := 1; i < len(childSymbols); i++ {
		c.requireSameArrayShape(childDims, c.arrayDimensions(childSymbols[i]), "stacked_array_shape")
	}

	dimensions := make([]llvm.Value, 0, arrayType.Rank)
	for _, dimension := range arrayLiteralLayoutShape(lit) {
		dimensions = append(dimensions, c.ConstI64(dimension))
	}
	dimensions = append(dimensions, childDims...)
	if !hasConcreteArrayElemType(arrayType.ElemType) {
		return &Symbol{Type: arrayType, Val: c.createArrayValue(llvm.Value{}, dimensions, arrayType)}
	}

	childLen := c.ArrayLen(childSymbols[0], childType.ElemType)
	totalLen := c.builder.CreateMul(c.ConstI64(uint64(len(childSymbols))), childLen, "stacked_array_len")
	data := c.CreateArrayForType(arrayType.ElemType, totalLen)
	for i, child := range childSymbols {
		childElemType := child.Type.(Array).ElemType
		if !hasConcreteArrayElemType(childElemType) {
			continue
		}
		offset := llvm.Value{}
		applyOffset := i > 0
		if applyOffset {
			offset = c.builder.CreateMul(c.ConstI64(uint64(i)), childLen, "stacked_array_offset")
		}
		c.CopyArrayInto(data, child, childElemType, arrayType.ElemType, offset, applyOffset)
		c.freeConsumedTemporary(children[i], []*Symbol{child})
	}

	return &Symbol{Type: arrayType, Val: c.createArrayValue(data, dimensions, arrayType)}
}

func (c *Compiler) compileArrayValuedCell(child ast.Expression) []*Symbol {
	var compiled []*Symbol
	c.withArrayLiteralCellMode(func() {
		compiled = c.compileExpression(child, nil)
	})
	return compiled
}

func (c *Compiler) compileStackedArrayCollector(lit *ast.ArrayLiteral, info *ExprInfo, arrayType Array) *Symbol {
	if !hasConcreteArrayElemType(arrayType.ElemType) {
		return c.makeZeroValue(arrayType)
	}

	// The runtime accumulator owns the flat leaf buffer. Shape is tracked
	// separately while lower-rank child arrays are appended.
	acc := c.NewArrayAccumulator(Array{ElemType: arrayType.ElemType, Rank: 1})
	outerLenSlot := c.createEntryBlockAlloca(c.mapToLLVMType(I64), "stacked_array_outer_len_mem")
	c.createStore(c.ConstI64(0), outerLenSlot, I64)

	childDimSlots := make([]llvm.Value, arrayType.Rank-1)
	for i := range childDimSlots {
		childDimSlots[i] = c.createEntryBlockAlloca(c.mapToLLVMType(I64), "stacked_array_dimension_mem")
		c.createStore(c.ConstI64(0), childDimSlots[i], I64)
	}

	c.withCollectorLoopNest(info.CollectRanges, lit, nil, func() {
		for _, row := range lit.Rows {
			for _, child := range row {
				c.appendStackedArrayChild(acc, outerLenSlot, childDimSlots, child)
			}
		}
	})

	dimensions := make([]llvm.Value, 0, arrayType.Rank)
	dimensions = append(dimensions, c.createLoad(outerLenSlot, I64, "stacked_array_outer_len"))
	for _, slot := range childDimSlots {
		dimensions = append(dimensions, c.createLoad(slot, I64, "stacked_array_dimension"))
	}

	return &Symbol{Type: arrayType, Val: c.createArrayValue(acc.Vec, dimensions, arrayType)}
}

func (c *Compiler) appendStackedArrayChild(
	acc *ArrayAccumulator,
	outerLenSlot llvm.Value,
	childDimSlots []llvm.Value,
	child ast.Expression,
) {
	compiled := c.compileArrayValuedCell(child)
	if len(compiled) != 1 {
		c.Errors = append(c.Errors, &token.CompileError{
			Token: child.Tok(),
			Msg:   fmt.Sprintf("array cell produced %d values; expected 1", len(compiled)),
		})
		return
	}

	childSymbol := c.derefIfPointer(compiled[0], "stacked_array_child")
	childType := childSymbol.Type.(Array)
	childDimensions := c.arrayDimensions(childSymbol)
	outerLen := c.createLoad(outerLenSlot, I64, "stacked_array_outer_len")
	firstChild := c.builder.CreateICmp(llvm.IntEQ, outerLen, c.ConstI64(0), "stacked_array_first_child")

	for i, dimension := range childDimensions {
		stored := c.createLoad(childDimSlots[i], I64, "stacked_array_dimension")
		equal := c.builder.CreateICmp(llvm.IntEQ, stored, dimension, "stacked_array_shape")
		c.failShapeOnFalse(c.builder.CreateOr(firstChild, equal, "stacked_array_shape_compatible"), stored, dimension, "stacked_array_shape")
		c.createStore(c.builder.CreateSelect(firstChild, dimension, stored, "stacked_array_dimension_value"), childDimSlots[i], I64)
	}

	if hasConcreteArrayElemType(childType.ElemType) {
		length := c.ArrayLen(childSymbol, childType.ElemType)
		c.createLoop(c.rangeZeroToN(length), func(iter llvm.Value) {
			value := c.ArrayGetBorrowed(childSymbol, childType.ElemType, iter)
			c.pushAccumCellValue(acc, &Symbol{Val: value, Type: childType.ElemType}, false, acc.ElemType)
		})
	}

	c.createStore(c.builder.CreateAdd(outerLen, c.ConstI64(1), "stacked_array_outer_len_next"), outerLenSlot, I64)
	c.freeTemporary(child, []*Symbol{childSymbol})
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
	fn := c.builder.GetInsertBlock().Parent()
	failBlock := c.Context.AddBasicBlock(fn, name+"_fail")
	contBlock := c.Context.AddBasicBlock(fn, name+"_cont")
	c.builder.CreateCondBr(ok, contBlock, failBlock)

	c.builder.SetInsertPointAtEnd(failBlock)
	failTy, failFn := c.GetCFunc(ARRAY_SHAPE_FAIL)
	c.builder.CreateCall(failTy, failFn, []llvm.Value{expected, got}, "")
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

	runtimeKind := runtimeElementKinds[IntKind]
	if hasConcreteArrayElemType(arrayType.ElemType) {
		runtimeKind = runtimeElementKinds[arrayType.ElemType.Kind()]
	}
	kind := llvm.ConstInt(c.Context.Int32Type(), runtimeKind, false)
	fnType, fn := c.GetCFunc(ARRAY_ND_STR)
	return c.builder.CreateCall(fnType, fn, []llvm.Value{
		c.arrayDataValue(s.Val, arrayType),
		kind,
		c.ConstI64(uint64(arrayType.Rank)),
		c.builder.CreateBitCast(dimensionsValue, llvm.PointerType(i64, 0), "array_dimensions_ptr"),
	}, "array_nd_str")
}
