#!/usr/bin/env python3
"""
LLVM/CGO environment helper for Pluto builds.

The Go command cannot read project-local default CGO flags from go.mod, so
Python build/test entrypoints use this helper to derive byollvm flags from the
LLVM installation that is already on the machine.
"""
from __future__ import annotations

import os
import shutil
import subprocess
from pathlib import Path
from typing import Mapping


CPP_DEFS = "-D_GNU_SOURCE -D__STDC_CONSTANT_MACROS -D__STDC_FORMAT_MACROS -D__STDC_LIMIT_MACROS"


def _is_windows_env(env: Mapping[str, str]) -> bool:
    return os.name == "nt" or env.get("MSYSTEM") is not None


def _which(name: str, env: Mapping[str, str]) -> str | None:
    return shutil.which(name, path=env.get("PATH"))


def _detect_llvm_bin(env: Mapping[str, str]) -> Path:
    env_bin = env.get("LLVM_BIN", "").strip()
    if env_bin:
        path = Path(env_bin)
        if path.exists():
            return path

    llvm_home = env.get("LLVM_HOME", "").strip()
    if llvm_home:
        path = Path(llvm_home) / "bin"
        if path.exists():
            return path

    if _is_windows_env(env):
        paths = [
            Path("C:/msys64/ucrt64/bin"),
            Path("C:/msys64/mingw64/bin"),
            Path("C:/Program Files/LLVM/bin"),
        ]
    else:
        paths = [
            Path("/usr/lib/llvm-22/bin"),
            Path("/usr/local/opt/llvm/bin"),
            Path("/opt/homebrew/opt/llvm/bin"),
        ]
    for path in paths:
        if path.exists():
            return path

    install = "Windows: install MSYS2 UCRT64 and mingw-w64-ucrt-x86_64-llvm"
    if not _is_windows_env(env):
        install = "Linux: install LLVM 22 from apt.llvm.org; macOS: brew install llvm"
    raise RuntimeError(f"LLVM 22 not found. {install}; or set LLVM_BIN/LLVM_HOME.")


def _detect_llvm_config(env: Mapping[str, str], llvm_bin: Path) -> Path:
    env_config = env.get("LLVM_CONFIG", "").strip()
    if env_config:
        path = Path(_which(env_config, env) or env_config)
        if path.exists():
            return path
        raise RuntimeError(f"LLVM_CONFIG points to {env_config}, but it was not found.")

    names = ["llvm-config.exe", "llvm-config"] if _is_windows_env(env) else ["llvm-config"]
    for name in names:
        path = llvm_bin / name
        if path.exists():
            return path

    found = _which("llvm-config", env)
    if found:
        return Path(found)
    raise RuntimeError(f"llvm-config not found under {llvm_bin}. Set LLVM_CONFIG, LLVM_BIN, or LLVM_HOME.")


def _llvm_config_output(llvm_config: Path, *args: str) -> str:
    return subprocess.check_output([str(llvm_config), *args], text=True).strip()


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


def build_env(base_env: Mapping[str, str] | None = None) -> dict[str, str]:
    """Return a subprocess environment configured for Pluto's LLVM 22 build."""
    env = dict(os.environ if base_env is None else base_env)

    if env.get("MSYSTEM") is not None:
        try:
            from msys2_env import compute_env  # type: ignore
        except ModuleNotFoundError as err:
            if err.name != "msys2_env":
                raise
            from scripts.msys2_env import compute_env  # type: ignore

        required = compute_env()
        env["GOFLAGS"] = _with_byollvm(env.get("GOFLAGS", ""))
        for key, value in required.items():
            if key.startswith("CGO_"):
                _append_env_flags(env, key, value)
            else:
                env.setdefault(key, value)
    else:
        llvm_bin = _detect_llvm_bin(env)
        llvm_config = _detect_llvm_config(env, llvm_bin)
        env["LLVM_BIN"] = str(llvm_bin)
        env["GOFLAGS"] = _with_byollvm(env.get("GOFLAGS", ""))
        _append_env_flags(env, "CGO_CPPFLAGS", f"{_llvm_config_output(llvm_config, '--cflags')} {CPP_DEFS}")
        _append_env_flags(env, "CGO_CXXFLAGS", f"-std=c++17 {_llvm_config_output(llvm_config, '--cxxflags')}")
        _append_env_flags(env, "CGO_LDFLAGS", _llvm_config_output(llvm_config, "--ldflags", "--libs", "all", "--system-libs"))

    llvm_bin = env.get("LLVM_BIN")
    if llvm_bin:
        env["PATH"] = f"{llvm_bin}{os.pathsep}{env.get('PATH', '')}"
    return env
