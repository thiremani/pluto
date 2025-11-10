#!/usr/bin/env python3
import re
import subprocess
import os
import shutil
import sys
from pathlib import Path
from difflib import Differ

def ensure_dependencies():
    """Ensure required dependencies from requirements.txt are installed"""
    requirements_file = Path(__file__).parent / "requirements.txt"
    if not requirements_file.exists():
        return
    
    # Try importing each package to check if already installed
    requirements = requirements_file.read_text().strip().split('\n')
    missing_packages = []
    
    for requirement in requirements:
        package_name = requirement.strip().split('==')[0].split('>=')[0].split('<')[0]
        try:
            __import__(package_name)
        except ImportError:
            missing_packages.append(requirement)
    
    if not missing_packages:
        return
    
    print(f"Missing dependencies: {', '.join(missing_packages)}")
    print("Creating virtual environment and installing dependencies...")
    
    venv_path = Path(__file__).parent / ".venv"
    
    try:
        # Create virtual environment if it doesn't exist
        if not venv_path.exists():
            subprocess.check_call([sys.executable, "-m", "venv", str(venv_path)])
        
        # Determine the python executable in the venv
        if os.name == 'nt':  # Windows
            venv_python = venv_path / "Scripts" / "python.exe"
        else:  # Unix-like
            venv_python = venv_path / "bin" / "python"
        
        # Install dependencies in the virtual environment
        subprocess.check_call([str(venv_python), "-m", "pip", "install"] + missing_packages)
        print("Dependencies installed successfully in virtual environment!")
        
        # Re-exec this script using the venv python
        os.execv(str(venv_python), [str(venv_python)] + sys.argv)
        
    except subprocess.CalledProcessError as e:
        print(f"Failed to install dependencies: {e}")
        print("Please install manually:")
        print(f"python3 -m venv .venv && source .venv/bin/activate && pip install -r {requirements_file}")
        sys.exit(1)

# Ensure dependencies before importing
ensure_dependencies()
from colorama import Fore, Style
MSYS2 = os.environ.get("MSYSTEM") is not None
try:
    from scripts.msys2_build import build_pluto  # type: ignore
except Exception:
    build_pluto = None

TEST_DIR = Path("tests")
BUILD_DIR = Path("build")
# On Windows (including MSYS2), the built binary is an .exe
IS_WINDOWS_ENV = (os.name == "nt") or (os.environ.get("MSYSTEM") is not None)
PLUTO_EXE = "pluto.exe" if IS_WINDOWS_ENV else "pluto"
KEEP_BUILD = False

