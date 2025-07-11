package compiler

import (
	"fmt"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

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
}

func NewTypeSolver(sc *ScriptCompiler) *TypeSolver {
	return &TypeSolver{
		ScriptCompiler: sc,
		Scopes:         []Scope[Type]{NewScope[Type](FuncScope)},
		InProgress:     map[string]struct{}{},
		ScriptFunc:     "",
		Converging:     false,
		Errors:         []*token.CompileError{},
	}
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
	for _, stmt := range program.Statements {
		ts.TypeStatement(stmt)
	}
}

func (ts *TypeSolver) TypePrintStatement(stmt *ast.PrintStatement) {
	for _, expr := range stmt.Expression {
		ts.TypeExpression(expr)
	}
}

func (ts *TypeSolver) TypeLetStatement(stmt *ast.LetStatement) {
	// type conditions in case there may be functions we have to type
	types := []Type{}
	for _, expr := range stmt.Value {
		types = append(types, ts.TypeExpression(expr)...)
	}

	if len(stmt.Name) != len(types) {
		ce := &token.CompileError{
			Token: stmt.Token,
			Msg:   fmt.Sprintf("Statement lhs identifiers are not equal to rhs values!!! lhs identifiers: %d. rhs values: %d. Stmt %q", len(stmt.Name), len(types), stmt.Token.Literal),
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

func (ts *TypeSolver) TypeRangeExpression(r *ast.RangeLiteral) []Type {
	// infer start and stop
	startT := ts.TypeExpression(r.Start)
	stopT := ts.TypeExpression(r.Stop)
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
		stepT := ts.TypeExpression(r.Step)
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
	// the whole expression is a Range of that integer type
	return []Type{Range{Iter: startT[0]}}
}

func (ts *TypeSolver) TypeExpression(expr ast.Expression) []Type {
	switch e := expr.(type) {
	case *ast.IntegerLiteral:
		return []Type{Int{Width: 64}}
	case *ast.FloatLiteral:
		return []Type{Float{Width: 64}}
	case *ast.StringLiteral:
		return []Type{Str{}}
	case *ast.RangeLiteral:
		return ts.TypeRangeExpression(e)
	case *ast.Identifier:
		return []Type{ts.TypeIdentifier(e)}
	case *ast.InfixExpression:
		return ts.TypeInfixExpression(e)
	case *ast.PrefixExpression:
		return ts.TypePrefixExpression(e)
	case *ast.CallExpression:
		return ts.TypeCallExpression(e)
	default:
		panic(fmt.Sprintf("unsupported expression type %T to infer type", e))
	}
}

// Type Identifier returns type of identifier if it is not a pointer
// If it is a pointer then returns type of the element it points to
// This is because we automatically dereference if it's a pointer
func (ts *TypeSolver) TypeIdentifier(ident *ast.Identifier) Type {
	if t, ok := Get(ts.Scopes, ident.Value); ok {
		if ptr, yes := t.(Pointer); yes {
			return ptr.Elem
		}
		return t
	}

	compiler := ts.ScriptCompiler.Compiler
	cc := compiler.CodeCompiler.Compiler
	if s, ok := Get(cc.Scopes, ident.Value); ok {
		t := s.Type
		if ptr, yes := t.(Pointer); yes {
			return ptr.Elem
		}
		return t
	}

	ce := &token.CompileError{
		Token: ident.Token,
		Msg:   fmt.Sprintf("variable not found in script or code files. Could not infer type. Undefined variable: %q", ident.Token.Literal),
	}
	ts.Errors = append(ts.Errors, ce)
	return Unresolved{}
}

// TypeInfixExpression returns output types of infix expression
// If either left or right operands are pointers, it will dereference them
// This is because pointers are automatically dereferenced
func (ts *TypeSolver) TypeInfixExpression(expr *ast.InfixExpression) []Type {
	left := ts.TypeExpression(expr.Left)
	right := ts.TypeExpression(expr.Right)
	if len(left) != len(right) {
		ce := &token.CompileError{
			Token: expr.Token,
			Msg:   fmt.Sprintf("left expression and right expression have unequal lengths! Left expr: %s, length: %d. Right expr: %s, length: %d. Operator: %q", expr.Left, len(left), expr.Right, len(right), expr.Token.Literal),
		}
		ts.Errors = append(ts.Errors, ce)
		return []Type{Unresolved{}}
	}

	res := []Type{}
	for i := range left {
		leftType := left[i]
		if ptr, ok := leftType.(Pointer); ok {
			leftType = ptr.Elem
		}

		rightType := right[i]
		if ptr, ok := rightType.(Pointer); ok {
			rightType = ptr.Elem
		}

		// If either operand's type is unresolved, the result of the
		// operation is also unresolved. We can't proceed.
		if leftType.Kind() == UnresolvedKind || rightType.Kind() == UnresolvedKind {
			res = append(res, Unresolved{})
			continue // Move to the next pair of operands
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
		res = append(res, fn(ts.ScriptCompiler.Compiler, leftSym, rightSym, false).Type)
	}

	return res
}

// TypePrefixExpression returns type of prefix expression output if input is not a pointer
// If input is a pointer then it applies prefix expression to the element pointed to
// This is because we automatically dereference if it's a pointer
func (ts *TypeSolver) TypePrefixExpression(expr *ast.PrefixExpression) []Type {
	operand := ts.TypeExpression(expr.Right)
	res := []Type{}
	for _, opType := range operand {
		if ptr, yes := opType.(Pointer); yes {
			opType = ptr.Elem
		}

		// If either operand's type is unresolved, the result of the
		// operation is also unresolved. We can't proceed.
		if opType.Kind() == UnresolvedKind {
			res = append(res, Unresolved{})
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
		res = append(res, fn(ts.ScriptCompiler.Compiler, opSym, false).Type)
	}

	return res
}

func (ts *TypeSolver) TypeCallExpression(ce *ast.CallExpression) []Type {
	args := []Type{}
	for _, e := range ce.Arguments {
		args = append(args, ts.TypeExpression(e)...)
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
	}

	mangled := mangle(ce.Function.Value, args)
	// first check scriptCompiler compiler if that itself has function in its function (perhaps through a previous script compilation)
	f, ok := ts.ScriptCompiler.Compiler.FuncCache[mangled]
	if ok && f.AllTypesInferred() {
		return f.Outputs
	}

	if !ok {
		f = &Func{
			Name:    ce.Function.Value,
			Params:  args,
			Outputs: []Type{},
		}
		for range template.Outputs {
			f.Outputs = append(f.Outputs, Unresolved{})
		}
		ts.ScriptCompiler.Compiler.FuncCache[mangled] = f
	}

	for _, arg := range args {
		if arg.Kind() == UnresolvedKind {
			if ts.ScriptFunc == "" {
				panic("Function in script called with unknown argument type. Func Name:" + f.Name)
			}
			return f.Outputs
		}
	}

	if ts.ScriptFunc == "" {
		return ts.TypeScriptFunc(mangled, template, f)
	}

	ts.TypeFunc(mangled, template, f)
	return f.Outputs
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
			panic(fmt.Sprintf("Inferring output types for function %s is not converging. Check for cyclic recursion and that each function has a base case", f.Name))
		}
		if f.AllTypesInferred() {
			return f.Outputs
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

	for _, stmt := range template.Body.Statements {
		ts.TypeStatement(stmt)
	}

	for i, id := range template.Outputs {
		outArg, ok := Get(ts.Scopes, id.Value)
		if !ok {
			panic(fmt.Sprintf("Should have either inferred type of output arg or marked it unresolved. Function: %s. output argument: %s", f.Name, id.Value))
		}

		oldOutArg := f.Outputs[i]
		if oldOutArg.Kind() != UnresolvedKind {
			continue
		}

		if outArg.Kind() != UnresolvedKind {
			ts.Converging = true
			f.Outputs[i] = outArg
			continue
		}
	}
}
