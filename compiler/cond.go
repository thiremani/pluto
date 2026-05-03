package compiler

import (
	"fmt"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

// condTemp holds a pre-compiled LHS operand and its source expression, used to
// free heap temporaries on the false branch of conditional expression lowering.
type condTemp struct {
	expr ast.Expression
	syms []*Symbol
}

// evalConditions compiles a list of condition expressions, ANDs all resulting
// i1 values together, and incorporates bounds guard checks. The caller must
// call pushBoundsGuard before and popBoundsGuard after.
func (c *Compiler) evalConditions(exprs []ast.Expression, guardPtr llvm.Value) llvm.Value {
	var cond llvm.Value
	for _, expr := range exprs {
		condSyms := c.compileExpression(expr, nil)
		for _, condSym := range condSyms {
			if cond.IsNil() {
				cond = condSym.Val
			} else {
				cond = c.builder.CreateAnd(cond, condSym.Val, "and_cond")
			}
		}
	}

	if c.stmtBoundsUsed() {
		boundsOK := c.createLoad(guardPtr, Int{Width: 1}, "cond_bounds_ok")
		cond = c.builder.CreateAnd(cond, boundsOK, "and_cond_bounds")
	}
	return cond
}

func (c *Compiler) compileConditions(stmt *ast.LetStatement) (cond llvm.Value, hasConditions bool) {
	if len(stmt.Condition) == 0 {
		hasConditions = false
		return
	}

	guardPtr := c.pushBoundsGuard("cond_bounds_guard")
	defer c.popBoundsGuard()

	hasConditions = true
	cond = c.evalConditions(stmt.Condition, guardPtr)
	return
}

// collectPromotableCallArgIdentifiers walks an expression and records bare
// identifier call arguments that lower indirectly. Those identifiers may be
// promoted to memory by lowerCallArgs, so conditional lowering pre-promotes
// only that subset before branching.
func (c *Compiler) collectPromotableCallArgIdentifiers(expr ast.Expression, out map[string]struct{}) {
	if ce, ok := expr.(*ast.CallExpression); ok {
		c.addPromotableArgs(ce, out)
	}
	for _, child := range ast.ExprChildren(expr) {
		c.collectPromotableCallArgIdentifiers(child, out)
	}
}

func (c *Compiler) addPromotableArgs(ce *ast.CallExpression, out map[string]struct{}) {
	info := c.ExprCache[key(c.FuncNameMangled, ce)]
	if info == nil {
		return
	}

	paramTypes := c.inferCallParamTypes(info)
	mangled := Mangle(c.MangledPath, ce.Function.Value, paramTypes)
	fnInfo := c.FuncCache[mangled]
	if fnInfo == nil {
		return
	}

	abi := classifyFuncABI(paramTypes, fnInfo.OutTypes)
	for i, arg := range ce.Arguments {
		if abi.Params[i].Mode != ABIParamIndirect {
			continue
		}
		ident, ok := arg.(*ast.Identifier)
		if !ok {
			continue
		}
		out[ident.Value] = struct{}{}
	}
}

// prePromoteConditionalCallArgs promotes local identifiers that are used as
// indirect call arguments so branch codegen does not introduce path-dependent
// promotions.
func (c *Compiler) prePromoteConditionalCallArgs(exprs []ast.Expression) {
	argNames := make(map[string]struct{})
	for _, expr := range exprs {
		c.collectPromotableCallArgIdentifiers(expr, argNames)
	}

	for name := range argNames {
		c.promoteExistingSym(name)
	}
}

func (c *Compiler) collectOutTypes(stmt *ast.LetStatement) []Type {
	outTypes := []Type{}
	for _, expr := range stmt.Value {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		outTypes = append(outTypes, info.OutTypes...)
	}
	return outTypes
}

// resolveDestSeed returns the current destination value when it already exists
// in scope, or the type zero value for a fresh destination.
func (c *Compiler) resolveDestSeed(ident *ast.Identifier, outType Type) *Symbol {
	existing, ok := Get(c.Scopes, ident.Value)
	if !ok {
		return c.makeZeroValue(outType)
	}
	return c.valueSymbol(ident.Value, existing, ident.Value+"_cond_seed")
}

func (c *Compiler) createConditionalTempOutputs(stmt *ast.LetStatement) ([]*ast.Identifier, []Type) {
	outTypes := c.resolvedDestTypes(stmt.Name, c.collectOutTypes(stmt))
	return c.createConditionalTempOutputsFor(stmt.Name, outTypes), outTypes
}

func (c *Compiler) createConditionalTempOutputsFor(dest []*ast.Identifier, outTypes []Type) []*ast.Identifier {
	tempNames := make([]*ast.Identifier, len(dest))
	for i, ident := range dest {
		tempName := fmt.Sprintf("condtmp_%s_%d", ident.Value, c.tmpCounter)
		c.tmpCounter++
		tempIdent := &ast.Identifier{Value: tempName}

		ptr := c.createEntryBlockAlloca(c.mapToLLVMType(outTypes[i]), tempName+".mem")
		tempSym := &Symbol{
			Val:      ptr,
			Type:     Ptr{Elem: outTypes[i]},
			Borrowed: true,
		}
		seed := c.resolveDestSeed(ident, outTypes[i])
		c.storeSymbolToSlot(tempSym, seed, outTypes[i], tempName+"_seed")

		// Temporary conditional outputs are borrowed so scope cleanup does not free
		// values that are transferred to real destinations in the merge block.
		Put(c.Scopes, tempName, tempSym)
		tempNames[i] = tempIdent
	}
	return tempNames
}

func (c *Compiler) commitConditionalOutputs(dest []*ast.Identifier, tempNames []*ast.Identifier, outTypes []Type) {
	for i, ident := range dest {
		tempSym, _ := Get(c.Scopes, tempNames[i].Value)

		finalType := outTypes[i]
		finalVal := c.createLoad(tempSym.Val, finalType, ident.Value+"_cond_final")
		finalSym := &Symbol{
			Val:  finalVal,
			Type: finalType,
		}

		oldSym, exists := Get(c.Scopes, ident.Value)
		if !exists {
			Put(c.Scopes, ident.Value, finalSym)
			continue
		}

		if _, ok := oldSym.Type.(Ptr); ok {
			c.storeSymbolToSlot(oldSym, finalSym, oldSym.Type.(Ptr).Elem, ident.Value+"_cond_commit")

			// Keep pointer element type in sync (important for string ownership flags).
			updated := GetCopy(oldSym)
			updated.Type = Ptr{Elem: finalType}
			if !SetExisting(c.Scopes, ident.Value, updated) {
				Put(c.Scopes, ident.Value, updated)
			}
			continue
		}

		// Non-pointer symbols are replaced directly. Old value ownership is already
		// handled in the IF branch assignment into temp slots.
		Put(c.Scopes, ident.Value, finalSym)
	}
}

func tempNamesToStrings(tempNames []*ast.Identifier) []string {
	names := make([]string, len(tempNames))
	for i, ident := range tempNames {
		names[i] = ident.Value
	}
	return names
}

// aliasCondDests maps existing destination names to conditional temp slots so
// RHS reads during IF-branch assignment see the latest temp writes.
func (c *Compiler) aliasCondDests(dest []*ast.Identifier, tempNames []*ast.Identifier) map[string]*Symbol {
	aliases := make(map[string]*Symbol, len(dest))

	for i, ident := range dest {
		oldSym, exists := Get(c.Scopes, ident.Value)
		if !exists {
			continue
		}
		tempSym, ok := Get(c.Scopes, tempNames[i].Value)
		if !ok {
			continue
		}
		aliases[ident.Value] = oldSym
		SetExisting(c.Scopes, ident.Value, tempSym)
	}

	return aliases
}

func (c *Compiler) restoreCondDests(aliases map[string]*Symbol) {
	for name, oldSym := range aliases {
		SetExisting(c.Scopes, name, oldSym)
	}
}

// compileCondAssignments wraps compileAssignments for conditional lowering.
// Existing destination names are temporarily aliased to temp slots so
// self-referential RHS expressions read/write the same evolving slot.
func (c *Compiler) compileCondAssignments(tempNames []*ast.Identifier, dest []*ast.Identifier, exprs []ast.Expression) {
	aliases := c.aliasCondDests(dest, tempNames)
	c.compileAssignments(tempNames, dest, exprs)
	c.restoreCondDests(aliases)
}

func (c *Compiler) compileCondAssignmentValues(
	tempNames []*ast.Identifier,
	dest []*ast.Identifier,
	exprs []ast.Expression,
) ([]*Symbol, []*Symbol, []string, []int) {
	aliases := c.aliasCondDests(dest, tempNames)
	oldValues := c.captureOldValues(tempNames)
	syms, rhsNames, resCounts := c.compileAssignmentValues(tempNames, exprs)
	c.restoreCondDests(aliases)
	return oldValues, syms, rhsNames, resCounts
}

func (c *Compiler) compileCondAssignmentsWithGuard(tempNames []*ast.Identifier, dest []*ast.Identifier, exprs []ast.Expression, guardPtr llvm.Value) {
	oldValues, syms, rhsNames, resCounts := c.compileCondAssignmentValues(tempNames, dest, exprs)
	c.finishAssignmentsWithGuard(tempNames, dest, exprs, oldValues, syms, rhsNames, resCounts, guardPtr)
}

func (c *Compiler) createStageTempOutputsFor(dest []*ast.Identifier) []*ast.Identifier {
	tempNames := make([]*ast.Identifier, len(dest))
	for i, ident := range dest {
		commitTempSym, _ := Get(c.Scopes, ident.Value)
		ptrType := commitTempSym.Type.(Ptr)
		outType := ptrType.Elem

		tempName := fmt.Sprintf("condstage_%s_%d", ident.Value, c.tmpCounter)
		c.tmpCounter++
		tempIdent := &ast.Identifier{Value: tempName}

		ptr := c.createEntryBlockAlloca(c.mapToLLVMType(outType), tempName+".mem")
		stageTempSym := &Symbol{
			Val:      ptr,
			Type:     Ptr{Elem: outType},
			Borrowed: true,
		}
		seed := c.resolveDestSeed(ident, outType)
		seed = c.deepCopyIfNeeded(seed)
		c.storeSymbolToSlot(stageTempSym, seed, outType, tempName+"_seed")
		Put(c.Scopes, tempName, stageTempSym)
		tempNames[i] = tempIdent
	}
	return tempNames
}

func (c *Compiler) commitStageTempOutputs(dest []*ast.Identifier, stageTempNames []*ast.Identifier) {
	for i, ident := range dest {
		stageSym, _ := Get(c.Scopes, stageTempNames[i].Value)
		destSym, _ := Get(c.Scopes, ident.Value)

		oldValue := c.valueSymbol(ident.Value, destSym, ident.Value+"_stage_old")
		ptrType := destSym.Type.(Ptr)

		stagedValue := c.valueSymbol(stageTempNames[i].Value, stageSym, stageTempNames[i].Value+"_stage_final")
		c.storeSymbolToSlot(destSym, stagedValue, ptrType.Elem, ident.Value+"_stage_commit")

		if c.skipBorrowedOldValueFree(oldValue) {
			continue
		}
		c.freeSymbolValue(oldValue, ident.Value+"_stage_old")
	}
}

type condStageGroup struct {
	commitTempNames []*ast.Identifier
	stageTempNames  []*ast.Identifier
}

func (c *Compiler) stageCondRangedExpr(expr ast.Expression, dest []*ast.Identifier, stageTempNames []*ast.Identifier) {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	stageAliases := c.aliasCondDests(dest, stageTempNames)
	defer c.restoreCondDests(stageAliases)

	compileStageAssign := func() {
		guardPtr := c.pushBoundsGuard("cond_value_guard")
		defer c.popBoundsGuard()

		c.compileCondAssignmentsWithGuard(stageTempNames, dest, []ast.Expression{expr}, guardPtr)
	}

	c.withLoopNestVersioned(info.Ranges, []ast.Expression{expr}, func() {
		if c.hasCondExprInTree(expr) {
			c.compileCondExprValue(expr, llvm.Value{}, compileStageAssign)
			return
		}

		compileStageAssign()
	})
}

func (c *Compiler) stageCondRangedAssignments(
	assignExprs []ast.Expression,
	assignDests []*ast.Identifier,
	commitTempNames []*ast.Identifier,
) []condStageGroup {
	assignTargetIdx := 0
	groups := make([]condStageGroup, 0, len(assignExprs))

	// Two alias layers are active here. The outer alias makes destination reads
	// resolve to commit temps while stage temps are seeded. stageCondRangedExpr
	// then temporarily aliases the same destinations to private stage temps so
	// self-referential RHS expressions read/write only that staged result.
	allAliases := c.aliasCondDests(assignDests, commitTempNames)
	defer c.restoreCondDests(allAliases)

	for _, expr := range assignExprs {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		numOutputs := len(info.OutTypes)
		exprCommitTempNames := commitTempNames[assignTargetIdx : assignTargetIdx+numOutputs]
		exprDestNames := assignDests[assignTargetIdx : assignTargetIdx+numOutputs]
		stageTempNames := c.createStageTempOutputsFor(exprCommitTempNames)

		c.stageCondRangedExpr(expr, exprDestNames, stageTempNames)
		groups = append(groups, condStageGroup{
			commitTempNames: exprCommitTempNames,
			stageTempNames:  stageTempNames,
		})
		assignTargetIdx += numOutputs
	}

	return groups
}

func (c *Compiler) commitCondRangedStages(groups []condStageGroup) {
	for _, group := range groups {
		c.commitStageTempOutputs(group.commitTempNames, group.stageTempNames)
		DeleteBulk(c.Scopes, tempNamesToStrings(group.stageTempNames))
	}
}

// compileCondStatement lowers:
//
//	name = cond value
//
// to:
//
// 1. Allocate per-output temp slots seeded with current destination value (or zero for new vars)
// 2. IF branch: alias existing destination names to temp slots, then compile assignment
// 3. ELSE branch: no-op (seed values already represent the else result)
// 4. Merge: commit temp slot values to real destinations once
func (c *Compiler) compileCondStatement(stmt *ast.LetStatement, cond llvm.Value) {
	// compileArgs may promote identifier call args by creating an alloca in entry
	// and storing the current value at the call site. If promotion happens only in
	// the IF block, the false path would skip that store and later loads can read
	// uninitialized memory. Pre-promote here so storage is initialized on all paths.
	c.prePromoteConditionalCallArgs(stmt.Value)

	tempNames, outTypes := c.createConditionalTempOutputs(stmt)

	ifBlock, contBlock := c.createIfCont(cond, "if", "continue")

	c.builder.SetInsertPointAtEnd(ifBlock)
	c.compileCondAssignments(tempNames, stmt.Name, stmt.Value)
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
	c.commitConditionalOutputs(stmt.Name, tempNames, outTypes)
	DeleteBulk(c.Scopes, tempNamesToStrings(tempNames))
}

// valuesHaveCondExpr returns true if any value expression contains an
// embedded cond-expr (scalar comparison in value position).
func (c *Compiler) valuesHaveCondExpr(values []ast.Expression) bool {
	for _, expr := range values {
		if c.hasCondExprInTree(expr) {
			return true
		}
	}
	return false
}

// hasCondExprInTree returns true if any node in the expression tree has
// conditional value lowering.
func (c *Compiler) hasCondExprInTree(expr ast.Expression) bool {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if info != nil && info.HasCondExpr() {
		return true
	}
	for _, child := range ast.ExprChildren(expr) {
		if c.hasCondExprInTree(child) {
			return true
		}
	}
	return false
}

func (c *Compiler) logicalOrCondExpr(expr ast.Expression) (*ast.InfixExpression, bool) {
	infix, ok := expr.(*ast.InfixExpression)
	if !ok || infix.Operator != token.SYM_LOGICAL_OR {
		return nil, false
	}

	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	return infix, info != nil && info.HasCondOr()
}

func (c *Compiler) hasLogicalOrCondExprInTree(expr ast.Expression) bool {
	if _, ok := c.logicalOrCondExpr(expr); ok {
		return true
	}
	for _, child := range ast.ExprChildren(expr) {
		if c.hasLogicalOrCondExprInTree(child) {
			return true
		}
	}
	return false
}

// handleComparisons processes each slot of a multi-return comparison based on
// its CondMode. CondScalar slots are compared and ANDed into cond. CondArray
// slots are compiled as array filters (source freed, marked borrowed).
func (c *Compiler) handleComparisons(op string, left, right []*Symbol, info *ExprInfo, cond llvm.Value) ([]*Symbol, llvm.Value) {
	lhsSyms := make([]*Symbol, len(left))
	for i := range left {
		switch info.CompareModes[i] {
		case CondScalar:
			lSym, cmpVal := c.compareScalars(op, left[i], right[i])
			if cond.IsNil() {
				cond = cmpVal
			} else {
				cond = c.builder.CreateAnd(cond, cmpVal, fmt.Sprintf("and_cond_%d", i))
			}
			lhsSyms[i] = lSym
		case CondArray:
			// compileArrayFilter handles deref internally
			lhsSyms[i] = c.compileArrayFilter(op, left[i], right[i], info.OutTypes[i])
			c.freeSymbolValue(left[i], "")
			left[i].Borrowed = true
		}
	}
	return lhsSyms, cond
}

// extractCondExprs walks the expression tree, evaluates each CondScalar
// comparison, ANDs results into cond, and stores LHS values in the
// statement-local condLHS map for substitution during later value compilation.
// LHS temporaries are appended to temps so the caller can free them on the
// false path.
func (c *Compiler) extractCondExprs(expr ast.Expression, cond llvm.Value, temps []condTemp) (llvm.Value, []condTemp) {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]

	// Handle conditional expression (comparison in value position).
	// Comparisons with ranges can be extracted only when all required iterators
	// are already bound by an outer loop (no pending ranges).
	if infix, ok := expr.(*ast.InfixExpression); ok && info.HasCondScalar() && len(c.pendingLoopRanges(info.Ranges)) == 0 {
		// Bottom-up: extract conditions from operands first
		cond, temps = c.extractCondExprs(infix.Left, cond, temps)
		cond, temps = c.extractCondExprs(infix.Right, cond, temps)

		// Compile both operands (may return pre-extracted values)
		left := c.compileExpression(infix.Left, nil)
		right := c.compileExpression(infix.Right, nil)

		var lhsSyms []*Symbol
		lhsSyms, cond = c.handleComparisons(infix.Operator, left, right, info, cond)

		c.requireCondLHSFrame()[key(c.FuncNameMangled, expr)] = lhsSyms
		temps = append(temps, condTemp{infix.Left, left})
		// Free right-side temporaries (only used for comparison).
		// Left-side values are retained in condLHS for later substitution.
		c.freeTemporary(infix.Right, right)
		return cond, temps
	}

	// Not a conditional expression — recurse into children
	for _, child := range ast.ExprChildren(expr) {
		cond, temps = c.extractCondExprs(child, cond, temps)
	}
	return cond, temps
}

