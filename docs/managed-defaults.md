# Managed defaults export and import

Managed defaults let an application publish its source-owned, non-secret
configuration baseline to an existing KMS application namespace. KMS owns the
artifact format, validation, preview, conflict detection, and atomic write.
The application owns only its defaults provider and a tiny exporter command.

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

## Preview and apply

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

Execute uses a fresh server preview and its opaque plan digest:

```bash
parameter-store defaults apply dev/payments \
  --from defaults.dev.json \
  --overwrite \
  --execute \
  --endpoint localhost:8443 \
  --token "$KMS_TOKEN"
```

Use `--from -` to read the artifact from standard input. Production
environments additionally require `--confirm-production ENV` with the exact
target environment name.

Apply never creates applications, namespaces, schemas, secrets, or releases.
The server verifies the exact application contract, any pinned schema digest,
alias resolution, namespace incarnation, and current parameter versions. All
created or updated parameter versions commit in one transaction; a stale plan
writes nothing and must be previewed again.

After defaults are applied and required secrets have been added manually, the
console can Ship with zero edits to create and activate the first release. An
established release still requires an explicit change.
