#!/usr/bin/env bash

set -euo pipefail

tag=${1:-${GITHUB_REF_NAME:-}}
main_ref=${2:-origin/main}

fail() {
  printf 'release validation failed: %s\n' "$*" >&2
  exit 1
}

if [[ ! $tag =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  fail "tag must be a stable semantic version in the form vX.Y.Z (got '$tag')"
fi

if [[ $(git cat-file -t "refs/tags/$tag" 2>/dev/null || true) != tag ]]; then
  fail "refs/tags/$tag must be an annotated tag"
fi

tag_commit=$(git rev-list -n 1 "refs/tags/$tag")
if ! git rev-parse --verify --quiet "$main_ref^{commit}" >/dev/null; then
  fail "main ref '$main_ref' does not exist"
fi
if ! git merge-base --is-ancestor "$tag_commit" "$main_ref"; then
  fail "tagged commit $tag_commit is not reachable from $main_ref"
fi

version=${tag#v}
python_version=$(python3 -c 'import pathlib, tomllib; print(tomllib.loads(pathlib.Path("sdk/python/pyproject.toml").read_text())["project"]["version"])')
typescript_version=$(node -p 'require("./sdk/typescript/package.json").version')
lock_version=$(node -p 'require("./sdk/typescript/package-lock.json").packages[""].version')

[[ $python_version == "$version" ]] || fail "Python version is $python_version; expected $version"
[[ $typescript_version == "$version" ]] || fail "TypeScript version is $typescript_version; expected $version"
[[ $lock_version == "$version" ]] || fail "TypeScript lockfile version is $lock_version; expected $version"

major=${version%%.*}
minor=${version%.*}
short_commit=${tag_commit:0:12}

if [[ -n ${GITHUB_OUTPUT:-} ]]; then
  {
    printf 'tag=%s\n' "$tag"
    printf 'version=%s\n' "$version"
    printf 'major=%s\n' "$major"
    printf 'minor=%s\n' "$minor"
    printf 'commit=%s\n' "$tag_commit"
    printf 'short_commit=%s\n' "$short_commit"
  } >>"$GITHUB_OUTPUT"
fi

printf 'validated %s at %s (package version %s)\n' "$tag" "$tag_commit" "$version"
