#!/usr/bin/env python3
from __future__ import annotations

import argparse
import os
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

from scripts.llvm_env import build_env


def is_windows_env() -> bool:
    return os.name == "nt" or os.environ.get("MSYSTEM") is not None


def default_output() -> str:
    return "pluto.exe" if is_windows_env() else "pluto"


def git_output(root: Path, *args: str) -> str:
    try:
        return subprocess.check_output(["git", *args], cwd=root, text=True, stderr=subprocess.DEVNULL).strip()
    except (subprocess.CalledProcessError, FileNotFoundError):
        return ""


def release_ldflags(root: Path) -> str:
    version = git_output(root, "describe", "--tags", "--always", "--dirty") or "dev"
    commit = git_output(root, "rev-parse", "--short", "HEAD") or "unknown"
    build_date = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    return f"-X main.Version={version} -X main.Commit={commit} -X main.BuildDate={build_date}"


def main() -> int:
    root = Path(__file__).parent.resolve()
    parser = argparse.ArgumentParser(description="Build the Pluto compiler with the LLVM 22 byollvm environment.")
    parser.add_argument("-o", "--output", default=default_output(), help="output binary path")
    parser.add_argument("--release", action="store_true", help="embed version, commit, and build date")
    args, go_args = parser.parse_known_args()

    if go_args[:1] == ["--"]:
        go_args = go_args[1:]

    env = build_env()
    cmd = ["go", "build", "-o", args.output]
    if args.release:
        cmd.extend(["-ldflags", release_ldflags(root)])
    cmd.extend(go_args)

    print("+", " ".join(cmd), flush=True)
    result = subprocess.run(cmd, cwd=root, env=env)
    if result.returncode != 0:
        print(f"build failed with exit code {result.returncode}", file=sys.stderr)
    return result.returncode


if __name__ == "__main__":
    sys.exit(main())
