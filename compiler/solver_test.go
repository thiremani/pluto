package compiler

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/parser"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

func TestMutualRecursion(t *testing.T) {
	codeStr := `# define isEven: returns (x, y) = (is-even?, is-odd?)
x, y = isEven(n)
    # recursive step: if n≠0, flip the pair returned by isOdd(n-1)
    x, y = n != 0 isOdd(n - 1)
    # base case: 0 is even, not odd
    x = n == 1 "no"
    x = n == 0 "yes"

# define isOdd: returns (x, y) = (is-odd?, is-even?)
# this function only infers type for y
x, y = isOdd(n)
    # recursive step: if n≠0, flip the pair returned by isEven(n-1)
    x, y = n != 0 isEven(n - 1)
    # base case: 0 is not odd, but even
    y = n == 1 "no"
    y = n == 0 "yes"`

	l := lexer.New("TestMutualRecursionCode", codeStr)
	cp := parser.NewCodeParser(l)
	code := cp.Parse()

	if errs := cp.Errors(); len(errs) > 0 {
		t.Error(strings.Join(errs, ","))
	}

	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "test", "", code)
	cc.Compile()

	script := `x, y = isEven(3)
x, y`

	sl := lexer.New("TestMutualRecursionScript", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()

	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)
	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
	ts := NewTypeSolver(sc)
	ts.Solve()

	// check func cache
	isEvenFunc := ts.ScriptCompiler.Compiler.FuncCache["Pt_4test_p_6isEven_f1_I64"]
	if isEvenFunc.OutTypes[0].Kind() != StrKind {
		t.Errorf("isEven func should strkind for output arg 0")
	}
	if isEvenFunc.OutTypes[1].Kind() != StrKind {
		t.Errorf("isEven func should strkind for output arg 1")
	}

	isOddFunc := ts.ScriptCompiler.Compiler.FuncCache["Pt_4test_p_5isOdd_f1_I64"]
	if isOddFunc.OutTypes[0].Kind() != UnresolvedKind {
		t.Errorf("isOdd func should remain unresolved for output arg 0 until called from script")
	}
	if isOddFunc.OutTypes[1].Kind() != StrKind {
		t.Errorf("isOdd func should strkind for output arg 1")
	}

	// now further compile for isOdd
	nextScript := `x, y = isOdd(17)
x, y`

	nsl := lexer.New("TestMutualRecursionScript2", nextScript)
	nsp := parser.NewScriptParser(nsl)
	nextProgram := nsp.Parse()

	nsc := NewScriptCompiler(ctx, nextProgram, cc, funcCache, exprCache)
	nts := NewTypeSolver(nsc)
	nts.Solve()

	nextOddFunc := nts.ScriptCompiler.Compiler.FuncCache["Pt_4test_p_5isOdd_f1_I64"]
	if nextOddFunc.OutTypes[0].Kind() != StrKind {
		t.Errorf("Next isOdd func should strkind for output arg 0")
	}
	if nextOddFunc.OutTypes[1].Kind() != StrKind {
		t.Errorf("Next isOdd func should strkind for output arg 1")
	}
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

	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)
	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
	ts := NewTypeSolver(sc)
	ts.Solve()

	if len(ts.Errors) != 1 {
		t.Error("Expected a cyclic recursion error, but got none")
	}
	if !strings.Contains(ts.Errors[0].Msg, "Function f is not converging. Check for cyclic recursion and that each function has a base case") {
		t.Errorf("Expected cyclic recursion error, but got: %s", ts.Errors[0].Msg)
	}
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

	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)
	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
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

	sc := NewScriptCompiler(ctx, &ast.Program{}, cc, make(map[string]*Func), cc.Compiler.ExprCache)
	ts := NewTypeSolver(sc)

	require.Len(t, code.Struct.Statements, 3, "expected canonical/full, subset, and empty struct statements")
	qType := ts.TypeStructLiteral(code.Struct.Statements[1].Value)
	rType := ts.TypeStructLiteral(code.Struct.Statements[2].Value)

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

	sc := NewScriptCompiler(ctx, &ast.Program{}, cc, make(map[string]*Func), cc.Compiler.ExprCache)
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

	sc := NewScriptCompiler(ctx, &ast.Program{}, cc, make(map[string]*Func), cc.Compiler.ExprCache)
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

			sc := NewScriptCompiler(ctx, program, cc, make(map[string]*Func), make(map[ExprKey]*ExprInfo))
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
	sc := NewScriptCompiler(ctx, program, cc, make(map[string]*Func), make(map[ExprKey]*ExprInfo))
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
	bindingType := ts.BindingTypes[BindingKey{Name: "locked"}].(Array)
	require.Equal(t, EmptyKind, resetType.ElemType.Kind())
	require.Equal(t, IntKind, bindingType.ElemType.Kind())
}

