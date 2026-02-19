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

// PushVal appends using kind-based runtime dispatch.
// Any future string flavor (e.g. StrS) auto-uses arr_str_push as long as
// Kind()==StrKind and the LLVM value is char* compatible.
func (c *Compiler) PushVal(acc *ArrayAccumulator, value *Symbol) {
	valSym := c.derefIfPointer(value, "")
	pushTy, pushFn := c.GetCFunc(acc.Info.PushName)
	c.builder.CreateCall(pushTy, pushFn, []llvm.Value{acc.Vec, valSym.Val}, "range_arr_push")
}

// PushValOwn pushes a value to an array accumulator, transferring ownership for strings.
// Only use this for heap-allocated temporaries (e.g., str_concat results), NOT for
// identifiers or borrowed values which would leave the original variable dangling.
func (c *Compiler) PushValOwn(acc *ArrayAccumulator, value *Symbol) {
	valSym := c.derefIfPointer(value, "")

	// Use push_own only for heap strings. Other string flavors (e.g. StrG/StrS)
	// must be copied so future string kinds remain auto-supported via PushVal.
	if acc.ElemType.Kind() == StrKind && IsStrH(valSym.Type) {
		pushTy, pushFn := c.GetCFunc(ARR_STR_PUSH_OWN)
		c.builder.CreateCall(pushTy, pushFn, []llvm.Value{acc.Vec, valSym.Val}, "range_arr_push_own")
		return
	}

	// Fallback to copy semantics for non-heap strings and value types.
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
		// Use borrowed get for internal copy - set will make its own copy for strings
		elem := c.ArrayGetBorrowed(src, srcElem, iter)
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

// ArraySetOwnForType sets an array element, taking ownership of the value for strings.
// For strings, this uses arr_str_set_own which avoids the duplicate strdup.
// For value types (int, float), this is identical to ArraySetForType.
func (c *Compiler) ArraySetOwnForType(elem Type, vec llvm.Value, idx llvm.Value, value llvm.Value) {
	info := ArrayInfos[elem.Kind()]

	// For strings, use set_own to transfer ownership without duplicating
	if elem.Kind() == StrKind {
		fnTy, fn := c.GetCFunc(ARR_STR_SET_OWN)
		c.builder.CreateCall(fnTy, fn, []llvm.Value{vec, idx, value}, ARR_STR_SET_OWN)
		return
	}

	// For value types, regular set is fine
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

// ArrayGetBorrowed returns a borrowed reference to an array element.
// For string arrays, this avoids the strdup that ArrayGet performs.
// For int/float arrays, this is identical to ArrayGet (value types have no ownership).
// Use this for internal operations where the value is immediately passed to set/push.
func (c *Compiler) ArrayGetBorrowed(arr *Symbol, elem Type, idx llvm.Value) llvm.Value {
	info := ArrayInfos[elem.Kind()]
	cast := c.ArrayBitCast(arr.Val, info, "arrp")

	// For strings, use the borrow function to avoid unnecessary strdup
	if elem.Kind() == StrKind {
		fnTy, fn := c.GetCFunc(ARR_STR_BORROW)
		return c.builder.CreateCall(fnTy, fn, []llvm.Value{cast, idx}, "borrow")
	}

	// For value types (int, float), regular get is fine - no ownership issues
	fnTy, fn := c.GetCFunc(info.GetName)
	return c.builder.CreateCall(fnTy, fn, []llvm.Value{cast, idx}, "get")
}

// isStrHTemporary returns true if the expression is a heap string temporary
// whose ownership can be transferred (not an identifier that needs to retain ownership).
func isStrHTemporary(sym *Symbol, expr ast.Expression) bool {
	if !IsStrH(sym.Type) {
		return false
	}
	_, isIdent := expr.(*ast.Identifier)
	return !isIdent
}

// ArraySetCells populates an array with cell values, using ownership transfer
// for heap-allocated temporaries (non-identifier expressions) to avoid double-allocation.
// Identifiers and static strings are copied to preserve the original value.
func (c *Compiler) ArraySetCells(vec llvm.Value, cells []*Symbol, exprs []ast.Expression, elemType Type) {
	info := ArrayInfos[elemType.Kind()]

	for i, cs := range cells {
		idx := c.ConstI64(uint64(i))
		val := cs.Val

		// Handle type conversions
		if elemType.Kind() == FloatKind && cs.Type.Kind() == IntKind {
			val = c.builder.CreateSIToFP(cs.Val, c.Context.DoubleType(), "i64_to_f64")
		}

		// For StrH temporaries, transfer ownership; all other values get copied
		if isStrHTemporary(cs, exprs[i]) {
			fnTy, fn := c.GetCFunc(ARR_STR_SET_OWN)
			c.builder.CreateCall(fnTy, fn, []llvm.Value{vec, idx, val}, "arr_set_own")
			continue
		}

		fnTy, fn := c.GetCFunc(info.SetName)
		c.builder.CreateCall(fnTy, fn, []llvm.Value{vec, idx, val}, "arr_set")
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
		cells[i] = c.derefIfPointer(vals[0], "")
	}

	arr := info.OutTypes[0].(Array)
	elemType := arr.ColTypes[0]

	nConst := c.ConstI64(uint64(len(row)))

	arrVal := c.CreateArrayForType(elemType, nConst)
	// Use expression-aware set to transfer ownership for temporaries only
	c.ArraySetCells(arrVal, cells, row, elemType)

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
			c.compileCondExprValue(cell, llvm.Value{}, func() {
				vals := c.compileExpression(cell, nil)
				_, isIdent := cell.(*ast.Identifier)
				c.pushAccumCellValue(acc, c.derefIfPointer(vals[0], ""), !isIdent, elemType)
			})
		}
	})

	return []*Symbol{c.ArrayAccResult(acc)}
}

