package compiler

import (
	"fmt"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

type RangeInfo struct {
	Name      string
	RangeLit  *ast.RangeLiteral
	ArrayExpr ast.Expression
	ArrayType Array
}

type ExprInfo struct {
	Ranges     []*RangeInfo   // either value from *ast.Identifier or a newly created value from tmp identifier for *ast.RangeLiteral
	Rewrite    ast.Expression // expression rewritten with a literal -> tmp value. (0:11) -> tmpIter0 etc.
	ExprLen    int
	OutTypes   []Type
	HasRanges  bool // True if expression involves ranges (propagated upward during typing)
	LoopInside bool // For CallExpression: true if function handles iteration, false if call site handles it
}

// ExprKey is the key for ExprCache, combining function context with expression.
// FuncNameMangled is the mangled function name ("" for script-level expressions).
type ExprKey struct {
	FuncNameMangled string
	Expr            ast.Expression
}

// key constructs an ExprKey for cache lookups
func key(funcNameMangled string, expr ast.Expression) ExprKey {
	return ExprKey{FuncNameMangled: funcNameMangled, Expr: expr}
}

// TypeSolver infers the types of various expressions
// It is mainly needed to infer the output types of a function
// given input arguments
type pendingExpr struct {
	expr       ast.Expression
	outTypeIdx int
}

type pendingBinding struct {
	FuncNameMangled string
	Name            string
}

type TypeSolver struct {
	ScriptCompiler  *ScriptCompiler
	Scopes          []Scope[Type]
	InProgress      map[string]struct{} // if we are currently in progress of inferring types for func. This is for recursion/mutual recursion
	ScriptFunc      string              // this is the current func in the main scope we are inferring type for
	FuncNameMangled string              // current function's mangled name ("" for script level)
	Converging      bool
	Errors          []*token.CompileError
	ExprCache       map[ExprKey]*ExprInfo
	TmpCounter      int // tmpCounter for uniquely naming temporary variables
	UnresolvedExprs map[pendingBinding][]pendingExpr
}

func NewTypeSolver(sc *ScriptCompiler) *TypeSolver {
	return &TypeSolver{
		ScriptCompiler:  sc,
		Scopes:          []Scope[Type]{NewScope[Type](FuncScope)},
		InProgress:      map[string]struct{}{},
		ScriptFunc:      "",
		FuncNameMangled: "",
		Converging:      false,
		Errors:          []*token.CompileError{},
		ExprCache:       sc.Compiler.ExprCache,
		TmpCounter:      0,
		UnresolvedExprs: make(map[pendingBinding][]pendingExpr),
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
		// For string arrays, normalize to StrH if either side is StrH.
		// This ensures consistent ownership semantics (runtime always heap-copies).
		if leftElemType.Kind() == StrKind && (IsStrH(leftElemType) || IsStrH(rightElemType)) {
			return concatWithMetadata(leftArr, rightArr, StrH{})
		}
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
	info := ts.ExprCache[key(ts.FuncNameMangled, expr)]
	info.OutTypes[0] = arr

	if ident, ok := expr.(*ast.Identifier); ok {
		// resolve any previously unresolved uses now that we know the concrete array type
		ts.resolveTrackedExprs(ident.Value, arr)
		return
	}

	switch e := expr.(type) {
	case *ast.InfixExpression:
		// Only recurse if the subexpressions are themselves array-typed
		if ts.ExprCache[key(ts.FuncNameMangled, e.Left)].OutTypes[0].Kind() == ArrayKind {
			ts.bindArrayOperand(e.Left, arr)
		}
		if ts.ExprCache[key(ts.FuncNameMangled, e.Right)].OutTypes[0].Kind() == ArrayKind {
			ts.bindArrayOperand(e.Right, arr)
		}
	case *ast.PrefixExpression:
		// Only recurse if the operand is array-typed
		if ts.ExprCache[key(ts.FuncNameMangled, e.Right)].OutTypes[0].Kind() == ArrayKind {
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

func (ts *TypeSolver) bindAssignment(name string, expr ast.Expression, idx int, t Type) {
	info := ts.ExprCache[key(ts.FuncNameMangled, expr)]
	if idx >= len(info.OutTypes) {
		return
	}

	// Invariant: assignment typing is one-way from RHS -> LHS.
	// We refine RHS expression output types from RHS-derived type `t` and then
	// propagate resolved types to tracked LHS bindings. Do not infer RHS types
	// from existing destination types, because that can corrupt ExprCache output
	// types for calls/expressions and lead to incorrect solver convergence.
	if CanRefineType(info.OutTypes[idx], t) {
		info.OutTypes[idx] = t
	}

	binding := pendingBinding{FuncNameMangled: ts.FuncNameMangled, Name: name}
	if !IsFullyResolvedType(t) {
		for _, pending := range ts.UnresolvedExprs[binding] {
			if pending.outTypeIdx == idx && pending.expr == expr {
				return
			}
		}
		ts.UnresolvedExprs[binding] = append(ts.UnresolvedExprs[binding], pendingExpr{expr: expr, outTypeIdx: idx})
		return
	}

	ts.resolveTrackedExprs(name, t)
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
	binding := pendingBinding{FuncNameMangled: ts.FuncNameMangled, Name: name}
	if entries, ok := ts.UnresolvedExprs[binding]; ok {
		for _, pending := range entries {
			// Invariant: pending expressions are only registered via bindAssignment
			// after their ExprCache entry has been created.
			info := ts.ExprCache[key(ts.FuncNameMangled, pending.expr)]
			if pending.outTypeIdx >= len(info.OutTypes) {
				continue
			}
			if CanRefineType(info.OutTypes[pending.outTypeIdx], t) {
				info.OutTypes[pending.outTypeIdx] = t
			}
		}
		if IsFullyResolvedType(t) {
			delete(ts.UnresolvedExprs, binding)
		}
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
func (ts *TypeSolver) HandleRanges(e ast.Expression) (ranges []*RangeInfo, rew ast.Expression) {
	rew = e
	switch t := e.(type) {
	case *ast.RangeLiteral:
		return ts.HandleRangeLiteral(t)
	case *ast.ArrayLiteral:
		return ts.HandleArrayLiteralRanges(t)
	case *ast.ArrayRangeExpression:
		return ts.HandleArrayRangeExpression(t)
	case *ast.InfixExpression:
		return ts.HandleInfixRanges(t)
	case *ast.PrefixExpression:
		return ts.HandlePrefixRanges(t)
	case *ast.CallExpression:
		return ts.HandleCallRanges(t)
	case *ast.Identifier:
		return ts.HandleIdentifierRanges(t)
	default:
		return
	}
}

// HandleRangeLiteral processes range literal expressions, converting them to temporary
// identifiers for use in loop generation.
func (ts *TypeSolver) HandleRangeLiteral(rangeLit *ast.RangeLiteral) (ranges []*RangeInfo, rew ast.Expression) {
	nm := ts.FreshIterName()
	ri := &RangeInfo{
		Name:     nm,
		RangeLit: rangeLit,
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

func (ts *TypeSolver) HandleArrayLiteralRanges(al *ast.ArrayLiteral) (ranges []*RangeInfo, rew ast.Expression) {
	info := ts.ExprCache[key(ts.FuncNameMangled, al)]

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
		// Cells always iterate to extract ranges and create scalars
		cellRanges, cellRew := ts.HandleRanges(cell)
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
		ts.ExprCache[key(ts.FuncNameMangled, newLit)] = infoCopy
		rew = newLit
	}

	info.Ranges = ranges
	info.Rewrite = rew

	return ranges, rew
}

func (ts *TypeSolver) HandleArrayRangeExpression(ar *ast.ArrayRangeExpression) (ranges []*RangeInfo, rew ast.Expression) {
	info := ts.ExprCache[key(ts.FuncNameMangled, ar)]

	arrRanges, arrRew := ts.HandleRanges(ar.Array)
	idxRanges, rangeRew := ts.HandleRanges(ar.Range)

	ranges = mergeUses(arrRanges, idxRanges)

	rew = ar
	if arrRew != ar.Array || rangeRew != ar.Range {
		newExpr := &ast.ArrayRangeExpression{Token: ar.Token, Array: arrRew, Range: rangeRew}
		ts.ExprCache[key(ts.FuncNameMangled, newExpr)] = &ExprInfo{
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
func (ts *TypeSolver) HandleInfixRanges(infix *ast.InfixExpression) (ranges []*RangeInfo, rew ast.Expression) {
	lRanges, l := ts.HandleRanges(infix.Left)
	rRanges, r := ts.HandleRanges(infix.Right)
	ranges = mergeUses(lRanges, rRanges)

	if l == infix.Left && r == infix.Right {
		rew = infix
	} else {
		cp := *infix
		cp.Left, cp.Right = l, r
		rew = &cp
		// Create a simple ExprCache entry for the rewritten expression
		// It should have no ranges since temporary iterators are scalars
		originalInfo := ts.ExprCache[key(ts.FuncNameMangled, infix)]
		ts.ExprCache[key(ts.FuncNameMangled, rew.(*ast.InfixExpression))] = &ExprInfo{
			OutTypes: originalInfo.OutTypes, // Same output types as original
			ExprLen:  originalInfo.ExprLen,
			Ranges:   nil, // No ranges for rewritten expressions
		}
	}

	info := ts.ExprCache[key(ts.FuncNameMangled, infix)]
	info.Ranges = ranges
	info.Rewrite = rew
	return
}

// HandlePrefixRanges processes prefix expressions, handling the operand and preserving
// the prefix operator while rewriting any contained ranges.
func (ts *TypeSolver) HandlePrefixRanges(prefix *ast.PrefixExpression) (ranges []*RangeInfo, rew ast.Expression) {
	ranges, r := ts.HandleRanges(prefix.Right)

	if r == prefix.Right {
		rew = prefix
	} else {
		cp := *prefix
		cp.Right = r
		rew = &cp
		originalInfo := ts.ExprCache[key(ts.FuncNameMangled, prefix)]
		ts.ExprCache[key(ts.FuncNameMangled, rew.(*ast.PrefixExpression))] = &ExprInfo{
			OutTypes: originalInfo.OutTypes,
			ExprLen:  originalInfo.ExprLen,
			Ranges:   nil,
		}
	}

	info := ts.ExprCache[key(ts.FuncNameMangled, prefix)]
	info.Ranges = ranges
	info.Rewrite = rew
	return
}

// collectExprRanges processes a list of expressions, collecting their ranges
// and building rewritten expressions. Returns the merged ranges, rewritten
// expressions, and whether any expression was changed.
func (ts *TypeSolver) collectExprRanges(exprs []ast.Expression) (ranges []*RangeInfo, rewrites []ast.Expression, changed bool) {
	rewrites = make([]ast.Expression, len(exprs))
	for i, e := range exprs {
		eRanges, e2 := ts.HandleRanges(e)
		rewrites[i] = e2
		changed = changed || (e2 != e)
		ranges = mergeUses(ranges, eRanges)
	}
	return
}

// HandleCallRanges processes function call expressions, handling all arguments
// and merging their range information for proper loop generation.
func (ts *TypeSolver) HandleCallRanges(call *ast.CallExpression) (ranges []*RangeInfo, rew ast.Expression) {
	ranges, args, changed := ts.collectExprRanges(call.Arguments)
	info := ts.ExprCache[key(ts.FuncNameMangled, call)]

	if !changed {
		info.Ranges = ranges
		info.Rewrite = call
		return ranges, call
	}

	cp := *call
	cp.Arguments = args
	rew = &cp
	// Cache the rewritten expression with no ranges (ranges have been extracted)
	ts.ExprCache[key(ts.FuncNameMangled, rew.(*ast.CallExpression))] = &ExprInfo{
		OutTypes: info.OutTypes,
		ExprLen:  info.ExprLen,
		Ranges:   nil,
	}
	info.Ranges = ranges
	info.Rewrite = rew
	return
}

// isBareRangeExpr checks if expression is a bare range expression.
// These are "simple" range arguments that can be passed to specialized functions.
// For ArrayRangeExpression, it's only bare if the array part doesn't have ranges
// and the index is itself bare (e.g., arr[i] is bare, but [i][j] or arr[i+1] is not).
func (ts *TypeSolver) isBareRangeExpr(expr ast.Expression) bool {
	switch e := expr.(type) {
	case *ast.Identifier, *ast.RangeLiteral:
		return true
	case *ast.ArrayRangeExpression:
		// Only bare if array doesn't have ranges and index is bare
		arrInfo := ts.ExprCache[key(ts.FuncNameMangled, e.Array)]
		return !arrInfo.HasRanges && ts.isBareRangeExpr(e.Range)
	default:
		return false
	}
}

// HandleIdentifierRanges processes identifier expressions, detecting if they refer
// to range-typed variables and including them in range tracking.
// Note: This returns ranges but does NOT set info.Ranges on the identifier itself.
// This is intentional - bare identifiers like `i` should print as range representations.
// Ranges are only extracted when the identifier is used in an expression context
// (e.g., in a function call or infix operation) that needs scalar values.
func (ts *TypeSolver) HandleIdentifierRanges(ident *ast.Identifier) (ranges []*RangeInfo, rew ast.Expression) {
	typ, ok := ts.GetIdentifier(ident.Value)
	if ok && (typ.Kind() == RangeKind || typ.Kind() == ArrayRangeKind) {
		ri := &RangeInfo{
			Name:     ident.Value,
			RangeLit: nil,
		}
		ranges = []*RangeInfo{ri}
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

	for binding, entries := range ts.UnresolvedExprs {
		// Intentional: only report unresolved bindings for script scope.
		// Function-scope unresolved types may be resolved later when that function
		// is reached by a concrete script-level call.
		if binding.FuncNameMangled != "" {
			continue
		}
		for _, pending := range entries {
			ts.Errors = append(ts.Errors, &token.CompileError{
				Token: pending.expr.Tok(),
				Msg:   fmt.Sprintf("type for %q could not be resolved", binding.Name),
			})
		}
	}

}

func (ts *TypeSolver) TypePrintStatement(stmt *ast.PrintStatement) {
	ts.TypeExpression(stmt.Expression, true)
}

// ensureScalarCallVariant ensures the scalar variant of a function exists.
// This is needed when a call with LoopInside=true (e.g., Square(m) where m is a bare range)
// is inside a print statement that iterates - at compile time, ranges are shadowed with scalars.
func (ts *TypeSolver) ensureScalarCallVariant(ce *ast.CallExpression) {
	// Compute scalar types for all arguments
	scalarArgs := []Type{}
	for _, arg := range ce.Arguments {
		argInfo := ts.ExprCache[key(ts.FuncNameMangled, arg)]
		if argInfo == nil {
			// This shouldn't happen if TypeExpression was called correctly
			ts.Errors = append(ts.Errors, &token.CompileError{
				Token: arg.Tok(),
				Msg:   "internal: missing type info for call argument",
			})
			return
		}
		for _, t := range argInfo.OutTypes {
			innerType := t
			switch t.Kind() {
			case RangeKind:
				innerType = t.(Range).Iter
			case ArrayRangeKind:
				innerType = t.(ArrayRange).Array.ColTypes[0]
			}
			scalarArgs = append(scalarArgs, innerType)
		}
	}

	// Look up and create the scalar variant
	template, mangled, ok := ts.lookupCallTemplate(ce, scalarArgs)
	if ok {
		ts.InferFuncTypes(ce, scalarArgs, mangled, template)
	}
}

func (ts *TypeSolver) TypeLetStatement(stmt *ast.LetStatement) {
	// type conditions in case there may be functions we have to type
	for _, expr := range stmt.Condition {
		ts.TypeExpression(expr, true)
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
		ts.bindAssignment(ident.Value, exprRefs[i], exprIdxs[i], newType)

		typ, exists := Get(ts.Scopes, ident.Value)
		if exists {
			// Existing bindings with unresolved RHS are left as-is.
			// Use deep resolution so container types (e.g. arrays with unresolved
			// element types) are also treated as unresolved.
			if !IsFullyResolvedType(newType) {
				continue
			}
			if !CanRefineType(typ, newType) {
				ce := &token.CompileError{
					Token: ident.Token,
					Msg:   fmt.Sprintf("cannot reassign type to identifier. Old Type: %s. New Type: %s. Identifier %q", typ, newType, ident.Token.Literal),
				}
				ts.Errors = append(ts.Errors, ce)
				return
			}
		}

		trueValues[ident.Value] = newType
	}

	PutBulk(ts.Scopes, trueValues)
}

// TypeArrayExpression infers the type of an Array literal. Columns must be
// homogeneous and of primitive types: I64, F64 (promotion from I64 allowed), or Str.
// The returned type slice contains a single Array type value.
func (ts *TypeSolver) TypeArrayExpression(al *ast.ArrayLiteral) []Type {
	// Empty array literal: [] (no rows)
	if len(al.Headers) == 0 && len(al.Rows) == 0 {
		arr := Array{Headers: nil, ColTypes: []Type{Unresolved{}}, Length: 0}
		ts.ExprCache[key(ts.FuncNameMangled, al)] = &ExprInfo{OutTypes: []Type{arr}, ExprLen: 1, HasRanges: false}
		return []Type{arr}
	}

	// Vector special-case: single row, no headers → treat as 1-column vector
	if len(al.Headers) == 0 && len(al.Rows) == 1 {
		colTypes := []Type{Unresolved{}}
		row := al.Rows[0]
		hasRanges := false
		for col := 0; col < len(row); col++ {
			cellT, ok := ts.typeCell(row[col], al.Tok())
			if !ok {
				continue
			}
			colTypes[0] = ts.mergeColType(colTypes[0], cellT, 0, al.Tok())
			// Propagate HasRanges from cells
			if ts.ExprCache[key(ts.FuncNameMangled, row[col])].HasRanges {
				hasRanges = true
			}
		}
		// Length = number of elements in the row (for vectors)
		arr := Array{Headers: nil, ColTypes: colTypes, Length: len(row)}

		// Cache this expression's resolved type for the compiler
		ts.ExprCache[key(ts.FuncNameMangled, al)] = &ExprInfo{OutTypes: []Type{arr}, ExprLen: 1, HasRanges: hasRanges}
		return []Type{arr}
	}
	// unsupported as of now
	types := []Type{Unresolved{}}
	ts.ExprCache[key(ts.FuncNameMangled, al)] = &ExprInfo{OutTypes: types, ExprLen: 1}
	return types
	/*
	   // 1. Determine number of columns
	   numCols := ts.arrayNumCols(al)

	   	if numCols == 0 {
	   		if iterate {
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
	tps := ts.TypeExpression(expr, false) // cells are nested, not root
	if len(tps) != 1 {
		ts.Errors = append(ts.Errors, &token.CompileError{
			Token: tok,
			Msg:   fmt.Sprintf("array cell produced %d types; expected 1", len(tps)),
		})
		return Unresolved{}, false
	}
	cellType := tps[0]
	if cellType.Kind() == UnresolvedKind {
		ts.Errors = append(ts.Errors, &token.CompileError{Token: tok, Msg: "array cell type could not be resolved"})
		return Unresolved{}, false
	}
	return cellType, true
}

// mergeColType merges a new cell type into the running column type, enforcing
// allowed schemas and numeric promotion (I64 -> F64). Reports errors against tok.
func (ts *TypeSolver) mergeColType(cur Type, newT Type, colIdx int, tok token.Token) Type {
	// If the cell type is unresolved, skip it.
	if newT.Kind() == UnresolvedKind {
		return cur
	}

	// Normalize strings to StrH since runtime always heap-copies into arrays.
	if newT.Kind() == StrKind {
		newT = StrH{}
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

	// Numeric promotion to F64 is allowed.
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
	case StrG, StrH:
		return true
	default:
		return false
	}
}

func (ts *TypeSolver) TypeRangeExpression(r *ast.RangeLiteral, isRoot bool) []Type {
	// infer start and stop - these are nested expressions
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
		types := []Type{Int{Width: 64}}
		ts.ExprCache[key(ts.FuncNameMangled, r)] = &ExprInfo{OutTypes: types, ExprLen: 1, HasRanges: true}
		return types // nested: return inner type
	}
	// root: return Range type
	types := []Type{Range{Iter: startT[0]}}
	ts.ExprCache[key(ts.FuncNameMangled, r)] = &ExprInfo{OutTypes: types, ExprLen: 1, HasRanges: true}
	return types
}

func (ts *TypeSolver) TypeArrayRangeExpression(ax *ast.ArrayRangeExpression, isRoot bool) []Type {
	info := &ExprInfo{OutTypes: []Type{Unresolved{}}, ExprLen: 1}
	ts.ExprCache[key(ts.FuncNameMangled, ax)] = info

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
	// String array element access does strdup at runtime, so result is heap-allocated
	if elemType.Kind() == StrKind {
		elemType = StrH{}
	}

	idxTypes := ts.TypeExpression(ax.Range, isRoot)
	info.HasRanges = ts.ExprCache[key(ts.FuncNameMangled, ax.Array)].HasRanges || ts.ExprCache[key(ts.FuncNameMangled, ax.Range)].HasRanges
	if len(idxTypes) != 1 {
		ts.Errors = append(ts.Errors, &token.CompileError{
			Token: ax.Tok(),
			Msg:   fmt.Sprintf("array index expects a single value, got %d", len(idxTypes)),
		})
		return info.OutTypes
	}

	idxType := idxTypes[0]
	if idxType.Kind() != IntKind && idxType.Kind() != RangeKind {
		ts.Errors = append(ts.Errors, &token.CompileError{
			Token: ax.Tok(),
			Msg:   fmt.Sprintf("array index expects an integer or range, got %s", idxType),
		})
		return info.OutTypes
	}

	if !isRoot {
		// nested: return element type (iterating)
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

	info.OutTypes = []Type{ArrayRange{
		Array: arrType,
		Range: idxType.(Range),
	}}
	info.ExprLen = 1
	return info.OutTypes
}

func (ts *TypeSolver) TypeExpression(expr ast.Expression, isRoot bool) (types []Type) {
	oldErrs := len(ts.Errors)
	types = []Type{}
	switch e := expr.(type) {
	case *ast.IntegerLiteral:
		types = append(types, Int{Width: 64})
		ts.ExprCache[key(ts.FuncNameMangled, e)] = &ExprInfo{OutTypes: types, ExprLen: 1}
	case *ast.FloatLiteral:
		types = append(types, Float{Width: 64})
		ts.ExprCache[key(ts.FuncNameMangled, e)] = &ExprInfo{OutTypes: types, ExprLen: 1}
	case *ast.StringLiteral:
		// Check if string has valid format markers - if so, it's a heap string
		var strType Type = StrG{}
		if hasValidMarkers(e.Value, ts.isDefined) {
			strType = StrH{}
		}
		types = append(types, strType)
		ts.ExprCache[key(ts.FuncNameMangled, e)] = &ExprInfo{OutTypes: types, ExprLen: 1}
	case *ast.HeapStringLiteral:
		types = append(types, StrH{})
		ts.ExprCache[key(ts.FuncNameMangled, e)] = &ExprInfo{OutTypes: types, ExprLen: 1}
	case *ast.ArrayLiteral:
		types = append(types, ts.TypeArrayExpression(e)...)
	case *ast.ArrayRangeExpression:
		types = append(types, ts.TypeArrayRangeExpression(e, isRoot)...)
	case *ast.RangeLiteral:
		types = append(types, ts.TypeRangeExpression(e, isRoot)...)
	case *ast.Identifier:
		t := ts.TypeIdentifier(e)
		// At root level, keep Range type; nested, extract inner type
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

	if len(ts.Errors) > oldErrs {
		return
	}

	if isRoot {
		ts.HandleRanges(expr)
	}
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

func (ts *TypeSolver) isDefined(name string) bool {
	_, ok := ts.GetIdentifier(name)
	return ok
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
		t = Unresolved{}
		ts.ExprCache[key(ts.FuncNameMangled, ident)] = &ExprInfo{OutTypes: []Type{t}, ExprLen: 1}
		return
	}

	ts.ExprCache[key(ts.FuncNameMangled, ident)] = &ExprInfo{OutTypes: []Type{t}, ExprLen: 1, HasRanges: t.Kind() == RangeKind || t.Kind() == ArrayRangeKind}
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
		types = []Type{Unresolved{}}
		ts.ExprCache[key(ts.FuncNameMangled, expr)] = &ExprInfo{OutTypes: types, ExprLen: 1}
		return
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
	ts.ExprCache[key(ts.FuncNameMangled, expr)] = &ExprInfo{
		OutTypes:  types,
		ExprLen:   len(types),
		HasRanges: ts.ExprCache[key(ts.FuncNameMangled, expr.Left)].HasRanges || ts.ExprCache[key(ts.FuncNameMangled, expr.Right)].HasRanges,
	}

	return
}

func (ts *TypeSolver) TypeArrayInfix(left, right Type, op string, tok token.Token) Type {
	// Handle string concatenation early - always returns heap-allocated string
	if op == token.SYM_CONCAT && left.Kind() == StrKind && right.Kind() == StrKind {
		return StrH{}
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
		LeftType:  left.Key(),
		RightType: right.Key(),
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
	// Operand is nested, so ranges become their inner type
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

		opKey := unaryOpKey{
			Operator:    expr.Operator,
			OperandType: opType,
		}

		var fn unaryOpFunc
		var ok bool
		if fn, ok = defaultUnaryOps[opKey]; !ok {
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
	ts.ExprCache[key(ts.FuncNameMangled, expr)] = &ExprInfo{
		OutTypes:  types,
		ExprLen:   len(types),
		HasRanges: ts.ExprCache[key(ts.FuncNameMangled, expr.Right)].HasRanges,
	}

	return
}

// inside a call expression a Range becomes its interior type
func (ts *TypeSolver) TypeCallExpression(ce *ast.CallExpression, isRoot bool) []Type {
	info := &ExprInfo{OutTypes: []Type{Unresolved{}}, ExprLen: 1}
	ts.ExprCache[key(ts.FuncNameMangled, ce)] = info

	args, innerArgs, loopInside := ts.collectCallArgs(ce, isRoot)

	// Compute hasRanges from all arguments
	hasRanges := false
	for _, e := range ce.Arguments {
		if ts.ExprCache[key(ts.FuncNameMangled, e)].HasRanges {
			hasRanges = true
			break
		}
	}

	// Handle builtins - no template lookup needed
	if builtin, ok := Builtins[ce.Function.Value]; ok {
		info.OutTypes = append([]Type(nil), builtin.ReturnTypes...)
		info.ExprLen = len(info.OutTypes)
		info.HasRanges = hasRanges
		info.LoopInside = loopInside
		return info.OutTypes
	}

	template, mangled, ok := ts.lookupCallTemplate(ce, args)
	if !ok {
		return info.OutTypes
	}

	f := ts.InferFuncTypes(ce, innerArgs, mangled, template)
	info.OutTypes = append([]Type(nil), f.OutTypes...)
	info.ExprLen = len(info.OutTypes)
	info.HasRanges = hasRanges
	info.LoopInside = loopInside
	return info.OutTypes
}

// TypeExprsForIter types a list of expressions and determines whether iteration
// should happen externally (loopInside=false) or be handled by callees (loopInside=true).
// When loopInside=false, ensures scalar variants exist for any calls that expected
// to handle ranges internally. Returns the outer types for each expression.
func (ts *TypeSolver) TypeExprsForIter(exprs []ast.Expression, isRoot bool) (outerTypes [][]Type, loopInside bool, hasRanges bool) {
	loopInside = true
	outerTypes = make([][]Type, len(exprs))
	for i, e := range exprs {
		outerTypes[i] = ts.TypeExpression(e, isRoot)
		info := ts.ExprCache[key(ts.FuncNameMangled, e)]
		if info == nil || !info.HasRanges {
			continue
		}
		hasRanges = true
		if !ts.isBareRangeExpr(e) {
			loopInside = false
		}
	}

	if loopInside {
		return
	}

	// loopInside=false: iteration happens externally via withLoopNest.
	// Inside that loop, ranges become scalars. But calls like Square(m) where
	// m is bare were typed with loopInside=true (Range variant only).
	// Ensure scalar variants exist for compile-time lookup.
	for _, e := range exprs {
		call, ok := e.(*ast.CallExpression)
		if !ok {
			continue
		}
		info := ts.ExprCache[key(ts.FuncNameMangled, e)]
		if info == nil || !info.LoopInside {
			continue
		}
		ts.ensureScalarCallVariant(call)
	}
	return
}

// collectCallArgs types arguments and builds arg type lists for function lookup.
// Uses the shared TypeExprsForIter for the core logic.
func (ts *TypeSolver) collectCallArgs(ce *ast.CallExpression, isRoot bool) (args []Type, innerArgs []Type, loopInside bool) {
	outerTypesPerArg, loopInside, _ := ts.TypeExprsForIter(ce.Arguments, isRoot)

	// Build args and innerArgs from outer types
	// If loopInside=false, ALL range args become their inner type (loop outside)
	for _, outerTypes := range outerTypesPerArg {
		for _, outerType := range outerTypes {
			innerType := outerType
			switch outerType.Kind() {
			case RangeKind:
				innerType = outerType.(Range).Iter
			case ArrayRangeKind:
				innerType = outerType.(ArrayRange).Array.ColTypes[0]
			}
			innerArgs = append(innerArgs, innerType)

			if loopInside {
				args = append(args, outerType)
			} else {
				args = append(args, innerType)
			}
		}
	}
	return
}

/*
// getInnerType returns the type that operations work with when given a Range/ArrayRange.
// Range → Range.Iter, ArrayRange → element type, other → unchanged

	func getInnerType(t Type) Type {
		switch t.Kind() {
		case RangeKind:
			return t.(Range).Iter
		case ArrayRangeKind:
			return t.(ArrayRange).Array.ColTypes[0]
		default:
			return t
		}
	}

	func (ts *TypeSolver) appendStandardCallArg(arg Type, args *[]Type, innerArgs *[]Type, hasIter *bool) {
		var paramType Type
		switch arg.Kind() {
		case RangeKind:
			paramType = arg
			*innerArgs = append(*innerArgs, getInnerType(arg))
			*hasIter = true
		case ArrayRangeKind:
			arrRange := arg.(ArrayRange)
			paramType = arrRange
			// Like Range parameters, ArrayRange parameters are passed as-is to the function.
			// The function will handle iteration internally via funcLoopNest.
			// We pass the element type as innerArgs so the function body is typed correctly.
			*innerArgs = append(*innerArgs, getInnerType(arg))
			*hasIter = true
		default:
			paramType = arg
			*innerArgs = append(*innerArgs, arg)
		}
		*args = append(*args, paramType)
	}
*/
func (ts *TypeSolver) expectSingleArray(source ast.Expression, tok token.Token, context string) (Array, bool) {
	arrayTypes := ts.TypeExpression(source, false) // nested expression
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

// lookupCallTemplate finds the function template and generates its mangled name.
// TODO: Currently only supports functions in the current module. When the module
// system is built, extend this to look up functions from imported packages and
// use the imported package's ModName/RelPath for mangling (not the caller's).
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

	cc := ts.ScriptCompiler.Compiler.CodeCompiler
	mangled := Mangle(cc.Compiler.MangledPath, ce.Function.Value, args)
	return template, mangled, true
}

// newFunc creates a new Func entry for the given call expression and caches it.
// String params keep their StrG/StrH type - functions are mangled separately for each.
func (ts *TypeSolver) newFunc(ce *ast.CallExpression, args []Type, mangled string, template *ast.FuncStatement) *Func {
	f := &Func{
		Name:     ce.Function.Value,
		Params:   args,
		OutTypes: make([]Type, len(template.Outputs)),
	}
	for i := range f.OutTypes {
		f.OutTypes[i] = Unresolved{}
	}
	ts.ScriptCompiler.Compiler.FuncCache[mangled] = f
	return f
}

func (ts *TypeSolver) InferFuncTypes(ce *ast.CallExpression, args []Type, mangled string, template *ast.FuncStatement) *Func {
	// Fetch existing func cache entry (if any).
	// Already-inferred fast-path is handled centrally in TypeFunc.
	f, ok := ts.ScriptCompiler.Compiler.FuncCache[mangled]

	// Create new Func if not cached (ok means recursive/previously seen call, reuse f)
	if !ok {
		f = ts.newFunc(ce, args, mangled, template)
	}

	// Inside a function - unresolved args are allowed (resolved in later passes)
	if ts.ScriptFunc != "" {
		ts.TypeFunc(mangled, template, f)
		return f
	}

	// At script level, all arg types must be resolved before typing
	for i, arg := range args {
		if arg.Kind() != UnresolvedKind {
			continue
		}
		ts.Errors = append(ts.Errors, &token.CompileError{
			Token: ce.Token,
			Msg:   fmt.Sprintf("Function in script called with unknown argument type. Func Name: %s. Argument #: %d", f.Name, i+1),
		})
		return f
	}
	ts.TypeScriptFunc(mangled, template, f)
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
		inferred := ts.TypeFunc(mangled, template, f)
		// Root script call only depends on this function's concrete outputs.
		if inferred {
			return f.OutTypes
		}

		// no further progress possible
		if !ts.Converging {
			ce := &token.CompileError{
				Token: template.Token,
				Msg:   fmt.Sprintf("Function %s is not converging. Check for cyclic recursion and that each function has a base case", f.Name),
			}
			ts.Errors = append(ts.Errors, ce)
			return f.OutTypes
		}
	}
	panic("Could not infer output types for function %s in script" + f.Name)
}

// refreshInferredFuncExprCache runs one extra local type pass to refresh ExprCache
// entries after a function first reaches fully inferred outputs.
// Unresolved callees are temporarily blocked so this pass does not recurse into
// additional function inference.
func (ts *TypeSolver) refreshInferredFuncExprCache(mangled string, template *ast.FuncStatement, f *Func) {
	blocked := make(map[string]struct{}, len(ts.InProgress)+len(ts.ScriptCompiler.Compiler.FuncCache))
	// Block this function and unresolved callees so this pass stays local.
	blocked[mangled] = struct{}{}
	for fn := range ts.InProgress {
		blocked[fn] = struct{}{}
	}
	for fn, cached := range ts.ScriptCompiler.Compiler.FuncCache {
		if !cached.OutputTypesInferred() {
			blocked[fn] = struct{}{}
		}
	}

	savedInProgress := ts.InProgress
	savedFuncNameMangled := ts.FuncNameMangled
	savedConverging := ts.Converging
	ts.InProgress = blocked
	ts.FuncNameMangled = mangled
	ts.TypeBlock(template, f)
	ts.FuncNameMangled = savedFuncNameMangled
	ts.InProgress = savedInProgress
	ts.Converging = savedConverging
}

// TypeFunc attempts to walk the function block and infer types for output variables.
// It ASSUMES all output types have not been inferred
func (ts *TypeSolver) TypeFunc(mangled string, template *ast.FuncStatement, f *Func) bool {
	if f.OutputTypesInferred() {
		return true
	}
	if _, ok := ts.InProgress[mangled]; ok {
		return f.OutputTypesInferred()
	}
	ts.InProgress[mangled] = struct{}{}
	defer func() { delete(ts.InProgress, mangled) }()

	// Set FuncNameMangled so ExprCache entries are keyed to this function
	savedFuncNameMangled := ts.FuncNameMangled
	ts.FuncNameMangled = mangled
	defer func() { ts.FuncNameMangled = savedFuncNameMangled }()

	ts.TypeBlock(template, f)
	inferred := f.OutputTypesInferred()
	if inferred {
		ts.refreshInferredFuncExprCache(mangled, template, f)
	}
	return inferred
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
		if IsFullyResolvedType(oldOutArg) {
			continue
		}

		if IsFullyResolvedType(outArg) {
			ts.Converging = true
			f.OutTypes[i] = outArg
			continue
		}
	}
}
