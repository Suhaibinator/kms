#!/usr/bin/env python3
"""Exercise standalone sdist/wheel builds, minimum runtimes and editable installs."""

from __future__ import annotations

from email.parser import BytesParser
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import venv
import zipfile

from packaging.requirements import Requirement


SDK = Path(__file__).resolve().parents[1]
GENERATED = ("kms_pb2.py", "kms_pb2.pyi", "kms_pb2_grpc.py")
SMOKE = """
import importlib.util
from pathlib import Path
import kms_paramstore
from kms_paramstore._gen import kms_pb2, kms_pb2_grpc
assert importlib.util.find_spec('grpc_tools') is None
assert Path(kms_pb2.__file__).with_suffix('.pyi').is_file()
message = kms_pb2.NamespaceRef(env='test', app='distribution')
assert kms_pb2.NamespaceRef.FromString(message.SerializeToString()) == message
assert callable(kms_pb2_grpc.ParameterServiceStub)
"""


def run(*args: str, cwd: Path) -> None:
    subprocess.run(args, cwd=cwd, check=True)


def main() -> None:
    with tempfile.TemporaryDirectory(prefix="kms-distribution-") as directory:
        root = Path(directory)
        artifacts = root / "artifacts"
        run(sys.executable, "-m", "build", "--sdist", "--outdir", str(artifacts),
            str(SDK), cwd=root)
        archive, = artifacts.glob("*.tar.gz")
        with tarfile.open(archive) as source:
            names = source.getnames()
            assert any(name.endswith("/proto/kms/v1/kms.proto") for name in names)
            assert not any(name.endswith(f"/_gen/{file}") for name in names for file in GENERATED)
            # The archive was just built from this checkout, but still use safe extraction.
            source.extractall(root / "source", filter="data")
        standalone, = (root / "source").iterdir()
        run(sys.executable, "-m", "build", "--wheel", "--outdir", str(artifacts),
            str(standalone), cwd=root)
        wheel, = artifacts.glob("*.whl")
        with zipfile.ZipFile(wheel) as package:
            for name in GENERATED:
                assert f"kms_paramstore/_gen/{name}" in package.namelist()
            metadata_name, = [name for name in package.namelist() if name.endswith(".dist-info/METADATA")]
            metadata = BytesParser().parsebytes(package.read(metadata_name))
        minimums = []
        for raw in metadata.get_all("Requires-Dist", []):
            requirement = Requirement(raw)
            if requirement.name in {"grpcio", "protobuf"}:
                minimum, = [spec.version for spec in requirement.specifier if spec.operator == ">="]
                minimums.append(f"{requirement.name}=={minimum}")
        assert len(minimums) == 2
        environment = root / "venv"
        venv.EnvBuilder(with_pip=True).create(environment)
        python = str(environment / ("Scripts/python.exe" if sys.platform == "win32" else "bin/python"))
        run(python, "-m", "pip", "install", str(wheel), *minimums, cwd=root)
        run(python, "-I", "-c", SMOKE, cwd=root)
        # Strict mode exercises setuptools' generated-file output mapping too.
        run(python, "-m", "pip", "install", "--no-deps", "--editable", str(standalone),
            "--config-settings", "editable_mode=strict", cwd=root)
        run(python, "-I", "-c", SMOKE, cwd=root)
        print("Standalone sdist, wheel, minimum runtimes and strict editable install passed")


if __name__ == "__main__":
    main()
