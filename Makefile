# Pluto Compiler Makefile

# Version information from git
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Build flags
LDFLAGS := -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildDate=$(BUILD_DATE)

.PHONY: build dev test clean

# Production build with version info
build:
	go build -ldflags "$(LDFLAGS)" -o pluto

# Development build (no version injection, faster)
dev:
	go build -o pluto

# Run tests
test:
	python3 test.py

# Clean build artifacts
clean:
	rm -f pluto
	./pluto clean 2>/dev/null || true
