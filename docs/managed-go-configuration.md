# Managed Go configuration

The managed Go configuration API builds an application-specific, typed store
on top of `kmsclient.ReleaseLoader`. The loader continues to own release
watching, exact pin resolution, digest checks, acknowledgements,
supersession, and last-known-good behavior. Generated bindings add strict
decoding, validation, application-owned defaults, drift policy, immutable
snapshots, typed consumer views, and deterministic schema artifacts.

This layer requires Go 1.27 or newer. Generated bindings expose canonical JSON
documents as `encoding/json/jsontext.Value`, matching the repository's Go JSON
v2 runtime.

For a runnable walkthrough, use:

```bash
go run ./examples/managed-config
```

The [`examples/managed-config`](../examples/managed-config) app demonstrates
typed views, immutable snapshots, atomic hot override, default-divergence
reporting, restart-required rejection, secret redaction, and last-known-good
preservation against a deterministic in-process KMS.

To populate a new application's existing namespaces from these source-owned
values, use the generated defaults encoder and KMS-owned exporter/import flow
described in [`managed-defaults.md`](managed-defaults.md). Secret provisioning
remains a separate manual operation.

Use this layer when a set of configuration fields must change as one release.
The lower-level `ReleaseLoader`, `RunTypedRelease`, `ParameterValue`, and
`SecretValue` APIs remain supported.

## Operating model

Application source is authoritative for every non-secret default. A normal KMS
release mirrors those values. Ordinary changes update source through code
review and deploy with the application; a KMS-only hot change is an emergency
override.

At startup, the generated store resolves and validates the active release and
compares every non-secret field with the application defaults. A mismatch is
applied and reported once at error severity, and the process remains visibly
divergent through status, the applied acknowledgement, and metrics; a replica
must always be able to restart onto the active release. After startup, valid
changes to `reload=hot` fields apply atomically and are reported as divergent
when they differ from source defaults. A later release that restores all
defaults clears divergence. A CI verify test compares the defaults in code
with the active release before a build ships.

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

The Go generator permits a generated schema up to the server's 1 MiB schema
limit. The Python and TypeScript generators currently enforce a smaller
256 KiB generation limit; their SDK guides call out that boundary.

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

