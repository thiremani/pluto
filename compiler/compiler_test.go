package compiler

import (
	"os"
	"os/exec"
	"path/filepath"
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

	codeAST := ast.NewCode()
	if strings.TrimSpace(codeSrc) != "" {
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

func TestPhase1ScalarABIDirectI64(t *testing.T) {
	code := `res = Add(x, y)
    res = x + y`
	script := `res = Add(2, 3)
res`

	moduleName := "phase1_scalar_abi_i64"
	scriptIR, _ := compileScriptAndCodeIR(t, moduleName, code, script)
	mangled := Mangle(MangleDirPath(moduleName, ""), "Add", []Type{I64, I64})

	require.Contains(t, scriptIR, "define noundef i64 @"+mangled+"(i64 noundef %0, i64 noundef %1)", "expected direct scalar signature with noundef attrs")
	require.Contains(t, scriptIR, "call i64 @"+mangled+"(i64 2, i64 3)", "expected direct scalar call")
	require.NotContains(t, scriptIR, mangled+"_ret", "single-scalar return should not use sret struct")
}

func TestPhase1ScalarABIDirectF64(t *testing.T) {
	code := `res = AddF(x, y)
    res = x + y`
	script := `res = AddF(2.5, 3.5)
res`

	moduleName := "phase1_scalar_abi_f64"
	scriptIR, _ := compileScriptAndCodeIR(t, moduleName, code, script)
	mangled := Mangle(MangleDirPath(moduleName, ""), "AddF", []Type{F64, F64})

	require.Contains(t, scriptIR, "define noundef double @"+mangled+"(double noundef %0, double noundef %1)", "expected direct float signature with noundef attrs")
	require.Contains(t, scriptIR, "call double @"+mangled+"(double 2.500000e+00, double 3.500000e+00)", "expected direct float call")
	require.NotContains(t, scriptIR, mangled+"_ret", "single-scalar float return should not use sret struct")
}

func TestPhase1ScalarABIMultiReturnKeepsIndirectReturn(t *testing.T) {
	code := `a, b = Pair(x, y)
    a, b = x, y`
	script := `a, b = Pair(2, 3)
a, b`

	moduleName := "phase1_scalar_abi_pair"
	scriptIR, _ := compileScriptAndCodeIR(t, moduleName, code, script)
	mangled := Mangle(MangleDirPath(moduleName, ""), "Pair", []Type{I64, I64})

	require.Contains(t, scriptIR, "define void @"+mangled+"(", "expected indirect multi-return definition")
	require.Contains(t, scriptIR, "ptr noundef nonnull sret(%"+mangled+"_ret) \"captures\"=\"none\" %0", "expected sret pointer attrs on the indirect return slot")
	require.Contains(t, scriptIR, "i64 noundef %1, i64 noundef %2", "expected noundef attrs on direct scalar params")
	require.Contains(t, scriptIR, "call void @"+mangled+"(", "expected indirect multi-return call")
}

func TestPhase1ScalarABIRangeVariantUsesDirectScalarBoundary(t *testing.T) {
	code := `res = Acc(a, x)
    res = a + x`
	script := `res = 10
res = Acc(res, 1:6)
res`

	moduleName := "phase1_scalar_abi_acc"
	scriptIR, _ := compileScriptAndCodeIR(t, moduleName, code, script)
	mangled := Mangle(MangleDirPath(moduleName, ""), "Acc", []Type{I64, Range{Iter: I64}})

	require.Contains(t, scriptIR, "define noundef i64 @"+mangled+"(", "range-bearing variant should keep the direct scalar return")
	require.Contains(t, scriptIR, "i64 noundef %0, ptr noundef nonnull \"captures\"=\"none\" %1, i32 noundef %2, i64 noundef %3", "range-bearing variant should keep the range indirect but lower scalar input/output directly with param attrs")
	require.Contains(t, scriptIR, "call i64 @"+mangled+"(", "expected direct scalar call/return for ranged accumulator case")
	require.NotContains(t, scriptIR, mangled+"_ret", "single-scalar range variant should not use sret struct")
}

func TestInferCallParamTypesReportsMissingArgType(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	c := NewCompiler(ctx, "call_param_type_error", nil)
	ce := &ast.CallExpression{
		Token:    token.Token{FileName: "test", Line: 1, Column: 1, Literal: "Add"},
		Function: &ast.Identifier{Token: token.Token{FileName: "test", Line: 1, Column: 1, Literal: "Add"}, Value: "Add"},
		Arguments: []ast.Expression{
			&ast.IntegerLiteral{Token: token.Token{FileName: "test", Line: 1, Column: 5, Literal: "1"}, Value: 1},
		},
	}

	paramTypes, ok := c.inferCallParamTypes(ce, &ExprInfo{})

	require.False(t, ok)
	require.Nil(t, paramTypes)
	require.Len(t, c.Errors, 1)
	require.Contains(t, c.Errors[0].Msg, "could not resolve type information for call argument")
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

func TestCompilerModuleTargetMetadata(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "target_metadata", "", ast.NewCode())

	triple, dataLayout := defaultModuleTargetMetadata()
	require.NotEmpty(t, triple, "expected default target triple to be available")
	require.Equal(t, triple, cc.Compiler.Module.Target())
	require.NotEmpty(t, cc.Compiler.Module.DataLayout())
	if dataLayout != "" {
		require.Equal(t, dataLayout, cc.Compiler.Module.DataLayout())
	}

	ir := cc.Compiler.GenerateIR()
	require.Contains(t, ir, `target triple = "`)
	require.Contains(t, ir, `target datalayout = "`)
}

func TestStructRepeatedDefs(t *testing.T) {
	codeA := mustParseCode(t, `p = Person
    :name age
    "Tejas" 35`)
	codeB := mustParseCode(t, `q = Person
    :name age
    "Ada" 28`)

	merged := ast.NewCode()
	merged.Merge(codeA)
	merged.Merge(codeB)

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "repeatedStructDefs", "", merged)
	errs := cc.Compile()
	require.Empty(t, errs, "expected same-order repeated struct defs to compile")
}

func TestStructAmbiguousFieldOrder(t *testing.T) {
	codeA := mustParseCode(t, `p = Person
    :name age
    "Tejas" 35`)
	codeB := mustParseCode(t, `q = Person
    :age name
    28 "Ada"`)

	merged := ast.NewCode()
	merged.Merge(codeA)
	merged.Merge(codeB)

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "ambiguousFieldOrder", "", merged)
	errs := cc.Compile()
	require.NotEmpty(t, errs, "expected field order error")

	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "fields reordered") {
			found = true
			break
		}
	}
	require.True(t, found, "expected field order error, got: %v", errs)
}