// cleanupCondExprElse frees temporaries retained during cond-expr extraction
// that are not consumed when the condition evaluates to false.
func (c *Compiler) cleanupCondExprElse(temps []condTemp) {
	for _, tmp := range temps {
		c.freeTemporary(tmp.expr, tmp.syms)
	}
	for exprKey, lhsSyms := range c.requireCondLHSFrame() {
		exprInfo := c.ExprCache[exprKey]
		for i, mode := range exprInfo.CompareModes {
			if mode == CondArray {
				c.freeSymbolValue(lhsSyms[i], "")
			}
		}
	}
}

// compileCondExprValue extracts cond-expr predicates for expr, branches through
// value-position conditions, and compiles onTrue on admitted paths only. Plain
// comparisons compose as AND; value-position logical OR tries the left operand
// first and falls back to the right operand only on the left false path.
func (c *Compiler) compileCondExprValue(expr ast.Expression, baseCond llvm.Value, onTrue func()) {
	c.pushCondLHSFrame()
	defer c.popCondLHSFrame()

	c.compileCondExprValueInFrame(expr, baseCond, onTrue)
}

func (c *Compiler) compileCondExprValueInFrame(expr ast.Expression, baseCond llvm.Value, onTrue func()) {
	if c.hasLogicalOrCondExprInTree(expr) {
		c.compileCondExprAlternative(expr, baseCond, onTrue, func() {})
		return
	}

	cond, temps := c.extractCondExprs(expr, baseCond, nil)
	c.compileCondExprBranch(cond, temps, onTrue)
}

