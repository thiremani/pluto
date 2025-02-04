#!/bin/bash
# Install dependencies in virtual environment
python3 -m venv .venv
source .venv/bin/activate
pip install colorama

# Run full test suite
python test.py

# Run with build artifacts kept
# python test.py --keep

# deactivate virtual environment
deactivate