#!/bin/bash
set -e  # Exit immediately on any command failure
set -o pipefail  # Properly handle failures in piped commands

# Run full test suite (dependencies will be auto-installed by test.py)
python3 test.py
exit_code=$?

# Run with build artifacts kept
# python3 test.py --keep
# exit_code=$?

# Propagate the exit code
exit $exit_code