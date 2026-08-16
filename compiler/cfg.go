package compiler

import (
	"fmt"

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

type readCollection struct {
	Events []VarEvent
	Errors []*token.CompileError
}

// CFG owns structural template validation and effect-sensitive dataflow.
type CFG struct {
	CodeCompiler *CodeCompiler
	Blocks       []*BasicBlock
	Scopes       []Scope[VarEvent]
	Errors       []*token.CompileError
}

func NewCFG(cc *CodeCompiler) *CFG {
	return &CFG{
		CodeCompiler: cc,
		Blocks:       make([]*BasicBlock, 0),
		Scopes:       []Scope[VarEvent]{NewScope[VarEvent](FuncScope)},
		Errors:       make([]*token.CompileError, 0),
	}
}

// PushBlock creates a new empty basic block.
func (cfg *CFG) PushBlock() {
	cfg.Blocks = append(cfg.Blocks, &BasicBlock{Stmts: []*StmtNode{}})
}

func (cfg *CFG) PopBlock() {
	if len(cfg.Blocks) == 0 {
		panic("cannot pop block: no blocks available")
	}
	cfg.Blocks = cfg.Blocks[:len(cfg.Blocks)-1]
}

// collectReads is a pure read/formatting analysis. It reports formatting
// errors to the caller and never publishes bindings or mutates CFG errors.
func (cfg *CFG) collectReads(expr ast.Expression) readCollection {
	switch e := expr.(type) {
	case *ast.IntegerLiteral, *ast.FloatLiteral:
		return readCollection{}
	case *ast.StringLiteral:
		return cfg.collectStringReads(e.Token.Literal, e.Token)
	case *ast.Identifier:
		return readCollection{
			Events: []VarEvent{{Name: e.Value, Kind: Read, Token: e.Tok()}},
		}
	}

	children := ast.ExprChildren(expr)
	if children == nil {
		panic(fmt.Sprintf("unhandled expression type: %T", expr))
	}

	var result readCollection
	for _, child := range children {
		result.append(cfg.collectReads(child))
	}

	return result
}

func (reads *readCollection) append(other readCollection) {
	reads.Events = append(reads.Events, other.Events...)
	reads.Errors = append(reads.Errors, other.Errors...)
}

func (cfg *CFG) collectStatementReads(stmt ast.Statement) readCollection {
	var expressions []ast.Expression
	switch s := stmt.(type) {
	case *ast.LetStatement:
		expressions = make([]ast.Expression, 0, len(s.Condition)+len(s.Value))
		expressions = append(expressions, s.Condition...)
		expressions = append(expressions, s.Value...)
	case *ast.PrintStatement:
		expressions = s.Expression.Arguments
	default:
		return readCollection{}
	}

	var result readCollection
	for _, expr := range expressions {
		result.append(cfg.collectReads(expr))
	}

	return result
}

func (cfg *CFG) collectStringReads(value string, tok token.Token) readCollection {
	var result readCollection
	runes := []rune(value)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' {
			_, next, _ := lexer.DecodeStringEscape(runes, i)
			i = next - 1
			continue
		}
		if !maybeMarker(runes, i) {
			continue
		}

		markerReads, end := cfg.collectMarkerReads(value, tok, runes, i)
		result.append(markerReads)
		i = end - 1
	}

	return result
}

// An unknown main marker itself is literal text.
func (cfg *CFG) collectMarkerReads(value string, tok token.Token, runes []rune, start int) (readCollection, int) {
	mainID, end := parseIdentifier(runes, start+1)
	if !cfg.isDefined(mainID) {
		return readCollection{}, end
	}

	result := readCollection{
		Events: []VarEvent{{Name: mainID, Kind: Read, Token: tok}},
	}
	if end >= len(runes) || runes[end] != '%' {
		return result, end
	}

	specifierReads, specifierEnd := cfg.collectSpecifierReads(value, tok, runes, end)
	result.append(specifierReads)
	return result, specifierEnd
}

func (cfg *CFG) collectSpecifierReads(value string, tok token.Token, runes []rune, start int) (readCollection, int) {
	spec, err := parseSpecifierSyntax(tok, value, runes, start)
	result := readCollection{}
	if err != nil {
		result.Errors = append(result.Errors, err)
		for _, specID := range spec.ids {
			if cfg.isDefined(specID) {
				result.Events = append(result.Events, VarEvent{Name: specID, Kind: Read, Token: tok})
			}
		}

		return result, spec.end
	}

	for _, specID := range spec.ids {
		if !cfg.isDefined(specID) {
			result.Errors = append(result.Errors, undefinedSpecifierVariableError(tok, value, specID))
			return result, spec.end
		}
		result.Events = append(result.Events, VarEvent{Name: specID, Kind: Read, Token: tok})
	}

	return result, spec.end
}

