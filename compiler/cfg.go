package compiler

import (
	"fmt"
	"maps"
	"slices"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
	"github.com/thiremani/pluto/token"
)

// EventType labels a variable access as Read or Write.
type EventType int

const (
	Read             EventType = iota
	Write                      // A normal, unconditional write
	ConditionalWrite           // A write that is part of a conditional
)

// VarEvent records a single read or write of Name.
type VarEvent struct {
	Name  string
	Kind  EventType
	Token token.Token
}

// StmtNode wraps a single AST statement plus its read/write events.
type StmtNode struct {
	Stmt   ast.Statement
	Events []VarEvent
}

// BasicBlock is a straight-line sequence of statements.
type BasicBlock struct {
	Stmts []*StmtNode
}

// CFG owns template validation, which classifies writes from the text, and
// the script's effect-sensitive dataflow.
type CFG struct {
	CodeCompiler *CodeCompiler
	Blocks       []*BasicBlock
	Scopes       []Scope[struct{}]
	Errors       []*token.CompileError
}

func NewCFG(cc *CodeCompiler) *CFG {
	return &CFG{
		CodeCompiler: cc,
		Blocks:       make([]*BasicBlock, 0),
		Scopes:       []Scope[struct{}]{NewScope[struct{}](FuncScope)},
		Errors:       make([]*token.CompileError, 0),
	}
}

// PushBlock creates a new empty basic block.
func (cfg *CFG) PushBlock() {
	cfg.Blocks = append(cfg.Blocks, &BasicBlock{Stmts: []*StmtNode{}})
}

func (cfg *CFG) PopBlock() {
	if len(cfg.Blocks) == 0 {
		panic("internal: cannot pop CFG block: block stack is empty")
	}
	cfg.Blocks = cfg.Blocks[:len(cfg.Blocks)-1]
}

// collectReads reports formatting errors through cfg.Errors but never publishes
// bindings. Callers commit statement destinations after all reads are collected.
func (cfg *CFG) collectReads(expr ast.Expression) []VarEvent {
	switch e := expr.(type) {
	case *ast.IntegerLiteral, *ast.FloatLiteral:
		return nil
	case *ast.StringLiteral:
		return cfg.collectStringReads(e.Token.Literal, e.Token)
	case *ast.Identifier:
		return []VarEvent{{Name: e.Value, Kind: Read, Token: e.Tok()}}
	}

	children := ast.ExprChildren(expr)
	if children == nil {
		panic(fmt.Sprintf("unhandled expression type: %T", expr))
	}

	var reads []VarEvent
	for _, child := range children {
		reads = append(reads, cfg.collectReads(child)...)
	}
	// A sample is typed but never evaluated, so it is not a child; it still
	// names its variable, as a read does.
	if lit, ok := expr.(*ast.ArrayLiteral); ok {
		if name, isName := lit.Sample.(*ast.Identifier); isName {
			reads = append(reads, VarEvent{Name: name.Value, Kind: Read, Token: name.Tok()})
		}
	}

	return reads
}

// statementExpressions returns the expressions a statement evaluates: an
// assignment's conditions and values, or a print's items.
func statementExpressions(stmt ast.Statement) []ast.Expression {
	switch s := stmt.(type) {
	case *ast.LetStatement:
		return slices.Concat(s.Condition, s.Value)
	case *ast.PrintStatement:
		return s.Expression.Arguments
	default:
		return nil
	}
}

func (cfg *CFG) collectStatementReads(stmt ast.Statement) []VarEvent {
	var reads []VarEvent
	for _, expr := range statementExpressions(stmt) {
		reads = append(reads, cfg.collectReads(expr)...)
	}

	return reads
}

func (cfg *CFG) collectStringReads(value string, tok token.Token) []VarEvent {
	var reads []VarEvent
	runes := []rune(value)
	positions := newStringPositions(tok, runes)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' {
			_, next, _ := lexer.DecodeStringEscape(runes, i)
			i = next - 1
			continue
		}
		if !maybeMarker(runes, i) {
			continue
		}

		markerReads, end := cfg.collectMarkerReads(value, tok, runes, positions, i)
		reads = append(reads, markerReads...)
		i = end - 1
	}

	return reads
}

