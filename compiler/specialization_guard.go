package compiler

import (
	"fmt"
	"strings"

	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
)

// maxActiveRecursiveSpecializations is deliberately generous for finite
// recursive closures while still stopping runaway cold discovery before a
// 257th active frame. It does not limit flat breadth or acyclic call depth.
const maxActiveRecursiveSpecializations = 256

// maxSpecializationTraceFrames retains the originating frame and the final
// seven frames near the failure. Together with maxSpecializationFrameRunes,
// this keeps a truncated signature chain to roughly 1.3K runes.
const maxSpecializationTraceFrames = 8

// maxSpecializationFrameRunes keeps one deeply nested demangled signature from
// dominating the diagnostic. It does not limit the underlying type or symbol.
const maxSpecializationFrameRunes = 160

type specializationFrame struct {
	mangled  string
	template *ast.FuncStatement
}

// specializationGuard owns the active recursive-discovery stack and its
// resource fuse. firstTemplateIndex and regionStart make recurrence tracking
// constant-time as frames are pushed and popped.
type specializationGuard struct {
	frames             []specializationFrame
	firstTemplateIndex map[*ast.FuncStatement]int
	regionStart        int
	limit              int
	failed             bool
}

func newSpecializationGuard() specializationGuard {
	return specializationGuard{
		firstTemplateIndex: make(map[*ast.FuncStatement]int),
		regionStart:        -1,
		limit:              maxActiveRecursiveSpecializations,
	}
}

func (guard *specializationGuard) reset() {
	guard.frames = guard.frames[:0]
	clear(guard.firstTemplateIndex)
	guard.regionStart = -1
	guard.failed = false
}

// checkAllocationLimit checks the recursive inference resource limit
// immediately before a new FuncInfo enters the shared cache. A recurrence
// starts at the earliest active frame whose template repeats in the candidate
// path. A true result with no error means an earlier allocation already
// encountered the limit.
func (guard *specializationGuard) checkAllocationLimit(mangled string, template *ast.FuncStatement, tok token.Token) (bool, *token.CompileError) {
	if guard.failed {
		return true, nil
	}

	regionStart := guard.regionStart
	if first, repeated := guard.firstTemplateIndex[template]; repeated && (regionStart < 0 || first < regionStart) {
		regionStart = first
	}
	if regionStart < 0 || len(guard.frames)+1-regionStart <= guard.limit {
		return false, nil
	}

	candidate := specializationFrame{mangled: mangled, template: template}
	guard.failed = true
	return true, &token.CompileError{
		Token: tok,
		Msg: fmt.Sprintf(
			"recursive specialization resource limit exceeded (limit %d active specialization frames in one recursive inference region); active signature chain: %s",
			guard.limit,
			formatSpecializationChain(guard.frames[regionStart:], candidate),
		),
	}
}

func (guard *specializationGuard) push(frame specializationFrame) int {
	previousRegionStart := guard.regionStart
	index := len(guard.frames)

	if first, repeated := guard.firstTemplateIndex[frame.template]; repeated {
		if guard.regionStart < 0 || first < guard.regionStart {
			guard.regionStart = first
		}
	} else {
		guard.firstTemplateIndex[frame.template] = index
	}
	guard.frames = append(guard.frames, frame)

	return previousRegionStart
}

func (guard *specializationGuard) pop(previousRegionStart int) {
	index := len(guard.frames) - 1
	frame := guard.frames[index]

	if guard.firstTemplateIndex[frame.template] == index {
		delete(guard.firstTemplateIndex, frame.template)
	}
	guard.frames = guard.frames[:index]
	guard.regionStart = previousRegionStart
}

func formatSpecializationChain(active []specializationFrame, candidate specializationFrame) string {
	totalFrames := len(active) + 1
	// Keep a nine-frame chain intact: replacing its one middle frame with an
	// omission marker would not shorten the output.
	if totalFrames <= maxSpecializationTraceFrames+1 {
		parts := make([]string, 0, totalFrames)
		for _, frame := range active {
			parts = append(parts, specializationDisplay(frame))
		}
		parts = append(parts, specializationDisplay(candidate))

		return strings.Join(parts, " -> ")
	}

	tailFrames := maxSpecializationTraceFrames - 1
	tailStart := totalFrames - tailFrames
	omitted := totalFrames - maxSpecializationTraceFrames
	parts := make([]string, 0, maxSpecializationTraceFrames+1)
	parts = append(parts, specializationDisplay(active[0]), specializationOmission(omitted))
	for i := tailStart; i < len(active); i++ {
		parts = append(parts, specializationDisplay(active[i]))
	}
	parts = append(parts, specializationDisplay(candidate))

	return strings.Join(parts, " -> ")
}

func specializationOmission(count int) string {
	noun := "specializations"
	if count == 1 {
		noun = "specialization"
	}

	return fmt.Sprintf("... %d %s omitted ...", count, noun)
}

func specializationDisplay(frame specializationFrame) string {
	parsed, err := DemangleParsed(frame.mangled)
	display := ""
	if err == nil && parsed.Kind == SymbolFunc {
		display = fmt.Sprintf("%s(%s)", parsed.Name, strings.Join(parsed.ArgTypes, ", "))
	} else {
		name := "function"
		if frame.template != nil {
			name = frame.template.Token.Literal
		}
		display = fmt.Sprintf("%s [%s]", name, frame.mangled)
	}

	runes := []rune(display)
	if len(runes) <= maxSpecializationFrameRunes {
		return display
	}

	return string(runes[:maxSpecializationFrameRunes-3]) + "..."
}