// pushAccumCellValue appends one accumulated cell value, handling element casts
// and string ownership transfer.
func (c *Compiler) pushAccumCellValue(acc *ArrayAccumulator, valSym *Symbol, isTemp bool, elemType Type) {
	val := valSym.Val
	valType := valSym.Type // Preserve original type for ownership info
	if valSym.Type.Kind() != elemType.Kind() {
		val = c.CastArrayElem(val, valSym.Type, elemType)
		valType = elemType // Cast changes the type
	}

	sym := &Symbol{Val: val, Type: valType}

	switch elemType.Kind() {
	case IntKind, FloatKind:
		c.PushVal(acc, sym)
		return
	case StrKind:
		// For strings, copy by default (works for StrG, StrH, and future string
		// flavors like StrS). Transfer ownership only for heap temporaries.
		if IsStrH(valType) && isTemp {
			c.PushValOwn(acc, sym)
			return
		}
		c.PushVal(acc, sym)
		return
	default:
		// Fail fast for new composite/heap-owning element kinds until explicit
		// ownership policy is defined (e.g., struct/stack-array element support).
		panic(fmt.Sprintf("unsupported accumulator element type: %s", elemType))
	}
}

// Array operation functions

// arrayPairMinLen returns min(len(left), len(right)) for two arrays.
func (c *Compiler) arrayPairMinLen(left *Symbol, right *Symbol) llvm.Value {
	leftElem := left.Type.(Array).ColTypes[0]
	rightElem := right.Type.(Array).ColTypes[0]
	leftLen := c.ArrayLen(left, leftElem)
	rightLen := c.ArrayLen(right, rightElem)
	return c.builder.CreateSelect(
		c.builder.CreateICmp(llvm.IntULT, leftLen, rightLen, "cmp_len"),
		leftLen, rightLen, "min_len",
	)
}

// forEachArrayPair iterates element-wise over an array LHS and either an array
// or scalar RHS, using loopLen as the iteration count.
func (c *Compiler) forEachArrayPair(
	left *Symbol,
	right *Symbol,
	loopLen llvm.Value,
	body func(iter llvm.Value, leftSym *Symbol, rightSym *Symbol),
) {
	leftElem := left.Type.(Array).ColTypes[0]
	rightIsArray := right.Type.Kind() == ArrayKind
	var rightElem Type
	if rightIsArray {
		rightElem = right.Type.(Array).ColTypes[0]
	}

	r := c.rangeZeroToN(loopLen)
	c.createLoop(r, func(iter llvm.Value) {
		leftVal := c.ArrayGetBorrowed(left, leftElem, iter)
		leftSym := &Symbol{Val: leftVal, Type: leftElem}

		currentRight := right
		if rightIsArray {
			rightVal := c.ArrayGetBorrowed(right, rightElem, iter)
			currentRight = &Symbol{Val: rightVal, Type: rightElem}
		}

		body(iter, leftSym, currentRight)
	})
}

