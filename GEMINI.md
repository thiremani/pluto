# Repository Guidelines

## Project Overview

This project is a compiler for the Pluto programming language, written in Go. It compiles Pluto source files (`.pt` and `.spt`) into LLVM Intermediate Representation (IR), which is then compiled into native executables.

## Key Files

*   `main.go`: The command-line interface for the Pluto compiler. It scans for source files, manages the compilation process, and generates the final executables.
*   `go.mod`: The Go module file, which defines the project's dependencies.
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
*   LLVM 21 (including `clang`, `opt`, `llc`, and `lld`)
*   Python 3.x
*   pip (for installing Python dependencies)

On macOS with Homebrew, you can install LLVM with `brew install llvm` and add it to your path. The path is `/opt/homebrew/opt/llvm/bin` (ARM) or `/usr/local/opt/llvm/bin` (Intel).

### Commands

*   **Install Python dependencies:**
    ```bash
    pip install -r requirements.txt
    ```

*   **Build the compiler:**
    ```bash
    # Development build (version shows as "dev")
    go build -o pluto

    # Production build with version from git tag (optional, used for releases)
    go build -ldflags "-X main.Version=$(git describe --tags --always --dirty) -X main.Commit=$(git rev-parse --short HEAD) -X main.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o pluto
    ```

*   **Run the full test suite:**
    ```bash
    python3 test.py
    python3 test.py --leak-check
    ```

*   **Run the Python test runner directly:**
    ```bash
    python3 test.py              # Run all tests
    python3 test.py --keep       # Keep build artifacts for debugging
    python3 test.py --leak-check # Run tests with memory leak detection
    ```

*   **Run unit tests:**
    ```bash
    go test -race ./lexer ./parser ./compiler
    ```

*   **Run the compiler:**
    ```bash
    ./pluto [directory]    # Compiles .pt and .spt files in directory
    ./pluto --version      # Show version information (or -v)
    ./pluto --clean        # Clear cache for current version (or -c)
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
- Cache layout (versioned to isolate different compiler versions):
  - `<PTCACHE>/<version>/runtime/<hash>/` for compiled runtime objects
  - `<PTCACHE>/<version>/<module-path>/{code,script}` for IR/objects

## Debugging & Configuration Tips

The compiler uses a cache to store intermediate build artifacts (LLVM IR and object files) to speed up subsequent compilations.

*   The cache location is determined by the `PTCACHE` environment variable.
*   If `PTCACHE` is not set, it defaults to:
    *   macOS: `$HOME/Library/Caches/pluto`
    *   Linux: `$HOME/.cache/pluto`
    *   Windows: `%LocalAppData%\pluto`

To clear the cache for the current version, run `./pluto --clean`. To clear the entire cache manually, delete the appropriate directory.

- Quick smoke check: `./pluto tests/` to see compile/link output.
- `PTCACHE` overrides cache location; ensure PATH includes LLVM 21 tools.
- Use `pluto --clean` to clear cache for current version.

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
  - Leak check run: `python3 test.py --leak-check [tests/math]`.
- Leak tools by platform:
  - Linux: `valgrind`
  - macOS: `leaks`

CI: GitHub Actions builds with Go 1.25, installs LLVM 21 + valgrind, and runs `python3 test.py --leak-check` on pushes/PRs.

## Commit & Pull Request Guidelines
- Commit style: Conventional Commits for the subject line (e.g., `feat(parser): ...`, `refactor(compiler): ...`).
- Production-quality commit expectation for non-trivial changes:
  - Add a short body describing what changed and the user-visible or behavioral impact.
  - Include important context needed by future readers (constraints, tradeoffs, or risks) when not obvious from the diff.
  - Reference issue/ticket IDs when applicable.
  - Call out breaking changes or migration steps explicitly.
- Test/validation command details are optional in commit messages; put full verification details in the PR description when possible.
- PRs: include a clear description, linked issues, unit/E2E tests for changes, and sample before/after output where relevant.

## Code Review Checklist

When reviewing PRs or preparing code for review, check:

1. **Modularity & readability**: Is each function focused on a single responsibility? Can a new reader follow the logic without excessive cross-referencing?
2. **Placement**: Do changes belong in the functions, arguments, and structs they touch, or should logic be moved to a more natural home?
3. **Duplication**: Is there code that duplicates existing patterns in the codebase? Extract shared logic into a helper or utility (e.g., `ast.ExprChildren` for tree-walking) rather than repeating type-switches or loop bodies.
4. **Nesting & control flow**: Can nested `if`/`for` blocks be flattened using early `return`, `continue`, or `break`? Prefer guard clauses over deep indentation.
5. **Naming**: Are new identifiers clear, consistent with existing conventions, and free of ambiguity? Avoid mixing synonyms (e.g., `tmp` vs `temp`) for the same concept.
6. **Edge cases**: Are zero-length slices, nil maps, and boundary values handled? Does the code distinguish "absent" from "empty"?
7. **Resource cleanup**: For compiler code specifically — are heap temporaries freed on all paths (true and false branches)? Are borrowed vs owned semantics respected?

## Instructions for AI Assistants
- Keep changes minimal and focused; avoid reflowing or reindenting unrelated lines.
- Use tabs for indentation (preserve existing indentation style).
- NEVER add "🤖 Generated with..." footers to git commits.