func TestArrayConcatTypeErrors(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "arrayConcatErrors", "", ast.NewCode())
	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)

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

			sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
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

	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
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
	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)

	script := "x = 1\nx = [2 3]"
	sl := lexer.New("arrayToScalar.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()

	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
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
	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)

	script := `a = "abc"
a = a ⊕ "d"`
	sl := lexer.New("StringBindingInference.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
	ts := NewTypeSolver(sc)
	ts.Solve()
	require.Empty(t, ts.Errors, "unexpected solver errors: %v", ts.Errors)

	slotType, ok := ts.GetIdentifier("a")
	require.True(t, ok, "expected identifier a")
	require.True(t, IsStrH(slotType), "binding a should widen to StrH")

	bindingType, ok := ts.BindingTypes[BindingKey{Name: "a"}]
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

func TestRangeBoundsCannotDependOnRangeValues(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "rangeBoundsDepend", "", ast.NewCode())
	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)

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

			sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
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
	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)

	script := "a = [1 2]\nb = [0 3]\nx = a > b"
	sl := lexer.New("arrayComparisonValue.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
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
	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)

	script := "cond = [1 2]\nx = cond 5"
	sl := lexer.New("arrayConditionDiagnostic.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
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

	sc := NewScriptCompiler(ctx, program, cc, make(map[string]*Func), make(map[ExprKey]*ExprInfo))
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

	sc := NewScriptCompiler(ctx, program, cc, make(map[string]*Func), make(map[ExprKey]*ExprInfo))
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

	sc := NewScriptCompiler(ctx, program, cc, make(map[string]*Func), make(map[ExprKey]*ExprInfo))
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

	sc := NewScriptCompiler(ctx, program, cc, make(map[string]*Func), make(map[ExprKey]*ExprInfo))
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

	sc := NewScriptCompiler(ctx, program, cc, make(map[string]*Func), make(map[ExprKey]*ExprInfo))
	ts := NewTypeSolver(sc)
	ts.Solve()

	require.Lenf(t, ts.Errors, 1, "function-body rejection should emit exactly one diagnostic, got: %v", ts.Errors)
	require.Contains(t, ts.Errors[0].Msg, "logical OR in value position requires a conditional left operand")
}

