package compiler

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thiremani/pluto/ast"
	"tinygo.org/x/go-llvm"
)

func requireTargetEffects(t *testing.T, effect StatementEffect, expected ...TargetWriteEffect) {
	t.Helper()

	require.Equal(t, expected, effect.Writes)
}

func TestValidStatementEffectShape(t *testing.T) {
	program := mustParseScript(t, "a, _, b = 1, 2, 3")
	stmt := program.Statements[0].(*ast.LetStatement)
	// This helper validates the published target shape; it does not derive
	// semantic effects from the statement's RHS.
	directWrites := []TargetWriteEffect{
		{TargetIndex: 0, Effect: MustWrite},
		{TargetIndex: 2, Effect: MustWrite},
	}
	mixedWrites := []TargetWriteEffect{
		{TargetIndex: 0, Effect: MustWrite},
		{TargetIndex: 2, Effect: MayWrite},
	}
	tests := []struct {
		name            string
		writes          []TargetWriteEffect
		readsSeed       []int
		calleeReadsSeed []int
		valid           bool
	}{
		{name: "valid direct writes", writes: directWrites, valid: true},
		{name: "valid mixed write effects", writes: mixedWrites, valid: true},
		{name: "valid seed targets", writes: directWrites, readsSeed: []int{0, 2}, valid: true},
		{name: "valid callee seed targets", writes: mixedWrites, calleeReadsSeed: []int{0, 2}, valid: true},
		{name: "valid overlapping seed facts", writes: directWrites, readsSeed: []int{0}, calleeReadsSeed: []int{0}, valid: true},
		{name: "missing named target", writes: directWrites[:1]},
		{name: "write targets discard", writes: []TargetWriteEffect{{TargetIndex: 0, Effect: MustWrite}, {TargetIndex: 1, Effect: MustWrite}}},
		{name: "invalid write state", writes: []TargetWriteEffect{{TargetIndex: 0, Effect: MustWrite}, {TargetIndex: 2, Effect: WriteInvalid}}},
		{name: "duplicate seed target", writes: directWrites, readsSeed: []int{0, 0}},
		{name: "seed targets discard", writes: directWrites, readsSeed: []int{1}},
		{name: "descending callee seed targets", writes: directWrites, calleeReadsSeed: []int{2, 0}},
		{name: "callee seed targets discard", writes: directWrites, calleeReadsSeed: []int{1}},
		{name: "callee seed target out of range", writes: directWrites, calleeReadsSeed: []int{3}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			effect := StatementEffect{Writes: test.writes, ReadsSeed: test.readsSeed, CalleeReadsSeed: test.calleeReadsSeed}

			require.Equal(t, test.valid, validStatementEffect(stmt, effect))
		})
	}
}

func TestRewriteExprInfoDoesNotCopySourceYieldEffects(t *testing.T) {
	source := &ExprInfo{OutTypes: []Type{I64}, YieldEffects: []YieldEffect{MustYield}}
	rewrite := &ast.IntegerLiteral{}

	cloned := cloneExprInfoWithRewrite(source, rewrite)

	require.Equal(t, []Type{I64}, cloned.OutTypes)
	require.Same(t, rewrite, cloned.Rewrite)
	require.Nil(t, cloned.YieldEffects)
	require.Equal(t, []YieldEffect{MustYield}, source.YieldEffects)
}

func TestYieldSlotRejectsEmptyEffects(t *testing.T) {
	require.PanicsWithValue(t, "internal: cannot select from empty yield effects", func() {
		yieldSlot(nil, 0)
	})
}

func TestStatementEffectsStayAlignedAcrossMixedRHSAndDiscard(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "mixedEffects", "", mustParseCode(t, ""))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `arr = [10 20]
i = 0
x, _, y = arr[i], arr[i], i + 1
x, y`)

	stmt := ts.ScriptCompiler.Program.Statements[2].(*ast.LetStatement)
	effect := ts.ScriptCompiler.Script.Root.StatementEffects[stmt]

	requireTargetEffects(t, effect,
		TargetWriteEffect{TargetIndex: 0, Effect: MayWrite},
		TargetWriteEffect{TargetIndex: 2, Effect: MustWrite},
	)
	require.Empty(t, effect.ReadsSeed)

	firstAccess := stmt.Value[0].(*ast.ArrayRangeExpression)
	secondAccess := stmt.Value[1].(*ast.ArrayRangeExpression)

	require.Equal(t, []YieldEffect{MayYield}, ts.ExprCache[key(ts.FuncNameMangled, firstAccess)].YieldEffects)
	require.Equal(t, []YieldEffect{MayYield}, ts.ExprCache[key(ts.FuncNameMangled, secondAccess)].YieldEffects)
	require.Equal(t, []YieldEffect{MustYield}, ts.ExprCache[key(ts.FuncNameMangled, stmt.Value[2])].YieldEffects)
}

