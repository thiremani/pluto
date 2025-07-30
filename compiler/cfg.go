package compiler

import (
	"fmt"

	"github.com/thiremani/pluto/ast"
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

// CFG holds all blocks for a function (or “main”).
type CFG struct {
	CodeCompiler *CodeCompiler // The context to look up globals
	Blocks       []*BasicBlock
	Scopes       []Scope[VarEvent] // Used ONLY by the forward pass
	Errors       []*token.CompileError
	CheckedFuncs map[ast.FuncKey]struct{} // Map of validated functions
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

func NewCFG(cc *CodeCompiler) *CFG {
	return &CFG{
		CodeCompiler: cc,
		Blocks:       make([]*BasicBlock, 0),
		Scopes:       []Scope[VarEvent]{NewScope[VarEvent](FuncScope)}, // Start with a global scope
		Errors:       make([]*token.CompileError, 0),
		CheckedFuncs: make(map[ast.FuncKey]struct{}),
	}
}

// collectReads walks an expression tree and returns a slice of all
// the identifier names it finds, put in VarEvent. This is a read-only analysis.
func (cfg *CFG) collectReads(expr ast.Expression) []VarEvent {
	switch e := expr.(type) {
	// Base cases that do NOT contain identifiers.
	// We do nothing and let the function return the initial nil slice.
	case *ast.IntegerLiteral, *ast.FloatLiteral:
		return nil

	case *ast.StringLiteral:
		// Collect any identifiers within the format string.
		return cfg.collectStringReads(e)

	case *ast.RangeLiteral:
		// a range literal “start:stop[:step]” reads start, stop, and optionally step
		evs := cfg.collectReads(e.Start)
		evs = append(cfg.collectReads(e.Stop), evs...)
		if e.Step != nil {
			evs = append(cfg.collectReads(e.Step), evs...)
		}
		return evs
	// Base case that IS an identifier.
	case *ast.Identifier:
		// Return a new slice
		return []VarEvent{{Name: e.Value, Kind: Read, Token: e.Tok()}}

	// Recursive cases: These nodes contain other expressions.
	case *ast.PrefixExpression:
		// The result is whatever we find in the right-hand side.
		return cfg.collectReads(e.Right)

	case *ast.InfixExpression:
		leftEvents := cfg.collectReads(e.Left)
		rightEvents := cfg.collectReads(e.Right)
		// Efficiently append the non-nil slices.
		return append(leftEvents, rightEvents...)

	case *ast.CallExpression:
		var evs []VarEvent // Declares a nil slice
		for _, arg := range e.Arguments {
			evs = append(evs, cfg.collectReads(arg)...)
		}
		return evs

	default:
		panic(fmt.Sprintf("unhandled expression type: %T", e))
	}
}

func (cfg *CFG) collectStringReads(sl *ast.StringLiteral) []VarEvent {
	// Collects all identifiers in the format string.
	var evs []VarEvent
	runes := []rune(sl.Value)
	for i := 0; i < len(runes); i++ {
		if maybeMarker(runes, i) {
			evs = append(evs, cfg.collectMarkerReads(sl, runes, i)...)
		}
	}
	return evs
}

// collectMarkerReads collects any identifiers used after marker `-` in the format string.
// it assumes start is at marker
func (cfg *CFG) collectMarkerReads(sl *ast.StringLiteral, runes []rune, start int) []VarEvent {
	mainId, end := parseIdentifier(runes, start+1)
	exists := cfg.isDefined(mainId)
	if !exists {
		// nothing to collect if the main identifier is not in the symbol table
		return nil
	}

	evs := []VarEvent{{Name: mainId, Kind: Read, Token: sl.Tok()}}
	// now collect any format specifier identifier reads
	if hasSpecifier(runes, end) {
		evs = append(evs, cfg.collectSpecifierReads(sl, runes, end)...)
	}
	return evs
}

// collectSpecifierReads collects all identifiers used in the format specifier
// It assumes the runes slice is valid start is at the `%` character
func (cfg *CFG) collectSpecifierReads(sl *ast.StringLiteral, runes []rune, start int) []VarEvent {
	var evs []VarEvent
	for it := start + 1; it < len(runes); it++ {
		if !specIdAhead(runes, it) {
			continue
		}

		specId, end := parseIdentifier(runes, it+2)
		if end >= len(runes) || runes[end] != ')' {
			err := &token.CompileError{
				Token: sl.Token,
				Msg:   fmt.Sprintf("Expected ) after the identifier %s. Str: %s", specId, sl.Value),
			}
			cfg.Errors = append(cfg.Errors, err)
			return nil
		}

		ok := cfg.isDefined(specId)
		if !ok {
			err := &token.CompileError{
				Token: sl.Token,
				Msg:   fmt.Sprintf("Undefined variable %s within specifier. String Literal is %s", specId, sl.Value),
			}
			cfg.Errors = append(cfg.Errors, err)
			return nil
		}

		evs = append(evs, VarEvent{Name: specId, Kind: Read, Token: sl.Tok()})
	}
	return evs
}

func (cfg *CFG) extractStmtEvents(stmt ast.Statement) []VarEvent {
	var evs []VarEvent // Holds all events for this statement
	switch s := stmt.(type) {
	case *ast.LetStatement:
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
		// Determine the type of write
		writeKind := Write
		if len(s.Condition) > 0 {
			writeKind = ConditionalWrite
		}
		for _, lhs := range s.Name {
			ve := VarEvent{Name: lhs.Value, Kind: writeKind, Token: lhs.Tok()}
			Put(cfg.Scopes, lhs.Value, ve)
			evs = append(evs, ve)
		}

	case *ast.PrintStatement:
		for _, expr := range s.Expression {
			evs = append(evs, cfg.collectReads(expr)...)
		}
	}
	return evs
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

	cfg.forwardPass(fn.Body.Statements)

	cfg.checkInputParams(fn.Parameters)
	cfg.checkOutputParams(fn.Outputs)

	// seed the live map in backward pass with output parameters
	// as the output parameters will be used later.
	live := make(map[string]struct{})
	for _, output := range fn.Outputs {
		live[output.Value] = struct{}{}
	}
	cfg.backwardPass(live)
}

// forwardPass checks for use-before-definition and simple write-after-write errors.
// This pass iterates forward through the events.
func (cfg *CFG) forwardPass(statements []ast.Statement) {
	block := cfg.Blocks[len(cfg.Blocks)-1] // Get the last block
	lastWrites := make(map[string]VarEvent)

	for _, stmt := range statements {
		evs := cfg.extractStmtEvents(stmt)
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
		sn := &StmtNode{Stmt: stmt, Events: evs}
		block.Stmts = append(block.Stmts, sn)
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
	if _, ok := cc.Code.Const.Map[e.Name]; ok {
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
	return cfg.isGlobalConst(name)
}

// isGlobalConst is a simple helper.
func (cfg *CFG) isGlobalConst(name string) bool {
	cc := cfg.CodeCompiler
	_, ok := cc.Code.Const.Map[name]
	return ok
}
