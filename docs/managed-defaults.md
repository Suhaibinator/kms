# Managed defaults export and import

Managed defaults let an application publish its source-owned, non-secret
configuration baseline to an existing KMS application namespace. KMS owns the
artifact format, validation, transport, preview, conflict detection, and atomic
write. The application owns only its defaults provider, profile-to-namespace
mapping, and a tiny wrapper command.

Secrets are deliberately outside this workflow. Add or rotate them with the
normal secret-management commands or console before creating the first
release. A defaults artifact contains the complete parameter/secret contract,
but it has no field capable of carrying a secret value.

## Export from an application

Generated Go bindings expose `EncodeDefaultsArtifact`. An application command
only wires its existing defaults provider to the KMS runner:

```go
package main

import (
    "os"

    appconfig "example.com/service/config"
    "example.com/service/configkms"
    "github.com/Suhaibinator/kms/sdk/go/configstore"
)

func main() {
    os.Exit(configstore.RunDefaultsExporter(
        os.Args[1:], os.Stdout, os.Stderr,
        appconfig.ManagedReleaseDefaults,
        configkms.EncodeDefaultsArtifact,
    ))
}
```

Generate an artifact for one source profile:

```bash
go run ./cmd/export-kms-defaults --profile dev --output defaults.dev.json
```

The TypeScript SDK provides the equivalent `runDefaultsExporter` and generated
`encodeDefaultsArtifact` entry points. Python generated stores expose
`defaults_artifact`, `export_defaults`, and sync/async `verify_defaults`;
`Client.apply_application_defaults` and its async equivalent preview or apply
the resulting artifact. See the
[`Python managed-configuration guide`](../sdk/python/MANAGED_CONFIG.md).

The deterministic `kms-config-defaults/v1` JSON records the profile, generated
schema SHA-256, complete contract, and exact encoded value for every parameter
alias. Entries are sorted. Commit the generated schema and contract as usual;
the defaults artifact itself may be generated when an operator needs it.

## Preview and apply directly from an application

Most Go applications do not need to write an artifact or invoke the
`parameter-store` binary. `RunDefaultsApplier` generates the artifact in
memory, resolves the application-owned profile to a namespace, connects to
KMS, performs a fresh preview, and optionally executes that exact plan:

```go
package main

import (
    "os"

    appconfig "example.com/service/config"
    "example.com/service/configkms"
    "github.com/Suhaibinator/kms/sdk/go/configstore"
)

func main() {
    os.Exit(configstore.RunDefaultsApplier(
        os.Args[1:], os.Stdout, os.Stderr,
        configstore.DefaultsApplierConfig[appconfig.Profile, appconfig.Config]{
            Provider: appconfig.ManagedReleaseDefaults,
            Encoder:  configkms.EncodeDefaultsArtifact,
            Namespace: appconfig.KMSNamespaceForProfile,
        },
    ))
}
```

Preview is the default. The runner reads the admin identity token from
`KMS_TOKEN`; it deliberately has no token flag, keeping bearer credentials out
of process listings and shell history:

```bash
export KMS_TOKEN
go run ./cmd/apply-kms-defaults \
  --profile dev \
  --endpoint localhost:8443 \
  --insecure
```

Execute the fresh preview plan with `--execute`. Existing identical values are
reported as `unchanged` and create no versions or revisions. Differing values
remain blocked until the operator also passes `--overwrite`. If preview reports
that the application definition differs, execution additionally requires
`--update-definition`:

```bash
go run ./cmd/apply-kms-defaults \
  --profile dev \
  --endpoint localhost:8443 \
  --insecure \
  --overwrite \
  --update-definition \
  --execute
```

TLS uses system roots by default, or `--ca`; mTLS additionally uses `--cert`
and `--key`. Production execution requires
`--confirm-production <environment>`.

## Verify code defaults against a release (CI)

Applying defaults is a write; verifying is the matching read. The verify test
hashes the source-owned parameter groups for a profile and asks KMS whether
the active release still pins the same canonical documents. Only lowercase
hex SHA-256 hashes travel to the server and only bounded verdicts come back,
so it is safe to run from any pipeline that can hold a token. Six lines:

```go
func TestReleaseMatchesSourceDefaults(t *testing.T) {
    kmsverify.Run(t, kmsverify.Spec[appconfig.Config]{
        Defaults: appconfig.ManagedReleaseDefaults,
        Verify:   configkms.VerifyReleaseDefaults,
    })
}
```

Connection and selection come from the environment:

