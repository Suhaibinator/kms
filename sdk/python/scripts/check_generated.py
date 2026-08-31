#!/usr/bin/env python3
"""Regenerate protobuf bindings in a temporary directory and compare bytes."""

from __future__ import annotations

import difflib
import pathlib
import subprocess
import sys
import tempfile


def main() -> None:
    sdk = pathlib.Path(__file__).resolve().parents[1]
    root = sdk.parents[1]
    proto = root / "proto" / "kms" / "v1" / "kms.proto"
    committed = sdk / "kms_paramstore" / "_gen"
    names = ("kms_pb2.py", "kms_pb2.pyi", "kms_pb2_grpc.py")
    with tempfile.TemporaryDirectory() as directory:
        out = pathlib.Path(directory)
        subprocess.run(
            [
                sys.executable, "-m", "grpc_tools.protoc", f"-I{root / 'proto'}",
                f"--python_out={out}", f"--grpc_python_out={out}", f"--pyi_out={out}",
                str(proto),
            ],
            check=True,
        )
        generated = out / "kms" / "v1"
        grpc_file = generated / "kms_pb2_grpc.py"
        grpc_file.write_text(
            grpc_file.read_text().replace(
                "from kms.v1 import kms_pb2 as",
                "from kms_paramstore._gen import kms_pb2 as",
            )
        )
        failed = False
        for name in names:
            actual = (committed / name).read_text().splitlines(keepends=True)
            wanted = (generated / name).read_text().splitlines(keepends=True)
            if actual != wanted:
                failed = True
                sys.stderr.writelines(
                    difflib.unified_diff(actual, wanted, fromfile=f"committed/{name}", tofile=f"generated/{name}")
                )
        if failed:
            raise SystemExit("generated Python protobuf bindings are stale; run sdk/python/gen.sh")
    print("Python protobuf bindings are current")


if __name__ == "__main__":
    main()
