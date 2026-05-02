package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"tinygo.org/x/go-llvm"
)

const llvmOptPipeline = "default<O3>"

var (
	llvmCodegenInitOnce sync.Once
	llvmCodegenInitErr  error
)

func ensureLLVMCodegenInitialized() error {
	llvmCodegenInitOnce.Do(func() {
		if err := llvm.InitializeNativeTarget(); err != nil {
			llvmCodegenInitErr = fmt.Errorf("initialize native LLVM target: %w", err)
			return
		}
		if err := llvm.InitializeNativeAsmPrinter(); err != nil {
			llvmCodegenInitErr = fmt.Errorf("initialize native LLVM asm printer: %w", err)
		}
	})
	return llvmCodegenInitErr
}

func (cfg buildConfig) llvmTargetCPU() string {
	if cfg.targetCPU.disabled {
		return ""
	}
	if strings.EqualFold(cfg.targetCPU.bare, "native") {
		return llvmHostCPUName()
	}
	return cfg.targetCPU.bare
}

func (cfg buildConfig) llvmTargetFeatures() string {
	if cfg.targetCPU.disabled || !strings.EqualFold(cfg.targetCPU.bare, "native") {
		return ""
	}
	return llvmHostCPUFeatures()
}

func (cfg buildConfig) llvmRelocMode() llvm.RelocMode {
	if runtime.GOOS == OS_WINDOWS {
		return llvm.RelocDefault
	}
	return llvm.RelocPIC
}

func (cfg buildConfig) newTargetMachine() (llvm.TargetMachine, error) {
	if err := ensureLLVMCodegenInitialized(); err != nil {
		return llvm.TargetMachine{}, err
	}

	triple := llvm.DefaultTargetTriple()
	if triple == "" {
		return llvm.TargetMachine{}, fmt.Errorf("LLVM default target triple is empty")
	}

	target, err := llvm.GetTargetFromTriple(triple)
	if err != nil {
		return llvm.TargetMachine{}, fmt.Errorf("get LLVM target for %q: %w", triple, err)
	}

	tm := target.CreateTargetMachine(
		triple,
		cfg.llvmTargetCPU(),
		cfg.llvmTargetFeatures(),
		llvm.CodeGenLevelAggressive,
		cfg.llvmRelocMode(),
		llvm.CodeModelDefault,
	)
	if tm.C == nil {
		return llvm.TargetMachine{}, fmt.Errorf("create LLVM target machine for %q", triple)
	}
	return tm, nil
}

func (p *Pluto) emitObject(module llvm.Module, objFile string) error {
	tm, err := p.Config.newTargetMachine()
	if err != nil {
		return err
	}
	defer tm.Dispose()

	pbo := llvm.NewPassBuilderOptions()
	defer pbo.Dispose()
	if err := module.RunPasses(llvmOptPipeline, tm, pbo); err != nil {
		return fmt.Errorf("optimize LLVM module with %s: %w", llvmOptPipeline, err)
	}

	obj, err := tm.EmitToMemoryBuffer(module, llvm.ObjectFile)
	if err != nil {
		return fmt.Errorf("emit object file: %w", err)
	}
	defer obj.Dispose()

	if err := os.MkdirAll(filepath.Dir(objFile), 0755); err != nil {
		return fmt.Errorf("create script cache dir: %w", err)
	}
	if err := os.WriteFile(objFile, obj.Bytes(), 0644); err != nil {
		return fmt.Errorf("write object file %s: %w", objFile, err)
	}
	return nil
}