| Variable | Meaning |
|---|---|
| `KMS_VERIFY_ENDPOINT` | KMS gRPC `host:port`. Unset: skip (or fail with `KMS_VERIFY_REQUIRED`). |
| `KMS_VERIFY_TOKEN` | Token of the verification identity. |
| `KMS_VERIFY_CA_FILE` / `KMS_VERIFY_CA_PEM` | CA bundle path or PEM text (mutually exclusive); system roots otherwise. |
| `KMS_VERIFY_PROFILE` | Defaults profile; empty selects the binding's default. |
| `KMS_VERIFY_NAMESPACE` | `env/app` to compare. Required unless `Spec.Namespace` derives it from the profile. |
| `KMS_VERIFY_RELEASE` | Release name; default `runtime`. |
| `KMS_VERIFY_REQUIRED` | Truthy turns a missing endpoint into a failure. |
| `KMS_VERIFY_INSECURE` | Cleartext; loopback endpoints only. |

Each parameter alias in the generated contract receives one verdict:

| Verdict | Meaning |
|---|---|
| `match` | The release pins a parameter whose canonical hash equals the source default. |
| `differs` | The release pins a different value (an override is active, or code moved on). |
| `missing_in_release` | The contract names an alias the active release does not pin. |
| `unknown_alias` | The alias is not part of the application definition on the server. |
| `secret_alias` | The alias is a secret; secrets are never compared. |
| `unsupported_content_type` | The server cannot canonicalize this content type. |
| `unverified` | Count of parameter aliases the release pins that the contract did not mention (new parameters in KMS that code does not know about). |

The run passes only when the schema digest matches and every alias is
`match`. Exit semantics: `kmsverify.Run` skips the test when
`KMS_VERIFY_ENDPOINT` is unset, fails it when `KMS_VERIFY_REQUIRED` is set and
the endpoint is missing, fails it with the value-free report when the
comparison does not pass, and fails it with the error for configuration or
transport problems (unreadable CA, non-loopback insecure endpoint, no
namespace, `kmsclient.ErrRateLimited` when the per-identity verify budget is
spent). `kmsverify.Verify` returns the `VerifyResult` for scripts that decide
themselves.

The verification identity needs the `configuration-release:verify-defaults`
operation on each namespace it checks. Create it **without** a home namespace
so it holds no implicit read grant, then allow exactly that implemented
operation per `env/app`. These are admin commands against a live server, so on
a TLS deployment supply the operator's admin client certificate as well as the
token (`--cert`/`--key`, or `KMS_CLIENT_CERT_FILE`/`KMS_CLIENT_KEY_FILE`; see
[`operations.md`](operations.md#admin-credentials-and-browser-setup)) —
connection flags are omitted here for readability:

```bash
parameter-store admin identity create ci-verify-payments --kind client --auth token
parameter-store admin policy create ci-verify-payments \
  --subject ci-verify-payments \
  --allow configuration-release:verify-defaults@staging/payments \
  --allow configuration-release:verify-defaults@prod/payments
```

Run the test on pushes to the main branch and on version tags, never on pull
requests: a pull request from a fork would otherwise execute with the
repository's verification token, and a failing verify on an unmerged branch
says nothing about what is deployed. The
[consumer adoption guide](consumer-adoption.md) has the workflow template.

## Preview and apply an exported artifact

The application and target namespace must already exist. Preview is the safe
default:

```bash
parameter-store defaults apply dev/payments \
  --from defaults.dev.json \
  --endpoint localhost:8443 \
  --token "$KMS_TOKEN"
```

Each parameter is reported as `create`, `unchanged`, `update`, or `blocked`.
Existing identical values are skipped. A differing value is blocked unless
the operator explicitly supplies `--overwrite`.

If the artifact contract or schema digest differs from the existing
application definition, preview reports the definition change but never writes
it. Register the generated schema for the application (KMS derives its release
name), then pass `--update-definition` to explicitly allow execution to replace
the contract and repin the matching registered schema version. KMS selects by
digest; callers never guess a version number.

Execute uses a fresh server preview and its opaque plan digest:

```bash
parameter-store defaults apply dev/payments \
  --from defaults.dev.json \
  --overwrite \
  --update-definition \
  --execute \
  --endpoint localhost:8443 \
  --token "$KMS_TOKEN"
```

Use `--from -` to read the artifact from standard input. Production
environments additionally require `--confirm-production ENV` with the exact
target environment name.

Apply never creates applications, namespaces, schemas, secrets, or releases.
With explicit definition-update permission, the server can replace the
existing application's contract and repin an already registered schema whose
canonical digest matches the artifact. That definition update and all created
or updated parameter versions commit in one transaction. The server also
verifies alias resolution, namespace incarnation, release state, resource
inventory, and current parameter versions; a stale plan writes nothing and
must be previewed again.

After defaults are applied and required secrets have been added manually, the
console can Ship with zero edits to create and activate the first release. An
established release still requires an explicit change.
