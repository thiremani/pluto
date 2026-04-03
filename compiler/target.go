package compiler

import (
	"sync"

	"tinygo.org/x/go-llvm"
)

var (
	targetMetadataOnce sync.Once
	targetTriple       string
	targetDataLayout   string
)

func defaultModuleTargetMetadata() (triple, dataLayout string) {
	targetMetadataOnce.Do(func() {
		llvm.InitializeAllTargetInfos()
		llvm.InitializeAllTargets()
		llvm.InitializeAllTargetMCs()

		targetTriple = llvm.DefaultTargetTriple()
		if targetTriple == "" {
			return
		}

		target, err := llvm.GetTargetFromTriple(targetTriple)
		if err != nil {
			return
		}

		tm := target.CreateTargetMachine(
			targetTriple,
			"",
			"",
			llvm.CodeGenLevelDefault,
			llvm.RelocDefault,
			llvm.CodeModelDefault,
		)
		if tm.C == nil {
			return
		}
		defer tm.Dispose()

		td := tm.CreateTargetData()
		defer td.Dispose()
		targetDataLayout = td.String()
	})

	return targetTriple, targetDataLayout
}