func TestScalarConditionEmitsTypeDiagnostic(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "scalarConditionDiagnostic", "", ast.NewCode())
	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)

	script := "cond = 5\nx = cond [1]"
	sl := lexer.New("scalarConditionDiagnostic.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
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

			sc := NewScriptCompiler(ctx, program, cc, make(map[string]*Func), make(map[ExprKey]*ExprInfo))
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

			sc := NewScriptCompiler(ctx, program, cc, make(map[string]*Func), make(map[ExprKey]*ExprInfo))
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
	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)

	script := "a = [1 2]\nx = 3 > a"
	sl := lexer.New("scalarArrayComparisonValue.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
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
	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)

	script := `idx = 0:5
res = [idx]`

	sl := lexer.New("ArrayLiteralRanges", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors())

	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
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

	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)
	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
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
	sc := NewScriptCompiler(ctx, program, cc, make(map[string]*Func), make(map[ExprKey]*ExprInfo))
	ts := NewTypeSolver(sc)
	ts.Solve()
	require.Emptyf(t, ts.Errors, "unexpected type errors: %v", ts.Errors)

	valueStmt := program.Statements[2].(*ast.LetStatement)
	call := valueStmt.Value[0].(*ast.CallExpression)
	callInfo := ts.ExprCache[key("", call)]
	require.True(t, callInfo.LoopInside)
	require.Equal(t, []Type{I64}, callInfo.ScalarCallParamTypes)
	require.Len(t, callInfo.CallParamTypes, 1)

	arrayRange, ok := callInfo.CallParamTypes[0].(ArrayRange)
	require.Truef(t, ok, "expected call-only ArrayRange, got %T", callInfo.CallParamTypes[0])
	require.Equal(t, Array{ElemType: I64, Rank: 1}, arrayRange.Array)
	require.Equal(t, Range{Iter: I64}, arrayRange.Range)

	selection := call.Arguments[0].(*ast.ArrayRangeExpression)
	require.Equal(t, []Type{I64}, ts.ExprCache[key("", selection)].OutTypes,
		"the source expression must remain element-typed outside the call ABI")
	valueType, ok := ts.GetIdentifier("value")
	require.True(t, ok)
	require.Equal(t, I64, valueType)

	printCall := program.Statements[3].(*ast.PrintStatement).Expression
	printInfo := ts.ExprCache[key("", printCall)]
	require.False(t, printInfo.LoopInside, "print must consume the selection at the caller")
	require.Equal(t, []Type{I64}, printInfo.CallParamTypes)
}

func TestArrayCollectorCreatesScalarVariantForArrayRangeCall(t *testing.T) {
	ctx := llvm.NewContext()
	code := mustParseCode(t, `out = Double(x)
    out = x * 2`)
	moduleName := "arrayRangeCollector"
	cc := NewCodeCompiler(ctx, moduleName, "", code)
	require.Empty(t, cc.Compile())

	program := mustParseScript(t, `arr = [10 20 30]
values = [Double(arr[0:3])]
i = 0:3
[i], [0:3]`)
	sc := NewScriptCompiler(ctx, program, cc, make(map[string]*Func), make(map[ExprKey]*ExprInfo))
	ts := NewTypeSolver(sc)
	ts.Solve()
	require.Emptyf(t, ts.Errors, "unexpected type errors: %v", ts.Errors)

	scalarMangled := Mangle(MangleDirPath(moduleName, ""), "Double", []Type{I64})
	require.Contains(t, ts.ScriptCompiler.Compiler.FuncCache, scalarMangled,
		"the surrounding array collector invokes a scalar callee per selected element")
}

func TestRankTwoSelectionSpecializesOverFullArraySchema(t *testing.T) {
	ctx := llvm.NewContext()
	code := mustParseCode(t, `out = Identity(x)
    out = x`)
	cc := NewCodeCompiler(ctx, "rankTwoCallScopedArrayRange", "", code)
	require.Empty(t, cc.Compile())

	program := mustParseScript(t, `rows = 0:2
matrix = [
    1 2
    3 4
]
row = Identity(matrix[rows])`)
	sc := NewScriptCompiler(ctx, program, cc, make(map[string]*Func), make(map[ExprKey]*ExprInfo))
	ts := NewTypeSolver(sc)
	ts.Solve()
	require.Emptyf(t, ts.Errors, "unexpected type errors: %v", ts.Errors)

	call := program.Statements[2].(*ast.LetStatement).Value[0].(*ast.CallExpression)
	info := ts.ExprCache[key("", call)]
	require.True(t, info.LoopInside)
	require.Equal(t, []Type{Array{ElemType: I64, Rank: 1}}, info.ScalarCallParamTypes)
	require.Equal(t, []Type{ArrayRange{
		Array: Array{ElemType: I64, Rank: 2},
		Range: Range{Iter: I64},
	}}, info.CallParamTypes)

	rowType, ok := ts.GetIdentifier("row")
	require.True(t, ok)
	require.Equal(t, Array{ElemType: I64, Rank: 1}, rowType)
}

