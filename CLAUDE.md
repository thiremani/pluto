# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Requirements

- Go 1.25+
- LLVM 21 (including `clang`, `opt`, `llc`, and `lld`)
- Python 3.x
- pip (for installing Python dependencies)

On macOS with Homebrew, you can install LLVM with `brew install llvm` and add it to your path. The path is `/opt/homebrew/opt/llvm/bin` (ARM) or `/usr/local/opt/llvm/bin` (Intel).

## Development Commands

### Install Python dependencies
```bash
pip install -r requirements.txt
```

### Building the compiler
```bash
go build -o pluto
```

### Running tests
```bash
# Full test suite (builds compiler, runs unit tests, runs integration tests)
python3 test.py              # Run all tests
python3 test.py --keep       # Keep build artifacts for debugging

# Run specific integration tests
python3 test.py tests/math
```

### Running unit tests only
```bash
go test -race ./lexer ./parser ./compiler
```

### Running the compiler
```bash
./pluto [directory]    # Compiles .pt and .spt files in directory
```
This will compile all `.pt` and `.spt` files in the specified directory and generate executables in the same directory.

### Debugging and Development
```bash
# Check for compilation errors with verbose output (quick smoke check)
./pluto tests/

# Clear cache when debugging compilation issues
rm -rf "$HOME/Library/Caches/pluto"  # macOS
rm -rf "$HOME/.cache/pluto"          # Linux
rd /s /q %LocalAppData%\pluto        # Windows

# Run integration tests for specific directory
python3 test.py tests/math
```

## Project Architecture

Pluto is a compiled programming language that generates LLVM IR and produces native executables. The language supports two file types:
- `.pt` files: "Code" files containing reusable functions and constants
- `.spt` files: "Script" files containing executable programs

### Core Components

#### Lexer (`lexer/`)
- Tokenizes Pluto source code with support for indentation-based syntax
- Handles UTF-8 input and tracks line/column positions for error reporting

#### Parser (`parser/`)
- Recursive descent parser that builds an Abstract Syntax Tree (AST)
- Supports both code parsing (`.pt` files) and script parsing (`.spt` files)
- Implements operator precedence for expressions

#### AST (`ast/`)
- Defines all AST node types including statements and expressions
- Supports range literals (`start:stop:step`), function calls, and infix/prefix expressions
- Code modules contain constants and functions that can be merged across files

#### Compiler (`compiler/`)
- Two-phase compilation: CodeCompiler for `.pt` files, ScriptCompiler for `.spt` files
- Generates LLVM IR with type checking and symbol resolution
- Supports ranges, functions, and mathematical operations
- Includes a CFG-based type solver for type inference

#### Runtime (`runtime/`)
- C runtime embedded in the binary for low-level operations
- Linked with generated object code to produce final executables

### Compilation Process

The compilation process consists of two main phases:

1. **Code Compilation**: All `.pt` files in the target directory are compiled into a single LLVM IR module. These files are intended for reusable functions and constants.
2. **Script Compilation**: For each `.spt` file, the compiler performs the following steps:
   - Links the code module (from the `.pt` files) into the script's module
   - Generates LLVM IR for the script
   - Optimizes the IR using `opt -O3`
   - Compiles the optimized IR into an object file using `llc`
   - Links the object file with the C runtime to create a native executable

**Module resolution**: Walks up to find `pt.mod` file to determine project root and module path; cache key based on module path.

### File Extensions and Structure
- `.pt`: Code files (functions, constants)
- `.spt`: Script files (executable programs) 
- `.exp`: Expected output files for tests
- `pt.mod`: Module declaration file (similar to `go.mod`)

### Testing Infrastructure
- Unit tests live under each package; run with `go test -race`
- End-to-end tests live in `tests/`:
  - Inputs: `.spt` (scripts) and optional `.pt` (shared code)
  - Expected output: `.exp` (line-by-line, supports `re:` regex prefixes)
- Python-based test runner (`test.py`) with colorized output
- Integration tests compare actual vs expected program output
- Run: `python3 test.py [--keep]` or focused run: `python3 test.py tests/math`

CI: GitHub Actions builds with Go 1.25, installs LLVM 21, and runs `python3 test.py` on pushes/PRs.

### Cache System
- Uses `PTCACHE` environment variable or platform-specific cache directories
- If `PTCACHE` is not set, it defaults to:
  - macOS: `$HOME/Library/Caches/pluto`
  - Linux: `$HOME/.cache/pluto`
  - Windows: `%LocalAppData%\pluto`
- Cache layout: `<PTCACHE>/<module-path>/{code,script}` stores intermediate LLVM IR files and objects
- `PTCACHE` overrides cache location; ensure PATH includes LLVM 21 tools

## Coding Style & Naming Conventions
- Indentation: Use tabs for indentation across the repository; do not convert leading tabs to spaces. Preserve existing indentation when editing.
- Go files: Leading indentation MUST be tabs (this is gofmt's default). Run `gofmt -w` (or enable format‑on‑save) before committing. It's fine for gofmt to leave spaces for alignment within a line; the rule applies to leading indentation only.
- Go formatting: `go fmt ./...`; basic checks: `go vet ./...`
- Packages: lowercase short names. Exports: `CamelCase`. Tests: `*_test.go` with `TestXxx` functions
- Filenames: lowercase with underscores where needed (Go convention)

## Commit & Pull Request Guidelines
- Commit style: Conventional Commits (e.g., `feat(parser): ...`, `refactor(compiler): ...`)
- PRs: include a clear description, linked issues, unit/E2E tests for changes, and sample before/after output where relevant

## Instructions for AI Assistants
- Keep changes minimal and focused; avoid reflowing or reindenting unrelated lines
- Use tabs for indentation (preserve existing indentation style)
- NEVER add "🤖 Generated with [Claude Code]..." footer to git commits
- Do what has been asked; nothing more, nothing less
- NEVER create files unless they're absolutely necessary for achieving your goal
- ALWAYS prefer editing an existing file to creating a new one
- NEVER proactively create documentation files (*.md) or README files. Only create documentation files if explicitly requested by the User