// An unknown main marker itself is literal text.
func (cfg *CFG) collectMarkerReads(value string, tok token.Token, runes []rune, positions *stringPositions, start int) ([]VarEvent, int) {
	mainID, end := parseIdentifier(runes, start+1)
	if !cfg.isDefined(mainID) {
		return nil, end
	}

	reads := []VarEvent{{Name: mainID, Kind: Read, Token: positions.at(start + 1)}}
	if end >= len(runes) || runes[end] != '%' {
		return reads, end
	}

	specifierReads, specifierEnd := cfg.collectSpecifierReads(value, tok, runes, positions, end)
	reads = append(reads, specifierReads...)
	return reads, specifierEnd
}

func (cfg *CFG) collectSpecifierReads(value string, tok token.Token, runes []rune, positions *stringPositions, start int) ([]VarEvent, int) {
	spec, err := parseSpecifierSyntax(tok, value, runes, start)
	var reads []VarEvent
	if err != nil {
		cfg.Errors = append(cfg.Errors, err)
		for _, specID := range spec.ids {
			if cfg.isDefined(specID.name) {
				reads = append(reads, VarEvent{Name: specID.name, Kind: Read, Token: positions.at(specID.index)})
			}
		}

		return reads, spec.end
	}

	for _, specID := range spec.ids {
		idTok := positions.at(specID.index)
		if !cfg.isDefined(specID.name) {
			cfg.Errors = append(cfg.Errors, undefinedSpecifierVariableError(idTok, value, specID.name))
			continue
		}
		reads = append(reads, VarEvent{Name: specID.name, Kind: Read, Token: idTok})
	}

	return reads, spec.end
}

// AnalyzeFuncs validates every function template once, whether or not
// anything calls it: structural checks and a classification of each write
// from the template's text alone, then dataflow checks over those writes.
// The dataflow checks wait until every template passes the first two, since a
// caller reads each call as returning values: a function rejected for holding
// a range in an output would mislead its callers' checks.
func (cfg *CFG) AnalyzeFuncs() {
	cfg.CodeCompiler.rangeBindings = make(map[funcKey]map[string]struct{})
	errorsBefore := len(cfg.Errors)

	var templates []classifiedTemplate
	for _, stmt := range cfg.CodeCompiler.Code.Statements {
		fn, ok := stmt.(*ast.FuncStatement)
		if !ok {
			continue
		}

		templates = append(templates, cfg.validateFuncTemplate(fn))
	}
	if len(cfg.Errors) > errorsBefore {
		return
	}

	for _, template := range templates {
		cfg.checkTemplateFlow(template.fn, template.statementReads, template.writes)
	}
}

// classifiedTemplate keeps what the dataflow checks need from a validated
// and classified template.
type classifiedTemplate struct {
	fn             *ast.FuncStatement
	statementReads [][]VarEvent
	writes         [][]textWrite
}

func (cfg *CFG) validateFuncTemplate(fn *ast.FuncStatement) classifiedTemplate {
	PushScope(&cfg.Scopes, FuncScope)
	defer PopScope(&cfg.Scopes)

	// Outputs are declared up front so that a formatting marker naming one
	// resolves as a read, instead of passing as literal text.
	for _, param := range fn.Parameters {
		cfg.declareName(param)
	}
	for _, output := range fn.Outputs {
		cfg.declareName(output)
	}

	body := cfg.validateTemplateBody(fn.Body.Statements, identSet(fn.Parameters), identSet(fn.Outputs))
	readInputs, assignedOutputs := body.readInputs, body.assignedOutputs
	cfg.CodeCompiler.outputReads[templateKey(fn)] = body.readOutputs

	for _, input := range fn.Parameters {
		if _, wasRead := readInputs[input.Value]; wasRead {
			continue
		}

		cfg.addError(input.Tok(), fmt.Sprintf("input parameter %q is never read", input.Value))
	}
	for _, output := range fn.Outputs {
		if _, wasAssigned := assignedOutputs[output.Value]; wasAssigned {
			continue
		}

		cfg.addError(output.Tok(), fmt.Sprintf("output parameter %q is never assigned", output.Value))
	}

	writes := cfg.classifyTemplateWrites(fn)
	return classifiedTemplate{fn: fn, statementReads: body.statementReads, writes: writes}
}

