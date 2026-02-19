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

func TestStringCompile(t *testing.T) {
	input := `"hello"`
	l := lexer.New("TestStringCompile", input)
	sp := parser.NewScriptParser(l)
	program := sp.Parse()

	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "testStringCompile", "", ast.NewCode())

	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)
	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
	sc.Compile()
	ir := sc.Compiler.GenerateIR()

	expectedIR := `@printf_fmt_0 = constant [7 x i8] c"hello\0A\00"`
	if !strings.Contains(ir, expectedIR) {
		t.Errorf("IR does not contain string constant:\n%s", ir)
	}
}

func TestFormatIdentifiers(t *testing.T) {
	input := `x = 5
six = 6
x, six`

	l := lexer.New("TestFormatIdentifiers", input)
	sp := parser.NewScriptParser(l)
	program := sp.Parse()

	ctx := llvm.NewContext()
	cc := NewCodeCompiler(ctx, "testFormatIdentifiers", "", ast.NewCode())

	funcCache := make(map[string]*Func)
	exprCache := make(map[ExprKey]*ExprInfo)
	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
	sc.Compile()
	testStr := "x = -x, six = -six"
	sl := &ast.StringLiteral{
		Token: token.Token{
			FileName: "FormatIdentifiers",
			Type:     token.STRING,
			Literal:  testStr,
			Line:     1,
			Column:   1,
		},
		Value: testStr,
	}
	res, vals, _ := sc.Compiler.formatString(sl.Token, sl.Value)
	expStr := "x = %lld, six = %lld"
	if res != expStr {
		t.Errorf("formattedStr does not match expected. got: %s, expected: %s", res, expStr)
	}
	if len(vals) != 2 {
		t.Errorf("len(vals) does not match expected. got: %d, expected: 2", len(vals))
	}
	expVals := []llvm.Value{llvm.ConstInt(sc.Compiler.Context.Int64Type(), 5, false), llvm.ConstInt(sc.Compiler.Context.Int64Type(), 6, false)}
	for i, val := range vals {
		if val != expVals[i] {
			t.Errorf("vals[%d] does not match expected.", i)
			t.Error("got", val, "expected", expVals[i])
		}
	}
}

func TestConstCompile(t *testing.T) {
	input := `pi = 3.1415926535
answer = 42
greeting = "hello"`

	l := lexer.New("TestConstCompile", input)
	cp := parser.NewCodeParser(l)
	code := cp.Parse()

	c := NewCodeCompiler(llvm.NewContext(), "testConst", "", code)
	c.Compile()
	ir := c.Compiler.GenerateIR()

	// Constants are now mangled per C ABI spec: Pt_[ModPath]_p_[Name]
	expPi := "@Pt_9testConst_p_2pi = unnamed_addr constant double 0x400921FB54411744"
	if !strings.Contains(ir, expPi) {
		t.Errorf("IR does not contain global constant for pi. Exp: %s, ir: \n%s", expPi, ir)
	}

	expAns := "@Pt_9testConst_p_6answer = unnamed_addr constant i64 42"
	if !strings.Contains(ir, expAns) {
		t.Errorf("IR does not contain global constant for answer. Exp: %s, ir: \n%s", expAns, ir)
	}

	expGreeting := `@Pt_9testConst_p_8greeting = unnamed_addr constant [6 x i8] c"hello\00"`

	if !strings.Contains(ir, expGreeting) {
		t.Errorf("IR does not contain global constant for greeting. Exp: %s, ir: \n%s", expGreeting, ir)
	}
}

func TestSetupRangeOutputsWithPointerSeed(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "ptr_seed_module", "", ast.NewCode())
	c := NewCompiler(ctx, cc.Compiler.MangledPath, cc)

	// Simulate the entry block state used by compileFuncBlock.
	fnType := llvm.FunctionType(ctx.VoidType(), nil, false)
	fn := llvm.AddFunction(c.Module, "ptr_seed_fn", fnType)
	entry := ctx.AddBasicBlock(fn, "entry")
	c.builder.SetInsertPointAtEnd(entry)

	// Function scope with an existing pointer-typed value named "seed"
	PushScope(&c.Scopes, FuncScope)
	ptrType := Ptr{Elem: Int{Width: 64}}
	global := llvm.AddGlobal(c.Module, ctx.Int64Type(), "seed_global")
	global.SetInitializer(llvm.ConstInt(ctx.Int64Type(), 5, false))
	Put(c.Scopes, "seed", &Symbol{
		Val:      global,
		Type:     ptrType,
		FuncArg:  true,
		Borrowed: true,
		ReadOnly: true,
	})

	// Act: seed a loop temporary for a pointer-valued output.
	dest := []*ast.Identifier{{Value: "seed"}}
	outTypes := []Type{I64}
	outputs := c.makeOutputs(dest, outTypes, false)

	require.Len(t, outputs, 1, "expect a single output symbol")
	// When seed is already a pointer, makeOutputs reuses it directly
	require.Equal(t, global, outputs[0].Val, "expected existing pointer to be reused")
	require.Equal(t, ptrType, outputs[0].Type, "expected pointer type to be preserved")

	c.builder.CreateRetVoid()

	ir := c.Module.String()
	// No store or load needed - we reuse the existing pointer directly
	require.NotContains(t, ir, "store", "pointer seed should be reused without store")
	require.NotContains(t, ir, "load i64, ptr @seed_global", "pointer seed should not be dereferenced")
}

func TestCompileCondScalarStrHUsesBranch(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "cond_scalar_strh", "", ast.NewCode())
	c := NewCompiler(ctx, cc.Compiler.MangledPath, cc)

	fnType := llvm.FunctionType(ctx.VoidType(), nil, false)
	fn := llvm.AddFunction(c.Module, "cond_scalar_strh_fn", fnType)
	entry := ctx.AddBasicBlock(fn, "entry")
	c.builder.SetInsertPointAtEnd(entry)

	lhsGlobal := c.createGlobalString("lhs_str", "banana", llvm.PrivateLinkage)
	rhsGlobal := c.createGlobalString("rhs_str", "apple", llvm.PrivateLinkage)
	lhs := &Symbol{Type: StrH{}, Val: c.copyString(lhsGlobal)}
	rhs := &Symbol{Type: StrH{}, Val: c.copyString(rhsGlobal)}

	res := c.compileCondScalar(token.SYM_GTR, lhs, rhs)
	require.Equal(t, StrH{}, res.Type)

	c.free([]llvm.Value{res.Val})
	c.builder.CreateRetVoid()

	ir := c.Module.String()
	require.Contains(t, ir, "cond_lhs_true", "StrH CondScalar should lower with explicit branching")
	require.Contains(t, ir, "cond_lhs_false", "StrH CondScalar should lower with explicit branching")
	require.NotContains(t, ir, "select i1", "StrH CondScalar should not use select")
}

func TestCanUseCondSelectWhitelist(t *testing.T) {
	require.True(t, canUseCondSelect(Int{Width: 64}))
	require.True(t, canUseCondSelect(Float{Width: 64}))
	require.True(t, canUseCondSelect(StrG{}))

	require.False(t, canUseCondSelect(StrH{}))
	require.False(t, canUseCondSelect(Array{ColTypes: []Type{I64}}))
}