// compileOperandCondExprValue extracts conditional expressions from expr's
// operands while leaving expr itself to the caller. Use this when the caller is
// compiling the root operation into an already-seeded output slot.
func (c *Compiler) compileOperandCondExprValue(expr ast.Expression, baseCond llvm.Value, onTrue func()) {
	c.pushCondLHSFrame()
	defer c.popCondLHSFrame()

	if c.hasLogicalOrCondExprInTree(expr) {
		c.compileCondExprChildrenInFrame(ast.ExprChildren(expr), baseCond, onTrue, func() {})
		return
	}

	cond := baseCond
	var temps []condTemp
	for _, child := range ast.ExprChildren(expr) {
		cond, temps = c.extractCondExprs(child, cond, temps)
	}
	c.compileCondExprBranch(cond, temps, onTrue)
}

func (c *Compiler) compileCondExprBranch(cond llvm.Value, temps []condTemp, onTrue func()) {
	c.compileCondExprBranchWithFailure(cond, temps, onTrue, func() {})
}

func (c *Compiler) compileCondExprBranchWithFailure(cond llvm.Value, temps []condTemp, onTrue func(), onFalse func()) {
	if cond.IsNil() {
		onTrue()
		return
	}

	ifBlock, elseBlock, contBlock := c.createIfElseCont(cond, "cond_if", "cond_else", "cond_cont")

	c.builder.SetInsertPointAtEnd(ifBlock)
	onTrue()
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(elseBlock)
	c.cleanupCondExprElse(temps)
	onFalse()
	c.builder.CreateBr(contBlock)

	c.builder.SetInsertPointAtEnd(contBlock)
}

