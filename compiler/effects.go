package compiler

import (
	"fmt"
	"slices"

	"github.com/thiremani/pluto/ast"
)

// WriteEffect describes whether one named target is guaranteed to receive a
// value. Invalid marks a target whose facts could not be derived; it is not a
// lattice member.
type WriteEffect uint8

const (
	WriteInvalid WriteEffect = iota
	MustWrite
	MayWrite
)

func (effect WriteEffect) String() string {
	switch effect {
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
// ReadsSeed holds LHS indices whose existing value a direct call keeps at the
// assignment boundary when its call-owned domain may run no iteration.
type StatementEffect struct {
	Writes    []TargetWriteEffect
	ReadsSeed []int
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

// classifyWriteEffect keeps analysis states outside the yield lattice invalid,
// so missing facts never pass as MayWrite.
func classifyWriteEffect(yield YieldEffect, maySkip bool) WriteEffect {
	if yield != MustYield && yield != MayYield {
		return WriteInvalid
	}

	if yield == MayYield || maySkip {
		return MayWrite
	}

	return MustWrite
}

type effectAnalyzer struct {
	compiler        *Compiler
	funcNameMangled string
}

func newEffectAnalyzer(compiler *Compiler, mangled string) *effectAnalyzer {
	return &effectAnalyzer{
		compiler:        compiler,
		funcNameMangled: mangled,
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

// deriveCall yields every output of a call that is invoked, since every body
// that runs writes every output. Its arguments can still fail, and a range
// that may run no iteration can keep it from being invoked.
func (analyzer *effectAnalyzer) deriveCall(expr *ast.CallExpression) []YieldEffect {
	info := analyzer.exprInfo(expr)
	invocation := analyzer.callInvocationEffect(expr)
	if hasPossiblyEmptyRange(info.Ranges, nil) {
		invocation = joinYield(invocation, MayYield)
	}

	info.YieldEffects = slices.Repeat([]YieldEffect{invocation}, len(info.OutTypes))
	return info.YieldEffects
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

func (analyzer *effectAnalyzer) seedResolvedYield(expr ast.Expression, targetExists bool, conditionRanges []*RangeInfo) (YieldEffect, bool) {
	call, ok := expr.(*ast.CallExpression)
	if !ok || !targetExists {
		return YieldUncomputed, false
	}

	// Every body that runs writes every output, so only a call-owned domain
	// that may run no iteration leaves a direct return's seed in place.
	if _, direct := directScalarABIReturnType(analyzer.exprInfo(call).OutTypes); !direct {
		return YieldUncomputed, false
	}
	if !analyzer.callOwnsPossiblyEmptyDomain(call, conditionRanges) {
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

func (analyzer *effectAnalyzer) deriveStatements(statements []ast.Statement) map[*ast.LetStatement]StatementEffect {
	defined := make(map[string]struct{})
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

		for _, yield := range yields {
			index := targetIndex
			target := stmt.Name[index]
			targetIndex++
			if isDiscard(target) {
				continue
			}

			_, targetExists := defined[target.Value]
			if seededYield, readsSeed := analyzer.seedResolvedYield(expr, targetExists, condRanges); readsSeed {
				result.ReadsSeed = append(result.ReadsSeed, index)
				yield = seededYield
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

	lastTarget := -1
	for _, targetIndex := range effect.ReadsSeed {
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

func (ts *TypeSolver) deriveScriptEffects() {
	root := ts.ScriptCompiler.Script.Root
	analyzer := newEffectAnalyzer(ts.ScriptCompiler.Compiler, ts.ScriptCompiler.ScriptMangled)
	root.StatementEffects = analyzer.deriveStatements(ts.ScriptCompiler.Program.Statements)

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
