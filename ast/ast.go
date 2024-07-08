package ast

import (
	"bytes"
	"pluto/token"
	"strings"
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

func (p *Program) Tok() token.Token {
	if len(p.Statements) > 0 {
		return p.Statements[0].Tok()
	} else {
		return token.Token{
			Type: token.EOF,
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

	ret := a[0].(Expression).String()
	for _, val := range a[1:] {
		n := val.(Expression)
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
type LetStatement struct {
	Token token.Token // the token.ASSIGN token
	Name  []*Identifier
	Value []Expression
	Condition []Expression
}

func (ls *LetStatement) statementNode()       {}
func (ls *LetStatement) Tok() token.Token { return ls.Token }
func (ls *LetStatement) String() string {
	var out bytes.Buffer

	out.WriteString(printVec(toExpressionVec(ls.Name)))
	out.WriteString(" = ")

	if ls.Condition != nil {
		out.WriteString(printVec(ls.Condition))
		out.WriteString(" ")
	}

	if ls.Value != nil {
		out.WriteString(printVec(ls.Value))
	}

	return out.String()
}

type PrintStatement struct {
	Token token.Token // the first token of the expression
	Expression []Expression
}

func (ps *PrintStatement) statementNode()       {}
func (ps *PrintStatement) Tok() token.Token {return ps.Token}
func (ps *PrintStatement) String() string {
	var out bytes.Buffer

	out.WriteString(printVec(ps.Expression))

	return out.String()
}

type BlockStatement struct {
	Token      token.Token // the { token
	Statements []Statement
}

func (bs *BlockStatement) statementNode()       {}
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

func (i *Identifier) expressionNode()      {}
func (i *Identifier) Tok() token.Token { return i.Token }
func (i *Identifier) String() string       { return i.Value }

type IntegerLiteral struct {
	Token token.Token
	Value int64
}

func (il *IntegerLiteral) expressionNode()      {}
func (il *IntegerLiteral) Tok() token.Token { return il.Token }
func (il *IntegerLiteral) String() string       { return il.Token.Literal }

type PrefixExpression struct {
	Token    token.Token // The prefix token, e.g. !
	Operator string
	Right    Expression
}

func (pe *PrefixExpression) expressionNode()      {}
func (pe *PrefixExpression) Tok() token.Token { return pe.Token }
func (pe *PrefixExpression) String() string {
	var out bytes.Buffer

	out.WriteString("(")
	out.WriteString(pe.Operator)
	out.WriteString(pe.Right.String())
	out.WriteString(")")

	return out.String()
}

type InfixExpression struct {
	Token    token.Token // The operator token, e.g. +
	Left     Expression
	Operator string
	Right    Expression
}

func (ie *InfixExpression) expressionNode()      {}
func (ie *InfixExpression) Tok() token.Token { return ie.Token }
func (ie *InfixExpression) String() string {
	var out bytes.Buffer

	out.WriteString("(")
	out.WriteString(ie.Left.String())
	out.WriteString(" " + ie.Operator + " ")
	out.WriteString(ie.Right.String())
	out.WriteString(")")

	return out.String()
}

type FunctionLiteral struct {
	Token      token.Token // The identifier
	Parameters []*Identifier
	Outputs    []*Identifier
	Body       *BlockStatement
}

func (fl *FunctionLiteral) expressionNode()      {}
func (fl *FunctionLiteral) Tok() token.Token { return fl.Token }
func (fl *FunctionLiteral) String() string {
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
	Function  *Identifier  // Identifier or FunctionLiteral
	Arguments []Expression
}

func (ce *CallExpression) expressionNode()      {}
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