# Managed configuration

The Python SDK uses Pydantic v2 as its model and validation machinery while
following the same managed-release semantics as the Go and TypeScript SDKs.
Every non-secret field belongs to a JSON parameter group and has a source
default. Every secret is a required, default-free `Secret` field.

```python
from typing import Annotated

from pydantic import BaseModel, ConfigDict
from kms_paramstore import Secret
from kms_paramstore.configstore import ConfigBinding, Parameter, SecretField

class AppConfig(BaseModel):
    model_config = ConfigDict(
        frozen=True, strict=True, extra="forbid", arbitrary_types_allowed=True
    )

    port: Annotated[int, Parameter("runtime", reload="restart")] = 8080
    debug: Annotated[bool, Parameter("runtime")] = False
    password: Annotated[Secret, SecretField("db_password")]

binding = ConfigBinding(AppConfig, {})
```

Generate the typed binding, Draft 2020-12 schema, and contract together:

```console
kms-config-gen-py --model app.config:AppConfig \
  --binding app/config_generated.py \
  --schema app/config.schema.json \
  --contract app/config.contract.json
```

Use `--check` in CI. It compares all three files without rewriting them.

The generated module is the normal runtime entry point. Construct its typed
store from source defaults, then start the sync or asyncio manager:

```python
from app.config import AppConfig
from app.config_generated import GeneratedConfigStore
from kms_paramstore.configstore import Callbacks

store = GeneratedConfigStore(AppConfig(password=bootstrap_password))
manager = store.start(
    client,
    release="runtime",
    callbacks=Callbacks(
        on_applied=on_applied,
        on_candidate_rejected=on_candidate_rejected,
        on_default_mismatch=on_default_mismatch,
    ),
)

snapshot = store.current
serve(port=snapshot.port, password=snapshot.password)
```

Use `await store.start_async(async_client, ...)` with `AsyncClient`. A binding
may be started only once; create a new binding for a separate lifecycle.

Candidate documents reject duplicate, unknown, missing, malformed, non-finite,
or incorrectly typed values. Source-default differences are reported and still
applied. After startup, a change to any `reload="restart"` field rejects the
entire candidate, leaving the last known good snapshot active. Snapshot and
view reads return defensive copies, and secret changes appear in reports only
as field paths.

Use `kms_paramstore.configstore.Duration` for Go-compatible duration fields;
it retains the full signed 64-bit nanosecond range. `datetime.timedelta` is
intentionally rejected because its microsecond precision cannot represent the
wire format faithfully. Config generation also rejects decoded-byte length
constraints, which JSON Schema cannot express for base64 strings without
misstating their meaning.

`encode_defaults_artifact` and `export_defaults` produce parameter-only
`kms-config-defaults/v1` artifacts. `verify_defaults` sends only canonical
SHA-256 hashes to KMS and returns a value-free result suitable for CI logs.
The generated store exposes these as `defaults_artifact`, `export_defaults`,
`verify_defaults`, and `verify_defaults_async`.