// classifyTemplateWrites classifies every assignment's writes from the
// template's text, indexed by statement, and records its range bindings for
// settlement to check. It reports the text it cannot classify: a call that
// names no template, an operator whose sides do not line up, and an
// assignment whose values do not fill its targets. The solver rejects each of
// these in any specialization; the flow checks, which would misread them, wait
// until no template has one. It also reports an output that holds a range,
// which a function cannot return.
func (cfg *CFG) classifyTemplateWrites(fn *ast.FuncStatement) [][]textWrite {
	flow := newRangeFlow(cfg.CodeCompiler, fn)
	outputs := identSet(fn.Outputs)
	writes := make([][]textWrite, len(fn.Body.Statements))
	for i, stmt := range fn.Body.Statements {
		cfg.rejectUnclassifiableExprs(statementExpressions(stmt))
		let, ok := stmt.(*ast.LetStatement)
		if !ok {
			continue
		}

		letWrites, ok := flow.letWrites(let)
		if !ok {
			cfg.addError(let.Token, assignmentMismatch(len(let.Name), cfg.CodeCompiler.valueCount(let.Value)))
			continue
		}
		cfg.rejectRangeOutputs(outputs, let, letWrites)
		flow.record(let, letWrites)
		writes[i] = letWrites
	}

	cfg.CodeCompiler.rangeBindings[templateKey(fn)] = flow.bindings
	return writes
}

func (cfg *CFG) rejectUnclassifiableExprs(exprs []ast.Expression) {
	for _, expr := range exprs {
		switch e := expr.(type) {
		case *ast.CallExpression:
			if _, found := cfg.CodeCompiler.callTemplate(e); !found {
				cfg.addError(e.Token, undefinedFunction(e.Function.Value))
			}
		case *ast.InfixExpression:
			cfg.rejectMisalignedOperands(e)
		}
		cfg.rejectUnclassifiableExprs(ast.ExprChildren(expr))
	}
}

// rejectMisalignedOperands reports an operator whose sides yield counts the
// solver rejects.
func (cfg *CFG) rejectMisalignedOperands(infix *ast.InfixExpression) {
	cc := cfg.CodeCompiler
	left, right := cc.valueSlots(infix.Left), cc.valueSlots(infix.Right)
	switch {
	case !operandsLineUp(infix, left, right) && infix.IsLogicalAnd():
		cfg.addError(infix.Token, logicalAndArityMismatch(left, right))
	case !operandsLineUp(infix, left, right):
		cfg.addError(infix.Token, operandMismatch(infix.Token.Literal, left, right))
	}
}

// operandsLineUp reports whether an operator's sides yield counts the solver
// accepts: equal counts, or for a && also a condition that folds onto one
// value or broadcasts to several.
func operandsLineUp(infix *ast.InfixExpression, left, right int) bool {
	if infix.IsLogicalAnd() {
		return left == right || left == 1 || right == 1
	}
	return left == right
}

// rejectRangeOutputs reports an output assigned a range. A function returns a
// range's bounds instead, and its caller builds the range.
func (cfg *CFG) rejectRangeOutputs(outputs map[string]struct{}, let *ast.LetStatement, writes []textWrite) {
	for i, target := range let.Name {
		if _, isOutput := outputs[target.Value]; !isOutput || !writes[i].holdsRange {
			continue
		}

		cfg.addError(target.Tok(), fmt.Sprintf("output %q cannot hold a range; return its bounds and build the range where it is used", target.Value))
	}
}

func undefinedFunction(name string) string {
	return fmt.Sprintf("undefined function: %s", name)
}

func assignmentMismatch(targets, values int) string {
	return fmt.Sprintf("assignment mismatch: %s but %s", countOf(targets, "target"), countOf(values, "value"))
}

