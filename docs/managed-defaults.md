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
`encodeDefaultsArtifact` entry points.

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
it. Register the generated schema under the application's existing schema ID,
then pass `--update-definition` to explicitly allow execution to replace the
contract and repin the matching registered schema version. KMS selects by
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
