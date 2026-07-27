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

// BasicBlock is a straight‐line sequence of statements.
type BasicBlock struct {
	Stmts []*StmtNode
}

// CFG holds all blocks for a function (or "main").
type CFG struct {
	ScriptCompiler *ScriptCompiler // The context to look up globals, ExprCache, FuncCache (can be nil for CodeCompiler use)
	CodeCompiler   *CodeCompiler   // The context to look up globals (for backward compatibility)
	Blocks         []*BasicBlock
	Scopes         []Scope[VarEvent] // Used ONLY by the forward pass
	Errors         []*token.CompileError
	CheckedFuncs   map[ast.FuncKey]struct{} // Map of validated functions
}

// PushBlock creates and returns a new, empty basic block
func (cfg *CFG) PushBlock() {
	cfg.Blocks = append(cfg.Blocks, &BasicBlock{
		Stmts: []*StmtNode{},
	})
}

func (cfg *CFG) PopBlock() {
	if len(cfg.Blocks) == 0 {
		panic("cannot pop block: no blocks available")
	}
	cfg.Blocks = cfg.Blocks[:len(cfg.Blocks)-1]
}

func NewCFG(sc *ScriptCompiler, cc *CodeCompiler) *CFG {
	return &CFG{
		ScriptCompiler: sc,
		CodeCompiler:   cc,
		Blocks:         make([]*BasicBlock, 0),
		Scopes:         []Scope[VarEvent]{NewScope[VarEvent](FuncScope)}, // Start with a global scope
		Errors:         make([]*token.CompileError, 0),
		CheckedFuncs:   make(map[ast.FuncKey]struct{}),
	}
}

// collectReads walks an expression tree and returns a slice of all
// the identifier names it finds, put in VarEvent. This is a read-only analysis.
func (cfg *CFG) collectReads(expr ast.Expression) []VarEvent {
	// Leaf cases with special handling
	switch e := expr.(type) {
	case *ast.IntegerLiteral, *ast.FloatLiteral:
		return nil
	case *ast.StringLiteral:
		return cfg.collectStringReads(e.Token.Literal, e.Token)
	case *ast.Identifier:
		return []VarEvent{{Name: e.Value, Kind: Read, Token: e.Tok()}}
	}

	// Recurse into children for composite expressions
	children := ast.ExprChildren(expr)
	if children == nil {
		panic(fmt.Sprintf("unhandled expression type: %T", expr))
	}
	var evs []VarEvent
	for _, child := range children {
		evs = append(evs, cfg.collectReads(child)...)
	}
	return evs
}

func (cfg *CFG) collectStringReads(value string, tok token.Token) []VarEvent {
	// Collects all identifiers in the format string.
	var evs []VarEvent
	runes := []rune(value)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' {
			_, next, _ := lexer.DecodeStringEscape(runes, i)
			i = next - 1
			continue
		}
		if maybeMarker(runes, i) {
			markerEvents, end := cfg.collectMarkerReads(value, tok, runes, i)
			evs = append(evs, markerEvents...)
			i = end - 1 // The loop increment advances past the marker.
		}
	}
	return evs
}

// collectMarkerReads collects any identifiers used after marker `-` in the format string.
// it assumes start is at marker
func (cfg *CFG) collectMarkerReads(value string, tok token.Token, runes []rune, start int) (evs []VarEvent, end int) {
	mainId, end := parseIdentifier(runes, start+1)
	exists := cfg.isDefined(mainId)
	if !exists {
		// nothing to collect if the main identifier is not in the symbol table
		return nil, end
	}

	evs = []VarEvent{{Name: mainId, Kind: Read, Token: tok}}
	// Collect dynamic width/precision reads when an attached specifier follows.
	if end < len(runes) && runes[end] == '%' {
		var specifierEvents []VarEvent
		specifierEvents, end = cfg.collectSpecifierReads(value, tok, runes, end)
		evs = append(evs, specifierEvents...)
	}
	return evs, end
}

