#!/usr/bin/env python3
"""
MSYS2 UCRT64 environment helper for Pluto builds.

Usage (inside MSYS2 UCRT64 shell):
    from scripts.msys2_env import compute_env
    env = compute_env()
    # then pass env to subprocess.run([...], env={**os.environ, **env})

This module discovers llvm-config and derives the CGO flags needed by
tinygo.org/x/go-llvm in byollvm mode.
"""
from __future__ import annotations

import os
import re
import shutil
import subprocess
from typing import Dict, List, Mapping

if __package__:
    from .llvm_version import read_llvm_major
else:
    from llvm_version import read_llvm_major


def _which_llvm_config(env: Mapping[str, str] | None = None) -> str:
    source = os.environ if env is None else env
    explicit = source.get("LLVM_CONFIG", "").strip()
    if explicit:
        path = shutil.which(explicit, path=source.get("PATH")) or explicit
        if os.path.isfile(path):
            return path
        raise FileNotFoundError(f"LLVM_CONFIG points to {explicit}, but it was not found")

    llvm_bin = source.get("LLVM_BIN", "").strip()
    if llvm_bin:
        for name in ("llvm-config.exe", "llvm-config"):
            path = os.path.join(llvm_bin, name)
            if os.path.isfile(path):
                return path
        raise FileNotFoundError(f"llvm-config was not found under LLVM_BIN={llvm_bin}")

    path = shutil.which("llvm-config", path=source.get("PATH"))
    if path:
        return path
    # Common MSYS2 paths
    candidates: List[str] = [
        "/ucrt64/bin/llvm-config",
        "/mingw64/bin/llvm-config",
        "C:/msys64/ucrt64/bin/llvm-config.exe",
        "C:/msys64/mingw64/bin/llvm-config.exe",
    ]
    for candidate in candidates:
        if os.path.exists(candidate):
            return candidate
    raise FileNotFoundError(
        "llvm-config not found on PATH. Install MSYS2 UCRT64 llvm: pacman -S --needed mingw-w64-ucrt-x86_64-llvm"
    )


def _run(cmd: List[str]) -> str:
    result = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)
    return result.stdout.decode().strip()


def _validate_llvm_version(llvm_config: str) -> None:
    required_major = read_llvm_major()
    version = _run([llvm_config, "--version"])
    match = re.match(r"^(\d+)(?:\.|$)", version)
    if not match:
        raise RuntimeError(f"could not determine the LLVM major from {llvm_config} --version: {version!r}")
    if match.group(1) != required_major:
        raise RuntimeError(
            f"Pluto requires LLVM {required_major}, but {llvm_config} reports {version}. "
            "Select the matching MSYS2 toolchain or set LLVM_CONFIG to its llvm-config."
        )


def _llvm_tool(llvm_bin: str, name: str) -> str:
    path = shutil.which(name, path=llvm_bin)
    if path:
        return path
    raise FileNotFoundError(
        f"{name} not found in {llvm_bin}, the bindir reported by llvm-config. "
        "Install a complete matching MSYS2 LLVM toolchain."
    )


def compute_env(base_env: Mapping[str, str] | None = None) -> Dict[str, str]:
    source = os.environ if base_env is None else base_env
    llvm_config = _which_llvm_config(source)
    _validate_llvm_version(llvm_config)
    llvm_bin = _run([llvm_config, "--bindir"])
    if not llvm_bin:
        raise RuntimeError(f"{llvm_config} --bindir returned an empty path")
    _llvm_tool(llvm_bin, "clang")
    _llvm_tool(llvm_bin, "clang++")

    cflags = _run([llvm_config, "--cflags"]) or ""
    cxxflags = _run([llvm_config, "--cxxflags"]) or ""
    ldflags = _run([llvm_config, "--ldflags", "--libs", "all", "--system-libs"]) or ""

    env: Dict[str, str] = {}
    # Pin LLVM bin for downstream tools (e.g., test.py) to avoid picking
    # an incompatible LLVM from Program Files. The compiler paths and bin
    # directory all come from the selected llvm-config installation.
    env["LLVM_BIN"] = llvm_bin
    env["LLVM_CONFIG"] = llvm_config
    env["LLVM_VERSION"] = read_llvm_major()
    env["CGO_ENABLED"] = "1"
    env["CC"] = "clang"
    env["CXX"] = "clang++"
    env["CGO_CPPFLAGS"] = (
        f"{cflags} -D_GNU_SOURCE -D__STDC_CONSTANT_MACROS -D__STDC_FORMAT_MACROS -D__STDC_LIMIT_MACROS".strip()
    )
    env["CGO_CXXFLAGS"] = f"-std=c++17 {cxxflags}".strip()
    env["CGO_LDFLAGS"] = ldflags.strip()
    # Ensure Go commands inherit the byollvm tag in this MSYS2 flow.
    # Use = form to avoid GOFLAGS tokenizing into a non-flag value.
    env["GOFLAGS"] = "-tags=byollvm"
    # Select GNU toolchain in Pluto on Windows under MSYS2.
    env["PLUTO_WIN_TOOLCHAIN"] = "gnu"
    current_path = source.get("PATH", "")
    env["PATH"] = f"{llvm_bin}{os.pathsep}{current_path}" if current_path else llvm_bin

    # If using MSYS2 Go, set GOROOT to a Windows-style path so the trimmed
    # Go tool can locate its tree. This does not affect non-MSYS2 Go.
    go_path = shutil.which("go", path=source.get("PATH")) or ""
    go_dir = os.path.dirname(go_path)
    norm = go_dir.replace("\\", "/").lower()
    if any(s in norm for s in ("/ucrt64/bin", "/mingw64/bin", "/mingw32/bin")):
        msys_root = os.path.dirname(go_dir)  # e.g. C:\msys64\ucrt64
        goroot_win = os.path.join(msys_root, "lib", "go")  # Windows path
        env["GOROOT"] = goroot_win

    return env


if __name__ == "__main__":
    # Print derived environment in KEY=VALUE form for quick inspection.
    for key, value in compute_env().items():
        print(f"{key}={value}")