func (c *Compiler) compileArrayArrayInfix(op string, left *Symbol, right *Symbol, resElem Type) *Symbol {
	leftArrType := left.Type.(Array)
	rightArrType := right.Type.(Array)

	leftElem := leftArrType.ColTypes[0]
	rightElem := rightArrType.ColTypes[0]

	// Array concatenation: arr1 ⊕ arr2
	if op == token.SYM_CONCAT {
		return c.compileArrayConcat(left, right, leftElem, rightElem, resElem)
	}

	// Element-wise array operation: arr1 op arr2
	// Strategy: iterate to min(len(arr1), len(arr2)) - no implicit padding.
	// This mirrors vector-style zip semantics and avoids silently inventing data.
	loopLen := c.arrayPairMinLen(left, right)
	resVec := c.CreateArrayForType(resElem, loopLen)
	c.forEachArrayPair(left, right, loopLen, func(iter llvm.Value, leftSym *Symbol, rightSym *Symbol) {
		// compileInfix reads values and produces a new result
		computed := c.compileInfix(op, leftSym, rightSym, resElem)

		// Convert to result element type if needed
		resultVal := computed.Val
		if computed.Type.Kind() == IntKind && resElem.Kind() == FloatKind {
			resultVal = c.builder.CreateSIToFP(computed.Val, c.Context.DoubleType(), "cast_to_resElem")
		}

		// Use set_own for strings to transfer ownership without double-strdup
		c.ArraySetOwnForType(resElem, resVec, iter, resultVal)
	})

	// Return result array
	i8p := llvm.PointerType(c.Context.Int8Type(), 0)
	resSym := &Symbol{Type: Array{Headers: nil, ColTypes: []Type{resElem}, Length: 0}}
	resSym.Val = c.builder.CreateBitCast(resVec, i8p, "arr_i8p")
	return resSym
}

func (c *Compiler) compileArrayConcat(left *Symbol, right *Symbol, leftElem Type, rightElem Type, resElem Type) *Symbol {
	// Array concatenation: arr1 ⊕ arr2
	// Result is [arr1..., arr2...]

	// Get lengths of both arrays
	leftLen := c.ArrayLen(left, leftElem)
	rightLen := c.ArrayLen(right, rightElem)

	// Calculate total length
	totalLen := c.builder.CreateAdd(leftLen, rightLen, "concat_len")

	// Create new array with total length
	resVec := c.CreateArrayForType(resElem, totalLen)

	// Copy left array elements
	c.CopyArrayInto(resVec, left, leftElem, resElem, llvm.Value{}, false)

	// Copy right array elements with offset
	c.CopyArrayInto(resVec, right, rightElem, resElem, leftLen, true)

	// Return concatenated array
	i8p := llvm.PointerType(c.Context.Int8Type(), 0)
	resSym := &Symbol{Type: Array{Headers: nil, ColTypes: []Type{resElem}, Length: 0}}
	resSym.Val = c.builder.CreateBitCast(resVec, i8p, "arr_i8p")
	return resSym
}

func (c *Compiler) compileArrayScalarInfix(op string, arr *Symbol, scalar *Symbol, resElem Type, arrayOnLeft bool) *Symbol {
	arrElem := arr.Type.(Array).ColTypes[0]
	loopLen := c.ArrayLen(arr, arrElem)
	resVec := c.CreateArrayForType(resElem, loopLen)
	c.forEachArrayPair(arr, scalar, loopLen, func(iter llvm.Value, elemSym *Symbol, scalarSym *Symbol) {
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

		// Use set_own for strings to transfer ownership without double-strdup
		c.ArraySetOwnForType(resElem, resVec, iter, resultVal)
	})

	i8p := llvm.PointerType(c.Context.Int8Type(), 0)
	resSym := &Symbol{Type: Array{Headers: nil, ColTypes: []Type{resElem}, Length: 0}}
	resSym.Val = c.builder.CreateBitCast(resVec, i8p, "arr_i8p")
	return resSym
}

// compileArrayFilter dispatches value-position comparison filtering when at least
// one operand is an array. It keeps LHS values where the comparison holds.
// For scalar-op-array, the scalar LHS is repeated for each true element.
func (c *Compiler) compileArrayFilter(op string, left *Symbol, right *Symbol, expected Type) *Symbol {
	l := c.derefIfPointer(left, "")
	r := c.derefIfPointer(right, "")

	resArr := expected.(Array)
	acc := c.NewArrayAccumulator(resArr)

	if l.Type.Kind() == ArrayKind && r.Type.Kind() == ArrayKind {
		return c.compileArrayArrayFilter(op, l, r, acc)
	}
	if l.Type.Kind() == ArrayKind {
		return c.compileArrayScalarFilter(op, l, r, acc, true)
	}
	if r.Type.Kind() == ArrayKind {
		return c.compileArrayScalarFilter(op, r, l, acc, false)
	}
	panic(fmt.Sprintf("compileArrayFilter expects at least one array operand, got %s and %s", l.Type, r.Type))
}

// filterPush conditionally appends sym to acc based on cond.
// Emits a branch: if cond is true, push sym; otherwise skip.
func (c *Compiler) filterPush(acc *ArrayAccumulator, sym *Symbol, cond llvm.Value) {
	fn := c.builder.GetInsertBlock().Parent()
	copyBlock := c.Context.AddBasicBlock(fn, "filter_copy")
	nextBlock := c.Context.AddBasicBlock(fn, "filter_next")
	c.builder.CreateCondBr(cond, copyBlock, nextBlock)

	c.builder.SetInsertPointAtEnd(copyBlock)
	c.PushVal(acc, sym)
	c.builder.CreateBr(nextBlock)

	c.builder.SetInsertPointAtEnd(nextBlock)
}

