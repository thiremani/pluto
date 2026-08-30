package compiler

import (
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/parser"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

func solveScriptTypes(t *testing.T, ctx llvm.Context, cc *CodeCompiler, name, source string) *TypeSolver {
	t.Helper()
	sc := NewScriptCompiler(ctx, name, mustParseScript(t, source), cc)
	ts := NewTypeSolver(sc)
	ts.Solve()
	require.Empty(t, ts.Errors)
	return ts
}

func numberedNames(prefix string, count int) string {
	parts := make([]string, count)
	for i := range count {
		parts[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return strings.Join(parts, ", ")
}

func TestMutualRecursion(t *testing.T) {
	codeStr := `# define isEven: returns (x, y) = (is-even?, is-odd?)
x, y = isEven(n)
    # recursive step: if n≠0, flip the pair returned by isOdd(n-1)
    x, y = n != 0 isOdd(n - 1)
    # base case: 0 is even, not odd
    x = n == 1 "no"
    x = n == 0 "yes"

# define isOdd: returns (x, y) = (is-odd?, is-even?)
x, y = isOdd(n)
    # recursive step: if n≠0, flip the pair returned by isEven(n-1)
    x, y = n != 0 isEven(n - 1)
    # base case: 0 is not odd, but even
    y = n == 1 "no"
    y = n == 0 "yes"`

	l := lexer.New("TestMutualRecursionCode", codeStr)
	cp := parser.NewCodeParser(l)
	code := cp.Parse()
	require.Empty(t, cp.Errors())

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "test", "", code)
	require.Empty(t, cc.Compile())

	script := `x, y = isEven(3)
x, y`

	sl := lexer.New("TestMutualRecursionScript", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)

	stmt := program.Statements[0].(*ast.LetStatement)
	call := stmt.Value[0].(*ast.CallExpression)
	args := []Type{I64}
	template, isEvenMangled, ok := ts.lookupCallTemplate(call, args)
	require.True(t, ok)
	isEvenFunc := newFunc(call.Function.Value, args, template)
	cc.Compiler.FuncCache[isEvenMangled] = isEvenFunc
	require.Equal(t, []WriteEffect{WriteUncomputed, WriteUncomputed}, isEvenFunc.BodyOutputEffects)
	isOddMangled := Mangle(cc.Compiler.MangledPath, "isOdd", args)

	ts.Converging = false
	ts.firstUnresolved = nil
	clear(ts.walkedFuncs)
	require.True(t, ts.TypeFunc(isEvenMangled, template))
	require.Equal(t, []WriteEffect{WriteUncomputed, WriteUncomputed}, isEvenFunc.BodyOutputEffects)
	isOddFunc := cc.Compiler.FuncCache[isOddMangled]
	require.NotNil(t, isOddFunc)
	require.True(t, isEvenFunc.AllTypesInferred())
	require.Equal(t, StrKind, isEvenFunc.Sig.OutTypes[0].Kind())
	require.Equal(t, StrKind, isEvenFunc.Sig.OutTypes[1].Kind())
	require.False(t, isOddFunc.AllTypesInferred())
	require.Equal(t, UnresolvedKind, isOddFunc.Sig.OutTypes[0].Kind())
	require.Equal(t, StrKind, isOddFunc.Sig.OutTypes[1].Kind())
	require.NotNil(t, ts.firstUnresolved)
	require.True(t, ts.Converging)

	ts.Converging = false
	ts.firstUnresolved = nil
	clear(ts.walkedFuncs)
	require.True(t, ts.TypeFunc(isEvenMangled, template))
	require.True(t, isOddFunc.AllTypesInferred())
	require.Equal(t, StrKind, isOddFunc.Sig.OutTypes[0].Kind())
	require.Equal(t, StrKind, isOddFunc.Sig.OutTypes[1].Kind())
	require.Nil(t, ts.firstUnresolved)
	require.True(t, ts.Converging)

	ts.TypeScriptFunc(isEvenMangled, template, isEvenFunc)
	require.Empty(t, ts.Errors)
	require.False(t, ts.Converging)
	require.True(t, isEvenFunc.Settled)
	require.True(t, isOddFunc.Settled)
	require.Equal(t, []WriteEffect{MayWrite, MayWrite}, isEvenFunc.BodyOutputEffects)
	require.Equal(t, []WriteEffect{MayWrite, MayWrite}, isOddFunc.BodyOutputEffects)

	ts.Solve()
	require.Empty(t, ts.Errors)
	require.Len(t, cc.Compiler.FuncCache, 3)
}

func TestCycles(t *testing.T) {
	codeStr := `# define cyclic recursion
y = f(x)
    y = g(x)

y = g(x)
    y = h(x)

y = h(x)
    y = f(x)`

	l := lexer.New("TestCyclesCode", codeStr)
	cp := parser.NewCodeParser(l)
	code := cp.Parse()

	if errs := cp.Errors(); len(errs) > 0 {
		t.Error(strings.Join(errs, ","))
	}

	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "test", "", code)
	cc.Compile()

	script := `x = 6
y = f(x)
y`
	sl := lexer.New("TestCyclesScript", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	// The exact-key f -> g -> h -> f backedge allocates no new specialization,
	// so ordinary convergence analysis remains responsible even at this limit.
	ts.recLimit.maxFrames = 1
	ts.Solve()

	require.Len(t, ts.Errors, 1)
	// Any cycle member is valid blame, but the token and message must agree.
	blamed := ts.Errors[0].Token.Literal
	require.Contains(t, []string{"f", "g", "h"}, blamed)
	require.Contains(t, ts.Errors[0].Msg, "Function "+blamed+" is not converging. Check for cyclic recursion and that each function has a base case")
}

func TestNoBaseCase(t *testing.T) {
	codeStr := `# define cyclic recursion
y = f(x)
    y = f(x-1)
`

	l := lexer.New("TestNoBaseCaseCode", codeStr)
	cp := parser.NewCodeParser(l)
	code := cp.Parse()

	if errs := cp.Errors(); len(errs) > 0 {
		t.Error(strings.Join(errs, ","))
	}

	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "test", "", code)
	cc.Compile()

	script := `x = 6
y = f(x)
y`
	sl := lexer.New("TestNoBaseCaseScript", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	if len(ts.Errors) != 1 {
		t.Error("Expected a cyclic recursion error, but got none")
	}

	if !strings.Contains(ts.Errors[0].Msg, "Function f is not converging. Check for cyclic recursion and that each function has a base case") {
		t.Errorf("Expected cyclic recursion error, but got: %s", ts.Errors[0].Msg)
	}
}

func TestTypeStructLiteralCanonicalizesToSchema(t *testing.T) {
	code := mustParseCode(t, `p = Person
  : name age height
    "Tejas" 35 184.5
q = Person
  : age
    28
r = Person`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "canonicalStruct", "", code)
	require.Empty(t, cc.Compile())

	sc := NewScriptCompiler(ctx, t.Name(), &ast.Program{}, cc)
	ts := NewTypeSolver(sc)

	require.Len(t, code.Statements, 3, "expected canonical/full, subset, and empty struct statements")
	qStmt := code.Statements[1].(*ast.StructStatement)
	rStmt := code.Statements[2].(*ast.StructStatement)
	qType := ts.TypeStructLiteral(qStmt.Value)
	rType := ts.TypeStructLiteral(rStmt.Value)

	require.Empty(t, ts.Errors)
	require.Len(t, qType, 1)
	require.Len(t, rType, 1)

	schema, ok := cc.Compiler.StructCache["Person"]
	require.True(t, ok, "expected canonical Person schema")
	require.True(t, TypeEqual(*schema, qType[0]))
	require.True(t, TypeEqual(*schema, rType[0]))
	require.True(t, CanRefineType(qType[0], rType[0]))
}

func TestTypeStructLiteralValidatesAgainstCanonicalSchema(t *testing.T) {
	code := mustParseCode(t, `p = Person
  : name age
    "Tejas" 35`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "canonicalStructValidation", "", code)
	require.Empty(t, cc.Compile())

	sc := NewScriptCompiler(ctx, t.Name(), &ast.Program{}, cc)
	ts := NewTypeSolver(sc)

	lit := &ast.StructLiteral{
		Token:   token.Token{Type: token.IDENT, Literal: "Person"},
		Headers: []token.Token{{Type: token.IDENT, Literal: "age"}},
		Row: []ast.Expression{
			&ast.StringLiteral{Token: token.Token{Type: token.STRING, Literal: "Ada"}},
		},
	}

	got := ts.TypeStructLiteral(lit)
	require.Len(t, got, 1)
	require.Equal(t, UnresolvedKind, got[0].Kind())
	require.NotEmpty(t, ts.Errors)
	require.Contains(t, ts.Errors[0].Error(), `struct field "age" expects I64, got Str`)
}

func TestTypeStructLiteralWidensStringFieldsFromValues(t *testing.T) {
	code := mustParseCode(t, `p = Person
  : name age
    "Tejas" 35`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "structStringFieldWiden", "", code)
	require.Empty(t, cc.Compile())

	sc := NewScriptCompiler(ctx, t.Name(), &ast.Program{}, cc)
	ts := NewTypeSolver(sc)

	lit := &ast.StructLiteral{
		Token:   token.Token{Type: token.IDENT, Literal: "Person"},
		Headers: []token.Token{{Type: token.IDENT, Literal: "name"}},
		Row: []ast.Expression{
			&ast.InfixExpression{
				Token:    token.Token{Type: token.OPERATOR, Literal: token.SYM_CONCAT},
				Left:     &ast.StringLiteral{Token: token.Token{Type: token.STRING, Literal: "Ada"}},
				Operator: token.SYM_CONCAT,
				Right:    &ast.StringLiteral{Token: token.Token{Type: token.STRING, Literal: "!"}},
			},
		},
	}

	got := ts.TypeStructLiteral(lit)
	require.Len(t, got, 1)
	require.Empty(t, ts.Errors)

	structType, ok := got[0].(Struct)
	require.True(t, ok)

	nameIdx := structType.FieldIndex("name")
	require.GreaterOrEqual(t, nameIdx, 0)
	require.True(t, IsStrH(structType.Fields[nameIdx].Type), "provided heap string field should widen field type to StrH")

	dot := &ast.DotExpression{
		Token: token.Token{Type: token.PERIOD, Literal: "."},
		Left:  lit,
		Field: "name",
	}
	dotTypes := ts.TypeDotExpression(dot)
	require.Len(t, dotTypes, 1)
	require.True(t, IsStrH(dotTypes[0]), "dot access should reflect widened field flavor")
}

func TestCollectionTypeErrors(t *testing.T) {
	cases := []struct {
		name        string
		script      string
		expectError string
	}{
		{
			name:        "DuplicateTableHeader",
			script:      "table = [\n    :Name Name\n    \"Ada\" \"A\"\n]",
			expectError: `duplicate table column "Name"`,
		},
		{
			name:        "RaggedTableRow",
			script:      "table = [\n    :Name Score\n    \"Ada\"\n]",
			expectError: "bracket literal row 1 has 1 cells, expected 2",
		},
		{
			name:        "RangedTableCell",
			script:      "i = 0:3\ntable = [\n    :Value\n    i\n]",
			expectError: "table rows require statically sized cells",
		},
		{
			name:        "RaggedArrayRow",
			script:      "arr = [\n    1 0\n    0\n]",
			expectError: "bracket literal row 2 has 1 cells, expected 2",
		},
		{
			name:        "IndexEmptyArray",
			script:      "empty = []\nempty[0]",
			expectError: "cannot index an empty array without an element type",
		},
		{
			name:        "ArrayTypeStaysLockedAfterEmptyReset",
			script:      "arr = [1]\narr = []\narr = [1.5]",
			expectError: `cannot reassign type to identifier. Old Type: [I64]. New Type: [F64]. Identifier "arr"`,
		},
		{
			name:        "Rank2EmptyIsNotRank1Reset",
			script:      "arr = [1]\narr = [[]]",
			expectError: `cannot reassign type to identifier. Old Type: [I64]. New Type: [[Empty]]. Identifier "arr"`,
		},
		{
			name:        "StackRankMismatch",
			script:      "m = [[1 2] [[3 4]]]\nm",
			expectError: "cannot stack rank-1 and rank-2 arrays",
		},
		{
			name:        "StackShapeMismatch",
			script:      "m = [[1 2] [3 4 5]]\nm",
			expectError: "cannot stack arrays with shapes [2] and [3]",
		},
		{
			name:        "MixedScalarAndArrayCells",
			script:      "m = [[1 2] 3]\nm",
			expectError: "cannot mix scalar and array-valued cells in the same array literal",
		},
		{
			name:        "ConcatRankMismatch",
			script:      "flat = [1 2]\nnested = [[3 4] [5 6]]\njoined = flat ⊕ nested\njoined",
			expectError: "cannot concatenate arrays with different ranks: 1 and 2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := llvm.NewContext()
			defer ctx.Dispose()

			cc := NewCodeCompiler(ctx, tc.name, "", ast.NewCode())
			require.Empty(t, cc.Compile())

			sl := lexer.New(tc.name+".spt", tc.script)
			sp := parser.NewScriptParser(sl)
			program := sp.Parse()
			require.Empty(t, sp.Errors())

			sc := NewScriptCompiler(ctx, t.Name(), program, cc)
			ts := NewTypeSolver(sc)
			ts.Solve()

			require.Len(t, ts.Errors, 1)
			require.Contains(t, ts.Errors[0].Msg, tc.expectError)
		})
	}
}

func TestArrayExpressionsPreserveOwnTypes(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	program := mustParseScript(t, `empty = ([] + []) ⊕ ["x"]
mixed = [1] + [2.5]
locked = [1]
locked = []`)
	cc := NewCodeCompiler(ctx, "arrayOperandTypes", "", ast.NewCode())
	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()
	require.Empty(t, ts.Errors)

	emptyStmt := program.Statements[0].(*ast.LetStatement)
	emptyOuter := emptyStmt.Value[0].(*ast.InfixExpression)
	emptyInner := emptyOuter.Left.(*ast.InfixExpression)
	emptyInnerType := ts.ExprCache[key(ts.FuncNameMangled, emptyInner)].OutTypes[0].(Array)
	emptyOuterType := ts.ExprCache[key(ts.FuncNameMangled, emptyOuter)].OutTypes[0].(Array)
	require.Equal(t, EmptyKind, emptyInnerType.ElemType.Kind())
	require.Equal(t, StrKind, emptyOuterType.ElemType.Kind())

	mixedStmt := program.Statements[1].(*ast.LetStatement)
	mixed := mixedStmt.Value[0].(*ast.InfixExpression)
	mixedLeftType := ts.ExprCache[key(ts.FuncNameMangled, mixed.Left)].OutTypes[0].(Array)
	mixedRightType := ts.ExprCache[key(ts.FuncNameMangled, mixed.Right)].OutTypes[0].(Array)
	mixedType := ts.ExprCache[key(ts.FuncNameMangled, mixed)].OutTypes[0].(Array)
	require.Equal(t, IntKind, mixedLeftType.ElemType.Kind())
	require.Equal(t, FloatKind, mixedRightType.ElemType.Kind())
	require.Equal(t, FloatKind, mixedType.ElemType.Kind())

	resetStmt := program.Statements[3].(*ast.LetStatement)
	resetType := ts.ExprCache[key(ts.FuncNameMangled, resetStmt.Value[0])].OutTypes[0].(Array)
	bindingType := sc.Script.Root.Vars["locked"].(Array)
	require.Equal(t, EmptyKind, resetType.ElemType.Kind())
	require.Equal(t, IntKind, bindingType.ElemType.Kind())
}

func TestArrayConcatTypeErrors(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "arrayConcatErrors", "", ast.NewCode())

	cases := []struct {
		name        string
		script      string
		expectError string
	}{
		{
			name:        "StringPlusIntArray",
			script:      "arr1 = [\"foo\" \"bar\"]\narr2 = [1 2]\nres = arr1 + arr2",
			expectError: "unsupported operator",
		},
		{
			name:        "FloatPlusStringArray",
			script:      "arr1 = [1.5 2.5]\narr2 = [\"foo\"]\nres = arr1 + arr2",
			expectError: "unsupported operator",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sl := lexer.New(tc.name+".spt", tc.script)
			sp := parser.NewScriptParser(sl)
			program := sp.Parse()

			sc := NewScriptCompiler(ctx, t.Name(), program, cc)
			ts := NewTypeSolver(sc)
			ts.Solve()

			if len(ts.Errors) == 0 {
				t.Fatalf("expected type error for %s, but got none", tc.name)
			}
			last := ts.Errors[len(ts.Errors)-1]
			if !strings.Contains(last.Msg, tc.expectError) {
				t.Fatalf("error message %q does not contain %q", last.Msg, tc.expectError)
			}
		})
	}

	script := "arr1 = [1 2]\narr2 = [3.5 4.5]\nres = arr1 + arr2"
	sl := lexer.New("MixedNumericConcat.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	resType, ok := ts.GetIdentifier("res")
	if !ok {
		t.Fatalf("expected concatenation result type")
	}
	arrType, ok := resType.(Array)
	if !ok {
		t.Fatalf("expected array type, got %T", resType)
	}
	if arrType.ElemType.Kind() != FloatKind {
		t.Fatalf("expected float array result, got %s", arrType.ElemType.String())
	}
}

