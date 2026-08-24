# Managed Go configuration

The managed Go configuration API builds an application-specific, typed store
on top of `kmsclient.ReleaseLoader`. The loader continues to own release
watching, exact pin resolution, digest checks, acknowledgements,
supersession, and last-known-good behavior. Generated bindings add strict
decoding, validation, application-owned defaults, drift policy, immutable
snapshots, typed consumer views, and deterministic schema artifacts.

For a runnable walkthrough, use:

```bash
go run ./examples/managed-config
```

The [`examples/managed-config`](../examples/managed-config) app demonstrates
typed views, immutable snapshots, atomic hot override, default-divergence
reporting, restart-required rejection, secret redaction, and last-known-good
preservation against a deterministic in-process KMS.

Use this layer when a set of configuration fields must change as one release.
The lower-level `ReleaseLoader`, `RunTypedRelease`, `ParameterValue`, and
`SecretValue` APIs remain supported.

## Operating model

Application source is authoritative for every non-secret default. A normal KMS
release mirrors those values. Ordinary changes update source through code
review and deploy with the application; a KMS-only hot change is an emergency
override.

At startup, the generated store resolves and validates the active release and
compares every non-secret field with the application defaults. A mismatch
fails startup unless the application explicitly passes a bypass. A bypassed
startup is still reported at error severity and remains visibly divergent.
After startup, valid changes to `reload=hot` fields apply atomically and are
reported as divergent when they differ from source defaults. A later release
that restores all defaults clears divergence.

Changes to any `reload=restart` field reject the complete runtime candidate.
No hot subset is applied. The active generation remains last-known-good.

## Declare the root type

Declare one root struct. Each exported field is either managed or explicitly
excluded:

```go
package config

import (
    "fmt"
    "time"

    "github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

type Config struct {
    DBHost string `json:"db_host" kms:"group=database,reload=restart" kms_views:"persistence_handler,database_health"`
    DBPort int    `json:"db_port" kms:"group=database,reload=restart" kms_views:"persistence_handler,database_health"`

    DBQueryTimeout time.Duration `json:"db_query_timeout" kms:"group=database,reload=hot" kms_views:"persistence_handler,database_health"`
    RequestLimit   int           `json:"request_limit" kms:"group=rate_limits,reload=hot" kms_views:"api_handler,background_jobs"`

    DBPassword kmsclient.Secret `json:"-" kms:"secret=db_password,reload=restart" kms_views:"persistence_handler"`

    LocalOnly string `kms:"-"`
}

func (c *Config) Validate() error {
    if c.DBHost == "" {
        return fmt.Errorf("db_host is required")
    }
    if c.DBPort < 1 || c.DBPort > 65535 {
        return fmt.Errorf("db_port must be between 1 and 65535")
    }
    if c.DBQueryTimeout <= 0 {
        return fmt.Errorf("db_query_timeout must be positive")
    }
    if c.RequestLimit <= 0 {
        return fmt.Errorf("request_limit must be positive")
    }
    if c.DBPassword.IsZero() {
        return fmt.Errorf("db_password is required")
    }
    return nil
}
```

The minimal application contract is:

```go
type Validatable interface {
    Validate() error
}
```

The root is an exported, non-generic struct containing only exported, named
fields. The generator enforces these declaration rules:

- A parameter field has one `group` alias, one `reload=hot|restart` policy,
  an explicit `json` property name, and at least one `kms_views` membership.
- A secret field has one `secret` alias, one reload policy, `json:"-"`, at
  least one view, and the exact type `kmsclient.Secret`.
- JSON tag options such as `omitempty` and `,string` are unsupported on root
  or nested managed fields. Nested struct fields may omit the tag to use their
  Go field name, but explicit names are recommended for a stable contract.
  Explicit JSON names, storage aliases, and view names use the bounded
  `[A-Za-z][A-Za-z0-9_-]{0,63}` grammar so canonical diagnostic paths are
  unambiguous and safe to format.
- `kms:"-"` excludes a field; excluded fields cannot declare views and must
  still use a structurally deep-cloneable supported type. Opaque structs with
  unexported state, embedded fields, recursive values, and other unsupported
  type graphs fail generation rather than being shallow-copied.
- One field cannot declare both `group` and `secret`. Aliases and generated
  view methods cannot conflict.
