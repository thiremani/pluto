#!/usr/bin/env python3
"""
LLVM/CGO environment helper for Pluto builds.

The Go command cannot read project-local default CGO flags from go.mod, so
Python build/test entrypoints use this helper to derive byollvm flags from the
LLVM installation that is already on the machine.
"""
from __future__ import annotations

import argparse
import os
import shlex
import shutil
import subprocess
import sys
import uuid
from pathlib import Path
from typing import Mapping

if __package__:
    from .llvm_version import read_llvm_major
else:
    from llvm_version import read_llvm_major


CPP_DEFS = "-D_GNU_SOURCE -D__STDC_CONSTANT_MACROS -D__STDC_FORMAT_MACROS -D__STDC_LIMIT_MACROS"
EXPORT_KEYS = (
    "LLVM_BIN",
    "LLVM_CONFIG",
    "LLVM_VERSION",
    "GOFLAGS",
    "CGO_ENABLED",
    "CC",
    "CXX",
    "CGO_CPPFLAGS",
    "CGO_CXXFLAGS",
    "CGO_LDFLAGS",
    "LD_LIBRARY_PATH",
    "PLUTO_WIN_TOOLCHAIN",
    "GOROOT",
)


def _is_windows_env(env: Mapping[str, str]) -> bool:
    return os.name == "nt" or env.get("MSYSTEM") is not None


def _which(name: str, env: Mapping[str, str]) -> str | None:
    return shutil.which(name, path=env.get("PATH"))


def _resolve_executable(value: str, env: Mapping[str, str]) -> Path | None:
    found = _which(value, env)
    path = Path(found or value)
    return path if path.is_file() else None


def _config_from_bin(llvm_bin: Path, llvm_major: str) -> Path | None:
    names = ["llvm-config.exe", "llvm-config", f"llvm-config-{llvm_major}"]
    for name in names:
        path = llvm_bin / name
        if path.is_file():
            return path

    return None


def _versioned_llvm_bins(llvm_major: str, env: Mapping[str, str]) -> list[Path]:
    if _is_windows_env(env):
        return [
            Path("C:/msys64/ucrt64/bin"),
            Path("C:/msys64/mingw64/bin"),
            Path("C:/Program Files/LLVM/bin"),
        ]

    return [
        Path(f"/usr/lib/llvm-{llvm_major}/bin"),
        Path(f"/usr/local/opt/llvm@{llvm_major}/bin"),
        Path(f"/opt/homebrew/opt/llvm@{llvm_major}/bin"),
    ]


def _detect_llvm_config(env: Mapping[str, str], llvm_major: str) -> Path:
    env_config = env.get("LLVM_CONFIG", "").strip()
    if env_config:
        path = _resolve_executable(env_config, env)
        if path is not None:
            return path
        raise RuntimeError(f"LLVM_CONFIG points to {env_config}, but it was not found.")

    for key in ("LLVM_BIN", "LLVM_HOME"):
        value = env.get(key, "").strip()
        if not value:
            continue
        llvm_bin = Path(value) if key == "LLVM_BIN" else Path(value) / "bin"
        if not llvm_bin.is_dir():
            raise RuntimeError(f"{key} points to {value}, but its LLVM bin directory was not found.")
        path = _config_from_bin(llvm_bin, llvm_major)
        if path is not None:
            return path
        raise RuntimeError(f"llvm-config was not found under {llvm_bin}.")

    versioned_name = "llvm-config.exe" if _is_windows_env(env) else f"llvm-config-{llvm_major}"
    found = _which(versioned_name, env)
    if found:
        return Path(found)

    for llvm_bin in _versioned_llvm_bins(llvm_major, env):
        path = _config_from_bin(llvm_bin, llvm_major)
        if path is not None:
            return path

    found = _which("llvm-config", env)
    if found:
        return Path(found)

    install = f"install LLVM {llvm_major} and set LLVM_CONFIG to its llvm-config executable"
    if _is_windows_env(env):
        install = f"install an MSYS2 LLVM {llvm_major} toolchain and put llvm-config on PATH"
    raise RuntimeError(f"LLVM {llvm_major} was not found; {install}.")


def _llvm_config_output(llvm_config: Path, *args: str) -> str:
    return subprocess.check_output([str(llvm_config), *args], text=True).strip()


def _validate_llvm_config(llvm_config: Path, llvm_major: str) -> None:
    version = _llvm_config_output(llvm_config, "--version")
    found_major = version.split(".", 1)[0]
    if found_major != llvm_major:
        raise RuntimeError(
            f"Pluto requires LLVM {llvm_major}, but {llvm_config} reports {version}. "
            "Select the matching llvm-config with LLVM_CONFIG."
        )


def _with_byollvm(goflags: str) -> str:
    return goflags if "-tags=byollvm" in goflags else f"{goflags} -tags=byollvm".strip()


def _append_env_flags(env: dict[str, str], key: str, value: str) -> None:
    value = value.strip()
    if not value:
        return
    current = env.get(key, "").strip()
    if current:
        env[key] = current if value in current else f"{current} {value}"
    else:
        env[key] = value