func TestCallRangePlacementPreservesSharedDriverIdentity(t *testing.T) {
	ctx := llvm.NewContext()
	code := mustParseCode(t, `out = Identity(x)
    out = x

left, right = Keep(a, b)
    left = a
    right = b`)
	cc := NewCodeCompiler(ctx, "callRangePlacement", "", code)
	require.Empty(t, cc.Compile())

	program := mustParseScript(t, `i = 0:3
j = 1:3
arr = [10 20 30]
rangeValue = Identity(j)
distinctLeft, distinctRight = Keep(arr[i], j)
sharedLeft, sharedRight = Keep(arr[i], i)`)
	sc := NewScriptCompiler(ctx, program, cc, make(map[string]*Func), make(map[ExprKey]*ExprInfo))
	ts := NewTypeSolver(sc)
	ts.Solve()
	require.Emptyf(t, ts.Errors, "unexpected type errors: %v", ts.Errors)

	rangeCall := program.Statements[3].(*ast.LetStatement).Value[0].(*ast.CallExpression)
	rangeInfo := ts.ExprCache[key("", rangeCall)]
	require.True(t, rangeInfo.LoopInside, "a bare Range argument should remain callee-iterated")
	require.Equal(t, []Type{Range{Iter: I64}}, rangeInfo.CallParamTypes)
	require.Equal(t, []Type{I64}, rangeInfo.ScalarCallParamTypes)

	distinctCall := program.Statements[4].(*ast.LetStatement).Value[0].(*ast.CallExpression)
	distinctInfo := ts.ExprCache[key("", distinctCall)]
	require.True(t, distinctInfo.LoopInside,
		"distinct Range and ArrayRange drivers may form the callee's cartesian loop")
	require.IsType(t, ArrayRange{}, distinctInfo.CallParamTypes[0])
	require.Equal(t, Range{Iter: I64}, distinctInfo.CallParamTypes[1])
	require.Equal(t, []Type{I64, I64}, distinctInfo.ScalarCallParamTypes)

	sharedCall := program.Statements[5].(*ast.LetStatement).Value[0].(*ast.CallExpression)
	sharedInfo := ts.ExprCache[key("", sharedCall)]
	require.False(t, sharedInfo.LoopInside,
		"a driver reused by arr[i] and i must advance once at the caller")
	require.Equal(t, []Type{I64, I64}, sharedInfo.CallParamTypes)
	require.Equal(t, []Type{I64, I64}, sharedInfo.ScalarCallParamTypes)
	require.Len(t, sharedInfo.Ranges, 1)
	require.Equal(t, "i", sharedInfo.Ranges[0].Name)
}

func TestArrayIndexRejectsI1(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "arrayIndexI1", "", ast.NewCode())
	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)

	script := "arr = [1 2 3]\nvalue = arr[idx]"
	sl := lexer.New("ArrayIndexRejectsI1.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
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
	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)

	script := "arr = [1 2 3]\nvalue = arr[idx]"
	sl := lexer.New("ArrayIndexAllowsWiderIntKinds.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
	ts := NewTypeSolver(sc)
	Put(ts.Scopes, "idx", Type(Int{Width: 32}))
	ts.Solve()
	require.Empty(t, ts.Errors, "unexpected solver errors for wider integer index: %v", ts.Errors)
}

func TestArrayRangeIndexRequiresI64Iter(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "arrayRangeIndexI64", "", ast.NewCode())
	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)

	script := "arr = [1 2 3]\nvalue = arr[idx]"
	sl := lexer.New("ArrayRangeIndexRequiresI64Iter.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
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
	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)

	script := "x = -(0:3)"
	sl := lexer.New("PrefixRewriteCopy.spt", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()
	require.Empty(t, sp.Errors(), "unexpected parse errors: %v", sp.Errors())

	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
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