func TestFallbackYieldEffectUsesFinalAlternative(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "fallbackEffects", "", mustParseCode(t, ""))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `resolved = 1 > 0 || 2
unresolved = 1 > 0 || 2 > 0
resolved, unresolved`)

	resolved := ts.ScriptCompiler.Program.Statements[0].(*ast.LetStatement)
	unresolved := ts.ScriptCompiler.Program.Statements[1].(*ast.LetStatement)

	requireTargetEffects(t, ts.ScriptCompiler.Script.Root.StatementEffects[resolved], TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	requireTargetEffects(t, ts.ScriptCompiler.Script.Root.StatementEffects[unresolved], TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
	require.Equal(t, []YieldEffect{MustYield}, ts.ExprCache[key(ts.FuncNameMangled, resolved.Value[0])].YieldEffects)
	require.Equal(t, []YieldEffect{MayYield}, ts.ExprCache[key(ts.FuncNameMangled, unresolved.Value[0])].YieldEffects)
}

func TestArrayMaskPreservesOperandYieldEffect(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "arrayMaskEffects", "", mustParseCode(t, ""))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `matrix = [
    1 2
    3 4
]
safe = matrix > 0
outOfBounds = matrix[2] > 0
safe, outOfBounds`)

	safe := ts.ScriptCompiler.Program.Statements[1].(*ast.LetStatement)
	outOfBounds := ts.ScriptCompiler.Program.Statements[2].(*ast.LetStatement)

	requireTargetEffects(t, ts.ScriptCompiler.Script.Root.StatementEffects[safe], TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Equal(t, []YieldEffect{MustYield}, ts.ExprCache[key(ts.FuncNameMangled, safe.Value[0])].YieldEffects)
	requireTargetEffects(t, ts.ScriptCompiler.Script.Root.StatementEffects[outOfBounds], TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
	require.Equal(t, []YieldEffect{MayYield}, ts.ExprCache[key(ts.FuncNameMangled, outOfBounds.Value[0])].YieldEffects)
}

func TestLiteralDomainEffectUsesProvableEmptiness(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "literalDomainEffects", "", mustParseCode(t, ""))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `nonempty = 5 + 1:3
empty = 5 + 0:0
nonempty, empty`)

	nonempty := ts.ScriptCompiler.Program.Statements[0].(*ast.LetStatement)
	empty := ts.ScriptCompiler.Program.Statements[1].(*ast.LetStatement)

	requireTargetEffects(t, ts.ScriptCompiler.Script.Root.StatementEffects[nonempty], TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	requireTargetEffects(t, ts.ScriptCompiler.Script.Root.StatementEffects[empty], TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
}

func TestDirectCallSeedEffects(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "callEffects", "", mustParseCode(t, `y = Always(x)
    y = x

y = Maybe(x)
    y = x > 0 x`))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `seeded = 7
seeded = Maybe(1)
fresh = Maybe(1)
always = 4
always = Always(1)
arr = [1]
failed = 9
failed = Always(arr[2])
gated = 8
gated = 1 > 0 Maybe(1)
seeded, always, failed, gated`)

	program := ts.ScriptCompiler.Program
	seeded := program.Statements[1].(*ast.LetStatement)
	fresh := program.Statements[2].(*ast.LetStatement)
	always := program.Statements[4].(*ast.LetStatement)
	failed := program.Statements[7].(*ast.LetStatement)
	gated := program.Statements[9].(*ast.LetStatement)

	seededEffect := ts.ScriptCompiler.Script.Root.StatementEffects[seeded]
	requireTargetEffects(t, seededEffect, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Equal(t, []int{0}, seededEffect.ReadsSeed)
	require.Empty(t, seededEffect.CalleeReadsSeed)
	require.Equal(t, []YieldEffect{MayYield}, ts.ExprCache[key(ts.FuncNameMangled, seeded.Value[0])].YieldEffects)

	freshEffect := ts.ScriptCompiler.Script.Root.StatementEffects[fresh]
	requireTargetEffects(t, freshEffect, TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
	require.Empty(t, freshEffect.ReadsSeed)

	alwaysEffect := ts.ScriptCompiler.Script.Root.StatementEffects[always]
	requireTargetEffects(t, alwaysEffect, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Empty(t, alwaysEffect.ReadsSeed)

	failedEffect := ts.ScriptCompiler.Script.Root.StatementEffects[failed]
	requireTargetEffects(t, failedEffect, TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
	require.Empty(t, failedEffect.ReadsSeed)

	gatedEffect := ts.ScriptCompiler.Script.Root.StatementEffects[gated]
	requireTargetEffects(t, gatedEffect, TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
	require.Equal(t, []int{0}, gatedEffect.ReadsSeed)

	maybeFunc := cc.Compiler.FuncCache[Mangle(cc.Compiler.MangledPath, "Maybe", []Type{I64})]
	alwaysFunc := cc.Compiler.FuncCache[Mangle(cc.Compiler.MangledPath, "Always", []Type{I64})]

	require.Equal(t, []WriteEffect{MayWrite}, maybeFunc.BodyOutputEffects)
	require.Equal(t, []WriteEffect{MustWrite}, alwaysFunc.BodyOutputEffects)
	// Skipping a write leaves preservation to the caller; neither body reads
	// its incoming value.
	require.Equal(t, []SeedEffect{NoSeedRead}, maybeFunc.BodySeedEffects)
	require.Equal(t, []SeedEffect{NoSeedRead}, alwaysFunc.BodySeedEffects)
	require.True(t, maybeFunc.Settled)
	require.True(t, alwaysFunc.Settled)
}

func TestFunctionOutputIsNotInitiallyWritten(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "wrapperEffects", "", mustParseCode(t, `y = Maybe(x)
    y = x > 0 x

y = Wrap(x)
    y = Maybe(x)
    y = Maybe(x)`))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `fresh = Wrap(1)
existing = 7
existing = Wrap(1)
fresh, existing`)

	wrap := cc.Compiler.FuncCache[Mangle(cc.Compiler.MangledPath, "Wrap", []Type{I64})]

	require.NotNil(t, wrap)
	require.Equal(t, []WriteEffect{MayWrite}, wrap.BodyOutputEffects)
	// The inner boundary resolution only passes the seed through; nothing in
	// the body consumes it.
	require.Equal(t, []SeedEffect{NoSeedRead}, wrap.BodySeedEffects)

	template, ok := cc.lookupFuncTemplate("Wrap", 1)
	require.True(t, ok)
	firstBodyStmt := template.Body.Statements[0].(*ast.LetStatement)
	firstBodyEffect := wrap.StatementEffects[firstBodyStmt]
	requireTargetEffects(t, firstBodyEffect, TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
	require.Empty(t, firstBodyEffect.ReadsSeed)
	require.Empty(t, firstBodyEffect.CalleeReadsSeed)

	secondBodyStmt := template.Body.Statements[1].(*ast.LetStatement)
	secondBodyEffect := wrap.StatementEffects[secondBodyStmt]
	requireTargetEffects(t, secondBodyEffect, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Equal(t, []int{0}, secondBodyEffect.ReadsSeed)
	require.Empty(t, secondBodyEffect.CalleeReadsSeed)

	fresh := ts.ScriptCompiler.Program.Statements[0].(*ast.LetStatement)
	freshEffect := ts.ScriptCompiler.Script.Root.StatementEffects[fresh]
	requireTargetEffects(t, freshEffect, TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
	require.Empty(t, freshEffect.ReadsSeed)

	existing := ts.ScriptCompiler.Program.Statements[2].(*ast.LetStatement)
	existingEffect := ts.ScriptCompiler.Script.Root.StatementEffects[existing]
	requireTargetEffects(t, existingEffect, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Equal(t, []int{0}, existingEffect.ReadsSeed)
}

func TestCallDomainComposesWithBodyOutputEffects(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "domainEffects", "", mustParseCode(t, `y = Increment(x)
    y = x + 1`))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `existing = 9
existing = Increment(0:0)
empty = Increment(0:0)
nonempty = Increment(0:2)
existing, empty, nonempty`)

	mangled := Mangle(cc.Compiler.MangledPath, "Increment", []Type{Range{Iter: I64}})
	increment := cc.Compiler.FuncCache[mangled]

	require.NotNil(t, increment)
	require.Equal(t, []WriteEffect{MustWrite}, increment.BodyOutputEffects)

	template, ok := cc.lookupFuncTemplate("Increment", 1)
	require.True(t, ok)
	bodyStmt := template.Body.Statements[0].(*ast.LetStatement)
	requireTargetEffects(t, increment.StatementEffects[bodyStmt], TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})

	existing := ts.ScriptCompiler.Program.Statements[1].(*ast.LetStatement)
	existingEffect := ts.ScriptCompiler.Script.Root.StatementEffects[existing]
	requireTargetEffects(t, existingEffect, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Equal(t, []int{0}, existingEffect.ReadsSeed)

	empty := ts.ScriptCompiler.Program.Statements[2].(*ast.LetStatement)
	emptyEffect := ts.ScriptCompiler.Script.Root.StatementEffects[empty]
	requireTargetEffects(t, emptyEffect, TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
	require.Empty(t, emptyEffect.ReadsSeed)
	require.Equal(t, []YieldEffect{MayYield}, ts.ExprCache[key(ts.FuncNameMangled, empty.Value[0])].YieldEffects)

	nonempty := ts.ScriptCompiler.Program.Statements[3].(*ast.LetStatement)
	nonemptyEffect := ts.ScriptCompiler.Script.Root.StatementEffects[nonempty]
	requireTargetEffects(t, nonemptyEffect, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Empty(t, nonemptyEffect.ReadsSeed)
	require.Equal(t, []YieldEffect{MustYield}, ts.ExprCache[key(ts.FuncNameMangled, nonempty.Value[0])].YieldEffects)

	nonemptyInfo := ts.ExprCache[key(ts.FuncNameMangled, nonempty.Value[0])]
	require.NotEqual(t, nonempty.Value[0], nonemptyInfo.Rewrite)

	rewriteInfo := ts.ExprCache[key(ts.FuncNameMangled, nonemptyInfo.Rewrite)]

	require.NotNil(t, rewriteInfo)
	require.Nil(t, rewriteInfo.YieldEffects)
}

func TestStatementConditionDomainIsNotCallOwned(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "conditionDomainEffects", "", mustParseCode(t, `y = Increment(x)
    y = x + 1`))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `i = 0:3
existing = 9
existing = i > 0 Increment(0:2)
existing = i > 0 Increment(i)
existing`)

	for _, statementIndex := range []int{2, 3} {
		stmt := ts.ScriptCompiler.Program.Statements[statementIndex].(*ast.LetStatement)
		effect := ts.ScriptCompiler.Script.Root.StatementEffects[stmt]
		requireTargetEffects(t, effect, TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
		require.Empty(t, effect.ReadsSeed)
	}
}

func TestConditionalBodyRemainsMayWriteAcrossNonemptyDomain(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "conditionalDomainEffects", "", mustParseCode(t, `y = ConditionalSquare(x)
    y = x > 5 x * x`))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `fresh = ConditionalSquare(0:5)
existing = 9
existing = ConditionalSquare(0:5)
fresh, existing`)

	mangled := Mangle(cc.Compiler.MangledPath, "ConditionalSquare", []Type{Range{Iter: I64}})
	conditional := cc.Compiler.FuncCache[mangled]

	require.NotNil(t, conditional)
	require.Equal(t, []WriteEffect{MayWrite}, conditional.BodyOutputEffects)

	fresh := ts.ScriptCompiler.Program.Statements[0].(*ast.LetStatement)
	freshEffect := ts.ScriptCompiler.Script.Root.StatementEffects[fresh]
	requireTargetEffects(t, freshEffect, TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
	require.Empty(t, freshEffect.ReadsSeed)

	existing := ts.ScriptCompiler.Program.Statements[2].(*ast.LetStatement)
	existingEffect := ts.ScriptCompiler.Script.Root.StatementEffects[existing]
	requireTargetEffects(t, existingEffect, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Equal(t, []int{0}, existingEffect.ReadsSeed)
}

func TestRecursiveBodyOutputEffectsConvergeAcrossSCC(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "recursiveEffects", "", mustParseCode(t, `y = A(n)
    y = B(n)

y = B(n)
    y = n > 0 A(n - 1)
    y = n == 0 "done"`))
	require.Empty(t, cc.Compile())

	solveScriptTypes(t, ctx, cc, t.Name(), `result = A(2)
result`)

	a := cc.Compiler.FuncCache[Mangle(cc.Compiler.MangledPath, "A", []Type{I64})]
	b := cc.Compiler.FuncCache[Mangle(cc.Compiler.MangledPath, "B", []Type{I64})]

	require.Equal(t, []WriteEffect{MayWrite}, a.BodyOutputEffects)
	require.Equal(t, []WriteEffect{MayWrite}, b.BodyOutputEffects)
	require.True(t, a.Settled)
	require.True(t, b.Settled)
}

func TestCallGraphWalkOrderAndEdgeViews(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "effectGraphIDs", "", mustParseCode(t, `y = Z(x)
    y = B(x)
    C(x)
    y = B(x)

y = B(x)
    y = C(x)

y = C(x)
    y = x`))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `result = Z(1)
result`)

	walkOrder := []string{
		Mangle(cc.Compiler.MangledPath, "Z", []Type{I64}),
		Mangle(cc.Compiler.MangledPath, "B", []Type{I64}),
		Mangle(cc.Compiler.MangledPath, "C", []Type{I64}),
	}

	graph := ts.buildSpecializationCallGraph()

	for index, name := range walkOrder {
		id := specializationNodeID(index)
		require.Equal(t, id, graph.byMangled[name])
		require.Equal(t, name, graph.nodes[id].mangled)
	}

	zID := graph.byMangled[Mangle(cc.Compiler.MangledPath, "Z", []Type{I64})]
	bID := graph.byMangled[Mangle(cc.Compiler.MangledPath, "B", []Type{I64})]
	cID := graph.byMangled[Mangle(cc.Compiler.MangledPath, "C", []Type{I64})]

	require.Equal(t, []specializationNodeID{bID, cID}, graph.nodes[zID].effectCallees)
	require.Equal(t, []specializationNodeID{zID}, graph.nodes[bID].effectCallers)
	require.Equal(t, []specializationNodeID{cID}, graph.nodes[bID].effectCallees)
	require.Equal(t, []specializationNodeID{zID, bID}, graph.nodes[cID].effectCallers)
	require.Equal(t, [][]specializationNodeID{{cID}, {bID}, {zID}}, graph.calleeFirstComponents())
	require.Equal(t, []string{walkOrder[1], walkOrder[2]}, graph.nodes[zID].directCallees)
	require.Equal(t, []string{walkOrder[2]}, graph.nodes[bID].directCallees)
	require.Empty(t, graph.nodes[cID].directCallees)
}

func TestCallGraphExcludesScalarEffectEdge(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "scalarCompanionEdges", "", mustParseCode(t, `result = Gather(arr)
    i = 0:3
    result = [Scale(arr[i])]

result = Scale(x)
    result = x`))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `arr = [10 20 30]
result = Gather(arr)
result`)
	gatherMangled := Mangle(cc.Compiler.MangledPath, "Gather", []Type{Array{ElemType: I64, Rank: 1}})
	gatherTemplate, ok := cc.lookupFuncTemplate("Gather", 1)
	require.True(t, ok)
	gatherCall := gatherTemplate.Body.Statements[1].(*ast.LetStatement).Value[0].(*ast.ArrayLiteral).Rows[0][0].(*ast.CallExpression)
	callInfo := ts.ExprCache[key(gatherMangled, gatherCall)]
	require.True(t, callInfo.ScalarCallVariantEnsured)

	primaryMangled := Mangle(cc.Compiler.MangledPath, "Scale", callInfo.CallParamTypes)
	scalarMangled := Mangle(cc.Compiler.MangledPath, "Scale", callInfo.ScalarCallParamTypes)
	require.NotEqual(t, primaryMangled, scalarMangled)

	graph := ts.buildSpecializationCallGraph()
	gatherID, gatherInBatch := graph.byMangled[gatherMangled]
	primaryID, primaryInBatch := graph.byMangled[primaryMangled]
	_, scalarInBatch := graph.byMangled[scalarMangled]
	require.True(t, gatherInBatch)
	require.True(t, primaryInBatch)
	require.True(t, scalarInBatch)
	require.Equal(t, []specializationNodeID{primaryID}, graph.nodes[gatherID].effectCallees)
	require.Equal(t, []string{primaryMangled, scalarMangled}, graph.nodes[gatherID].directCallees)
	require.Equal(t, []string{primaryMangled, scalarMangled}, cc.Compiler.FuncCache[gatherMangled].CFGResult.DirectCallees)
}

