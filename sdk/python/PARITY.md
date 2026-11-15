# Python SDK parity ledger

Python v0.3 follows the language-neutral behavior of the current Go and
TypeScript SDKs. Framework-specific TypeScript features (browser/Next.js
publishing) and administrative control-plane RPCs are intentionally outside the
Python application SDK.

| Capability | Sync | Async | Executable evidence |
|---|---:|---:|---|
| Parameter values, metadata, listing, mutation | yes | yes | `tests/test_core_parity.py`, `tests/test_async_client.py` |
| Secret values, inventory, lifecycle, redaction | yes | yes | `tests/test_secrets.py`, `tests/test_core_parity.py`, `tests/test_async_client.py` |
| Versioned binding transitions plus bound-cohort and unbound-version purge | yes | yes | `tests/test_secrets.py`, `tests/test_async_client.py`, `tests/test_release.py`, `tests/test_async_release.py` |
| Namespace discovery and explicit cross-namespace refs | yes | yes | `tests/test_namespace.py`, `tests/test_async_client.py` |
| Immutable pages, models, typed errors | yes | yes | `tests/test_core_parity.py` |
| Resumable namespace watch and value-free health | yes | yes | `tests/test_watch.py`, `tests/test_async_client.py` |
| Release loading and acknowledgements | yes | yes | `tests/test_release.py`, `tests/test_async_release.py` |
| Hash-only defaults verification/application | yes | yes | `tests/test_core_parity.py`, `tests/test_configstore_verify.py` |
| Managed Pydantic configuration and generation | yes | yes | `tests/test_configstore_*.py`, `tests/test_configgen.py` |

`scripts/check_protocol_coverage.py` fails when a new RPC is neither implemented
nor explicitly classified. Package builds generate protobuf bindings from
`proto/kms/v1/kms.proto`. `scripts/check_distribution.py` verifies standalone
source/wheel builds and imports the generated bindings with the minimum declared
runtime versions, including a strict editable install.