func operandMismatch(operator string, left, right int) string {
	return fmt.Sprintf("operand mismatch: %q has %s on its left but %s on its right", operator, countOf(left, "value"), countOf(right, "value"))
}

func logicalAndArityMismatch(left, right int) string {
	return fmt.Sprintf("logical AND condition arity must match the value's, fold to one, or broadcast from one — got %d and %d", left, right)
}

// countOf writes n with its noun, in the plural unless n is one.
func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// checkTemplateFlow runs the dataflow checks once per template over the writes
// classified from its text: an output is read only after a definite
// assignment and is definitely assigned by the end of the body, and no write
// is dead or overwrites a definite write that nothing read. It runs only on a
// structurally valid body whose text classifies, so every output read follows
// some write and every assignment has its writes.
func (cfg *CFG) checkTemplateFlow(fn *ast.FuncStatement, statementReads [][]VarEvent, writes [][]textWrite) {
	cfg.PushBlock()
	defer cfg.PopBlock()

	outputs := identSet(fn.Outputs)
	assigned := make(map[string]struct{}, len(outputs))
	lastWrites := make(map[string]VarEvent)

	for i, stmt := range fn.Body.Statements {
		cfg.rejectUnassignedOutputReads(statementReads[i], outputs, assigned)
		events := append([]VarEvent(nil), statementReads[i]...)
		if let, ok := stmt.(*ast.LetStatement); ok {
			events = append(events, textWriteEvents(let, writes[i])...)
		}
		cfg.processDataflowEvents(stmt, events, lastWrites)

		for _, event := range events {
			if _, isOutput := outputs[event.Name]; isOutput && event.Kind == Write {
				assigned[event.Name] = struct{}{}
			}
		}
	}

	for _, output := range fn.Outputs {
		if _, ok := assigned[output.Value]; ok {
			continue
		}

		cfg.addError(output.Tok(), fmt.Sprintf("output %q may be left unassigned; assign it unconditionally first, or pass the previous value as an input and initialize from it (%s = prev), with each caller passing its destination as that input", output.Value, output.Value))
	}

	cfg.backwardPass(maps.Clone(outputs))
}

func (cfg *CFG) rejectUnassignedOutputReads(reads []VarEvent, outputs, assigned map[string]struct{}) {
	for _, read := range reads {
		if _, isOutput := outputs[read.Name]; !isOutput {
			continue
		}
		if _, ok := assigned[read.Name]; !ok {
			cfg.addError(read.Token, fmt.Sprintf("output %q is read where it may still be unassigned; assign it unconditionally first, or pass the previous value as an input and initialize from it", read.Name))
		}
	}
}

// textWriteEvents turns an assignment's text classification into write events
// at its named targets.
func textWriteEvents(let *ast.LetStatement, writes []textWrite) []VarEvent {
	var events []VarEvent
	for i, target := range let.Name {
		if isDiscard(target) {
			continue
		}

		kind := ConditionalWrite
		if writes[i].definite {
			kind = Write
		}
		events = append(events, VarEvent{Name: target.Value, Kind: kind, Token: target.Tok()})
	}
	return events
}

// textWrite classifies one assignment target from the text: definite unless
// the statement's gate, a value that can fail, or a range that may be empty
// can skip the write; holdsRange when the target receives a Range descriptor.
type textWrite struct {
	definite   bool
	holdsRange bool
}

// rangeFlow reads from a template's text which bindings hold a Range
// descriptor and which values a range drives, as the solver types them. Only
// a range literal assigned on its own constructs a descriptor: a parameter
// never holds one, since a range argument runs the body once per element; no
// call yields one, since a function cannot return a range; and a range name
// iterates wherever it is used, so a binding assigned from one holds an
// element. A binding holds a range as its latest assignment left it; a
// reassignment between a range and a non-range value is the solver's to
// reject, like any other change of type.
type rangeFlow struct {
	cc       *CodeCompiler
	bindings map[string]struct{}
	defined  map[string]struct{}
}