func TestScriptEffectsRejectInvalidExpressionFacts(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "invalidScriptEffects", "", mustParseCode(t, ""))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `value = 1
value`)

	stmt := ts.ScriptCompiler.Program.Statements[0].(*ast.LetStatement)
	ts.ExprCache[key(ts.FuncNameMangled, stmt.Value[0])].OutTypes = []Type{Unresolved{}}

	require.PanicsWithValue(t,
		`internal: invalid effects for script statement "value = 1"`,
		ts.deriveScriptEffects,
	)
}

func TestMayWriteCallPreservesInvalidInvocationEffect(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "invalidCallEffects", "", mustParseCode(t, `y = Maybe(x)
    y = x > 0 x`))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `value = Maybe(1 + 1)
value`)

	stmt := ts.ScriptCompiler.Program.Statements[0].(*ast.LetStatement)
	call := stmt.Value[0].(*ast.CallExpression)
	argument := call.Arguments[0]
	ts.ExprCache[key(ts.FuncNameMangled, argument)].OutTypes = []Type{Unresolved{}}

	analyzer := newEffectAnalyzer(ts.ScriptCompiler.Compiler, ts.ScriptCompiler.ScriptMangled, nil, nil)

	require.Equal(t, []YieldEffect{YieldInvalid}, analyzer.deriveExpr(call))
}

