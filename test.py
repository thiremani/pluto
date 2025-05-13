#!/usr/bin/env python3
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
        # Try common LLVM 19 paths
        paths = [
            Path("/usr/lib/llvm-19/bin"),  # Linux
            Path("/usr/local/opt/llvm@19/bin"),  # macOS
            Path("/opt/homebrew/opt/llvm@19/bin")  # macOS ARM
        ]
        for p in paths:
            if p.exists():
                return p
        raise RuntimeError("LLVM 19 not found. Install with:\n"
                           "Linux: https://apt.llvm.org/\n"
                           "macOS: brew install llvm@19")
        
    def run_command(self, cmd: list, cwd: Path = None) -> str:
        """Execute a command and return its output"""
        # Prepend LLVM bin to PATH
        env = os.environ.copy()
        env["PATH"] = f"{self.llvm_bin}:{env['PATH']}"
        try:
            result = subprocess.run(
                cmd,
                env=env,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                cwd=str(cwd) if cwd else None,
                text=True,
                check=True
            )
            return result.stdout
        except subprocess.CalledProcessError as e:
            print(f"\n{Fore.RED}Command failed: {' '.join(cmd)}{Style.RESET_ALL}")
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
        try:
            compiler_output = self.run_command(
                [self.project_root/PLUTO_EXE],
                cwd=dir
            )
            if compiler_output != "":
                print(f"{Fore.BLUE}Compiler output:\n{compiler_output}{Style.RESET_ALL}")
        except subprocess.CalledProcessError as e:
            print(f"{Fore.RED}❌ Compilation failed for {dir}{Style.RESET_ALL}")
            raise

        # Find generated IR files (both code and script)
        ptcache = os.getenv("PTCACHE")
        build_dir = ptcache/dir.name
        ir_files = list(build_dir.glob("**/*.ll"))
        if not ir_files:
            raise RuntimeError("No LLVM IR files generated")
        # Compile all IR files to object files
        obj_files = []
        for ir_file in ir_files:
            obj_file = build_dir / (ir_file.stem + ".o")
            self.run_command(["llc", "-filetype=obj", str(ir_file), "-o", str(obj_file)])
            obj_files.append(str(obj_file))

        # Link objects into executable
        exe_file = build_dir / test_name
        self.run_command(["clang", *obj_files, "-o", str(exe_file)])

    def compile_and_run(self, test_dir: Path) -> str:
        """Compile and run a Pluto test file"""
        test_name = test_dir
        build_dir = BUILD_DIR / test_name
        build_dir.mkdir(parents=True, exist_ok=True)

        # Generate LLVM IR - run compiler in test directory
        try:
            compiler_output = self.run_command(
                [self.project_root/PLUTO_EXE], 
                cwd=test_dir
            )
            if compiler_output != "":
                print(f"{Fore.BLUE}Compiler output:\n{compiler_output}{Style.RESET_ALL}")
        except subprocess.CalledProcessError as e:
            print(f"{Fore.RED}❌ Compilation failed for {test_name}{Style.RESET_ALL}")
            raise





        # Execute
        return self.run_command([str(exe_file)])

    def run_compiler_tests(self):
        """Run all compiler end-to-end tests"""
        print(f"\n{Fore.YELLOW}=== Running Compiler Tests ==={Style.RESET_ALL}")        
        exp_files = list((self.project_root/TEST_DIR).glob("*.exp"))

        for exp_file in exp_files:
            test_name = exp_file.name.removesuffix(".exp")
            print(f"\n{Fore.CYAN}Testing {exp_file.name}:{Style.RESET_ALL}")

            try:
                actual_output = self.compile_and_run(exp_file)
                expected_output = exp_file.read_text()

                if actual_output == expected_output:
                    print(f"{Fore.GREEN}✅ Passed{Style.RESET_ALL}")
                    self.passed += 1
                else:
                    self.show_diff(expected_output, actual_output)
                    self.failed += 1
                    
            except Exception as e:
                print(f"{Fore.RED}❌ Failed: {e}{Style.RESET_ALL}")
                self.failed += 1
                if KEEP_BUILD:
                    print(f"Build artifacts preserved in {BUILD_DIR}")
                else:
                    shutil.rmtree(BUILD_DIR, ignore_errors=True)

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
        sys.exit(self.failed > 0)

if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("--keep", action="store_true", help="Keep build artifacts")
    args = parser.parse_args()

    KEEP_BUILD = args.keep
    TestRunner().run()