func newRangeFlow(cc *CodeCompiler, fn *ast.FuncStatement) *rangeFlow {
	defined := identSet(fn.Parameters)
	for _, output := range fn.Outputs {
		defined[output.Value] = struct{}{}
	}
	return &rangeFlow{cc: cc, bindings: make(map[string]struct{}), defined: defined}
}

// letWrites classifies every target of an assignment, discards included, so
// the result lines up with its names. Values take targets by their output
// counts; it reports false when those do not fill the targets.
func (rf *rangeFlow) letWrites(let *ast.LetStatement) ([]textWrite, bool) {
	if rf.cc.valueCount(let.Value) != len(let.Name) {
		return nil, false
	}

	gated := len(let.Condition) > 0
	writes := make([]textWrite, 0, len(let.Name))
	for _, value := range let.Value {
		definite := !gated && !rf.maySkip(value)
		for _, holdsRange := range rf.slotRanges(value) {
			writes = append(writes, textWrite{definite: definite, holdsRange: holdsRange})
		}
	}
	return writes, true
}

// maySkip reports whether a value can leave its targets unwritten: it can
// fail (a value-position comparison, &&, a checked access, a || whose last
// alternative can fail, or a call with such an argument), or a range that may
// be empty drives it.
func (rf *rangeFlow) maySkip(value ast.Expression) bool {
	return treeCanFail(value, textNodeFails) || rf.drivesValue(value)
}

func textNodeFails(expr ast.Expression) bool {
	switch e := expr.(type) {
	case *ast.InfixExpression:
		return e.Token.IsComparison() || e.IsLogicalAnd()
	case *ast.ArrayRangeExpression:
		return true
	}
	return false
}

// slotRanges reports, per target a value fills, whether it receives a Range
// descriptor: only a bare range literal constructs one.
func (rf *rangeFlow) slotRanges(value ast.Expression) []bool {
	ranges := make([]bool, rf.cc.valueSlots(value))
	if _, isLiteral := value.(*ast.RangeLiteral); isLiteral {
		ranges[0] = true
	}
	return ranges
}

// record publishes an assignment's targets once its reads are classified:
// they become defined, and each holds a range exactly when this assignment
// gives it a descriptor.
func (rf *rangeFlow) record(let *ast.LetStatement, writes []textWrite) {
	for i, target := range let.Name {
		if isDiscard(target) {
			continue
		}

		rf.defined[target.Value] = struct{}{}
		if writes[i].holdsRange {
			rf.bindings[target.Value] = struct{}{}
			continue
		}
		delete(rf.bindings, target.Value)
	}
}

// drivesValue reports whether a range that may be empty drives an
// assignment's value: any range the value iterates, apart from a range
// literal it assigns whole.
func (rf *rangeFlow) drivesValue(value ast.Expression) bool {
	switch v := value.(type) {
	case *ast.RangeLiteral:
		return rf.drivesAny(ast.ExprChildren(v))
	case *ast.CallExpression:
		return rf.drivesAny(v.Arguments)
	}
	return rf.drives(value)
}

// drives reports whether expr iterates a range that may be empty. Every range
// in it is iterated, except inside an array literal, which settles its cells
// itself. Only a range literal with constant, nonempty bounds always runs.
func (rf *rangeFlow) drives(expr ast.Expression) bool {
	switch e := expr.(type) {
	case *ast.ArrayLiteral:
		return false
	case *ast.RangeLiteral:
		return !rangeLiteralGuaranteedNonEmpty(e)
	case *ast.Identifier:
		return rf.isRangeBinding(e.Value)
	case *ast.StringLiteral:
		// A marker iterates the range it names, whether it formats the range
		// or uses it as a width or precision.
		return slices.ContainsFunc(formatMarkerNames(e.Token.Literal, rf.isDefined), rf.isRangeBinding)
	}
	return rf.drivesAny(ast.ExprChildren(expr))
}

func (rf *rangeFlow) drivesAny(exprs []ast.Expression) bool {
	return slices.ContainsFunc(exprs, rf.drives)
}

func (rf *rangeFlow) isRangeBinding(name string) bool {
	_, ok := rf.bindings[name]
	return ok
}

