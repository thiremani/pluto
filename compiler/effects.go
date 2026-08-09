package compiler

import (
	"fmt"
	"slices"
	"sort"

	"github.com/thiremani/pluto/ast"
)

// WriteEffect describes whether one named target is guaranteed to receive a
// value. Uncomputed and Invalid are publication states, not lattice members.
type WriteEffect uint8

const (
	WriteUncomputed WriteEffect = iota
	WriteInvalid
	MustWrite
	MayWrite
)

func (effect WriteEffect) String() string {
	switch effect {
	case WriteUncomputed:
		return "Uncomputed"
	case WriteInvalid:
		return "Invalid"
	case MustWrite:
		return "MustWrite"
	case MayWrite:
		return "MayWrite"
	default:
		return fmt.Sprintf("WriteEffect(%d)", effect)
	}
}

// YieldEffect describes whether one expression outcome is guaranteed to
// produce a value. Uncomputed and Invalid are publication states, not lattice
// members.
type YieldEffect uint8

const (
	YieldUncomputed YieldEffect = iota
	YieldInvalid
	MustYield
	MayYield
)

func (effect YieldEffect) String() string {
	switch effect {
	case YieldUncomputed:
		return "Uncomputed"
	case YieldInvalid:
		return "Invalid"
	case MustYield:
		return "MustYield"
	case MayYield:
		return "MayYield"
	default:
		return fmt.Sprintf("YieldEffect(%d)", effect)
	}
}

// TargetWriteEffect is one entry in a sparse, position-preserving target
// vector. Discard targets have no entry; TargetIndex keeps later entries tied
// to their original LHS slots.
type TargetWriteEffect struct {
	TargetIndex int
	Effect      WriteEffect
}

// StatementEffect contains the target facts derived for one assignment.
// ReadsSeed holds LHS indices whose existing value is consumed by a direct
// MayWrite callee output at the assignment boundary.
type StatementEffect struct {
	Writes    []TargetWriteEffect
	ReadsSeed []int
}

func validPublishedEffects(effects []WriteEffect, count int) bool {
	if len(effects) != count {
		return false
	}
	for _, effect := range effects {
		if effect != MustWrite && effect != MayWrite {
			return false
		}
	}
	return true
}

func joinYield(left, right YieldEffect) YieldEffect {
	if left == YieldInvalid || right == YieldInvalid {
		return YieldInvalid
	}
	if left == YieldUncomputed || right == YieldUncomputed {
		return YieldUncomputed
	}
	if left == MayYield || right == MayYield {
		return MayYield
	}
	return MustYield
}

type effectAnalyzer struct {
	compiler        *Compiler
	funcNameMangled string
	calleeEffects   map[string][]WriteEffect
}

func newEffectAnalyzer(compiler *Compiler, mangled string, calleeEffects map[string][]WriteEffect) *effectAnalyzer {
	return &effectAnalyzer{
		compiler:        compiler,
		funcNameMangled: mangled,
		calleeEffects:   calleeEffects,
	}
}

func (analyzer *effectAnalyzer) info(expr ast.Expression) *ExprInfo {
	return analyzer.compiler.ExprCache[key(analyzer.funcNameMangled, expr)]
}

func (analyzer *effectAnalyzer) invalidExprEffects(expr ast.Expression) []YieldEffect {
	info := analyzer.info(expr)
	count := 1
	if info != nil && len(info.OutTypes) > 0 {
		count = len(info.OutTypes)
	}
	effects := slices.Repeat([]YieldEffect{YieldInvalid}, count)
	if info != nil {
		info.YieldEffects = slices.Clone(effects)
	}
	return effects
}

func (analyzer *effectAnalyzer) cacheExprEffects(expr ast.Expression, effects []YieldEffect) []YieldEffect {
	info := analyzer.info(expr)
	if info == nil || len(effects) != len(info.OutTypes) {
		return analyzer.invalidExprEffects(expr)
	}
	info.YieldEffects = slices.Clone(effects)
	return effects
}