// requireBodyEffects checks the published write and seed facts of every
// settled specialization named name; a ranged call also settles the scalar
// companion, and both summarize the same body.
func requireBodyEffects(t *testing.T, cc *CodeCompiler, name string, writes []WriteEffect, seeds []SeedEffect) {
	t.Helper()

	found := 0
	for mangled, f := range cc.Compiler.FuncCache {
		if f.Sig.Name != name {
			continue
		}
		found++
		require.True(t, f.Settled, mangled)
		require.Equal(t, writes, f.BodyOutputEffects, mangled)
		require.Equal(t, seeds, f.BodySeedEffects, mangled)
	}
	require.NotZero(t, found, "missing specialization of %s", name)
}

func scriptStatementEffect(t *testing.T, ts *TypeSolver, index int) StatementEffect {
	t.Helper()

	stmt := ts.ScriptCompiler.Program.Statements[index].(*ast.LetStatement)
	effect, exists := ts.ScriptCompiler.Script.Root.StatementEffects[stmt]
	require.True(t, exists)
	return effect
}

func bodyStatementEffect(t *testing.T, cc *CodeCompiler, name string, index int) StatementEffect {
	t.Helper()

	template, ok := cc.lookupFuncTemplate(name, 1)
	require.True(t, ok)
	stmt := template.Body.Statements[index].(*ast.LetStatement)

	for _, f := range cc.Compiler.FuncCache {
		if f.Sig.Name != name {
			continue
		}
		effect, exists := f.StatementEffects[stmt]
		require.True(t, exists)
		return effect
	}

	require.Fail(t, "missing specialization", name)
	return StatementEffect{}
}

