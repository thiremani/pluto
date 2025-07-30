package compiler

import (
	"strings"
	"testing"

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
	sc := NewScriptCompiler(ctx, "TestMutualRecursionScript", program, cc, funcCache)
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

	nsc := NewScriptCompiler(ctx, "testNext", nextProgram, cc, funcCache)
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
	sc := NewScriptCompiler(ctx, "TestCyclesScript", program, cc, funcCache)
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
	sc := NewScriptCompiler(ctx, "TestNoBaseCaseScript", program, cc, funcCache)
	ts := NewTypeSolver(sc)
	ts.Solve()

	if len(ts.Errors) != 1 {
		t.Error("Expected a cyclic recursion error, but got none")
	}

	if !strings.Contains(ts.Errors[0].Msg, "Function f is not converging. Check for cyclic recursion and that each function has a base case") {
		t.Errorf("Expected cyclic recursion error, but got: %s", ts.Errors[0].Msg)
	}
}

func noConvergeRecover(t *testing.T) {
	// `recover()` catches a panic. If there was no panic, it returns nil.
	r := recover()
	if r == nil {
		// If we get here, it means ts.Solve() completed WITHOUT panicking.
		// This is an error for this specific test case.
		t.Errorf("The code did not panic, but was expected to for an unresolvable type cycle.")
		return
	}

	// Optionally, you can check if the panic message is what you expect.
	// `r` holds the value passed to `panic()`.
	errMsg, ok := r.(string)
	if !ok {
		t.Errorf("Panic object is not a string: %v", r)
		return
	}

	expectedPanicMsg := "Inferring output types for function f is not converging"
	if !strings.Contains(errMsg, expectedPanicMsg) {
		t.Errorf("Expected panic message to contain '%s', but got '%s'", expectedPanicMsg, errMsg)
	}

	// If we get here, the test passed because it panicked as expected.
	t.Logf("Successfully caught expected panic: %v", r)
}