func (analyzer *effectAnalyzer) deriveExpr(expr ast.Expression) []YieldEffect {
	info := analyzer.info(expr)
	if info == nil || len(info.OutTypes) == 0 || !typesResolved(info.OutTypes) {
		return analyzer.invalidExprEffects(expr)
	}

	switch value := expr.(type) {
	case *ast.IntegerLiteral, *ast.FloatLiteral, *ast.StringLiteral, *ast.Identifier:
		return analyzer.cacheExprEffects(expr, broadcastYield(MustYield, len(info.OutTypes)))
	case *ast.ArrayLiteral:
		analyzer.deriveChildren(expr)
		return analyzer.cacheExprEffects(expr, broadcastYield(MustYield, len(info.OutTypes)))
	case *ast.ArrayRangeExpression:
		analyzer.deriveChildren(expr)
		return analyzer.cacheExprEffects(expr, broadcastYield(MayYield, len(info.OutTypes)))
	case *ast.CallExpression:
		return analyzer.deriveCall(value)
	case *ast.InfixExpression:
		return analyzer.deriveInfix(value)
	case *ast.PrefixExpression:
		children := analyzer.deriveChildren(expr)
		return analyzer.cacheExprEffects(expr, alignYieldEffects(children, len(info.OutTypes)))
	case *ast.DotExpression:
		children := analyzer.deriveChildren(expr)
		return analyzer.cacheExprEffects(expr, alignYieldEffects(children, len(info.OutTypes)))
	case *ast.RangeLiteral, *ast.StructLiteral:
		children := analyzer.deriveChildren(expr)
		combined := foldYieldEffects(children)
		return analyzer.cacheExprEffects(expr, broadcastYield(combined, len(info.OutTypes)))
	default:
		return analyzer.invalidExprEffects(expr)
	}
}

func (analyzer *effectAnalyzer) deriveChildren(expr ast.Expression) []YieldEffect {
	var effects []YieldEffect
	for _, child := range ast.ExprChildren(expr) {
		effects = append(effects, analyzer.deriveExpr(child)...)
	}
	return effects
}

func foldYieldEffects(effects []YieldEffect) YieldEffect {
	result := MustYield
	for _, effect := range effects {
		result = joinYield(result, effect)
	}
	return result
}

func broadcastYield(effect YieldEffect, count int) []YieldEffect {
	return slices.Repeat([]YieldEffect{effect}, count)
}

func alignYieldEffects(effects []YieldEffect, count int) []YieldEffect {
	if len(effects) == count {
		return slices.Clone(effects)
	}
	return broadcastYield(foldYieldEffects(effects), count)
}

func (analyzer *effectAnalyzer) deriveInfix(expr *ast.InfixExpression) []YieldEffect {
	info := analyzer.info(expr)
	left := analyzer.deriveExpr(expr.Left)
	right := analyzer.deriveExpr(expr.Right)
	effects := make([]YieldEffect, len(info.OutTypes))
	for i := range effects {
		mode := CondNone
		if i < len(info.CompareModes) {
			mode = info.CompareModes[i]
		}
		switch mode {
		case CondScalar, CondAnd:
			effects[i] = MayYield
		case CondArray:
			effects[i] = MustYield
		case CondOr:
			effects[i] = yieldSlot(right, i)
		case CondNone:
			effects[i] = joinYield(yieldSlot(left, i), yieldSlot(right, i))
		default:
			effects[i] = YieldInvalid
		}
	}
	return analyzer.cacheExprEffects(expr, effects)
}

func yieldSlot(effects []YieldEffect, index int) YieldEffect {
	if len(effects) == 0 {
		return YieldInvalid
	}
	if len(effects) == 1 {
		return effects[0]
	}
	if index >= len(effects) {
		return foldYieldEffects(effects)
	}
	return effects[index]
}

