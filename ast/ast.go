package ast

import (
	"bytes"
	"fmt"
	"maps"
	"strings"

	"github.com/thiremani/pluto/token"
)

// The base Node interface
type Node interface {
	Tok() token.Token
	String() string
}

// All statement nodes implement this
type Statement interface {
	Node
	statementNode()
}

// All expression nodes implement this
type Expression interface {
	Node
	expressionNode()
}

type Program struct {
	Statements []Statement
}

type Code struct {
	Const      Const
	ConstNames map[string]token.Token
	Func       Func
	Struct     Struct
}

type FuncKey struct {
	FuncName string
	Arity    int
}

type Struct struct {
	Statements []*StructStatement
	Map        map[string]*StructStatement
}

func NewCode() *Code {
	Const := Const{
		Statements: []*ConstStatement{},
		Map:        make(map[string]*ConstStatement),
	}
	Func := Func{
		Statements: []*FuncStatement{},
		Map:        make(map[FuncKey]*FuncStatement),
	}
	Struct := Struct{
		Statements: []*StructStatement{},
		Map:        make(map[string]*StructStatement),
	}

	return &Code{
		Const:      Const,
		ConstNames: make(map[string]token.Token),
		Func:       Func,
		Struct:     Struct,
	}
}

func (c *Code) Merge(other *Code) {
	// Merge constants
	if other != nil {
		c.Const.Statements = append(c.Const.Statements, other.Const.Statements...)
		maps.Copy(c.Const.Map, other.Const.Map)
		maps.Copy(c.ConstNames, other.ConstNames)
		c.Func.Statements = append(c.Func.Statements, other.Func.Statements...)
		maps.Copy(c.Func.Map, other.Func.Map)
		c.Struct.Statements = append(c.Struct.Statements, other.Struct.Statements...)
		maps.Copy(c.Struct.Map, other.Struct.Map)
	}
}

type Const struct {
	Statements []*ConstStatement
	Map        map[string]*ConstStatement
}

type Func struct {
	Statements []*FuncStatement
	Map        map[FuncKey]*FuncStatement
}

// StructStatement represents a struct constant definition in a .pt file.
// For example:
//
//	p = Person
//	    :name age
//	    "Tejas" 35
type StructStatement struct {
	Token token.Token // The token.ASSIGN token
	Name  *Identifier
	Value *StructLiteral
}

func (ss *StructStatement) statementNode() {}

func (ss *StructStatement) Tok() token.Token {
	return ss.Token
}

func (ss *StructStatement) String() string {
	var out bytes.Buffer
	out.WriteString(ss.Name.String())
	out.WriteString(" = ")
	out.WriteString(ss.Value.String())
	return out.String()
}

func (p *Program) Tok() token.Token {
	if len(p.Statements) > 0 {
		return p.Statements[0].Tok()
	} else {
		return token.Token{
			Type:    token.EOF,
			Literal: "",
		}
	}
}

func (p *Program) String() string {
	var out bytes.Buffer

	for _, s := range p.Statements {
		out.WriteString(s.String())
	}

	return out.String()
}

func printVec(a []Expression) string {
	if len(a) == 0 {
		return ""
	}

	ret := a[0].String()
	for _, val := range a[1:] {
		n := val
		ret += ", "
		ret += n.String()
	}

	return ret
}

func toExpressionVec(idents []*Identifier) []Expression {
	ret := []Expression{}
	for _, val := range idents {
		ret = append(ret, val)
	}
	return ret
}

// Statements
// ConstStatement represents a constant assignment in a .pt file.
// For example: PI = 3.14159
type ConstStatement struct {
	Token token.Token   // The token.ASSIGN token
	Name  []*Identifier // Constant names
	Value []Expression  // The literal expression(s) representing the constant
}

func (cs *ConstStatement) statementNode() {}

func (cs *ConstStatement) Tok() token.Token {
	return cs.Token
}

func (cs *ConstStatement) String() string {
	var out bytes.Buffer

	out.WriteString(printVec(toExpressionVec(cs.Name)))
	out.WriteString(" = ")

	out.WriteString(printVec(cs.Value))

	return out.String()
}

type LetStatement struct {
	Token     token.Token // the token.ASSIGN token
	Name      []*Identifier
	Value     []Expression
	Condition []Expression
}

func (ls *LetStatement) statementNode()   {}
func (ls *LetStatement) Tok() token.Token { return ls.Token }
func (ls *LetStatement) String() string {
	var out bytes.Buffer

	out.WriteString(printVec(toExpressionVec(ls.Name)))
	out.WriteString(" = ")

	if len(ls.Condition) > 0 {
		out.WriteString(printVec(ls.Condition))
		out.WriteString(" ")
	}

	if len(ls.Value) > 0 {
		out.WriteString(printVec(ls.Value))
	}

	return out.String()
}

