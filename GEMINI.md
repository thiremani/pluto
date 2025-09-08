# Repository Guidelines

## Project Overview

This project is a compiler for the Pluto programming language, written in Go. It compiles Pluto source files (`.pt` and `.spt`) into LLVM Intermediate Representation (IR), which is then compiled into native executables.

## Key Files

*   `main.go`: The command-line interface for the Pluto compiler. It scans for source files, manages the compilation process, and generates the final executables.
*   `go.mod`: The Go module file, which defines the project's dependencies.
*   `Makefile`: Contains helper commands for building and testing the project.
*   `test.py`: A Python script used to run integration tests.
*   `requirements.txt`: Lists the Python dependencies for the test runner.
*   `ast/`: Defines the Abstract Syntax Tree (AST) for the Pluto language.
*   `lexer/`: Handles tokenization of the Pluto source code, including support for indentation-based syntax.
*   `parser/`: Parses the token stream from the lexer and builds the AST. It uses a recursive descent approach and supports operator precedence.
*   `compiler/`: Performs type checking, symbol resolution, and generates LLVM IR from the AST. It includes a CFG-based type solver for type inference.
*   `runtime/`: Contains a small C runtime library that is embedded in the binary and linked with the compiled Pluto code to provide low-level operations.
*   `tests/`: Contains end-to-end integration tests, with `.spt` source files and `.exp` files for the expected output.
*   `pt.mod`: The module declaration file, similar to `go.mod`.

## Building, Testing, and Running

### Requirements

*   Go 1.25+
*   LLVM 20 (including `clang`, `opt`, `llc`, and `lld`)
*   Python 3.x
*   pip (for installing Python dependencies)

On macOS with Homebrew, you can install LLVM with `brew install llvm@20` and add it to your path. The paths are `/opt/homebrew/opt/llvm@20/bin` (ARM) or `/usr/local/opt/llvm@20/bin` (Intel).

### Commands

*   **Install Python dependencies:**
    ```bash
    pip install -r requirements.txt
    ```

*   **Build the compiler:**
    ```bash
    go build -o pluto main.go
    ```

*   **Run the full test suite:**
    ```bash
    python3 test.py
    ```

*   **Run the Python test runner directly:**
    ```bash
    python3 test.py              # Run all tests
    python3 test.py --keep       # Keep build artifacts for debugging
    ```

*   **Run unit tests:**
    ```bash
    go test -race ./lexer ./parser ./compiler
    ```

*   **Run the compiler:**
    ```bash
    ./pluto [directory]
    ```
    This will compile all `.pt` and `.spt` files in the specified directory and generate executables in the same directory.

*   **Run specific integration tests:**
    ```bash
    python3 test.py tests/math
    ```

## Architecture Overview

The compilation process consists of two main phases:

1.  **Code Compilation:** All `.pt` files in the target directory are compiled into a single LLVM IR module. These files are intended for reusable functions and constants.
2.  **Script Compilation:** For each `.spt` file, the compiler performs the following steps:
    a.  Links the code module (from the `.pt` files) into the script's module.
    b.  Generates LLVM IR for the script.
    c.  Optimizes the IR using `opt -O3`.
    d.  Compiles the optimized IR into an object file using `llc`.
    e.  Links the object file with the C runtime to create a native executable.

- Module resolution: walks up to find `pt.mod`; cache key based on module path.
- Cache layout: `<PTCACHE>/<module-path>/{code,script}` stores IR/objects.

## Debugging & Configuration Tips

The compiler uses a cache to store intermediate build artifacts (LLVM IR and object files) to speed up subsequent compilations.

*   The cache location is determined by the `PTCACHE` environment variable.
*   If `PTCACHE` is not set, it defaults to:
    *   macOS: `$HOME/Library/Caches/pluto`
    *   Linux: `$HOME/.cache/pluto`
    *   Windows: `%LocalAppData%\pluto`

To clear the cache, you can delete the appropriate directory.

- Quick smoke check: `./pluto tests/` to see compile/link output.
- `PTCACHE` overrides cache location; ensure PATH includes LLVM 20 tools.

## Coding Style & Naming Conventions
- Indentation: Use tabs for indentation across the repository; do not convert leading tabs to spaces. Preserve existing indentation when editing.
- Go files: Leading indentation MUST be tabs (this is gofmt's default). Run `gofmt -w` (or enable format‑on‑save) before committing. It's fine for gofmt to leave spaces for alignment within a line; the rule applies to leading indentation only.
- Go formatting: `go fmt ./...`; basic checks: `go vet ./...`.
- Packages: lowercase short names. Exports: `CamelCase`. Tests: `*_test.go` with `TestXxx` functions.
- Filenames: lowercase with underscores where needed (Go convention).

## Testing Guidelines
- Unit tests live under each package; run with `go test -race` as above.
- E2E tests live in `tests/`:
  - Inputs: `.spt` (scripts) and optional `.pt` (shared code).
  - Expected output: `.exp` (line-by-line, supports `re:` regex prefixes).
- Run: `python3 test.py [--keep]`.
  - Focused run: `python3 test.py tests/math`.

CI: GitHub Actions builds with Go 1.25, installs LLVM 20, and runs `python3 test.py` on pushes/PRs.

## Commit & Pull Request Guidelines
- Commit style: Conventional Commits (e.g., `feat(parser): ...`, `refactor(compiler): ...`).
- PRs: include a clear description, linked issues, unit/E2E tests for changes, and sample before/after output where relevant.

## Instructions for AI Assistants
- Keep changes minimal and focused; avoid reflowing or reindenting unrelated lines.
- Use tabs for indentation (preserve existing indentation style).
- NEVER add "🤖 Generated with..." footers to git commits.