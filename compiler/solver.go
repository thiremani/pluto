package compiler

import (
	"fmt"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

type RangeInfo struct {
	Name string
	Lit  *ast.RangeLiteral // if Lit is nil then it is an identifier and compiler must get the value from its scope
}

type ExprInfo struct {
	Ranges   []*RangeInfo   // either value from *ast.Identifier or a newly created value from tmp identifier for *ast.RangeLiteral
	Rewrite  ast.Expression // expression rewritten with a literal -> tmp value. (0:11) -> tmpIter0 etc.
	ExprLen  int
	LeftVar  bool // true means left has ranges in its sub expressions so it is variable
	RightVar bool // true means right has ranges in its sub expressions so it is variablel
}

// TypeSolver infers the types of various expressions
// It is mainly needed to infer the output types of a function
// given input arguments
type TypeSolver struct {
	ScriptCompiler *ScriptCompiler
	Scopes         []Scope[Type]
	InProgress     map[string]struct{} // if we are currently in progress of inferring types for func. This is for recursion/mutual recursion
	ScriptFunc     string              // this is the current func in the main scope we are inferring type for
	Converging     bool
	Errors         []*token.CompileError
	ExprCache      map[ast.Expression]*ExprInfo
	TmpCounter     int // tmpCounter for uniquely naming temporary variables
}

func NewTypeSolver(sc *ScriptCompiler) *TypeSolver {
	return &TypeSolver{
		ScriptCompiler: sc,
		Scopes:         []Scope[Type]{NewScope[Type](FuncScope)},
		InProgress:     map[string]struct{}{},
		ScriptFunc:     "",
		Converging:     false,
		Errors:         []*token.CompileError{},
		ExprCache:      make(map[ast.Expression]*ExprInfo),
		TmpCounter:     0,
	}
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

// HandleRanges processes expressions to identify and rewrite range literals for loop generation.
// It traverses the AST, replacing range literals with temporary identifiers and collecting
// range information for later compilation into loops.
func (ts *TypeSolver) HandleRanges(e ast.Expression, isRoot bool, exprLen int) (ranges []*RangeInfo, rew ast.Expression) {
	rew = e
	switch t := e.(type) {
	case *ast.RangeLiteral:
		return ts.HandleRangeLiteral(t, isRoot)
	case *ast.InfixExpression:
		return ts.HandleInfixRanges(t, isRoot, exprLen)
	case *ast.PrefixExpression:
		return ts.HandlePrefixRanges(t, isRoot, exprLen)
	case *ast.CallExpression:
		return ts.HandleCallRanges(t, isRoot, exprLen)
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
		Name: nm,
		Lit:  rangeLit,
	}
	ranges = []*RangeInfo{ri}
	rew = &ast.Identifier{Value: nm, Token: rangeLit.Tok()}
	return
}

// HandleInfixRanges processes infix expressions, recursively handling both operands
// and tracking whether each side contains ranges for optimization decisions.
func (ts *TypeSolver) HandleInfixRanges(infix *ast.InfixExpression, isRoot bool, exprLen int) (ranges []*RangeInfo, rew ast.Expression) {
	lRanges, l := ts.HandleRanges(infix.Left, false, exprLen)
	rRanges, r := ts.HandleRanges(infix.Right, false, exprLen)
	ranges = mergeUses(lRanges, rRanges)
	
	if l == infix.Left && r == infix.Right {
		rew = infix
	} else {
		cp := *infix
		cp.Left, cp.Right = l, r
		rew = &cp
	}
	
	if isRoot {
		ts.ExprCache[infix] = &ExprInfo{
			Ranges:   ranges,
			Rewrite:  rew,
			ExprLen:  exprLen,
			LeftVar:  len(lRanges) > 0,
			RightVar: len(rRanges) > 0,
		}
	}
	return
}

// HandlePrefixRanges processes prefix expressions, handling the operand and preserving
// the prefix operator while rewriting any contained ranges.
func (ts *TypeSolver) HandlePrefixRanges(prefix *ast.PrefixExpression, isRoot bool, exprLen int) (ranges []*RangeInfo, rew ast.Expression) {
	ranges, r := ts.HandleRanges(prefix.Right, false, exprLen)
	
	if r == prefix.Right {
		rew = prefix
	} else {
		cp := *prefix
		cp.Right = r
		rew = &cp
	}
	
	if isRoot {
		ts.ExprCache[prefix] = &ExprInfo{
			Ranges:  ranges,
			Rewrite: rew,
			ExprLen: exprLen,
		}
	}
	return
}

// HandleCallRanges processes function call expressions, handling all arguments
// and merging their range information for proper loop generation.
func (ts *TypeSolver) HandleCallRanges(call *ast.CallExpression, isRoot bool, exprLen int) (ranges []*RangeInfo, rew ast.Expression) {
	changed := false
	args := make([]ast.Expression, len(call.Arguments))
	
	for i, a := range call.Arguments {
		aRanges, a2 := ts.HandleRanges(a, isRoot, exprLen)
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
				Name: ident.Value,
				Lit:  nil,
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
}

func (ts *TypeSolver) TypePrintStatement(stmt *ast.PrintStatement) {
	for _, expr := range stmt.Expression {
		types := ts.TypeExpression(expr, true)
		ts.HandleRanges(expr, true, len(types))
	}
}

func (ts *TypeSolver) TypeLetStatement(stmt *ast.LetStatement) {
	// type conditions in case there may be functions we have to type
	types := []Type{}
	for _, expr := range stmt.Value {
		exprTypes := ts.TypeExpression(expr, true)
		types = append(types, exprTypes...)
		ts.HandleRanges(expr, true, len(exprTypes))
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
		var typ Type
		var ok bool
		if typ, ok = Get(ts.Scopes, ident.Value); ok {
			// if new type is Unresolved then don't update new type or do a type check
			if newType.Kind() == UnresolvedKind {
				continue
			}
			// allow for type reassignment from unknown type to concrete type
			if typ.Kind() == UnresolvedKind {
				trueValues[ident.Value] = newType
				continue
			}
			if newType.String() != typ.String() {
				ce := &token.CompileError{
					Token: ident.Token,
					Msg:   fmt.Sprintf("cannot reassign type to identifier. Old Type: %s. New Type: %s. Identifier %q", typ, newType, ident.Token.Literal),
				}
				ts.Errors = append(ts.Errors, ce)
				return
			}
			continue
		}
		trueValues[ident.Value] = newType
	}

	PutBulk(ts.Scopes, trueValues)
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

func (ts *TypeSolver) TypeExpression(expr ast.Expression, isRoot bool) (types []Type) {
	types = []Type{}
	switch e := expr.(type) {
	case *ast.IntegerLiteral:
		types = append(types, Int{Width: 64})
	case *ast.FloatLiteral:
		types = append(types, Float{Width: 64})
	case *ast.StringLiteral:
		types = append(types, Str{})
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
	return
}

func (ts *TypeSolver) GetIdentifier(name string) (Type, bool) {
	if t, ok := Get(ts.Scopes, name); ok {
		if ptr, yes := t.(Pointer); yes {
			return ptr.Elem, true
		}
		return t, true
	}

	compiler := ts.ScriptCompiler.Compiler
	cc := compiler.CodeCompiler.Compiler
	if s, ok := Get(cc.Scopes, name); ok {
		t := s.Type
		if ptr, yes := t.(Pointer); yes {
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
	}
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
	var ptr Pointer
	for i := range left {
		leftType := left[i]
		if ptr, ok = leftType.(Pointer); ok {
			leftType = ptr.Elem
		}

		rightType := right[i]
		if ptr, ok = rightType.(Pointer); ok {
			rightType = ptr.Elem
		}

		if leftType.Kind() == UnresolvedKind || rightType.Kind() == UnresolvedKind {
			types = append(types, Unresolved{})
			continue
		}

		key := opKey{
			Operator:  expr.Operator,
			LeftType:  leftType.String(),
			RightType: rightType.String(),
		}

		var fn opFunc
		var ok bool
		if fn, ok = defaultOps[key]; !ok {
			cerr := &token.CompileError{
				Token: expr.Token,
				Msg:   fmt.Sprintf("unsupported operator: %q for types: %s, %s", expr.Token, leftType, rightType),
			}
			ts.Errors = append(ts.Errors, cerr)
			return []Type{Unresolved{}}
		}

		leftSym := &Symbol{
			Val:  llvm.Value{},
			Type: leftType,
		}
		rightSym := &Symbol{
			Val:  llvm.Value{},
			Type: rightType,
		}
		// compiler shouldn't matter as we are only inferring types
		// need to give compile flag as false to fn
		types = append(types, fn(ts.ScriptCompiler.Compiler, leftSym, rightSym, false).Type)
	}

	return
}

// TypePrefixExpression returns type of prefix expression output if input is not a pointer
// If input is a pointer then it applies prefix expression to the element pointed to
// This is because we automatically dereference if it's a pointer
func (ts *TypeSolver) TypePrefixExpression(expr *ast.PrefixExpression) (types []Type) {
	operand := ts.TypeExpression(expr.Right, false)
	types = []Type{}
	for _, opType := range operand {
		if ptr, yes := opType.(Pointer); yes {
			opType = ptr.Elem
		}

		// If either operand's type is unresolved, the result of the
		// operation is also unresolved. We can't proceed.
		if opType.Kind() == UnresolvedKind {
			types = append(types, Unresolved{})
			continue // Move to the next pair of operands
		}

		key := unaryOpKey{
			Operator:    expr.Operator,
			OperandType: opType.String(),
		}

		var fn unaryOpFunc
		var ok bool
		if fn, ok = defaultUnaryOps[key]; !ok {
			cerr := &token.CompileError{
				Token: expr.Token,
				Msg:   fmt.Sprintf("unsupported unary operator: %q for type: %s", expr.Operator, opType),
			}
			ts.Errors = append(ts.Errors, cerr)
		}

		opSym := &Symbol{
			Val:  llvm.Value{},
			Type: opType,
		}
		// compiler shouldn't matter as we are only inferring types
		// need to give compile flag as false to function
		types = append(types, fn(ts.ScriptCompiler.Compiler, opSym, false).Type)
	}

	return
}

// inside a call expression a Range becomes its interior type
func (ts *TypeSolver) TypeCallExpression(ce *ast.CallExpression, isRoot bool) (types []Type) {
	types = []Type{}
	args := []Type{}
	innerArgs := []Type{}
	for _, e := range ce.Arguments {
		exprArgs := ts.TypeExpression(e, isRoot)
		for _, arg := range exprArgs {
			if arg.Kind() == RangeKind {
				innerArgs = append(innerArgs, arg.(Range).Iter)
			} else {
				innerArgs = append(innerArgs, arg)
			}
			args = append(args, arg)
		}
	}

	fk := ast.FuncKey{
		FuncName: ce.Function.Value,
		Arity:    len(args),
	}

	compiler := ts.ScriptCompiler.Compiler
	code := compiler.CodeCompiler.Code
	template, yes := code.Func.Map[fk]
	if !yes {
		cerr := &token.CompileError{
			Token: ce.Token,
			Msg:   fmt.Sprintf("undefined function: %s", ce.Function.Value),
		}
		ts.Errors = append(ts.Errors, cerr)
		return
	}

	mangled := mangle(ce.Function.Value, args)
	return ts.InferFuncTypes(ce, innerArgs, mangled, template)
}

func (ts *TypeSolver) InferFuncTypes(ce *ast.CallExpression, args []Type, mangled string, template *ast.FuncStatement) []Type {
	// first check scriptCompiler compiler if that itself has function in its function (perhaps through a previous script compilation)
	f, ok := ts.ScriptCompiler.Compiler.FuncCache[mangled]
	if ok && f.AllTypesInferred() {
		return f.OutTypes
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
		return f.OutTypes
	}

	if ts.ScriptFunc == "" {
		return ts.TypeScriptFunc(mangled, template, f)
	}

	ts.TypeFunc(mangled, template, f)
	return f.OutTypes
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
