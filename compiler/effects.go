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

// SeedEffect describes whether a scalar body can observe one output's
// incoming value before that output is definitely replaced. It is independent
// of WriteEffect: an unconditional computed update reads its seed, while a
// conditional write may leave preservation entirely to the caller. Uncomputed
// and Invalid are publication states, not lattice members.
type SeedEffect uint8

const (
	SeedUncomputed SeedEffect = iota
	SeedInvalid
	NoSeedRead
	MaySeedRead
)

func (effect SeedEffect) String() string {
	switch effect {
	case SeedUncomputed:
		return "Uncomputed"
	case SeedInvalid:
		return "Invalid"
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
// ReadsSeed holds LHS indices whose existing value resolves a direct MayWrite
// callee output at the assignment boundary; such a write preserves a value
// rather than producing one. CalleeReadsSeed holds LHS indices whose incoming
// value the callee body itself may read before definitely replacing it,
// whatever the write effect. It is recorded for every named target because a
// fresh destination supplies a zero seed: consumers decide whether an
// existing value is involved.
type StatementEffect struct {
	Writes          []TargetWriteEffect
	ReadsSeed       []int
	CalleeReadsSeed []int
}

// replacesTarget reports whether the statement definitely stores a value the
// body produced: a MustWrite not manufactured by reading the destination seed.
func (effect StatementEffect) replacesTarget(write TargetWriteEffect) bool {
	return write.Effect == MustWrite && !slices.Contains(effect.ReadsSeed, write.TargetIndex)
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

func validPublishedSeedEffects(effects []SeedEffect, count int) bool {
	if len(effects) != count {
		return false
	}

	for _, effect := range effects {
		if effect != NoSeedRead && effect != MaySeedRead {
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

	// Direct-return eligibility depends only on output types. Check it before
	// resolving callee effects because indirect calls cannot consume a seed.
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

// calleeSeedEffects returns the callee's per-output seed facts for a resolved
// call and nil for every other expression, which reads no destination. An
// unpublished fact is an ICE: unknown analysis must not mean no seed reads.
func (analyzer *effectAnalyzer) calleeSeedEffects(expr ast.Expression) []SeedEffect {
	call, ok := expr.(*ast.CallExpression)
	if !ok || !typesResolved(analyzer.exprInfo(call).OutTypes) {
		return nil
	}

	seeds := analyzer.callBodyEffects(call).seeds
	if !validPublishedSeedEffects(seeds, len(analyzer.exprInfo(call).OutTypes)) {
		panic(fmt.Sprintf("internal: call %s has unpublished seed effects %v", call.Function.Value, seeds))
	}

	return seeds
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
		calleeSeeds := analyzer.calleeSeedEffects(expr)

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
			if calleeSeeds != nil && calleeSeeds[slot] == MaySeedRead {
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

func outputIndices(template *ast.FuncStatement) map[string]int {
	outputIndex := make(map[string]int, len(template.Outputs))

	for i, output := range template.Outputs {
		outputIndex[output.Value] = i
	}

	return outputIndex
}

func deriveBodyOutputEffects(template *ast.FuncStatement, statements map[*ast.LetStatement]StatementEffect) []WriteEffect {
	effects := slices.Repeat([]WriteEffect{MayWrite}, len(template.Outputs))
	outputIndex := outputIndices(template)

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
			if !statementEffect.replacesTarget(write) {
				continue
			}
			index, isOutput := outputIndex[stmt.Name[write.TargetIndex].Value]
			if isOutput {
				effects[index] = MustWrite
			}
		}
	}

	return effects
}

// seedFold walks one body in statement order and records which outputs are
// read while they may still hold their incoming value. Reads precede writes
// within a statement, and a recorded read is permanent: a seed copied to a
// local before the output is replaced still reaches the body's results.
type seedFold struct {
	outputIndex map[string]int
	replaced    []bool
	effects     []SeedEffect
	defined     map[string]struct{}
	isGlobal    func(string) bool
}

func newSeedFold(template *ast.FuncStatement, isGlobal func(string) bool) *seedFold {
	fold := &seedFold{
		outputIndex: outputIndices(template),
		replaced:    make([]bool, len(template.Outputs)),
		effects:     slices.Repeat([]SeedEffect{NoSeedRead}, len(template.Outputs)),
		defined:     functionInitialBindings(template),
		isGlobal:    isGlobal,
	}

	// Outputs are bound to their seeds before the body runs, so a marker
	// naming one resolves like the solver and lowering resolve it.
	for _, output := range template.Outputs {
		fold.defined[output.Value] = struct{}{}
	}

	return fold
}

func (fold *seedFold) isDefined(name string) bool {
	if _, exists := fold.defined[name]; exists {
		return true
	}

	return fold.isGlobal(name)
}

func (fold *seedFold) read(name string) {
	index, isOutput := fold.outputIndex[name]
	if isOutput && !fold.replaced[index] {
		fold.effects[index] = MaySeedRead
	}
}

func (fold *seedFold) replace(name string) {
	if index, isOutput := fold.outputIndex[name]; isOutput {
		fold.replaced[index] = true
	}
}

func (fold *seedFold) foldStatement(stmt *ast.LetStatement, effect StatementEffect) {
	for _, targetIndex := range effect.CalleeReadsSeed {
		fold.read(stmt.Name[targetIndex].Value)
	}

	for _, write := range effect.Writes {
		if effect.replacesTarget(write) {
			fold.replace(stmt.Name[write.TargetIndex].Value)
		}
	}

	for _, target := range stmt.Name {
		if !isDiscard(target) {
			fold.defined[target.Value] = struct{}{}
		}
	}
}

// deriveBodySeedEffects summarizes, per output, whether the scalar body may
// read that output's incoming value. Explicit reads come from conditions,
// values, print arguments, and resolved formatting markers; implicit reads
// come from callees that read their own seed. A boundary-resolved write only
// preserves the seed, so it does not replace the output.
func deriveBodySeedEffects(template *ast.FuncStatement, statements map[*ast.LetStatement]StatementEffect, isGlobal func(string) bool) []SeedEffect {
	fold := newSeedFold(template, isGlobal)

	for _, statement := range template.Body.Statements {
		for _, name := range statementReadNames(statement, fold.isDefined) {
			fold.read(name)
		}

		stmt, ok := statement.(*ast.LetStatement)
		if !ok {
			continue
		}

		effect, exists := statements[stmt]
		if !exists || !validStatementEffect(stmt, effect) {
			return slices.Repeat([]SeedEffect{SeedInvalid}, len(template.Outputs))
		}
		fold.foldStatement(stmt, effect)
	}

	return fold.effects
}

// statementReadNames returns every identifier a statement reads in source
// order, including resolved formatting markers and their dynamic width and
// precision operands.
func statementReadNames(statement ast.Statement, isDefined func(string) bool) []string {
	var names []string

	switch stmt := statement.(type) {
	case *ast.LetStatement:
		for _, condition := range stmt.Condition {
			names = appendExprReadNames(names, condition, isDefined)
		}
		for _, value := range stmt.Value {
			names = appendExprReadNames(names, value, isDefined)
		}
	case *ast.PrintStatement:
		for _, argument := range stmt.Expression.Arguments {
			names = appendExprReadNames(names, argument, isDefined)
		}
	}

	return names
}

func appendExprReadNames(names []string, expr ast.Expression, isDefined func(string) bool) []string {
	switch e := expr.(type) {
	case *ast.Identifier:
		return append(names, e.Value)
	case *ast.StringLiteral:
		mains, specs := formatMarkerIdentifiers(e.Token.Literal, isDefined)
		return append(append(names, mains...), specs...)
	}

	for _, child := range ast.ExprChildren(expr) {
		names = appendExprReadNames(names, child, isDefined)
	}

	return names
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
// Both directions are conservative, and either one must requeue callers.
func (ts *TypeSolver) deriveEffectNode(graph *specializationCallGraph, working []bodyEffects, id specializationNodeID) bool {
	node := &graph.nodes[id]
	walked := ts.walkedFuncs[node.mangled]
	initial := functionInitialBindings(walked.template)
	compiler := ts.ScriptCompiler.Compiler
	analyzer := newEffectAnalyzer(compiler, node.mangled, graph, working)
	statements := analyzer.deriveStatements(walked.template.Body.Statements, initial)
	writes := deriveBodyOutputEffects(walked.template, statements)
	seeds := deriveBodySeedEffects(walked.template, statements, compiler.CodeCompiler.isGlobalBinding)

	outputs := len(walked.info.Sig.OutTypes)
	if !validPublishedEffects(writes, outputs) || !validPublishedSeedEffects(seeds, outputs) {
		panic(fmt.Sprintf("internal: invalid effects for specialization %s", node.mangled))
	}

	changed := false
	for outputIndex := range outputs {
		if working[id].writes[outputIndex] == MustWrite && writes[outputIndex] == MayWrite {
			working[id].writes[outputIndex] = MayWrite
			changed = true
		}
		if working[id].seeds[outputIndex] == NoSeedRead && seeds[outputIndex] == MaySeedRead {
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

func functionInitialBindings(template *ast.FuncStatement) map[string]struct{} {
	defined := make(map[string]struct{}, len(template.Parameters))

	for _, parameter := range template.Parameters {
		defined[parameter.Value] = struct{}{}
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
