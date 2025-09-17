package compiler

import (
	"strings"
	"testing"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/parser"
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
	cc := NewCodeCompiler(ctx, "test", code)
	cc.Compile()

	script := `x, y = isEven(3)
x, y`

	sl := lexer.New("TestMutualRecursionScript", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()

	funcCache := make(map[string]*Func)
	exprCache := make(map[ast.Expression]*ExprInfo)
	sc := NewScriptCompiler(ctx, "TestMutualRecursionScript", program, cc, funcCache, exprCache)
	ts := NewTypeSolver(sc)
	ts.Solve()

	// check func cache
	isEvenFunc := ts.ScriptCompiler.Compiler.FuncCache["$isEven$I64"]
	if isEvenFunc.OutTypes[0].Kind() != StrKind {
		t.Errorf("isEven func should strkind for output arg 0")
	}
	if isEvenFunc.OutTypes[1].Kind() != StrKind {
		t.Errorf("isEven func should strkind for output arg 1")
	}

	isOddFunc := ts.ScriptCompiler.Compiler.FuncCache["$isOdd$I64"]
	if isOddFunc.OutTypes[0].Kind() != UnresolvedKind {
		t.Errorf("isOdd func should strkind for output arg 0")
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

	nsc := NewScriptCompiler(ctx, "testNext", nextProgram, cc, funcCache, exprCache)
	nts := NewTypeSolver(nsc)
	nts.Solve()

	nextOddFunc := nts.ScriptCompiler.Compiler.FuncCache["$isOdd$I64"]
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
	cc := NewCodeCompiler(ctx, "test", code)
	cc.Compile()

	script := `x = 6
y = f(x)
y`
	sl := lexer.New("TestCyclesScript", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()

	funcCache := make(map[string]*Func)
	exprCache := make(map[ast.Expression]*ExprInfo)
	sc := NewScriptCompiler(ctx, "TestCyclesScript", program, cc, funcCache, exprCache)
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
	cc := NewCodeCompiler(ctx, "test", code)
	cc.Compile()

	script := `x = 6
y = f(x)
y`
	sl := lexer.New("TestNoBaseCaseScript", script)
	sp := parser.NewScriptParser(sl)
	program := sp.Parse()

	funcCache := make(map[string]*Func)
	exprCache := make(map[ast.Expression]*ExprInfo)
	sc := NewScriptCompiler(ctx, "TestNoBaseCaseScript", program, cc, funcCache, exprCache)
	ts := NewTypeSolver(sc)
	ts.Solve()

	if len(ts.Errors) != 1 {
		t.Error("Expected a cyclic recursion error, but got none")
	}

	if !strings.Contains(ts.Errors[0].Msg, "Function f is not converging. Check for cyclic recursion and that each function has a base case") {
		t.Errorf("Expected cyclic recursion error, but got: %s", ts.Errors[0].Msg)
	}
}

func TestArrayConcatTypeErrors(t *testing.T) {
	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "arrayConcatErrors", ast.NewCode())
	funcCache := make(map[string]*Func)
	exprCache := make(map[ast.Expression]*ExprInfo)

	cases := []struct {
		name        string
		script      string
		expectError string
	}{
		{
			name:        "StringPlusIntArray",
			script:      "arr1 = [\"foo\" \"bar\"]\narr2 = [1 2]\nres = arr1 + arr2",
			expectError: "cannot concatenate arrays with incompatible element types",
		},
		{
			name:        "FloatPlusStringArray",
			script:      "arr1 = [1.5 2.5]\narr2 = [\"foo\"]\nres = arr1 + arr2",
			expectError: "cannot concatenate arrays with incompatible element types",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sl := lexer.New(tc.name+".spt", tc.script)
			sp := parser.NewScriptParser(sl)
			program := sp.Parse()

			sc := NewScriptCompiler(ctx, tc.name, program, cc, funcCache, exprCache)
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

	sc := NewScriptCompiler(ctx, "MixedNumericConcat", program, cc, funcCache, exprCache)
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