func TestSeedDependentMustWriteReadsDestination(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "seedDependentEffects", "", mustParseCode(t, `y = MaybeIncrement(x)
    y = x > 0 x
    y = y + 1

y = Overwrite(x)
    y = x
    y = y + 1`))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `seeded = 20
seeded = MaybeIncrement(-1)
fresh = MaybeIncrement(-1)
replaced = 7
replaced = Overwrite(3)
seeded, fresh, replaced`)

	// The write effect is the same; only the seed dependency differs.
	requireBodyEffects(t, cc, "MaybeIncrement", []WriteEffect{MustWrite}, []SeedEffect{MaySeedRead})
	requireBodyEffects(t, cc, "Overwrite", []WriteEffect{MustWrite}, []SeedEffect{NoSeedRead})

	seeded := scriptStatementEffect(t, ts, 1)
	requireTargetEffects(t, seeded, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Empty(t, seeded.ReadsSeed)
	require.Equal(t, []int{0}, seeded.CalleeReadsSeed)

	// The fact describes the callee; a fresh destination has no value for the
	// CFG to read.
	fresh := scriptStatementEffect(t, ts, 2)
	requireTargetEffects(t, fresh, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Empty(t, fresh.ReadsSeed)
	require.Equal(t, []int{0}, fresh.CalleeReadsSeed)

	replaced := scriptStatementEffect(t, ts, 4)
	requireTargetEffects(t, replaced, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Empty(t, replaced.ReadsSeed)
	require.Empty(t, replaced.CalleeReadsSeed)
}

func TestSeedReadSurvivesCopyConditionAndPrint(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "stickySeedEffects", "", mustParseCode(t, `y = CopyThenReplace(x)
    y = x > 0 x
    saved = y
    y = x
    y = y + saved

y = GatedRead(x)
    y = x > 0 x
    y = y > 0 x
    y = x

y = PrintedRead(x)
    y = x > 0 x
    y
    y = x

y = MarkerRead(x)
    y = x > 0 x
    "seed -y"
    y = x

y = WidthRead(x)
    y = x > 0 x
    "-x%(-y)d"
    y = x`))
	require.Empty(t, cc.Compile())

	solveScriptTypes(t, ctx, cc, t.Name(), `a = CopyThenReplace(1)
b = GatedRead(1)
c = PrintedRead(1)
d = MarkerRead(1)
e = WidthRead(1)
a, b, c, d, e`)

	for _, name := range []string{"CopyThenReplace", "GatedRead", "PrintedRead", "MarkerRead", "WidthRead"} {
		requireBodyEffects(t, cc, name, []WriteEffect{MustWrite}, []SeedEffect{MaySeedRead})
	}
}

func TestNestedSeedReadsCompose(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "nestedSeedEffects", "", mustParseCode(t, `y = MaybeIncrement(x)
    y = x > 0 x
    y = y + 1

y = Outer(x)
    y = MaybeIncrement(x)

y = ResetThenIncrement(x)
    y = 0
    y = MaybeIncrement(x)`))
	require.Empty(t, cc.Compile())

	solveScriptTypes(t, ctx, cc, t.Name(), `a = Outer(1)
b = ResetThenIncrement(1)
a, b`)

	requireBodyEffects(t, cc, "Outer", []WriteEffect{MustWrite}, []SeedEffect{MaySeedRead})
	requireBodyEffects(t, cc, "ResetThenIncrement", []WriteEffect{MustWrite}, []SeedEffect{NoSeedRead})

	// The output is unpublished at its first statement, so the callee's read
	// is recorded without a boundary resolution.
	outerCall := bodyStatementEffect(t, cc, "Outer", 0)
	requireTargetEffects(t, outerCall, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Empty(t, outerCall.ReadsSeed)
	require.Equal(t, []int{0}, outerCall.CalleeReadsSeed)

	// The inner call reads the reset value, which keeps that write live.
	resetCall := bodyStatementEffect(t, cc, "ResetThenIncrement", 1)
	requireTargetEffects(t, resetCall, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Empty(t, resetCall.ReadsSeed)
	require.Equal(t, []int{0}, resetCall.CalleeReadsSeed)
}

func TestCrossOutputSeedReads(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "crossOutputSeedEffects", "", mustParseCode(t, `a, b = Cross(x)
    a = x > 0 x
    b = a + 1
    a = x`))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `p = 20
p, q = Cross(-1)
p, q`)

	requireBodyEffects(t, cc, "Cross", []WriteEffect{MustWrite, MustWrite}, []SeedEffect{MaySeedRead, NoSeedRead})

	call := scriptStatementEffect(t, ts, 1)
	requireTargetEffects(t, call,
		TargetWriteEffect{TargetIndex: 0, Effect: MustWrite},
		TargetWriteEffect{TargetIndex: 1, Effect: MustWrite},
	)
	require.Empty(t, call.ReadsSeed)
	require.Equal(t, []int{0}, call.CalleeReadsSeed)
}