func (analyzer *effectAnalyzer) deriveCall(expr *ast.CallExpression) []YieldEffect {
	info := analyzer.info(expr)
	invocation := analyzer.callInvocationEffect(expr)
	if analyzer.expressionDomainMayBeEmpty(expr) {
		invocation = joinYield(invocation, MayYield)
	}

	effects := make([]YieldEffect, len(info.OutTypes))
	if _, builtin := Builtins[expr.Function.Value]; builtin {
		for i := range effects {
			effects[i] = invocation
		}
		return analyzer.cacheExprEffects(expr, effects)
	}

	callee := analyzer.callBodyOutputEffects(expr)
	if len(callee) != len(effects) {
		return analyzer.invalidExprEffects(expr)
	}
	for i, effect := range callee {
		if effect == MustWrite {
			effects[i] = invocation
		} else if effect == MayWrite {
			effects[i] = MayYield
		} else {
			effects[i] = YieldInvalid
		}
	}
	return analyzer.cacheExprEffects(expr, effects)
}

func (analyzer *effectAnalyzer) callBodyOutputEffects(expr *ast.CallExpression) []WriteEffect {
	info := analyzer.info(expr)
	mangled := Mangle(analyzer.compiler.MangledPath, expr.Function.Value, info.CallParamTypes)
	if effects, ok := analyzer.calleeEffects[mangled]; ok {
		return effects
	}
	f := analyzer.compiler.FuncCache[mangled]
	if f == nil {
		panic(fmt.Sprintf("internal: missing callee specialization %s during effect analysis", mangled))
	}
	if !f.Settled || !validPublishedEffects(f.BodyOutputEffects, len(f.Sig.OutTypes)) {
		panic(fmt.Sprintf("internal: read of unpublished effects for %s", mangled))
	}
	return f.BodyOutputEffects
}

func (analyzer *effectAnalyzer) expressionDomainMayBeEmpty(expr ast.Expression) bool {
	info := analyzer.info(expr)
	if info == nil {
		return false
	}
	for _, driver := range info.Ranges {
		if !rangeLiteralGuaranteedNonEmpty(driver.RangeLit) {
			return true
		}
	}
	return false
}

func (analyzer *effectAnalyzer) expressionUsesLocalDomain(expr ast.Expression) bool {
	if call, ok := expr.(*ast.CallExpression); ok {
		info := analyzer.info(call)
		if info != nil && info.LoopInside {
			return false
		}
	}
	return analyzer.expressionDomainMayBeEmpty(expr)
}

func rangeLiteralGuaranteedNonEmpty(literal *ast.RangeLiteral) bool {
	if literal == nil {
		return false
	}
	start, startOK := literal.Start.(*ast.IntegerLiteral)
	stop, stopOK := literal.Stop.(*ast.IntegerLiteral)
	if !startOK || !stopOK {
		return false
	}
	step := int64(1)
	if literal.Step != nil {
		stepLiteral, ok := literal.Step.(*ast.IntegerLiteral)
		if !ok {
			return false
		}
		step = stepLiteral.Value
	}
	return step > 0 && start.Value < stop.Value || step < 0 && start.Value > stop.Value
}

func (analyzer *effectAnalyzer) callInvocationEffect(expr *ast.CallExpression) YieldEffect {
	effect := MustYield
	for _, argument := range expr.Arguments {
		effect = joinYield(effect, foldYieldEffects(analyzer.deriveExpr(argument)))
	}
	return effect
}

func (analyzer *effectAnalyzer) directCallResolvesSeed(expr ast.Expression, slot int, targetExists bool) bool {
	call, ok := expr.(*ast.CallExpression)
	if !ok || !targetExists {
		return false
	}
	if _, builtin := Builtins[call.Function.Value]; builtin {
		return false
	}
	callee := analyzer.callBodyOutputEffects(call)
	if slot >= len(callee) {
		return false
	}
	calleeMaySkip := callee[slot] == MayWrite || analyzer.callOwnsPossiblyEmptyDomain(call)
	if !calleeMaySkip {
		return false
	}
	info := analyzer.info(call)
	// Direct-return eligibility depends only on output types. Using the shared
	// ABI predicate avoids tying ReadsSeed to either the range or scalar call
	// variant selected later by lowering.
	_, direct := directScalarABIReturnType(info.OutTypes)
	return direct
}

