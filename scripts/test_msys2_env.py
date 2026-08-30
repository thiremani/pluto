from pathlib import Path
from tempfile import TemporaryDirectory
import unittest
from unittest import mock

from scripts import msys2_env
from scripts.llvm_version import read_llvm_major


LLVM_MAJOR = read_llvm_major()


class FakeMSYSConfigOutput:
    def __call__(self, cmd: list[str]) -> str:
        outputs = {
            ("--version",): f"{LLVM_MAJOR}.1.0",
            ("--bindir",): "C:/msys64/ucrt64/bin",
            ("--cflags",): "-IC:/msys64/ucrt64/include",
            ("--cxxflags",): "-IC:/msys64/ucrt64/include -std=c++17",
            ("--ldflags", "--libs", "all", "--system-libs"): "-LC:/msys64/ucrt64/lib -lLLVM",
        }
        return outputs[tuple(cmd[1:])]


class MSYS2EnvTest(unittest.TestCase):
    def test_explicit_config_takes_precedence(self) -> None:
        with TemporaryDirectory() as temp_dir:
            llvm_config = Path(temp_dir) / "llvm-config.exe"
            llvm_config.touch()

            with mock.patch.dict(msys2_env.os.environ, {"LLVM_CONFIG": str(llvm_config)}, clear=True):
                self.assertEqual(msys2_env._which_llvm_config(), str(llvm_config))

    def test_compute_env_uses_one_toolchain(self) -> None:
        config_output = FakeMSYSConfigOutput()
        tools = {
            "clang": "C:/msys64/ucrt64/bin/clang.exe",
            "clang++": "C:/msys64/ucrt64/bin/clang++.exe",
        }

        with (
            mock.patch.object(msys2_env, "_which_llvm_config", return_value="C:/msys64/ucrt64/bin/llvm-config.exe"),
            mock.patch.object(msys2_env, "_run", side_effect=config_output),
            mock.patch.object(msys2_env, "_llvm_tool", side_effect=lambda _llvm_bin, name: tools[name]),
            mock.patch.object(msys2_env.shutil, "which", return_value=None),
        ):
            result = msys2_env.compute_env()

        self.assertEqual(result["LLVM_BIN"], "C:/msys64/ucrt64/bin")
        self.assertEqual(result["LLVM_CONFIG"], "C:/msys64/ucrt64/bin/llvm-config.exe")
        self.assertEqual(result["LLVM_VERSION"], LLVM_MAJOR)
        self.assertEqual(result["CC"], "clang")
        self.assertEqual(result["CXX"], "clang++")
        self.assertTrue(result["PATH"].startswith(f"C:/msys64/ucrt64/bin{msys2_env.os.pathsep}"))

    def test_rejects_mismatched_config(self) -> None:
        wrong_version = f"{int(LLVM_MAJOR) + 1}.1.0"

        with mock.patch.object(msys2_env, "_run", return_value=wrong_version):
            with self.assertRaisesRegex(RuntimeError, f"requires LLVM {LLVM_MAJOR}.*reports {wrong_version}"):
                msys2_env._validate_llvm_version("llvm-config")


if __name__ == "__main__":
    unittest.main()
