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

if ! tag_commit=$(git rev-parse --verify "refs/tags/$tag^{commit}" 2>/dev/null); then
  fail "refs/tags/$tag must exist and point to a commit"
fi

if ! git rev-parse --verify --quiet "$main_ref^{commit}" >/dev/null; then
  fail "main ref '$main_ref' does not exist"
fi
if ! git merge-base --is-ancestor "$tag_commit" "$main_ref"; then
  fail "tagged commit $tag_commit is not reachable from $main_ref"
fi

version=${tag#v}
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

printf 'validated %s at %s (release version %s)\n' "$tag" "$tag_commit" "$version"