def _prepend_path_env(env: dict[str, str], key: str, value: str) -> None:
    value = value.strip()
    if not value:
        return
    current = env.get(key, "").strip()
    parts = [part for part in current.split(os.pathsep) if part]
    if value in parts:
        env[key] = current
    elif current:
        env[key] = f"{value}{os.pathsep}{current}"
    else:
        env[key] = value


def build_env(base_env: Mapping[str, str] | None = None) -> dict[str, str]:
    """Return a subprocess environment configured for Pluto's pinned LLVM build."""
    env = dict(os.environ if base_env is None else base_env)
    llvm_major = read_llvm_major()
    env["LLVM_VERSION"] = llvm_major

    if env.get("MSYSTEM") is not None:
        if __package__:
            from .msys2_env import compute_env
        else:
            from msys2_env import compute_env

        required = compute_env(env)
        env["GOFLAGS"] = _with_byollvm(env.get("GOFLAGS", ""))
        for key, value in required.items():
            if key.startswith("CGO_"):
                _append_env_flags(env, key, value)
            elif key != "GOFLAGS":
                env[key] = value
    else:
        llvm_config = _detect_llvm_config(env, llvm_major)
        _validate_llvm_config(llvm_config, llvm_major)
        llvm_bin = Path(_llvm_config_output(llvm_config, "--bindir"))
        if not llvm_bin.is_dir():
            raise RuntimeError(f"{llvm_config} reports missing LLVM bin directory {llvm_bin}.")

        env["LLVM_BIN"] = str(llvm_bin)
        env["LLVM_CONFIG"] = str(llvm_config)
        env["GOFLAGS"] = _with_byollvm(env.get("GOFLAGS", ""))
        clang_suffix = ".exe" if _is_windows_env(env) else ""
        clang = llvm_bin / f"clang{clang_suffix}"
        clangxx = llvm_bin / f"clang++{clang_suffix}"
        missing_tools = [str(path) for path in (clang, clangxx) if not path.exists()]
        if missing_tools:
            raise RuntimeError(f"selected LLVM installation is missing required tools: {', '.join(missing_tools)}")
        env["CC"] = clang.name
        env["CXX"] = clangxx.name
        _append_env_flags(env, "CGO_CPPFLAGS", f"{_llvm_config_output(llvm_config, '--cflags')} {CPP_DEFS}")
        _append_env_flags(env, "CGO_CXXFLAGS", f"-std=c++17 {_llvm_config_output(llvm_config, '--cxxflags')}")
        _append_env_flags(
            env,
            "CGO_LDFLAGS",
            _llvm_config_output(llvm_config, "--ldflags", "--libs", "all", "--system-libs"),
        )
        _prepend_path_env(env, "LD_LIBRARY_PATH", _llvm_config_output(llvm_config, "--libdir"))

    llvm_bin = env.get("LLVM_BIN")
    if llvm_bin:
        _prepend_path_env(env, "PATH", llvm_bin)
    return env


def _export_env(env: Mapping[str, str]) -> dict[str, str]:
    return {key: env[key] for key in EXPORT_KEYS if env.get(key)}


def _print_shell(env: Mapping[str, str]) -> None:
    for key, value in _export_env(env).items():
        print(f"export {key}={shlex.quote(value)}")
    if env.get("LLVM_BIN"):
        print(f'export PATH={shlex.quote(env["LLVM_BIN"])}:"$PATH"')


def _write_github_env_value(path: str, key: str, value: str) -> None:
    with open(path, "a", encoding="utf-8") as f:
        if "\n" in value:
            delimiter = f"PLUTO_ENV_{uuid.uuid4().hex}"
            f.write(f"{key}<<{delimiter}\n{value}\n{delimiter}\n")
        else:
            f.write(f"{key}={value}\n")


def _write_github_actions(env: Mapping[str, str]) -> None:
    github_env = os.environ.get("GITHUB_ENV")
    github_path = os.environ.get("GITHUB_PATH")
    if not github_env or not github_path:
        raise RuntimeError("GITHUB_ENV and GITHUB_PATH must be set for --github-actions")
    for key, value in _export_env(env).items():
        if key != "LLVM_BIN":
            _write_github_env_value(github_env, key, value)
    if env.get("LLVM_BIN"):
        with open(github_path, "a", encoding="utf-8") as f:
            f.write(f"{env['LLVM_BIN']}\n")


def main() -> int:
    parser = argparse.ArgumentParser(description="Print or apply Pluto's pinned byollvm build environment.")
    parser.add_argument("--shell", action="store_true", help="print POSIX shell export commands")
    parser.add_argument("--github-actions", action="store_true", help="write env/path entries to GitHub Actions files")
    parser.add_argument("--llvm-version", action="store_true", help="print the required LLVM major without probing the toolchain")
    args = parser.parse_args()

    if args.llvm_version:
        print(read_llvm_major())
        return 0

    try:
        env = build_env()
    except (OSError, RuntimeError, subprocess.CalledProcessError) as err:
        print(f"error: {err}", file=sys.stderr)
        return 1

    if args.github_actions:
        _write_github_actions(env)
    else:
        _print_shell(env)
    return 0


if __name__ == "__main__":
    sys.exit(main())
