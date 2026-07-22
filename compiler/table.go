package compiler

import (
	"github.com/thiremani/pluto/ast"
	"tinygo.org/x/go-llvm"
)

func (c *Compiler) compileTable(lit *ast.ArrayLiteral, tableType Table) *Symbol {
	if len(lit.Rows) == 0 {
		return c.makeZeroValue(tableType)
	}

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
		columns[i] = c.tableColumnValue(value, i)
		if hasConcreteArrayElemType(column.ElemType) {
			columns[i] = c.copyArray(columns[i], column.ElemType)
		}
	}
	return c.createTableValue(rows, columns, tableType)
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
		// Empty columns have zero rows, so the runtime never reads this placeholder.
		runtimeKind := runtimeElementKinds[IntKind]
		if hasConcreteArrayElemType(column.ElemType) {
			runtimeKind = runtimeElementKinds[column.ElemType.Kind()]
		}
		c.builder.CreateStore(llvm.ConstInt(i32, runtimeKind, false), kindSlot)
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