// collectSpecifierReads collects all identifiers used in the format specifier
// It assumes the runes slice is valid start is at the `%` character
func (cfg *CFG) collectSpecifierReads(value string, tok token.Token, runes []rune, start int) (evs []VarEvent, end int) {
	spec, err := parseSpecifierSyntax(tok, value, runes, start)
	if err != nil {
		cfg.Errors = append(cfg.Errors, err)
		// Still collect the identifiers to avoid unrelated dead-store diagnostics.
		for _, specID := range spec.ids {
			if cfg.isDefined(specID) {
				evs = append(evs, VarEvent{Name: specID, Kind: Read, Token: tok})
			}
		}
		return evs, spec.end
	}
	for _, specID := range spec.ids {
		if !cfg.isDefined(specID) {
			cfg.Errors = append(cfg.Errors, undefinedSpecifierVariableError(tok, value, specID))
			return evs, spec.end
		}
		evs = append(evs, VarEvent{Name: specID, Kind: Read, Token: tok})
	}
	return evs, spec.end
}

func (cfg *CFG) extractLetEvents(s *ast.LetStatement, kinds []EventType) []VarEvent {
	var evs []VarEvent
	// A LetStatement always follows the same order:
	// 1. Read all variables used in the Condition(s).
	for _, expr := range s.Condition {
		evs = append(evs, cfg.collectReads(expr)...)
	}
	// 2. Read all variables used in the Value(s).
	for _, expr := range s.Value {
		evs = append(evs, cfg.collectReads(expr)...)
	}
	// 3. Write to the destination variable(s).
	for i, lhs := range s.Name {
		// Treat '_' as a discard target: do not record writes or liveness.
		if lhs.Value == "_" {
			continue
		}

		ve := VarEvent{Name: lhs.Value, Kind: kinds[i], Token: lhs.Tok()}
		Put(cfg.Scopes, lhs.Value, ve)
		evs = append(evs, ve)
	}
	return evs
}

func (cfg *CFG) extractStmtEvents(stmt ast.Statement) []VarEvent {
	switch s := stmt.(type) {
	case *ast.LetStatement:
		return cfg.extractLetEvents(s, cfg.destWriteKinds(s))
	case *ast.PrintStatement:
		var evs []VarEvent
		for _, expr := range s.Expression.Arguments {
			evs = append(evs, cfg.collectReads(expr)...)
		}
		return evs
	default:
		return nil
	}
}

// destWriteKinds classifies each destination write of a statement. A statement
// condition suspends the whole simultaneous assignment. Every other source of
// a skipped write belongs to one value expression — an empty driver, a callee
// that keeps its output, a value that never yields — and leaves sibling
// expressions' writes untouched, so those mark only the destinations their own
// expression feeds and a dead store behind an unconditional sibling is still
// reported.
func (cfg *CFG) destWriteKinds(s *ast.LetStatement) []EventType {
	kinds := make([]EventType, len(s.Name))
	for i := range kinds {
		kinds[i] = Write
	}
	if len(s.Condition) > 0 {
		for i := range kinds {
			kinds[i] = ConditionalWrite
		}
		return kinds
	}

	c := cfg.ScriptCompiler.Compiler
	dest := 0
	for _, v := range s.Value {
		maySkip := cfg.valueMaySkip(v)
		span := c.ExprCache[key(c.FuncNameMangled, v)].ExprLen
		for j := 0; j < span; j++ {
			if maySkip {
				kinds[dest] = ConditionalWrite
			}
			dest++
		}
	}
	return kinds
}

// funcDestWriteKinds classifies writes using the syntax available in an
// untyped .pt function template. Values pair one-to-one with destinations when
// their counts match. Otherwise output arity is unknown, so any syntactically
// skippable value protects every destination. Range effects that depend on
// inferred bindings are unavailable in this pass.
func (cfg *CFG) funcDestWriteKinds(s *ast.LetStatement) []EventType {
	kinds := make([]EventType, len(s.Name))
	for i := range kinds {
		kinds[i] = Write
	}
	if len(s.Condition) > 0 {
		for i := range kinds {
			kinds[i] = ConditionalWrite
		}
		return kinds
	}

	if len(s.Value) == len(s.Name) {
		for i, v := range s.Value {
			if cfg.funcValueMaySkip(v) {
				kinds[i] = ConditionalWrite
			}
		}
		return kinds
	}

	for _, v := range s.Value {
		if !cfg.funcValueMaySkip(v) {
			continue
		}
		for i := range kinds {
			kinds[i] = ConditionalWrite
		}
		break
	}
	return kinds
}

// valueMaySkip reports whether an RHS expression can leave its destination
// unchanged in a typed script: outside an inline collector, an empty range
// driver can run no iterations; a root call can keep an unwritten output; and
// a failable value can yield nothing. The shared tree traversal finds failures
// below the root, so `y = Square(x < 5) + 5` is recognized even though the call
// feeds an operator.
func (cfg *CFG) valueMaySkip(expr ast.Expression) bool {
	return cfg.hasRangeExpr(expr) ||
		cfg.callRootMaySkip(expr) ||
		treeCanFail(expr, cfg.nodeMayNotYield)
}

