# Removing per-secret access tokens

KMS no longer generates or accepts per-secret access tokens. Clients authenticate
with their normal identity credentials and remain subject to namespace
permissions. Bound secret versions additionally require their binding key.
Identity tokens and binding-key encryption are unchanged.

## Existing databases

Independent schema release tracks require a fresh database using baseline 3.
Existing databases, including baselines 1 and 2, are rejected without being
upgraded, converted, or deleted. The earlier token-removal upgrade from baseline
1 to 2 is no longer supported by the current server.

Retain the existing database separately for rollback and audit. Provision a new
database explicitly and repopulate it through the current resource APIs; do not
copy old release rows into it. Deploy the updated server, SDKs, and regenerated
configuration clients together. See
[schema selection and deployment cutover](configuration-releases.md#schema-selection-and-deployment-cutover)
for runtime selection requirements and database inspection limits.

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