- Legacy `ParameterValue` and `SecretValue` handles cannot be managed fields;
  they own independent mutable/subscription state. Keep them outside the
  generated root; `kms:"-"` does not bypass structural clone checks.

A storage group and a consumer view are different concepts. A group is one
stable release alias and one complete JSON document. A field may belong to
several views, but it is decoded, validated, compared, and reloaded only once.
Physical KMS paths remain in the release manifest and are never inferred from
Go names.

### Compose an application root from shared fragments

The importing application owns the generated store, schema, and contract. A
library may publish a reusable configuration fragment, but it must not generate
an independent managed store for that fragment. Compose the library fields into
the application's one atomic release with `kms:"inline"`:

```go
package config

import commonconfig "example.com/go-common/pkg/config"

type Config struct {
    Common *commonconfig.Config `kms:"inline"`

    WorkerCount int `json:"worker_count" kms:"group=workers,reload=restart" kms_views:"workers"`
}

func (c *Config) Validate() error {
    if c.Common == nil {
        return fmt.Errorf("common config is required")
    }
    if err := c.Common.Validate(); err != nil {
        return fmt.Errorf("common config: %w", err)
    }
    if c.WorkerCount <= 0 {
        return fmt.Errorf("worker_count must be positive")
    }
    return nil
}
```

An inline field may be an exported named struct or a pointer to one. Inline
composition is recursive. The generator flattens its managed fields into the
application's global aliases, views, schema, contract, mismatch comparison,
reload admission, and immutable snapshot; it does not introduce a JSON wrapper
or a second activation boundary. Alias, view-method, and generated-method
collisions are checked across the complete composed root.

Pointer fragments must be non-nil in the supplied defaults and in every
decoded candidate. A value fragment avoids that additional state and is the
preferred form for new APIs. The application root must declare its own
`Validate() error` method directly: a promoted fragment method is not aggregate
validation. Root validation should explicitly delegate to every fragment and
then enforce application-wide invariants.

Run generation in the importing application whenever either its fields or any
inline fragment contract changes, including when upgrading the library that
owns a fragment. Commit the resulting application-owned artifacts and verify
them with `-check` in that application's CI.

## Supported encodings

Generated schema and runtime metadata come from one normalized type model:

| Go value | JSON representation |
|---|---|
| strings and named strings | string |
| booleans and named booleans | boolean |
| `int` / `uint` and named forms | portable signed/unsigned 32-bit JSON integer |
| explicitly sized integers and named forms | JSON integer bounded to the declared width |
| `float32` / `float64` and named forms | bounded JSON number |
| `time.Duration` | Go duration string such as `"30s"` |
| pointer to a supported scalar | scalar or `null` |
| struct | strict object with all encoded fields required |
| array | fixed-length array |
| slice | array or `null` |
| string-keyed map | object or `null` |
| `[]byte` | canonical base64 string or `null` |

Recursive types, interfaces, functions, channels, ambiguous embedded fields,
unsupported map keys, and other ambiguous representations fail generation.
The fixed 32-bit contract for machine-sized `int` and `uint` keeps generated
artifacts identical across `GOARCH` and ensures every schema-valid value fits
both 32-bit and 64-bit binaries. Use `int64` or `uint64` when the wider range is
part of the application contract.

Pointers, slices, maps, and `[]byte` preserve presence exactly. For collections,
JSON `null` decodes to Go `nil`; an empty array, object, or base64 string
decodes to a non-nil empty value. Default and reload comparison distinguish
those states. Mirror an intentional nil default with `null`, and initialize an
intentional empty default as `[]T{}`, `map[string]T{}`, or `[]byte{}` with the
corresponding empty JSON representation.

The KMS schema compiler asserts the generated `go-duration` and `kms-base64`
formats. Numeric parsing retains exact JSON numbers rather than rounding
through `float64`.

Strict decoding rejects malformed JSON, trailing values, duplicate or unknown
properties at any nesting level, missing required properties, wrong JSON
types, numeric overflow, missing or extra release aliases, wrong alias kinds,
and a parameter group whose content type is not `json`.

## Generate bindings and artifacts

Run generation during development when the contract changes:

```go
//go:generate go run github.com/Suhaibinator/kms/cmd/kms-config-gen -package . -type Config -binding-package configkms -binding-output ../../internal/configkms/config_kms.gen.go -schema-output ../../config/runtime.schema.json -contract-output ../../config/runtime.contract.json
```