func (c *Compiler) compileCondExprChildrenInFrame(children []ast.Expression, baseCond llvm.Value, onTrue func(), onFalse func()) {
	if len(children) == 0 {
		c.compileCondExprBranchWithFailure(baseCond, nil, onTrue, onFalse)
		return
	}

	child := children[0]
	rest := children[1:]
	if c.hasCondExprInTree(child) {
		c.compileCondExprAlternative(child, baseCond, func() {
			c.compileCondExprChildrenInFrame(rest, llvm.Value{}, onTrue, onFalse)
		}, onFalse)
		return
	}

	c.compileCondExprChildrenInFrame(rest, baseCond, onTrue, onFalse)
}

func (c *Compiler) compileCondExprAlternative(expr ast.Expression, baseCond llvm.Value, onTrue func(), onFalse func()) {
	if logicalOr, ok := c.logicalOrCondExpr(expr); ok {
		c.compileLogicalOrCondExprAlternative(logicalOr, baseCond, onTrue, onFalse)
		return
	}

	if !c.hasLogicalOrCondExprInTree(expr) {
		cond, temps := c.extractCondExprs(expr, baseCond, nil)
		c.compileCondExprBranchWithFailure(cond, temps, onTrue, onFalse)
		return
	}

	c.compileCondExprChildrenInFrame(ast.ExprChildren(expr), baseCond, func() {
		if infix, ok := expr.(*ast.InfixExpression); ok {
			info := c.ExprCache[key(c.FuncNameMangled, infix)]
			if info != nil && info.HasCondScalar() && len(c.pendingLoopRanges(info.Ranges)) == 0 {
				left := c.compileExpression(infix.Left, nil)
				right := c.compileExpression(infix.Right, nil)

				lhsSyms, cond := c.handleComparisons(infix.Operator, left, right, info, llvm.Value{})
				c.requireCondLHSFrame()[key(c.FuncNameMangled, expr)] = lhsSyms
				temps := []condTemp{{infix.Left, left}}
				c.freeTemporary(infix.Right, right)
				c.compileCondExprBranchWithFailure(cond, temps, onTrue, onFalse)
				return
			}
		}

		onTrue()
	}, onFalse)
}