// funcValueMaySkip applies the syntax-only approximation available in an
// untyped .pt function template. Calls and syntactically failable values are
// visible here; range effects that depend on inferred bindings are not.
func (cfg *CFG) funcValueMaySkip(expr ast.Expression) bool {
	return cfg.callRootMaySkip(expr) ||
		treeCanFail(expr, cfg.funcNodeMayNotYield)
}

// nodeMayNotYield uses exact solver metadata to classify one node in a typed
// script. It also counts an array read, whose out-of-bounds case preserves the
// destination. That widening belongs only to diagnostics: applying it to the
// solver's validity predicate would incorrectly legalize `arr[9] || -1`.
func (cfg *CFG) nodeMayNotYield(expr ast.Expression) bool {
	if _, ok := expr.(*ast.ArrayRangeExpression); ok {
		return true
	}
	c := cfg.ScriptCompiler.Compiler
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	return info.HasCondScalar() || info.HasCondAnd()
}

// funcNodeMayNotYield classifies one node in an untyped .pt function template.
// Without specialization metadata, comparison and logical-AND syntax is the
// conservative signal that a scalar specialization may fail to yield.
func (cfg *CFG) funcNodeMayNotYield(expr ast.Expression) bool {
	if _, ok := expr.(*ast.ArrayRangeExpression); ok {
		return true
	}
	if infix, ok := expr.(*ast.InfixExpression); ok {
		return infix.Token.IsComparison() || infix.IsLogicalAnd()
	}
	return false
}

// callRootMaySkip reports whether a value is a bare call to a user-defined
// function. Such a callee may leave an output unwritten, so the caller keeps
// its previous value and that previous write is not dead. Only root position
// qualifies: a call feeding an operator always yields a new value. Proving a
// given callee always writes would need per-specialization range types
// unavailable here, so this stays conservative.
func (cfg *CFG) callRootMaySkip(v ast.Expression) bool {
	call, ok := v.(*ast.CallExpression)
	if !ok {
		return false
	}
	_, builtin := Builtins[call.Function.Value]
	return !builtin
}

// hasRangeExpr reports whether an RHS expression uses a range in an iterated
// position, mirroring the solver's behavior. A bare range literal is a
// descriptor value and therefore does not count.
func (cfg *CFG) hasRangeExpr(e ast.Expression) bool {
	c := cfg.ScriptCompiler.Compiler

	switch t := e.(type) {
	case *ast.Identifier:
		// Descriptor-copy assignments clear their cached ranges during typing.
		// A remaining range here is a scalar use of a driver already bound by
		// the statement, so an empty driver may leave the destination unchanged.
		return len(c.ExprCache[key(c.FuncNameMangled, t)].Ranges) > 0
	case *ast.StringLiteral:
		// Formatting markers can reference named Range drivers even though the
		// dependency is not represented as an AST child.
		return len(c.ExprCache[key(c.FuncNameMangled, t)].Ranges) > 0
	case *ast.InfixExpression, *ast.PrefixExpression:
		return len(c.ExprCache[key(c.FuncNameMangled, t)].Ranges) > 0
	case *ast.ArrayRangeExpression:
		return len(c.ExprCache[key(c.FuncNameMangled, t)].Ranges) > 0
	case *ast.CallExpression:
		if len(c.ExprCache[key(c.FuncNameMangled, t)].Ranges) > 0 {
			return true
		}
		// Check if any argument contains ranges
		for _, arg := range t.Arguments {
			if cfg.hasRangeExpr(arg) {
				return true
			}
		}
		return false
	case *ast.ArrayLiteral:
		// A collector materializes an array even when its domain is empty, so
		// its write is unconditional; cells resolve failures locally. Same
		// boundary as treeCanFail.
		return false
	case *ast.StructLiteral:
		for _, cell := range t.Row {
			if cfg.hasRangeExpr(cell) {
				return true
			}
		}
		return false
	case *ast.DotExpression:
		return cfg.hasRangeExpr(t.Left)
	default:
		// Literals and other scalar roots are unconditional. A bare range
		// literal is a driver constructor rather than an iterated use.
		return false
	}
}