func (analyzer *effectAnalyzer) callOwnsPossiblyEmptyDomain(call *ast.CallExpression) bool {
	info := analyzer.info(call)
	if info == nil || !info.LoopInside || !analyzer.expressionDomainMayBeEmpty(call) {
		return false
	}
	for _, paramType := range info.CallParamTypes {
		if paramType.Kind() == RangeKind || paramType.Kind() == ArrayRangeKind {
			return true
		}
	}
	return false
}

func (analyzer *effectAnalyzer) deriveStatements(statements []ast.Statement, initiallyDefined map[string]struct{}) map[*ast.LetStatement]StatementEffect {
	defined := make(map[string]struct{}, len(initiallyDefined))
	for name := range initiallyDefined {
		defined[name] = struct{}{}
	}
	results := make(map[*ast.LetStatement]StatementEffect)
	for _, statement := range statements {
		switch stmt := statement.(type) {
		case *ast.PrintStatement:
			for _, argument := range stmt.Expression.Arguments {
				analyzer.deriveExpr(argument)
			}
		case *ast.LetStatement:
			results[stmt] = analyzer.deriveLet(stmt, defined)
			for _, target := range stmt.Name {
				if !isDiscard(target) {
					defined[target.Value] = struct{}{}
				}
			}
		}
	}
	return results
}

func (analyzer *effectAnalyzer) deriveLet(stmt *ast.LetStatement, defined map[string]struct{}) StatementEffect {
	for _, condition := range stmt.Condition {
		analyzer.deriveExpr(condition)
	}

	result := StatementEffect{}
	targetIndex := 0
	for _, expr := range stmt.Value {
		yields := analyzer.deriveExpr(expr)
		localDomain := analyzer.expressionUsesLocalDomain(expr)
		for slot, yield := range yields {
			if targetIndex >= len(stmt.Name) {
				return StatementEffect{Writes: []TargetWriteEffect{{TargetIndex: targetIndex, Effect: WriteInvalid}}}
			}
			target := stmt.Name[targetIndex]
			if isDiscard(target) {
				targetIndex++
				continue
			}

			_, targetExists := defined[target.Value]
			if analyzer.directCallResolvesSeed(expr, slot, targetExists) {
				result.ReadsSeed = append(result.ReadsSeed, targetIndex)
				yield = analyzer.callInvocationEffect(expr.(*ast.CallExpression))
			}

			write := MustWrite
			if yield != MustYield && yield != MayYield {
				// Analysis states outside the yield lattice must remain invalid so
				// function publication cannot turn missing facts into MayWrite.
				write = WriteInvalid
			} else if yield == MayYield || len(stmt.Condition) > 0 || localDomain {
				write = MayWrite
			}
			result.Writes = append(result.Writes, TargetWriteEffect{TargetIndex: targetIndex, Effect: write})
			targetIndex++
		}
	}
	if targetIndex != len(stmt.Name) {
		return StatementEffect{Writes: []TargetWriteEffect{{TargetIndex: targetIndex, Effect: WriteInvalid}}}
	}
	return result
}

func validStatementEffect(stmt *ast.LetStatement, effect StatementEffect) bool {
	writeIndex := 0
	for targetIndex, target := range stmt.Name {
		if isDiscard(target) {
			continue
		}
		if writeIndex >= len(effect.Writes) {
			return false
		}
		write := effect.Writes[writeIndex]
		if write.TargetIndex != targetIndex || write.Effect != MustWrite && write.Effect != MayWrite {
			return false
		}
		writeIndex++
	}
	if writeIndex != len(effect.Writes) {
		return false
	}
	for _, targetIndex := range effect.ReadsSeed {
		if targetIndex < 0 || targetIndex >= len(stmt.Name) || isDiscard(stmt.Name[targetIndex]) {
			return false
		}
	}
	return true
}