func (c *Compiler) withCondLHS(expr ast.Expression, syms []*Symbol, body func()) {
	frame := c.requireCondLHSFrame()
	exprKey := key(c.FuncNameMangled, expr)
	old, hadOld := frame[exprKey]
	frame[exprKey] = syms
	body()
	if hadOld {
		frame[exprKey] = old
		return
	}
	delete(frame, exprKey)
}

func (c *Compiler) compileLogicalOrCondExprAlternative(expr *ast.InfixExpression, baseCond llvm.Value, onTrue func(), onFalse func()) {
	leftTrue := func() {
		left := c.compileExpression(expr.Left, nil)
		c.withCondLHS(expr, left, onTrue)
	}
	rightTrue := func() {
		right := c.compileExpression(expr.Right, nil)
		c.withCondLHS(expr, right, onTrue)
	}

	c.compileCondExprAlternative(expr.Left, baseCond, leftTrue, func() {
		c.compileCondExprAlternative(expr.Right, baseCond, rightTrue, onFalse)
	})
}

func (c *Compiler) isRangeDriverCond(expr ast.Expression) bool {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if len(info.OutTypes) != 1 {
		return false
	}

	return isRangeDriverType(info.OutTypes[0])
}

func (c *Compiler) collectDriverRanges(expr ast.Expression) []*RangeInfo {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if len(info.Ranges) > 0 {
		return info.Ranges
	}

	ident, ok := expr.(*ast.Identifier)
	if !ok {
		panic(fmt.Sprintf("internal: bare range driver %T missing cached ranges", expr))
	}
	return []*RangeInfo{{Name: ident.Value}}
}