func TestStructUnknownField(t *testing.T) {
	codeA := mustParseCode(t, `p = Person
    :name age score
    "Tejas" 35 100`)
	codeB := mustParseCode(t, `q = Person
    :name height
    "Ada" 170`)

	merged := ast.NewCode()
	merged.Merge(codeA)
	merged.Merge(codeB)

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "unknownField", "", merged)
	errs := cc.Compile()
	require.NotEmpty(t, errs, "expected unknown field error")

	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), `field "height" not in struct type Person`) {
			found = true
			break
		}
	}
	require.True(t, found, "expected unknown field error, got: %v", errs)
}

func TestStructFieldTypeMismatch(t *testing.T) {
	codeA := mustParseCode(t, `p = Person
    :name age height
    "Tejas" 35 184.5`)
	codeB := mustParseCode(t, `q = Person
    :name age
    "Ada" 28.5`)

	merged := ast.NewCode()
	merged.Merge(codeA)
	merged.Merge(codeB)

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "typeMismatch", "", merged)
	errs := cc.Compile()
	require.NotEmpty(t, errs, "expected type mismatch error")

	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "expects I64") {
			found = true
			break
		}
	}
	require.True(t, found, "expected type mismatch error, got: %v", errs)
}

func TestStructSameArityConflictingTypes(t *testing.T) {
	codeA := mustParseCode(t, `p = Person
    :name age
    "Tejas" 35`)
	codeB := mustParseCode(t, `q = Person
    :name age
    35 "Ada"`)

	merged := ast.NewCode()
	merged.Merge(codeA)
	merged.Merge(codeB)

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "sameArityConflict", "", merged)
	errs := cc.Compile()
	require.NotEmpty(t, errs, "expected type conflict error for same-arity definitions")

	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), `struct field "name" expects`) {
			found = true
			break
		}
	}
	require.True(t, found, "expected field type conflict error, got: %v", errs)
}