class TestRunner:
    def __init__(self, test_dir: Path = None):
        self.passed = 0
        self.failed = 0
        self.project_root = Path(__file__).parent.resolve()
        # Keep simple: prefer MSYS2-provided LLVM_BIN if present; otherwise fall back.
        if os.name == 'nt':
            self.llvm_bin = self.detect_llvm_path_windows()
        else:
            self.llvm_bin = self.detect_llvm_path_unix()
        self.test_dir = test_dir

    def detect_llvm_path_windows(self) -> Path:
        # Prefer MSYS2 UCRT64/MINGW64 paths if present, then LLVM_HOME, then Program Files
        env_bin = os.environ.get("LLVM_BIN", "").strip()
        if env_bin:
            p = Path(env_bin)
            if p.exists():
                return p
        paths = [
            Path("C:/msys64/ucrt64/bin"),
            Path("C:/msys64/mingw64/bin"),
            Path(os.environ.get("LLVM_HOME", "")) / "bin",
            Path("C:/Program Files/LLVM/bin"),
        ]
        for p in paths:
            if p.exists():
                return p
        raise RuntimeError(
            "LLVM 21 not found. On Windows, install MSYS2 UCRT64 and 'mingw-w64-ucrt-x86_64-llvm', or set LLVM_BIN/LLVM_HOME."
        )

    def detect_llvm_path_unix(self) -> Path:
        # Try common LLVM 21 paths
        paths = [
            Path("/usr/lib/llvm-21/bin"),  # Linux
            Path("/usr/local/opt/llvm/bin"),  # macOS Intel
            Path("/opt/homebrew/opt/llvm/bin")  # macOS ARM
        ]
        for p in paths:
            if p.exists():
                return p
        raise RuntimeError("LLVM 21 not found. Install with:\n"
                           "Linux: https://apt.llvm.org/\n"
                           "macOS: brew install llvm")

        
    def run_command(self, cmd: list, cwd: Path = None) -> str:
        """Execute a command and return its output"""
        # Merge MSYS2 LLVM/CGO env when running inside MSYS2 so go build/test works consistently.
        env = os.environ.copy()
        if MSYS2:
            try:
                sys.path.insert(0, str((self.project_root / "scripts").resolve()))
                from msys2_env import compute_env  # type: ignore
                env.update(compute_env())
            except Exception:
                pass
        # Prepend LLVM bin to PATH, but let existing LLVM_BIN (e.g., from MSYS2) take precedence.
        prepend = env.get("LLVM_BIN") or str(self.llvm_bin)
        if prepend:
            env["PATH"] = f"{prepend}{os.pathsep}{env['PATH']}"

        str_cmd = [str(c) for c in cmd]
        try:
            result = subprocess.run(
                str_cmd,
                env=env,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                cwd=str(cwd) if cwd else None,
                text=True,
                encoding="utf-8",
                errors="replace",
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
        # If MSYS2 build function is available (Windows UCRT64), use it to unify flags.
        if os.name == 'nt' and build_pluto is not None and os.environ.get('MSYSTEM'):
            build_pluto(self.project_root)
            return
        build_command = ["go", "build", "-o", str(PLUTO_EXE), "main.go"]
        self.run_command(build_command, self.project_root)

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
            expected_output = exp_file.read_text(encoding="utf-8")

            if self._compare_outputs(expected_output, actual_output):
                print(f"{Fore.GREEN}✅ Passed{Style.RESET_ALL}")
                self.passed += 1
                # Clean up executable after successful test
                if not KEEP_BUILD:
                    Path(executable_path).unlink(missing_ok=True)
            else:
                self.failed += 1

        except Exception as e:
            print(f"{Fore.RED}❌ Failed with exception: {e}{Style.RESET_ALL}")
            self.failed += 1

    def test_relative_path_compilation(self):
        """Test that compiling with relative paths works correctly"""
        print(f"\n{Fore.YELLOW}=== Testing Relative Path Compilation ==={Style.RESET_ALL}")
        print(f"{Fore.CYAN}Testing: cd tests/relpath && ../../pluto sample.spt{Style.RESET_ALL}")

        # Test compiling a single file with a relative path from a different directory
        # This simulates: cd tests/relpath && ../../pluto sample.spt
        test_dir = self.project_root / "tests" / "relpath"
        test_script = "sample.spt"
        test_binary = test_dir / "sample"
        if IS_WINDOWS_ENV:
            test_binary = test_dir / "sample.exe"

        try:
            # Change to tests/relpath directory and compile with relative path to pluto
            relative_pluto = self.project_root / PLUTO_EXE
            self.run_command([relative_pluto, test_script], cwd=test_dir)

            # Check that binary was created
            if test_binary.exists():
                print(f"{Fore.GREEN}✅ Relative path compilation passed{Style.RESET_ALL}")
                self.passed += 1
                # Clean up
                if not KEEP_BUILD:
                    test_binary.unlink(missing_ok=True)
            else:
                print(f"{Fore.RED}❌ Binary not created for relative path compilation{Style.RESET_ALL}")
                self.failed += 1

        except Exception as e:
            print(f"{Fore.RED}❌ Relative path compilation failed: {e}{Style.RESET_ALL}")
            self.failed += 1

    def run_compiler_tests(self):
        """Run all compiler end-to-end tests"""
        print(f"\n{Fore.YELLOW}=== Running Compiler Tests ==={Style.RESET_ALL}")

        if self.test_dir:
            # Run tests for specific directory
            if not self.test_dir.exists():
                print(f"{Fore.RED}❌ Test directory {self.test_dir} does not exist{Style.RESET_ALL}")
                return

            exp_files = list(self.test_dir.glob("*.exp"))
            if not exp_files:
                print(f"{Fore.RED}❌ No .exp files found in {self.test_dir}{Style.RESET_ALL}")
                return

            test_dirs = [self.test_dir]
        else:
            # Run all tests
            test_dirs = {exp_path.parent for exp_path in TEST_DIR.rglob("*.exp")}
            test_dirs = sorted(test_dirs)  # Sorting provides deterministic order

        for test_dir in test_dirs:
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
            for exp_file in sorted(test_dir.glob("*.exp")):
                self._run_single_test(test_dir, exp_file)

        # 3. Test relative path compilation (only when running all tests)
        if not self.test_dir:
            self.test_relative_path_compilation()

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
    parser.add_argument("test_dir", nargs="?", help="Specific test directory to run")
    args = parser.parse_args()

    KEEP_BUILD = args.keep
    test_dir = Path(args.test_dir) if args.test_dir else None
    runner = TestRunner(test_dir)
    runner.run()
