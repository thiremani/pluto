The Pluto Programming Language
==============================

*Taking programming to another planet 🚀*

Pluto is a compiled language that aims for the readability of scripting languages with the safety and performance of C/Go. A Go front‑end lowers `.pt` (code) and `.spt` (script) to LLVM 20 IR and emits native binaries. Design highlights include range‑driven auto‑vectorization, safe arrays and slices, scope‑based memory (no nulls, no out‑of‑bounds, no GC), and concurrency by construction.

Highlights
----------
- Go front‑end, LLVM 20 back‑end; emits native binaries
- Range literals with auto‑vectorized execution
- First‑class arrays (safe slicing) and link semantics
- Scope‑based memory: no nulls, no OOB, no garbage collector
- printf‑style formatting; arrays/ranges printable
- Cross‑platform; GitHub Actions CI (Go 1.25 + LLVM 20)

Examples
--------

Hello world (`tests/helloworld.spt`):

```
"hello world"
x = "hello 🙏"
x, "👍"
```

Run it (Pluto compiles directories, not single files):

```
# compile all scripts in the tests/ folder
./pluto tests
# run the produced binary for helloworld.spt
./tests/helloworld
```

Code + script (`tests/math`):

`tests/math/math.pt`
```
res = square(x)
	res = x * x
```

`tests/math/func.spt`
```
y = square(2)
z = square(5.6)
y
z
```

Run it:

```
./pluto tests/math
./tests/math/func
```

Quick Start
-----------
- Build compiler: `go build -o pluto main.go`
- Run unit tests (race): `go test -race ./lexer ./parser ./compiler`
- Run full suite: `python3 test.py`
- New project setup: add a `pt.mod` at your repo root to define the module path (project root). Minimal example:
  - `pt.mod` first non‑comment line: `module github.com/you/yourproject`
  - Pluto walks up from the working directory to find `pt.mod` and treats that directory as the project root.
- Compile a directory: `./pluto [directory]` (writes binaries next to sources)

Requirements
------------
- Go `1.25` on PATH
- LLVM `20` tools on PATH: `clang`, `opt`, `llc`, `ld.lld`
- macOS Homebrew paths:
  - Apple Silicon: `export PATH=/opt/homebrew/opt/llvm@20/bin:$PATH`
  - Intel: `export PATH=/usr/local/opt/llvm@20/bin:$PATH`

Build and Run
-------------

Linux/macOS (primary)
- Requirements: Go 1.25+ and LLVM 20 (`opt`, `llc`, `clang`, `ld.lld`)
- Linux install and set PATH:
  - See https://apt.llvm.org for instructions. Typical flow:
    - `wget https://apt.llvm.org/llvm.sh && chmod +x llvm.sh && sudo ./llvm.sh 20`
    - `sudo apt install lld-20`
    - `export PATH=/usr/lib/llvm-20/bin:$PATH`
- macOS (Homebrew):
  - `brew install llvm@20`
  - Intel: `export PATH=/usr/local/opt/llvm@20/bin:$PATH`
  - Apple Silicon: `export PATH=/opt/homebrew/opt/llvm@20/bin:$PATH`
- Build & test end‑to‑end:
  - `python3 test.py`
- Unit tests only:
  - `go test -race ./lexer ./parser ./compiler`

Windows (MSYS2 UCRT64)
----------------------
- Install MSYS2 and use the "MSYS2 UCRT64" shell: https://www.msys2.org
- Packages: `pacman -S --needed mingw-w64-ucrt-x86_64-{go,llvm,clang,lld,python}`
- Quick build: `python scripts/msys2_build.py` → creates `pluto.exe`
- Tests: `python test.py` (or `python test.py tests/math` for a subset)
- Manual build/test: ensure `clang`, `lld`, and LLVM 20 libs are active in the UCRT64 env; for BYO LLVM builds you may need `GOFLAGS='-tags=byollvm'` and `CGO_*` flags from `llvm-config`.
 - Verify LLVM 20 tools are present (e.g., `C:\\msys64\\ucrt64\\bin`).
 - The Windows runner automatically applies the correct MSYS2 environment (CGO + LLVM) so `go build` and `go test` work consistently.