func TestStructSubsetDefs(t *testing.T) {
	codeA := mustParseCode(t, `p = Person
    :name age height
    "Tejas" 35 184.5`)
	codeB := mustParseCode(t, `q = Person
    :age name
    28 "Ada"`)

	merged := ast.NewCode()
	merged.Merge(codeA)
	merged.Merge(codeB)

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "subsetStructDefs", "", merged)
	errs := cc.Compile()
	require.Empty(t, errs, "expected subset/reordered struct definition to compile")
}

func TestStructMaxHeaderDef(t *testing.T) {
	// Smaller statement first, larger definition second — larger wins.
	codeA := mustParseCode(t, `q = Person
    :age name
    28 "Ada"`)
	codeB := mustParseCode(t, `p = Person
    :name age height
    "Tejas" 35 184.5`)

	merged := ast.NewCode()
	merged.Merge(codeA)
	merged.Merge(codeB)

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "maxHeaderDef", "", merged)
	errs := cc.Compile()
	require.Empty(t, errs, "expected max-header definition to compile")

	schema, ok := cc.Compiler.StructCache["Person"]
	require.True(t, ok, "expected Person in StructCache")
	require.Len(t, schema.Fields, 3, "expected 3 fields from max-header definition")
}

func TestStructEmptyInit(t *testing.T) {
	code := mustParseCode(t, `p = Person
    :name age
    "Tejas" 35
q = Person`)

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "emptyStructInit", "", code)
	errs := cc.Compile()
	require.Empty(t, errs, "expected empty struct initializer to compile after definition")
}

func TestStructUseBeforeDef(t *testing.T) {
	// Build AST directly: parser now rejects this, so we test the compiler guard independently.
	code := ast.NewCode()
	code.Struct.Statements = append(code.Struct.Statements, &ast.StructStatement{
		Token: token.Token{Type: token.ASSIGN, Literal: "="},
		Name:  &ast.Identifier{Token: token.Token{Type: token.IDENT, Literal: "q"}, Value: "q"},
		Value: &ast.StructLiteral{
			Token: token.Token{Type: token.IDENT, Literal: "Person"},
		},
	})

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "structUseBeforeDef", "", code)
	errs := cc.Compile()
	require.NotEmpty(t, errs, "expected undefined struct type error")

	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "struct type Person has not been defined") {
			found = true
			break
		}
	}
	require.True(t, found, "expected undefined struct type error, got: %v", errs)
}

func TestStructReservedTypeName(t *testing.T) {
	// Build AST directly to bypass parser-side checks.
	code := ast.NewCode()
	code.Struct.Statements = append(code.Struct.Statements, &ast.StructStatement{
		Token: token.Token{Type: token.ASSIGN, Literal: "="},
		Name:  &ast.Identifier{Token: token.Token{Type: token.IDENT, Literal: "x"}, Value: "x"},
		Value: &ast.StructLiteral{
			Token:   token.Token{Type: token.IDENT, Literal: "Int"},
			Headers: []token.Token{{Type: token.IDENT, Literal: "val"}},
			Row:     []ast.Expression{&ast.IntegerLiteral{Token: token.Token{Type: token.INT, Literal: "1"}, Value: 1}},
		},
	})

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "reservedTypeName", "", code)
	errs := cc.Compile()
	require.NotEmpty(t, errs, "expected reserved type name error")

	require.Contains(t, errs[0].Error(), `struct type name "Int" is a reserved name`)
}