`EncodeParameterGroups` returns `map[string]jsontext.Value`, keyed by the
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
sink := configstore.NewLogSink(nil) // buffers startup records until Set
store, err := configkms.Start(ctx, client, configkms.Options{
    Release:   "runtime",
    Defaults:  appconfig.Defaults,
    Callbacks: configstore.SlogCallbacks(sink, configstore.SlogOptions{Component: "kms"}),
    SecretTokenProvider: func(alias, path string) (string, bool) {
        token, ok := bootstrapSecretTokens[alias]
        return token, ok
    },
})
if err != nil {
    return err
}
logger := buildLogger(store.Current()) // the logger usually depends on config
sink.Set(logger)                       // replays the buffered startup records
```

`Options` embeds `configstore.Callbacks`, so an application that wants its own
observers writes them out; only `OnDefaultMismatch` is required:

```go
store, err := configkms.Start(ctx, client, configkms.Options{
    Release:  "runtime",
    Defaults: appconfig.Defaults,
    Callbacks: configstore.Callbacks{
        OnDefaultMismatch: func(report configstore.DefaultMismatchReport) {
            // Applied anyway; this is the reconciliation signal.
            logger.Error("KMS release diverges from source defaults",
                zap.String("phase", string(report.Phase())),
                zap.String("release", report.Release().String()),
                zap.Any("differences", report.Fields()))
        },
        OnApplied: func(report configstore.AppliedReport) {
            for _, change := range report.Changed() {
                logger.Info("KMS config field changed",
                    zap.String("path", change.Path),
                    zap.Any("previous", change.Previous),
                    zap.Any("current", change.Current))
            }
            logger.Info("KMS config applied",
                zap.String("phase", string(report.Phase())),
                zap.String("release", report.Release().String()),
                zap.Bool("divergent", report.DefaultDivergent()))
        },
        OnCandidateRejected: func(report configstore.CandidateRejectionReport) {
            logger.Warn("KMS candidate rejected",
                zap.String("category", string(report.Category())),
                zap.String("release", report.Release().String()),
                zap.Strings("paths", report.Paths()))
        },
    },
})
```

`Start` synchronously resolves, decodes, validates, compares, and publishes the
initial release. It returns only after `Current` is usable, then watches in the
background until the context is canceled. `Start` fails only on transport,
contract, decode, and validation errors; there is no typed startup error for
default divergence. A divergent active release is applied and reported, so a
replica can always restart onto whatever is active. A mismatch callback is
mandatory so divergence cannot be silent.

`OnApplied` fires after every published generation, including the initial one.
Its immutable `AppliedReport` carries the phase (`startup` or `runtime`), the
release identity, `DefaultDivergent()`, `Changed()` (every non-secret field
whose canonical value differs from the previously applied generation, with
`Previous` and `Current`; a rotated secret appears path-only with nil values;
the initial generation has an empty list), and `Groups()` (the canonical
non-secret parameter group documents of the generation, keyed by alias).
Ordinary formatting of a report prints paths only.

`OnCandidateRejected` is optional local diagnostics. Its immutable report
contains only a bounded category, safe release identity, and generated
canonical paths. Decode failures identify the group or known field;
restart-required failures identify the changed fields; application validation
text and candidate values are never included. The callback is invoked at most
once per distinct candidate, and a callback panic is isolated from admission
and last-known-good behavior. Server acknowledgements still contain only the
bounded category.

Every callback runs synchronously on the loader goroutine and must not block.

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
release identities, `DefaultDivergent`, the last bounded rejection category and
time, and reconnect count. `Stats` contains cumulative candidate, applied,
rejection, and reconnect counters plus divergence and the applied version and
activation revision. Neither contains aliases, paths, values, or secret
metadata; see [Export metrics](#export-metrics) for the ready-made collector.

## Log the applied configuration

`configstore.SlogCallbacks(sink, opts)` returns a complete `Callbacks` value
that logs through `log/slog`:

| Event | Level | Message | Attributes |
|---|---|---|---|
| default mismatch | `ERROR` | `kms config diverges from source defaults` | `component`, `phase`, `release`, `fields` |
| initial generation applied | `INFO` | `kms config applied` | `component`, `phase`, `release`, `release_version`, `activation_revision`, `default_divergent` |
| per parameter group at startup | `INFO` | `kms config group` | `component`, `alias`, `values` (canonical non-secret JSON), `release_version`, `activation_revision` |
| group documents unavailable | `ERROR` | `kms config groups unavailable` | `component`, `release`, `error` |
| reload applied | `INFO` | `kms config reloaded` | `component`, `release`, `default_divergent`, `changed_count` |
| per changed field on reload | `INFO` | `kms config field changed` | `component`, `release`, `path`, `previous`, `current` |
| per parameter group after a reload | `INFO` | `kms config group` | same as at startup — every applied generation is dumped in full |
| candidate rejected | `ERROR` | `kms config candidate rejected` | `component`, `category`, `release`, `paths` |

`SlogOptions.Component` sets the `component` attribute (default
`configstore`); `DisableStartupSnapshot`, `DisableReloadChanges` and
`DisableReloadSnapshot` suppress
the per-group and per-field records. Values are the report values, which the
manager has already redacted of secret content, and no attribute key names
secret material.

To reconstruct the configuration behind any log line, stamp request and job
loggers with the generation that was sampled for that operation (its
`activation_revision` is the unique key — a rollback re-activates an old
version under a new revision) and look up the matching `kms config group`
records, which are emitted for every applied generation. The same revision
also identifies the immutable release in KMS (`parameter-store release show`),
so the log dump is a convenience rather than the only source of truth.

The logger usually cannot exist before the configuration that shapes it has
been loaded. `configstore.NewLogSink(nil)` therefore returns a `LogSink` with
no logger: startup records (the initial mismatch report, the applied notice,
the group snapshot, and any candidate rejected before the first generation)
are buffered, bounded, and replayed in order on the first `sink.Set(logger)`.
Runtime records emitted while no logger is installed are dropped. `Set` may be
called again to swap loggers; `Logger()` returns the current one.

Applications logging through zap wrap their logger with
`go.uber.org/zap/exp/zapslog` and pass the resulting `*slog.Logger` to `Set`.

## Export metrics

`sdk/go/kmsmetrics` reads `Status` and `Stats` on every scrape, so it needs no
bookkeeping in the application:

```go
registry.MustRegister(kmsmetrics.NewCollector("myapp", store))
```

| Metric | Type | Meaning |
|---|---|---|
| `<ns>_kms_config_default_divergent` | gauge | 1 while the applied generation differs from source defaults |
| `<ns>_kms_config_ready` | gauge | 1 once an initial generation has been applied |
| `<ns>_kms_config_applied_release_version` | gauge | version of the applied release |
| `<ns>_kms_config_applied_activation_revision` | gauge | activation revision of the applied release |
| `<ns>_kms_config_candidates_total` | counter | release candidates received |
| `<ns>_kms_config_candidates_applied_total` | counter | candidates applied |
| `<ns>_kms_config_candidates_rejected_total{category}` | counter | candidates rejected, by bounded category (every category is always present) |
| `<ns>_kms_config_reconnects_total` | counter | release stream reconnects |

The namespace is sanitized to a valid metric identifier; an empty namespace
yields bare `kms_config_*` names. No alias, field path, value, or secret
metadata is ever exported, and release names and digests are not labels.

## Verify defaults in CI

Runtime reporting tells a running replica that it diverges. The CI verify test
closes the loop before a release is cut: it hashes the source-owned defaults
and asks KMS whether the active release still matches them, without either
side sending a value. The generated binding exposes
`VerifyReleaseDefaults(ctx, client, root, opts)`; `sdk/go/kmsverify` wraps it
in a test entry point driven by the environment:

```go
func TestReleaseMatchesSourceDefaults(t *testing.T) {
    kmsverify.Run(t, kmsverify.Spec[appconfig.Config]{
        Defaults:  appconfig.ManagedReleaseDefaults,     // func(profile string) (*Config, error)
        Verify:    configkms.VerifyReleaseDefaults,      // generated
        Namespace: appconfig.KMSNamespaceForProfile,     // optional: profile -> "env/app"
    })
}
```

| Variable | Meaning |
|---|---|
| `KMS_VERIFY_ENDPOINT` | KMS gRPC `host:port`. Unset: the test **skips** (or fails when `KMS_VERIFY_REQUIRED` is truthy). |
| `KMS_VERIFY_TOKEN` | Token of the verification identity. |
| `KMS_VERIFY_CA_FILE` / `KMS_VERIFY_CA_PEM` | CA bundle as a path or as the PEM text; mutually exclusive. System roots when both are empty. |
| `KMS_VERIFY_PROFILE` | Defaults profile passed to `Spec.Defaults` and `Spec.Namespace`. |
| `KMS_VERIFY_NAMESPACE` | `env/app` to compare; overrides `Spec.Namespace`. |
| `KMS_VERIFY_RELEASE` | Release name; default `runtime`. |
| `KMS_VERIFY_REQUIRED` | Truthy makes a missing endpoint a failure instead of a skip. Set it in release pipelines. |
| `KMS_VERIFY_INSECURE` | Cleartext connection; accepted for loopback endpoints only. |

A passing run logs the value-free report; a failing run prints it and fails
the test. Each parameter alias receives one bounded verdict (`match`,
`differs`, `missing_in_release`, `unknown_alias`, `secret_alias`,
`unsupported_content_type`); `unverified` counts parameter aliases pinned by
the release that the contract did not mention; a schema digest mismatch fails
the run as well. `kmsverify.Verify` is the script-friendly form that returns
the `VerifyResult` instead of driving `testing.TB`. The call requires the
`configuration-release:verify-defaults` operation and is rate limited per
identity; `kmsclient.ErrRateLimited` is returned when the budget is spent.
Identity bootstrap and a workflow template are in the
[consumer adoption guide](consumer-adoption.md).

## Drift and reload behavior

At startup the candidate is compared with the validated source defaults. With
matching values it is published and acknowledged as applied. With differences
it is **still published**: `OnDefaultMismatch` receives one report at
`PhaseStartup`, `Status().DefaultDivergent` and `Stats().DefaultDivergent`
become true, `OnApplied` reports the generation with `DefaultDivergent()`
true, and the applied acknowledgement carries `applied_divergent` and a field
count (never field names or values) so the console can show which replicas run
overrides. There is no fatal startup path and no bypass flag; a replica must
be able to restart onto whatever release is active, and the report is the
signal to reconcile code and KMS.

After startup, a valid candidate that changes only hot fields is prepared in a
fresh root and published with one atomic swap. `OnApplied` lists the changed
canonical paths with previous and current values (path-only for rotated
secrets). A difference from source defaults is always reported at error
severity and applied; this is the emergency override path. Reports are
deduplicated per distinct release candidate. Restoring every non-secret
default clears divergent status and appears in the next `OnApplied` change
list.

The CI verify test ([Verify defaults in CI](#verify-defaults-in-ci)) is the
tripwire that catches a release left divergent after an override, before the
next deployment ships code that no longer matches it.

If any restart field changes, the entire runtime candidate is rejected as
`restart_required`. Restart-bound secrets compare their pinned resource
identity/version metadata, never plaintext bytes. Diagnostics expose only safe
canonical field paths locally; acknowledgements and metrics contain bounded
categories. Invalid, superseded, validation-failing, and contract-failing
candidates cannot displace last-known-good.

## Operator workflow

### Expose schema, defaults, and release commands from the application

Generated bindings embed the exact emitted schema and return a fresh copy from
`GeneratedSchema()`. An application can expose all three source-owned operations
without teaching each consumer how to construct KMS requests:

```go
os.Exit(configstore.RunManagedConfigCommand(
    os.Args[1:], os.Stdout, os.Stderr,
    configstore.ManagedConfigCommandConfig[appconfig.Profile, appconfig.Config]{
        Application: appconfig.APP_NAME,
        Schema: configkms.GeneratedSchema,
        Defaults: configstore.DefaultsApplierConfig[appconfig.Profile, appconfig.Config]{
            Provider: appconfig.ManagedReleaseDefaults,
            Encoder: configkms.EncodeDefaultsArtifact,
            Namespace: configkms.NamespaceForProfile,
        },
    },
))
```

Use `managed-config schema upload` to register a new immutable version. It has
no profile because the schema belongs to the application's release stream and
is shared by every profile. Use `managed-config defaults apply --profile ...`
to preview or apply profile-specific values. Add `--update-definition` on the
previewed defaults operation to repin the application to an already uploaded
matching schema. Uploading alone never changes the pin, and uploading an exact
duplicate fails.

After defaults are applied, use `managed-config release create --profile ...`
to preview the exact immutable release KMS would create, then repeat it with
`--execute` to create the release. Parameter values must still match the
generated defaults. Existing secret aliases retain the active release's exact
secret pins even when their `current` labels have moved; first-release and new
secret aliases resolve `current`. Missing secrets fail closed. This command
never activates a release—the new inactive version is reviewed and activated
in the web console. All three commands read `KMS_TOKEN` and accept the standard
endpoint/TLS flags. `RunDefaultsApplier` remains available as a standalone API.

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

The schema lineage is owned by the application and its immutable release name;
only the registration version is pinned. Every environment release for that
application must pin that same version and match the application's
alias/kind/content-type contract. Profiles choose defaults and environments,
not schemas: all profiles share this one lineage. KMS validates the application
and manifest pin, while the process independently enforces the generated
contract and strict decoder.

### Import the artifacts into the console

The console's **Create application** wizard accepts both generated artifacts
directly, so an application onboarded from `kms-config-gen` does not need its
contract typed by hand:

1. **Schema step.** Paste or import `runtime.schema.json`. The wizard creates
   the application, its first immutable schema version, contract, and pin in
   one transaction. A failure leaves none of them behind. Schema coordinates
   are derived from the application and release names rather than entered as a
   free-form ID.
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
3. **Pairing check.** `schema_sha256` is the SHA-256 of the schema JSON with
   insignificant whitespace removed. KMS registration and both generators use
   the same representation, so formatting the JSON does not change its digest.
   When both artifacts are supplied, the wizard hashes the schema text you
   imported and shows whether it matches the contract's `schema_sha256`. A
   mismatch usually means the two files come from different builds; rerun
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
  annotated the same way, and so are fields of types inlined or nested from
  other packages and modules (a shared `go-common` config, say) — their doc
  comments are read from the loaded sources. The root type's doc comment
  becomes the schema's top-level `description`. A section-header comment
  placed directly above a field is that field's doc comment as far as Go is
  concerned; leave a blank line after headers.
- A package-level `Defaults` function whose last statement is `return
  &Config{...}` (or `return Config{...}`) supplies `default` values: every
  literal element that is a compile-time constant — including named constants,
  nested struct literals, slices, arrays, and maps of constants — is emitted at
  the property it sets, and fields omitted from the literal get Go's zero
  value. `new(v)` reads as a pointer to `v` and `new(T)` as a pointer to the
  zero value, and a call to a zero-argument function in the same package
  whose last statement returns a literal is followed (up to four levels
  deep), so `Timeouts: defaultTimeouts()` contributes its literal too.
  Elements the generator cannot evaluate statically (a local variable, a
  call with arguments, a `[]byte` conversion) simply have no `default`, and a
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
parameter-store release schema create my-service config/runtime.schema.json \
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
| `default_mismatch` | Legacy category from SDKs that refused divergent startup. Current SDKs apply and report divergence instead (`applied_divergent` on the acknowledgement); restore the source-owned defaults or update them in code. |
| `restart_required` | Keep last-known-good serving and roll or restart replicas that are intended to adopt the restart-bound change. |
| `superseded` | Usually no action; a newer activation replaced preparation of this candidate. |
| `active_check_failed` | Check KMS connectivity and authorization for the final freshness read. After readiness the loader reconciles again; if startup returned an error, restart `Start` after recovery. |
| `prepare_failed` / `internal` | Inspect local application logs and the deployed build; managed configuration normally emits a more specific category. |

A hot emergency override is applied by running processes and reported as
divergent; a process that starts later applies the same release and reports
the divergence once at startup. The subscriber view marks such replicas as
applied-divergent. Restore the source default in another complete parameter
version and activate a new release to clear divergence, or change the source
defaults to adopt the override and let the CI verify test confirm the match.

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

Generated bindings should be tested for strict decoding, divergent startup reporting,
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
