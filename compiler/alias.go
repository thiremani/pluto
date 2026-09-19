package compiler

import (
	"strconv"
	"strings"
)

// widenedOutputStorage returns the storage a call's output slot uses: the
// destination's own storage when it is a compatible wider representation of
// the declared output (an owned string slot receiving a static output, a
// concrete-rank array slot receiving an untyped empty one), otherwise the
// declared type. Lowering and the CFG share it so both see the same sharing.
func widenedOutputStorage(declared, storage Type) Type {
	if TypeEqual(storage, declared) || !bindingSlotCompatible(storage, declared) {
		return declared
	}
	if !TypeEqual(mergeBindingSlotType(storage, declared), storage) {
		return declared
	}
	return storage
}

// aliasPattern decides, per callee parameter, the one-based caller destination
// whose binding the argument shares, or 0; nil when no parameter shares one.
// argNames holds one entry per parameter, empty for an argument that is not a
// plain identifier; dests names the destinations of the call's outputs in
// order; outTypes already carry storage widening. enclosing maps a caller-body
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
			if !aliasableOutput(paramTypes[i], outTypes[j]) {
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

// aliasPatternKey identifies one alias context; the empty key is the
// unshared context in which no parameter shares a destination.
func aliasPatternKey(pattern []int) string {
	parts := make([]string, len(pattern))
	for i, slot := range pattern {
		parts[i] = strconv.Itoa(slot)
	}
	return strings.Join(parts, "_")
}
