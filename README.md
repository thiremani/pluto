# The Pluto Programming Language

*Taking programming to another planet 🚀*

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/pluto-transparent-dark.png" />
    <source media="(prefers-color-scheme: light)" srcset="assets/pluto-transparent-light.png" />
    <img src="assets/pluto-transparent-light.png" alt="Pluto language logo" width="460" />
  </picture>
</p>

Pluto is a compiled language that combines the clarity of scripting languages with the type-safety, performance, and determinism of systems languages.

Functions in Pluto are **templates** defined in `.pt` files. Call a template with different argument types and Pluto generates specialized native code for each — no generics syntax, no runtime dispatch. Write concise programs that compile into efficient, fully specialized executables.

## Philosophy

- 💎 Purity by design — functions have read-only inputs and writable outputs; structs contain raw data with no hidden pointers
- ✨ The simplicity of scripting
- ⚡ The type safety and speed of low-level languages
- 🧠 Deterministic memory
- 🔧 Automatic specialization
- 🔒 Predictable concurrency

Intended for performance-sensitive scripting, numerical work, simulation, and systems tools.

---

**Design highlights:**

**Purity:** Functions have read-only inputs and writable outputs; structs are raw data with no hidden pointers.

Range-driven auto-vectorization, safe arrays and slices.

Scope-based memory (no nulls, no out-of-bounds, no GC), and concurrency by construction.

---

## Highlights

- Go front-end, LLVM back-end; emits native binaries
- Template functions in `.pt`: specialized per argument types (generics by use)
- Range literals with auto-vectorized execution
- First-class arrays (safe slicing) and link semantics
- Scope-based memory: no nulls, no out-of-bounds, no garbage collector
- printf-style formatting; arrays/ranges printable
- Cross-platform (Linux/macOS/Windows)

## Command name

Release artifacts ship both command names:

- `pluto`
- `plt` (conflict-safe alias)

If `pluto` conflicts on your system, use `plt`.

---

## Quick example

Hello world (`tests/helloworld.spt`):

```python
"hello world"
x = "hello 🙏"
x, "👍"
```

No `main()`, no `print()`, no imports. Values on a line get printed.

Compile and run:

```sh
./pluto tests/helloworld.spt
./tests/helloworld
```

Output:

```text
hello world
hello 🙏 👍
```

Pluto can compile a single `.spt` file or an entire directory — each `.spt` produces a native executable.

---

## How Pluto programs are structured

Pluto separates reusable templates from executable scripts:

| Extension | Purpose                                                                      |
|-----------|------------------------------------------------------------------------------|
| `.pt`     | Template files — functions, operators, structs, constants (act as libraries) |
| `.spt`    | Script files — executable programs that compile to native binaries           |

A directory is the unit of compilation:

```text
math/
├── math.pt      # defines Square, etc.
└── func.spt     # uses them → compiles to ./math/func
```

Compile and run:

```sh
./pluto math
./math/func
```

---

## Templates and specialization

Templates are defined once with a clear input/output contract. The first line declares the output and input — the indented body describes the transformation.

Think of a template as a **black box**: data flows in through inputs, gets transformed, and flows out through outputs. Outputs work **by reference** — calling a template directly modifies the output variable in the caller's scope.

`math.pt`
```python
# y is the output (writable), x is the input (read-only)
y = Square(x)
	y = x * x
```

Inputs are read-only — they flow in. Outputs are writable — they flow out. Every function is a transformation.

### Generics by use

Call `Square` with different types — Pluto compiles a specialized version for each:

`func.spt`
```python
arr = [1 2 3 5]

# Basic specializations
a = Square(5)            # int specialization
b = Square(2.2)          # float specialization
c = Square(arr)          # squares each array element
a, b, c

# Range and filter (non-accumulated)
d = Square(1:3)          # range: final iteration result
e = Square(arr[1:3])     # array-range: final iteration result
f = Square(arr > 3)      # filtered array
g = Square(arr[1:3] > 3) # filtered array-range
d, e, f, g

# Accumulation forms
h = [Square(1:3)]          # range accumulation
i = [Square(arr[1:3])]     # array-range accumulation
j = [Square((1:3) > 1)]    # conditional range accumulation
k = [Square(arr[1:4] > 2)] # conditional array-range accumulation
h, i, j, k
```