func (rf *rangeFlow) isDefined(name string) bool {
	_, ok := rf.defined[name]
	return ok || rf.cc.isGlobalBinding(name)
}

func templateKey(fn *ast.FuncStatement) funcKey {
	return funcKey{name: fn.Token.Literal, arity: len(fn.Parameters)}
}

// valueSlots counts the targets a value fills, read from the text the way the
// solver counts a tuple: a call fills its template's outputs, and an operator
// its right operand's. An operator whose sides do not line up fills one, as
// the solver counts it after reporting the mismatch.
func (cc *CodeCompiler) valueSlots(value ast.Expression) int {
	switch v := value.(type) {
	case *ast.CallExpression:
		if template, ok := cc.callTemplate(v); ok {
			return len(template.Outputs)
		}
	case *ast.InfixExpression:
		left, right := cc.valueSlots(v.Left), cc.valueSlots(v.Right)
		if operandsLineUp(v, left, right) {
			return right
		}
	case *ast.PrefixExpression:
		return cc.valueSlots(v.Right)
	}
	return 1
}

// valueCount counts the values a list of expressions yields, as valueSlots
// counts each.
func (cc *CodeCompiler) valueCount(values []ast.Expression) int {
	count := 0
	for _, value := range values {
		count += cc.valueSlots(value)
	}
	return count
}

// callTemplate finds a call's template by its name and arity, where an
// argument counts once for each value it yields.
func (cc *CodeCompiler) callTemplate(call *ast.CallExpression) (*ast.FuncStatement, bool) {
	return cc.lookupFuncTemplate(call.Function.Value, cc.valueCount(call.Arguments))
}

// checkSettledRanges panics when a settled specialization types a binding as
// a Range where its template's text summary does not, or the reverse: the
// template checks classified the body's writes from that summary.
func (cc *CodeCompiler) checkSettledRanges(template *ast.FuncStatement, info *FuncInfo) {
	bindings := cc.rangeBindings[templateKey(template)]
	for name, typ := range info.Vars {
		_, textRange := bindings[name]
		if textRange != (typ.Kind() == RangeKind) {
			panic(fmt.Sprintf("internal: %s solves %q as %s, which disagrees with its template's text range summary", info.Sig.Name, name, typ))
		}
	}
}

// templateBody is the structural summary of one template body.
type templateBody struct {
	statementReads  [][]VarEvent
	readInputs      map[string]struct{}
	readOutputs     map[string]struct{}
	assignedOutputs map[string]struct{}
}

func identSet(idents []*ast.Identifier) map[string]struct{} {
	set := make(map[string]struct{}, len(idents))
	for _, ident := range idents {
		set[ident.Value] = struct{}{}
	}
	return set
}

// validateTemplateBody runs structural validation over one template body. A
// script is a zero-input, zero-output template: it passes nil name sets and
// consumes only the reads.
func (cfg *CFG) validateTemplateBody(statements []ast.Statement, parameterNames, outputNames map[string]struct{}) templateBody {
	body := templateBody{
		statementReads:  make([][]VarEvent, 0, len(statements)),
		readInputs:      make(map[string]struct{}, len(parameterNames)),
		readOutputs:     make(map[string]struct{}, len(outputNames)),
		assignedOutputs: make(map[string]struct{}, len(outputNames)),
	}
	for _, stmt := range statements {
		reads := cfg.collectStatementReads(stmt)
		targets := cfg.validateStatementStructure(stmt, reads, parameterNames, outputNames, body.assignedOutputs)
		if let, ok := stmt.(*ast.LetStatement); ok {
			cfg.declareTargets(let.Name)
		}

		body.statementReads = append(body.statementReads, reads)
		for _, event := range reads {
			if _, isParameter := parameterNames[event.Name]; isParameter {
				body.readInputs[event.Name] = struct{}{}
			}
			if _, isOutput := outputNames[event.Name]; isOutput {
				body.readOutputs[event.Name] = struct{}{}
			}
		}
		for _, target := range targets {
			if _, isOutput := outputNames[target.Value]; isOutput {
				body.assignedOutputs[target.Value] = struct{}{}
			}
		}
	}

	return body
}