// splitCondRanges collects merged ranges and boolean guard expressions
// from statement conditions. Bare range/array-range drivers contribute only
// ranges; comparisons contribute both ranges and a per-iteration guard.
// Returns nil, nil if no condition introduces ranges.
func (c *Compiler) splitCondRanges(conditions []ast.Expression) ([]*RangeInfo, []ast.Expression) {
	var ranges []*RangeInfo
	var condExprs []ast.Expression
	for _, expr := range conditions {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		if c.isRangeDriverCond(expr) {
			ranges = mergeUses(ranges, c.collectDriverRanges(expr))
			continue
		}

		if len(info.Ranges) == 0 {
			continue
		}

		ranges = mergeUses(ranges, info.Ranges)
		if info.Rewrite != nil {
			condExprs = append(condExprs, info.Rewrite)
			continue
		}
		condExprs = append(condExprs, expr)
	}
	if len(ranges) == 0 {
		return nil, nil
	}
	return ranges, condExprs
}

// withCondRangeLoop sets up the shared loop+guard+branch scaffold used by
// both accumulation and iteration paths: loop over all ranges, evaluate
// conditions, branch on the combined result, and call body on the true path.
// Probes cover every expression that can issue array accesses inside that loop
// region, letting the affine fast path apply uniformly when it can be proven.
func (c *Compiler) withCondRangeLoop(allRanges []*RangeInfo, condExprs []ast.Expression, probes []ast.Expression, guardName, ifName, contName string, body func()) {
	c.withLoopNestVersioned(allRanges, probes, func() {
		if len(condExprs) == 0 {
			body()
			return
		}

		guardPtr := c.pushBoundsGuard(guardName)
		combinedCond := c.evalConditions(condExprs, guardPtr)
		c.popBoundsGuard()

		ifBlock, contBlock := c.createIfCont(combinedCond, ifName, contName)

		c.builder.SetInsertPointAtEnd(ifBlock)
		body()
		c.builder.CreateBr(contBlock)

		c.builder.SetInsertPointAtEnd(contBlock)
	})
}