func (c *Compiler) compileArrayArrayFilter(op string, left *Symbol, right *Symbol, acc *ArrayAccumulator) *Symbol {
	c.forEachArrayPair(left, right, c.arrayPairMinLen(left, right), func(_ llvm.Value, leftSym *Symbol, rightSym *Symbol) {
		cmpResult := defaultOps[opKey{
			Operator:  op,
			LeftType:  opType(leftSym.Type.Key()),
			RightType: opType(rightSym.Type.Key()),
		}](c, leftSym, rightSym, true)

		c.filterPush(acc, leftSym, cmpResult.Val)
	})

	return c.ArrayAccResult(acc)
}

func (c *Compiler) compileArrayScalarFilter(op string, arr *Symbol, scalar *Symbol, acc *ArrayAccumulator, arrayOnLeft bool) *Symbol {
	arrElem := arr.Type.(Array).ColTypes[0]
	c.forEachArrayPair(arr, scalar, c.ArrayLen(arr, arrElem), func(_ llvm.Value, elemSym *Symbol, scalarSym *Symbol) {
		leftSym := elemSym
		rightSym := scalarSym
		pushSym := elemSym
		if !arrayOnLeft {
			// Preserve original operand order for non-commutative comparisons,
			// and keep scalar LHS values on true comparisons.
			leftSym = scalarSym
			rightSym = elemSym
			pushSym = scalarSym
		}
		cmpResult := defaultOps[opKey{
			Operator:  op,
			LeftType:  opType(leftSym.Type.Key()),
			RightType: opType(rightSym.Type.Key()),
		}](c, leftSym, rightSym, true)

		c.filterPush(acc, pushSym, cmpResult.Val)
	})

	return c.ArrayAccResult(acc)
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
		// Use borrowed get - compilePrefix reads value and produces new result
		v := c.ArrayGetBorrowed(arr, elem, idx)
		opSym := &Symbol{Val: v, Type: elem}
		computed := c.compilePrefix(op, opSym, resElem)

		// Convert to result element type if needed
		resultVal := computed.Val
		if computed.Type.Kind() == IntKind && resElem.Kind() == FloatKind {
			resultVal = c.builder.CreateSIToFP(computed.Val, c.Context.DoubleType(), "cast_to_resElem")
		}

		// Use set_own for strings to transfer ownership without double-strdup
		c.ArraySetOwnForType(resElem, resVec, idx, resultVal)
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
	arrayLoadName := ""
	if arrayIdent, ok := expr.Array.(*ast.Identifier); ok {
		arrayLoadName = arrayIdent.Value + "_load"
	}
	idxLoadName := ""
	if idxIdent, ok := expr.Range.(*ast.Identifier); ok {
		idxLoadName = idxIdent.Value + "_load"
	}

	arraySym := c.derefIfPointer(c.compileExpression(expr.Array, nil)[0], arrayLoadName)
	idxSym := c.derefIfPointer(c.compileExpression(expr.Range, nil)[0], idxLoadName)
	arrType := arraySym.Type.(Array)

	// Check actual compiled index type to determine ArrayRange vs element access
	if idxSym.Type.Kind() == RangeKind {
		// ArrayRange from an identifier is a borrowed view into existing storage.
		_, arrayIsIdent := expr.Array.(*ast.Identifier)
		borrowed := arrayIsIdent || arraySym.Borrowed

		arrRange := ArrayRange{
			Array: arrType,
			Range: idxSym.Type.(Range),
		}
		return []*Symbol{{
			Val:      c.CreateArrayRange(arraySym.Val, idxSym.Val, arrRange),
			Type:     arrRange,
			Borrowed: borrowed,
		}}
	}

	// Scalar index - element access
	elemType := arrType.ColTypes[0]
	idxVal := idxSym.Val
	if intType, ok := idxSym.Type.(Int); ok && intType.Width != 64 {
		idxVal = c.builder.CreateIntCast(idxVal, c.Context.Int64Type(), "arr_idx_cast")
	}

	elemVal := c.ArrayGet(arraySym, elemType, idxVal)

	// Scalar element access does not retain the source array pointer.
	// Release temporary array sources immediately.
	c.freeTemporary(expr.Array, []*Symbol{arraySym})

	// For string arrays, arr_str_get returns an owned copy that must be freed.
	// String copies are heap-allocated regardless of original type.
	if elemType.Kind() == StrKind {
		elemType = StrH{}
	}

	return []*Symbol{{Type: elemType, Val: elemVal}}
}