// AnalyzeScript treats the script as a zero-input, zero-output template before
// running effect-sensitive dataflow over its fully typed body.
func (cfg *CFG) AnalyzeScript(statements []ast.Statement, effects map[*ast.LetStatement]StatementEffect) {
	if len(statements) == 0 {
		return
	}

	statementReads := cfg.validateScriptTemplate(statements)

	cfg.PushBlock()
	defer cfg.PopBlock()
	PushScope(&cfg.Scopes, BlockScope)
	defer PopScope(&cfg.Scopes)

	cfg.typedScriptForwardPass(statements, effects, statementReads)
	cfg.backwardPass(make(map[string]struct{}))
}

func (cfg *CFG) validateScriptTemplate(statements []ast.Statement) [][]VarEvent {
	PushScope(&cfg.Scopes, BlockScope)
	defer PopScope(&cfg.Scopes)

	return cfg.validateTemplateBody(statements, nil, nil).statementReads
}

func (cfg *CFG) typedScriptForwardPass(statements []ast.Statement, effects map[*ast.LetStatement]StatementEffect, statementReads [][]VarEvent) {
	if len(statementReads) != len(statements) {
		panic("internal: script CFG read count does not match statement count")
	}

	lastWrites := make(map[string]VarEvent)
	for i, stmt := range statements {
		cfg.processTypedStatement(stmt, statementReads[i], effects, lastWrites)
	}
}

func (cfg *CFG) processTypedStatement(stmt ast.Statement, reads []VarEvent, effects map[*ast.LetStatement]StatementEffect, lastWrites map[string]VarEvent) {
	events := cfg.typedStatementEvents(stmt, reads, effects)
	cfg.processDataflowEvents(stmt, events, lastWrites)

	if let, ok := stmt.(*ast.LetStatement); ok {
		cfg.declareTargets(let.Name)
	}
}

// validateStatementStructure reports template-stable read and write errors and
// returns named targets for caller-specific bookkeeping. The caller publishes
// them only after all statement reads have been checked.
func (cfg *CFG) validateStatementStructure(stmt ast.Statement, reads []VarEvent, parameters, outputs, assigned map[string]struct{}) []*ast.Identifier {
	for _, event := range reads {
		cfg.validateStructuralRead(event, outputs, assigned)
	}

	let, ok := stmt.(*ast.LetStatement)
	if !ok {
		return nil
	}

	targets := make([]*ast.Identifier, 0, len(let.Name))
	for _, target := range let.Name {
		if isDiscard(target) {
			continue
		}

		cfg.validateStructuralWrite(target, parameters)
		targets = append(targets, target)
	}

	return targets
}

func (cfg *CFG) typedStatementEvents(stmt ast.Statement, reads []VarEvent, effects map[*ast.LetStatement]StatementEffect) []VarEvent {
	events := append([]VarEvent(nil), reads...)
	let, ok := stmt.(*ast.LetStatement)
	if !ok {
		return events
	}

	effect, exists := effects[let]
	if !exists {
		panic(fmt.Sprintf("internal: missing CFG effects for statement %q", let))
	}

	for _, targetIndex := range effect.ReadsSeed {
		target := let.Name[targetIndex]
		if !cfg.isDefined(target.Value) {
			panic(fmt.Sprintf("internal: CFG seed read targets undefined binding %q in statement %q", target.Value, let))
		}
		events = append(events, VarEvent{Name: target.Value, Kind: Read, Token: target.Tok()})
	}
	for _, write := range effect.Writes {
		target := let.Name[write.TargetIndex]
		var kind EventType
		switch write.Effect {
		case MustWrite:
			kind = Write
		case MayWrite:
			kind = ConditionalWrite
		default:
			panic(fmt.Sprintf("internal: invalid CFG write effect %s for statement %q", write.Effect, let))
		}
		events = append(events, VarEvent{Name: target.Value, Kind: kind, Token: target.Tok()})
	}

	return events
}