func TestArrayToScalarAssignmentError(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "arrayToScalar", "", ast.NewCode())

	script := "x = 1\nx = [2 3]"
	sl := lexer.New("arrayToScalar.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	if len(ts.Errors) == 0 {
		t.Fatalf("expected type error when assigning array to scalar")
	}
	last := ts.Errors[len(ts.Errors)-1]
	if !strings.Contains(last.Msg, "cannot reassign type") {
		t.Fatalf("unexpected error message: %q", last.Msg)
	}
}

func TestStringBindingInferencePreservesExprTypes(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "stringBindingInference", "", ast.NewCode())

	script := `a = "abc"
a = a ⊕ "d"`
	sl := lexer.New("StringBindingInference.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()
	require.Empty(t, ts.Errors, "unexpected solver errors: %v", ts.Errors)

	slotType, ok := ts.GetIdentifier("a")
	require.True(t, ok, "expected identifier a")
	require.True(t, IsStrH(slotType), "binding a should widen to StrH")

	bindingType, ok := sc.Script.Root.Vars["a"]
	require.True(t, ok, "expected recorded binding type for a")
	require.True(t, IsStrH(bindingType), "binding map should record StrH for a")

	firstStmt, ok := program.Statements[0].(*ast.LetStatement)
	require.True(t, ok)
	firstInfo := ts.ExprCache[key(ts.FuncNameMangled, firstStmt.Value[0])]
	require.NotNil(t, firstInfo)
	require.True(t, IsStrG(firstInfo.OutTypes[0]), "plain literal expression should remain StrG")

	secondStmt, ok := program.Statements[1].(*ast.LetStatement)
	require.True(t, ok)
	secondExpr, ok := secondStmt.Value[0].(*ast.InfixExpression)
	require.True(t, ok)
	secondInfo := ts.ExprCache[key(ts.FuncNameMangled, secondExpr)]
	require.NotNil(t, secondInfo)
	require.True(t, IsStrH(secondInfo.OutTypes[0]), "concat expression should remain StrH")
}