`go generate` runs the directive with the root package directory as its working
directory, so every relative output above is resolved from that directory.
`-package` defaults literally to `.` in the generator process; it does not
search for the named type.

The binding must be emitted into a separate package whose name matches
`-binding-package`. Generated code imports the root configuration package, and
the root package must not import the generated binding. This one-way dependency
avoids an import cycle and lets a shared root type remain independent of KMS
runtime wiring.

When invoking the generator from a module root instead, select the root package
and make every output path relative to that module root. Use `-check` (or
`-verify`) in CI to compare all three committed outputs without rewriting them:

```bash
go run github.com/Suhaibinator/kms/cmd/kms-config-gen \
  -package ./pkg/config \
  -type Config \
  -binding-package configkms \
  -binding-output internal/configkms/config_kms.gen.go \
  -schema-output config/runtime.schema.json \
  -contract-output config/runtime.contract.json \
  -check
```

Within this KMS repository, `make check-configgen` is the canonical read-only
verification command used by CI.

Generation is deterministic. It never emits defaults, current KMS values,
secret plaintext, tokens, physical paths, release pins, or timestamps. Rerun
it when fields, JSON names, types, groups, views, reload policies, or secret
aliases change. A value-only KMS update does not require regeneration.

The Draft 2020-12 schema describes the alias-keyed parameter object validated
by KMS. Secrets are excluded because KMS validates them as independent pinned
entries. The versioned `kms-config-contract/v1` artifact records parameter
groups, secrets, canonical encodings, reload policies, views, and the schema
digest without storing sensitive or environment-specific data. The generated
Go binding embeds the same alias/kind/content-type contract for prefetch
validation.

## Supply application defaults

The importing application supplies a complete literal for all managed
non-secret fields:

```go
func Defaults() *commonconfig.Config {
    return &commonconfig.Config{
        DBHost:         "postgres.prod.svc",
        DBPort:         5432,
        DBQueryTimeout: 30 * time.Second,
        RequestLimit:   1_000,
        // Secrets intentionally come only from KMS.
    }
}
```

Every default `kmsclient.Secret` field must be its exact zero value: no
plaintext, path, version, or content type. Generated `Start` rejects a non-zero
secret default before starting the release loader. Secrets must come only from
the exact release pins.

The generated store structurally deep-clones the defaults immediately and
never publishes or mutates the caller's object. The declaration checks apply
to `kms:"-"` fields too, so the generator rejects a type it cannot isolate by
deep copy. Any zero value is allowed when intentional and accepted by
`Validate`. A nil slice, map, or byte default matches JSON `null`; a non-nil
empty default matches its empty JSON representation. Each canonical
non-secret field is compared once even when several views expose it; secrets
and secret metadata are never compared with source defaults.

Because defaults intentionally omit secrets while application validation may
require them, effective-default validation occurs during every candidate
preparation. The generated binding clones the saved defaults, injects that
candidate's resolved secrets, and calls `Validate` on the complete temporary
value. It separately validates the fully decoded candidate. Validation may
therefore run repeatedly as releases arrive; it should be deterministic and
must not rely on side effects. The binding clones again after validation so
even a validator that mutates its receiver cannot mutate saved defaults or a
published generation.

## Encode baseline parameter groups

The generated binding can turn an application-owned root into the complete
parameter documents used to seed or restore KMS:

```go
defaults := appconfig.Defaults()
groups, err := configkms.EncodeParameterGroups(defaults)
if err != nil {
    return err
}

databaseJSON := groups["database"]
rateLimitsJSON := groups["rate_limits"]
```

`EncodeParameterGroups` returns `map[string]json.RawMessage`, keyed by the
declared parameter-group aliases. Each value is a complete strict group
document, not a patch. The encoder uses the same generated descriptors as the
runtime decoder: durations are Go duration strings, byte slices are canonical
base64, supported named and composite values are encoded recursively, and
`nil` remains distinct from a non-nil empty slice, map, or byte slice. Inline
fragment fields are flattened into their declared application-level groups.

The encoder rejects a nil root, a nil pointer-valued inline fragment, values
outside a generated portable numeric contract, and JSON values that cannot be
represented, such as `NaN` or infinity. It never emits managed secrets,
excluded fields, secret aliases, secret metadata, or secret plaintext, even if
the supplied root is a populated runtime snapshot. It does not call the
application's `Validate` method; schema validation, release validation, and the
runtime store still validate the assembled parameter-and-secret candidate.

