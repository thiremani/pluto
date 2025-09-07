#!/usr/bin/env python3
import os
import sys
import shutil
import subprocess
from pathlib import Path

# Ensure local imports work when called as: python scripts/msys2_build.py
SCRIPT_DIR = os.path.dirname(__file__)
if SCRIPT_DIR and SCRIPT_DIR not in sys.path:
    sys.path.insert(0, SCRIPT_DIR)

from msys2_env import compute_env  # noqa: E402


def run(cmd, env):
    print("+", " ".join(cmd))
    proc = subprocess.run(cmd, env=env)
    if proc.returncode != 0:
        sys.exit(proc.returncode)


def build_pluto(project_root: str | Path | None = None, extra_env: dict | None = None) -> Path:
    """Build pluto.exe with MSYS2 UCRT64 toolchain and return path to binary."""
    root = Path(project_root) if project_root else Path.cwd()
    env = os.environ.copy()
    env.update(compute_env())
    if extra_env:
        env.update({k: str(v) for k, v in extra_env.items()})

    if not shutil.which("go", path=env.get("PATH")):
        print(
            "error: 'go' not found on PATH. Install MSYS2 Go: pacman -S --needed mingw-w64-ucrt-x86_64-go",
            file=sys.stderr,
        )
        raise SystemExit(1)

    run(["go", "version"], env)
    # GOFLAGS from msys2_env.py will apply -tags byollvm.
    out = root / "pluto.exe"
    run(["go", "build", "-o", str(out), "main.go"], env | {"PWD": str(root)})
    print(f"OK: ./{out.name} built.")
    return out


def main():
    build_pluto()


if __name__ == "__main__":
    main()
