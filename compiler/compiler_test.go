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

func compileScriptAndCodeIR(t *testing.T, moduleName, codeSrc, scriptSrc string) (scriptIR string, codeIR string) {
	t.Helper()

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	var codeAST *ast.Code
	if strings.TrimSpace(codeSrc) == "" {
		codeAST = ast.NewCode()
	} else {
		codeAST = mustParseCode(t, codeSrc)
	}

	cc := NewCodeCompiler(ctx, moduleName, "", codeAST)
	program := mustParseScript(t, scriptSrc)

	funcCache := make(map[string]*Func)
	exprCache := cc.Compiler.ExprCache
	sc := NewScriptCompiler(ctx, program, cc, funcCache, exprCache)
	errs := sc.Compile()
	require.Empty(t, errs)

	return sc.Compiler.GenerateIR(), cc.Compiler.GenerateIR()
}

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

func TestAffineArrayIndexExprUsesVersionedLoop(t *testing.T) {
	script := `arr = [10 20 30 40 50]
i = 0:4
x = arr[i + 1] + 1
x`

	scriptIR, _ := compileScriptAndCodeIR(t, "affine_expr_emit", "", script)
	require.Contains(t, scriptIR, "idx_affine_all_safe", "expected affine safety predicate in script IR")
	require.Contains(t, scriptIR, "loop_affine_fast", "expected affine fast loop block in script IR")
	require.Contains(t, scriptIR, "loop_affine_checked", "expected affine checked loop block in script IR")
	require.NotContains(t, scriptIR, "arr_get_affine_fast", "versioned loop fast path should not branch per iteration")
}

func TestAffineArrayIndexExprNegativeStepUsesVersionedLoop(t *testing.T) {
	script := `arr = [10 20 30 40 50]
j = 4:0:-1
x = arr[j - 1] + 1
x`

	scriptIR, _ := compileScriptAndCodeIR(t, "affine_expr_emit_neg", "", script)
	require.Contains(t, scriptIR, "idx_affine_all_safe", "expected affine safety predicate in script IR")
	require.Contains(t, scriptIR, "loop_affine_fast", "expected affine fast loop block in script IR")
	require.Contains(t, scriptIR, "loop_affine_checked", "expected affine checked loop block in script IR")
	require.NotContains(t, scriptIR, "arr_get_affine_fast", "versioned loop fast path should not branch per iteration")
}

func TestAffineArrayIndexStmtInFuncUsesCheckedPath(t *testing.T) {
	code := `out = pick(arr, i)
    out = arr[i + 1]
`
	script := `arr = [10 20 30 40 50]
i = 0:4
out = pick(arr, i)
out`

	scriptIR, codeIR := compileScriptAndCodeIR(t, "affine_stmt_emit", code, script)
	combinedIR := scriptIR + "\n" + codeIR
	require.NotContains(t, combinedIR, "arr_get_affine_fast", "function-body indexing should use normal checked path")
	require.Contains(t, combinedIR, "idx_in_bounds", "function-body indexing should emit checked bounds predicate")
	require.Contains(t, combinedIR, "arr_get_oob", "function-body indexing should emit checked OOB block")
}

func TestNonAffineArrayIndexExprUsesCheckedPath(t *testing.T) {
	script := `arr = [10 20 30 40 50]
i = 0:2
x = arr[i * i - 2i + 3] + 1
x`

	scriptIR, _ := compileScriptAndCodeIR(t, "non_affine_expr_emit", "", script)
	require.NotContains(t, scriptIR, "idx_affine_all_safe", "non-affine index must not emit affine predicate")
	require.NotContains(t, scriptIR, "arr_get_affine_fast", "non-affine index must not emit affine fast block")
	require.Contains(t, scriptIR, "idx_in_bounds", "non-affine index should use checked bounds predicate")
	require.Contains(t, scriptIR, "arr_get_oob", "non-affine index should use checked OOB block")
}

func TestNonAffineArrayIndexStmtInFuncUsesCheckedPath(t *testing.T) {
	code := `out = pick_poly(arr, i)
    out = arr[i * i - 2i + 3]
`
	script := `arr = [10 20 30 40 50]
i = 0:2
out = pick_poly(arr, i)
out`

	scriptIR, codeIR := compileScriptAndCodeIR(t, "non_affine_stmt_emit", code, script)
	combinedIR := scriptIR + "\n" + codeIR
	require.NotContains(t, combinedIR, "idx_affine_all_safe", "non-affine index must not emit affine predicate")
	require.NotContains(t, combinedIR, "arr_get_affine_fast", "non-affine index must not emit affine fast block")
	require.Contains(t, combinedIR, "idx_in_bounds", "non-affine index should use checked bounds predicate")
	require.Contains(t, combinedIR, "arr_get_oob", "non-affine index should use checked OOB block")
}

func TestNonAffineModuloArrayIndexExprUsesCheckedPath(t *testing.T) {
	script := `arr = [10 20 30 40 50]
i = 0:5
x = arr[i % 3] + 1
x`

	scriptIR, _ := compileScriptAndCodeIR(t, "non_affine_mod_expr_emit", "", script)
	require.NotContains(t, scriptIR, "idx_affine_all_safe", "modulo index must not emit affine predicate")
	require.NotContains(t, scriptIR, "loop_affine_fast", "modulo index must not emit affine fast loop")
	require.Contains(t, scriptIR, "idx_in_bounds", "modulo index should use checked bounds predicate")
	require.Contains(t, scriptIR, "arr_get_oob", "modulo index should use checked OOB block")
}

func TestNonAffineQuotientArrayIndexExprUsesCheckedPath(t *testing.T) {
	script := `arr = [10 20 30 40 50]
i = 0:6
x = arr[i ÷ 2 + 1] + 1
x`

	scriptIR, _ := compileScriptAndCodeIR(t, "non_affine_quo_expr_emit", "", script)
	require.NotContains(t, scriptIR, "idx_affine_all_safe", "quotient index must not emit affine predicate")
	require.NotContains(t, scriptIR, "loop_affine_fast", "quotient index must not emit affine fast loop")
	require.Contains(t, scriptIR, "idx_in_bounds", "quotient index should use checked bounds predicate")
	require.Contains(t, scriptIR, "arr_get_oob", "quotient index should use checked OOB block")
}
