import io
import os
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest
from unittest import mock

from scripts import llvm_env
from scripts.llvm_version import read_llvm_major


LLVM_MAJOR = read_llvm_major()
TOOL_SUFFIX = ".exe" if os.name == "nt" else ""


class FakeLLVMConfigOutput:
    def __init__(self, llvm_bin: Path, version: str | None = None) -> None:
        self.llvm_bin = llvm_bin
        self.version = version or f"{LLVM_MAJOR}.1.0"

    def __call__(self, _llvm_config: Path, *args: str) -> str:
        outputs = {
            ("--version",): self.version,
            ("--bindir",): str(self.llvm_bin),
            ("--cflags",): "-I/fake/include",
            ("--cxxflags",): "-I/fake/include -std=c++17",
            ("--ldflags", "--libs", "all", "--system-libs"): f"-L/fake/lib -lLLVM-{LLVM_MAJOR}",
            ("--libdir",): "/fake/lib",
        }
        return outputs[args]


class LLVMEnvTest(unittest.TestCase):
    def test_version_flag_does_not_probe_toolchain(self) -> None:
        stdout = io.StringIO()

        with (
            mock.patch.object(llvm_env.sys, "argv", ["llvm_env.py", "--llvm-version"]),
            mock.patch.object(llvm_env, "read_llvm_major", return_value="37"),
            mock.patch.object(llvm_env, "build_env", side_effect=AssertionError("toolchain probe")),
            mock.patch("sys.stdout", stdout),
        ):
            result = llvm_env.main()

        self.assertEqual(result, 0)
        self.assertEqual(stdout.getvalue(), "37\n")

    def test_explicit_config_takes_precedence_over_bin(self) -> None:
        with TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            explicit = root / "explicit-llvm-config"
            explicit.touch()
            other_bin = root / "other" / "bin"
            other_bin.mkdir(parents=True)
            (other_bin / "llvm-config").touch()
            env = {
                "LLVM_CONFIG": str(explicit),
                "LLVM_BIN": str(other_bin),
                "PATH": os.environ.get("PATH", ""),
            }

            self.assertEqual(llvm_env._detect_llvm_config(env, LLVM_MAJOR), explicit)

    def test_build_env_derives_tools_from_config_bindir(self) -> None:
        with TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            llvm_config = root / "llvm-config"
            llvm_config.touch()
            llvm_bin = root / "selected toolchain" / "bin"
            llvm_bin.mkdir(parents=True)
            (llvm_bin / f"clang{TOOL_SUFFIX}").touch()
            (llvm_bin / f"clang++{TOOL_SUFFIX}").touch()
            config_output = FakeLLVMConfigOutput(llvm_bin)
            env = {
                "LLVM_CONFIG": str(llvm_config),
                "CC": "/usr/bin/cc",
                "CXX": "/usr/bin/c++",
                "PATH": "/usr/bin",
            }

            with mock.patch.object(llvm_env, "_llvm_config_output", side_effect=config_output):
                result = llvm_env.build_env(env)

            self.assertEqual(result["LLVM_BIN"], str(llvm_bin))
            self.assertEqual(result["LLVM_CONFIG"], str(llvm_config))
            self.assertEqual(result["LLVM_VERSION"], LLVM_MAJOR)
            self.assertEqual(result["CC"], f"clang{TOOL_SUFFIX}")
            self.assertEqual(result["CXX"], f"clang++{TOOL_SUFFIX}")
            self.assertEqual(result["PATH"].split(os.pathsep)[0], str(llvm_bin))
            self.assertIn("-tags=byollvm", result["GOFLAGS"])

    def test_rejects_mismatched_explicit_config(self) -> None:
        llvm_config = Path("/fake/llvm-config")
        wrong_major = str(int(LLVM_MAJOR) + 1)
        wrong_version = f"{wrong_major}.1.0"
        pattern = rf"requires LLVM {LLVM_MAJOR}.*reports {wrong_version}"

        with mock.patch.object(llvm_env, "_llvm_config_output", return_value=wrong_version):
            with self.assertRaisesRegex(RuntimeError, pattern):
                llvm_env._validate_llvm_config(llvm_config, LLVM_MAJOR)

    def test_msys_selection_replaces_stale_tool_paths(self) -> None:
        selected_bin = "C:/msys64/ucrt64/bin"
        selected_config = f"{selected_bin}/llvm-config.exe"
        required = {
            "LLVM_BIN": selected_bin,
            "LLVM_CONFIG": selected_config,
            "LLVM_VERSION": LLVM_MAJOR,
            "GOFLAGS": "-tags=byollvm",
            "CC": "clang",
            "CXX": "clang++",
        }
        env = {
            "MSYSTEM": "UCRT64",
            "LLVM_BIN": "C:/Program Files/LLVM/bin",
            "LLVM_CONFIG": "C:/Program Files/LLVM/bin/llvm-config.exe",
            "PATH": "C:/Windows/System32",
        }

        with mock.patch("scripts.msys2_env.compute_env", return_value=required):
            result = llvm_env.build_env(env)

        self.assertEqual(result["LLVM_BIN"], selected_bin)
        self.assertEqual(result["LLVM_CONFIG"], selected_config)
        self.assertTrue(result["PATH"].startswith(f"{selected_bin}{os.pathsep}"))


if __name__ == "__main__":
    unittest.main()