func TestMergeBindingSlotTypeIsMonotonic(t *testing.T) {
	headerOnly := Table{Columns: []TableColumn{
		{Name: "Name", ElemType: Empty{}},
		{Name: "Score", ElemType: Empty{}},
	}}
	concreteTable := Table{Columns: []TableColumn{
		{Name: "Name", ElemType: StrH{}},
		{Name: "Score", ElemType: I64},
	}}
	heapStruct := Struct{Name: "Person", Fields: []StructField{
		{Name: "name", Type: StrH{}},
		{Name: "age", Type: I64},
	}}
	staticStruct := Struct{Name: "Person", Fields: []StructField{
		{Name: "name", Type: StrG{}},
		{Name: "age", Type: I64},
	}}

	for _, tt := range []struct {
		name       string
		oldType    Type
		newType    Type
		mergedType Type
	}{
		{"string widens", StrG{}, StrH{}, StrH{}},
		{"string never narrows", StrH{}, StrG{}, StrH{}},
		{"empty array refines", Array{ElemType: Empty{}, Rank: 1}, Array{ElemType: I64, Rank: 1}, Array{ElemType: I64, Rank: 1}},
		{"empty array resets", Array{ElemType: I64, Rank: 2}, Array{ElemType: Empty{}, Rank: 1}, Array{ElemType: I64, Rank: 2}},
		{"header table refines", headerOnly, concreteTable, concreteTable},
		{"header table resets", concreteTable, headerOnly, concreteTable},
		{"struct field never narrows", heapStruct, staticStruct, heapStruct},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.True(t, bindingSlotCompatible(tt.oldType, tt.newType))
			merged := mergeBindingSlotType(tt.oldType, tt.newType)
			require.True(t, TypeEqual(tt.mergedType, merged), "got %s, want %s", merged, tt.mergedType)
			require.True(t, bindingSlotCompatible(merged, tt.newType))
			require.True(t, TypeEqual(merged, mergeBindingSlotType(merged, tt.newType)), "joining the same observation twice must be idempotent")
		})
	}

	require.False(t, bindingSlotCompatible(
		heapStruct,
		Struct{Name: "Person", Fields: []StructField{{Name: "name", Type: I64}, {Name: "age", Type: I64}}},
	))
}

func TestFunctionOutputBindingRejectsIncompatibleReassignment(t *testing.T) {
	code := mustParseCode(t, `res = Bad(k)
    res = k == 0 1
    res = k != 0 "later"
`)
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "incompatibleOutput", "", code)
	require.Empty(t, cc.Compile())

	sc := NewScriptCompiler(
		ctx,
		t.Name(),
		mustParseScript(t, "value = Bad(0)\nvalue"),
		cc,
	)
	ts := NewTypeSolver(sc)
	ts.Solve()

	require.Len(t, ts.Errors, 1)
	err := ts.Errors[0]
	require.Equal(t, `cannot reassign type to identifier. Old Type: I64. New Type: Str. Identifier "res"`, err.Msg)
	require.Equal(t, "res", err.Token.Literal)
	require.Equal(t, "test.pt:3:5", err.Token.Location())
}

func TestRangeBoundsCannotDependOnRangeValues(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "rangeBoundsDepend", "", ast.NewCode())

	cases := []struct {
		name        string
		script      string
		expectError string
	}{
		{
			name: "stop depends on range",
			script: `i = 0:3
j = 0:i`,
			expectError: "range stop cannot depend on range values in this scope",
		},
		{
			name: "start depends on range",
			script: `i = 0:3
j = i:5`,
			expectError: "range start cannot depend on range values in this scope",
		},
		{
			name: "step depends on range",
			script: `i = 0:3
j = 0:5:i`,
			expectError: "range step cannot depend on range values in this scope",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sl := lexer.New(tc.name+".spt", tc.script)
			sp := parser.NewScriptParser(sl)
			program := sp.Parse()

			sc := NewScriptCompiler(ctx, t.Name(), program, cc)
			ts := NewTypeSolver(sc)
			ts.Solve()

			if len(ts.Errors) == 0 {
				t.Fatalf("expected type error for %s, but got none", tc.name)
			}

			found := false
			for _, err := range ts.Errors {
				if strings.Contains(err.Msg, tc.expectError) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected error containing %q, got: %v", tc.expectError, ts.Errors)
			}
		})
	}
}

func TestArrayComparisonInValuePositionIsMask(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "arrayComparisonValue", "", ast.NewCode())

	script := "a = [1 2]\nb = [0 3]\nx = a > b"
	sl := lexer.New("arrayComparisonValue.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	require.Empty(t, ts.Errors, "unexpected solver errors: %v", ts.Errors)

	letStmt, ok := program.Statements[2].(*ast.LetStatement)
	require.True(t, ok)
	infix, ok := letStmt.Value[0].(*ast.InfixExpression)
	require.True(t, ok)

	info := ts.ExprCache[key(ts.FuncNameMangled, infix)]
	require.NotNil(t, info)
	require.Len(t, info.CompareModes, 1, "should have one compare mode entry")
	require.Equal(t, CondArray, info.CompareModes[0], "array comparison in value position should be tagged as element-wise mask (CondArray)")
}

func TestArrayConditionEmitsSingleDiagnostic(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "arrayConditionDiagnostic", "", ast.NewCode())

	script := "cond = [1 2]\nx = cond 5"
	sl := lexer.New("arrayConditionDiagnostic.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	require.Len(t, ts.Errors, 1, "array-valued statement condition should emit one diagnostic")
	require.Contains(t, ts.Errors[0].Msg, "statement condition must produce a scalar value, not an array")
}

