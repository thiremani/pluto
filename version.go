package main

import (
	"fmt"
	"runtime"
)

// Build-time variables injected via linker flags (ldflags).
//
// These defaults are used for development builds (go build -o pluto).
// Production builds use: make build
//
// The Makefile runs:
//
//	go build -ldflags "-X main.Version=$(git describe --tags) ..." -o pluto
//
// The -X flag overwrites these string variables at link time.
// See: https://pkg.go.dev/cmd/link (-X importpath.name=value)
var (
	Version   = "dev"     // Overwritten with git tag (e.g., "v0.5.0")
	Commit    = "unknown" // Overwritten with git commit hash
	BuildDate = "unknown" // Overwritten with build timestamp
)

// printVersion prints version information to stdout.
func printVersion() {
	fmt.Printf("pluto %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
	if Commit != "unknown" {
		fmt.Printf("  commit: %s\n", Commit)
	}
	if BuildDate != "unknown" {
		fmt.Printf("  built:  %s\n", BuildDate)
	}
}