type PrintStatement struct {
	Token      token.Token // the first token of the expression
	Expression *CallExpression
}

func (ps *PrintStatement) statementNode()   {}
func (ps *PrintStatement) Tok() token.Token { return ps.Token }
func (ps *PrintStatement) String() string {
	return printVec(ps.Expression.Arguments)
}

type BlockStatement struct {
	Token      token.Token // the { token
	Statements []Statement
}

func (bs *BlockStatement) statementNode()   {}
func (bs *BlockStatement) Tok() token.Token { return bs.Token }
func (bs *BlockStatement) String() string {
	var out bytes.Buffer

	for _, s := range bs.Statements {
		out.WriteString(s.String())
	}

	return out.String()
}

// Expressions
type Identifier struct {
	Token token.Token // the token.IDENT token
	Value string
}

func (i *Identifier) expressionNode()  {}
func (i *Identifier) Tok() token.Token { return i.Token }
func (i *Identifier) String() string   { return i.Value }

type IntegerLiteral struct {
	Token token.Token
	Value int64
}

func (il *IntegerLiteral) expressionNode()  {}
func (il *IntegerLiteral) Tok() token.Token { return il.Token }
func (il *IntegerLiteral) String() string   { return il.Token.Literal }

type FloatLiteral struct {
	Token token.Token
	Value float64
}

func (fl *FloatLiteral) expressionNode()  {}
func (fl *FloatLiteral) Tok() token.Token { return fl.Token }
func (fl *FloatLiteral) String() string   { return fl.Token.Literal }

type StringLiteral struct {
	Token token.Token
	Value string
}

func (sl *StringLiteral) expressionNode()  {}
func (sl *StringLiteral) Tok() token.Token { return sl.Token }
func (sl *StringLiteral) String() string   { return sl.Token.Literal }

type HeapStringLiteral struct {
	Token token.Token
	Value string
}

func (hl *HeapStringLiteral) expressionNode()  {}
func (hl *HeapStringLiteral) Tok() token.Token { return hl.Token }
func (hl *HeapStringLiteral) String() string   { return hl.Token.Literal }

// RangeLiteral represents start:stop[:step]
type RangeLiteral struct {
	Token token.Token // the first ':' token
	Start Expression  // e.g. the “0” in 0:5 or x in x:y
	Stop  Expression  // the “5” in 0:5
	Step  Expression  // optional; nil if omitted
}

func (rl *RangeLiteral) expressionNode()  {}
func (rl *RangeLiteral) Tok() token.Token { return rl.Token }
func (rl *RangeLiteral) String() string {
	if rl.Step != nil {
		return fmt.Sprintf("%s:%s:%s",
			rl.Start.String(), rl.Stop.String(), rl.Step.String())
	}
	return fmt.Sprintf("%s:%s", rl.Start.String(), rl.Stop.String())
}

type ArrayLiteral struct {
	Token   token.Token      // the '[' token
	Headers []string         // column headers (empty for matrices)
	Rows    [][]Expression   // row data
	Indices map[string][]int // named row indices like "books": [2,3]
}

func (al *ArrayLiteral) expressionNode()  {}
func (al *ArrayLiteral) Tok() token.Token { return al.Token }
func (al *ArrayLiteral) String() string {
	var out bytes.Buffer
	out.WriteString("[")

	// Print headers if present
	if len(al.Headers) > 0 {
		out.WriteString("\n  :  ") // 2 spaces after :
		for j, header := range al.Headers {
			if j > 0 {
				out.WriteString(" ")
			}
			out.WriteString(header)
		}
		out.WriteString("\n")
	}

	// Print rows
	for _, row := range al.Rows {
		if len(al.Headers) > 0 {
			out.WriteString("     ") // 5 spaces for header tables
		} else {
			out.WriteString("    ") // 4 spaces for matrices
		}
		for j, expr := range row {
			if j > 0 {
				out.WriteString(" ")
			}
			if expr != nil {
				out.WriteString(expr.String())
			} else {
				out.WriteString("<nil>")
			}
		}
		out.WriteString("\n")
	}

	out.WriteString("]")
	return out.String()
}

// StructLiteral represents a struct value declared in .pt code mode.
// Example:
// p = Person
//
//	:name age
//	"Tejas" 35
type StructLiteral struct {
	Token   token.Token // the type name token (e.g. "Person")
	Headers []token.Token
	Row     []Expression // Single value row for this struct definition/usage
}

func (sl *StructLiteral) expressionNode()  {}
func (sl *StructLiteral) Tok() token.Token { return sl.Token }
func (sl *StructLiteral) String() string {
	var out bytes.Buffer
	out.WriteString(sl.Token.Literal)

	if len(sl.Headers) > 0 {
		out.WriteString("\n    :")
		for i, header := range sl.Headers {
			if i > 0 {
				out.WriteString(" ")
			}
			out.WriteString(header.Literal)
		}
	}

	if len(sl.Row) > 0 {
		out.WriteString("\n    ")
		for i, expr := range sl.Row {
			if i > 0 {
				out.WriteString(" ")
			}
			out.WriteString(expr.String())
		}
	}
	return out.String()
}

