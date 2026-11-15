"""Setuptools hooks for generated SDK bindings and standalone source archives."""

import ast
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

from setuptools import setup
from setuptools.command.build_py import build_py
from setuptools.command.sdist import sdist

GENERATED_NAMES = ("kms_pb2.py", "kms_pb2.pyi", "kms_pb2_grpc.py")
SCHEMA = Path("proto/kms/v1/kms.proto")
SDK = Path(__file__).resolve().parent


def schema_root() -> Path:
    # Source distributions carry their own schema; checkouts use the canonical one.
    root = SDK if (SDK / SCHEMA).is_file() else SDK.parents[1]
    if not (root / SCHEMA).is_file():
        raise RuntimeError(f"Missing protobuf schema: {root / SCHEMA}")
    return root


def generate(directory: Path) -> Path:
    root = schema_root()
    subprocess.run(
        [sys.executable, "-m", "grpc_tools.protoc", f"-I{root / 'proto'}",
         f"--python_out={directory}", f"--grpc_python_out={directory}",
         f"--pyi_out={directory}", str(root / SCHEMA)],
        check=True,
    )
    output = directory / "kms/v1"
    stub = output / "kms_pb2_grpc.py"
    source = stub.read_text(encoding="utf-8")
    original = "from kms.v1 import kms_pb2 as"
    if source.count(original) != 1:
        raise RuntimeError("Unexpected generated gRPC import; update the build adapter")
    stub.write_text(source.replace(original, "from kms_paramstore._gen import kms_pb2 as"),
                    encoding="utf-8")
    return output


def runtime_requirements(generated: Path) -> list[str]:
    """Read the actual runtime guards, without importing the generated SDK."""
    grpc_tree = ast.parse((generated / "kms_pb2_grpc.py").read_text(encoding="utf-8"))
    grpc_versions = [
        ast.literal_eval(node.value)
        for node in grpc_tree.body if isinstance(node, ast.Assign)
        if any(isinstance(target, ast.Name) and target.id == "GRPC_GENERATED_VERSION"
               for target in node.targets)
    ]
    proto_tree = ast.parse((generated / "kms_pb2.py").read_text(encoding="utf-8"))
    proto_versions = [
        [ast.literal_eval(arg) for arg in node.args[1:5]]
        for node in ast.walk(proto_tree) if isinstance(node, ast.Call)
        if isinstance(node.func, ast.Attribute)
        and node.func.attr == "ValidateProtobufRuntimeVersion"
    ]
    if len(grpc_versions) != 1 or len(proto_versions) != 1:
        raise RuntimeError("Generated runtime guards changed; update the build adapter")
    major, minor, patch, suffix = proto_versions[0]
    if suffix:
        raise RuntimeError("Prerelease protobuf generators are not supported")
    return [f"grpcio>={grpc_versions[0]}", f"protobuf>={major}.{minor}.{patch},<{major + 1}"]


class BuildBindings(build_py):
    def run(self):
        super().run()
        destination = self.binding_directory()
        destination.mkdir(parents=True, exist_ok=True)
        for name in GENERATED_NAMES:
            shutil.copyfile(generated / name, destination / name)

    def binding_directory(self):
        root = SDK if self.editable_mode else Path(self.build_lib)
        return root / "kms_paramstore/_gen"

    def get_outputs(self, include_bytecode=1):
        return list(dict.fromkeys([
            *super().get_outputs(include_bytecode),
            *(str(Path(self.build_lib) / "kms_paramstore/_gen" / name)
              for name in GENERATED_NAMES),
        ]))

    def get_output_mapping(self):
        mapping = super().get_output_mapping()
        if self.editable_mode:
            mapping.update({
                str(Path(self.build_lib) / "kms_paramstore/_gen" / name):
                str(SDK / "kms_paramstore/_gen" / name)
                for name in GENERATED_NAMES
            })
        return mapping


class SourceWithSchema(sdist):
    def make_release_tree(self, base_dir, files):
        # An editable install may have left bindings in the source tree.
        generated_paths = {Path("kms_paramstore/_gen") / name for name in GENERATED_NAMES}
        files = [path for path in files if Path(path) not in generated_paths]
        super().make_release_tree(base_dir, files)
        destination = Path(base_dir) / SCHEMA
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(schema_root() / SCHEMA, destination)


# Metadata and wheel builds use the same generated guards, so upgrading the
# compiler automatically updates the wheel's runtime dependency minimums.
with tempfile.TemporaryDirectory(prefix="kms-protobuf-") as temporary:
    generated = generate(Path(temporary))
    setup(
        install_requires=["pydantic>=2.13,<3", *runtime_requirements(generated)],
        cmdclass={"build_py": BuildBindings, "sdist": SourceWithSchema},
    )
