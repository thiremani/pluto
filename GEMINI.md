# Pluto Language Compiler

## Project Overview

This project is a compiler for the Pluto programming language, written in Go. It compiles Pluto source files (`.pt` and `.spt`) into LLVM Intermediate Representation (IR), which is then compiled into native executables.

## Key Files

*   `main.go`: The command-line interface for the Pluto compiler. It scans for source files, manages the compilation process, and generates the final executables.
*   `go.mod`: The Go module file, which defines the project's dependencies.
*   `Makefile`: Contains helper commands for building and testing the project.
*   `test.sh`: The main test script, which orchestrates the entire test process.
*   `test.py`: A Python script used by `test.sh` to run integration tests.
*   `ast/`: Defines the Abstract Syntax Tree (AST) for the Pluto language.
*   `lexer/`: Handles tokenization of the Pluto source code, including support for indentation-based syntax.
*   `parser/`: Parses the token stream from the lexer and builds the AST. It uses a recursive descent approach and supports operator precedence.
*   `compiler/`: Performs type checking, symbol resolution, and generates LLVM IR from the AST. It includes a CFG-based type solver for type inference.
*   `runtime/`: Contains a small C runtime library that is embedded in the binary and linked with the compiled Pluto code to provide low-level operations.
*   `tests/`: Contains end-to-end integration tests, with `.spt` source files and `.exp` files for the expected output.
*   `pt.mod`: The module declaration file, similar to `go.mod`.

## Building, Testing, and Running

### Requirements

*   Go 1.24+
*   LLVM 20 (including `clang`, `opt`, `llc`, and `lld`)

On macOS with Homebrew, you can install LLVM with `brew install llvm@20` and add it to your path.

### Commands

*   **Build the compiler:**
    ```bash
    go build -o pluto main.go
    ```

*   **Run the full test suite:**
    ```bash
    ./test.sh
    ```
    This script builds the compiler, runs unit tests, and executes the integration tests via the `test.py` script.

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
    python test.py tests/math
    ```

## Compilation Process

The compilation process consists of two main phases:

1.  **Code Compilation:** All `.pt` files in the target directory are compiled into a single LLVM IR module. These files are intended for reusable functions and constants.
2.  **Script Compilation:** For each `.spt` file, the compiler performs the following steps:
    a.  Links the code module (from the `.pt` files) into the script's module.
    b.  Generates LLVM IR for the script.
    c.  Optimizes the IR using `opt -O3`.
    d.  Compiles the optimized IR into an object file using `llc`.
    e.  Links the object file with the C runtime to create a native executable.

## Caching

The compiler uses a cache to store intermediate build artifacts (LLVM IR and object files) to speed up subsequent compilations.

*   The cache location is determined by the `PTCACHE` environment variable.
*   If `PTCACHE` is not set, it defaults to:
    *   macOS: `$HOME/Library/Caches/pluto`
    *   Linux: `$HOME/.cache/pluto`
    *   Windows: `%LocalAppData%\pluto`

To clear the cache, you can delete the appropriate directory.

## Coding Style

*   **Formatting:** Use `go fmt ./...` to format the Go code.
*   **Linting:** Use `go vet ./...` to identify potential issues.
*   **Naming:** Follow standard Go naming conventions (e.g., `CamelCase` for exported identifiers, `snake_case` for filenames where appropriate).

## Contributing

*   **Commits:** Use the [Conventional Commits](https://www.conventionalcommits.org/) specification.
*   **Pull Requests:** Provide a clear description of the changes, link to any relevant issues, and include tests for your changes.