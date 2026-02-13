# Releasing Pluto with GoReleaser

This repository now uses GoReleaser via `.goreleaser.yaml` and `.github/workflows/release.yaml`.

## What this setup does

- Creates a GitHub release when a tag like `v0.5.0` is pushed.
- Builds Pluto natively on each target OS (no CGO cross-compiling):
  - Linux `x86_64` (`amd64`)
  - Linux `arm64`
  - macOS `arm64`
  - macOS `x86_64` (`amd64`)
  - Windows `x86_64` (`amd64`)
- Ships both command names in release artifacts:
  - `pluto`
  - `plt` (conflict-safe alias)
- Injects version metadata into the binary:
  - `main.Version`
  - `main.Commit`
  - `main.BuildDate`
- Produces OS archives and per-OS checksums (`checksums_<os>_<arch>.txt`).
- Produces Linux package artifacts (`.deb`, `.rpm`, `.apk`) from the Linux job.
- Creates the release as a draft.

## Why native-per-OS jobs are used

Pluto depends on LLVM and CGO (`tinygo.org/x/go-llvm`). Cross-compiling all targets from a single runner is fragile.

The release workflow now:

- runs a smoke gate (`go test -race ./lexer ./parser ./compiler`) on the tagged commit
- ensures the draft release exists first
- fans out native release jobs in parallel (macOS arm64/amd64, Linux amd64/arm64, Windows amd64)

## Cut a release

1. Ensure `master` is green.
2. Tag and push:

```bash
git tag v0.5.0
git push origin v0.5.0
```

3. Wait for `.github/workflows/release.yaml` to finish.
4. Open the generated draft release on GitHub.
5. Verify assets and publish the draft.

## Package managers

The GoReleaser config already includes scaffolded package-manager sections for:

- Homebrew Cask (tap repository)
- Scoop (bucket repository)
- Winget (manifests repository)
- Chocolatey
- Linux packages via `nfpms` (`.deb`, `.rpm`, `.apk`)

Linux `nfpms` entries also declare LLVM/Clang/LLD dependencies with format-specific overrides (`deb` vs `rpm`/`apk` naming).

In the release workflow, Homebrew/Scoop/Winget/Chocolatey publishing is currently skipped so release jobs stay deterministic with split per-OS builds.

## Inputs needed to enable manager publishing

Provide these values/secrets when you want to enable publishing:

- Homebrew tap:
  - `HOMEBREW_TAP_OWNER`
  - `HOMEBREW_TAP_REPO`
  - Token with push access to that tap repo
- Scoop bucket:
  - `SCOOP_BUCKET_OWNER`
  - `SCOOP_BUCKET_REPO`
  - Token with push access to that bucket repo
- Winget manifests:
  - `WINGET_PACKAGE_IDENTIFIER` (example: `PlutoLang.Pluto`)
  - `WINGET_PUBLISHER` (display publisher name)
  - `WINGET_REPO_OWNER`
  - `WINGET_REPO_NAME`
  - Token with push access
- Chocolatey:
  - `CHOCOLATEY_API_KEY`
  - `CHOCOLATEY_SOURCE_REPO` (community feed URL or your internal feed)
  - Set `skip_publish: false` in `.goreleaser.yaml`

Because each ecosystem has separate moderation/review flows, a practical sequence is: ship GitHub assets first, then enable each package manager one by one.