func deriveBodyOutputEffects(template *ast.FuncStatement, statements map[*ast.LetStatement]StatementEffect) []WriteEffect {
	effects := make([]WriteEffect, len(template.Outputs))
	for i := range effects {
		effects[i] = MayWrite
	}
	outputIndex := make(map[string]int, len(template.Outputs))
	for i, output := range template.Outputs {
		outputIndex[output.Value] = i
	}
	for _, statement := range template.Body.Statements {
		stmt, ok := statement.(*ast.LetStatement)
		if !ok {
			continue
		}
		statementEffect, exists := statements[stmt]
		if !exists || !validStatementEffect(stmt, statementEffect) {
			return slices.Repeat([]WriteEffect{WriteInvalid}, len(template.Outputs))
		}
		for _, write := range statementEffect.Writes {
			index, isOutput := outputIndex[stmt.Name[write.TargetIndex].Value]
			if isOutput && write.Effect == MustWrite {
				effects[index] = MustWrite
			}
		}
	}
	return effects
}

type effectNode struct {
	mangled  string
	info     *FuncInfo
	template *ast.FuncStatement
	edges    []string
}

type effectGraph struct {
	nodes map[string]*effectNode
}

func (ts *TypeSolver) buildEffectGraph(walked map[string]struct{}) *effectGraph {
	graph := &effectGraph{nodes: make(map[string]*effectNode, len(walked))}
	for mangled := range walked {
		f := ts.ScriptCompiler.Compiler.FuncCache[mangled]
		if f == nil {
			panic(fmt.Sprintf("internal: missing walked specialization %s", mangled))
		}
		template, ok := ts.ScriptCompiler.Compiler.CodeCompiler.lookupFuncTemplate(f.Sig.Name, len(f.Sig.Params))
		if !ok {
			panic(fmt.Sprintf("internal: missing template for specialization %s", mangled))
		}
		graph.nodes[mangled] = &effectNode{mangled: mangled, info: f, template: template}
	}
	for _, node := range graph.nodes {
		calls := collectBodyCalls(node.template.Body.Statements)
		for _, call := range calls {
			if _, builtin := Builtins[call.Function.Value]; builtin {
				continue
			}
			info := ts.ExprCache[key(node.mangled, call)]
			if info == nil {
				panic(fmt.Sprintf("internal: missing call facts for %s in specialization %s during effect graph construction", call.Function.Value, node.mangled))
			}
			callee := Mangle(ts.ScriptCompiler.Compiler.MangledPath, call.Function.Value, info.CallParamTypes)
			if _, unsettled := graph.nodes[callee]; unsettled {
				node.edges = append(node.edges, callee)
			}
		}
		sort.Strings(node.edges)
		node.edges = slices.Compact(node.edges)
	}
	return graph
}

func collectBodyCalls(statements []ast.Statement) []*ast.CallExpression {
	var calls []*ast.CallExpression
	for _, statement := range statements {
		switch stmt := statement.(type) {
		case *ast.LetStatement:
			for _, condition := range stmt.Condition {
				calls = append(calls, collectExprCalls(condition)...)
			}
			for _, value := range stmt.Value {
				calls = append(calls, collectExprCalls(value)...)
			}
		case *ast.PrintStatement:
			calls = append(calls, collectExprCalls(stmt.Expression)...)
		}
	}
	return calls
}

func collectExprCalls(expr ast.Expression) []*ast.CallExpression {
	var calls []*ast.CallExpression
	if call, ok := expr.(*ast.CallExpression); ok {
		calls = append(calls, call)
	}
	for _, child := range ast.ExprChildren(expr) {
		calls = append(calls, collectExprCalls(child)...)
	}
	return calls
}