func (cfg *CFG) Analyze(statements []ast.Statement) {
	if len(statements) == 0 {
		return
	}

	cfg.PushBlock()
	defer cfg.PopBlock()

	PushScope(&cfg.Scopes, BlockScope) // Start with a global scope
	// cannot pop global scope

	cfg.forwardPass(statements)                 // Forward pass for use-before-definition and write-after-write
	cfg.backwardPass(make(map[string]struct{})) // Backward pass for liveness and dead store
}

func (cfg *CFG) AnalyzeFuncs() {
	for fk, fn := range cfg.CodeCompiler.Code.Func.Map {
		if _, ok := cfg.CheckedFuncs[fk]; ok {
			continue
		}

		cfg.validateFunc(fn)
		cfg.CheckedFuncs[fk] = struct{}{}
	}
}

func (cfg *CFG) checkInputParam(inParam *ast.Identifier) {
	// scan once for both reads and illegal writes
	wasRead := false
	block := cfg.Blocks[len(cfg.Blocks)-1] // Get the last block
	for _, sn := range block.Stmts {
		for _, ev := range sn.Events {
			if ev.Name != inParam.Value {
				continue
			}
			switch ev.Kind {
			case Read:
				wasRead = true
				// keep scanning to catch a write if it exists
			case Write, ConditionalWrite:
				cfg.addError(ev.Token,
					fmt.Sprintf("cannot write to input parameter %q", inParam.Value))
				// still want to record whether it was ever read, so don’t break out completely
			}
		}
	}

	if !wasRead {
		cfg.addError(inParam.Tok(),
			fmt.Sprintf("input parameter %q is never read", inParam.Value))
	}
}

// Combined “write‐to‐input” and “unused‐input” check
func (cfg *CFG) checkInputParams(params []*ast.Identifier) {
	for _, inParam := range params {
		cfg.checkInputParam(inParam)
	}
}

func (cfg *CFG) checkOutputParam(outParam *ast.Identifier) {
	// scan once for both writes and reads
	sawWrite := false
	block := cfg.Blocks[len(cfg.Blocks)-1] // Get the last block
	for _, sn := range block.Stmts {
		for _, ev := range sn.Events {
			if ev.Name != outParam.Value {
				continue
			}
			switch ev.Kind {
			case Write, ConditionalWrite:
				sawWrite = true
				return
			}
		}
	}

	if !sawWrite {
		cfg.addError(outParam.Tok(),
			fmt.Sprintf("output parameter %q is never assigned", outParam.Value))
	}
}

func (cfg *CFG) checkOutputParams(outputs []*ast.Identifier) {
	for _, outParam := range outputs {
		cfg.checkOutputParam(outParam)
	}
}

func (cfg *CFG) validateFunc(fn *ast.FuncStatement) {
	cfg.PushBlock()
	defer cfg.PopBlock()

	PushScope(&cfg.Scopes, FuncScope)
	defer PopScope(&cfg.Scopes) // Ensure we pop the function scope after validation

	// add the input arguments to the scope
	for _, param := range fn.Parameters {
		ve := VarEvent{Name: param.Value, Kind: Write, Token: param.Tok()}
		Put(cfg.Scopes, param.Value, ve)
	}

	cfg.funcForwardPass(fn.Body.Statements)

	// Build set of output names
	outSet := make(map[string]struct{}, len(fn.Outputs))
	for _, o := range fn.Outputs {
		outSet[o.Value] = struct{}{}
	}

	// Filter the params: inputsOnly = params that are NOT outputs
	var inputsOnly []*ast.Identifier
	for _, p := range fn.Parameters {
		if _, isOutput := outSet[p.Value]; !isOutput {
			inputsOnly = append(inputsOnly, p)
		}
	}

	cfg.checkInputParams(inputsOnly)
	cfg.checkOutputParams(fn.Outputs)

	// seed the live map in backward pass with output parameters
	// as the output parameters will be used later.
	live := make(map[string]struct{})
	for _, output := range fn.Outputs {
		live[output.Value] = struct{}{}
	}
	cfg.backwardPass(live)
}

// processForwardEvents applies one statement's events and records them for the
// backward liveness pass.
func (cfg *CFG) processForwardEvents(stmt ast.Statement, evs []VarEvent, lastWrites map[string]VarEvent) {
	for _, e := range evs {
		switch e.Kind {
		case Read:
			cfg.checkRead(lastWrites, e)
		case Write, ConditionalWrite:
			cfg.checkWrite(lastWrites, e)
		default:
			panic(fmt.Sprintf("unhandled event type: %v", e.Kind))
		}
	}
	block := cfg.Blocks[len(cfg.Blocks)-1]
	block.Stmts = append(block.Stmts, &StmtNode{Stmt: stmt, Events: evs})
}