// compileCondRangedStatement lowers ranged statement conditions.
// Statement conditions are shared across the whole assignment. They determine
// the outer admitted iteration domain, while each RHS expression keeps any
// extra local drivers to itself inside that shared gate. Top-level 1D array
// literals accumulate across admitted iterations; all other outputs use normal
// conditional iteration (last value wins).
func (c *Compiler) compileCondRangedStatement(stmt *ast.LetStatement, condRanges []*RangeInfo, condExprs []ast.Expression) {
	c.prePromoteConditionalCallArgs(stmt.Value)

	assignExprs := []ast.Expression{}
	assignDests := []*ast.Identifier{}
	assignOutTypes := []Type{}
	assignCollectorTemps := []string{}
	// loopProbes collect every expression in the shared ranged-condition loop
	// that can issue array accesses, so affine versioning can prove the whole
	// region safe up front instead of only individual RHS shapes.
	loopProbes := append([]ast.Expression{}, condExprs...)

	accumLits := []*ast.ArrayLiteral{}
	accumAccs := []*ArrayAccumulator{}
	accumDests := []*ast.Identifier{}
	accumOldValues := []*Symbol{}

	targetIdx := 0
	for _, expr := range stmt.Value {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		numOutputs := len(info.OutTypes)

		if lit, ok := expr.(*ast.ArrayLiteral); ok && len(lit.Headers) == 0 && len(lit.Rows) == 1 {
			accumDest := stmt.Name[targetIdx]
			accumLits = append(accumLits, lit)
			accumAccs = append(accumAccs, c.NewArrayAccumulator(info.OutTypes[0].(Array)))
			accumDests = append(accumDests, accumDest)
			accumOldValues = append(accumOldValues, c.captureOldValues([]*ast.Identifier{accumDest})[0])
			resolvedLit, _ := c.resolveArrayLiteralRewrite(lit)
			loopProbes = append(loopProbes, resolvedLit)
			targetIdx += numOutputs
			continue
		}

		preparedExpr, collectorTemps := c.prepareCollectorTreeFor(expr, condRanges, condExprs)
		assignExprs = append(assignExprs, preparedExpr)
		loopProbes = append(loopProbes, preparedExpr)
		assignCollectorTemps = append(assignCollectorTemps, collectorTemps...)
		for i := 0; i < numOutputs; i++ {
			dest := stmt.Name[targetIdx+i]
			assignDests = append(assignDests, dest)
			assignOutTypes = append(assignOutTypes, c.bindingSlotType(dest.Value, info.OutTypes[i]))
		}
		targetIdx += numOutputs
	}

	hasAssigns := len(assignDests) > 0
	defer c.cleanupMaterializedCollectors(assignCollectorTemps)

	var assignTempNames []*ast.Identifier
	if hasAssigns {
		assignTempNames = c.createConditionalTempOutputsFor(assignDests, assignOutTypes)
	}

	c.withCondRangeLoop(condRanges, condExprs, loopProbes, "cond_iter_guard", "cond_iter_if", "cond_iter_cont", func() {
		c.compileCondRangedIteration(
			assignExprs, assignDests, assignTempNames,
			accumAccs, accumLits,
		)
	})

	if hasAssigns {
		c.commitConditionalOutputs(assignDests, assignTempNames, assignOutTypes)
		DeleteBulk(c.Scopes, tempNamesToStrings(assignTempNames))
	}

	for i, acc := range accumAccs {
		result := c.ArrayAccResult(acc)
		c.storeValue(accumDests[i].Value, result, false)
		if accumOldValues[i] == nil || c.skipBorrowedOldValueFree(accumOldValues[i]) {
			continue
		}
		c.freeSymbolValue(accumOldValues[i], "old_accum")
	}
}