Use this API in an application-owned baseline/export command or deployment
tool rather than maintaining duplicate hand-written default JSON. Map each
alias to an operator-chosen physical KMS parameter path, publish the returned
document with content type `json`, and pin the resulting immutable version in
the release manifest. Create and pin secrets separately.

## Start and consume the store

The generated package presents the application-specific API:

```go
store, err := configkms.Start(ctx, client, configkms.Options{
    Release:             "runtime",
    Defaults:            appconfig.Defaults,
    AllowDefaultMismatch: flags.AllowKMSDefaultMismatch,
    OnDefaultMismatch: func(report configstore.DefaultMismatchReport) {
        switch report.Severity() {
        case configstore.MismatchFatal:
            logger.Error("KMS defaults mismatch",
                zap.String("release", report.Release().String()),
                zap.Any("differences", report.Fields()))
        case configstore.MismatchError:
            logger.Error("KMS emergency override active",
                zap.String("release", report.Release().String()),
                zap.Any("differences", report.Fields()))
        }
    },
    OnCandidateRejected: func(report configstore.CandidateRejectionReport) {
        logger.Warn("KMS candidate rejected",
            zap.String("category", string(report.Category())),
            zap.String("release", report.Release().String()),
            zap.Strings("paths", report.Paths()))
    },
    SecretTokenProvider: func(alias, path string) (string, bool) {
        token, ok := bootstrapSecretTokens[alias]
        return token, ok
    },
})
if err != nil {
    return err
}
```

`Start` synchronously resolves, decodes, validates, compares, and publishes the
initial release. It returns only after `Current` is usable, then watches in the
background until the context is canceled. A mismatch callback is mandatory so
divergence cannot be silent. When a fatal callback records instead of exiting,
`Start` returns a typed `DefaultMismatchError` and publishes nothing.

`OnCandidateRejected` is optional local diagnostics. Its immutable report
contains only a bounded category, safe release identity, and generated
canonical paths. Decode failures identify the group or known field;
restart-required failures identify the changed fields; application validation
text and candidate values are never included. The callback is invoked at most
once per distinct candidate, and a callback panic is isolated from admission
and last-known-good behavior. Server acknowledgements still contain only the
bounded category.

The application owns any process flag and passes its value through
`AllowDefaultMismatch`; the SDK never registers flags. The bypass changes only
startup admission and never suppresses reporting.

Capture one snapshot at the boundary of a request, job, message, or other
logical operation:

```go
snapshot := store.Current()
persistence := snapshot.PersistenceHandler()

host := persistence.DBHost()
port := persistence.DBPort()
timeout := persistence.DBQueryTimeout()
password := persistence.DBPassword()
release := snapshot.Release()
```

When existing startup constructors need the complete root, use the generated
defensive clone:

```go
startupConfig := store.Current().Config()
```

`Config()` is intended for restart-bound startup wiring. Hot consumers should
capture one snapshot and use a narrow generated view so related reads remain
within one generation.

Views from one snapshot hold the same immutable generation. Do not call
`store.Current()` separately for related fields; two loads may cross an
activation boundary.

`Current` performs one atomic pointer load. View capture and scalar getters use
ordinary field access: no locks, further atomics, reflection, maps, KMS calls,
projection copies, allocations, or version checks. Composite getters return a
generated defensive deep copy. Secret getters return `Secret.Clone()`, because
`Secret.Value()` deliberately exposes its byte buffer. Those explicit copies
may allocate and cannot mutate the published generation.

A pointer-to-scalar source field accepts a scalar or `null`, and default/reload
comparison preserves the nil distinction. Its generated consumer getter
returns the scalar value and maps nil to that scalar's zero value. If consumers
must observe presence separately, use an explicit supported value field rather
than a pointer.

Inspect lifecycle state without field names or values:

```go
status := store.Status()
stats := store.Stats()
err := store.Wait()
```

`Wait` returns unexpected terminal loader failures. Context cancellation after
the store became ready is normal shutdown and returns `nil`.

`Status` contains redacted point-in-time state: readiness, observed and applied
release identities, divergence, the last bounded rejection category and time,
and reconnect count. `Stats` contains cumulative candidate, applied, rejection,
and reconnect counters plus divergence and the applied version and activation
revision. Applications may export them with this low-cardinality mapping; the
SDK does not register a metrics backend:

