#!/usr/bin/env bash

set -euo pipefail

version=${1:-}
output_dir=${2:-dist/release}

if [[ ! $version =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  printf 'usage: %s X.Y.Z [OUTPUT_DIR]\n' "$0" >&2
  exit 2
fi
if [[ ! -f frontend/out/index.html ]]; then
  printf 'frontend/out/index.html is missing; build the frontend first\n' >&2
  exit 1
fi

repo_root=$(git rev-parse --show-toplevel)
mkdir -p "$output_dir"
output_dir=$(cd "$output_dir" && pwd)
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

ldflags="-s -w -X github.com/Suhaibinator/kms/internal/cli.Version=v$version"
targets=(
  linux/amd64
  linux/arm64
  darwin/amd64
  darwin/arm64
  windows/amd64
)

for target in "${targets[@]}"; do
  goos=${target%/*}
  goarch=${target#*/}
  archive_root="kms_${version}_${goos}_${goarch}"
  stage="$work_dir/$archive_root"
  mkdir -p "$stage"

  extension=
  if [[ $goos == windows ]]; then
    extension=.exe
  fi

  printf 'building %s/%s\n' "$goos" "$goarch"
  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -mod=readonly -trimpath -ldflags "$ldflags" \
    -o "$stage/parameter-store$extension" ./cmd/parameter-store
  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -mod=readonly -trimpath -ldflags "$ldflags" \
    -o "$stage/kms-config-gen$extension" ./cmd/kms-config-gen
  cp "$repo_root/README.md" "$repo_root/LICENSE" "$stage/"

  if [[ $goos == windows ]]; then
    (
      cd "$work_dir"
      zip -X -q -r "$output_dir/$archive_root.zip" "$archive_root"
    )
  else
    COPYFILE_DISABLE=1 tar -C "$work_dir" -czf "$output_dir/$archive_root.tar.gz" "$archive_root"
  fi
  rm -rf "$stage"
done

printf 'built release archives in %s\n' "$output_dir"
