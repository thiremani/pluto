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
	Const Const
	Func  Func
	// Struct Struct
}

type FuncKey struct {
	FuncName string
	Arity    int
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

	return &Code{
		Const: Const,
		Func:  Func,
	}
}

func (c *Code) Merge(other *Code) {
	// Merge constants
	if other != nil {
		c.Const.Statements = append(c.Const.Statements, other.Const.Statements...)
		maps.Copy(c.Const.Map, other.Const.Map)
		c.Func.Statements = append(c.Func.Statements, other.Func.Statements...)
		maps.Copy(c.Func.Map, other.Func.Map)
	}

	// Add similar merging logic for struct when implemented
	// c.Struct.Merge(other.Struct)
}

type Const struct {
	Statements []*ConstStatement
	Map        map[string]*ConstStatement
}

type Func struct {
	Statements []*FuncStatement
	Map        map[FuncKey]*FuncStatement
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
	Expression []Expression
}

func (ps *PrintStatement) statementNode()   {}
func (ps *PrintStatement) Tok() token.Token { return ps.Token }
func (ps *PrintStatement) String() string {
	var out bytes.Buffer

	out.WriteString(printVec(ps.Expression))

	return out.String()
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