Manual go build/test (MSYS2)
- If you want to run `go build` or `go test` yourself in the UCRT64 shell, set:
  - `export CGO_ENABLED=1`
  - `export CC=clang CXX=clang++`
  - `export GOFLAGS='-tags=byollvm'`
  - `export CGO_CPPFLAGS="$(llvm-config --cflags) -D_GNU_SOURCE -D__STDC_CONSTANT_MACROS -D__STDC_FORMAT_MACROS -D__STDC_LIMIT_MACROS"`
  - `export CGO_CXXFLAGS="-std=c++17 $(llvm-config --cxxflags)"`
  - `export CGO_LDFLAGS="$(llvm-config --ldflags --libs all --system-libs)"`
  - `export PATH="/ucrt64/bin:$PATH"`  # already set by the UCRT64 shell
  - Now: `go test -race ./compiler` or `go build -o pluto.exe main.go`.

Repository Structure
--------------------
- `main.go`: CLI entry; scans working dir for `.pt` and `.spt`, emits binaries
- `ast`, `lexer`, `parser`: Front‑end for the Pluto language
- `compiler`: Type solving, IR generation, and LLVM emission
- `runtime`: Embedded C runtime linked into final executables
- `tests`: End‑to‑end tests (`.spt` inputs with `.exp` expected outputs)
- `pt.mod`: Module declaration at the repo root

Architecture
------------
- Two phases: CodeCompiler for `.pt` (reusable funcs/consts) → IR; ScriptCompiler for `.spt` (programs) links code IR.
- Pipeline: generate IR → optimize `-O3` via `opt` → object via `llc` → link with runtime via `clang`/`lld`.
- Module resolution: walks up from CWD to find `pt.mod` and derives module path.
- Cache layout: `<PTCACHE>/<module-path>/{code,script}` stores IR/objects.

Usage
-----
- From a directory with `.pt`/`.spt`: `./pluto` (or `./pluto path/to/dir`)
- Outputs a native binary for each `.spt` next to the source file.

Testing
-------
- Unit tests: `go test -race ./lexer ./parser ./compiler`
- E2E: `python3 test.py` builds the compiler, then compiles/executes tests under `tests/` and compares output to `.exp`.
- Focus a subset: `python3 test.py tests/math`

Troubleshooting
---------------
- `undefined: run_build_sh` during `go build/test` (Windows):
  - Ensure `GOFLAGS='-tags=byollvm'` and CGO flags from `llvm-config` are set (the MSYS2 runner and `test.py` in MSYS2 set these automatically).
- Encoding issues / mojibake in test output (Windows):
  - Run from the MSYS2 UCRT64 shell; the runner decodes output as UTF‑8.
- Missing LLVM tools:
  - Verify `opt`, `llc`, `clang`, `ld.lld` from LLVM 20 are on PATH.
- Clear Pluto cache if behavior seems stale:
  - macOS: `rm -rf "$HOME/Library/Caches/pluto"`
  - Linux: `rm -rf "$HOME/.cache/pluto"` (or `XDG_CACHE_HOME`)
  - Windows: `rd /s /q %LocalAppData%\pluto`
- Override cache location with `PTCACHE` env var.

Notes
-----
- On Windows the produced binary is `pluto.exe`.
- The compiler shells out to LLVM tools (`opt`, `llc`, `clang`, `ld.lld`).
- The runtime enables `%n` on UCRT to match POSIX `printf` behavior.

Coding Style
------------
- Go formatting: `go fmt ./...`; vet: `go vet ./...`
- Indentation: tabs for leading indentation across the repo (default gofmt behavior).
- Packages: lowercase short names; exports in CamelCase; tests in `*_test.go`.

Contributing
------------
- Conventional Commits (e.g., `feat(parser): ...`, `refactor(compiler): ...`).
- PRs: include a clear description, linked issues, and unit/E2E tests for changes.

Further Reading
---------------
- Language overview and design notes: see `CLAUDE.md` for the philosophy (no nulls, no OOB, vectorization, conditional statements, concurrency model, arrays/links, etc.).

License
-------
- Pluto is licensed under the MIT License. See `LICENSE`.
- Vendored third‑party: `runtime/third_party/klib/` (klib by Attractive Chaos) is MIT‑licensed; its license is preserved in `runtime/third_party/klib/LICENSE.txt`.