func TestMixedArrayScalarStatementConditionRejected(t *testing.T) {
	ctx := llvm.NewContext()
	// MixSA returns (scalar, array); the array cell is the second slot, so the
	// gate's array cell is caught only by checking every cell, not just the first.
	// Such a gate would otherwise silently drop the array comparison at lowering.
	code := "s, arr = MixSA(x)\n    s = x\n    arr = [x x + 1]"
	cc := NewCodeCompiler(ctx, "mixedArrayStmtCond", "", mustParseCode(t, code))
	require.Empty(t, cc.Compile())

	script := "y = MixSA(5) > MixSA(3)  100"
	sl := lexer.New("mixedArrayStmtCond.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	found := false
	for _, err := range ts.Errors {
		if strings.Contains(err.Msg, "statement condition must produce a scalar value, not an array") {
			found = true
			break
		}
	}
	require.Truef(t, found, "expected array-cell rejection, got: %v", ts.Errors)
}

func TestChainedTupleComparisonTypes(t *testing.T) {
	ctx := llvm.NewContext()
	// Pair returns two values; a chained comparison over them (Pair < Pair > Pair)
	// resolves per slot — each slot chains like a single-value comparison — so
	// the solver accepts it and types both outputs.
	code := "p, q = Pair(x, y)\n    p = x\n    q = y"
	cc := NewCodeCompiler(ctx, "chainedTupleCmp", "", mustParseCode(t, code))
	require.Empty(t, cc.Compile())

	script := "a, b = Pair(5, 7) < Pair(4, 9) > Pair(2, 6)"
	sl := lexer.New("chainedTupleCmp.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	require.Emptyf(t, ts.Errors, "chained tuple comparison should type cleanly, got: %v", ts.Errors)
}

func TestInnerFallbackOrTupleComparisonTypes(t *testing.T) {
	ctx := llvm.NewContext()
	// A value-position || nested in a multi-return comparison operand
	// (Pair(5 > 2 || 7, 9) > Pair(1, 1)) resolves during extraction like any
	// other per-slot condition, so the solver accepts it.
	code := "p, q = Pair(x, y)\n    p = x\n    q = y"
	cc := NewCodeCompiler(ctx, "innerOrTupleCmp", "", mustParseCode(t, code))
	require.Empty(t, cc.Compile())

	script := "px, py = Pair(5 > 2 || 7, 9) > Pair(1, 1)"
	sl := lexer.New("innerOrTupleCmp.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	require.Emptyf(t, ts.Errors, "inner-|| tuple comparison should type cleanly, got: %v", ts.Errors)
}

func TestInnerAndOrInTupleComparisonTypes(t *testing.T) {
	ctx := llvm.NewContext()
	// A &&/|| composition nested in a multi-return comparison operand
	// (Pair((1 > 0 && 0 > 1) || 7, 9) > Pair(1, 1)) resolves during extraction
	// like any other per-slot condition, so the solver accepts it.
	code := "p, q = Pair(x, y)\n    p = x\n    q = y"
	cc := NewCodeCompiler(ctx, "innerAndOrTupleCmp", "", mustParseCode(t, code))
	require.Empty(t, cc.Compile())

	script := "px, py = Pair(1 > 0 && 0 > 1 || 7, 9) > Pair(1, 1)"
	sl := lexer.New("innerAndOrTupleCmp.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	require.Emptyf(t, ts.Errors, "inner &&/|| tuple comparison should type cleanly, got: %v", ts.Errors)
}

func TestFunctionBodyRejectionEmittedOnce(t *testing.T) {
	ctx := llvm.NewContext()
	// Function bodies are re-typed on every solver fixpoint pass; a rejection
	// inside one must be emitted exactly once — not duplicated per pass, and
	// with no spurious "not converging" cascade.
	code := "r = BadOr(x)\n    r = x || 2"
	cc := NewCodeCompiler(ctx, "badOrFunc", "", mustParseCode(t, code))
	require.Empty(t, cc.Compile())

	script := "m = BadOr(5)\n\"-m\""
	sl := lexer.New("badOrFunc.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	require.Lenf(t, ts.Errors, 1, "function-body rejection should emit exactly one diagnostic, got: %v", ts.Errors)
	require.Contains(t, ts.Errors[0].Msg, "logical OR in value position requires a conditional left operand")
}

func TestScalarConditionEmitsTypeDiagnostic(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "scalarConditionDiagnostic", "", ast.NewCode())

	script := "cond = 5\nx = cond [1]"
	sl := lexer.New("scalarConditionDiagnostic.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	require.Len(t, ts.Errors, 1, "scalar-valued statement condition should emit one diagnostic")
	require.Contains(t, ts.Errors[0].Msg, "statement condition must be a comparison or bare range/array-selection driver, got I64")
}

func TestLogicalAndDiagnostics(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cases := []struct {
		name        string
		code        string
		script      string
		expectError string
	}{
		{
			// The left of a value-position && must be able to fail, or the
			// gate is dead.
			name:        "LeftMustBeFailable",
			script:      "n = 5\nx = n && 10",
			expectError: "logical AND in value position requires a conditional left operand",
		},
		{
			// An always-yielding || (a > 0 || a always yields a value) cannot
			// gate a &&.
			name:        "UnfailableOrLeftRejected",
			script:      "a = 1\nx = (a > 0 || a) && 7",
			expectError: "logical AND in value position requires a conditional left operand",
		},
		{
			// Wrapping an always-yielding || in arithmetic does not make it
			// failable.
			name:        "WrappedUnfailableOrLeftRejected",
			script:      "a = 1\nx = ((a > 0 || a) + 1) && 7",
			expectError: "logical AND in value position requires a conditional left operand",
		},
		{
			// An array lane is a mask (a value, not a boolean), so it cannot
			// gate: folding or zipping would silently ignore it — the same
			// rule anyArrayCell enforces for statement gates.
			name:        "MixedArrayLaneRejected",
			code:        "s, arr = MixSA(x)\n    s = x\n    arr = [x x + 1]",
			script:      "y = MixSA(5) > MixSA(3) && 3",
			expectError: "logical AND condition must produce scalar values, not an array",
		},
		{
			// A && is a valid failable left operand of a value-position ||;
			// the fallback's type must still match the yielded value's.
			name:        "AndOrFallbackTypeMismatch",
			script:      "a = 1\nx = a > 0 && 10 || \"s\"",
			expectError: "logical OR value operands must have matching output types, got I64 and Str",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			codeAST := ast.NewCode()
			if strings.TrimSpace(tc.code) != "" {
				codeAST = mustParseCode(t, tc.code)
			}
			cc := NewCodeCompiler(ctx, tc.name, "", codeAST)
			require.Empty(t, cc.Compile())

			sl := lexer.New(tc.name+".spt", tc.script)
			sp := parser.NewScriptParser(sl)
			program := sp.Parse()
			require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

			sc := NewScriptCompiler(ctx, t.Name(), program, cc)
			ts := NewTypeSolver(sc)
			ts.Solve()

			found := false
			for _, err := range ts.Errors {
				if strings.Contains(err.Msg, tc.expectError) {
					found = true
					break
				}
			}
			require.Truef(t, found, "expected error containing %q, got: %v", tc.expectError, ts.Errors)
		})
	}
}

func TestLogicalOrDiagnostics(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cases := []struct {
		name        string
		code        string
		script      string
		expectError string
	}{
		{
			name:        "ValueOrRequiresConditionalLeft",
			script:      "x = 1 || 2",
			expectError: "logical OR in value position requires a conditional left operand",
		},
		{
			name:        "FallbackTypesMustMatch",
			script:      "a = 1\nx = a > 0 || \"fallback\"",
			expectError: "logical OR value operands must have matching output types, got I64 and Str",
		},
		{
			name:        "UnfailableFallbackLeftRejected",
			script:      "a = 1\nx = (a > 0 || a) || 7",
			expectError: "logical OR in value position requires a conditional left operand",
		},
		{
			name:        "WrappedUnfailableFallbackLeftRejected",
			script:      "a = 1\nx = ((a > 0 || a) + 1) || 7",
			expectError: "logical OR in value position requires a conditional left operand",
		},
		{
			// Conditions are value-position now, so `a > 0 || b` is a fallback whose
			// `b` arm always yields — the gate can never fail, which is rejected.
			name:        "ConditionWithUnconditionalFallbackCannotGate",
			script:      "a = 1\nb = 2\nx = a > 0 || b 7",
			expectError: "statement condition can never fail",
		},
		{
			// Same, but deeper: only the final arm of the || chain is unconditional,
			// so the whole chain still always yields and cannot gate.
			name:        "DeepOrChainUnconditionalFinalArmCannotGate",
			script:      "a = 1\nb = 2\nc = 3\nx = a > 0 || b > 5 || c 7",
			expectError: "statement condition can never fail",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			codeAST := ast.NewCode()
			if strings.TrimSpace(tc.code) != "" {
				codeAST = mustParseCode(t, tc.code)
			}
			cc := NewCodeCompiler(ctx, tc.name, "", codeAST)
			require.Empty(t, cc.Compile())

			sl := lexer.New(tc.name+".spt", tc.script)
			sp := parser.NewScriptParser(sl)
			program := sp.Parse()
			require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

			sc := NewScriptCompiler(ctx, t.Name(), program, cc)
			ts := NewTypeSolver(sc)
			ts.Solve()

			found := false
			for _, err := range ts.Errors {
				if strings.Contains(err.Msg, tc.expectError) {
					found = true
					break
				}
			}
			require.Truef(t, found, "expected error containing %q, got: %v", tc.expectError, ts.Errors)
		})
	}
}

func TestScalarArrayComparisonInValuePositionIsMask(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "scalarArrayComparisonValue", "", ast.NewCode())

	script := "a = [1 2]\nx = 3 > a"
	sl := lexer.New("scalarArrayComparisonValue.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	require.Empty(t, ts.Errors, "unexpected solver errors: %v", ts.Errors)

	letStmt, ok := program.Statements[1].(*ast.LetStatement)
	require.True(t, ok)
	infix, ok := letStmt.Value[0].(*ast.InfixExpression)
	require.True(t, ok)

	info := ts.ExprCache[key(ts.FuncNameMangled, infix)]
	require.NotNil(t, info)
	require.Len(t, info.CompareModes, 1, "should have one compare mode entry")
	require.Equal(t, CondArray, info.CompareModes[0], "scalar-array comparison in value position should be tagged as element-wise mask (CondArray)")

	outArr, ok := info.OutTypes[0].(Array)
	require.True(t, ok, "expected scalar-array mask output type to be array")
	require.Equal(t, IntKind, outArr.ElemType.Kind(), "scalar-array mask should keep scalar LHS element type")
}

func TestArrayLiteralRangesRecording(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "arrayLiteralRanges", "", ast.NewCode())

	script := `idx = 0:5
res = [idx]`

	sl := lexer.New("ArrayLiteralRanges", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()
	require.Empty(t, ts.Errors)

	letStmt, ok := program.Statements[1].(*ast.LetStatement)
	require.True(t, ok)

	arrLit, ok := letStmt.Value[0].(*ast.ArrayLiteral)
	require.True(t, ok)

	info := ts.ExprCache[key(ts.FuncNameMangled, arrLit)]
	require.NotNil(t, info)
	require.Empty(t, info.Ranges)
	require.Len(t, info.CollectRanges, 1)
	require.NotNil(t, info.Rewrite)
	require.IsType(t, &ast.ArrayLiteral{}, info.Rewrite)
}

func TestBareRangeAssignmentsCopyDescriptors(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "bareRangeCopies", "", ast.NewCode())
	program := mustParseScript(t, `source = 0:5
copy = (source)
last = source + 0
outer = 0:2
gatedCopy = outer < 2 source
filtered = source > 2 source`)

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()
	require.Emptyf(t, ts.Errors, "unexpected type errors: %v", ts.Errors)

	for _, name := range []string{"source", "copy", "gatedCopy"} {
		typ, ok := ts.GetIdentifier(name)
		require.Truef(t, ok, "expected %s binding", name)
		require.Equal(t, Range{Iter: I64}, typ)
	}
	for _, name := range []string{"last", "filtered"} {
		typ, ok := ts.GetIdentifier(name)
		require.Truef(t, ok, "expected %s binding", name)
		require.Equal(t, I64, typ)
	}

	copyExpr := program.Statements[1].(*ast.LetStatement).Value[0]
	copyInfo := ts.ExprCache[key(ts.FuncNameMangled, copyExpr)]
	require.False(t, copyInfo.HasRanges)
	require.Empty(t, copyInfo.Ranges)
	require.Nil(t, copyInfo.Rewrite)

	gatedCopyExpr := program.Statements[4].(*ast.LetStatement).Value[0]
	gatedCopyInfo := ts.ExprCache[key(ts.FuncNameMangled, gatedCopyExpr)]
	require.Equal(t, []Type{Range{Iter: I64}}, gatedCopyInfo.OutTypes)
	require.Len(t, gatedCopyInfo.Ranges, 1)
	require.Equal(t, "outer", gatedCopyInfo.Ranges[0].Name)

	filteredExpr := program.Statements[5].(*ast.LetStatement).Value[0]
	filteredInfo := ts.ExprCache[key(ts.FuncNameMangled, filteredExpr)]
	require.Equal(t, []Type{I64}, filteredInfo.OutTypes)
	require.Len(t, filteredInfo.Ranges, 1)
	require.Equal(t, "source", filteredInfo.Ranges[0].Name)
}

func TestRangedArrayAccessTypesAsElementStream(t *testing.T) {
	ctx := llvm.NewContext()
	code := ast.NewCode()
	cc := NewCodeCompiler(ctx, "rangedArrayAccessTyping", "", code)
	cc.Compile()

	script := "arr = [1 2 3]\nvalue = arr[0:2]\nsum = 0\nsum = sum + arr[0:2]"
	sl := lexer.New("RangedArrayAccessTyping.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	valueType, ok := ts.GetIdentifier("value")
	require.True(t, ok, "expected value identifier")
	value, ok := valueType.(Int)
	require.Truef(t, ok, "expected ranged access to finalize as Int, got %T", valueType)
	require.EqualValues(t, 64, value.Width)

	valueStmt := program.Statements[1].(*ast.LetStatement)
	valueExpr := valueStmt.Value[0].(*ast.ArrayRangeExpression)
	valueInfo := ts.ExprCache[key(ts.FuncNameMangled, valueExpr)]
	require.Equal(t, []Type{Int{Width: 64}}, valueInfo.OutTypes)
	require.Len(t, valueInfo.Ranges, 1)

	sumType, ok := ts.GetIdentifier("sum")
	require.True(t, ok, "expected sum identifier")
	sumInt, ok := sumType.(Int)
	require.Truef(t, ok, "expected sum to be Int, got %T", sumType)
	require.EqualValues(t, 64, sumInt.Width)
}

func TestImmediateArraySelectionUsesCallScopedArrayRange(t *testing.T) {
	ctx := llvm.NewContext()
	code := mustParseCode(t, `out = Identity(x)
    out = x`)
	cc := NewCodeCompiler(ctx, "callScopedArrayRange", "", code)
	require.Empty(t, cc.Compile())

	program := mustParseScript(t, `i = 0:2
arr = [1 2 3]
value = Identity(arr[i])
arr[i]`)
	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()
	require.Emptyf(t, ts.Errors, "unexpected type errors: %v", ts.Errors)

	valueStmt := program.Statements[2].(*ast.LetStatement)
	call := valueStmt.Value[0].(*ast.CallExpression)
	callInfo := ts.ExprCache[key(ts.FuncNameMangled, call)]
	require.True(t, callInfo.LoopInside)
	require.Equal(t, []Type{I64}, callInfo.ScalarCallParamTypes)
	require.Len(t, callInfo.CallParamTypes, 1)

	arrayRange, ok := callInfo.CallParamTypes[0].(ArrayRange)
	require.Truef(t, ok, "expected call-only ArrayRange, got %T", callInfo.CallParamTypes[0])
	require.Equal(t, Array{ElemType: I64, Rank: 1}, arrayRange.Array)
	require.Equal(t, Range{Iter: I64}, arrayRange.Range)

	selection := call.Arguments[0].(*ast.ArrayRangeExpression)
	require.Equal(t, []Type{I64}, ts.ExprCache[key(ts.FuncNameMangled, selection)].OutTypes,
		"the source expression must remain element-typed outside the call ABI")
	valueType, ok := ts.GetIdentifier("value")
	require.True(t, ok)
	require.Equal(t, I64, valueType)

	printCall := program.Statements[3].(*ast.PrintStatement).Expression
	printInfo := ts.ExprCache[key(ts.FuncNameMangled, printCall)]
	require.False(t, printInfo.LoopInside, "print must consume the selection at the caller")
	require.Equal(t, []Type{I64}, printInfo.CallParamTypes)
}

func TestArrayIndexRejectsI1(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "arrayIndexI1", "", ast.NewCode())

	script := "arr = [1 2 3]\nvalue = arr[idx]"
	sl := lexer.New("ArrayIndexRejectsI1.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	Put(ts.Scopes, "idx", Type(Int{Width: 1}))
	ts.Solve()

	require.NotEmpty(t, ts.Errors, "expected type error for I1 array index")

	found := false
	for _, err := range ts.Errors {
		if strings.Contains(err.Msg, "array index cannot be I1") {
			found = true
			break
		}
	}
	require.True(t, found, "expected I1 array index error, got: %v", ts.Errors)
}

func TestArrayIndexAllowsWiderIntKinds(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "arrayIndexI32", "", ast.NewCode())

	script := "arr = [1 2 3]\nvalue = arr[idx]"
	sl := lexer.New("ArrayIndexAllowsWiderIntKinds.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	Put(ts.Scopes, "idx", Type(Int{Width: 32}))
	ts.Solve()
	require.Empty(t, ts.Errors, "unexpected solver errors for wider integer index: %v", ts.Errors)
}

func TestArrayRangeIndexRequiresI64Iter(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "arrayRangeIndexI64", "", ast.NewCode())

	script := "arr = [1 2 3]\nvalue = arr[idx]"
	sl := lexer.New("ArrayRangeIndexRequiresI64Iter.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	Put(ts.Scopes, "idx", Type(Range{Iter: Int{Width: 1}}))
	ts.Solve()

	require.NotEmpty(t, ts.Errors, "expected type error for non-I64 array range index iterator")

	found := false
	for _, err := range ts.Errors {
		if strings.Contains(err.Msg, "range-valued array index expects an I64 iterator") {
			found = true
			break
		}
	}
	require.True(t, found, "expected I64 array range iterator error, got: %v", ts.Errors)
}

func TestPrefixRewriteCopiesOutTypes(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "prefixRewriteCopy", "", ast.NewCode())

	script := "x = -(0:3)"
	sl := lexer.New("PrefixRewriteCopy.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()
	require.Empty(t, ts.Errors, "unexpected solver errors: %v", ts.Errors)

	letStmt, ok := program.Statements[0].(*ast.LetStatement)
	require.True(t, ok)
	prefix, ok := letStmt.Value[0].(*ast.PrefixExpression)
	require.True(t, ok)

	origInfo := ts.ExprCache[key(ts.FuncNameMangled, prefix)]
	require.NotNil(t, origInfo)
	rewPrefix, ok := origInfo.Rewrite.(*ast.PrefixExpression)
	require.True(t, ok, "expected rewritten prefix expression")

	rewInfo := ts.ExprCache[key(ts.FuncNameMangled, rewPrefix)]
	require.NotNil(t, rewInfo)
	require.NotEmpty(t, origInfo.OutTypes)
	require.NotEmpty(t, rewInfo.OutTypes)

	origBefore := origInfo.OutTypes[0]
	rewInfo.OutTypes[0] = Float{Width: 64}
	require.Equal(t, origBefore, origInfo.OutTypes[0], "rewritten prefix OutTypes must not alias original")
}

// TmpCounter observes leaf walks because the leaf contains one range literal.
func closureLeafWalks(t *testing.T, depth, callSites int) int {
	t.Helper()

	var b strings.Builder
	for i := range depth {
		fmt.Fprintf(&b, "res = F%d(k)\n    a = F%d(k)\n    b = F%d(k + 1)\n    res = a + b\n\n", i, i+1, i+1)
	}
	fmt.Fprintf(&b, "res = F%d(k)\n    i = 0:2\n    res = k + i\n", depth)

	l := lexer.New("TestFuncClosureCode", b.String())
	cp := parser.NewCodeParser(l)
	code := cp.Parse()
	require.Empty(t, cp.Errors())

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "test", "", code)
	require.Empty(t, cc.Compile())

	var script strings.Builder
	for i := range callSites {
		fmt.Fprintf(&script, "v%d = F0(%d)\nv%d\n", i, i, i)
	}
	sl := lexer.New("TestFuncClosureScript", script.String())
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	// An acyclic chain never starts a recursive specialization region, regardless
	// of its call depth or the recursive-region test limit.
	ts.recLimit.maxFrames = 1
	ts.Solve()

	require.Empty(t, ts.Errors)
	require.Len(t, cc.Compiler.FuncCache, depth+2)
	return ts.TmpCounter
}

func TestFuncClosureWalksEachSpecializationOnce(t *testing.T) {
	shallow := closureLeafWalks(t, 8, 1)
	require.Positive(t, shallow, "TmpCounter must still track leaf body walks")

	deep := closureLeafWalks(t, 16, 1)
	require.Equal(t, shallow, deep, "leaf walks must not grow with call-graph depth")

	repeated := closureLeafWalks(t, 8, 8)
	require.Equal(t, shallow, repeated, "leaf walks must not grow with script call sites")
}

func TestRecursiveGrowthLimitReportsActiveChain(t *testing.T) {
	const testLimit = 3

	for _, tt := range []struct {
		name       string
		code       string
		script     string
		chainStart string
	}{
		{
			name: "direct",
			code: `res = Grow(x)
    res = Grow([x])
`,
			script:     "v = Grow(1)\nv",
			chainStart: "Grow(I64) -> Grow(Array_t1_I64)",
		},
		{
			name: "mutual",
			code: `res = Left(x)
    res = Right([x])

res = Right(x)
    res = Left(x)
`,
			script:     "v = Left(1)\nv",
			chainStart: "Left(I64) -> Right(Array_t1_I64) -> Left(Array_t1_I64)",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := llvm.NewContext()
			defer ctx.Dispose()
			cc := NewCodeCompiler(ctx, t.Name(), "", mustParseCode(t, tt.code))
			require.Empty(t, cc.Compile())

			sc := NewScriptCompiler(ctx, t.Name(), mustParseScript(t, tt.script), cc)
			ts := NewTypeSolver(sc)
			ts.recLimit.maxFrames = testLimit
			ts.Solve()

			require.Len(t, ts.Errors, 1)
			require.Contains(t, ts.Errors[0].Msg,
				fmt.Sprintf("recursive specialization resource limit exceeded (limit %d active specialization frames in one recursive inference region)", testLimit))
			require.Contains(t, ts.Errors[0].Msg, tt.chainStart)

			cacheEntriesBeforeRetry := len(cc.Compiler.FuncCache)
			retrySC := NewScriptCompiler(ctx, t.Name()+"Retry", mustParseScript(t, tt.script), cc)
			retryTS := NewTypeSolver(retrySC)
			retryTS.recLimit.maxFrames = testLimit
			retryTS.Solve()

			require.Len(t, retryTS.Errors, 1)
			require.Contains(t, retryTS.Errors[0].Msg,
				fmt.Sprintf("recursive specialization resource limit exceeded (limit %d active specialization frames in one recursive inference region)", testLimit))
			require.Len(t, cc.Compiler.FuncCache, cacheEntriesBeforeRetry+1,
				"a retry may add its script root but must not ratchet the rejected function closure")
		})
	}
}

func TestRecursiveFailureStopsSiblingRewalks(t *testing.T) {
	const testLimit = 3

	code := mustParseCode(t, `res = Grow(x)
    res = Grow([x])
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, t.Name(), "", code)
	require.Empty(t, cc.Compile())

	script := "value = Grow(1) + Grow([1]) + Grow([[1]]) + Grow([[[1]]])\nvalue"
	sc := NewScriptCompiler(ctx, t.Name(), mustParseScript(t, script), cc)
	ts := NewTypeSolver(sc)
	ts.recLimit.maxFrames = testLimit
	ts.Solve()

	require.Len(t, ts.Errors, 1, "later calls in the statement must not rewalk the failed unsettled closure")
	require.Contains(t, ts.Errors[0].Msg, "recursive specialization resource limit exceeded")
	require.NotContains(t, ts.Errors[0].Msg, "not converging")
}

func growingMutualCycleCode(templateCount int) string {
	var code strings.Builder
	for index := range templateCount {
		next := (index + 1) % templateCount
		argument := "x"
		if next == 0 {
			argument = "[x]"
		}
		fmt.Fprintf(&code, "res = Cycle%d(x)\n    res = Cycle%d(%s)\n\n", index, next, argument)
	}

	return code.String()
}

func TestMutualGrowthSharesRecursiveLimit(t *testing.T) {
	const templateCount = 6
	const testLimit = 10

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "wideMutualGrowth", "", mustParseCode(t, growingMutualCycleCode(templateCount)))
	require.Empty(t, cc.Compile())

	sc := NewScriptCompiler(ctx, t.Name(), mustParseScript(t, "value = Cycle0(1)\nvalue"), cc)
	ts := NewTypeSolver(sc)
	ts.recLimit.maxFrames = testLimit
	ts.Solve()

	require.Len(t, ts.Errors, 1)
	require.Contains(t, ts.Errors[0].Msg,
		fmt.Sprintf("recursive specialization resource limit exceeded (limit %d active specialization frames in one recursive inference region)", testLimit))
	require.Contains(t, ts.Errors[0].Msg, "Cycle0(I64)")
	require.LessOrEqual(t, len(cc.Compiler.FuncCache), testLimit+1,
		"the recursive-region limit must not multiply by the number of templates in the cycle")
}

func traceSpecializationFrame(name string, typ Type) specializationFrame {
	template := &ast.FuncStatement{Token: token.Token{Literal: name}}

	return specializationFrame{
		mangled:  Mangle(MangleDirPath("trace", ""), name, []Type{typ}),
		template: template,
	}
}

func TestSpecializationTraceIsBounded(t *testing.T) {
	active := make([]specializationFrame, 20)
	for i := range active {
		active[i] = traceSpecializationFrame(fmt.Sprintf("F%d", i), I64)
	}

	trace := formatSpecializationChain(active, traceSpecializationFrame("F20", I64))
	require.Contains(t, trace, "F0(I64)")
	require.Contains(t, trace, "13 specializations omitted")
	require.NotContains(t, trace, "F1(I64)")
	require.Contains(t, trace, "F20(I64)")
}

func TestSpecializationTracePluralizesOmissions(t *testing.T) {
	active := make([]specializationFrame, 8)
	for i := range active {
		active[i] = traceSpecializationFrame(fmt.Sprintf("F%d", i), I64)
	}

	trace := formatSpecializationChain(active, traceSpecializationFrame("F8", I64))
	require.NotContains(t, trace, "omitted")
	require.Equal(t, 8, strings.Count(trace, " -> "))
	require.Equal(t, "... 1 specialization omitted ...", specializationOmission(1))
	require.Equal(t, "... 2 specializations omitted ...", specializationOmission(2))
}

func TestSpecializationTraceCapsIndividualFrames(t *testing.T) {
	name := strings.Repeat("LongName", maxSpecializationFrameRunes)
	display := specializationDisplay(traceSpecializationFrame(name, I64))

	require.LessOrEqual(t, len([]rune(display)), maxSpecializationFrameRunes)
	require.True(t, strings.HasSuffix(display, "..."))
}

const fixedRankRecursionSource = `res = FixedRank(x)
    "-x"
    res = 0
    nested = FixedRank([[1]])
    res = res + nested
`

func TestRecursiveGrowthReachesFixedClosure(t *testing.T) {
	code := mustParseCode(t, fixedRankRecursionSource)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "fixedRankRecursion", "", code)
	require.Empty(t, cc.Compile())

	solveScriptTypes(t, ctx, cc, t.Name(), "v = FixedRank(1)\nv")
	require.Contains(t, cc.Compiler.FuncCache, Mangle(cc.Compiler.MangledPath, "FixedRank", []Type{I64}))
	require.Contains(t, cc.Compiler.FuncCache, Mangle(cc.Compiler.MangledPath, "FixedRank", []Type{
		Array{ElemType: I64, Rank: 2},
	}))
}

func TestRecursiveLimitCountsColdDiscovery(t *testing.T) {
	const testLimit = 1

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	coldCC := NewCodeCompiler(ctx, "fixedRankColdLimit", "", mustParseCode(t, fixedRankRecursionSource))
	require.Empty(t, coldCC.Compile())
	coldSC := NewScriptCompiler(ctx, t.Name()+"Cold", mustParseScript(t, "value = FixedRank(1)\nvalue"), coldCC)
	coldTS := NewTypeSolver(coldSC)
	coldTS.recLimit.maxFrames = testLimit
	coldTS.Solve()
	require.Len(t, coldTS.Errors, 1, "a cold type-changing re-entry consumes recursive discovery work")
	require.Contains(t, coldTS.Errors[0].Msg, "recursive specialization resource limit exceeded")

	warmCC := NewCodeCompiler(ctx, "fixedRankWarmLimit", "", mustParseCode(t, fixedRankRecursionSource))
	require.Empty(t, warmCC.Compile())
	tailSC := NewScriptCompiler(ctx, t.Name()+"Tail", mustParseScript(t, "value = FixedRank([[1]])\nvalue"), warmCC)
	tailTS := NewTypeSolver(tailSC)
	tailTS.recLimit.maxFrames = testLimit
	tailTS.Solve()
	require.Empty(t, tailTS.Errors)

	tailMangled := Mangle(warmCC.Compiler.MangledPath, "FixedRank", []Type{
		Array{ElemType: I64, Rank: 2},
	})
	require.Contains(t, warmCC.Compiler.FuncCache, tailMangled)
	require.True(t, warmCC.Compiler.FuncCache[tailMangled].Settled)

	warmSC := NewScriptCompiler(ctx, t.Name()+"Warm", mustParseScript(t, "value = FixedRank(1)\nvalue"), warmCC)
	warmTS := NewTypeSolver(warmSC)
	warmTS.recLimit.maxFrames = testLimit
	warmTS.Solve()
	require.Empty(t, warmTS.Errors,
		"a settled tail consumes no cold specialization-discovery work")
}

func TestFinitePolymorphicRecursionIsAccepted(t *testing.T) {
	code := mustParseCode(t, `res = Outer(x)
    res = 0
    inner = Inner([x])
    res = res + inner

res = Inner(xs)
    res = 0
    outer = Outer(xs[0])
    res = res + outer
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "finitePolymorphicRecursion", "", code)
	require.Empty(t, cc.Compile())

	solveScriptTypes(t, ctx, cc, t.Name(), "v = Outer(1)\nv")
	require.Contains(t, cc.Compiler.FuncCache, Mangle(cc.Compiler.MangledPath, "Outer", []Type{I64}))
	require.Contains(t, cc.Compiler.FuncCache, Mangle(cc.Compiler.MangledPath, "Inner", []Type{
		Array{ElemType: I64, Rank: 1},
	}))
}

func TestRecursiveLimitPrecedesCacheAllocation(t *testing.T) {
	code := mustParseCode(t, `res = Identity(x)
    res = x
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "recursiveSpecializationLimit", "", code)
	require.Empty(t, cc.Compile())

	program := mustParseScript(t, "v = Identity(1)\nv")
	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)

	call := program.Statements[0].(*ast.LetStatement).Value[0].(*ast.CallExpression)
	template, mangled, ok := ts.lookupCallTemplate(call, []Type{I64})
	require.True(t, ok)
	ts.recLimit.maxFrames = 3
	for rank := 1; rank <= ts.recLimit.maxFrames; rank++ {
		ts.recLimit.push(specializationFrame{
			mangled: Mangle(cc.Compiler.MangledPath, "Identity", []Type{
				Array{ElemType: I64, Rank: rank},
			}),
			template: template,
		})
	}
	ts.InferFuncTypes(call, []Type{I64}, mangled, template)

	require.Len(t, ts.Errors, 1)
	require.Contains(t, ts.Errors[0].Msg,
		"recursive specialization resource limit exceeded (limit 3 active specialization frames in one recursive inference region)")
	require.Contains(t, ts.Errors[0].Msg, "active signature chain: Identity(Array_t1_I64)")
	require.Contains(t, ts.Errors[0].Msg, "Identity(I64)")
	require.Equal(t, call.Function.Token, ts.Errors[0].Token)
	require.Equal(t, "Identity", ts.Errors[0].Token.Literal)
	require.Equal(t, call.Function.Token.Location(), ts.Errors[0].Token.Location())
	require.NotContains(t, cc.Compiler.FuncCache, mangled, "the rejected specialization must not enter the shared cache")
}

func flatSpecializationCode(count int) string {
	var code strings.Builder
	for i := range count {
		fmt.Fprintf(&code, "res = Flat%d(x)\n    res = x\n\n", i)
	}

	return code.String()
}

func flatSpecializationScript(start, end int) string {
	var script strings.Builder
	for i := start; i < end; i++ {
		fmt.Fprintf(&script, "Flat%d(%d)\n", i, i)
	}

	return script.String()
}

func TestRecursiveLimitAllowsFlatBreadth(t *testing.T) {
	const specializationCount = 300
	const warmCount = 100

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	coldCC := NewCodeCompiler(ctx, "flatSpecializationsCold", "", mustParseCode(t, flatSpecializationCode(specializationCount)))
	require.Empty(t, coldCC.Compile())
	coldSC := NewScriptCompiler(ctx, t.Name()+"Cold", mustParseScript(t, flatSpecializationScript(0, specializationCount)), coldCC)
	require.Empty(t, coldSC.Compile(), "a cold script may instantiate more than 256 unrelated functions")

	warmCC := NewCodeCompiler(ctx, "flatSpecializationsWarm", "", mustParseCode(t, flatSpecializationCode(specializationCount)))
	require.Empty(t, warmCC.Compile())
	warmingSC := NewScriptCompiler(ctx, t.Name()+"Warming", mustParseScript(t, flatSpecializationScript(0, warmCount)), warmCC)
	require.Empty(t, warmingSC.Compile())

	warmedSC := NewScriptCompiler(ctx, t.Name()+"Warmed", mustParseScript(t, flatSpecializationScript(0, specializationCount)), warmCC)
	require.Empty(t, warmedSC.Compile(), "warming sibling specializations must not change acceptance")
}

func TestScalarCompanionPreservesRecursiveBudget(t *testing.T) {
	code := mustParseCode(t, `res = Scale(x)
    res = x * 3
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "scalarCompanionGuard", "", code)
	require.Empty(t, cc.Compile())

	program := mustParseScript(t, `arr = [10 20 30]
i = 0:3
scaled = [Scale(arr[i])]
scaled`)
	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.recLimit.maxFrames = 1
	ts.Solve()
	require.Empty(t, ts.Errors)

	scaledStmt := program.Statements[2].(*ast.LetStatement)
	collector := scaledStmt.Value[0].(*ast.ArrayLiteral)
	call := collector.Rows[0][0].(*ast.CallExpression)
	callInfo := ts.ExprCache[key(ts.FuncNameMangled, call)]
	require.NotNil(t, callInfo)

	primaryMangled := Mangle(cc.Compiler.MangledPath, "Scale", callInfo.CallParamTypes)
	scalarMangled := Mangle(cc.Compiler.MangledPath, "Scale", []Type{I64})
	require.True(t, callInfo.ScalarCallVariantEnsured)
	directCallees, _ := collectSpecializationCallEdges(sc.Compiler, sc.ScriptMangled, program.Statements)
	require.Equal(t, []string{primaryMangled, scalarMangled}, directCallees)
	callInfo.ScalarCallVariantEnsured = false
	directCallees, _ = collectSpecializationCallEdges(sc.Compiler, sc.ScriptMangled, program.Statements)
	require.Equal(t, []string{primaryMangled}, directCallees,
		"a scalar key already present in the shared cache must not create an edge without a call-local ensured fact")
	callInfo.ScalarCallVariantEnsured = true
	require.Contains(t, cc.Compiler.FuncCache, primaryMangled)
	require.Contains(t, cc.Compiler.FuncCache, scalarMangled)
	template, ok := cc.lookupFuncTemplate("Scale", 1)
	require.True(t, ok)
	require.NotEqual(t,
		specializationDisplay(specializationFrame{mangled: primaryMangled, template: template}),
		specializationDisplay(specializationFrame{mangled: scalarMangled, template: template}),
		"diagnostic frames must retain the actual specialization key when body parameter types collapse")
}

func TestCFGDiagnosticsDoNotFailSolver(t *testing.T) {
	code := mustParseCode(t, `result = Noisy(x)
    unused = x
    result = x
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "specializationCFGDiagnostics", "", code)
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), "value = Noisy(1)\nvalue")
	mangled := Mangle(cc.Compiler.MangledPath, "Noisy", []Type{I64})
	info := cc.Compiler.FuncCache[mangled]

	require.Empty(t, ts.Errors, "function CFG diagnostics must not become type-solver failures")
	require.True(t, info.Settled)
	require.NotNil(t, info.CFGResult)
	require.Len(t, info.CFGResult.Errors, 1)
	require.Contains(t, info.CFGResult.Errors[0].Msg, `"unused"`)
}

func TestCFGRecordsSettledDirectCallee(t *testing.T) {
	code := mustParseCode(t, `result = Leaf(x)
    result = x

result = Wrapper(x)
    result = Leaf(x)
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "settledCFGEdge", "", code)
	require.Empty(t, cc.Compile())

	solveScriptTypes(t, ctx, cc, t.Name()+"Leaf", "value = Leaf(1)\nvalue")
	leafMangled := Mangle(cc.Compiler.MangledPath, "Leaf", []Type{I64})
	leaf := cc.Compiler.FuncCache[leafMangled]
	require.True(t, leaf.Settled)
	require.NotNil(t, leaf.CFGResult)
	require.Empty(t, leaf.CFGResult.Errors)
	require.Empty(t, leaf.CFGResult.DirectCallees)

	solveScriptTypes(t, ctx, cc, t.Name()+"Wrapper", "value = Wrapper(1)\nvalue")
	wrapperMangled := Mangle(cc.Compiler.MangledPath, "Wrapper", []Type{I64})
	wrapper := cc.Compiler.FuncCache[wrapperMangled]

	require.True(t, wrapper.Settled)
	require.NotNil(t, wrapper.CFGResult)
	require.Equal(t, []string{leafMangled}, wrapper.CFGResult.DirectCallees)
}

func TestSettledSpecializationRequiresCFG(t *testing.T) {
	code := mustParseCode(t, `result = Leaf(x)
    result = x

result = Wrapper(x)
    result = Leaf(x)
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "missingSettledCFG", "", code)
	require.Empty(t, cc.Compile())

	solveScriptTypes(t, ctx, cc, t.Name()+"Leaf", "value = Leaf(1)\nvalue")
	leafMangled := Mangle(cc.Compiler.MangledPath, "Leaf", []Type{I64})
	cc.Compiler.FuncCache[leafMangled].CFGResult = nil

	program := mustParseScript(t, "value = Wrapper(1)\nvalue")
	sc := NewScriptCompiler(ctx, t.Name()+"Wrapper", program, cc)
	ts := NewTypeSolver(sc)
	require.PanicsWithValue(t,
		"internal: settled specialization "+leafMangled+" has no CFG result",
		ts.Solve,
	)
}

func TestNonConvergingCalleeIsBlamed(t *testing.T) {
	codeStr := `y = m(x)
    y = bad(x)

y = bad(x)
    y = bad(x-1)
`
	l := lexer.New("TestBlameCode", codeStr)
	cp := parser.NewCodeParser(l)
	code := cp.Parse()
	require.Empty(t, cp.Errors())

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "test", "", code)
	require.Empty(t, cc.Compile())

	sl := lexer.New("TestBlameScript", "x = 6\ny = m(x)\ny")
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	require.Len(t, ts.Errors, 1)
	require.Contains(t, ts.Errors[0].Msg, "Function bad is not converging")
	require.Equal(t, 4, ts.Errors[0].Token.Line, "must point at bad's definition, not the root's")
}

// A settled specialization must carry its variable types across scripts (#71).
func TestWarmFuncCacheReusesVars(t *testing.T) {
	codeStr := `res = Reset(k)
    i = 0:2
    "-i"
    a = [10 20 30]
    a = k > 0 []
    res = a ⊕ [7]

res = OuterReset(k)
    res = Reset(k)
`
	l := lexer.New("TestWarmCode", codeStr)
	cp := parser.NewCodeParser(l)
	code := cp.Parse()
	require.Empty(t, cp.Errors())

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "test", "", code)
	require.Empty(t, cc.Compile())

	resetMangled := Mangle(cc.Compiler.MangledPath, "Reset", []Type{I64})
	coldSolver := solveScriptTypes(t, ctx, cc, t.Name()+"Cold", "v = OuterReset(0)\nv")
	resetFunc := cc.Compiler.FuncCache[resetMangled]
	require.NotNil(t, resetFunc)
	require.True(t, resetFunc.Settled)
	coldVars := maps.Clone(resetFunc.Vars)

	warmSolver := solveScriptTypes(t, ctx, cc, t.Name()+"Warm", "v = OuterReset(0)\nv")
	require.Same(t, resetFunc, cc.Compiler.FuncCache[resetMangled])
	warmVars := maps.Clone(resetFunc.Vars)

	require.Equal(t, Array{ElemType: I64, Rank: 1}, coldVars["a"])
	require.Equal(t, Array{ElemType: I64, Rank: 1}, warmVars["a"])
	require.Equal(t, coldVars, warmVars, "a warm FuncCache must retain the specialization's variable types")
	require.Positive(t, coldSolver.TmpCounter)
	require.Zero(t, warmSolver.TmpCounter, "a settled closure must not be walked again by another script")
}

// Wide resolves one of 130 outputs per pass, exceeding the former limit.
func TestWideOutputClosureConverges(t *testing.T) {
	const outs = 130

	var b strings.Builder
	fmt.Fprintf(&b, "%s = Wide(k)\n", numberedNames("o", outs))
	fmt.Fprintf(&b, "    %s = Wide(k - 1)\n", numberedNames("p", outs))
	fmt.Fprintf(&b, "    \"-p%d\"\n", outs-1) // consume the tail slot; only o1..oN-1 read p
	b.WriteString("    o0 = 1\n")
	for i := 1; i < outs; i++ {
		fmt.Fprintf(&b, "    o%d = p%d + 1\n", i, i-1)
	}

	l := lexer.New("TestWideCode", b.String())
	cp := parser.NewCodeParser(l)
	code := cp.Parse()
	require.Empty(t, cp.Errors())

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "test", "", code)
	require.Empty(t, cc.Compile())

	sl := lexer.New("TestWideScript", fmt.Sprintf("%s = Wide(3)\nv0", numberedNames("v", outs)))
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	require.Empty(t, ts.Errors)
	wide := cc.Compiler.FuncCache[Mangle(cc.Compiler.MangledPath, "Wide", []Type{I64})]
	require.NotNil(t, wide)
	require.True(t, wide.AllTypesInferred(), "every one of the %d output slots must resolve", outs)
}

// C's p becomes known only after resolving the C -> B backedge.
func TestNestedBackEdgeRebuildsBodyFacts(t *testing.T) {
	code := mustParseCode(t, `res = A(k)
    t = B(k)
    res = t + 1

res = B(k)
    u = C(k)
    res = u + 1

res = C(k)
    p = B(k)
    res = 1
    res = k > 0 p
`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "nestedBackEdge", "", code)
	require.Empty(t, cc.Compile())

	sl := lexer.New("TestNestedBackEdgeScript", "v = A(2)\nv")
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()
	require.Empty(t, ts.Errors)

	for _, fn := range []struct {
		name  string
		local string
	}{{"A", "t"}, {"B", "u"}, {"C", "p"}} {
		mangled := Mangle(cc.Compiler.MangledPath, fn.name, []Type{I64})
		cached := sc.Compiler.FuncCache[mangled]
		require.NotNil(t, cached)
		require.True(t, cached.Settled)
		require.Contains(t, cached.Vars, fn.local,
			"%s's local %q must be typed by a sweep that reached it", fn.name, fn.local)
	}
}

func TestBrokenCalleeBlamedThroughWrapper(t *testing.T) {
	code := mustParseCode(t, `res = Root(k)
    res = AA(k)

res = AA(k)
    res = ZBrokenX(k)

res = ZBrokenX(k)
    res = ZBrokenX(k - 1)
`)
	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "wrapperBlame", "", code)
	require.Empty(t, cc.Compile())

	sl := lexer.New("TestWrapperBlameScript", "v = Root(3)\nv")
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	require.Len(t, ts.Errors, 1)
	require.Contains(t, ts.Errors[0].Msg, "Function ZBrokenX is not converging")
	require.Equal(t, 7, ts.Errors[0].Token.Line, "must point at ZBrokenX's own declaration")
}

// Array(Empty) and StrG outputs must widen on both cold and warm solves.
func TestOutputTypesRefineMonotonically(t *testing.T) {
	for _, tt := range []struct {
		name string
		seed string
		grow string
		want Type
	}{
		{"array", "[]", "⊕ [1]", Array{ElemType: I64, Rank: 1}},
		{"string", `"lit"`, `⊕ "x"`, StrH{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code := mustParseCode(t, fmt.Sprintf(`res = Root(k)
    res = %s
    res = k > 0 Relay(k)

res = Relay(k)
    res = Root(k - 1) %s
`, tt.seed, tt.grow))

			ctx := llvm.NewContext()
			defer ctx.Dispose()
			cc := NewCodeCompiler(ctx, "outputRefine"+tt.name, "", code)
			require.Empty(t, cc.Compile())

			solveScriptTypes(t, ctx, cc, t.Name()+"Cold", "v = Root(3)\nv")
			coldBindings := make(map[string]Type)
			for _, name := range []string{"Root", "Relay"} {
				mangled := Mangle(cc.Compiler.MangledPath, name, []Type{I64})
				coldBindings[name] = cc.Compiler.FuncCache[mangled].Vars["res"]
			}

			solveScriptTypes(t, ctx, cc, t.Name()+"Warm", "v = Root(3)\nv")
			warmBindings := make(map[string]Type)
			for _, name := range []string{"Root", "Relay"} {
				mangled := Mangle(cc.Compiler.MangledPath, name, []Type{I64})
				cached := cc.Compiler.FuncCache[mangled]
				require.NotNil(t, cached)
				warmBindings[name] = cached.Vars["res"]
				require.True(t, TypeEqual(tt.want, cached.Sig.OutTypes[0]), "%s output: got %s, want %s", name, cached.Sig.OutTypes[0], tt.want)

				require.True(t, cached.Settled)
				require.True(t, TypeEqual(tt.want, coldBindings[name]), "%s cold output binding", name)
				require.True(t, TypeEqual(tt.want, warmBindings[name]), "%s warm output binding", name)
			}
		})
	}
}

// Consume precedes Root's StrH refinement and must be remangled on the stable sweep.
func TestRefinedOutputSeedsStableBody(t *testing.T) {
	code := mustParseCode(t, `res = Root(k)
    res = "lit"
    tmp = Consume(res)
    "-tmp"
    res = k > 0 Relay(k)

res = Relay(k)
    res = Root(k - 1) ⊕ "x"

res = Consume(x)
    res = x
`)
	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "seedRefinedOutput", "", code)
	require.Empty(t, cc.Compile())

	sl := lexer.New("TestSeedRefinedOutputScript", "v = Root(3)\nv")
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors())

	sc := NewScriptCompiler(ctx, t.Name(), program, cc)
	ts := NewTypeSolver(sc)
	ts.Solve()

	require.Empty(t, ts.Errors)
	heapConsumer := cc.Compiler.FuncCache[Mangle(cc.Compiler.MangledPath, "Consume", []Type{StrH{}})]
	require.NotNil(t, heapConsumer, "the stable body sweep must remangle Consume with Root's StrH output slot")
	require.True(t, heapConsumer.AllTypesInferred())

	root := code.Statements[0].(*ast.FuncStatement)
	consumeStmt := root.Body.Statements[1].(*ast.LetStatement)
	consumeCall := consumeStmt.Value[0].(*ast.CallExpression)
	rootMangled := Mangle(cc.Compiler.MangledPath, "Root", []Type{I64})
	callInfo := ts.ExprCache[key(rootMangled, consumeCall)]
	require.NotNil(t, callInfo)
	require.Len(t, callInfo.CallParamTypes, 1)
	require.Len(t, callInfo.ScalarCallParamTypes, 1)
	require.True(t, TypeEqual(StrH{}, callInfo.CallParamTypes[0]), "final call metadata must use the output slot's StrH storage type")
	require.True(t, TypeEqual(StrH{}, callInfo.ScalarCallParamTypes[0]), "final scalar-call metadata must use the output slot's StrH storage type")
}

func TestFunctionOutputTableJoinMatchesStorage(t *testing.T) {
	code := mustParseCode(t, `res = RefineTable(k)
    "-k"
    res = [
      : Name Score
        "Ada" 10
    ]

res = ResetTable(k)
    "-k"
    res = [
      : Name Score
    ]
`)
	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "tableOutputJoin", "", code)
	require.Empty(t, cc.Compile())

	sc := NewScriptCompiler(ctx, t.Name(), &ast.Program{}, cc)
	ts := NewTypeSolver(sc)
	headerOnly := Table{Columns: []TableColumn{
		{Name: "Name", ElemType: Empty{}},
		{Name: "Score", ElemType: Empty{}},
	}}
	concrete := Table{Columns: []TableColumn{
		{Name: "Name", ElemType: StrH{}},
		{Name: "Score", ElemType: I64},
	}}

	refineTemplate := code.Statements[0].(*ast.FuncStatement)
	refineMangled := Mangle(cc.Compiler.MangledPath, "RefineTable", []Type{I64})
	refine := &FuncInfo{
		Sig:  Func{Name: "RefineTable", Params: []Type{I64}, OutTypes: []Type{headerOnly}},
		Vars: make(map[string]Type),
	}
	cc.Compiler.FuncCache[refineMangled] = refine
	require.True(t, ts.TypeFunc(refineMangled, refineTemplate))
	require.Empty(t, ts.Errors)
	require.True(t, TypeEqual(concrete, refine.Sig.OutTypes[0]))
	require.True(t, TypeEqual(concrete, refine.Vars["res"]))

	clear(ts.walkedFuncs)
	resetTemplate := code.Statements[1].(*ast.FuncStatement)
	resetMangled := Mangle(cc.Compiler.MangledPath, "ResetTable", []Type{I64})
	reset := &FuncInfo{
		Sig:  Func{Name: "ResetTable", Params: []Type{I64}, OutTypes: []Type{concrete}},
		Vars: make(map[string]Type),
	}
	cc.Compiler.FuncCache[resetMangled] = reset
	ts.Converging = false
	require.True(t, ts.TypeFunc(resetMangled, resetTemplate))
	require.Empty(t, ts.Errors)
	require.False(t, ts.Converging, "a header-only reset must not narrow or count as output progress")
	require.True(t, TypeEqual(concrete, reset.Sig.OutTypes[0]))
	require.True(t, TypeEqual(concrete, reset.Vars["res"]))
}

func TestUnsettledProvisionalIsRevisitedAcrossScripts(t *testing.T) {
	code := mustParseCode(t, `res = Outer(k)
    t = A(k)
    res = Leaf(t)
    res = k == 0 0

res = A(k)
    p = Outer(k)
    res = p + 1

res = Other(k)
    t = B(k)
    res = Leaf(t)
    res = k == 0 0.5

res = B(k)
    p = Other(k)
    res = p + 1.0

res = Leaf(x)
    res = x
`)
	ctx := llvm.NewContext()
	defer ctx.Dispose()
	cc := NewCodeCompiler(ctx, "remangle", "", code)
	require.Empty(t, cc.Compile())

	solveScriptTypes(t, ctx, cc, t.Name()+"Outer", "v = Outer(0)\nv")
	integerLeaf := cc.Compiler.FuncCache[Mangle(cc.Compiler.MangledPath, "Leaf", []Type{I64})]
	require.NotNil(t, integerLeaf)
	require.True(t, integerLeaf.Settled)

	provisional := cc.Compiler.FuncCache[Mangle(cc.Compiler.MangledPath, "Leaf", []Type{Unresolved{}})]
	require.NotNil(t, provisional)
	require.False(t, provisional.Settled)
	provisional.Vars["sentinel"] = I64

	otherSolver := solveScriptTypes(t, ctx, cc, t.Name()+"Other", "v = Other(0)\nv")
	require.Same(t, provisional, cc.Compiler.FuncCache[Mangle(cc.Compiler.MangledPath, "Leaf", []Type{Unresolved{}})])
	require.False(t, provisional.Settled)
	require.NotContains(t, provisional.Vars, "sentinel", "the second script must rewalk the unsettled specialization")
	floatLeaf := cc.Compiler.FuncCache[Mangle(cc.Compiler.MangledPath, "Leaf", []Type{F64})]
	require.NotNil(t, floatLeaf)
	require.True(t, floatLeaf.Settled)

	other := code.Statements[2].(*ast.FuncStatement)
	leafCall := other.Body.Statements[1].(*ast.LetStatement).Value[0].(*ast.CallExpression)
	otherMangled := Mangle(cc.Compiler.MangledPath, "Other", []Type{I64})
	callInfo := otherSolver.ExprCache[key(otherMangled, leafCall)]
	require.NotNil(t, callInfo)
	require.Equal(t, []Type{F64}, callInfo.CallParamTypes)
	require.Equal(t, []Type{F64}, callInfo.ScalarCallParamTypes)
}