Output:

```text
25 4.84 [1 4 9 25]
4 9 [25] 0
[1 4] [4 9] [0 4] [0 9 25]
```

No generics syntax, no type parameters. Write the template once — call it with a scalar, an array, or a filtered view. `arr > 3` filters the array to elements greater than 3 and passes that subset through.

Generated code is equivalent to handwritten specialized code. There is no runtime overhead.

---

## Ranges as the execution primitive

Ranges are first-class values:

```python
Square(1:5)
```

Passing a range to a template executes it across all values. The compiler can map these operations to SIMD instructions — this is range-driven auto-vectorization.

Data-parallel execution without explicit loop syntax.

---

## Arrays

Arrays use bracket syntax with no commas:

```python
x = [1 2 3 4 5]
y = [1.1 2.2 3.3]
```

Arrays are safe by construction — out-of-bounds access is not possible. Comparisons like `arr > 2` produce filtered views that work anywhere an array does.

---

## Function model

Functions are pure transformations:

- Inputs are read-only
- Outputs are writable
- Global constants allowed
- No hidden mutation

This keeps behavior predictable and easier to parallelize.

---

## Memory model

Pluto uses deterministic, scope-based memory:

- No garbage collector
- No null values
- No out-of-bounds access
- Memory freed when scope ends

Predictable performance with minimal runtime overhead.

---

## Concurrency direction

Pluto is designed so that:

- Structs are pure data
- Functions are transformations
- Inputs are read-only

This enables strong guarantees around race-free execution. The concurrency model is evolving but built around deterministic behavior.

---

## Building and Running

**Build the compiler:**

```bash
go build -o pluto
```

If you prefer the conflict-safe command name locally:

```bash
go build -o plt
```

**Compile a directory:**

```bash
./pluto tests           # Compiles all .spt files in tests/
./tests/helloworld      # Run the compiled executable
```

**Run tests:**

```bash
python3 test.py                              # Full E2E suite
python3 test.py --leak-check                 # Full suite + memory leak detection
go test -race ./lexer ./parser ./compiler    # Unit tests only
```

**New project setup:**

Add a `pt.mod` file at your project root to define the module path:

```
module github.com/you/yourproject
```

Pluto walks up from the working directory to find `pt.mod` and treats that directory as the project root.

**Other commands:**

- `./pluto --version` (or `-v`) — show version
- `./pluto --clean` (or `-c`) — clear cache for current version

---

## Requirements

- Go 1.25+
- LLVM 21 tools on PATH: `clang`, `opt`, `llc`, `ld.lld`
- Python 3.x (for running tests)
- Leak check tools (only for `python3 test.py --leak-check`):
  - Linux: `valgrind`
  - macOS: `leaks`

---

## Installation

<details>
<summary><strong>Linux</strong></summary>

Install LLVM 21 from apt.llvm.org:

```bash
wget https://apt.llvm.org/llvm.sh && chmod +x llvm.sh && sudo ./llvm.sh 21
sudo apt install lld-21
export PATH=/usr/lib/llvm-21/bin:$PATH
```

Build and test:

```bash
go build -o pluto
python3 test.py
```

</details>

<details>
<summary><strong>macOS (Homebrew)</strong></summary>

```bash
brew install llvm
```

Set PATH for your architecture:

```bash
# Apple Silicon
export PATH=/opt/homebrew/opt/llvm/bin:$PATH

# Intel
export PATH=/usr/local/opt/llvm/bin:$PATH
```

Build and test:

```bash
go build -o pluto
python3 test.py
```

</details>

<details>
<summary><strong>Windows (MSYS2 UCRT64)</strong></summary>