func TestRecursiveSeedReadsConvergeAcrossSCC(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	// A reads its seed only through B, so the fact must grow across the
	// component after B's own read is discovered.
	cc := NewCodeCompiler(ctx, "recursiveSeedEffects", "", mustParseCode(t, `y = A(n)
    y = B(n)

y = B(n)
    y = n == 0 1
    y = n > 0 A(n - 1)
    y = y + 1`))
	require.Empty(t, cc.Compile())

	solveScriptTypes(t, ctx, cc, t.Name(), `result = A(2)
result`)

	requireBodyEffects(t, cc, "A", []WriteEffect{MustWrite}, []SeedEffect{MaySeedRead})
	requireBodyEffects(t, cc, "B", []WriteEffect{MustWrite}, []SeedEffect{MaySeedRead})
}

func TestIndirectCalleeSeedReadsCompose(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "indirectSeedEffects", "", mustParseCode(t, `s = MaybeTag(n, t)
    s = n > 0 t
    s = s ⊕ "!"

s = GatedTag(n, t)
    s = n > 0 t
    s = n > 1 s ⊕ "!"`))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `w = "hi"
w = MaybeTag(-1, "x")
v = "hi"
v = GatedTag(-1, "x")
w, v`)

	requireBodyEffects(t, cc, "MaybeTag", []WriteEffect{MustWrite}, []SeedEffect{MaySeedRead})
	requireBodyEffects(t, cc, "GatedTag", []WriteEffect{MayWrite}, []SeedEffect{MaySeedRead})

	// Indirect outputs never resolve at the boundary, but the dependency on
	// the destination-seeded staging slot composes all the same.
	mustWrite := scriptStatementEffect(t, ts, 1)
	requireTargetEffects(t, mustWrite, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Empty(t, mustWrite.ReadsSeed)
	require.Equal(t, []int{0}, mustWrite.CalleeReadsSeed)

	mayWrite := scriptStatementEffect(t, ts, 3)
	requireTargetEffects(t, mayWrite, TargetWriteEffect{TargetIndex: 0, Effect: MayWrite})
	require.Empty(t, mayWrite.ReadsSeed)
	require.Equal(t, []int{0}, mayWrite.CalleeReadsSeed)
}

