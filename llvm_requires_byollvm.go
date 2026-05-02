//go:build !byollvm

package main

// Pluto links tinygo.org/x/go-llvm against the system LLVM 22 installation.
// Build with GOFLAGS='-tags=byollvm' and CGO flags from llvm-config.
var _ = build_with_GOFLAGS_tags_byollvm_and_CGO_flags_from_llvm_config
