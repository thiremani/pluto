package compiler

import (
	"fmt"
	"slices"

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

// SeedEffect describes whether a body may read one output's incoming value
// before a statement definitely assigns it. It is independent of WriteEffect:
// an unconditional update can read its seed, and a conditional write can
// leave preservation entirely to the caller. Uncomputed is a publication
// state, not a lattice member.
type SeedEffect uint8

const (
	SeedUncomputed SeedEffect = iota
	NoSeedRead
	MaySeedRead
)

func (effect SeedEffect) String() string {
	switch effect {
	case SeedUncomputed:
		return "Uncomputed"
	case NoSeedRead:
		return "NoSeedRead"
	case MaySeedRead:
		return "MaySeedRead"
	default:
		return fmt.Sprintf("SeedEffect(%d)", effect)
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
// MayWrite callee output at the assignment boundary; such a write preserves
// a value rather than producing one. CalleeReadsSeed holds LHS indices whose
// existing value the callee body may itself read, whatever it writes.
type StatementEffect struct {
	Writes          []TargetWriteEffect
	ReadsSeed       []int
	CalleeReadsSeed []int
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

// classifyWriteEffect keeps analysis states outside the yield lattice invalid
// so function publication cannot turn missing facts into MayWrite.
func classifyWriteEffect(yield YieldEffect, maySkip bool) WriteEffect {
	if yield != MustYield && yield != MayYield {
		return WriteInvalid
	}

	if yield == MayYield || maySkip {
		return MayWrite
	}

	return MustWrite
}

// bodyEffects pairs the write and seed facts of one specialization's outputs:
// the published summary of a settled callee, or the provisional working
// values of a component still being settled.
type bodyEffects struct {
	writes []WriteEffect
	seeds  []SeedEffect
}

type effectAnalyzer struct {
	compiler        *Compiler
	funcNameMangled string
	graph           *specializationCallGraph
	working         []bodyEffects
}

func newEffectAnalyzer(compiler *Compiler, mangled string, graph *specializationCallGraph, working []bodyEffects) *effectAnalyzer {
	return &effectAnalyzer{
		compiler:        compiler,
		funcNameMangled: mangled,
		graph:           graph,
		working:         working,
	}
}

func (analyzer *effectAnalyzer) exprInfo(expr ast.Expression) *ExprInfo {
	return analyzer.compiler.ExprCache[key(analyzer.funcNameMangled, expr)]
}

func (analyzer *effectAnalyzer) makeInvalidYieldEffects(expr ast.Expression) []YieldEffect {
	info := analyzer.exprInfo(expr)
	info.YieldEffects = slices.Repeat([]YieldEffect{YieldInvalid}, len(info.OutTypes))
	return info.YieldEffects
}

func (analyzer *effectAnalyzer) deriveExpr(expr ast.Expression) []YieldEffect {
	info := analyzer.exprInfo(expr)
	if !typesResolved(info.OutTypes) {
		return analyzer.makeInvalidYieldEffects(expr)
	}

	switch value := expr.(type) {
	case *ast.IntegerLiteral, *ast.FloatLiteral, *ast.StringLiteral, *ast.Identifier:
		info.YieldEffects = slices.Repeat([]YieldEffect{MustYield}, len(info.OutTypes))
	case *ast.ArrayLiteral:
		analyzer.deriveChildren(expr)
		info.YieldEffects = slices.Repeat([]YieldEffect{MustYield}, len(info.OutTypes))
	case *ast.ArrayRangeExpression:
		analyzer.deriveChildren(expr)
		info.YieldEffects = slices.Repeat([]YieldEffect{MayYield}, len(info.OutTypes))
	case *ast.CallExpression:
		return analyzer.deriveCall(value)
	case *ast.InfixExpression:
		return analyzer.deriveInfix(value)
	case *ast.PrefixExpression:
		children := analyzer.deriveChildren(expr)
		info.YieldEffects = alignYieldEffects(children, len(info.OutTypes))
	case *ast.DotExpression:
		children := analyzer.deriveChildren(expr)
		info.YieldEffects = alignYieldEffects(children, len(info.OutTypes))
	case *ast.RangeLiteral, *ast.StructLiteral:
		children := analyzer.deriveChildren(expr)
		combined := foldYieldEffects(children)
		info.YieldEffects = slices.Repeat([]YieldEffect{combined}, len(info.OutTypes))
	default:
		return analyzer.makeInvalidYieldEffects(expr)
	}

	return info.YieldEffects
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

func alignYieldEffects(effects []YieldEffect, count int) []YieldEffect {
	if len(effects) == count {
		return effects
	}

	return slices.Repeat([]YieldEffect{foldYieldEffects(effects)}, count)
}

func (analyzer *effectAnalyzer) deriveInfix(expr *ast.InfixExpression) []YieldEffect {
	info := analyzer.exprInfo(expr)
	left := analyzer.deriveExpr(expr.Left)
	right := analyzer.deriveExpr(expr.Right)
	info.YieldEffects = make([]YieldEffect, len(info.OutTypes))

	for i := range info.YieldEffects {
		leftEffect := yieldSlot(left, i)
		rightEffect := yieldSlot(right, i)
		mode := CondNone

		if i < len(info.CompareModes) {
			mode = info.CompareModes[i]
		}

		switch mode {
		case CondScalar, CondAnd:
			info.YieldEffects[i] = joinYield(MayYield, joinYield(leftEffect, rightEffect))
		case CondArray, CondNone:
			info.YieldEffects[i] = joinYield(leftEffect, rightEffect)
		case CondOr:
			info.YieldEffects[i] = rightEffect
			if leftEffect == YieldInvalid || leftEffect == YieldUncomputed {
				info.YieldEffects[i] = joinYield(leftEffect, rightEffect)
			}
		default:
			info.YieldEffects[i] = YieldInvalid
		}
	}

	return info.YieldEffects
}

func yieldSlot(effects []YieldEffect, index int) YieldEffect {
	if len(effects) == 0 {
		panic("internal: cannot select from empty yield effects")
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
	info := analyzer.exprInfo(expr)
	invocation := analyzer.callInvocationEffect(expr)
	if hasPossiblyEmptyRange(info.Ranges, nil) {
		invocation = joinYield(invocation, MayYield)
	}

	callee := analyzer.callBodyEffects(expr).writes
	if len(callee) != len(info.OutTypes) {
		panic(fmt.Sprintf("internal: call %s has %d output effects for %d typed outputs", expr.Function.Value, len(callee), len(info.OutTypes)))
	}

	info.YieldEffects = make([]YieldEffect, len(info.OutTypes))

	for i, effect := range callee {
		switch effect {
		case MustWrite:
			info.YieldEffects[i] = invocation
		case MayWrite:
			info.YieldEffects[i] = joinYield(invocation, MayYield)
		default:
			info.YieldEffects[i] = YieldInvalid
		}
	}

	return info.YieldEffects
}

func (analyzer *effectAnalyzer) callBodyEffects(expr *ast.CallExpression) bodyEffects {
	info := analyzer.exprInfo(expr)
	mangled := Mangle(analyzer.compiler.MangledPath, expr.Function.Value, info.CallParamTypes)
	f := analyzer.compiler.FuncCache[mangled]
	if f.Settled {
		return bodyEffects{writes: f.BodyOutputEffects, seeds: f.BodySeedEffects}
	}

	if analyzer.graph == nil {
		panic(fmt.Sprintf("internal: unsettled callee %s outside effect settlement", mangled))
	}

	id, ok := analyzer.graph.byMangled[mangled]
	if !ok {
		panic(fmt.Sprintf("internal: unsettled callee %s missing from effect graph", mangled))
	}

	return analyzer.working[id]
}

// hasPossiblyEmptyRange ignores named drivers already owned by an enclosing
// domain, such as a statement condition around a call.
func hasPossiblyEmptyRange(ranges, excluded []*RangeInfo) bool {
	for _, driver := range ranges {
		if rangeDriverNamed(excluded, driver.Name) {
			continue
		}
		if !rangeLiteralGuaranteedNonEmpty(driver.RangeLit) {
			return true
		}
	}

	return false
}

func (analyzer *effectAnalyzer) expressionUsesLocalDomain(expr ast.Expression) bool {
	info := analyzer.exprInfo(expr)
	if _, isCall := expr.(*ast.CallExpression); isCall && info.LoopInside {
		return false
	}

	return hasPossiblyEmptyRange(info.Ranges, nil)
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

func (analyzer *effectAnalyzer) seedResolvedYield(expr ast.Expression, slot int, targetExists bool, conditionRanges []*RangeInfo) (YieldEffect, bool) {
	call, ok := expr.(*ast.CallExpression)
	if !ok || !targetExists {
		return YieldUncomputed, false
	}

	// Only a direct return resolves a skipped write to its seed, and that
	// depends only on output types, so check it before resolving callee
	// effects. calleeReadsSeed covers a body reading its seed, for direct and
	// indirect returns alike.
	if _, direct := directScalarABIReturnType(analyzer.exprInfo(call).OutTypes); !direct {
		return YieldUncomputed, false
	}

	callee := analyzer.callBodyEffects(call).writes
	needsSeed := callee[slot] == MayWrite || analyzer.callOwnsPossiblyEmptyDomain(call, conditionRanges)
	if !needsSeed {
		return YieldUncomputed, false
	}

	return analyzer.callInvocationEffect(call), true
}

func (analyzer *effectAnalyzer) callOwnsPossiblyEmptyDomain(call *ast.CallExpression, conditionRanges []*RangeInfo) bool {
	info := analyzer.exprInfo(call)
	if !info.LoopInside {
		return false
	}
	if !slices.ContainsFunc(info.CallParamTypes, isRangeDriverType) {
		return false
	}

	for _, argument := range call.Arguments {
		if hasPossiblyEmptyRange(analyzer.exprInfo(argument).Ranges, conditionRanges) {
			return true
		}
	}

	return false
}

// calleeReadsSeed reports whether a statement's value call may read the
// incoming value of one output slot, which lowering seeds from the slot's
// destination for direct and indirect returns alike. Unknown analysis must
// not mean the seed goes unread.
func (analyzer *effectAnalyzer) calleeReadsSeed(expr ast.Expression, slot int) bool {
	call, ok := expr.(*ast.CallExpression)
	if !ok {
		return false
	}

	switch seed := analyzer.callBodyEffects(call).seeds[slot]; seed {
	case MaySeedRead:
		return true
	case NoSeedRead:
		return false
	default:
		panic(fmt.Sprintf("internal: call %s has unpublished seed effect %s", call.Function.Value, seed))
	}
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

	condRanges := conditionRanges(analyzer.compiler.ExprCache, analyzer.funcNameMangled, stmt.Condition)

	result := StatementEffect{}
	targetIndex := 0

	for _, expr := range stmt.Value {
		yields := analyzer.deriveExpr(expr)
		maySkip := len(stmt.Condition) > 0 || analyzer.expressionUsesLocalDomain(expr)

		for slot, yield := range yields {
			index := targetIndex
			target := stmt.Name[index]
			targetIndex++
			if isDiscard(target) {
				continue
			}

			_, targetExists := defined[target.Value]
			if seededYield, readsSeed := analyzer.seedResolvedYield(expr, slot, targetExists, condRanges); readsSeed {
				result.ReadsSeed = append(result.ReadsSeed, index)
				yield = seededYield
			}
			// A fresh destination seeds its callee with zero, so nothing is read.
			if targetExists && analyzer.calleeReadsSeed(expr, slot) {
				result.CalleeReadsSeed = append(result.CalleeReadsSeed, index)
			}
			result.Writes = append(result.Writes, TargetWriteEffect{
				TargetIndex: index,
				Effect:      classifyWriteEffect(yield, maySkip),
			})
		}
	}

	return result
}

// validStatementEffect verifies the sparse effect shape before publication.
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

	return validSeedTargets(stmt, effect.ReadsSeed) && validSeedTargets(stmt, effect.CalleeReadsSeed)
}

// validSeedTargets requires ascending, unique, named LHS indices.
func validSeedTargets(stmt *ast.LetStatement, targets []int) bool {
	lastTarget := -1
	for _, targetIndex := range targets {
		if targetIndex <= lastTarget || targetIndex >= len(stmt.Name) {
			return false
		}
		if isDiscard(stmt.Name[targetIndex]) {
			return false
		}

		lastTarget = targetIndex
	}

	return true
}

// deriveBodyEffects folds a body's statements in order, with the reads the
// structural pass recorded, into each output's facts. An output is MustWrite
// once a statement definitely assigns it; before that it holds its seed, which
// a read by name or by a seed-reading callee makes MaySeedRead. Reads precede
// writes within a statement. Missing or malformed statement facts invalidate
// every write effect.
func deriveBodyEffects(template *ast.FuncStatement, reads [][]VarEvent, statements map[*ast.LetStatement]StatementEffect) bodyEffects {
	body := bodyEffects{
		writes: slices.Repeat([]WriteEffect{MayWrite}, len(template.Outputs)),
		seeds:  slices.Repeat([]SeedEffect{NoSeedRead}, len(template.Outputs)),
	}

	for i, statement := range template.Body.Statements {
		read := make(map[string]struct{}, len(reads[i]))
		for _, event := range reads[i] {
			read[event.Name] = struct{}{}
		}

		var assigned map[string]struct{}
		if stmt, ok := statement.(*ast.LetStatement); ok {
			effect, exists := statements[stmt]
			if !exists || !validStatementEffect(stmt, effect) {
				body.writes = slices.Repeat([]WriteEffect{WriteInvalid}, len(template.Outputs))
				return body
			}
			for _, targetIndex := range effect.CalleeReadsSeed {
				read[stmt.Name[targetIndex].Value] = struct{}{}
			}
			assigned = definiteTargets(stmt, effect)
		}

		for index, output := range template.Outputs {
			if _, isRead := read[output.Value]; isRead && body.writes[index] == MayWrite {
				body.seeds[index] = MaySeedRead
			}
			if _, isAssigned := assigned[output.Value]; isAssigned {
				body.writes[index] = MustWrite
			}
		}
	}

	return body
}

// definiteTargets is the set of targets a statement leaves holding its own
// value on every path: unconditional writes that do not merely preserve the
// target's seed.
func definiteTargets(stmt *ast.LetStatement, effect StatementEffect) map[string]struct{} {
	targets := make(map[string]struct{}, len(effect.Writes))
	for _, write := range effect.Writes {
		if write.Effect == MustWrite && !slices.Contains(effect.ReadsSeed, write.TargetIndex) {
			targets[stmt.Name[write.TargetIndex].Value] = struct{}{}
		}
	}
	return targets
}

type specializationNodeID int

type specializationNode struct {
	mangled        string
	effectCallees  []specializationNodeID
	effectCallers  []specializationNodeID
	directCallees  []string
	componentIndex int
}

// specializationCallGraph interns the newly walked batch once. Dense effect
// edges drive SCC settlement, while complete mangled edges persist for CFG
// diagnostic replay across warm-cache scripts.
type specializationCallGraph struct {
	nodes     []specializationNode
	byMangled map[string]specializationNodeID
}

func newSpecializationCallGraph(walked map[string]walkedSpecialization) *specializationCallGraph {
	graph := &specializationCallGraph{
		nodes:     make([]specializationNode, len(walked)),
		byMangled: make(map[string]specializationNodeID, len(walked)),
	}

	for mangled, walkedFunc := range walked {
		id := specializationNodeID(walkedFunc.walkIndex)
		graph.byMangled[mangled] = id
		graph.nodes[id] = specializationNode{mangled: mangled}
	}

	return graph
}

// collectSpecializationCallEdges returns stable unique lowering and replay
// targets plus source-order primary effect dependencies. Each direct primary
// precedes its distinct scalar companion.
func collectSpecializationCallEdges(compiler *Compiler, callerMangled string, statements []ast.Statement) ([]string, []string) {
	seen := make(map[string]struct{})
	var directCallees []string
	var effectCallees []string

	for _, call := range collectBodyCalls(statements) {
		if _, builtin := Builtins[call.Function.Value]; builtin {
			continue
		}

		info := compiler.ExprCache[key(callerMangled, call)]
		primary := Mangle(compiler.MangledPath, call.Function.Value, info.CallParamTypes)
		requireSpecializationCallTarget(compiler, callerMangled, primary)
		effectCallees = append(effectCallees, primary)
		directCallees = appendUniqueMangled(directCallees, seen, primary)

		if !info.ScalarCallVariantEnsured {
			continue
		}

		scalar := Mangle(compiler.MangledPath, call.Function.Value, info.ScalarCallParamTypes)
		if scalar == primary {
			panic(fmt.Sprintf("internal: call %s in %s marks a non-distinct scalar specialization", call.Function.Value, callerMangled))
		}
		requireSpecializationCallTarget(compiler, callerMangled, scalar)
		directCallees = appendUniqueMangled(directCallees, seen, scalar)
	}

	return directCallees, effectCallees
}

func appendUniqueMangled(names []string, seen map[string]struct{}, mangled string) []string {
	if _, exists := seen[mangled]; exists {
		return names
	}

	seen[mangled] = struct{}{}
	return append(names, mangled)
}

func requireSpecializationCallTarget(compiler *Compiler, callerMangled, calleeMangled string) {
	if compiler.FuncCache[calleeMangled] == nil {
		panic(fmt.Sprintf("internal: typed call from %s targets missing specialization %s", callerMangled, calleeMangled))
	}
}

func (ts *TypeSolver) addSpecializationGraphEdges(graph *specializationCallGraph, callerID specializationNodeID) {
	caller := &graph.nodes[callerID]
	walked := ts.walkedFuncs[caller.mangled]
	compiler := ts.ScriptCompiler.Compiler
	directCallees, effectCallees := collectSpecializationCallEdges(compiler, caller.mangled, walked.template.Body.Statements)
	caller.directCallees = directCallees

	for _, callee := range effectCallees {
		if calleeID, inGraph := graph.byMangled[callee]; inGraph {
			caller.effectCallees = append(caller.effectCallees, calleeID)
		}
	}

	slices.Sort(caller.effectCallees)
	caller.effectCallees = slices.Compact(caller.effectCallees)

	for _, calleeID := range caller.effectCallees {
		graph.nodes[calleeID].effectCallers = append(graph.nodes[calleeID].effectCallers, callerID)
	}
}

func (ts *TypeSolver) buildSpecializationCallGraph() *specializationCallGraph {
	graph := newSpecializationCallGraph(ts.walkedFuncs)

	for id := range graph.nodes {
		ts.addSpecializationGraphEdges(graph, specializationNodeID(id))
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
			for _, argument := range stmt.Expression.Arguments {
				calls = append(calls, collectExprCalls(argument)...)
			}
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
	graph      *specializationCallGraph
	index      int
	indices    []int
	lowlink    []int
	stack      []specializationNodeID
	onStack    []bool
	components [][]specializationNodeID
}

func (graph *specializationCallGraph) calleeFirstComponents() [][]specializationNodeID {
	state := &tarjanState{
		graph:   graph,
		indices: make([]int, len(graph.nodes)),
		lowlink: make([]int, len(graph.nodes)),
		onStack: make([]bool, len(graph.nodes)),
	}

	for id := range graph.nodes {
		if state.indices[id] == 0 {
			state.visit(specializationNodeID(id))
		}
	}

	return state.components
}

func (state *tarjanState) visit(id specializationNodeID) {
	state.index++
	state.indices[id] = state.index
	state.lowlink[id] = state.index
	state.stack = append(state.stack, id)
	state.onStack[id] = true

	for _, calleeID := range state.graph.nodes[id].effectCallees {
		if state.indices[calleeID] == 0 {
			state.visit(calleeID)
			state.lowlink[id] = min(state.lowlink[id], state.lowlink[calleeID])
		} else if state.onStack[calleeID] {
			state.lowlink[id] = min(state.lowlink[id], state.indices[calleeID])
		}
	}

	if state.lowlink[id] != state.indices[id] {
		return
	}

	componentIndex := len(state.components)
	var component []specializationNodeID

	for {
		last := len(state.stack) - 1
		member := state.stack[last]
		state.stack = state.stack[:last]
		state.onStack[member] = false
		state.graph.nodes[member].componentIndex = componentIndex
		component = append(component, member)
		if member == id {
			break
		}
	}

	slices.Sort(component)
	state.components = append(state.components, component)
}

// deriveEffectNode refreshes one specialization and reports whether any output
// weakened from MustWrite to MayWrite or grew from NoSeedRead to MaySeedRead.
// Either change can create a seed read in a caller, so both requeue callers.
func (ts *TypeSolver) deriveEffectNode(graph *specializationCallGraph, working []bodyEffects, id specializationNodeID) bool {
	node := &graph.nodes[id]
	walked := ts.walkedFuncs[node.mangled]
	compiler := ts.ScriptCompiler.Compiler
	initial := functionInitialBindings(walked.template)
	analyzer := newEffectAnalyzer(compiler, node.mangled, graph, working)
	statements := analyzer.deriveStatements(walked.template.Body.Statements, initial)
	reads := compiler.CodeCompiler.templateBodies[templateKey(walked.template)].statementReads
	derived := deriveBodyEffects(walked.template, reads, statements)

	if !validPublishedEffects(derived.writes, len(walked.info.Sig.OutTypes)) {
		panic(fmt.Sprintf("internal: invalid effects for specialization %s", node.mangled))
	}

	changed := false
	for outputIndex := range derived.writes {
		if working[id].writes[outputIndex] == MustWrite && derived.writes[outputIndex] == MayWrite {
			working[id].writes[outputIndex] = MayWrite
			changed = true
		}
		if working[id].seeds[outputIndex] == NoSeedRead && derived.seeds[outputIndex] == MaySeedRead {
			working[id].seeds[outputIndex] = MaySeedRead
			changed = true
		}
	}

	walked.info.StatementEffects = statements

	return changed
}

func enqueueRecursiveEffectCallers(graph *specializationCallGraph, id specializationNodeID, pending []specializationNodeID, queued []bool) []specializationNodeID {
	componentIndex := graph.nodes[id].componentIndex

	for _, callerID := range graph.nodes[id].effectCallers {
		if graph.nodes[callerID].componentIndex != componentIndex || queued[callerID] {
			continue
		}
		pending = append(pending, callerID)
		queued[callerID] = true
	}

	return pending
}

// settleEffectComponent drives one SCC to a fixed point and publishes all
// members' write and seed facts together, only after their shared worklist
// drains.
func (ts *TypeSolver) settleEffectComponent(graph *specializationCallGraph, working []bodyEffects, component []specializationNodeID, queued []bool) {
	pending := slices.Clone(component)

	for _, id := range pending {
		queued[id] = true
	}

	for next := 0; next < len(pending); next++ {
		id := pending[next]
		queued[id] = false
		if ts.deriveEffectNode(graph, working, id) {
			pending = enqueueRecursiveEffectCallers(graph, id, pending, queued)
		}
	}

	for _, id := range component {
		info := ts.walkedFuncs[graph.nodes[id].mangled].info
		info.BodyOutputEffects = slices.Clone(working[id].writes)
		info.BodySeedEffects = slices.Clone(working[id].seeds)
	}
}

// settleEffects starts every output at the optimistic end of both lattices:
// writes only weaken and seed reads only grow.
func (ts *TypeSolver) settleEffects(graph *specializationCallGraph) {
	working := make([]bodyEffects, len(graph.nodes))

	for id := range graph.nodes {
		outputs := len(ts.walkedFuncs[graph.nodes[id].mangled].info.Sig.OutTypes)
		working[id] = bodyEffects{
			writes: slices.Repeat([]WriteEffect{MustWrite}, outputs),
			seeds:  slices.Repeat([]SeedEffect{NoSeedRead}, outputs),
		}
	}

	components := graph.calleeFirstComponents()
	queued := make([]bool, len(graph.nodes))

	for _, component := range components {
		ts.settleEffectComponent(graph, working, component, queued)
	}
}

// functionInitialBindings are the names bound before a body runs: the inputs,
// and the outputs holding their seeds.
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
	analyzer := newEffectAnalyzer(ts.ScriptCompiler.Compiler, ts.ScriptCompiler.ScriptMangled, nil, nil)
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