// compileCondRangedIteration runs inside the per-iteration body of
// compileCondRangedStatement. It stages scalar assignments independently
// and appends array literal cells to accumulators.
func (c *Compiler) compileCondRangedIteration(
	assignExprs []ast.Expression,
	assignDests []*ast.Identifier,
	commitTempNames []*ast.Identifier,
	accumAccs []*ArrayAccumulator,
	accumLits []*ast.ArrayLiteral,
) {
	// Accum-only: no assigns, just push cells.
	if len(assignDests) == 0 {
		c.appendArrayLiterals(accumAccs, accumLits)
		return
	}

	// The outer statement condition admits one iteration here, but sibling RHS
	// expressions may still skip later in that same admitted step. Each RHS first
	// writes into a private stage temp under its own bounds guard, so a local skip
	// leaves that stage temp seeded with the prior value without suppressing
	// sibling RHS writes.
	stageGroups := c.stageCondRangedAssignments(assignExprs, assignDests, commitTempNames)

	if len(accumLits) > 0 {
		c.appendArrayLiterals(accumAccs, accumLits)
	}

	c.commitCondRangedStages(stageGroups)
}

// compileCondExprStatement handles let statements that have conditional
// expressions (comparisons) embedded in their value expressions.
// Each value expression is processed independently: its conditions are
// ANDed with statement conditions and branched on separately, so
// p, q = a > 2, d < 10 evaluates each condition independently rather
// than ANDing them all-or-nothing.
func (c *Compiler) compileCondExprStatement(stmt *ast.LetStatement, stmtCond llvm.Value) {
	c.prePromoteConditionalCallArgs(stmt.Value)

	tempNames, outTypes := c.createConditionalTempOutputs(stmt)

	targetIdx := 0
	for _, expr := range stmt.Value {
		info := c.ExprCache[key(c.FuncNameMangled, expr)]
		numOutputs := len(info.OutTypes)
		exprTempNames := tempNames[targetIdx : targetIdx+numOutputs]
		exprDestNames := stmt.Name[targetIdx : targetIdx+numOutputs]
		exprValues := []ast.Expression{expr}

		c.compileCondExprValue(expr, stmtCond, func() {
			c.compileCondAssignments(exprTempNames, exprDestNames, exprValues)
		})

		targetIdx += numOutputs
	}

	c.commitConditionalOutputs(stmt.Name, tempNames, outTypes)
	DeleteBulk(c.Scopes, tempNamesToStrings(tempNames))
}
