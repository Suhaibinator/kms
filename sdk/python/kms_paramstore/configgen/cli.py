"""Command-line entry point for deterministic Python config generation."""

from __future__ import annotations

import argparse
import importlib
import sys
from typing import Sequence

from pydantic import BaseModel

from .generator import StaleArtifactsError, generate_artifacts, write_artifacts


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="kms-config-gen-py")
    parser.add_argument("--model", required=True, help="Pydantic model as module:Class")
    parser.add_argument("--binding", required=True)
    parser.add_argument("--schema", required=True)
    parser.add_argument("--contract", required=True)
    parser.add_argument("--check", "--verify", action="store_true", dest="check")
    options = parser.parse_args(argv)
    try:
        module_name, separator, type_name = options.model.partition(":")
        if not separator or not module_name or not type_name:
            raise ValueError("--model must use module:Class")
        model = getattr(importlib.import_module(module_name), type_name)
        if not isinstance(model, type) or not issubclass(model, BaseModel):
            raise TypeError("selected object is not a Pydantic model")
        artifacts = generate_artifacts(model, source_module=module_name, source_type=type_name)
        write_artifacts(
            artifacts, binding=options.binding, schema=options.schema,
            contract=options.contract, check=options.check,
        )
    except StaleArtifactsError as error:
        print(str(error), file=sys.stderr)
        return 1
    except Exception as error:
        print(f"configgen: {error}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