func TestFunctionDomainSeedReadsKeepBoundaryFacts(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	cc := NewCodeCompiler(ctx, "domainSeedEffects", "", mustParseCode(t, `y = AccRange(r)
    y = r > 5 r
    y = y + r`))
	require.Empty(t, cc.Compile())

	ts := solveScriptTypes(t, ctx, cc, t.Name(), `existing = 20
existing = AccRange(0:3)
empty = 20
empty = AccRange(0:0)
fresh = AccRange(0:3)
existing, empty, fresh`)

	requireBodyEffects(t, cc, "AccRange", []WriteEffect{MustWrite}, []SeedEffect{MaySeedRead})

	existing := scriptStatementEffect(t, ts, 1)
	requireTargetEffects(t, existing, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Empty(t, existing.ReadsSeed)
	require.Equal(t, []int{0}, existing.CalleeReadsSeed)

	// A possibly empty call-owned domain still resolves at the boundary; the
	// two facts coexist on one target.
	empty := scriptStatementEffect(t, ts, 3)
	requireTargetEffects(t, empty, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Equal(t, []int{0}, empty.ReadsSeed)
	require.Equal(t, []int{0}, empty.CalleeReadsSeed)

	fresh := scriptStatementEffect(t, ts, 4)
	requireTargetEffects(t, fresh, TargetWriteEffect{TargetIndex: 0, Effect: MustWrite})
	require.Empty(t, fresh.ReadsSeed)
	require.Equal(t, []int{0}, fresh.CalleeReadsSeed)
}
