# Removing per-secret access tokens

KMS no longer generates or accepts per-secret access tokens. Clients authenticate
with their normal identity credentials and remain subject to namespace
permissions. Bound secret versions additionally require their binding key.
Identity tokens and binding-key encryption are unchanged.

## Existing databases

Back up the SQLite database and stop all processes using it before upgrading.
On open, KMS upgrades the exact supported schema-version-1 database to version 2
in a transaction. The upgrade removes `secrets.access_token_hash` and
`secret_versions.has_access_token`, preserving secret values, ciphertext,
versions, labels, binding state, and all other data. Fresh databases use version 2.

The upgrade requires every token hash to be null or empty and every token flag
to be zero, including historical versions. If any token is in use, KMS refuses
the upgrade without removing its protection or data. Read-only validation checks
eligibility without performing the upgrade. Unsupported schemas are also rejected.
An old KMS binary cannot open the upgraded database; restore the backup if a
binary downgrade is required.

## Applications and tooling

Update the server, CLI, and SDK dependencies together. Remove per-secret token
arguments, token-generation options, token-provider callbacks, and retired
`KMS_SECRET_TOKEN_*` environment variables from application deployment settings.
Keep identity authentication and binding keys in place. Go, Python, and
TypeScript secret writes now return only their version and revision.

Imports still produce source-key-to-destination-path reports, with no credentials.
Release loaders resolve bound versions using their alias binding keys. A missing
key rejects a candidate as `binding_key_unavailable`; unsuccessful reads use
`resolution_failed`.

The removed protobuf field numbers and names are reserved. The server explicitly
rejects the retired token-generation field from an old writer so it cannot
silently discard requested protection. HTTP requests with removed fields fail
strict request validation.