func (cfg *CFG) processDataflowEvents(stmt ast.Statement, events []VarEvent, lastWrites map[string]VarEvent) {
	for _, event := range events {
		switch event.Kind {
		case Read:
			delete(lastWrites, event.Name)
		case Write, ConditionalWrite:
			cfg.transferWrite(lastWrites, event)
		default:
			panic(fmt.Sprintf("unhandled event type: %v", event.Kind))
		}
	}

	block := cfg.Blocks[len(cfg.Blocks)-1]
	block.Stmts = append(block.Stmts, &StmtNode{Stmt: stmt, Events: events})
}

func (cfg *CFG) transferWrite(lastWrites map[string]VarEvent, event VarEvent) {
	if previous, exists := lastWrites[event.Name]; exists && previous.Kind == Write && event.Kind == Write {
		previousLocation := fmt.Sprintf("line %d:%d", previous.Token.Line, previous.Token.Column)
		cfg.addError(event.Token, fmt.Sprintf("unconditional assignment to %q overwrites a previous value that was never used. It was previously written at %s", event.Name, previousLocation))
	}

	lastWrites[event.Name] = event
}

// backwardPass identifies unused values and dead stores.
func (cfg *CFG) backwardPass(live map[string]struct{}) {
	block := cfg.Blocks[len(cfg.Blocks)-1]
	for i := len(block.Stmts) - 1; i >= 0; i-- {
		statement := block.Stmts[i]
		for j := len(statement.Events) - 1; j >= 0; j-- {
			event := statement.Events[j]

			switch event.Kind {
			case Write:
				if _, isLive := live[event.Name]; !isLive {
					cfg.addError(event.Token, fmt.Sprintf("value assigned to %q is never used", event.Name))
				}
				delete(live, event.Name)
			case ConditionalWrite:
				if _, isLive := live[event.Name]; !isLive {
					cfg.addError(event.Token, fmt.Sprintf("value assigned to %q in conditional statement is never used", event.Name))
				}
			case Read:
				live[event.Name] = struct{}{}
			default:
				panic(fmt.Sprintf("unhandled event type: %v", event.Kind))
			}
		}
	}
}

// An output is readable once an earlier statement has assigned it; a
// statement's reads precede its own writes. The template's dataflow check
// narrows this to writes that definitely assign.
func (cfg *CFG) validateStructuralRead(event VarEvent, outputs, assigned map[string]struct{}) {
	if _, isOutput := outputs[event.Name]; isOutput {
		if _, isAssigned := assigned[event.Name]; !isAssigned {
			cfg.addError(event.Token, fmt.Sprintf("output %q is read before it is assigned", event.Name))
		}
		return
	}
	if !cfg.isDefined(event.Name) {
		cfg.addError(event.Token, fmt.Sprintf("variable %q has not been defined", event.Name))
	}
}

func (cfg *CFG) validateStructuralWrite(target *ast.Identifier, parameters map[string]struct{}) {
	if _, isParameter := parameters[target.Value]; isParameter {
		cfg.addError(target.Tok(), fmt.Sprintf("cannot write to input parameter %q", target.Value))
	}
	if cfg.CodeCompiler.isGlobalBinding(target.Value) {
		cfg.addError(target.Tok(), fmt.Sprintf("cannot write to constant %q", target.Value))
	}
}

func (cfg *CFG) declareTargets(targets []*ast.Identifier) {
	for _, target := range targets {
		if !isDiscard(target) {
			cfg.declareName(target)
		}
	}
}

// declareName makes a name resolvable in the current scope. It records no
// event: reads and writes reach the dataflow passes only through VarEvents.
func (cfg *CFG) declareName(target *ast.Identifier) {
	Put(cfg.Scopes, target.Value, struct{}{})
}

func (cfg *CFG) addError(tok token.Token, msg string) {
	cfg.Errors = append(cfg.Errors, &token.CompileError{Token: tok, Msg: msg})
}

func (cfg *CFG) isDefined(name string) bool {
	if _, exists := Get(cfg.Scopes, name); exists {
		return true
	}
	return cfg.CodeCompiler.isGlobalBinding(name)
}
