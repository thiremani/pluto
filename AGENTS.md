# Repository Guidelines

## Project Structure & Module Organization
- `main.go`: CLI entry; scans the working directory for `.pt` (code) and `.spt` (script) and emits native binaries.
- `ast`, `lexer`, `parser`: Front‑end for the Pluto language.
- `compiler`: Type solving, IR generation, and code emission (LLVM).
- `runtime`: Embedded C runtime linked into final executables.
- `tests`: End‑to‑end tests with `.spt` plus `.exp` expected outputs.
- `pt.mod`: Module declaration at the repo root.

## Build, Test, and Development Commands

- Build compiler: `python3 build.py`
- Production build with version: `python3 build.py --release`
- Unit tests (race): `go test -race ./lexer ./parser ./compiler`
- Full suite: `python3 test.py` (builds compiler, runs unit and integration tests)
- Full suite with leak detection: `python3 test.py --leak-check`
- Run compiler: `./pluto [directory]` (writes binaries next to sources)
- Run compiler and keep linked pre-optimization script IR: `./pluto -emit-ir [directory]`
- Show version: `./pluto -version` (or `-v`)
- Clear cache: `./pluto -clean` (or `-c`, clears cache for current version)

Requirements: Go `1.26`, LLVM `22` development libraries and tools on PATH (`llvm-config`, `clang`). macOS Homebrew paths: `/opt/homebrew/opt/llvm/bin` (ARM) or `/usr/local/opt/llvm/bin` (Intel).
`python3 build.py` and `python3 test.py` derive the LLVM 22 byollvm CGO flags from `llvm-config`; direct `go build`/`go test` can use `eval "$(python3 scripts/llvm_env.py --shell)"`. See `README.md`.
`PLUTO_TARGET_CPU` defaults to `native`; set it to a CPU name or `portable` to override host CPU tuning.

## Architecture Overview
- Two phases: CodeCompiler for `.pt` (reusable funcs/consts) → IR; ScriptCompiler for `.spt` (programs) links code IR.
- Pipeline: generate IR → optimize `-O3` and emit objects in-process with LLVM → link with cached runtime objects via `clang`.
- Module resolution: walks up to find `pt.mod`; cache key based on module path.
- Cache layout (versioned to isolate different compiler versions):
  - `<PTCACHE>/<version>/runtime/<hash>/` for compiled runtime objects
  - Default host CPU builds: `<PTCACHE>/<version>/<module-path>/{code,script}`
  - Non-default `PLUTO_TARGET_CPU` builds: `<PTCACHE>/<version>/target_cpu-<setting>/<module-path>/{code,script}`

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

CI: GitHub Actions builds with Go 1.26, installs LLVM 22 + valgrind, and runs `python3 test.py --leak-check` on pushes/PRs.

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

## Debugging & Configuration Tips
- Quick smoke check: `./pluto tests/` to see compile/link output.
- Clear cache for current version: `./pluto -clean`
- Clear entire cache manually:
  - macOS: `rm -rf "$HOME/Library/Caches/pluto"`
  - Linux: `rm -rf "$HOME/.cache/pluto"`
  - Windows: `rd /s /q %LocalAppData%\pluto`
- `PTCACHE` overrides cache location; ensure PATH includes LLVM 22 `llvm-config` and `clang`.
- `PLUTO_TARGET_CPU` overrides host CPU tuning; set it to `portable` to disable the default `-mcpu=native`.

## Instructions for AI Assistants
- Keep changes minimal and focused; avoid reflowing or reindenting unrelated lines.
- Use tabs for indentation (preserve existing indentation style).
- NEVER add "🤖 Generated with..." footers to git commits.
