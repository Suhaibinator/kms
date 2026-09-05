# Minimal managed configuration app

This runnable example uses the in-process `kmsclienttest` server, so it needs
no external KMS, credentials, ports, or network access.

```bash
go run ./examples/managed-config
```

It demonstrates:

- a source-owned, tagged `Config` with defaults and validation;
- generated schema, contract, typed snapshots, and consumer views;
- an `OnApplied` report for every published generation, listing the canonical
  paths that changed since the previous generation (secret rotations are
  path-only) and whether the generation diverges from source defaults;
- one atomic hot override and secret rotation with explicit default-divergence
  reporting and redacted secret formatting;
- an old snapshot remaining immutable across activation;
- whole-candidate rejection when a release mixes restart-bound and hot changes;
- last-known-good serving after that rejection;
- a later release restoring source defaults and clearing divergence.

A process that starts while an override is active does not fail: the active
release is applied as-is, reported once through `OnDefaultMismatch` at
`PhaseStartup`, and exposed as `Status().DefaultDivergent`. The applied
acknowledgement carries the divergence flag so the console can show which
replicas are running overrides. The CI verify test below is the tripwire that
keeps source defaults and the active release from drifting apart unnoticed.

The example wires channel-backed callbacks so its transcript is
deterministic. A real service passes the ready-made slog implementation and
installs its logger on the sink once configuration has built it:

```go
sink := configstore.NewLogSink(nil) // buffers startup records until Set
store, err := configkms.Start(ctx, client, configkms.Options{
    Release:   "runtime",
    Defaults:  appconfig.Defaults,
    Callbacks: configstore.SlogCallbacks(sink, configstore.SlogOptions{Component: "kms"}),
})
// ... build the application logger from store.Current() ...
sink.Set(logger)
```

Export the store's status and counters as Prometheus metrics with one line;
no alias, path, or value is ever exported:

```go
registry.MustRegister(kmsmetrics.NewCollector("managed_config_example", store))
```

`verify_test.go` is the CI verify test. It hashes the source defaults and asks
KMS whether the active release still matches; only hashes and bounded
verdicts cross the wire. It skips when `KMS_VERIFY_ENDPOINT` is unset, so a
plain `go test ./...` needs no server:

```bash
KMS_VERIFY_ENDPOINT=kms.dev.example:8443 \
KMS_VERIFY_TOKEN="$VERIFY_TOKEN" \
KMS_VERIFY_CA_FILE=/etc/kms/ca.pem \
KMS_VERIFY_NAMESPACE=dev/managed-config \
KMS_VERIFY_REQUIRED=1 \
go test ./examples/managed-config -run TestReleaseMatchesSourceDefaults -v
```

In production, remove the `demo_kms.go` scaffolding and pass a normally
configured client to the same generated `configkms.Start` call:

```go
client, err := kmsclient.NewClient(kmsclient.Config{
    Endpoint:  os.Getenv("KMS_ENDPOINT"),
    Namespace: "prod/my-service",
    Token:     os.Getenv("KMS_TOKEN"),
    TLS:       kmsclient.TLSFromFiles(os.Getenv("KMS_CA_FILE")),
})
```

If a release contains access-token-protected secrets, also set
`configkms.Options.SecretTokenProvider` from the application's secure bootstrap
credentials. The generated store, typed snapshots, and view access are
otherwise unchanged.

For a bound secret, put its key in the source declaration rather than a start
option:

```go
oauthDefaults := struct {
    LinkedInOAuthClientSecret kmsclient.Secret
    OktaOAuthClientSecret     kmsclient.Secret
}{
    LinkedInOAuthClientSecret: kmsclient.Secret{}, // unbound
    OktaOAuthClientSecret: kmsclient.Secret{       // bound
        BindKey: kmsclient.NewBindingKey(os.Getenv("OKTA_OAUTH_KMS_BIND_KEY")),
    },
}
```

The generated store extracts the key into a private alias-keyed loader map and
strips it from retained defaults and published snapshots. Access tokens and
binding keys are independent, and secret plaintext is never cached. This
example's current fake release uses unbound secrets, so it needs neither
credential.

The in-process fake exercises the client, release loader, generated decoder,
and publication policy. It deliberately does **not** exercise TLS, auth,
persistent storage, schema registration, or server-side release admission;
cover those paths with a real KMS in deployment and integration tests.

Regenerate the binding and deterministic artifacts after changing the config
contract:

```bash
go generate ./examples/managed-config/config
```

Register a newly generated schema once for the application release stream:

```bash
go run ./examples/managed-config/cmd/apply-defaults \
  schema upload \
  --endpoint localhost:8443 \
  --insecure
```

Export the source-owned parameter baseline (secret values are never included):

```bash
go run ./examples/managed-config/cmd/export-defaults \
  --profile default \
  --output /tmp/managed-config.defaults.json
```

Or preview and apply it directly through the application-owned thin wrapper.
The application and namespace must already exist, and `KMS_TOKEN` must carry an
administrative identity:

```bash
go run ./examples/managed-config/cmd/apply-defaults \
  defaults apply \
  --profile default \
  --endpoint localhost:8443 \
  --insecure

go run ./examples/managed-config/cmd/apply-defaults \
  defaults apply \
  --profile default \
  --endpoint localhost:8443 \
  --insecure \
  --execute
```

The second invocation is idempotent: identical values are reported as
`unchanged` without creating new versions. Add `--overwrite` only when a
differing current parameter should be replaced.

Once the defaults and application definition match, preview the next immutable
release directly from the same application-owned wrapper:

```bash
go run ./examples/managed-config/cmd/apply-defaults \
  release create \
  --profile default \
  --endpoint localhost:8443 \
  --insecure

go run ./examples/managed-config/cmd/apply-defaults \
  release create \
  --profile default \
  --endpoint localhost:8443 \
  --insecure \
  --execute
```

The created release remains inactive for review and activation in the web
console. Existing secret aliases retain the active release's exact pins; the
command never reads or prints secret plaintext.
