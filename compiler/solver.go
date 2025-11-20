package compiler

import (
	"fmt"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

type IterOver int

const (
	IterRange IterOver = iota
	IterArrayRange
	IterArray
)

type RangeInfo struct {
	Name      string
	RangeLit  *ast.RangeLiteral
	ArrayExpr ast.Expression
	ArrayType Array
	Over      IterOver
}

type ExprInfo struct {
	Ranges   []*RangeInfo   // either value from *ast.Identifier or a newly created value from tmp identifier for *ast.RangeLiteral
	Rewrite  ast.Expression // expression rewritten with a literal -> tmp value. (0:11) -> tmpIter0 etc.
	ExprLen  int
	OutTypes []Type
}

// TypeSolver infers the types of various expressions
// It is mainly needed to infer the output types of a function
// given input arguments
type pendingExpr struct {
	expr       ast.Expression
	outTypeIdx int
}

type TypeSolver struct {
	ScriptCompiler  *ScriptCompiler
	Scopes          []Scope[Type]
	InProgress      map[string]struct{} // if we are currently in progress of inferring types for func. This is for recursion/mutual recursion
	ScriptFunc      string              // this is the current func in the main scope we are inferring type for
	Converging      bool
	Errors          []*token.CompileError
	ExprCache       map[ast.Expression]*ExprInfo
	TmpCounter      int // tmpCounter for uniquely naming temporary variables
	UnresolvedExprs map[string][]pendingExpr
}

func NewTypeSolver(sc *ScriptCompiler) *TypeSolver {
	return &TypeSolver{
		ScriptCompiler:  sc,
		Scopes:          []Scope[Type]{NewScope[Type](FuncScope)},
		InProgress:      map[string]struct{}{},
		ScriptFunc:      "",
		Converging:      false,
		Errors:          []*token.CompileError{},
		ExprCache:       sc.Compiler.ExprCache,
		TmpCounter:      0,
		UnresolvedExprs: make(map[string][]pendingExpr),
	}
}

func cloneArrayHeaders(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	return append([]string(nil), src...)
}

func concatWithMetadata(leftArr, rightArr Array, elem Type) Array {
	headers := cloneArrayHeaders(leftArr.Headers)
	if len(headers) == 0 {
		headers = cloneArrayHeaders(rightArr.Headers)
	}

	length := 0
	switch {
	case leftArr.Length > 0 && rightArr.Length > 0:
		length = leftArr.Length + rightArr.Length
	case leftArr.Length > 0:
		length = leftArr.Length
	case rightArr.Length > 0:
		length = rightArr.Length
	}

	return Array{
		Headers:  headers,
		ColTypes: []Type{elem},
		Length:   length,
	}
}

func (ts *TypeSolver) concatArrayTypes(leftArr, rightArr Array, tok token.Token) Type {
	leftElemType := leftArr.ColTypes[0]
	rightElemType := rightArr.ColTypes[0]

	if leftElemType.Kind() == UnresolvedKind {
		return concatWithMetadata(leftArr, rightArr, rightElemType)
	}

	if rightElemType.Kind() == UnresolvedKind {
		return concatWithMetadata(leftArr, rightArr, leftElemType)
	}

	if leftElemType.Kind() == rightElemType.Kind() {
		return concatWithMetadata(leftArr, rightArr, leftElemType)
	}

	if (leftElemType.Kind() == IntKind && rightElemType.Kind() == FloatKind) ||
		(leftElemType.Kind() == FloatKind && rightElemType.Kind() == IntKind) {
		return concatWithMetadata(leftArr, rightArr, Float{Width: 64})
	}

	ce := &token.CompileError{
		Token: tok,
		Msg:   fmt.Sprintf("cannot concatenate arrays with incompatible element types: %s and %s", leftElemType.String(), rightElemType.String()),
	}
	ts.Errors = append(ts.Errors, ce)
	return Unresolved{}
}

func (ts *TypeSolver) bindArrayOperand(expr ast.Expression, arr Array) {
	// Update the ExprCache with refined array metadata (headers, length).
	// Note: handleInfixArrays only calls this when expr is already array-typed,
	// so we can unconditionally update.
	info := ts.ExprCache[expr]
	info.OutTypes[0] = arr

	if ident, ok := expr.(*ast.Identifier); ok {
		// resolve any previously unresolved uses now that we know the concrete array type
		ts.resolveTrackedExprs(ident.Value, arr)
		return
	}

	switch e := expr.(type) {
	case *ast.InfixExpression:
		// Only recurse if the subexpressions are themselves array-typed
		if ts.ExprCache[e.Left].OutTypes[0].Kind() == ArrayKind {
			ts.bindArrayOperand(e.Left, arr)
		}
		if ts.ExprCache[e.Right].OutTypes[0].Kind() == ArrayKind {
			ts.bindArrayOperand(e.Right, arr)
		}
	case *ast.PrefixExpression:
		// Only recurse if the operand is array-typed
		if ts.ExprCache[e.Right].OutTypes[0].Kind() == ArrayKind {
			ts.bindArrayOperand(e.Right, arr)
		}
	case *ast.ArrayRangeExpression:
		// For array slicing like arr[1:3], the array itself must be array-typed
		ts.bindArrayOperand(e.Array, arr)
	}
}

func (ts *TypeSolver) handleInfixArrays(expr *ast.InfixExpression, leftType, rightType Type) (Type, bool) {
	if leftType.Kind() != ArrayKind && rightType.Kind() != ArrayKind {
		return nil, false
	}
	finalType := ts.TypeArrayInfix(leftType, rightType, expr.Operator, expr.Token)
	if arr, ok := finalType.(Array); ok {
		if leftType.Kind() == ArrayKind {
			ts.bindArrayOperand(expr.Left, arr)
		}
		if rightType.Kind() == ArrayKind {
			ts.bindArrayOperand(expr.Right, arr)
		}
	}
	return finalType, true
}

func (ts *TypeSolver) bindArrayAssignment(name string, expr ast.Expression, idx int, t Type) {
	arrType, ok := t.(Array)
	if !ok {
		return
	}

	if len(arrType.ColTypes) > 0 && arrType.ColTypes[0].Kind() != UnresolvedKind {
		info := ts.ExprCache[expr]
		info.OutTypes[idx] = arrType
		ts.resolveTrackedExprs(name, arrType)
		return
	}

	ts.UnresolvedExprs[name] = append(ts.UnresolvedExprs[name], pendingExpr{expr: expr, outTypeIdx: idx})
}

// appendUses appends one occurrence s to out, respecting the policy:
// - Identifiers (named ranges): dedupe by name (first one wins)
// - Range literals: keep every occurrence
// 'seen' tracks which identifier names we've already added.
func appendUses(out []*RangeInfo, ri *RangeInfo, seen map[string]struct{}) []*RangeInfo {
	if _, ok := seen[ri.Name]; ok {
		return out
	}
	seen[ri.Name] = struct{}{}
	return append(out, ri)
}

// mergeUses merges two ordered occurrence lists, preserving order,
// deduping identifiers by name, and keeping all literal occurrences.
func mergeUses(a, b []*RangeInfo) []*RangeInfo {
	out := []*RangeInfo{}
	seen := make(map[string]struct{}) // identifier names we've already added

	for _, e := range a {
		out = appendUses(out, e, seen)
	}
	for _, e := range b {
		out = appendUses(out, e, seen)
	}
	return out
}

func (ts *TypeSolver) FreshIterName() string {
	n := ts.TmpCounter
	ts.TmpCounter++
	return fmt.Sprintf("tmpIter$%d", n)
}

func (ts *TypeSolver) resolveTrackedExprs(name string, t Type) {
	entries, ok := ts.UnresolvedExprs[name]
	if ok {
		for _, pending := range entries {
			ts.ExprCache[pending.expr].OutTypes[pending.outTypeIdx] = t
		}
		delete(ts.UnresolvedExprs, name)
	}

	if typ, ok := Get(ts.Scopes, name); ok {
		if CanRefineType(typ, t) {
			SetExisting(ts.Scopes, name, t)
		}
	}
}

// HandleRanges processes expressions to identify and rewrite range literals for loop generation.
// It traverses the AST, replacing range literals with temporary identifiers and collecting
// range information for later compilation into loops.
func (ts *TypeSolver) HandleRanges(e ast.Expression, isRoot bool) (ranges []*RangeInfo, rew ast.Expression) {
	rew = e
	switch t := e.(type) {
	case *ast.RangeLiteral:
		return ts.HandleRangeLiteral(t, isRoot)
	case *ast.ArrayLiteral:
		return ts.HandleArrayLiteralRanges(t, isRoot)
	case *ast.ArrayRangeExpression:
		return ts.HandleArrayRangeExpression(t, isRoot)
	case *ast.InfixExpression:
		return ts.HandleInfixRanges(t, isRoot)
	case *ast.PrefixExpression:
		return ts.HandlePrefixRanges(t, isRoot)
	case *ast.CallExpression:
		return ts.HandleCallRanges(t, isRoot)
	case *ast.Identifier:
		return ts.HandleIdentifierRanges(t, isRoot)
	default:
		return
	}
}

// HandleRangeLiteral processes range literal expressions, converting them to temporary
// identifiers for use in loop generation. Root-level ranges are preserved as-is.
func (ts *TypeSolver) HandleRangeLiteral(rangeLit *ast.RangeLiteral, isRoot bool) (ranges []*RangeInfo, rew ast.Expression) {
	if isRoot {
		return nil, rangeLit
	}

	nm := ts.FreshIterName()
	ri := &RangeInfo{
		Name:     nm,
		RangeLit: rangeLit,
		Over:     IterRange,
	}
	ranges = []*RangeInfo{ri}
	rew = &ast.Identifier{Value: nm, Token: rangeLit.Tok()}
	return
}

func cloneArrayIndices(indices map[string][]int) map[string][]int {
	if len(indices) == 0 {
		return nil
	}
	out := make(map[string][]int, len(indices))
	for name, idxs := range indices {
		copyIdxs := make([]int, len(idxs))
		copy(copyIdxs, idxs)
		out[name] = copyIdxs
	}
	return out
}

func (ts *TypeSolver) HandleArrayLiteralRanges(al *ast.ArrayLiteral, isRoot bool) (ranges []*RangeInfo, rew ast.Expression) {
	info := ts.ExprCache[al]

	// If no ExprCache entry exists (e.g., empty array with unresolved type), nothing to handle
	if info == nil {
		return nil, al
	}

	// Only 1D array literals are currently supported by the compiler.
	if !(len(al.Headers) == 0 && len(al.Rows) == 1) {
		info.Ranges = nil
		info.Rewrite = al
		return nil, al
	}

	row := al.Rows[0]
	changed := false
	newRow := make([]ast.Expression, len(row))
	for i, cell := range row {
		cellRanges, cellRew := ts.HandleRanges(cell, false)
		ranges = mergeUses(ranges, cellRanges)
		newRow[i] = cellRew
		if cellRew != cell {
			changed = true
		}
	}

	rew = al
	if changed {
		newLit := &ast.ArrayLiteral{
			Token:   al.Token,
			Headers: append([]string(nil), al.Headers...),
			Rows:    [][]ast.Expression{newRow},
			Indices: cloneArrayIndices(al.Indices),
		}
		infoCopy := &ExprInfo{
			OutTypes: info.OutTypes,
			ExprLen:  info.ExprLen,
			Ranges:   append([]*RangeInfo(nil), ranges...),
			Rewrite:  newLit,
		}
		ts.ExprCache[newLit] = infoCopy
		rew = newLit
	}

	info.Ranges = ranges
	info.Rewrite = rew

	return ranges, rew
}

func (ts *TypeSolver) HandleArrayRangeExpression(ar *ast.ArrayRangeExpression, isRoot bool) (ranges []*RangeInfo, rew ast.Expression) {
	info := ts.ExprCache[ar]

	arrRanges, arrRew := ts.HandleRanges(ar.Array, false)
	idxRanges, rangeRew := ts.HandleRanges(ar.Range, false)

	ranges = mergeUses(arrRanges, idxRanges)

	rew = ar
	if arrRew != ar.Array || rangeRew != ar.Range {
		newExpr := &ast.ArrayRangeExpression{Token: ar.Token, Array: arrRew, Range: rangeRew}
		ts.ExprCache[newExpr] = &ExprInfo{
			OutTypes: info.OutTypes,
			ExprLen:  info.ExprLen,
			Ranges:   append([]*RangeInfo(nil), ranges...),
		}
		rew = newExpr
	}

	info.Ranges = ranges
	info.Rewrite = rew

	return ranges, rew
}

// HandleInfixRanges processes infix expressions, recursively handling both operands
// and tracking whether each side contains ranges for optimization decisions.
func (ts *TypeSolver) HandleInfixRanges(infix *ast.InfixExpression, isRoot bool) (ranges []*RangeInfo, rew ast.Expression) {
	lRanges, l := ts.HandleRanges(infix.Left, false)
	rRanges, r := ts.HandleRanges(infix.Right, false)
	ranges = mergeUses(lRanges, rRanges)

	if l == infix.Left && r == infix.Right {
		rew = infix
	} else {
		cp := *infix
		cp.Left, cp.Right = l, r
		rew = &cp
		// Create a simple ExprCache entry for the rewritten expression
		// It should have no ranges since temporary iterators are scalars
		originalInfo := ts.ExprCache[infix]
		ts.ExprCache[rew.(*ast.InfixExpression)] = &ExprInfo{
			OutTypes: originalInfo.OutTypes, // Same output types as original
			ExprLen:  originalInfo.ExprLen,
			Ranges:   nil, // No ranges for rewritten expressions
		}
	}

	if isRoot {
		info := ts.ExprCache[infix]
		info.Ranges = ranges
		info.Rewrite = rew
	}
	return
}

// HandlePrefixRanges processes prefix expressions, handling the operand and preserving
// the prefix operator while rewriting any contained ranges.
func (ts *TypeSolver) HandlePrefixRanges(prefix *ast.PrefixExpression, isRoot bool) (ranges []*RangeInfo, rew ast.Expression) {
	ranges, r := ts.HandleRanges(prefix.Right, false)

	if r == prefix.Right {
		rew = prefix
	} else {
		cp := *prefix
		cp.Right = r
		rew = &cp
		// Create a simple ExprCache entry for the rewritten expression
		// It should have no ranges since temporary iterators are scalars
		originalInfo := ts.ExprCache[prefix]
		ts.ExprCache[rew.(*ast.PrefixExpression)] = &ExprInfo{
			OutTypes: originalInfo.OutTypes, // Same output types as original
			ExprLen:  originalInfo.ExprLen,
			Ranges:   nil, // No ranges for rewritten expressions
		}
	}

	if isRoot {
		info := ts.ExprCache[prefix]
		info.Ranges = ranges
		info.Rewrite = rew
	}
	return
}

// HandleCallRanges processes function call expressions, handling all arguments
// and merging their range information for proper loop generation.
func (ts *TypeSolver) HandleCallRanges(call *ast.CallExpression, isRoot bool) (ranges []*RangeInfo, rew ast.Expression) {
	changed := false
	args := make([]ast.Expression, len(call.Arguments))

	for i, a := range call.Arguments {
		aRanges, a2 := ts.HandleRanges(a, isRoot)
		args[i] = a2
		changed = changed || (a2 != a)
		ranges = mergeUses(ranges, aRanges)
	}

	if !changed {
		rew = call
	} else {
		cp := *call
		cp.Arguments = args
		rew = &cp
	}
	return
}

// HandleIdentifierRanges processes identifier expressions, detecting if they refer
// to range-typed variables and including them in range tracking.
func (ts *TypeSolver) HandleIdentifierRanges(ident *ast.Identifier, isRoot bool) (ranges []*RangeInfo, rew ast.Expression) {
	if !isRoot {
		typ, ok := ts.GetIdentifier(ident.Value)
		if ok && typ.Kind() == RangeKind {
			ri := &RangeInfo{
				Name:     ident.Value,
				RangeLit: nil,
				Over:     IterRange,
			}
			ranges = []*RangeInfo{ri}
		}
	}
	rew = ident
	return
}

func (ts *TypeSolver) TypeStatement(stmt ast.Statement) {
	switch s := stmt.(type) {
	case *ast.LetStatement:
		ts.TypeLetStatement(s)
	case *ast.PrintStatement:
		ts.TypePrintStatement(s)
	default:
		panic(fmt.Sprintf("Cannot infer types for statement type %T", s))
	}
}

func (ts *TypeSolver) Solve() {
	program := ts.ScriptCompiler.Program
	oldErrs := len(ts.Errors)
	for _, stmt := range program.Statements {
		ts.TypeStatement(stmt)
		if len(ts.Errors) > oldErrs {
			return
		}
	}

	for name, entries := range ts.UnresolvedExprs {
		for _, pending := range entries {
			ts.Errors = append(ts.Errors, &token.CompileError{
				Token: pending.expr.Tok(),
				Msg:   fmt.Sprintf("array %q element type could not be resolved", name),
			})
		}
	}

}

func (ts *TypeSolver) TypePrintStatement(stmt *ast.PrintStatement) {
	for _, expr := range stmt.Expression {
		ts.TypeExpression(expr, true)
	}
}

func (ts *TypeSolver) TypeLetStatement(stmt *ast.LetStatement) {
	// type conditions in case there may be functions we have to type
	for _, expr := range stmt.Condition {
		ts.TypeExpression(expr, false)
	}

	types := []Type{}
	exprRefs := make([]ast.Expression, 0, len(stmt.Name))
	exprIdxs := make([]int, 0, len(stmt.Name))
	for _, expr := range stmt.Value {
		exprTypes := ts.TypeExpression(expr, true)
		for idx := range exprTypes {
			types = append(types, exprTypes[idx])
			exprRefs = append(exprRefs, expr)
			exprIdxs = append(exprIdxs, idx)
		}
	}

	if len(stmt.Name) != len(types) {
		ce := &token.CompileError{
			Token: stmt.Token,
			Msg:   fmt.Sprintf("Statement lhs identifiers are not equal to rhs values!!! lhs identifiers: %d. rhs values: %d. Stmt %q", len(stmt.Name), len(types), stmt),
		}
		ts.Errors = append(ts.Errors, ce)
		return
	}

	trueValues := make(map[string]Type)
	for i, ident := range stmt.Name {
		newType := types[i]
		ts.bindArrayAssignment(ident.Value, exprRefs[i], exprIdxs[i], newType)
		var typ Type
		var ok bool
		if typ, ok = Get(ts.Scopes, ident.Value); ok {
			// if new type is Unresolved then don't update new type or do a type check
			if newType.Kind() == UnresolvedKind {
				continue
			}
			// allow for type refinement (e.g., Unresolved -> Concrete, [?] -> [I64])
			if CanRefineType(typ, newType) {
				trueValues[ident.Value] = newType
				continue
			}
			// types are incompatible
			ce := &token.CompileError{
				Token: ident.Token,
				Msg:   fmt.Sprintf("cannot reassign type to identifier. Old Type: %s. New Type: %s. Identifier %q", typ, newType, ident.Token.Literal),
			}
			ts.Errors = append(ts.Errors, ce)
			return
		}

		trueValues[ident.Value] = newType
	}

	PutBulk(ts.Scopes, trueValues)
}

// TypeArrayExpression infers the type of an Array literal. Columns must be
// homogeneous and of primitive types: I64, F64 (promotion from I64 allowed), or Str.
// The returned type slice contains a single Array type value.
// Or if it is not a root expression, it returns the underlying element type
func (ts *TypeSolver) TypeArrayExpression(al *ast.ArrayLiteral, isRoot bool) []Type {
	// Empty array literal: [] (no rows)
	if len(al.Headers) == 0 && len(al.Rows) == 0 {
		arr := Array{Headers: nil, ColTypes: []Type{Unresolved{}}, Length: 0}
		ts.ExprCache[al] = &ExprInfo{OutTypes: []Type{arr}, ExprLen: 1}
		return []Type{arr}
	}

	// Vector special-case: single row, no headers → treat as 1-column vector
	if len(al.Headers) == 0 && len(al.Rows) == 1 {
		colTypes := []Type{Unresolved{}}
		row := al.Rows[0]
		for col := 0; col < len(row); col++ {
			cellT, ok := ts.typeCell(row[col], al.Tok())
			if !ok {
				continue
			}
			colTypes[0] = ts.mergeColType(colTypes[0], cellT, 0, al.Tok())
		}
		// Length = number of elements in the row (for vectors)
		arr := Array{Headers: nil, ColTypes: colTypes, Length: len(row)}

		// Cache this expression's resolved type for the compiler
		ts.ExprCache[al] = &ExprInfo{OutTypes: []Type{arr}, ExprLen: 1}
		return []Type{arr}
	}
	// unsupported as of now
	return []Type{Unresolved{}}
	/*
	   // 1. Determine number of columns
	   numCols := ts.arrayNumCols(al)

	   	if numCols == 0 {
	   		if !isRoot {
	   			return []Type{Unresolved{}}
	   		}
	   		return []Type{Array{Headers: nil, ColTypes: nil, Length: 0}}
	   	}

	   // 2. Initialize column types
	   colTypes := ts.initColTypes(numCols)

	   // 3. Walk rows/cells and merge types per column

	   	for _, row := range al.Rows {
	   		for col := 0; col < numCols; col++ {
	   			if col >= len(row) {
	   				break // ragged row; remaining cells are missing
	   			}
	   			cellT, ok := ts.typeCell(row[col], al.Tok())
	   			if !ok {
	   				continue
	   			}
	   			colTypes[col] = ts.mergeColType(colTypes[col], cellT, col, al.Tok())
	   		}
	   	}

	   // 4. Build final Array type (no defaulting of unresolved columns)
	   arr := Array{Headers: append([]string(nil), al.Headers...), ColTypes: colTypes, Length: len(al.Rows)}

	   // Cache this expression's resolved type for the compiler
	   ts.ExprCache[al] = &ExprInfo{OutTypes: []Type{arr}, ExprLen: 1}
	   return []Type{arr}
	*/
}

// arrayNumCols returns column count based on headers or max row width.
func (ts *TypeSolver) arrayNumCols(al *ast.ArrayLiteral) int {
	if len(al.Headers) > 0 {
		return len(al.Headers)
	}
	maxW := 0
	for _, row := range al.Rows {
		if l := len(row); l > maxW {
			maxW = l
		}
	}
	return maxW
}

func (ts *TypeSolver) initColTypes(n int) []Type {
	out := make([]Type, n)
	for i := 0; i < n; i++ {
		out[i] = Unresolved{}
	}
	return out
}

// typeCell infers the type of a single cell expression. Returns (type, ok).
func (ts *TypeSolver) typeCell(expr ast.Expression, tok token.Token) (Type, bool) {
	tps := ts.TypeExpression(expr, false)
	if len(tps) != 1 {
		ts.Errors = append(ts.Errors, &token.CompileError{
			Token: tok,
			Msg:   fmt.Sprintf("array cell produced %d types; expected 1", len(tps)),
		})
		return Unresolved{}, false
	}
	if tps[0].Kind() == UnresolvedKind {
		ts.Errors = append(ts.Errors, &token.CompileError{Token: tok, Msg: "array cell type could not be resolved"})
		return Unresolved{}, false
	}
	return tps[0], true
}

// mergeColType merges a new cell type into the running column type, enforcing
// allowed schemas and numeric promotion (I64 -> F64). Reports errors against tok.
func (ts *TypeSolver) mergeColType(cur Type, newT Type, colIdx int, tok token.Token) Type {
	// If the cell type is unresolved, skip it.
	if newT.Kind() == UnresolvedKind {
		return cur
	}

	// Only allow I64, F64, or Str as array elements.
	if !AllowedArrayElem(newT) {
		ts.Errors = append(ts.Errors, &token.CompileError{Token: tok, Msg: fmt.Sprintf("unsupported array element type %s in column %d", newT.String(), colIdx)})
		return cur
	}

	// If column schema not yet set, adopt the new type.
	if cur.Kind() == UnresolvedKind {
		return newT
	}

	// Exact match: stable.
	if TypeEqual(cur, newT) {
		return cur
	}

	// Only numeric promotion to F64 is allowed; any Str/numeric mix is invalid.
	if cur.Kind() == IntKind && newT.Kind() == FloatKind {
		return newT
	}
	if cur.Kind() == FloatKind && newT.Kind() == IntKind {
		return cur
	}

	// Otherwise, incompatible column schema.
	ts.Errors = append(ts.Errors, &token.CompileError{Token: tok, Msg: fmt.Sprintf("cannot mix %s and %s in column %d", cur.String(), newT.String(), colIdx)})
	return cur
}

// AllowedArrayElem returns true only for I64, F64, and Str cells.
func AllowedArrayElem(t Type) bool {
	switch v := t.(type) {
	case Int:
		return v.Width == 64
	case Float:
		return v.Width == 64
	case Str:
		return true
	default:
		return false
	}
}

func (ts *TypeSolver) TypeRangeExpression(r *ast.RangeLiteral, isRoot bool) []Type {
	// infer start and stop
	startT := ts.TypeExpression(r.Start, false)
	stopT := ts.TypeExpression(r.Stop, false)
	if len(startT) != 1 || len(stopT) != 1 {
		ce := &token.CompileError{
			Token: r.Tok(),
			Msg:   fmt.Sprintf("start and stop in range should be of length 1. start length: %d, stop length: %d", len(startT), len(stopT)),
		}
		ts.Errors = append(ts.Errors, ce)
	}
	// must be integers
	if startT[0].Kind() != IntKind || stopT[0].Kind() != IntKind {
		ce := &token.CompileError{
			Token: r.Tok(),
			Msg:   fmt.Sprintf("range bounds should be Integer. start type: %s, stop type: %s", startT[0], stopT[0]),
		}
		ts.Errors = append(ts.Errors, ce)
	}
	// must match
	if !EqualTypes(startT, stopT) {
		ce := &token.CompileError{
			Token: r.Tok(),
			Msg:   fmt.Sprintf("range start and stop must have same type. start Type: %s, stop Type: %s", startT[0], stopT[0]),
		}
		ts.Errors = append(ts.Errors, ce)
	}
	// optional step
	if r.Step != nil {
		stepT := ts.TypeExpression(r.Step, false)
		if len(stepT) != 1 {
			ce := &token.CompileError{
				Token: r.Tok(),
				Msg:   fmt.Sprintf("range step got from expression should have length 1. step length: %d", len(stepT)),
			}
			ts.Errors = append(ts.Errors, ce)
		}
		if stepT[0].Kind() != IntKind {
			ce := &token.CompileError{
				Token: r.Tok(),
				Msg:   fmt.Sprintf("range bounds should be Integer. got step type: %s", stepT[0]),
			}
			ts.Errors = append(ts.Errors, ce)
		}
		if !EqualTypes(startT, stepT) {
			ce := &token.CompileError{
				Token: r.Tok(),
				Msg:   fmt.Sprintf("range start and step must have same type. start Type: %s, step Type: %s", startT[0], stepT[0]),
			}
			ts.Errors = append(ts.Errors, ce)
		}
	}
	if !isRoot {
		return []Type{Int{Width: 64}} // if not root, return integer type as any sub root expression automatically loops over the range
	}
	// the whole expression is a Range of that integer type
	return []Type{Range{Iter: startT[0]}}
}

func (ts *TypeSolver) TypeArrayRangeExpression(ax *ast.ArrayRangeExpression, isRoot bool) []Type {
	info := &ExprInfo{OutTypes: []Type{Unresolved{}}, ExprLen: 1}
	ts.ExprCache[ax] = info

	arrType, ok := ts.expectSingleArray(ax.Array, ax.Tok(), "array access")
	if !ok {
		return info.OutTypes
	}
	if len(arrType.ColTypes) == 0 {
		ts.Errors = append(ts.Errors, &token.CompileError{
			Token: ax.Tok(),
			Msg:   "array type has no element columns",
		})
		return info.OutTypes
	}

	elemType := arrType.ColTypes[0]

	idxTypes := ts.TypeExpression(ax.Range, true)
	if len(idxTypes) != 1 {
		ts.Errors = append(ts.Errors, &token.CompileError{
			Token: ax.Tok(),
			Msg:   fmt.Sprintf("array index expects a single value, got %d", len(idxTypes)),
		})
		return info.OutTypes
	}

	idxType := idxTypes[0]
	var rangeType Type
	if idxType.Kind() == RangeKind {
		rangeType = idxType.(Range).Iter
	}

	if rangeType != nil && rangeType.Kind() != IntKind {
		ts.Errors = append(ts.Errors, &token.CompileError{
			Token: ax.Tok(),
			Msg:   fmt.Sprintf("array index expects an integer or range, got %s", idxType),
		})
		return info.OutTypes
	}

	if !isRoot {
		// if not root, return the element type since any sub root expression automatically loops over the range
		info.OutTypes = []Type{elemType}
		info.ExprLen = 1
		return info.OutTypes
	}

	if idxType.Kind() == IntKind {
		// single index access returns the element type
		info.OutTypes = []Type{elemType}
		info.ExprLen = 1
		return info.OutTypes
	}

	if rangeType == nil {
		// index is unresolved, cannot determine output type
		ts.Errors = append(ts.Errors, &token.CompileError{
			Token: ax.Tok(),
			Msg:   fmt.Sprintf("array index expects an integer or range, got %s", idxType),
		})
		return info.OutTypes
	}

	info.OutTypes = []Type{ArrayRange{
		Array: arrType,
		Range: idxType.(Range),
	}}
	info.ExprLen = 1
	return info.OutTypes
}

func (ts *TypeSolver) TypeExpression(expr ast.Expression, isRoot bool) (types []Type) {
	types = []Type{}
	switch e := expr.(type) {
	case *ast.IntegerLiteral:
		types = append(types, Int{Width: 64})
		ts.ExprCache[e] = &ExprInfo{OutTypes: types, ExprLen: 1}
	case *ast.FloatLiteral:
		types = append(types, Float{Width: 64})
		ts.ExprCache[e] = &ExprInfo{OutTypes: types, ExprLen: 1}
	case *ast.StringLiteral:
		types = append(types, Str{})
		ts.ExprCache[e] = &ExprInfo{OutTypes: types, ExprLen: 1}
	case *ast.ArrayLiteral:
		types = append(types, ts.TypeArrayExpression(e, isRoot)...)
	case *ast.ArrayRangeExpression:
		types = append(types, ts.TypeArrayRangeExpression(e, isRoot)...)
	case *ast.RangeLiteral:
		types = append(types, ts.TypeRangeExpression(e, isRoot)...)
	case *ast.Identifier:
		t := ts.TypeIdentifier(e)
		if !isRoot && t.Kind() == RangeKind {
			types = append(types, t.(Range).Iter)
			return
		}
		types = append(types, t)
	case *ast.InfixExpression:
		types = ts.TypeInfixExpression(e)
	case *ast.PrefixExpression:
		types = ts.TypePrefixExpression(e)
	case *ast.CallExpression:
		types = ts.TypeCallExpression(e, isRoot)
	default:
		panic(fmt.Sprintf("unsupported expression type %T to infer type", e))
	}

	ts.HandleRanges(expr, isRoot)
	return
}

func (ts *TypeSolver) GetIdentifier(name string) (Type, bool) {
	if t, ok := Get(ts.Scopes, name); ok {
		if ptr, yes := t.(Ptr); yes {
			return ptr.Elem, true
		}
		return t, true
	}

	// get any constants from the code compiler
	compiler := ts.ScriptCompiler.Compiler
	cc := compiler.CodeCompiler.Compiler
	if s, ok := Get(cc.Scopes, name); ok {
		t := s.Type
		if ptr, yes := t.(Ptr); yes {
			return ptr.Elem, true
		}
		return t, true
	}

	return Unresolved{}, false
}

// Type Identifier returns type of identifier if it is not a pointer
// If it is a pointer then returns type of the element it points to
// This is because we automatically dereference if it's a pointer
func (ts *TypeSolver) TypeIdentifier(ident *ast.Identifier) (t Type) {
	var ok bool
	t, ok = ts.GetIdentifier(ident.Value)
	if !ok {
		cerr := &token.CompileError{
			Token: ident.Token,
			Msg:   fmt.Sprintf("undefined identifier: %s", ident.Value),
		}
		ts.Errors = append(ts.Errors, cerr)
		return
	}

	ts.ExprCache[ident] = &ExprInfo{OutTypes: []Type{t}, ExprLen: 1}
	return
}

// TypeInfixExpression returns output types of infix expression
// If either left or right operands are pointers, it will dereference them
// This is because pointers are automatically dereferenced
func (ts *TypeSolver) TypeInfixExpression(expr *ast.InfixExpression) (types []Type) {
	left := ts.TypeExpression(expr.Left, false)
	right := ts.TypeExpression(expr.Right, false)
	if len(left) != len(right) {
		ce := &token.CompileError{
			Token: expr.Token,
			Msg:   fmt.Sprintf("left expression and right expression have unequal lengths! Left expr: %s, length: %d. Right expr: %s, length: %d. Operator: %q", expr.Left, len(left), expr.Right, len(right), expr.Token.Literal),
		}
		ts.Errors = append(ts.Errors, ce)
		return []Type{Unresolved{}}
	}

	types = []Type{}
	var ok bool
	var ptr Ptr
	for i := range left {
		leftType := left[i]
		if ptr, ok = leftType.(Ptr); ok {
			leftType = ptr.Elem
		}

		rightType := right[i]
		if ptr, ok = rightType.(Ptr); ok {
			rightType = ptr.Elem
		}

		if leftType.Kind() == UnresolvedKind || rightType.Kind() == UnresolvedKind {
			types = append(types, Unresolved{})
			continue
		}

		// Handle any expression involving arrays
		if finalType, ok := ts.handleInfixArrays(expr, leftType, rightType); ok {
			types = append(types, finalType)
			continue
		}

		types = append(types, ts.TypeInfixOp(leftType, rightType, expr.Operator, expr.Token))
	}

	// Create new entry
	ts.ExprCache[expr] = &ExprInfo{
		OutTypes: types,
		ExprLen:  len(types),
	}

	return
}

func (ts *TypeSolver) TypeArrayInfix(left, right Type, op string, tok token.Token) Type {
	// Handle string concatenation early
	if op == token.SYM_CONCAT && left.Kind() == StrKind && right.Kind() == StrKind {
		return Str{}
	}

	leftIsArr := left.Kind() == ArrayKind
	rightIsArr := right.Kind() == ArrayKind

	// Route to specialized handlers based on operand types
	if leftIsArr && rightIsArr {
		return ts.typeArrayArrayInfix(left.(Array), right.(Array), op, tok)
	}

	if leftIsArr || rightIsArr {
		return ts.typeArrayScalarInfix(left, right, leftIsArr, op, tok)
	}

	// Both are scalars - use standard scalar type inference
	return ts.TypeInfixOp(left, right, op, tok)
}

// typeArrayArrayInfix handles operations where both operands are arrays
func (ts *TypeSolver) typeArrayArrayInfix(leftArr, rightArr Array, op string, tok token.Token) Type {
	if op == token.SYM_CONCAT {
		return ts.concatArrayTypes(leftArr, rightArr, tok)
	}

	// Element-wise operations - resolve element types
	leftElem := leftArr.ColTypes[0]
	rightElem := rightArr.ColTypes[0]

	resultElem := ts.resolveArrayElemTypes(leftElem, rightElem, op, tok)

	// Return array type with dynamic length (determined at runtime)
	return Array{
		Headers:  nil,
		ColTypes: []Type{resultElem},
		Length:   0,
	}
}

// resolveArrayElemTypes handles unresolved element types and type inference for array operations
func (ts *TypeSolver) resolveArrayElemTypes(leftElem, rightElem Type, op string, tok token.Token) Type {
	leftUnresolved := leftElem.Kind() == UnresolvedKind
	rightUnresolved := rightElem.Kind() == UnresolvedKind

	// Both unresolved - result is unresolved
	if leftUnresolved && rightUnresolved {
		return Unresolved{}
	}

	// One side unresolved - use the resolved type
	if leftUnresolved {
		return rightElem
	}
	if rightUnresolved {
		return leftElem
	}

	// Both resolved - perform element-wise type inference
	return ts.TypeInfixOp(leftElem, rightElem, op, tok)
}

// typeArrayScalarInfix handles operations where one operand is an array and the other is a scalar
func (ts *TypeSolver) typeArrayScalarInfix(left, right Type, leftIsArr bool, op string, tok token.Token) Type {
	// Concatenation only works on arrays (and strings)
	if op == token.SYM_CONCAT {
		cerr := &token.CompileError{
			Token: tok,
			Msg:   fmt.Sprintf("concatenation requires array operands (wrap scalars in [...]), got: %s ⊕ %s", left, right),
		}
		ts.Errors = append(ts.Errors, cerr)
		return Unresolved{}
	}

	// Extract element types for type inference
	var arrType Array
	var leftType, rightType Type

	if leftIsArr {
		arrType = left.(Array)
		leftType = arrType.ColTypes[0]
		rightType = right
	} else {
		arrType = right.(Array)
		leftType = left
		rightType = arrType.ColTypes[0]
	}

	// Compute result element type
	elemType := ts.TypeInfixOp(leftType, rightType, op, tok)

	// Return array with computed element type
	return Array{
		Headers:  cloneArrayHeaders(arrType.Headers),
		ColTypes: []Type{elemType},
		Length:   arrType.Length,
	}
}

func (ts *TypeSolver) TypeInfixOp(left, right Type, op string, tok token.Token) Type {
	key := opKey{
		Operator:  op,
		LeftType:  left,
		RightType: right,
	}

	var fn opFunc
	var ok bool
	if fn, ok = defaultOps[key]; !ok {
		cerr := &token.CompileError{
			Token: tok,
			Msg:   fmt.Sprintf("unsupported operator: %+v for types: %s, %s", tok, left, right),
		}
		ts.Errors = append(ts.Errors, cerr)
		return Unresolved{}
	}

	leftSym := &Symbol{
		Val:  llvm.Value{},
		Type: left,
	}
	rightSym := &Symbol{
		Val:  llvm.Value{},
		Type: right,
	}
	// compiler shouldn't matter as we are only inferring types
	// need to give compile flag as false to fn
	return fn(ts.ScriptCompiler.Compiler, leftSym, rightSym, false).Type
}

// TypePrefixExpression returns type of prefix expression output if input is not a pointer
// If input is a pointer then it applies prefix expression to the element pointed to
// This is because we automatically dereference if it's a pointer
func (ts *TypeSolver) TypePrefixExpression(expr *ast.PrefixExpression) (types []Type) {
	operand := ts.TypeExpression(expr.Right, false)
	types = []Type{}
	for _, opType := range operand {
		if ptr, yes := opType.(Ptr); yes {
			opType = ptr.Elem
		}

		if opType.Kind() == UnresolvedKind {
			types = append(types, Unresolved{})
			continue
		}

		isArrayExpr := false
		origArrayType := Array{}
		if opType.Kind() == ArrayKind {
			isArrayExpr = true
			origArrayType = opType.(Array)
			opType = origArrayType.ColTypes[0]
		}

		key := unaryOpKey{
			Operator:    expr.Operator,
			OperandType: opType,
		}

		var fn unaryOpFunc
		var ok bool
		if fn, ok = defaultUnaryOps[key]; !ok {
			cerr := &token.CompileError{
				Token: expr.Token,
				Msg:   fmt.Sprintf("unsupported unary operator: %q for type: %s", expr.Operator, opType),
			}
			ts.Errors = append(ts.Errors, cerr)
			types = append(types, Unresolved{})
			continue
		}

		opSym := &Symbol{
			Val:  llvm.Value{},
			Type: opType,
		}
		// compiler shouldn't matter as we are only inferring types
		// need to give compile flag as false to function
		resultType := fn(ts.ScriptCompiler.Compiler, opSym, false).Type

		if isArrayExpr {
			finalType := Array{
				Headers:  origArrayType.Headers,
				ColTypes: []Type{resultType},
				Length:   origArrayType.Length,
			}
			types = append(types, finalType)
			continue
		}

		types = append(types, resultType)
	}

	// Create new entry
	ts.ExprCache[expr] = &ExprInfo{
		OutTypes: types,
		ExprLen:  len(types),
	}

	return
}

// inside a call expression a Range becomes its interior type
func (ts *TypeSolver) TypeCallExpression(ce *ast.CallExpression, isRoot bool) []Type {
	args, innerArgs, arrayIters := ts.collectCallArgs(ce, isRoot)

	template, mangled, ok := ts.lookupCallTemplate(ce, args)
	if !ok {
		return nil
	}

	f := ts.InferFuncTypes(ce, innerArgs, mangled, template)
	return ts.finishCallTypes(f, ce, arrayIters)
}

func (ts *TypeSolver) collectCallArgs(ce *ast.CallExpression, isRoot bool) (args []Type, innerArgs []Type, arrayIters []*RangeInfo) {
	for _, e := range ce.Arguments {
		if e == nil {
			// Nil argument indicates a parse error; skip it
			// Parser errors are already recorded
			continue
		}
		exprArgs := ts.TypeExpression(e, isRoot)
		for _, arg := range exprArgs {
			ts.appendStandardCallArg(arg, e, &args, &innerArgs, &arrayIters)
		}
	}
	return args, innerArgs, arrayIters
}

func (ts *TypeSolver) appendStandardCallArg(arg Type, expr ast.Expression, args *[]Type, innerArgs *[]Type, arrayIters *[]*RangeInfo) {
	var paramType Type
	switch arg.Kind() {
	case RangeKind:
		paramType = arg
		*innerArgs = append(*innerArgs, arg.(Range).Iter)
	case ArrayKind:
		arr := arg.(Array)
		paramType = arr
		*innerArgs = append(*innerArgs, arr.ColTypes[0])
		*arrayIters = append(*arrayIters, &RangeInfo{
			Name:      ts.FreshIterName(),
			ArrayExpr: expr,
			ArrayType: arr,
			Over:      IterArray,
		})
	case ArrayRangeKind:
		arrRange := arg.(ArrayRange)
		paramType = arrRange
		// Like Range parameters, ArrayRange parameters are passed as-is to the function.
		// The function will handle iteration internally via funcLoopNest.
		// We pass the element type as innerArgs so the function body is typed correctly.
		*innerArgs = append(*innerArgs, arrRange.Array.ColTypes[0])
	default:
		paramType = arg
		*innerArgs = append(*innerArgs, arg)
	}
	*args = append(*args, paramType)
}

func (ts *TypeSolver) expectSingleArray(source ast.Expression, tok token.Token, context string) (Array, bool) {
	arrayTypes := ts.TypeExpression(source, false)
	if len(arrayTypes) != 1 {
		ts.Errors = append(ts.Errors, &token.CompileError{
			Token: tok,
			Msg:   fmt.Sprintf("%s expects a single array value, got %d", context, len(arrayTypes)),
		})
		return Array{}, false
	}

	arrType, ok := arrayTypes[0].(Array)
	if !ok {
		ts.Errors = append(ts.Errors, &token.CompileError{
			Token: tok,
			Msg:   fmt.Sprintf("%s target is not an array", context),
		})
		return Array{}, false
	}
	return arrType, true
}

func (ts *TypeSolver) lookupCallTemplate(ce *ast.CallExpression, args []Type) (*ast.FuncStatement, string, bool) {
	fk := ast.FuncKey{
		FuncName: ce.Function.Value,
		Arity:    len(args),
	}

	compiler := ts.ScriptCompiler.Compiler
	code := compiler.CodeCompiler.Code
	template, ok := code.Func.Map[fk]
	if !ok {
		cerr := &token.CompileError{
			Token: ce.Token,
			Msg:   fmt.Sprintf("undefined function: %s", ce.Function.Value),
		}
		ts.Errors = append(ts.Errors, cerr)
		return nil, "", false
	}

	mangled := mangle(ce.Function.Value, args)
	return template, mangled, true
}

func (ts *TypeSolver) finishCallTypes(f *Func, ce *ast.CallExpression, arrayIters []*RangeInfo) []Type {
	rawOut := append([]Type(nil), f.OutTypes...)
	finalOut := rawOut
	if len(arrayIters) > 0 {
		wrapped := make([]Type, len(rawOut))
		for i, ot := range rawOut {
			wrapped[i] = Array{Headers: nil, ColTypes: []Type{ot}, Length: 0}
		}
		finalOut = wrapped
	}

	rangesCopy := append([]*RangeInfo(nil), arrayIters...)
	info := &ExprInfo{OutTypes: finalOut, ExprLen: len(finalOut), Ranges: rangesCopy}
	ts.ExprCache[ce] = info
	return finalOut
}

func (ts *TypeSolver) InferFuncTypes(ce *ast.CallExpression, args []Type, mangled string, template *ast.FuncStatement) *Func {
	// first check scriptCompiler compiler if that itself has function in its function (perhaps through a previous script compilation)
	f, ok := ts.ScriptCompiler.Compiler.FuncCache[mangled]
	if ok && f.AllTypesInferred() {
		return f
	}

	if !ok {
		f = &Func{
			Name:     ce.Function.Value,
			Params:   args,
			OutTypes: []Type{},
		}
		for range template.Outputs {
			f.OutTypes = append(f.OutTypes, Unresolved{})
		}
		ts.ScriptCompiler.Compiler.FuncCache[mangled] = f
	}

	canType := true
	for i, arg := range args {
		if arg.Kind() == UnresolvedKind {
			if ts.ScriptFunc == "" {
				ce := &token.CompileError{
					Token: ce.Token,
					Msg:   fmt.Sprintf("Function in script called with unknown argument type. Func Name: %s. Argument #: %d", f.Name, i+1),
				}
				ts.Errors = append(ts.Errors, ce)
				canType = false
			}
		}
	}
	if !canType {
		return f
	}

	if ts.ScriptFunc == "" {
		ts.TypeScriptFunc(mangled, template, f)
		return f
	}

	ts.TypeFunc(mangled, template, f)
	return f
}

// This is a blocking iterative solver
// This function is assumed to be in the top level script
// If it does not resolve eventually to concrete output types then the code cannot and should not proceed further
func (ts *TypeSolver) TypeScriptFunc(mangled string, template *ast.FuncStatement, f *Func) []Type {
	ts.ScriptFunc = f.Name
	defer func() { ts.ScriptFunc = "" }()
	// multiple passes may be needed to infer types for script level function
	for range 100 {
		ts.Converging = false
		ts.TypeFunc(mangled, template, f)
		if !ts.Converging {
			ce := &token.CompileError{
				Token: template.Token,
				Msg:   fmt.Sprintf("Function %s is not converging. Check for cyclic recursion and that each function has a base case", f.Name),
			}
			ts.Errors = append(ts.Errors, ce)
			return f.OutTypes
		}
		if f.AllTypesInferred() {
			return f.OutTypes
		}
	}
	panic("Could not infer output types for function %s in script" + f.Name)
}

// TyeFunc attempts to walk the function block and infer types for outputs variables
// It ASSUMES all output types have not been inferred
func (ts *TypeSolver) TypeFunc(mangled string, template *ast.FuncStatement, f *Func) {
	if _, ok := ts.InProgress[mangled]; ok {
		return
	}
	ts.InProgress[mangled] = struct{}{}
	defer func() { delete(ts.InProgress, mangled) }()
	ts.TypeBlock(template, f)
}

func (ts *TypeSolver) TypeBlock(template *ast.FuncStatement, f *Func) {
	PushScope(&ts.Scopes, FuncScope)
	defer PopScope(&ts.Scopes)

	for i, id := range template.Parameters {
		Put(ts.Scopes, id.Value, f.Params[i])
	}

	oldErrs := len(ts.Errors)
	for _, stmt := range template.Body.Statements {
		ts.TypeStatement(stmt)
		if len(ts.Errors) > oldErrs {
			return
		}
	}

	for i, id := range template.Outputs {
		outArg, ok := Get(ts.Scopes, id.Value)
		if !ok {
			ce := &token.CompileError{
				Token: id.Token,
				Msg:   fmt.Sprintf("Should have either inferred type of output or marked it unresolved. Function %s. output argument: %s", f.Name, id.Value),
			}
			ts.Errors = append(ts.Errors, ce)
			return
		}

		oldOutArg := f.OutTypes[i]
		if oldOutArg.Kind() != UnresolvedKind {
			continue
		}

		if outArg.Kind() != UnresolvedKind {
			ts.Converging = true
			f.OutTypes[i] = outArg
			continue
		}
	}
}