Install [MSYS2](https://www.msys2.org) and use the "MSYS2 UCRT64" shell.

Install packages:

```bash
pacman -S --needed mingw-w64-ucrt-x86_64-{go,llvm,clang,lld,python}
```

Quick build:

```bash
python scripts/msys2_build.py
```

Or manual build with required environment:

```bash
export CGO_ENABLED=1
export CC=clang CXX=clang++
export GOFLAGS='-tags=byollvm'
export CGO_CPPFLAGS="$(llvm-config --cflags) -D_GNU_SOURCE -D__STDC_CONSTANT_MACROS -D__STDC_FORMAT_MACROS -D__STDC_LIMIT_MACROS"
export CGO_CXXFLAGS="-std=c++17 $(llvm-config --cxxflags)"
export CGO_LDFLAGS="$(llvm-config --ldflags --libs all --system-libs)"
go build -o pluto.exe
```

Run tests:

```bash
python test.py
```

Notes:

- On Windows the produced binary is `pluto.exe`.
- The runtime enables `%n` on UCRT to match POSIX `printf` behavior.

</details>

---

## Status

Pluto is under active development.

**Working today:**

- 🎯 Template specialization
- 📊 Arrays and ranges
- 🖥️ Native binaries
- 🧞‍♂️ Cross-platform builds

**In progress:**

- 🧵 Concurrency model
- 🧰 Tooling
- 📘 Documentation

---

## Repository structure

| Path | Description |
|------|-------------|
| `main.go` | CLI entry point |
| `ast/` | Abstract syntax tree definitions |
| `lexer/` | Tokenizer with indentation-based syntax |
| `parser/` | Recursive descent parser |
| `compiler/` | Type solving, IR generation, LLVM emission |
| `runtime/` | Embedded C runtime linked into executables |
| `tests/` | End-to-end tests (`.spt` inputs, `.exp` expected outputs) |

---

## Architecture

- **Two phases:** CodeCompiler parses `.pt` files and saves function templates and constants; ScriptCompiler compiles specialized versions for each usage in `.spt` files (if not already cached).
- **Automatic pipeline:** generate IR → optimize `-O3` via `opt` → object via `llc` → link with runtime via `clang`/`lld` — all handled transparently.
- **Module resolution:** walks up from CWD to find `pt.mod` and derives module path.
- **Cache layout** (versioned to isolate different compiler versions):
  - `<PTCACHE>/<version>/runtime/<hash>/` for compiled runtime objects
  - `<PTCACHE>/<version>/<module-path>/{code,script}` for IR/objects

---

## Troubleshooting

<details>
<summary><strong>Common issues</strong></summary>

**`undefined: run_build_sh` during build (Windows):**
- Ensure `GOFLAGS='-tags=byollvm'` and CGO flags are set (see Windows installation above)

**Missing LLVM tools:**
- Verify `opt`, `llc`, `clang`, `ld.lld` from LLVM 21 are on PATH

**Stale cache behavior:**
- Clear current version: `./pluto --clean`
- Clear all versions:
  - macOS: `rm -rf "$HOME/Library/Caches/pluto"`
  - Linux: `rm -rf "$HOME/.cache/pluto"`
  - Windows: `rd /s /q %LocalAppData%\pluto`
- Override cache location with `PTCACHE` environment variable

**Encoding issues on Windows:**
- Run from the MSYS2 UCRT64 shell; the runner decodes output as UTF-8

</details>

---

## Contributing

- Commit style: [Conventional Commits](https://www.conventionalcommits.org/) (e.g., `feat(parser): ...`, `fix(compiler): ...`)
- PRs: include a clear description, linked issues, and tests for changes
- Go formatting: `go fmt ./...`
- Run tests before submitting: `python3 test.py`

---

## License

Pluto is licensed under the MIT License. See [LICENSE](LICENSE).

**Vendored third-party code:**
- `runtime/third_party/klib/` (klib by Attractive Chaos) — MIT License, see `runtime/third_party/klib/LICENSE.txt`
