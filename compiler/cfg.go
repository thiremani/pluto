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

	return reads
}

func (cfg *CFG) collectStatementReads(stmt ast.Statement) []VarEvent {
	var expressions []ast.Expression
	switch s := stmt.(type) {
	case *ast.LetStatement:
		expressions = make([]ast.Expression, 0, len(s.Condition)+len(s.Value))
		expressions = append(expressions, s.Condition...)
		expressions = append(expressions, s.Value...)
	case *ast.PrintStatement:
		expressions = s.Expression.Arguments
	default:
		return nil
	}

	var reads []VarEvent
	for _, expr := range expressions {
		reads = append(reads, cfg.collectReads(expr)...)
	}

	return reads
}

func (cfg *CFG) collectStringReads(value string, tok token.Token) []VarEvent {
	var reads []VarEvent
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
		reads = append(reads, markerReads...)
		i = end - 1
	}

	return reads
}

// An unknown main marker itself is literal text.
func (cfg *CFG) collectMarkerReads(value string, tok token.Token, runes []rune, start int) ([]VarEvent, int) {
	mainID, end := parseIdentifier(runes, start+1)
	if !cfg.isDefined(mainID) {
		return nil, end
	}

	reads := []VarEvent{{Name: mainID, Kind: Read, Token: tok}}
	if end >= len(runes) || runes[end] != '%' {
		return reads, end
	}

	specifierReads, specifierEnd := cfg.collectSpecifierReads(value, tok, runes, end)
	reads = append(reads, specifierReads...)
	return reads, specifierEnd
}

func (cfg *CFG) collectSpecifierReads(value string, tok token.Token, runes []rune, start int) ([]VarEvent, int) {
	spec, err := parseSpecifierSyntax(tok, value, runes, start)
	var reads []VarEvent
	if err != nil {
		cfg.Errors = append(cfg.Errors, err)
		for _, specID := range spec.ids {
			if cfg.isDefined(specID) {
				reads = append(reads, VarEvent{Name: specID, Kind: Read, Token: tok})
			}
		}

		return reads, spec.end
	}

	for _, specID := range spec.ids {
		if !cfg.isDefined(specID) {
			cfg.Errors = append(cfg.Errors, undefinedSpecifierVariableError(tok, value, specID))
			return reads, spec.end
		}
		reads = append(reads, VarEvent{Name: specID, Kind: Read, Token: tok})
	}

	return reads, spec.end
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

	parameterNames := make(map[string]struct{}, len(fn.Parameters))
	for _, parameter := range fn.Parameters {
		parameterNames[parameter.Value] = struct{}{}
	}

	outputNames := make(map[string]struct{}, len(fn.Outputs))
	for _, output := range fn.Outputs {
		outputNames[output.Value] = struct{}{}
	}

	readInputs, assignedOutputs := cfg.validateTemplateBody(fn.Body.Statements, parameterNames, outputNames)

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
}

func (cfg *CFG) validateTemplateBody(statements []ast.Statement, parameterNames, outputNames map[string]struct{}) (map[string]struct{}, map[string]struct{}) {
	readInputs := make(map[string]struct{}, len(parameterNames))
	assignedOutputs := make(map[string]struct{}, len(outputNames))
	for _, stmt := range statements {
		reads := cfg.collectStatementReads(stmt)
		targets := cfg.validateStatementStructure(stmt, reads, parameterNames)
		for _, event := range reads {
			if _, isParameter := parameterNames[event.Name]; isParameter {
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

	return readInputs, assignedOutputs
}

// AnalyzeScript treats the script as a zero-input, zero-output template before
// running effect-sensitive dataflow over its fully typed body.
func (cfg *CFG) AnalyzeScript(statements []ast.Statement, effects map[*ast.LetStatement]StatementEffect) {
	if len(statements) == 0 {
		return
	}

	errorsAtEntry := len(cfg.Errors)
	cfg.validateScriptTemplate(statements)
	if len(cfg.Errors) > errorsAtEntry {
		return
	}

	cfg.PushBlock()
	defer cfg.PopBlock()
	PushScope(&cfg.Scopes, BlockScope)
	defer PopScope(&cfg.Scopes)

	cfg.typedForwardPass(statements, effects)
	cfg.backwardPass(make(map[string]struct{}))
}

func (cfg *CFG) validateScriptTemplate(statements []ast.Statement) {
	PushScope(&cfg.Scopes, BlockScope)
	defer PopScope(&cfg.Scopes)

	cfg.validateTemplateBody(statements, nil, nil)
}

// AnalyzeSpecialization runs only typed dataflow. Structural diagnostics were
// already produced once from the function template.
func (cfg *CFG) AnalyzeSpecialization(template *ast.FuncStatement, info *FuncInfo) {
	cfg.PushBlock()
	defer cfg.PopBlock()
	PushScope(&cfg.Scopes, FuncScope)
	defer PopScope(&cfg.Scopes)

	for _, param := range template.Parameters {
		cfg.publishTarget(param)
	}

	cfg.typedForwardPass(template.Body.Statements, info.StatementEffects)

	live := make(map[string]struct{}, len(template.Outputs))
	for _, output := range template.Outputs {
		live[output.Value] = struct{}{}
	}
	cfg.backwardPass(live)
}

func (cfg *CFG) typedForwardPass(statements []ast.Statement, effects map[*ast.LetStatement]StatementEffect) {
	lastWrites := make(map[string]VarEvent)
	for _, stmt := range statements {
		reads := cfg.collectStatementReads(stmt)
		let, isLet := stmt.(*ast.LetStatement)

		events := cfg.typedStatementEvents(stmt, reads, effects)
		cfg.processDataflowEvents(stmt, events, lastWrites)
		if isLet {
			cfg.publishTargets(let.Name)
		}
	}
}

// validateStatementStructure reports template-stable read and write errors and
// returns named targets for caller-specific bookkeeping. The caller publishes
// them only after all statement reads have been checked.
func (cfg *CFG) validateStatementStructure(stmt ast.Statement, reads []VarEvent, parameters map[string]struct{}) []*ast.Identifier {
	for _, event := range reads {
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

func (cfg *CFG) validateStructuralRead(event VarEvent) {
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
