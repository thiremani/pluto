# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Development Commands

### Building the compiler
```bash
go build -o pluto main.go
```

### Running tests
```bash
# Full test suite (builds compiler, runs unit tests, runs integration tests)
./test.sh

# Python test runner with options (auto-installs dependencies if missing)
python test.py              # Run all tests
python test.py --keep       # Keep build artifacts for debugging
```

### Running unit tests only
```bash
go test -race ./lexer
go test -race ./parser  
go test -race ./compiler
```

### Running the compiler
```bash
./pluto [directory]    # Compiles .pt and .spt files in directory
```

### Debugging and Development
```bash
# Check for compilation errors with verbose output
./pluto tests/

# Clear cache when debugging compilation issues
rm -rf $HOME/Library/Caches/pluto  # macOS
rm -rf $HOME/.cache/pluto          # Linux
rm -rf %LocalAppData%/pluto        # Windows

# Run integration tests for specific directory
python test.py tests/math
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

1. **Module resolution**: Finds `pt.mod` file to determine project root and module path
2. **Code compilation**: Compiles all `.pt` files in directory to LLVM IR
3. **Script compilation**: For each `.spt` file:
   - Links with code module
   - Generates LLVM IR  
   - Optimizes with `-O3`
   - Compiles to object file with `llc`
   - Links with runtime to create executable

### File Extensions and Structure
- `.pt`: Code files (functions, constants)
- `.spt`: Script files (executable programs) 
- `.exp`: Expected output files for tests
- `pt.mod`: Module declaration file (similar to `go.mod`)

### Dependencies
- Go 1.24+
- LLVM 20 (required for IR generation and optimization)
- clang, opt, llc tools for compilation pipeline
- lld linker for final executable generation

### Testing Infrastructure
- Python-based test runner (`test.py`) with colorized output
- Integration tests compare actual vs expected program output
- Supports regex patterns in `.exp` files with `re:` prefix
- Unit tests for individual components using Go's testing framework

### Cache System
- Uses `PTCACHE` environment variable or platform-specific cache directories
- Stores intermediate LLVM IR files organized by module path
- Separate `code/` and `script/` subdirectories for different compilation phases