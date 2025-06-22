#!/usr/bin/env python3
import re
import subprocess
import os
import shutil
import sys
from pathlib import Path
from difflib import Differ
from colorama import Fore, Style

TEST_DIR = Path("tests")
BUILD_DIR = Path("build")
PLUTO_EXE = "pluto"
KEEP_BUILD = False

class TestRunner:
    def __init__(self):
        self.passed = 0
        self.failed = 0
        self.project_root = Path(__file__).parent.resolve()
        self.llvm_bin = self.detect_llvm_path()

    def detect_llvm_path(self) -> Path:
        # Try common LLVM 20 paths
        paths = [
            Path("/usr/lib/llvm-20/bin"),  # Linux
            Path("/usr/local/opt/llvm@20/bin"),  # macOS
            Path("/opt/homebrew/opt/llvm@20/bin")  # macOS ARM
        ]
        for p in paths:
            if p.exists():
                return p
        raise RuntimeError("LLVM 20 not found. Install with:\n"
                           "Linux: https://apt.llvm.org/\n"
                           "macOS: brew install llvm@20")
        
    def run_command(self, cmd: list, cwd: Path = None) -> str:
        """Execute a command and return its output"""
        # Prepend LLVM bin to PATH
        env = os.environ.copy()
        env["PATH"] = f"{self.llvm_bin}:{env['PATH']}"
        str_cmd = [str(c) for c in cmd]
        try:
            result = subprocess.run(
                str_cmd,
                env=env,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                cwd=str(cwd) if cwd else None,
                text=True,
                check=True
            )
            return result.stdout
        except subprocess.CalledProcessError as e:
            print(f"\n{Fore.RED}Command failed: {' '.join(str_cmd)}{Style.RESET_ALL}")
            print(e.output)
            raise

    def build_compiler(self):
        """Build the Pluto compiler"""
        print(f"{Fore.YELLOW}=== Building Compiler ==={Style.RESET_ALL}")
        self.run_command(["go", "build", "-o", str(PLUTO_EXE), "main.go"], self.project_root)

    def run_unit_tests(self):
        """Run Go unit tests"""
        print(f"\n{Fore.YELLOW}=== Running Unit Tests ==={Style.RESET_ALL}")
        try:
            print(f"{Fore.CYAN}Testing lexer...{Style.RESET_ALL}")
            self.run_command(["go", "test", "-race"], self.project_root/"lexer")
            
            print(f"\n{Fore.CYAN}Testing parser...{Style.RESET_ALL}")
            self.run_command(["go", "test", "-race"], self.project_root/"parser")

            print(f"\n{Fore.CYAN}Testing compiler...{Style.RESET_ALL}")
            self.run_command(["go", "test", "-race"], self.project_root/"compiler")
        except subprocess.CalledProcessError:
            print(f"{Fore.RED}❌ Unit tests failed!{Style.RESET_ALL}")
            sys.exit(1)

    def compile(self, dir: Path):
        """Compile a pluto directory"""
        print(f"{Fore.CYAN}Compiling {dir}...{Style.RESET_ALL}")
        try:
            compiler_output = self.run_command(
                [self.project_root/PLUTO_EXE],
                cwd=dir
            )
            if compiler_output != "":
                print(f"{Fore.BLUE}Compiler output:\n{compiler_output}{Style.RESET_ALL}")
        except subprocess.CalledProcessError as e:
            print(f"{Fore.RED}❌ Compilation failed for {dir}{Style.RESET_ALL}")
            # Print the captured stdout/stderr from the failed pluto process
            if e.output:
                print(f"{Fore.BLUE}Compiler output was:\n{e.output.strip()}{Style.RESET_ALL}")
            raise # Re-raise the exception to stop tests for this directory.

    def _compare_outputs(self, expected_output: str, actual_output: str) -> bool:
        """
        Compares expected and actual output line by line, supporting regex.
        Returns True on match, False on mismatch.
        """
        actual_lines = actual_output.splitlines()
        expected_lines = expected_output.splitlines()

        if len(actual_lines) != len(expected_lines):
            print(f"{Fore.RED}❌ Mismatched number of output lines.{Style.RESET_ALL}")
            self.show_diff(expected_output, actual_output)
            return False

        for i, (expected_line, actual_line) in enumerate(zip(expected_lines, actual_lines), 1):
            if expected_line.startswith("re:"):
                pattern = expected_line[len("re:"):].strip()
                if not re.fullmatch(pattern, actual_line):
                    print(f"{Fore.RED}❌ Line {i} did not match regex{Style.RESET_ALL}")
                    print(f"    pattern: {pattern!r}")
                    print(f"    actual : {actual_line!r}")
                    return False
            else:
                if expected_line != actual_line:
                    print(f"{Fore.RED}❌ Line {i} mismatch{Style.RESET_ALL}")
                    print(f"    expected: {expected_line!r}")
                    print(f"    actual  : {actual_line!r}")
                    return False
        
        return True

    def _run_single_test(self, test_dir: Path, exp_file: Path):
        """
        Runs a single test case (one .exp file) and updates pass/fail counters.
        """
        test_name = exp_file.stem
        print(f"{Fore.CYAN}Testing {test_name}:{Style.RESET_ALL}")
        try:
            executable_path = str(test_dir / test_name)
            actual_output = self.run_command([executable_path])
            expected_output = exp_file.read_text()

            if self._compare_outputs(expected_output, actual_output):
                print(f"{Fore.GREEN}✅ Passed{Style.RESET_ALL}")
                self.passed += 1
            else:
                self.failed += 1

        except Exception as e:
            print(f"{Fore.RED}❌ Failed with exception: {e}{Style.RESET_ALL}")
            self.failed += 1

    def run_compiler_tests(self):
        """Run all compiler end-to-end tests"""
        print(f"\n{Fore.YELLOW}=== Running Compiler Tests ==={Style.RESET_ALL}")
        
        test_dirs = {exp_path.parent for exp_path in TEST_DIR.rglob("*.exp")}
        
        for test_dir in sorted(list(test_dirs)): # Sorting provides deterministic order
            print(f"\n{Fore.YELLOW}📁 Testing directory: {test_dir}{Style.RESET_ALL}")
            
            # 1. Compile the entire directory
            try:
                self.compile(test_dir)
            except subprocess.CalledProcessError:
                print(f"{Fore.RED}❌ Compilation failed for directory, skipping tests.{Style.RESET_ALL}")
                # We count this as one failure for the whole directory's tests
                num_tests_in_dir = len(list(test_dir.glob("*.exp")))
                self.failed += num_tests_in_dir
                continue

            # 2. Run each test in the directory
            for exp_file in sorted(list(test_dir.glob("*.exp"))):
                self._run_single_test(test_dir, exp_file)

    def show_diff(self, expected: str, actual: str):
        """Show colored diff output"""
        print(f"{Fore.RED}Output mismatch:{Style.RESET_ALL}")
        differ = Differ()
        diff = list(differ.compare(
            expected.splitlines(keepends=True),
            actual.splitlines(keepends=True)
        ))
        print(''.join(diff), end='')

    def run(self):
        """Main test runner"""
        try:
            self.build_compiler()
            self.run_unit_tests()
            self.run_compiler_tests()
        finally:
            if not KEEP_BUILD:
                shutil.rmtree(BUILD_DIR, ignore_errors=True)

        # Print summary
        print(f"\n{Fore.YELLOW}📊 Final Results:{Style.RESET_ALL}")
        print(f"{Fore.GREEN}✅ {self.passed} Passed{Style.RESET_ALL}")
        print(f"{Fore.RED}❌ {self.failed} Failed{Style.RESET_ALL}")
        sys.exit(1 if self.failed > 0 else 0)

if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("--keep", action="store_true", help="Keep build artifacts")
    args = parser.parse_args()

    KEEP_BUILD = args.keep
    TestRunner().run()