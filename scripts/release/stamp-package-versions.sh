#!/usr/bin/env bash

set -euo pipefail

version=${1:-}

if [[ ! $version =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  printf 'package version stamping failed: expected X.Y.Z (got %q)\n' "$version" >&2
  exit 1
fi

python3 - "$version" <<'PY'
from pathlib import Path
import re
import sys

version = sys.argv[1]
path = Path("sdk/python/pyproject.toml")
lines = path.read_text(encoding="utf-8").splitlines(keepends=True)
section = None
replacements = 0

for index, line in enumerate(lines):
    stripped = line.strip()
    if stripped.startswith("[") and stripped.endswith("]"):
        section = stripped
    elif section == "[project]" and re.fullmatch(r'version\s*=\s*"[^"]+"', stripped):
        newline = "\n" if line.endswith("\n") else ""
        indent = line[: len(line) - len(line.lstrip())]
        lines[index] = f'{indent}version = "{version}"{newline}'
        replacements += 1

if replacements != 1:
    raise SystemExit(
        f"expected one [project] version in {path}, found {replacements}"
    )

path.write_text("".join(lines), encoding="utf-8")
PY

(
  cd sdk/typescript
  npm version "$version" \
    --no-git-tag-version \
    --ignore-scripts \
    --allow-same-version >/dev/null
)

python_version=$(python3 -c 'import pathlib, tomllib; print(tomllib.loads(pathlib.Path("sdk/python/pyproject.toml").read_text())["project"]["version"])')
typescript_version=$(node -p 'require("./sdk/typescript/package.json").version')
lock_version=$(node -p 'require("./sdk/typescript/package-lock.json").packages[""].version')

[[ $python_version == "$version" ]]
[[ $typescript_version == "$version" ]]
[[ $lock_version == "$version" ]]

printf 'stamped Python and TypeScript packages with version %s\n' "$version"