type tarjanState struct {
	graph      *effectGraph
	index      int
	indices    map[string]int
	lowlink    map[string]int
	stack      []string
	onStack    map[string]bool
	components [][]string
}

func (graph *effectGraph) calleeFirstComponents() [][]string {
	state := &tarjanState{
		graph:   graph,
		indices: make(map[string]int),
		lowlink: make(map[string]int),
		onStack: make(map[string]bool),
	}
	names := make([]string, 0, len(graph.nodes))
	for name := range graph.nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, visited := state.indices[name]; !visited {
			state.visit(name)
		}
	}
	return state.components
}

func (state *tarjanState) visit(name string) {
	state.index++
	state.indices[name] = state.index
	state.lowlink[name] = state.index
	state.stack = append(state.stack, name)
	state.onStack[name] = true

	for _, edge := range state.graph.nodes[name].edges {
		if _, visited := state.indices[edge]; !visited {
			state.visit(edge)
			state.lowlink[name] = min(state.lowlink[name], state.lowlink[edge])
		} else if state.onStack[edge] {
			state.lowlink[name] = min(state.lowlink[name], state.indices[edge])
		}
	}
	if state.lowlink[name] != state.indices[name] {
		return
	}

	var component []string
	for {
		last := len(state.stack) - 1
		member := state.stack[last]
		state.stack = state.stack[:last]
		state.onStack[member] = false
		component = append(component, member)
		if member == name {
			break
		}
	}
	sort.Strings(component)
	state.components = append(state.components, component)
}

func (ts *TypeSolver) settleEffects(walked map[string]struct{}) {
	graph := ts.buildEffectGraph(walked)
	working := make(map[string][]WriteEffect, len(graph.nodes))
	for name, node := range graph.nodes {
		working[name] = slices.Repeat([]WriteEffect{MustWrite}, len(node.info.Sig.OutTypes))
	}

	for _, component := range graph.calleeFirstComponents() {
		changed := true
		for changed {
			changed = false
			for _, name := range component {
				node := graph.nodes[name]
				initial := functionInitialBindings(node.template)
				analyzer := newEffectAnalyzer(ts.ScriptCompiler.Compiler, name, working)
				statements := analyzer.deriveStatements(node.template.Body.Statements, initial)
				derived := deriveBodyOutputEffects(node.template, statements)
				if !validPublishedEffects(derived, len(node.info.Sig.OutTypes)) {
					panic(fmt.Sprintf("internal: invalid effects for specialization %s", name))
				}
				for i, effect := range derived {
					if working[name][i] == MustWrite && effect == MayWrite {
						working[name][i] = MayWrite
						changed = true
					}
				}
				node.info.StatementEffects = statements
			}
		}
		for _, name := range component {
			node := graph.nodes[name]
			node.info.BodyOutputEffects = slices.Clone(working[name])
		}
	}
}

func functionInitialBindings(template *ast.FuncStatement) map[string]struct{} {
	defined := make(map[string]struct{}, len(template.Parameters)+len(template.Outputs))
	for _, parameter := range template.Parameters {
		defined[parameter.Value] = struct{}{}
	}
	for _, output := range template.Outputs {
		defined[output.Value] = struct{}{}
	}
	return defined
}

func (ts *TypeSolver) deriveScriptEffects() {
	root := ts.ScriptCompiler.Script.Root
	analyzer := newEffectAnalyzer(ts.ScriptCompiler.Compiler, ts.ScriptCompiler.ScriptMangled, nil)
	root.StatementEffects = analyzer.deriveStatements(ts.ScriptCompiler.Program.Statements, nil)
	for _, statement := range ts.ScriptCompiler.Program.Statements {
		stmt, ok := statement.(*ast.LetStatement)
		if !ok {
			continue
		}
		effect, exists := root.StatementEffects[stmt]
		if !exists || !validStatementEffect(stmt, effect) {
			panic(fmt.Sprintf("internal: invalid effects for script statement %q", stmt))
		}
	}
}
