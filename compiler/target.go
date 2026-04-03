package compiler

import (
	"sync"

	"tinygo.org/x/go-llvm"
)

var (
	// Cache process-global host target metadata so each new LLVM module can reuse
	// it without repeating LLVM target initialization and target-machine setup.
	targetMetadataOnce sync.Once
	targetTriple       string
	targetDataLayout   string
)

func defaultModuleTargetMetadata() (triple, dataLayout string) {
	targetMetadataOnce.Do(func() {
		targetTriple = llvm.DefaultTargetTriple()
		if targetTriple == "" {
			return
		}

		if err := llvm.InitializeNativeTarget(); err != nil {
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
