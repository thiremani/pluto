package compiler

import (
	"maps"

	"tinygo.org/x/go-llvm"
)

type ScopeKind int

const (
	FuncScope ScopeKind = iota
	BlockScope
)

type Symbol struct {
	Val      llvm.Value
	Type     Type
	FuncArg  bool
	ReadOnly bool
}

func GetCopy(s *Symbol) (newSym *Symbol) {
	newSym = &Symbol{}
	newSym.Val = s.Val
	newSym.Type = s.Type
	newSym.FuncArg = s.FuncArg
	newSym.ReadOnly = s.ReadOnly
	return newSym
}

type Scope struct {
	Symbols   map[string]*Symbol
	ScopeKind ScopeKind
}

func NewScope(sk ScopeKind) Scope {
	return Scope{
		Symbols:   make(map[string]*Symbol), // Start with global scope
		ScopeKind: sk,
	}
}

func (c *Compiler) PushScope(sk ScopeKind) {
	c.Scopes = append(c.Scopes, NewScope(sk))
}

func (c *Compiler) PopScope() {
	if len(c.Scopes) == 1 {
		panic("cannot pop global scope")
	}
	c.Scopes = c.Scopes[:len(c.Scopes)-1]
}

func (c *Compiler) Put(name string, s *Symbol) {
	c.Scopes[len(c.Scopes)-1].Symbols[name] = s
}

func (c *Compiler) PutBulk(syms map[string]*Symbol) {
	maps.Copy(c.Scopes[len(c.Scopes)-1].Symbols, syms)
}

func (c *Compiler) Get(name string) (*Symbol, bool) {
	// Search from innermost scope outward
	// if in func we only search until func scope
	for i := len(c.Scopes) - 1; i >= 0; i-- {
		if sym, ok := c.Scopes[i].Symbols[name]; ok {
			return sym, true
		}
		if c.Scopes[i].ScopeKind == FuncScope {
			break
		}
	}
	return nil, false
}