// AnalyzeFuncs runs syntax-stable structural validation once for every
// function template. Effect-sensitive diagnostics are deferred until a
// concrete specialization is settled.
func (cfg *CFG) AnalyzeFuncs() {
	for _, stmt := range cfg.CodeCompiler.Code.Statements {
		fn, ok := stmt.(*ast.FuncStatement)
		if !ok {
			continue
		}

		cfg.validateFuncTemplate(fn)
	}
}

func (cfg *CFG) validateFuncTemplate(fn *ast.FuncStatement) {
	PushScope(&cfg.Scopes, FuncScope)
	defer PopScope(&cfg.Scopes)

	for _, param := range fn.Parameters {
		cfg.publishTarget(param)
	}

	outputNames := make(map[string]struct{}, len(fn.Outputs))
	for _, output := range fn.Outputs {
		outputNames[output.Value] = struct{}{}
	}

	inputNames := make(map[string]struct{}, len(fn.Parameters))
	for _, input := range fn.Parameters {
		if _, isOutput := outputNames[input.Value]; !isOutput {
			inputNames[input.Value] = struct{}{}
		}
	}

	readInputs := make(map[string]struct{}, len(inputNames))
	assignedOutputs := make(map[string]struct{}, len(outputNames))
	for _, stmt := range fn.Body.Statements {
		reads := cfg.collectStatementReads(stmt)
		targets := cfg.validateStatementStructure(stmt, reads, inputNames)
		for _, event := range reads.Events {
			if _, isInput := inputNames[event.Name]; isInput {
				readInputs[event.Name] = struct{}{}
			}
		}

		for _, target := range targets {
			if _, isOutput := outputNames[target.Value]; isOutput {
				assignedOutputs[target.Value] = struct{}{}
			}
		}

		if let, ok := stmt.(*ast.LetStatement); ok {
			cfg.publishTargets(let.Name)
		}
	}

	for _, input := range fn.Parameters {
		if _, isInput := inputNames[input.Value]; !isInput {
			continue
		}
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
}

// AnalyzeScript combines structural validation with effect-sensitive dataflow
// for one fully typed script body.
func (cfg *CFG) AnalyzeScript(statements []ast.Statement, effects map[*ast.LetStatement]StatementEffect) {
	if len(statements) == 0 {
		return
	}

	cfg.PushBlock()
	defer cfg.PopBlock()
	PushScope(&cfg.Scopes, BlockScope)
	defer PopScope(&cfg.Scopes)

	cfg.typedForwardPass(statements, effects, true)
	cfg.backwardPass(make(map[string]struct{}))
}

// AnalyzeSpecialization runs only typed dataflow. Structural diagnostics were
// already produced once from the function template.
func (cfg *CFG) AnalyzeSpecialization(template *ast.FuncStatement, info *FuncInfo) {
	if info == nil {
		panic("internal: cannot analyze CFG for a nil function specialization")
	}

	cfg.PushBlock()
	defer cfg.PopBlock()
	PushScope(&cfg.Scopes, FuncScope)
	defer PopScope(&cfg.Scopes)

	for _, param := range template.Parameters {
		cfg.publishTarget(param)
	}

	cfg.typedForwardPass(template.Body.Statements, info.StatementEffects, false)

	live := make(map[string]struct{}, len(template.Outputs))
	for _, output := range template.Outputs {
		live[output.Value] = struct{}{}
	}
	cfg.backwardPass(live)
}

func (cfg *CFG) typedForwardPass(statements []ast.Statement, effects map[*ast.LetStatement]StatementEffect, validateStructure bool) {
	lastWrites := make(map[string]VarEvent)
	for _, stmt := range statements {
		reads := cfg.collectStatementReads(stmt)
		let, isLet := stmt.(*ast.LetStatement)
		if validateStructure {
			cfg.validateStatementStructure(stmt, reads, nil)
		}

		events := cfg.typedStatementEvents(stmt, reads.Events, effects)
		cfg.processDataflowEvents(stmt, events, lastWrites)
		if isLet {
			cfg.publishTargets(let.Name)
		}
	}
}

// validateStatementStructure reports template-stable read and write errors and
// returns named targets for caller-specific bookkeeping. It deliberately does
// not publish targets: typed seed reads must be checked against the pre-write
// scope before simultaneous assignment commits its destinations.
func (cfg *CFG) validateStatementStructure(stmt ast.Statement, reads readCollection, inputs map[string]struct{}) []*ast.Identifier {
	cfg.Errors = append(cfg.Errors, reads.Errors...)
	for _, event := range reads.Events {
		cfg.validateStructuralRead(event)
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

		cfg.validateStructuralWrite(target, inputs)
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
	cfg.validateStatementEffect(let, effect)

	for _, targetIndex := range effect.ReadsSeed {
		target := let.Name[targetIndex]
		if !cfg.isDefined(target.Value) {
			panic(fmt.Sprintf("internal: CFG seed read targets undefined binding %q in statement %q", target.Value, let))
		}
		events = append(events, VarEvent{Name: target.Value, Kind: Read, Token: target.Tok()})
	}
	for _, write := range effect.Writes {
		target := let.Name[write.TargetIndex]
		kind := Write
		if write.Effect == MayWrite {
			kind = ConditionalWrite
		}
		events = append(events, VarEvent{Name: target.Value, Kind: kind, Token: target.Tok()})
	}

	return events
}

func (cfg *CFG) validateStatementEffect(stmt *ast.LetStatement, effect StatementEffect) {
	writtenTargets := make(map[int]struct{}, len(effect.Writes))
	lastTarget := -1
	for _, write := range effect.Writes {
		if write.TargetIndex <= lastTarget || write.TargetIndex < 0 || write.TargetIndex >= len(stmt.Name) {
			panic(fmt.Sprintf("internal: invalid CFG write target %d for statement %q", write.TargetIndex, stmt))
		}
		if isDiscard(stmt.Name[write.TargetIndex]) {
			panic(fmt.Sprintf("internal: CFG write targets discard slot %d in statement %q", write.TargetIndex, stmt))
		}
		if write.Effect != MustWrite && write.Effect != MayWrite {
			panic(fmt.Sprintf("internal: invalid CFG write effect %s for statement %q", write.Effect, stmt))
		}

		writtenTargets[write.TargetIndex] = struct{}{}
		lastTarget = write.TargetIndex
	}

	namedTargets := 0
	for index, target := range stmt.Name {
		if isDiscard(target) {
			continue
		}
		namedTargets++
		if _, exists := writtenTargets[index]; !exists {
			panic(fmt.Sprintf("internal: CFG effects omit target %d for statement %q", index, stmt))
		}
	}
	if len(effect.Writes) != namedTargets {
		panic(fmt.Sprintf("internal: CFG effects have %d writes for %d named targets in statement %q", len(effect.Writes), namedTargets, stmt))
	}

	seededTargets := make(map[int]struct{}, len(effect.ReadsSeed))
	lastTarget = -1
	for _, targetIndex := range effect.ReadsSeed {
		if targetIndex <= lastTarget || targetIndex < 0 || targetIndex >= len(stmt.Name) {
			panic(fmt.Sprintf("internal: invalid CFG seed target %d for statement %q", targetIndex, stmt))
		}
		if isDiscard(stmt.Name[targetIndex]) {
			panic(fmt.Sprintf("internal: CFG seed targets discard slot %d in statement %q", targetIndex, stmt))
		}
		if _, exists := writtenTargets[targetIndex]; !exists {
			panic(fmt.Sprintf("internal: CFG seed target %d has no write in statement %q", targetIndex, stmt))
		}
		if _, duplicate := seededTargets[targetIndex]; duplicate {
			panic(fmt.Sprintf("internal: duplicate CFG seed target %d in statement %q", targetIndex, stmt))
		}

		seededTargets[targetIndex] = struct{}{}
		lastTarget = targetIndex
	}
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

func (cfg *CFG) validateStructuralRead(event VarEvent) {
	if !cfg.isDefined(event.Name) {
		cfg.addError(event.Token, fmt.Sprintf("variable %q has not been defined", event.Name))
	}
}

func (cfg *CFG) validateStructuralWrite(target *ast.Identifier, inputs map[string]struct{}) {
	if _, isInput := inputs[target.Value]; isInput {
		cfg.addError(target.Tok(), fmt.Sprintf("cannot write to input parameter %q", target.Value))
	}
	if cfg.CodeCompiler.isGlobalBinding(target.Value) {
		cfg.addError(target.Tok(), fmt.Sprintf("cannot write to constant %q", target.Value))
	}
}

func (cfg *CFG) publishTargets(targets []*ast.Identifier) {
	for _, target := range targets {
		if !isDiscard(target) {
			cfg.publishTarget(target)
		}
	}
}

func (cfg *CFG) publishTarget(target *ast.Identifier) {
	Put(cfg.Scopes, target.Value, VarEvent{Name: target.Value, Kind: Write, Token: target.Tok()})
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
