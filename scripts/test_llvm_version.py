from pathlib import Path
from tempfile import TemporaryDirectory
import unittest

from scripts.llvm_version import read_llvm_major


class LLVMVersionTest(unittest.TestCase):
    def test_reads_positive_major(self) -> None:
        with TemporaryDirectory() as temp_dir:
            version_file = Path(temp_dir) / ".llvm-version"
            version_file.write_text("37\n", encoding="utf-8")

            self.assertEqual(read_llvm_major(version_file), "37")

    def test_rejects_invalid_values(self) -> None:
        invalid_values = ("", "0", "37.1", "llvm37", "37\n38\n")

        with TemporaryDirectory() as temp_dir:
            version_file = Path(temp_dir) / ".llvm-version"
            for value in invalid_values:
                with self.subTest(value=value):
                    version_file.write_text(value, encoding="utf-8")

                    with self.assertRaisesRegex(RuntimeError, "one positive LLVM major"):
                        read_llvm_major(version_file)


if __name__ == "__main__":
    unittest.main()
