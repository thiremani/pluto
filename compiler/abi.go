package compiler

type ABIParamMode int

const (
	ABIParamIndirect ABIParamMode = iota
	ABIParamDirect
)

type ABIReturnMode int

const (
	ABIReturnIndirect ABIReturnMode = iota
	ABIReturnDirect
)

type ABIParam struct {
	Source  Type
	Lowered Type
	Mode    ABIParamMode
}

type ABIReturn struct {
	Mode       ABIReturnMode
	DirectType Type
	OutTypes   []Type
}

// FuncABI captures the lowered function boundary for one mangled variant.
// Direct scalar returns carry a hidden destination seed so a skipped write
// preserves the caller's value. Whether an input shares a caller binding with
// an output is a compile-time property of each call site, lowered as a private
// variant of the function; it never appears in the native signature.
type FuncABI struct {
	Params []ABIParam
	Return ABIReturn
}

func isDirectScalarABIType(t Type) bool {
	switch tt := t.(type) {
	case Int:
		return tt.Width == 64
	case Float:
		return tt.Width == 64
	default:
		return false
	}
}

func directScalarABIReturnType(outTypes []Type) (Type, bool) {
	if len(outTypes) != 1 {
		return nil, false
	}
	if !isDirectScalarABIType(outTypes[0]) {
		return nil, false
	}
	return outTypes[0], true
}

func classifyFuncABI(paramTypes []Type, outTypes []Type) FuncABI {
	abi := FuncABI{
		Params: make([]ABIParam, len(paramTypes)),
		Return: ABIReturn{
			Mode:     ABIReturnIndirect,
			OutTypes: append([]Type(nil), outTypes...),
		},
	}

	for i, paramType := range paramTypes {
		paramABI := ABIParam{
			Source:  paramType,
			Lowered: Ptr{Elem: paramType},
			Mode:    ABIParamIndirect,
		}
		if isDirectScalarABIType(paramType) {
			paramABI.Mode = ABIParamDirect
			paramABI.Lowered = paramType
		}
		abi.Params[i] = paramABI
	}

	if directType, ok := directScalarABIReturnType(outTypes); ok {
		// Whether a function body writes its output conditionally is not part
		// of the type-based mangle. Keep the native C ABI stable across body
		// changes: direct-return mode always implies a destination seed.
		abi.Return.Mode = ABIReturnDirect
		abi.Return.DirectType = directType
	}

	return abi
}

func (abi FuncABI) UsesIndirectReturn() bool {
	return abi.Return.Mode == ABIReturnIndirect
}

func (abi FuncABI) sourceParamBaseIndex() int {
	if abi.UsesIndirectReturn() {
		return 1
	}
	return 0
}

func (abi FuncABI) SourceFunctionParamIndex(paramIndex int) int {
	return abi.sourceParamBaseIndex() + paramIndex
}

func (abi FuncABI) DirectReturnSeedParamIndex() int {
	if abi.Return.Mode != ABIReturnDirect {
		return -1
	}
	return abi.sourceParamBaseIndex() + len(abi.Params)
}

// sharableOutput reports whether an input of paramType can share an output
// declared as outType: the input's storage must be the declared type or a
// compatible wider representation that a store converts (an owned string for
// a static output, a concrete-rank array for an untyped empty one, a schema
// for a header-only table). A struct shares only at its exact type, since
// nothing converts its fields. The shared output then uses the input's
// storage, so a write lands where the next read looks.
func sharableOutput(paramType, outType Type) bool {
	if _, isStruct := outType.(Struct); isStruct {
		return TypeEqual(paramType, outType)
	}
	return bindingSlotCompatible(paramType, outType) && TypeEqual(mergeBindingSlotType(paramType, outType), paramType)
}

// aliasPattern decides, per callee parameter, the one-based caller destination
// whose binding the argument shares, or 0; nil when no parameter shares one.
// argNames holds one entry per parameter, empty for an argument that is not a
// plain identifier. dests contains output destination names in order, with
// synthetic staging names already resolved to the bindings they represent.
// outTypes are the declared output types. enclosing maps a caller-body input to
// the caller output it already shares, so a nested call forwards that sharing.
// A parameter shares at most one destination, the first that matches.
func aliasPattern(argNames, dests []string, paramTypes, outTypes []Type, enclosing map[string]string) []int {
	var pattern []int
	for i, name := range argNames {
		if name == "" {
			continue
		}

		for j, dest := range dests {
			if j >= len(outTypes) {
				break
			}
			if !sharableOutput(paramTypes[i], outTypes[j]) {
				continue
			}
			if dest != name && enclosing[name] != dest {
				continue
			}
			if pattern == nil {
				pattern = make([]int, len(argNames))
			}
			pattern[i] = j + 1
			break
		}
	}

	return pattern
}
