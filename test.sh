#!/bin/bash
set -e  # Exit immediately on any command failure
set -o pipefail  # Properly handle failures in piped commands

# Install dependencies in virtual environment
python3 -m venv .venv
source .venv/bin/activate
pip install colorama

# Run full test suite
python test.py
exit_code=$?

# Run with build artifacts kept
# python test.py --keep
# exit_code=$?

# deactivate virtual environment
deactivate

# Propagate the exit code
exit $exit_code