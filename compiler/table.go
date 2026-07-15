package compiler

import (
	"github.com/thiremani/pluto/ast"
	"tinygo.org/x/go-llvm"
)

var runtimeElementKinds = map[Kind]uint64{
	IntKind:   0,
	FloatKind: 1,
	StrKind:   2,
}

func (c *Compiler) compileMatrixLiteral(lit *ast.ArrayLiteral, matrixType Matrix) *Symbol {
	rows := len(lit.Rows)
	cols := 0
	if rows > 0 {
		cols = len(lit.Rows[0])
	}

	data := c.CreateArrayForType(matrixType.ElemType, c.ConstI64(uint64(rows*cols)))
	for rowIndex, row := range lit.Rows {
		for colIndex, cell := range row {
			index := c.ConstI64(uint64(rowIndex*cols + colIndex))
			c.compileArrayLiteralCell(cell, matrixType.ElemType, func(cellSlot *Symbol) bool {
				return c.setArrayCellSlot(data, index, cellSlot, matrixType.ElemType)
			})
		}
	}

	data = c.builder.CreateBitCast(data, llvm.PointerType(c.Context.Int8Type(), 0), "matrix_data")
	return &Symbol{
		Type: matrixType,
		Val: c.createMatrixValue(
			data,
			c.ConstI64(uint64(rows)),
			c.ConstI64(uint64(cols)),
			matrixType,
		),
	}
}

func (c *Compiler) compileTableLiteral(lit *ast.ArrayLiteral, tableType Table) *Symbol {
	rowCount := c.ConstI64(uint64(len(lit.Rows)))
	columns := make([]llvm.Value, len(tableType.Columns))
	for i, column := range tableType.Columns {
		columns[i] = c.CreateArrayForType(column.ElemType, rowCount)
	}

	// Preserve source evaluation order even though storage is columnar.
	for rowIndex, row := range lit.Rows {
		for colIndex, cell := range row {
			column := tableType.Columns[colIndex]
			index := c.ConstI64(uint64(rowIndex))
			c.compileArrayLiteralCell(cell, column.ElemType, func(cellSlot *Symbol) bool {
				return c.setArrayCellSlot(columns[colIndex], index, cellSlot, column.ElemType)
			})
		}
	}

	i8p := llvm.PointerType(c.Context.Int8Type(), 0)
	for i := range columns {
		columns[i] = c.builder.CreateBitCast(columns[i], i8p, "table_column")
	}
	return &Symbol{
		Type: tableType,
		Val:  c.createTableValue(rowCount, columns, tableType),
	}
}

func (c *Compiler) createMatrixValue(data, rows, cols llvm.Value, matrixType Matrix) llvm.Value {
	value := llvm.Undef(c.mapToLLVMType(matrixType))
	value = c.builder.CreateInsertValue(value, data, 0, "matrix_data_value")
	value = c.builder.CreateInsertValue(value, rows, 1, "matrix_rows")
	return c.builder.CreateInsertValue(value, cols, 2, "matrix_cols")
}

func (c *Compiler) matrixArrayValue(matrix llvm.Value) llvm.Value {
	return c.builder.CreateExtractValue(matrix, 0, "matrix_data")
}

func (c *Compiler) copyMatrixValue(value llvm.Value, matrixType Matrix) llvm.Value {
	data := c.copyArray(c.matrixArrayValue(value), matrixType.ElemType)
	rows := c.builder.CreateExtractValue(value, 1, "matrix_rows")
	cols := c.builder.CreateExtractValue(value, 2, "matrix_cols")
	return c.createMatrixValue(data, rows, cols, matrixType)
}

func (c *Compiler) createTableValue(rowCount llvm.Value, columns []llvm.Value, tableType Table) llvm.Value {
	value := llvm.Undef(c.mapToLLVMType(tableType))
	value = c.builder.CreateInsertValue(value, rowCount, 0, "table_rows")
	for i, column := range columns {
		value = c.builder.CreateInsertValue(value, column, i+1, "table_column")
	}
	return value
}

func (c *Compiler) tableColumnValue(table llvm.Value, column int) llvm.Value {
	return c.builder.CreateExtractValue(table, column+1, "table_column")
}

func (c *Compiler) copyTableValue(value llvm.Value, tableType Table) llvm.Value {
	rows := c.builder.CreateExtractValue(value, 0, "table_rows")
	columns := make([]llvm.Value, len(tableType.Columns))
	for i, column := range tableType.Columns {
		columns[i] = c.copyArray(c.tableColumnValue(value, i), column.ElemType)
	}
	return c.createTableValue(rows, columns, tableType)
}

func (c *Compiler) matrixStrArg(s *Symbol) llvm.Value {
	matrixType := s.Type.(Matrix)
	data := c.matrixArrayValue(s.Val)
	rows := c.builder.CreateExtractValue(s.Val, 1, "matrix_rows")
	cols := c.builder.CreateExtractValue(s.Val, 2, "matrix_cols")
	kind := llvm.ConstInt(c.Context.Int32Type(), runtimeElementKinds[matrixType.ElemType.Kind()], false)
	fnType, fn := c.GetCFunc(MATRIX_STR)
	return c.builder.CreateCall(fnType, fn, []llvm.Value{data, kind, rows, cols}, "matrix_str")
}

func (c *Compiler) tableStrArg(s *Symbol) llvm.Value {
	tableType := s.Type.(Table)
	rows := c.builder.CreateExtractValue(s.Val, 0, "table_rows")
	columnCount := len(tableType.Columns)
	if columnCount == 0 {
		null := llvm.ConstPointerNull(llvm.PointerType(c.Context.Int8Type(), 0))
		fnType, fn := c.GetCFunc(TABLE_STR)
		return c.builder.CreateCall(fnType, fn, []llvm.Value{rows, c.ConstI64(0), null, null, null}, "table_str")
	}

	i8p := llvm.PointerType(c.Context.Int8Type(), 0)
	i32 := c.Context.Int32Type()
	namesType := llvm.ArrayType(i8p, columnCount)
	kindsType := llvm.ArrayType(i32, columnCount)
	columnsType := llvm.ArrayType(i8p, columnCount)
	names := c.createEntryBlockAlloca(namesType, "table_names")
	kinds := c.createEntryBlockAlloca(kindsType, "table_kinds")
	columns := c.createEntryBlockAlloca(columnsType, "table_columns")
	zero := c.ConstI64(0)

	for i, column := range tableType.Columns {
		index := c.ConstI64(uint64(i))
		indices := []llvm.Value{zero, index}
		nameSlot := c.builder.CreateGEP(namesType, names, indices, "table_name_slot")
		kindSlot := c.builder.CreateGEP(kindsType, kinds, indices, "table_kind_slot")
		columnSlot := c.builder.CreateGEP(columnsType, columns, indices, "table_column_slot")
		c.builder.CreateStore(c.constCString(column.Name), nameSlot)
		c.builder.CreateStore(llvm.ConstInt(i32, runtimeElementKinds[column.ElemType.Kind()], false), kindSlot)
		c.builder.CreateStore(c.tableColumnValue(s.Val, i), columnSlot)
	}

	fnType, fn := c.GetCFunc(TABLE_STR)
	return c.builder.CreateCall(fnType, fn, []llvm.Value{
		rows,
		c.ConstI64(uint64(columnCount)),
		c.builder.CreateBitCast(names, i8p, "table_names_ptr"),
		c.builder.CreateBitCast(kinds, i8p, "table_kinds_ptr"),
		c.builder.CreateBitCast(columns, i8p, "table_columns_ptr"),
	}, "table_str")
}
