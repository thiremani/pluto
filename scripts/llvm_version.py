"""Read Pluto's repository-pinned LLVM major version."""
from __future__ import annotations

from pathlib import Path


LLVM_VERSION_FILE = Path(__file__).resolve().parents[1] / ".llvm-version"


def read_llvm_major(path: Path = LLVM_VERSION_FILE) -> str:
    """Return the supported LLVM major from path."""
    try:
        value = path.read_text(encoding="utf-8").strip()
    except OSError as err:
        raise RuntimeError(f"could not read LLVM version from {path}: {err}") from err

    if not value.isascii() or not value.isdecimal() or int(value) < 1:
        raise RuntimeError(f"{path} must contain one positive LLVM major version, got {value!r}")

    return value
