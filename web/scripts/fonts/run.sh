#!/bin/sh
# `pnpm fonts`: runs build.py in a virtual environment holding exactly requirements.txt. Only for development: CI and
# the build use the committed output and need no Python.
set -eu
venv="${XDG_CACHE_HOME:-$HOME/.cache}/axiom-fonts/venv"
[ -x "$venv/bin/python" ] || python3 -m venv "$venv"
"$venv/bin/pip" install --quiet --require-hashes --requirement "$(dirname "$0")/requirements.txt"
exec "$venv/bin/python" -I "$(dirname "$0")/build.py"
