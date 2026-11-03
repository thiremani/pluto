from pathlib import Path
import unittest

from scripts.llvm_version import read_llvm_major


ROOT = Path(__file__).resolve().parents[1]


class LLVMVersionConsistencyTest(unittest.TestCase):
    def test_goreleaser_debian_dependencies_match_pin(self) -> None:
        llvm_major = read_llvm_major()
        config = (ROOT / ".goreleaser.yaml").read_text(encoding="utf-8")

        self.assertIn(f"requires LLVM {llvm_major} toolchain", config)
        for package in ("llvm", "clang", "lld"):
            with self.subTest(package=package):
                self.assertIn(f"      - {package}-{llvm_major}\n", config)
        for formula in ("llvm", "lld"):
            with self.subTest(formula=formula):
                self.assertIn(f"      - formula: {formula}@{llvm_major}\n", config)

    def test_goreleaser_builds_tag_the_llvm_major(self) -> None:
        config = (ROOT / ".goreleaser.yaml").read_text(encoding="utf-8")
        tag_blocks = config.count("    tags:\n")

        self.assertGreater(tag_blocks, 0)
        self.assertEqual(config.count("    tags:\n      - byollvm\n      - 'llvm{{ .Env.LLVM_VERSION }}'\n"), tag_blocks)


if __name__ == "__main__":
    unittest.main()
