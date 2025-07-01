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
	Blocks []*BasicBlock
	Errors []*token.CompileError // Errors encountered during Analyze
}

// newBlock creates and returns a new, empty basic block
func newBlock() *BasicBlock {
	blk := &BasicBlock{
		Stmts: []*StmtNode{},
	}
	return blk
}

// BuildCFG constructs a true Control Flow Graph from a program's statements.
// This version is designed for simplicity and minimal changes.
func BuildCFG(statements []ast.Statement) *CFG {
	cfg := &CFG{}
	if len(statements) == 0 {
		return cfg
	}

	block := newBlock()
	for _, stmt := range statements {
		addStatement(block, stmt)
	}

	cfg.Blocks = append(cfg.Blocks, block)
	return cfg
}

// addStatementToBlock adds a simple statement and its events to a block.
func addStatement(b *BasicBlock, stmt ast.Statement) {
	events := extractEvents(stmt)
	sn := &StmtNode{Stmt: stmt, Events: events}
	b.Stmts = append(b.Stmts, sn)
}

// collectReads walks an expression tree and returns a slice of all
// the identifier names it finds, put in VarEvent. This is a read-only analysis.
func collectReads(expr ast.Expression) []VarEvent {
	switch e := expr.(type) {
	// Base cases that do NOT contain identifiers.
	// We do nothing and let the function return the initial nil slice.
	case *ast.IntegerLiteral, *ast.FloatLiteral, *ast.StringLiteral:
		return nil

	// Base case that IS an identifier.
	case *ast.Identifier:
		// Return a new slice
		return []VarEvent{{Name: e.Value, Kind: Read, Token: e.Tok()}}

	// Recursive cases: These nodes contain other expressions.
	case *ast.PrefixExpression:
		// The result is whatever we find in the right-hand side.
		return collectReads(e.Right)

	case *ast.InfixExpression:
		leftEvents := collectReads(e.Left)
		rightEvents := collectReads(e.Right)
		// Efficiently append the non-nil slices.
		return append(leftEvents, rightEvents...)

	case *ast.CallExpression:
		var evs []VarEvent // Declares a nil slice
		for _, arg := range e.Arguments {
			evs = append(evs, collectReads(arg)...)
		}
		return evs

	default:
		panic(fmt.Sprintf("unhandled expression type: %T", e))
	}
}

// extractEvents pulls VarEvent from a single AST statement.
// For a LetStatement: first collects all reads on the RHS, then writes on the LHS.
// For a PrintStatement: collects all reads.
// extractEvents is now the single source of truth for a statement's events.
func extractEvents(stmt ast.Statement) []VarEvent {
	var evs []VarEvent
	switch s := stmt.(type) {
	case *ast.LetStatement:
		// A LetStatement always follows the same order:
		// 1. Read all variables used in the Condition(s).
		for _, expr := range s.Condition {
			evs = append(evs, collectReads(expr)...)
		}
		// 2. Read all variables used in the Value(s).
		for _, expr := range s.Value {
			evs = append(evs, collectReads(expr)...)
		}
		// 3. Write to the destination variable(s).
		// Determine the type of write
		writeKind := Write
		if len(s.Condition) > 0 {
			writeKind = ConditionalWrite
		}
		for _, lhs := range s.Name {
			evs = append(evs, VarEvent{Name: lhs.Value, Kind: writeKind, Token: lhs.Tok()})
		}

	case *ast.PrintStatement:
		for _, expr := range s.Expression {
			evs = append(evs, collectReads(expr)...)
		}
	}
	return evs
}

// Analyze performs all data-flow checks on the CFG.
// This is the functional approach you suggested.
func (cfg *CFG) Analyze() {
	if len(cfg.Blocks) == 0 {
		return // Nothing to analyze
	}
	block := cfg.Blocks[0]
	cfg.forwardPass(block)  // Forward pass for use-before-definition and write-after-write
	cfg.backwardPass(block) // Backward pass for liveness and dead store
}

// forwardPass checks for use-before-definition and simple write-after-write errors.
// This pass iterates forward through the events.
func (cfg *CFG) forwardPass(block *BasicBlock) {
	defined := make(map[string]struct{})
	lastWrites := make(map[string]VarEvent)

	for _, sn := range block.Stmts {
		for _, e := range sn.Events {
			if e.Kind == Read {
				if _, ok := defined[e.Name]; !ok {
					cfg.addError(e.Token, fmt.Sprintf("variable %q has not been defined", e.Name))
				}
				// A read "uses" the value, so clear the last write type.
				delete(lastWrites, e.Name)
				continue
			}

			// Write or ConditionalWrite
			if prevWrite, ok := lastWrites[e.Name]; ok {
				// Error only on an unconditional write overwriting an unused value.
				if prevWrite.Kind == Write && e.Kind == Write {
					// We explicitly format the location of the previous token.
					prevLocation := fmt.Sprintf("line %d:%d", prevWrite.Token.Line, prevWrite.Token.Column)
					cfg.addError(e.Token, fmt.Sprintf("unconditional assignment to %q overwrites a previous value that was never used. It was previously written at %s", e.Name, prevLocation))
				}
			}
			// Mark as defined and update the last write type.
			defined[e.Name] = struct{}{}
			lastWrites[e.Name] = e
		}
	}
}

// backwardPass checks for liveness, identifying unused variables and dead stores.
// This pass iterates backward through the events.
func (cfg *CFG) backwardPass(block *BasicBlock) {
	live := make(map[string]struct{})

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
