#!/usr/bin/env bash
# Regenerate the vendored protobuf/gRPC stubs from proto/kms/v1/kms.proto.
#
# Requires grpcio-tools (pip install grpcio-tools). Run from anywhere:
#   sdk/python/gen.sh
#
# The generated grpc module imports the message module absolutely; we rewrite
# that to the vendored package path so the stubs are self-contained under
# kms_paramstore._gen (no top-level "kms" package is added to the import
# namespace).
set -euo pipefail

sdk_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"   # sdk/python
repo_root="$(cd "$sdk_dir/../.." && pwd)"
proto_dir="$repo_root/proto"
out_pkg="$sdk_dir/kms_paramstore/_gen"

if [[ "${1:-}" == "--check" ]]; then
  exec python3 "$sdk_dir/scripts/check_generated.py"
fi
if [[ $# -ne 0 ]]; then
  echo "usage: $0 [--check]" >&2
  exit 2
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

python3 -m grpc_tools.protoc \
  -I"$proto_dir" \
  --python_out="$tmp" \
  --grpc_python_out="$tmp" \
  --pyi_out="$tmp" \
  "$proto_dir/kms/v1/kms.proto"

cp "$tmp/kms/v1/kms_pb2.py"      "$out_pkg/kms_pb2.py"
cp "$tmp/kms/v1/kms_pb2.pyi"     "$out_pkg/kms_pb2.pyi"
cp "$tmp/kms/v1/kms_pb2_grpc.py" "$out_pkg/kms_pb2_grpc.py"

# Make the grpc stub import the message module from the vendored location.
# perl -i is portable across GNU and BSD/macOS (unlike `sed -i`).
perl -i -pe 's/^from kms\.v1 import kms_pb2 as/from kms_paramstore._gen import kms_pb2 as/' \
  "$out_pkg/kms_pb2_grpc.py"

echo "Regenerated stubs in $out_pkg"