// ArrayRangeExpression represents arr[expr] accesses, where expr can be either
// a single index or a range literal (e.g., arr[0:5]).
type ArrayRangeExpression struct {
	Token token.Token // the '[' token
	Array Expression  // base array expression
	Range Expression  // index or range expression inside the brackets
}

func (ar *ArrayRangeExpression) expressionNode()  {}
func (ar *ArrayRangeExpression) Tok() token.Token { return ar.Token }
func (ar *ArrayRangeExpression) String() string {
	return fmt.Sprintf("%s[%s]", ar.Array.String(), ar.Range.String())
}

// DotExpression represents field access: base.field
type DotExpression struct {
	Token token.Token // the '.' token
	Left  Expression
	Field string
}

func (de *DotExpression) expressionNode()  {}
func (de *DotExpression) Tok() token.Token { return de.Token }
func (de *DotExpression) String() string {
	return fmt.Sprintf("%s.%s", de.Left.String(), de.Field)
}

type PrefixExpression struct {
	Token    token.Token // The prefix token, e.g. !
	Operator string
	Right    Expression
}

func (pe *PrefixExpression) expressionNode()  {}
func (pe *PrefixExpression) Tok() token.Token { return pe.Token }
func (pe *PrefixExpression) String() string {
	var out bytes.Buffer

	out.WriteString("(")
	out.WriteString(pe.Operator)
	if pe.Right != nil {
		out.WriteString(pe.Right.String())
	} else {
		out.WriteString("<nil>")
	}
	out.WriteString(")")

	return out.String()
}

type InfixExpression struct {
	Token    token.Token // The operator token, e.g. +
	Left     Expression
	Operator string
	Right    Expression
}

func (ie *InfixExpression) expressionNode()  {}
func (ie *InfixExpression) Tok() token.Token { return ie.Token }
func (ie *InfixExpression) String() string {
	var out bytes.Buffer

	out.WriteString("(")
	if ie.Left != nil {
		out.WriteString(ie.Left.String())
	} else {
		out.WriteString("<nil>")
	}
	out.WriteString(" " + ie.Operator + " ")
	if ie.Right != nil {
		out.WriteString(ie.Right.String())
	} else {
		out.WriteString("<nil>")
	}
	out.WriteString(")")

	return out.String()
}

type FuncStatement struct {
	Token      token.Token // The identifier
	Parameters []*Identifier
	Outputs    []*Identifier
	Body       *BlockStatement
}

func (fl *FuncStatement) statementNode()   {}
func (fl *FuncStatement) Tok() token.Token { return fl.Token }
func (fl *FuncStatement) String() string {
	var out bytes.Buffer

	params := []string{}
	for _, p := range fl.Parameters {
		params = append(params, p.String())
	}

	out.WriteString(fl.Tok().Literal)
	out.WriteString("(")
	out.WriteString(strings.Join(params, ", "))
	out.WriteString(") ")
	out.WriteString(fl.Body.String())

	return out.String()
}

type CallExpression struct {
	Token     token.Token // The '(' token
	Function  *Identifier // Identifier or FunctionLiteral
	Arguments []Expression
}

func (ce *CallExpression) expressionNode()  {}
func (ce *CallExpression) Tok() token.Token { return ce.Token }
func (ce *CallExpression) String() string {
	var out bytes.Buffer

	args := []string{}
	for _, a := range ce.Arguments {
		args = append(args, a.String())
	}

	out.WriteString(ce.Function.String())
	out.WriteString("(")
	out.WriteString(strings.Join(args, ", "))
	out.WriteString(")")

	return out.String()
}

// ExprChildren returns the immediate child expressions of an AST node.
// Returns nil for leaf nodes (Identifier, IntegerLiteral, FloatLiteral,
// StringLiteral, HeapStringLiteral).
func ExprChildren(expr Expression) []Expression {
	switch e := expr.(type) {
	case *InfixExpression:
		return []Expression{e.Left, e.Right}
	case *PrefixExpression:
		return []Expression{e.Right}
	case *CallExpression:
		return e.Arguments
	case *ArrayLiteral:
		children := []Expression{}
		for _, row := range e.Rows {
			children = append(children, row...)
		}
		return children
	case *StructLiteral:
		return append([]Expression(nil), e.Row...)
	case *ArrayRangeExpression:
		return []Expression{e.Array, e.Range}
	case *DotExpression:
		return []Expression{e.Left}
	case *RangeLiteral:
		children := []Expression{e.Start, e.Stop}
		if e.Step != nil {
			children = append(children, e.Step)
		}
		return children
	}
	return nil
}
