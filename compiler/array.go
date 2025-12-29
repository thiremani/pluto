package compiler

import (
	"fmt"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

type ArrayInfo struct {
	PtrName    string
	NewName    string
	ResizeName string
	SetName    string
	GetName    string
	LenName    string
	StrName    string
	PushName   string
}

type ArrayAccumulator struct {
	Vec       llvm.Value
	ElemType  Type
	ArrayType Array
	Info      ArrayInfo
}

var ArrayInfos = map[Kind]ArrayInfo{
	IntKind: {
		PtrName:    "PtArrayI64",
		NewName:    ARR_I64_NEW,
		ResizeName: ARR_I64_RESIZE,
		SetName:    ARR_I64_SET,
		GetName:    ARR_I64_GET,
		LenName:    ARR_I64_LEN,
		StrName:    ARR_I64_STR,
		PushName:   ARR_I64_PUSH,
	},
	FloatKind: {
		PtrName:    "PtArrayF64",
		NewName:    ARR_F64_NEW,
		ResizeName: ARR_F64_RESIZE,
		SetName:    ARR_F64_SET,
		GetName:    ARR_F64_GET,
		LenName:    ARR_F64_LEN,
		StrName:    ARR_F64_STR,
		PushName:   ARR_F64_PUSH,
	},
	StrKind: {
		PtrName:    "PtArrayStr",
		NewName:    ARR_STR_NEW,
		ResizeName: ARR_STR_RESIZE,
		SetName:    ARR_STR_SET,
		GetName:    ARR_STR_GET,
		LenName:    ARR_STR_LEN,
		StrName:    ARR_STR_STR,
		PushName:   ARR_STR_PUSH,
	},
}

func (c *Compiler) NewArrayAccumulator(arr Array) *ArrayAccumulator {
	info := ArrayInfos[arr.ColTypes[0].Kind()]
	fnTy, fn := c.GetCFunc(info.NewName)
	vec := c.builder.CreateCall(fnTy, fn, nil, "range_arr_new")
	return &ArrayAccumulator{
		Vec:       vec,
		ElemType:  arr.ColTypes[0],
		ArrayType: arr,
		Info:      info,
	}
}

func (c *Compiler) PushVal(acc *ArrayAccumulator, value *Symbol) {
	valSym := c.derefIfPointer(value)
	pushTy, pushFn := c.GetCFunc(acc.Info.PushName)
	c.builder.CreateCall(pushTy, pushFn, []llvm.Value{acc.Vec, valSym.Val}, "range_arr_push")
}

func (c *Compiler) ArrayAccResult(acc *ArrayAccumulator) *Symbol {
	i8p := llvm.PointerType(c.Context.Int8Type(), 0)
	return &Symbol{
		Val:  c.builder.CreateBitCast(acc.Vec, i8p, "range_arr_result"),
		Type: acc.ArrayType,
	}
}

func (c *Compiler) CastArrayElem(val llvm.Value, from, to Type) llvm.Value {
	if from.Kind() == to.Kind() {
		return val
	}
	switch {
	case from.Kind() == IntKind && to.Kind() == FloatKind:
		// Safe: lossless widening conversion
		return c.builder.CreateSIToFP(val, c.Context.DoubleType(), "i64_to_f64")
	default:
		// Note: Float→Int cast is intentionally NOT supported here.
		// The type solver always promotes int→float to prevent lossy conversions.
		// If explicit conversion is needed in the future, add an int() function.
		panic(fmt.Sprintf("unsupported array element cast: %s -> %s", from.String(), to.String()))
	}
}

func (c *Compiler) CopyArrayInto(vec llvm.Value, src *Symbol, srcElem, resElem Type, offset llvm.Value, applyOffset bool) {
	length := c.ArrayLen(src, srcElem)
	r := c.rangeZeroToN(length)
	c.createLoop(r, func(iter llvm.Value) {
		dstIdx := iter
		if applyOffset {
			dstIdx = c.builder.CreateAdd(iter, offset, "concat_idx")
		}
		elem := c.ArrayGet(src, srcElem, iter)
		elem = c.CastArrayElem(elem, srcElem, resElem)
		c.ArraySetForType(resElem, vec, dstIdx, elem)
	})
}

// ArrayBitCast casts an array value to the appropriate named opaque pointer type
func (c *Compiler) ArrayBitCast(arr llvm.Value, info ArrayInfo, name string) llvm.Value {
	pt := c.NamedOpaquePtr(info.PtrName)
	return c.builder.CreateBitCast(arr, pt, name)
}

func (c *Compiler) CreateArrayForType(elem Type, length llvm.Value) llvm.Value {
	info := ArrayInfos[elem.Kind()]

	// Create new array
	_, newFn := c.GetCFunc(info.NewName)
	vec := c.builder.CreateCall(c.GetFnType(info.NewName), newFn, nil, "arr_new")

	// Resize array
	_, rezFn := c.GetCFunc(info.ResizeName)
	switch elem.Kind() {
	case IntKind:
		zero := c.ConstI64(0)
		c.builder.CreateCall(c.GetFnType(info.ResizeName), rezFn, []llvm.Value{vec, length, zero}, "arr_resize")
	case FloatKind:
		zero := c.ConstF64(0)
		c.builder.CreateCall(c.GetFnType(info.ResizeName), rezFn, []llvm.Value{vec, length, zero}, "arr_resize")
	case StrKind:
		c.builder.CreateCall(c.GetFnType(info.ResizeName), rezFn, []llvm.Value{vec, length}, "arr_resize")
	}

	return vec
}

func (c *Compiler) ArraySetForType(elem Type, vec llvm.Value, idx llvm.Value, value llvm.Value) {
	info := ArrayInfos[elem.Kind()]

	fnTy, fn := c.GetCFunc(info.SetName)
	c.builder.CreateCall(fnTy, fn, []llvm.Value{vec, idx, value}, info.SetName)
}

func (c *Compiler) ArrayLen(arr *Symbol, elem Type) llvm.Value {
	info := ArrayInfos[elem.Kind()]

	cast := c.ArrayBitCast(arr.Val, info, "arrp")
	fnTy, fn := c.GetCFunc(info.LenName)
	return c.builder.CreateCall(fnTy, fn, []llvm.Value{cast}, "len")
}

func (c *Compiler) ArrayGet(arr *Symbol, elem Type, idx llvm.Value) llvm.Value {
	info := ArrayInfos[elem.Kind()]

	cast := c.ArrayBitCast(arr.Val, info, "arrp")
	fnTy, fn := c.GetCFunc(info.GetName)
	return c.builder.CreateCall(fnTy, fn, []llvm.Value{cast, idx}, "get")
}

// ArraySetCells populates an array with cell values, handling type conversions
func (c *Compiler) ArraySetCells(vec llvm.Value, cells []*Symbol, elemType Type) {
	info := ArrayInfos[elemType.Kind()]

	_, setFn := c.GetCFunc(info.SetName)
	for i, cs := range cells {
		idx := c.ConstI64(uint64(i))
		val := cs.Val

		// Handle type conversions
		// Note: Only safe int→float promotion is supported.
		// The type solver prevents lossy float→int conversions.
		if elemType.Kind() == FloatKind && cs.Type.Kind() == IntKind {
			val = c.builder.CreateSIToFP(cs.Val, c.Context.DoubleType(), "i64_to_f64")
		}

		c.builder.CreateCall(c.GetFnType(info.SetName), setFn, []llvm.Value{vec, idx, val}, "arr_set")
	}
}

// Array compilation functions

// compileArrayExpression materializes simple array literals into runtime vectors.
// Currently supports only a single row with no headers, e.g. [1 2 3 4].
func (c *Compiler) compileArrayExpression(e *ast.ArrayLiteral, _ []*ast.Identifier) (res []*Symbol) {
	lit, info := c.resolveArrayLiteralRewrite(e)

	// If ArrayLiteral has ranges, use with-loops path which creates accumulator
	// pendingLoopRanges will filter already-bound ranges to prevent double-looping
	if len(info.Ranges) == 0 {
		return c.compileArrayLiteralImmediate(lit, info)
	}

	return c.compileArrayLiteralWithLoops(lit, info)
}

// resolveArrayLiteralRewrite retrieves the potentially rewritten array literal and its ExprInfo.
// The type solver may rewrite array literals to replace range expressions with temporary iterators.
func (c *Compiler) resolveArrayLiteralRewrite(e *ast.ArrayLiteral) (*ast.ArrayLiteral, *ExprInfo) {
	lit := e
	info := c.ExprCache[key(c.FuncNameMangled, e)]

	// Check if the expression was rewritten by the type solver
	if info.Rewrite != nil {
		if rew, ok := info.Rewrite.(*ast.ArrayLiteral); ok {
			lit = rew
		}
	}

	// Use the rewritten literal's cache entry if available
	if alt, ok := c.ExprCache[key(c.FuncNameMangled, lit)]; ok && alt != nil {
		info = alt
	}

	return lit, info
}

func (c *Compiler) compileArrayLiteralImmediate(lit *ast.ArrayLiteral, info *ExprInfo) (res []*Symbol) {
	s := &Symbol{}

	if !(len(lit.Headers) == 0 && (len(lit.Rows) == 0 || len(lit.Rows) == 1)) {
		c.Errors = append(c.Errors, &token.CompileError{Token: lit.Tok(), Msg: "2D arrays/tables not implemented yet"})
		return nil
	}

	// Handle empty array literal: []
	if len(lit.Rows) == 0 {
		arr := info.OutTypes[0].(Array)
		elemType := arr.ColTypes[0]

		// If element type is unresolved, create a null pointer
		// The actual array will be created when the variable is used with a concrete type
		if elemType.Kind() == UnresolvedKind {
			s.Type = arr
			s.Val = llvm.ConstPointerNull(llvm.PointerType(c.Context.Int8Type(), 0))
			return []*Symbol{s}
		}

		nConst := c.ConstI64(0)
		arrVal := c.CreateArrayForType(elemType, nConst)

		s.Type = arr
		s.Val = c.builder.CreateBitCast(arrVal, llvm.PointerType(c.Context.Int8Type(), 0), "arr_i8p")
		return []*Symbol{s}
	}

	row := lit.Rows[0]
	cells := make([]*Symbol, len(row))
	for i, cell := range row {
		// Compile cells
		vals := c.compileExpression(cell, nil)
		cells[i] = c.derefIfPointer(vals[0])
	}

	arr := info.OutTypes[0].(Array)
	elemType := arr.ColTypes[0]

	nConst := c.ConstI64(uint64(len(row)))

	arrVal := c.CreateArrayForType(elemType, nConst)
	c.ArraySetCells(arrVal, cells, elemType)

	s.Type = arr
	s.Val = c.builder.CreateBitCast(arrVal, llvm.PointerType(c.Context.Int8Type(), 0), "arr_i8p")

	return []*Symbol{s}
}

func (c *Compiler) compileArrayLiteralWithLoops(lit *ast.ArrayLiteral, info *ExprInfo) []*Symbol {
	arr := info.OutTypes[0].(Array)
	elemType := arr.ColTypes[0]
	acc := c.NewArrayAccumulator(arr)
	row := lit.Rows[0]

	c.withLoopNest(info.Ranges, func() {
		for _, cell := range row {
			vals := c.compileExpression(cell, nil)
			valSym := c.derefIfPointer(vals[0])

			val := valSym.Val
			if valSym.Type.Kind() != elemType.Kind() {
				val = c.CastArrayElem(val, valSym.Type, elemType)
			}

			c.PushVal(acc, &Symbol{Val: val, Type: elemType})
		}
	})

	return []*Symbol{c.ArrayAccResult(acc)}
}

// Array operation functions

func (c *Compiler) compileArrayArrayInfix(op string, leftArr *Symbol, rightArr *Symbol, resElem Type) *Symbol {
	leftArrType := leftArr.Type.(Array)
	rightArrType := rightArr.Type.(Array)

	leftElem := leftArrType.ColTypes[0]
	rightElem := rightArrType.ColTypes[0]

	// Array concatenation: arr1 ⊕ arr2
	if op == token.SYM_CONCAT {
		return c.compileArrayConcat(leftArr, rightArr, leftElem, rightElem, resElem)
	}

	// Element-wise array operation: arr1 op arr2
	// Strategy: iterate to min(len(arr1), len(arr2)) - no implicit padding.
	// This mirrors vector-style zip semantics and avoids silently inventing data.

	// Get lengths of both arrays
	leftLen := c.ArrayLen(leftArr, leftElem)
	rightLen := c.ArrayLen(rightArr, rightElem)

	// Calculate min length for result
	minLen := c.builder.CreateSelect(
		c.builder.CreateICmp(llvm.IntULT, leftLen, rightLen, "cmp_len"),
		leftLen,
		rightLen,
		"min_len",
	)

	// Create result array
	resVec := c.CreateArrayForType(resElem, minLen)

	// Element-wise operation over the full length
	r := c.rangeZeroToN(minLen)
	c.createLoop(r, func(iter llvm.Value) {
		leftVal := c.ArrayGet(leftArr, leftElem, iter)
		rightVal := c.ArrayGet(rightArr, rightElem, iter)

		leftSym := &Symbol{Val: leftVal, Type: leftElem}
		rightSym := &Symbol{Val: rightVal, Type: rightElem}

		computed := c.compileInfix(op, leftSym, rightSym, resElem)

		// Convert to result element type if needed
		resultVal := computed.Val
		if computed.Type.Kind() == IntKind && resElem.Kind() == FloatKind {
			resultVal = c.builder.CreateSIToFP(computed.Val, c.Context.DoubleType(), "cast_to_resElem")
		}

		c.ArraySetForType(resElem, resVec, iter, resultVal)
	})

	// Return result array
	i8p := llvm.PointerType(c.Context.Int8Type(), 0)
	resSym := &Symbol{Type: Array{Headers: nil, ColTypes: []Type{resElem}, Length: 0}}
	resSym.Val = c.builder.CreateBitCast(resVec, i8p, "arr_i8p")
	return resSym
}

func (c *Compiler) compileArrayConcat(leftArr *Symbol, rightArr *Symbol, leftElem Type, rightElem Type, resElem Type) *Symbol {
	// Array concatenation: arr1 ⊕ arr2
	// Result is [arr1..., arr2...]

	// Get lengths of both arrays
	leftLen := c.ArrayLen(leftArr, leftElem)
	rightLen := c.ArrayLen(rightArr, rightElem)

	// Calculate total length
	totalLen := c.builder.CreateAdd(leftLen, rightLen, "concat_len")

	// Create new array with total length
	resVec := c.CreateArrayForType(resElem, totalLen)

	// Copy left array elements
	c.CopyArrayInto(resVec, leftArr, leftElem, resElem, llvm.Value{}, false)

	// Copy right array elements with offset
	c.CopyArrayInto(resVec, rightArr, rightElem, resElem, leftLen, true)

	// Return concatenated array
	i8p := llvm.PointerType(c.Context.Int8Type(), 0)
	resSym := &Symbol{Type: Array{Headers: nil, ColTypes: []Type{resElem}, Length: 0}}
	resSym.Val = c.builder.CreateBitCast(resVec, i8p, "arr_i8p")
	return resSym
}

func (c *Compiler) compileArrayScalarInfix(op string, arr *Symbol, scalar *Symbol, resElem Type, arrayOnLeft bool) *Symbol {
	arrType := arr.Type.(Array)
	arrElem := arrType.ColTypes[0]

	lenVal := c.ArrayLen(arr, arrElem)
	resVec := c.CreateArrayForType(resElem, lenVal)

	scalarSym := c.derefIfPointer(scalar)

	r := c.rangeZeroToN(lenVal)
	c.createLoop(r, func(iter llvm.Value) {
		idx := iter
		val := c.ArrayGet(arr, arrElem, idx)
		elemSym := &Symbol{Val: val, Type: arrElem}

		// Respect the original operand order for non-commutative operations
		var computed *Symbol
		if arrayOnLeft {
			computed = c.compileInfix(op, elemSym, scalarSym, resElem)
		} else {
			computed = c.compileInfix(op, scalarSym, elemSym, resElem)
		}

		// Convert to result element type if needed
		resultVal := computed.Val
		if computed.Type.Kind() == IntKind && resElem.Kind() == FloatKind {
			resultVal = c.builder.CreateSIToFP(computed.Val, c.Context.DoubleType(), "cast_to_resElem")
		}

		c.ArraySetForType(resElem, resVec, idx, resultVal)
	})

	i8p := llvm.PointerType(c.Context.Int8Type(), 0)
	resSym := &Symbol{Type: Array{Headers: nil, ColTypes: []Type{resElem}, Length: 0}}
	resSym.Val = c.builder.CreateBitCast(resVec, i8p, "arr_i8p")
	return resSym
}

func (c *Compiler) compileArrayUnaryPrefix(op string, arr *Symbol, result Array) *Symbol {
	arrType := arr.Type.(Array)
	elem := arrType.ColTypes[0]
	resElem := result.ColTypes[0]
	n := c.ArrayLen(arr, elem)
	resVec := c.CreateArrayForType(resElem, n)

	r := c.rangeZeroToN(n)
	c.createLoop(r, func(iter llvm.Value) {
		idx := iter
		v := c.ArrayGet(arr, elem, idx)
		opSym := &Symbol{Val: v, Type: elem}
		computed := c.compilePrefix(op, opSym, resElem)

		// Convert to result element type if needed
		resultVal := computed.Val
		if computed.Type.Kind() == IntKind && resElem.Kind() == FloatKind {
			resultVal = c.builder.CreateSIToFP(computed.Val, c.Context.DoubleType(), "cast_to_resElem")
		}

		c.ArraySetForType(resElem, resVec, idx, resultVal)
	})

	i8p := llvm.PointerType(c.Context.Int8Type(), 0)
	resSym := &Symbol{Type: result}
	resSym.Val = c.builder.CreateBitCast(resVec, i8p, "arr_i8p")
	return resSym
}

// Array string conversion function

func (c *Compiler) arrayStrArg(s *Symbol) llvm.Value {
	arr := s.Type.(Array)
	if len(arr.ColTypes) != 1 {
		panic("internal: arrayStrArg supports only single-column vectors")
	}

	elemType := arr.ColTypes[0]
	info, ok := ArrayInfos[elemType.Kind()]
	if !ok {
		panic("internal: unsupported array element kind for printing")
	}

	cast := c.ArrayBitCast(s.Val, info, "arr_cast")
	fnTy, fn := c.GetCFunc(info.StrName)
	return c.builder.CreateCall(fnTy, fn, []llvm.Value{cast}, "arr_str")
}

func (c *Compiler) arrayRangeStrArgs(s *Symbol) (arrayStr llvm.Value, rangeStr llvm.Value) {
	arrRange := s.Type.(ArrayRange)
	agg := s.Val
	arrPtr := c.builder.CreateExtractValue(agg, 0, "array_range_arr")
	arrSym := &Symbol{Val: arrPtr, Type: arrRange.Array}
	arrayStr = c.arrayStrArg(arrSym)

	rangeVal := c.builder.CreateExtractValue(agg, 1, "array_range_rng")
	rangeSym := &Symbol{Val: rangeVal, Type: arrRange.Range}
	rangeStr = c.rangeStrArg(rangeSym)
	return
}

// compileArrayRangeExpression compiles an array indexing expression.
// If the index is a range (e.g., arr[0:10]), returns an ArrayRange symbol.
// If the index is a scalar (e.g., arr[4]), returns the element.
// We check the actual compiled index type, not cached OutTypes, because
// inside a loop the index may be bound to a scalar even if originally a range.
func (c *Compiler) compileArrayRangeExpression(expr *ast.ArrayRangeExpression) []*Symbol {
	arraySym := c.derefIfPointer(c.compileExpression(expr.Array, nil)[0])
	idxSym := c.derefIfPointer(c.compileExpression(expr.Range, nil)[0])
	arrType := arraySym.Type.(Array)

	// Check actual compiled index type to determine ArrayRange vs element access
	if idxSym.Type.Kind() == RangeKind {
		arrRange := ArrayRange{
			Array: arrType,
			Range: idxSym.Type.(Range),
		}
		return []*Symbol{{
			Val:  c.CreateArrayRange(arraySym.Val, idxSym.Val, arrRange),
			Type: arrRange,
		}}
	}

	// Scalar index - element access
	elemType := arrType.ColTypes[0]
	idxVal := idxSym.Val
	if intType, ok := idxSym.Type.(Int); ok && intType.Width != 64 {
		idxVal = c.builder.CreateIntCast(idxVal, c.Context.Int64Type(), "arr_idx_cast")
	}

	elemVal := c.ArrayGet(arraySym, elemType, idxVal)
	return []*Symbol{{Type: elemType, Val: elemVal}}
}
