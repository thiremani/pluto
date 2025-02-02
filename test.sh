#!/bin/bash
set -eo pipefail

# Configuration for E2E tests
TEST_DIR="tests"
BUILD_DIR="build"
PLUTO_EXE="./pluto"

# Cleanup function
cleanup() {
  if [[ "$1" != "keep" ]]; then
    rm -rf "$BUILD_DIR"
  fi
}

# Error handler
error() {
  echo -e "\n❌ Test failed!"
  cleanup "$@"
  exit 1
}

# Check dependencies for compiler tests
check_compiler_deps() {
  for dep in llc clang; do
    if ! command -v "$dep" &> /dev/null; then
      echo "🚨 Error: $dep is required but not installed"
      exit 1
    fi
  done
}

# Run unit tests
echo "=== Running Unit Tests ==="
(
  echo "Testing lexer..."
  cd lexer && go test -race
)
(
  echo "Testing parser..."
  cd parser && go test -race
)

# Run compiler E2E tests
echo -e "\n=== Running Compiler Tests ==="
mkdir -p "$BUILD_DIR"
trap 'error' ERR
check_compiler_deps

declare -a TEST_FILES
while IFS= read -r -d $'\0' file; do
  TEST_FILES+=("$file")
done < <(find "$TEST_DIR" -name '*.pt' -print0)

PASSED=0
FAILED=0

for TEST_FILE in "${TEST_FILES[@]}"; do
  BASE_NAME=$(basename "$TEST_FILE" .pt)
  TEST_NAME="${BASE_NAME%.*}"
  EXPECTED_FILE="${TEST_FILE%.pt}.exp"
  BUILD_PREFIX="$BUILD_DIR/$TEST_NAME"

  # Compilation pipeline with debugging
  echo "=== Compilation Steps ==="
  $PLUTO_EXE "$TEST_FILE" > "$BUILD_PREFIX.ll"
  echo "Generated LLVM IR:"
  cat "$BUILD_PREFIX.ll"

  llc -filetype=obj "$BUILD_PREFIX.ll" -o "$BUILD_PREFIX.o"
  clang -v "$BUILD_PREFIX.o" -o "$BUILD_PREFIX.out" -L/usr/lib/llvm-19/lib -Wl,-rpath=/usr/lib/llvm-19/lib

  # Run with library path
  echo "=== Execution ==="
  export LD_LIBRARY_PATH=/usr/lib/llvm-19/lib:$LD_LIBRARY_PATH
  set -x
  ACTUAL_OUTPUT=$("$BUILD_PREFIX.out")
  set +x

  if [ "$ACTUAL_OUTPUT" = "$EXPECTED_OUTPUT" ]; then
    echo "✅ Passed"
    ((PASSED++))
  else
    echo "❌ Failed"
    echo "   Expected: '$EXPECTED_OUTPUT'"
    echo "   Received: '$ACTUAL_OUTPUT'"
    ((FAILED++))
  fi
done

# Cleanup
cleanup "$@"

# Final report
echo -e "\n📊 Test results:"
echo "✅ $PASSED Passed"
echo "❌ $FAILED Failed"

exit $((FAILED > 0 ? 1 : 0))