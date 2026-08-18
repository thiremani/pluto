package compiler

type ScopeKind int

const (
	FuncScope ScopeKind = iota
	BlockScope
)

type Scope[T any] struct {
	Elems map[string]T
	// BindingOrder records first insertion so owned values can be released in reverse order.
	BindingOrder []string
	ScopeKind    ScopeKind
}

func NewScope[T any](sk ScopeKind) Scope[T] {
	return Scope[T]{
		Elems:     make(map[string]T),
		ScopeKind: sk,
	}
}

func PushScope[T any](scopes *[]Scope[T], sk ScopeKind) {
	*scopes = append(*scopes, NewScope[T](sk))
}

func PopScope[T any](scopes *[]Scope[T]) {
	if len(*scopes) == 1 {
		panic("internal: cannot pop global scope")
	}
	*scopes = (*scopes)[:len(*scopes)-1]
}

// Put adds or replaces a binding in the current scope without reordering existing names.
func Put[T any](scopes []Scope[T], name string, elem T) {
	scope := &scopes[len(scopes)-1]
	if _, exists := scope.Elems[name]; !exists {
		scope.BindingOrder = append(scope.BindingOrder, name)
	}
	scope.Elems[name] = elem
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

// DeleteBulk removes names from the current scope only.
func DeleteBulk[T any](scopes []Scope[T], names []string) {
	scope := &scopes[len(scopes)-1]
	for _, name := range names {
		delete(scope.Elems, name)
	}

	order := scope.BindingOrder[:0]
	for _, name := range scope.BindingOrder {
		if _, exists := scope.Elems[name]; exists {
			order = append(order, name)
		}
	}
	scope.BindingOrder = order
}
