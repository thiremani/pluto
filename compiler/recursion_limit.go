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

// recursionLimit owns the active recursive-discovery stack and its resource
// fuse. firstFrame and cycleStart make recurrence tracking constant-time as
// frames are pushed and popped.
type recursionLimit struct {
	stack      []specializationFrame
	firstFrame map[*ast.FuncStatement]int
	cycleStart int
	maxFrames  int
	hit        bool
}

func newRecursionLimit(maxFrames int) recursionLimit {
	return recursionLimit{
		firstFrame: make(map[*ast.FuncStatement]int),
		cycleStart: -1,
		maxFrames:  maxFrames,
	}
}

// check checks the recursive inference resource limit immediately before a new
// FuncInfo enters the shared cache. A cycle starts at the earliest active frame
// whose template repeats in the candidate path. It returns an error only when
// the limit is first encountered.
func (limit *recursionLimit) check(mangled string, template *ast.FuncStatement, tok token.Token) *token.CompileError {
	if limit.hit {
		return nil
	}

	cycleStart := limit.cycleStart
	if first, repeated := limit.firstFrame[template]; repeated && (cycleStart < 0 || first < cycleStart) {
		cycleStart = first
	}
	if cycleStart < 0 || len(limit.stack)+1-cycleStart <= limit.maxFrames {
		return nil
	}

	candidate := specializationFrame{mangled: mangled, template: template}
	limit.hit = true
	return &token.CompileError{
		Token: tok,
		Msg: fmt.Sprintf(
			"recursive specialization resource limit exceeded (limit %d active specialization frames in one recursive inference region); active signature chain: %s",
			limit.maxFrames,
			formatSpecializationChain(limit.stack[cycleStart:], candidate),
		),
	}
}

func (limit *recursionLimit) push(frame specializationFrame) int {
	previousCycleStart := limit.cycleStart
	index := len(limit.stack)

	if first, repeated := limit.firstFrame[frame.template]; repeated {
		if limit.cycleStart < 0 || first < limit.cycleStart {
			limit.cycleStart = first
		}
	} else {
		limit.firstFrame[frame.template] = index
	}
	limit.stack = append(limit.stack, frame)

	return previousCycleStart
}

func (limit *recursionLimit) pop(previousCycleStart int) {
	index := len(limit.stack) - 1
	frame := limit.stack[index]

	if limit.firstFrame[frame.template] == index {
		delete(limit.firstFrame, frame.template)
	}
	limit.stack = limit.stack[:index]
	limit.cycleStart = previousCycleStart
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
