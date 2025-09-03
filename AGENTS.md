# Repository Guidelines

## Project Structure & Module Organization
- `main.go`: CLI entry; scans the working directory for `.pt` (code) and `.spt` (script) and emits native binaries.
- `ast`, `lexer`, `parser`: Front‑end for the Pluto language.
- `compiler`: Type solving, IR generation, and code emission (LLVM).
- `runtime`: Embedded C runtime linked into final executables.
- `tests`: End‑to‑end tests with `.spt` plus `.exp` expected outputs.
- `pt.mod`: Module declaration at the repo root.

## Build, Test, and Development Commands
- Build compiler: `go build -o pluto main.go`
- Unit tests (race): `go test -race ./lexer ./parser ./compiler`
- Full suite: `./test.sh` (builds compiler, runs unit and integration tests)
- Python test runner: `python3 test.py` (use `--keep` to retain artifacts)
- Run compiler: `./pluto [directory]` (writes binaries next to sources).

Requirements: Go `1.24`, LLVM `20` on PATH (`clang`, `opt`, `llc`, `ld.lld`). macOS Homebrew paths: `/opt/homebrew/opt/llvm@20/bin` (ARM) or `/usr/local/opt/llvm@20/bin` (Intel).

## Architecture Overview
- Two phases: CodeCompiler for `.pt` (reusable funcs/consts) → IR; ScriptCompiler for `.spt` (programs) links code IR.
- Pipeline: generate IR → optimize `-O3` (`opt`) → object (`llc`) → link with runtime via `clang`/`lld`.
- Module resolution: walks up to find `pt.mod`; cache key based on module path.
- Cache layout: `<PTCACHE>/<module-path>/{code,script}` stores IR/objects.

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
  - Run: `./test.sh` or `python3 test.py [--keep]`.
  - Focused run: `python3 test.py tests/math`.

CI: GitHub Actions builds with Go 1.24, installs LLVM 20, and runs `./test.sh` on pushes/PRs.

## Commit & Pull Request Guidelines
- Commit style: Conventional Commits (e.g., `feat(parser): ...`, `refactor(compiler): ...`).
- PRs: include a clear description, linked issues, unit/E2E tests for changes, and sample before/after output where relevant.

## Debugging & Configuration Tips
- Quick smoke check: `./pluto tests/` to see compile/link output.
- Clear cache if behavior seems stale:
  - macOS: `rm -rf "$HOME/Library/Caches/pluto"`
  - Linux: `rm -rf "$HOME/.cache/pluto"`
  - Windows: `rd /s /q %LocalAppData%\pluto`
- `PTCACHE` overrides cache location; ensure PATH includes LLVM 20 tools.

## Instructions for AI Assistants
- Keep changes minimal and focused; avoid reflowing or reindenting unrelated lines.
- Use tabs for indentation (preserve existing indentation style).
- NEVER add "🤖 Generated with..." footers to git commits.

### Local Git Hooks
- To enforce gofmt (tabs for leading indentation) on push, enable repo-local hooks:
  - `git config --local core.hooksPath .githooks`
  - The `.githooks/pre-push` script blocks pushes if `gofmt -l` reports issues.
