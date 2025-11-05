package compiler

import (
	"maps"
)

type ScopeKind int

const (
	FuncScope ScopeKind = iota
	BlockScope
)

type Scope[T any] struct {
	Elems     map[string]T
	ScopeKind ScopeKind
}

func NewScope[T any](sk ScopeKind) Scope[T] {
	return Scope[T]{
		Elems:     make(map[string]T), // Start with global scope
		ScopeKind: sk,
	}
}

func PushScope[T any](scopes *[]Scope[T], sk ScopeKind) {
	*scopes = append(*scopes, NewScope[T](sk))
}

func PopScope[T any](scopes *[]Scope[T]) {
	if len(*scopes) == 1 {
		panic("cannot pop global scope")
	}
	*scopes = (*scopes)[:len(*scopes)-1]
}

// Put does not need a pointer, as it modifies the map within a scope, not the slice itself.
func Put[T any](scopes []Scope[T], name string, elem T) {
	scopes[len(scopes)-1].Elems[name] = elem
}

// PutBulk is also fine without a pointer.
func PutBulk[T any](scopes []Scope[T], elems map[string]T) {
	maps.Copy(scopes[len(scopes)-1].Elems, elems)
}

func findScopeIndex[T any](scopes []Scope[T], name string) (int, bool) {
	for i := len(scopes) - 1; i >= 0; i-- {
		if _, ok := scopes[i].Elems[name]; ok {
			return i, true
		}
		if scopes[i].ScopeKind == FuncScope {
			break
		}
	}
	return -1, false
}

// SetExisting updates the innermost scope (up to the nearest FuncScope) where name is defined.
// Returns true if the name was found and updated.
func SetExisting[T any](scopes []Scope[T], name string, elem T) bool {
	if idx, ok := findScopeIndex(scopes, name); ok {
		scopes[idx].Elems[name] = elem
		return true
	}
	return false
}

func Get[T any](scopes []Scope[T], name string) (T, bool) {
	if idx, ok := findScopeIndex(scopes, name); ok {
		return scopes[idx].Elems[name], true
	}
	var zero T
	return zero, false
}