| Store value | Suggested metric |
|---|---|
| `Stats.Candidates` | `config_store_candidates_total` |
| `Stats.Applied` | `config_store_applied_total` |
| `Stats.Rejected[reason]` | `config_store_rejected_total{reason}` |
| `Stats.Reconnects` | `config_store_reconnects_total` |
| `Stats.DefaultDivergent` | `config_store_default_divergent` gauge |
| applied release version/revision | `config_store_applied_release_version` / `config_store_applied_activation_revision` gauges |

Do not turn release names, digests, aliases, paths, field names, values, or
secret metadata into metric labels.

## Drift and reload behavior

At startup with matching values, the candidate is published and acknowledged
as applied. With differences and no bypass, the callback receives fatal
severity, `Start` returns `DefaultMismatchError`, no generation is published,
and the release is rejected with the bounded `default_mismatch` category. With
the bypass, the same candidate is published, reported at error severity, and
status becomes divergent.

After startup, a valid candidate that changes only hot fields is prepared in a
fresh root and published with one atomic swap. A difference from source
defaults is always reported at error severity and applied; this is the
emergency override path. Reports are deduplicated per distinct release
candidate. Restoring every non-secret default clears divergent status.

If any restart field changes, the entire runtime candidate is rejected as
`restart_required`. Restart-bound secrets compare their pinned resource
identity/version metadata, never plaintext bytes. Diagnostics expose only safe
canonical field paths locally; acknowledgements and metrics contain bounded
categories. Invalid, superseded, validation-failing, and contract-failing
candidates cannot displace last-known-good.

## Operator workflow

### Check the generated contract

The generated `runtime.contract.json` is a deployment/tooling artifact; it is
not registered with KMS and is not a release manifest. Before creating a
release, use it to check that the manifest contains exactly the declared group
and secret aliases with the recorded kinds, that every parameter group uses
content type `json`, and that the schema is the artifact paired by
`schema_sha256`. The contract also records reload policies and consumer views
for rollout review. It deliberately contains no physical paths, exact versions,
defaults, current values, secret plaintext, or tokens; operators continue to
choose paths and pins in the release manifest.

The schema ID and immutable registration version are operator-owned and are
attached to the first-class application record. Every environment release for
that application must pin that same schema version and match the application's
alias/kind/content-type contract. The generated runtime does not hardcode the
registry coordinates: KMS validates the application and manifest pins, while
the process independently enforces the generated contract and strict decoder.

### Import the artifacts into the console

The console's **Create application** wizard accepts both generated artifacts
directly, so an application onboarded from `kms-config-gen` does not need its
contract typed by hand:

1. **Schema step.** Paste or import `runtime.schema.json`. The wizard
   registers it as a new immutable version under the schema ID you choose
   when you advance to the next step, before the application record exists,
   so a rejected schema leaves nothing behind. Picking an already-registered
   `id@version` is equally valid.
2. **Contract step.** **Import** accepts the `kms-config-contract/v1`
   envelope (`format`, `source`, `schema_sha256`, `groups`, `fields`,
   `secrets`, `views`) or a bare array of `{alias, kind, content_type}`
   entries. From the envelope, each `groups[]` entry becomes a parameter
   alias with content type `json` and each `secrets[]` entry becomes a secret
   alias; `fields`, `views`, and reload policies are informational to KMS and
   are not stored on the application. Rows that came from the artifact are
   marked *from artifact*; any row you edit afterwards is marked *diverged*
   so a reviewer can see that the application no longer matches the
   generated contract.
3. **Pairing check.** `schema_sha256` is the SHA-256 of the exact bytes of
   the schema file the generator wrote alongside the contract. When both
   artifacts are supplied, the wizard hashes the schema text you imported and
   shows whether it matches the contract's `schema_sha256`. Import the file
   as generated — reformatting or re-serialising it changes the hash and the
   check reports a mismatch even though the schema is semantically the same.
   A mismatch usually means the two files come from different builds; rerun
   generation and import the pair together.
4. **Environments step.** Add each environment (production names are
   outlined); an existing namespace with the application's name is attached
   rather than reported as a conflict.

