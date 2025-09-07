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
import shutil
import subprocess
from typing import Dict, List


def _which_llvm_config() -> str:
    path = shutil.which("llvm-config")
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


def compute_env() -> Dict[str, str]:
    llvm_config = _which_llvm_config()
    cflags = _run([llvm_config, "--cflags"]) or ""
    cxxflags = _run([llvm_config, "--cxxflags"]) or ""
    ldflags = _run([llvm_config, "--ldflags", "--libs", "all", "--system-libs"]) or ""

    env: Dict[str, str] = {}
    # Pin LLVM bin for downstream tools (e.g., test.py) to avoid picking
    # an incompatible LLVM from Program Files.
    # Prefer clang's directory to make PATH Windows-friendly for pluto.exe
    clang_path = shutil.which("clang")
    if clang_path:
        env["LLVM_BIN"] = os.path.dirname(clang_path)
    else:
        env["LLVM_BIN"] = os.path.dirname(llvm_config)
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

    # If using MSYS2 Go, set GOROOT to a Windows-style path so the trimmed
    # Go tool can locate its tree. This does not affect non-MSYS2 Go.
    go_path = shutil.which("go") or ""
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