// forwardPass checks a typed script for use-before-definition and simple
// write-after-write errors.
func (cfg *CFG) forwardPass(statements []ast.Statement) {
	lastWrites := make(map[string]VarEvent)
	for _, stmt := range statements {
		cfg.processForwardEvents(stmt, cfg.extractStmtEvents(stmt), lastWrites)
	}
}

// funcForwardPass performs the same checks on an untyped .pt function
// template, using its syntax-only write classification.
func (cfg *CFG) funcForwardPass(statements []ast.Statement) {
	lastWrites := make(map[string]VarEvent)
	for _, stmt := range statements {
		var evs []VarEvent
		if s, ok := stmt.(*ast.LetStatement); ok {
			evs = cfg.extractLetEvents(s, cfg.funcDestWriteKinds(s))
		} else {
			evs = cfg.extractStmtEvents(stmt)
		}
		cfg.processForwardEvents(stmt, evs, lastWrites)
	}
}

func (cfg *CFG) checkRead(lastWrites map[string]VarEvent, e VarEvent) {
	if !cfg.isDefined(e.Name) {
		cfg.addError(e.Token, fmt.Sprintf("variable %q has not been defined", e.Name))
	}
	// A read "uses" the value, so clear the last write type.
	delete(lastWrites, e.Name)
}

func (cfg *CFG) checkWrite(lastWrites map[string]VarEvent, e VarEvent) {
	// Write or ConditionalWrite
	if prevWrite, ok := lastWrites[e.Name]; ok {
		// Error only on an unconditional write overwriting an unused value.
		if prevWrite.Kind == Write && e.Kind == Write {
			// We explicitly format the location of the previous token.
			prevLocation := fmt.Sprintf("line %d:%d", prevWrite.Token.Line, prevWrite.Token.Column)
			cfg.addError(e.Token, fmt.Sprintf("unconditional assignment to %q overwrites a previous value that was never used. It was previously written at %s", e.Name, prevLocation))
		}
	}
	// check we are not writing to a constant
	cc := cfg.CodeCompiler
	if _, ok := cc.Code.ConstNames[e.Name]; ok {
		cfg.addError(e.Token, fmt.Sprintf("cannot write to constant %q", e.Name))
	}
	// update the last write type.
	lastWrites[e.Name] = e
}

// backwardPass checks for liveness, identifying unused variables and dead stores.
// This pass iterates backward through the events.
func (cfg *CFG) backwardPass(live map[string]struct{}) {
	block := cfg.Blocks[len(cfg.Blocks)-1] // Get the last block
	for i := len(block.Stmts) - 1; i >= 0; i-- {
		sn := block.Stmts[i]
		for j := len(sn.Events) - 1; j >= 0; j-- {
			e := sn.Events[j]

			switch e.Kind {
			case Write:
				// If we are writing to a variable that is not "live", it's a dead store.
				if _, ok := live[e.Name]; !ok {
					cfg.addError(e.Token, fmt.Sprintf("value assigned to %q is never used", e.Name))
				}
				// An unconditional write ALWAYS satisfies the liveness, so we kill it.
				delete(live, e.Name)

			case ConditionalWrite:
				// A conditional write is also a dead store if the var is not live later.
				if _, ok := live[e.Name]; !ok {
					cfg.addError(e.Token, fmt.Sprintf("value assigned to %q in conditional statement is never used", e.Name))
				}
				// CRUCIAL: We DO NOT delete the liveness here. Because this write
				// might not happen, the variable must remain live for whatever
				// came before it.

			case Read:
				// A read makes the variable live *before* this point.
				live[e.Name] = struct{}{}
			}
		}
	}
}

func (cfg *CFG) addError(tok token.Token, msg string) {
	cfg.Errors = append(cfg.Errors, &token.CompileError{Token: tok, Msg: msg})
}

// isDefined checks local scopes first, then global constants.
func (cfg *CFG) isDefined(name string) bool {
	if _, ok := Get(cfg.Scopes, name); ok {
		return true
	}
	cc := cfg.CodeCompiler
	_, ok := cc.Code.ConstNames[name]
	return ok
}