When the schema is imported without the contract, the wizard prefills the
contract from the schema with the
[schema type ↔ content type mapping](configuration-releases.md#schema-type--content-type);
the reverse derivation is offered when only a contract is supplied. After
creation the application page's definition card keeps checking that the
contract and pinned schema agree and offers the same derive fixes. The
generated runtime still enforces its embedded contract independently, so a
diverged application record is caught by the process as
`config_contract_mismatch` at the latest.

### Descriptions and defaults in the schema

The generator makes the schema self-describing so the console can render a
value as a form instead of a JSON box:

- A managed field's doc comment (or trailing `//` comment) becomes that
  property's `description`, collapsed to one line. Nested struct fields are
  annotated the same way. The root type's doc comment becomes the schema's
  top-level `description`.
- A package-level `Defaults` function whose last statement is `return
  &Config{...}` (or `return Config{...}`) supplies `default` values: every
  literal element that is a compile-time constant — including named constants,
  nested struct literals, slices, arrays, and maps of constants — is emitted at
  the property it sets, and fields omitted from the literal get Go's zero
  value. Elements the generator cannot evaluate statically (a local variable,
  a function call, a `[]byte` conversion) simply have no `default`, and a
  group only gets an object-level `default` when every one of its fields is
  known. Secrets never appear in the schema.
- `-defaults NAME` selects a different function and requires it to exist;
  `-defaults -` disables default extraction. With no flag, `Defaults` is used
  when present.

Because defaults are read from the same literal the application returns at
runtime, the schema cannot drift from source defaults; a `Defaults` function
that builds its value imperatively yields no defaults rather than wrong ones.

### Register the generated schema

Generate baseline group documents from the exact application build whose
schema and contract are being deployed. A small application-owned command can
call `configkms.EncodeParameterGroups(appconfig.Defaults())` and write one file
per alias for operator tooling. This keeps baseline documents synchronized
with source defaults and guarantees the runtime's duration, base64, nullable,
composite, and inline-fragment encodings. Review the generated non-secret
documents before publishing them; secret values are provisioned separately.

```bash
parameter-store release schema create my-service/runtime config/runtime.schema.json \
  --endpoint "$PARAM_STORE_ENDPOINT" --token "$ADMIN_TOKEN" \
  --ca "$PARAM_STORE_SERVER_CA_CERT"
```

Record the returned immutable schema version in the release manifest. Each
parameter alias points to one complete JSON group document with content type
`json` (the KMS content-type token is `json`, not `application/json`):

```bash
parameter-store put-parameter \
  /prod/my-service/config/groups/rate-limits \
  '{"request_limit":2000}' \
  --content-type json \
  --endpoint "$PARAM_STORE_ENDPOINT" --token "$ADMIN_TOKEN" \
  --ca "$PARAM_STORE_SERVER_CA_CERT"
```

Do not publish sparse patches. Copy the current release manifest and update
only the exact version pin that changed:

```yaml
namespace: prod/my-service
name: runtime
schema_id: my-service/runtime
schema_version: 1
entries:
  - {alias: database, kind: parameter, key: config/groups/database, version: 12}
  - {alias: rate_limits, kind: parameter, key: config/groups/rate-limits, version: 9}
  - {alias: db_password, kind: secret, key: db-password, version: 3}
```

Create, validate, diff, and activate with compare-and-swap:

```bash
parameter-store release create runtime-release.yaml \
  --endpoint "$PARAM_STORE_ENDPOINT" --token "$ADMIN_TOKEN" \
  --ca "$PARAM_STORE_SERVER_CA_CERT"

parameter-store release validate prod/my-service runtime 15 \
  --endpoint "$PARAM_STORE_ENDPOINT" --token "$ADMIN_TOKEN" \
  --ca "$PARAM_STORE_SERVER_CA_CERT"

parameter-store release diff prod/my-service runtime 14 15 \
  --endpoint "$PARAM_STORE_ENDPOINT" --token "$ADMIN_TOKEN" \
  --ca "$PARAM_STORE_SERVER_CA_CERT"

parameter-store release activate prod/my-service runtime 15 \
  --expected-current-version 14 \
  --endpoint "$PARAM_STORE_ENDPOINT" --token "$ADMIN_TOKEN" \
  --ca "$PARAM_STORE_SERVER_CA_CERT"
```

Activation is validation-gated. KMS repeats release validation immediately
before moving `current`/`previous`, then rechecks immutable pins, digests,
content types, secret state and expiry, and protection metadata in the same
storage transaction that moves the labels. A failed activation returns
`FAILED_PRECONDITION` (HTTP 412) with the same sanitized structured validation
errors printed by `release validate`; labels and activation revision remain
unchanged. Calling `release validate` first remains useful operator preflight,
but it is not required for safety and does not create a reusable approval.

Rollback reactivates the previous immutable version with an automatic
compare-and-swap guard, or accepts an explicit retained version:

```bash
parameter-store release rollback prod/my-service runtime \
  --endpoint "$PARAM_STORE_ENDPOINT" --token "$ADMIN_TOKEN" \
  --ca "$PARAM_STORE_SERVER_CA_CERT"
```

Monitor per-instance lifecycle acknowledgements:

```bash
parameter-store release subscribers prod/my-service runtime \
  --endpoint "$PARAM_STORE_ENDPOINT" --token "$ADMIN_TOKEN" \
  --ca "$PARAM_STORE_SERVER_CA_CERT"
```

### Diagnose a rejected candidate

The `REJECTED` column includes the form `vVERSION/rREVISION:category`. Use the
bounded category first; arbitrary application diagnostics are deliberately not
sent to the server:

| Category | Operator response |
|---|---|
| `resolution_failed` | Revalidate that every exact pin exists, is readable, and is authorized for the application identity. |
| `token_unavailable` | Provision the local token used by `SecretTokenProvider` for the protected secret; never put it in the release. |
| `version_mismatch` / `digest_mismatch` | Treat the pin or returned resource as inconsistent; validate again and investigate the server or storage before activating another release. |
| `config_contract_mismatch` | Compare aliases, kinds, and literal `json` content types with the generated contract. This check happens before resource fetches. |
| `config_decode_failed` | Publish a new complete group document fixing missing, unknown, duplicate, mistyped, out-of-range, or noncanonical values. |
| `config_validation_failed` | Check the application's local `Validate` diagnostic and fix either the complete candidate or application validation. |
| `default_mismatch` | Restore the source-owned defaults. An application-owned startup bypass is temporary emergency admission, not a release repair. |
| `restart_required` | Keep last-known-good serving and roll or restart replicas that are intended to adopt the restart-bound change. |
| `superseded` | Usually no action; a newer activation replaced preparation of this candidate. |
| `active_check_failed` | Check KMS connectivity and authorization for the final freshness read. After readiness the loader reconciles again; if startup returned an error, restart `Start` after recovery. |
| `prepare_failed` / `internal` | Inspect local application logs and the deployed build; managed configuration normally emits a more specific category. |

A hot emergency override is applied by running processes and reported as
divergent. A later process start fails unless the application explicitly
supplies its mismatch bypass. Restore the source default in another complete
parameter version and activate a new release to clear divergence.

A running process rejects restart-bound changes and keeps last-known-good. For
a code-first rolling deployment, activate the release matching the upcoming
source defaults, let old replicas reject restart-bound changes, start new
replicas against the new matching release, and monitor subscriber state until
rollout completion.

Secret rotation creates a new immutable secret version and updates the exact
secret pin in a release. A `reload=hot` secret pin can apply in-process; a
`reload=restart` secret pin is rejected by running replicas and adopted on the
intended restart or rollout. Secret plaintext and tokens never enter parameter
JSON, defaults, generated schema/contract, drift reports, status, metrics, or
acknowledgements.

## Testing

`kmsclienttest` supports exact-version parameter/secret values and scripted
configuration releases in-process. Use `SetParameterVersion`,
`SetSecretVersion`, `SetActiveRelease`, and
`ActivateConfigurationRelease`; `WaitForReleaseSubscribe` exposes lifecycle
acknowledgements and disconnect controls.

Generated bindings should be tested for strict decoding, default admission,
runtime override/restoration, restart rejection, secret pin comparison,
last-known-good, composite mutation protection, concurrent snapshot
consistency, and zero-allocation scalar access. Keep one hermetic integration
scenario against a temporary real KMS stack to cover schema registration,
release validation/activation, and subscriber acknowledgements.

This repository also keeps adversarial generated-store and real-KMS matrices.
They cover malformed and duplicate JSON, numeric/format boundaries, nullable
versus non-nil-empty collections, exact secret pins, callback redaction and
panic isolation, transient reconciliation, stale/superseded candidates,
atomic LKG behavior, and concurrent readers under rapid activation.
