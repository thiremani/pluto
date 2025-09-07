pluto
===== 

This project builds a native compiler that shells out to LLVM 20 tools. Below
are quick start instructions to build and run on the primary development
environments.

Build and Run
-------------

**Linux/macOS (primary)**
- Requirements: Go 1.24+ and LLVM 20 (tools: opt, llc, clang, ld.lld)
- Install and set PATH:
  - Linux:
    - See https://apt.llvm.org for instructions. Typical flow:
      - wget https://apt.llvm.org/llvm.sh && chmod +x llvm.sh && sudo ./llvm.sh 20
      - sudo apt install lld-20
      - export PATH=/usr/lib/llvm-20/bin:$PATH
  - macOS (Homebrew):
    - brew install llvm@20
    - Intel: export PATH=/usr/local/opt/llvm@20/bin:$PATH
    - Apple Silicon: export PATH=/opt/homebrew/opt/llvm@20/bin:$PATH
- Build & test end-to-end:
  - python3 test.py
- Unit tests only:
  - go test -race ./lexer ./parser ./compiler

**Windows (MSYS2 UCRT64)**
- Install MSYS2 (https://www.msys2.org) and open the "MSYS2 UCRT64" shell.
- Update and install toolchain packages:
  - pacman -Syu
  - pacman -S --needed mingw-w64-ucrt-x86_64-{go,llvm,clang,lld,python}
- Quick build (creates `pluto.exe` in repo root):
  - python scripts/msys2_build.py
- Unit + E2E tests:
  - python test.py
- Focused directory:
  - python test.py tests/math

The Windows runner automatically applies the correct MSYS2 environment (CGO +
LLVM) so `go build` and `go test` work consistently.

**Windows (MSYS2 UCRT64)**
- Install MSYS2 (https://www.msys2.org) and open the “MSYS2 UCRT64” shell.
- Update and install toolchain packages:
  - pacman -Syu
  - pacman -S --needed mingw-w64-ucrt-x86_64-{go,llvm,clang,lld,python}
- Verify LLVM 20 tools are present (in `C:\msys64\ucrt64\bin`).

**Manual go build/test (MSYS2)**
- If you want to run `go build` or `go test` yourself in the MSYS2 shell,
  set the environment as follows (simplified):
  - export CGO_ENABLED=1
  - export CC=clang CXX=clang++
  - export GOFLAGS='-tags=byollvm'
  - export CGO_CPPFLAGS="$(llvm-config --cflags) -D_GNU_SOURCE -D__STDC_CONSTANT_MACROS -D__STDC_FORMAT_MACROS -D__STDC_LIMIT_MACROS"
  - export CGO_CXXFLAGS="-std=c++17 $(llvm-config --cxxflags)"
  - export CGO_LDFLAGS="$(llvm-config --ldflags --libs all --system-libs)"
  - export PATH="/ucrt64/bin:$PATH"   # already set by the UCRT64 shell
  Now: `go test -race ./compiler` or `go build -o pluto.exe main.go`.


**Notes**
- On Windows the produced binary is `pluto.exe`.
- The compiler links with LLVM tools externally (opt/llc/clang/ld.lld).
- The runtime enables `%n` on UCRT to match POSIX printf behavior.

**Troubleshooting**
- `undefined: run_build_sh` during `go build/test`:
  - Ensure `GOFLAGS='-tags=byollvm'` and CGO flags from `llvm-config` are set
    (the MSYS2 runner and `test.py` in MSYS2 set these automatically).
- Encoding issues / mojibake in test output:
  - Run from the MSYS2 UCRT64 shell; the runner decodes output as UTF‑8.
- Missing LLVM tools:
  - Verify `opt`, `llc`, `clang`, `ld.lld` are in PATH (LLVM 20).