func TestReservedConstantName(t *testing.T) {
	code := ast.NewCode()
	code.Const.Statements = append(code.Const.Statements, &ast.ConstStatement{
		Token: token.Token{Type: token.ASSIGN, Literal: "="},
		Name:  []*ast.Identifier{{Token: token.Token{Type: token.IDENT, Literal: "Int"}, Value: "Int"}},
		Value: []ast.Expression{&ast.IntegerLiteral{Token: token.Token{Type: token.INT, Literal: "42"}, Value: 42}},
	})

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	errs := NewCodeCompiler(ctx, "reservedConst", "", code).Compile()
	require.NotEmpty(t, errs)
	require.Contains(t, errs[0].Error(), `constant name "Int" is a reserved name`)
}

func TestReservedFuncName(t *testing.T) {
	code := ast.NewCode()
	code.Func.Statements = append(code.Func.Statements, &ast.FuncStatement{
		Token: token.Token{Type: token.IDENT, Literal: "Float"},
	})

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	errs := NewCodeCompiler(ctx, "reservedFunc", "", code).Compile()
	require.NotEmpty(t, errs)
	require.Contains(t, errs[0].Error(), `function name "Float" is a reserved name`)
}

func TestReservedStructBindingName(t *testing.T) {
	code := ast.NewCode()
	code.Struct.Statements = append(code.Struct.Statements, &ast.StructStatement{
		Token: token.Token{Type: token.ASSIGN, Literal: "="},
		Name:  &ast.Identifier{Token: token.Token{Type: token.IDENT, Literal: "I64"}, Value: "I64"},
		Value: &ast.StructLiteral{
			Token:   token.Token{Type: token.IDENT, Literal: "Person"},
			Headers: []token.Token{{Type: token.IDENT, Literal: "name"}},
			Row:     []ast.Expression{&ast.StringLiteral{Token: token.Token{Type: token.STRING, Literal: "Tejas"}, Value: "Tejas"}},
		},
	})

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	errs := NewCodeCompiler(ctx, "reservedBinding", "", code).Compile()
	require.NotEmpty(t, errs)
	require.Contains(t, errs[0].Error(), `struct constant name "I64" is a reserved name`)
}

func TestReservedScriptVariableName(t *testing.T) {
	code := ast.NewCode()
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "reservedVar", "", code)
	cc.Compile()

	program := &ast.Program{
		Statements: []ast.Statement{
			&ast.LetStatement{
				Token: token.Token{Type: token.ASSIGN, Literal: "="},
				Name:  []*ast.Identifier{{Token: token.Token{Type: token.IDENT, Literal: "Str"}, Value: "Str"}},
				Value: []ast.Expression{&ast.IntegerLiteral{Token: token.Token{Type: token.INT, Literal: "42"}, Value: 42}},
			},
		},
	}

	sc := NewScriptCompiler(ctx, program, cc, cc.Compiler.FuncCache, cc.Compiler.ExprCache)
	errs := sc.Compile()
	require.NotEmpty(t, errs)
	require.Contains(t, errs[0].Error(), `variable name "Str" is a reserved name`)
}

func TestStructUnknownFieldNoSpuriousError(t *testing.T) {
	codeA := mustParseCode(t, `p = Person
    :name age
    "Tejas" 35`)
	codeB := mustParseCode(t, `q = Person
    :height age
    170 28`)

	merged := ast.NewCode()
	merged.Merge(codeA)
	merged.Merge(codeB)

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "unknownFieldNoSpurious", "", merged)
	errs := cc.Compile()
	require.NotEmpty(t, errs, "expected redefined struct error")

	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "struct Person redefined with different fields") {
			found = true
		}
		require.NotContains(t, err.Error(), "fields reordered",
			"should not emit spurious order error for redefined struct")
	}
	require.True(t, found, "expected redefined struct error, got: %v", errs)
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

