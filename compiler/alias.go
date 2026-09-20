package compiler

// sharableOutput reports whether an input of paramType can share an output
// declared as outType: the input's storage must be the declared type or a
// compatible wider representation of it (an owned string for a static
// output, a concrete-rank array for an untyped empty one). The shared output
// then uses the input's storage, so a write lands where the next read looks.
func sharableOutput(paramType, outType Type) bool {
	return bindingSlotCompatible(paramType, outType) && TypeEqual(mergeBindingSlotType(paramType, outType), paramType)
}

// aliasPattern decides, per callee parameter, the one-based caller destination
// whose binding the argument shares, or 0; nil when no parameter shares one.
// argNames holds one entry per parameter, empty for an argument that is not a
// plain identifier; dests names the destinations of the call's outputs in
// order; outTypes are the declared output types. enclosing maps a caller-body
// input to the caller output it already shares, so a nested call forwards that
// sharing. A parameter shares at most one destination, the first that matches.
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
