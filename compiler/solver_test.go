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
    :name age height
    "Tejas" 35 184.5
q = Person
    :age
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
    :name age
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
			&ast.StringLiteral{Token: token.Token{Type: token.STRING, Literal: "Ada"}, Value: "Ada"},
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
    :name age
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
				Left:     &ast.StringLiteral{Token: token.Token{Type: token.STRING, Literal: "Ada"}, Value: "Ada"},
				Operator: token.SYM_CONCAT,
				Right:    &ast.StringLiteral{Token: token.Token{Type: token.STRING, Literal: "!"}, Value: "!"},
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
	if len(arrType.ColTypes) != 1 {
		t.Fatalf("expected single-column array type")
	}
	if arrType.ColTypes[0].Kind() != FloatKind {
		t.Fatalf("expected float array result, got %s", arrType.ColTypes[0].String())
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

func TestArrayComparisonInValuePositionIsFilter(t *testing.T) {
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
	require.Equal(t, CondArray, info.CompareModes[0], "array comparison in value position should be tagged as filter")
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
	require.Contains(t, ts.Errors[0].Msg, "statement condition must be a comparison or bare range/array-range driver, got I64")
}

func TestScalarArrayComparisonInValuePositionIsFilter(t *testing.T) {
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
	require.Equal(t, CondArray, info.CompareModes[0], "scalar-array comparison in value position should be tagged as filter")

	outArr, ok := info.OutTypes[0].(Array)
	require.True(t, ok, "expected scalar-array filter output type to be array")
	require.Equal(t, IntKind, outArr.ColTypes[0].Kind(), "scalar-array filter should keep scalar LHS element type")
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

func TestArrayRangeTyping(t *testing.T) {
	ctx := llvm.NewContext()
	code := ast.NewCode()
	cc := NewCodeCompiler(ctx, "arrayRangeTyping", "", code)
	cc.Compile()

	script := "arr = [1 2 3]\nvalue = arr[0:2]\nsum = 0\nsum = sum + arr[0:2]"
	sl := lexer.New("ArrayRangeTyping.spt", script)
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
	value, ok := valueType.(ArrayRange)
	require.Truef(t, ok, "expected value to be ArrayRange, got %T", valueType)
	require.EqualValues(t, value.Array.ColTypes[0], Int{Width: 64})
	require.EqualValues(t, value.Range, Range{Iter: Int{Width: 64}})

	sumType, ok := ts.GetIdentifier("sum")
	require.True(t, ok, "expected sum identifier")
	sumInt, ok := sumType.(Int)
	require.Truef(t, ok, "expected sum to be Int, got %T", sumType)
	require.EqualValues(t, 64, sumInt.Width)
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
		if strings.Contains(err.Msg, "array range index expects I64 iterator") {
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