func TestCallStatusCheckedEmitsTrap(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "status_checked_trap", "", ast.NewCode())
	c := NewCompiler(ctx, cc.Compiler.MangledPath, cc)

	fnType := llvm.FunctionType(ctx.VoidType(), nil, false)
	fn := llvm.AddFunction(c.Module, "status_checked_fn", fnType)
	entry := ctx.AddBasicBlock(fn, "entry")
	c.builder.SetInsertPointAtEnd(entry)

	failTy := llvm.FunctionType(ctx.Int32Type(), nil, false)
	failFn := llvm.AddFunction(c.Module, "always_fail_status", failTy)
	c.callStatusChecked(failTy, failFn, nil, "status_checked")
	c.builder.CreateRetVoid()

	ir := c.Module.String()
	require.Contains(t, ir, "call i32 @always_fail_status()", "expected status-returning call")
	require.Contains(t, ir, "status_checked_fail", "expected fail block for non-zero status")
	require.Contains(t, ir, "call void @llvm.trap()", "expected trap on runtime failure")
	require.Contains(t, ir, "unreachable", "expected fail block terminator")
}

func TestPushValOwnNullStrHEmitsTrapPath(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "status_checked_push_null", "", ast.NewCode())
	c := NewCompiler(ctx, cc.Compiler.MangledPath, cc)

	fnType := llvm.FunctionType(ctx.VoidType(), nil, false)
	fn := llvm.AddFunction(c.Module, "status_checked_push_fn", fnType)
	entry := ctx.AddBasicBlock(fn, "entry")
	c.builder.SetInsertPointAtEnd(entry)

	acc := c.NewArrayAccumulator(Array{ColTypes: []Type{StrH{}}})
	nullStr := llvm.ConstPointerNull(llvm.PointerType(c.Context.Int8Type(), 0))
	c.PushValOwn(acc, &Symbol{Val: nullStr, Type: StrH{}})
	c.builder.CreateRetVoid()

	ir := c.Module.String()
	require.Contains(t, ir, "call i32 @arr_str_push_own", "expected owned-string push call")
	require.Contains(t, ir, "ptr null", "expected injected null pointer argument")
	require.Contains(t, ir, "range_arr_push_own_fail", "expected status fail block for push_own")
	require.Contains(t, ir, "call void @llvm.trap()", "expected trap on push_own failure")
}

func TestCallStatusCheckedTrapExecutesNonZero(t *testing.T) {
	lliPath, err := exec.LookPath("lli")
	if err != nil {
		t.Skip("lli not found on PATH")
	}

	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "status_checked_exec_trap", "", ast.NewCode())
	c := NewCompiler(ctx, cc.Compiler.MangledPath, cc)

	failTy := llvm.FunctionType(ctx.Int32Type(), nil, false)
	failFn := llvm.AddFunction(c.Module, "always_fail_status_exec", failTy)
	failEntry := ctx.AddBasicBlock(failFn, "entry")
	c.builder.SetInsertPointAtEnd(failEntry)
	c.builder.CreateRet(llvm.ConstInt(ctx.Int32Type(), 1, false))

	mainTy := llvm.FunctionType(ctx.Int32Type(), nil, false)
	mainFn := llvm.AddFunction(c.Module, "main", mainTy)
	mainEntry := ctx.AddBasicBlock(mainFn, "entry")
	c.builder.SetInsertPointAtEnd(mainEntry)
	c.callStatusChecked(failTy, failFn, nil, "status_checked_exec")
	c.builder.CreateRet(llvm.ConstInt(ctx.Int32Type(), 0, false))

	tempDir := t.TempDir()
	irPath := filepath.Join(tempDir, "trap_exec.ll")
	require.NoError(t, os.WriteFile(irPath, []byte(c.Module.String()), 0o644))

	cmd := exec.Command(lliPath, irPath)
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("expected non-zero exit from trap path, got success. output:\n%s", string(out))
	}

	exitErr, ok := runErr.(*exec.ExitError)
	require.True(t, ok, "expected ExitError for non-zero process exit")
	require.NotEqual(t, 0, exitErr.ExitCode(), "trap path should not exit with status 0")
}
