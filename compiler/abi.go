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
	Source    Type
	Lowered   Type
	Mode      ABIParamMode
	AliasSlot int
}

type ABIReturn struct {
	Mode       ABIReturnMode
	DirectType Type
	OutTypes   []Type
}

// FuncABI captures the lowered function boundary for one mangled variant.
// Direct scalar returns carry a hidden destination seed so a skipped write
// preserves the caller's value. Range-bearing variants may additionally need
// hidden alias state for loop-carried accumulation.
type FuncABI struct {
	Params         []ABIParam
	Return         ABIReturn
	HasRangeParams bool
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

	for _, paramType := range paramTypes {
		if paramType.Kind() == RangeKind || paramType.Kind() == ArrayRangeKind {
			abi.HasRangeParams = true
			break
		}
	}

	aliasSlot := 0
	for i, paramType := range paramTypes {
		paramABI := ABIParam{
			Source:    paramType,
			Lowered:   Ptr{Elem: paramType},
			Mode:      ABIParamIndirect,
			AliasSlot: -1,
		}
		if isDirectScalarABIType(paramType) {
			paramABI.Mode = ABIParamDirect
			paramABI.Lowered = paramType
			if abi.HasRangeParams {
				paramABI.AliasSlot = aliasSlot
				aliasSlot++
			}
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

func (abi FuncABI) NumAliasSlots() int {
	count := 0
	for _, param := range abi.Params {
		if param.AliasSlot >= 0 {
			count++
		}
	}
	return count
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

func (abi FuncABI) AliasParamBaseIndex() int {
	return abi.sourceParamBaseIndex() + len(abi.Params)
}

func (abi FuncABI) AliasFunctionParamIndex(paramIndex int) int {
	slot := abi.Params[paramIndex].AliasSlot
	if slot < 0 {
		return -1
	}
	return abi.AliasParamBaseIndex() + slot
}

func (abi FuncABI) DirectReturnSeedParamIndex() int {
	if abi.Return.Mode != ABIReturnDirect {
		return -1
	}
	return abi.AliasParamBaseIndex() + abi.NumAliasSlots()
}
