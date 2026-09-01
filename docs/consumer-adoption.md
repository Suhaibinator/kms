# Adopting the managed configuration changes in an application

This is the short path for a Go application that already imports
`github.com/Suhaibinator/kms/sdk/go/configstore` and a generated `configkms`
binding. The full behaviour is documented in the
[managed Go configuration guide](managed-go-configuration.md); this page only
lists what to change.

What changed, in one paragraph: a release that diverges from your source
defaults is now **applied and reported** instead of failing startup.
`Options.AllowDefaultMismatch`, `MismatchFatal`, and `DefaultMismatchError`
are gone; `Options` embeds `configstore.Callbacks` (`OnDefaultMismatch`,
`OnApplied`, `OnCandidateRejected`); `OnApplied` tells you what changed on
every reload; `SlogCallbacks` logs all of it; `kmsmetrics` exports status;
and a six-line test verifies in CI that the active release still matches the
defaults in code.

## 1. Bump the module and regenerate the binding

```bash
go get github.com/Suhaibinator/kms@latest
go mod tidy
go generate ./config   # or the kms-config-gen command your module uses
```

Commit the regenerated `config_kms.gen.go`, `runtime.schema.json`, and
`runtime.contract.json`. The generated `Options` now embeds
`configstore.Callbacks`, and the binding gains `VerifyReleaseDefaults`.

## 2. Replace the callbacks

Before:

```go
store, err := configkms.Start(ctx, client, configkms.Options{
    Release:              "runtime",
    Defaults:             appconfig.Defaults,
    AllowDefaultMismatch: flags.AllowKMSDefaultMismatch,
    OnDefaultMismatch:    func(report configstore.DefaultMismatchReport) { /* switch on Severity */ },
    OnCandidateRejected:  func(report configstore.CandidateRejectionReport) { /* ... */ },
})
```

After:

```go
sink := configstore.NewLogSink(nil) // no logger yet: startup records are buffered
store, err := configkms.Start(ctx, client, configkms.Options{
    Release:   "runtime",
    Defaults:  appconfig.Defaults,
    Callbacks: configstore.SlogCallbacks(sink, configstore.SlogOptions{Component: "kms"}),
})
if err != nil {
    return err // startup/configuration, transport, contract, decode, or validation; never divergence itself
}
```

Delete the `--allow-kms-default-mismatch` flag (or whatever the application
called it) and any code that matched `*configstore.DefaultMismatchError` or
`MismatchFatal`. If the application keeps hand-written callbacks, wrap them in
`configstore.Callbacks{...}` and drop the severity switch: the only severity
is `MismatchError`, and `report.Phase()` says whether it was `startup` or
`runtime`.

## 3. Install the real logger once it exists

Most services build their logger from configuration. The sink buffers the
startup records (mismatch report, applied notice, per-group snapshot, early
rejections) until a logger is installed, then replays them in order:

```go
logger := newLogger(store.Current().Logging()) // slog
sink.Set(logger)
```

For zap applications, bridge with `go.uber.org/zap/exp/zapslog`:

```go
zapLogger := newZapLogger(store.Current().Logging())
sink.Set(slog.New(zapslog.NewHandler(zapLogger.Core())))
```

## 4. Export metrics

```go
registry.MustRegister(kmsmetrics.NewCollector("myapp", store))
```

This exposes `myapp_kms_config_default_divergent`, `myapp_kms_config_ready`,
the applied version and revision gauges, and candidate/applied/rejected/
reconnect counters. Alert on `default_divergent == 1` for longer than an
override should live.

## 5. Add the verify test

```go
func TestReleaseMatchesSourceDefaults(t *testing.T) {
    kmsverify.Run(t, kmsverify.Spec[appconfig.Config]{
        Defaults:  appconfig.ManagedReleaseDefaults,
        Verify:    configkms.VerifyReleaseDefaults,
        Namespace: appconfig.KMSNamespaceForProfile, // optional when KMS_VERIFY_NAMESPACE is set
    })
}
```

Without `KMS_VERIFY_ENDPOINT` the test skips, so `go test ./...` on a laptop
is unchanged. Only hashes go to the server and only verdicts come back; the
failure report names aliases, never values.

## 6. Run it from CI on main and tags

The workflow below runs one job per environment and reads its connection from
per-environment secrets. Every configured matrix environment is required: a
missing endpoint fails the test instead of silently removing a release gate.
It runs on pushes to `main` and on `v*` tags only. Never run it on
`pull_request`: a pull request from a fork would execute with the
repository's verification token, and a failing verify on an unmerged branch
says nothing about what is deployed.

```yaml
name: verify-kms-defaults

on:
  push:
    branches: [main]
    tags: ["v*"]

jobs:
  verify:
    runs-on: ubuntu-latest
    strategy:
      fail-fast: false
      matrix:
        env: [staging, prod]
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Verify ${{ matrix.env }} release matches source defaults
        env:
          KMS_VERIFY_ENDPOINT: ${{ secrets[format('KMS_VERIFY_{0}_ENDPOINT', matrix.env)] }}
          KMS_VERIFY_TOKEN: ${{ secrets[format('KMS_VERIFY_{0}_TOKEN', matrix.env)] }}
          KMS_VERIFY_CA_PEM: ${{ secrets[format('KMS_VERIFY_{0}_CA_PEM', matrix.env)] }}
          KMS_VERIFY_PROFILE: ${{ matrix.env }}
          KMS_VERIFY_REQUIRED: "1"
        run: go test ./config/... -run TestReleaseMatchesSourceDefaults -count=1 -v
```

Secret names are `KMS_VERIFY_<ENV>_ENDPOINT`, `KMS_VERIFY_<ENV>_TOKEN`, and
`KMS_VERIFY_<ENV>_CA_PEM` (the CA as PEM text, so no file needs to be checked
in). `KMS_VERIFY_REQUIRED=1` makes the test fail rather than skip if the
endpoint secret is empty. Set
`KMS_VERIFY_NAMESPACE` in the step as well if the application does not map the
profile to a namespace in `Spec.Namespace`.

## 7. Operator bootstrap

Each environment needs one verification identity. Create it **without** a
home namespace (`--namespace` omitted) so it carries no implicit read grant,
then allow exactly the verify operation on that environment's namespace. The
server exposes this as the explicit
`configuration-release:verify-defaults` operation:

```bash
parameter-store admin identity create ci-verify-payments-staging --kind client --auth token
parameter-store admin policy create ci-verify-payments-staging \
  --subject ci-verify-payments-staging \
  --allow configuration-release:verify-defaults@staging/payments

parameter-store admin identity create ci-verify-payments-prod --kind client --auth token
parameter-store admin policy create ci-verify-payments-prod \
  --subject ci-verify-payments-prod \
  --allow configuration-release:verify-defaults@prod/payments
```

Store each printed token in its matching `KMS_VERIFY_<ENV>_TOKEN` secret. These
identities can read nothing: each can only ask whether hashes match in its one
allowed namespace. Rotate each token every 90 days with `parameter-store admin
identity rotate NAME` and update the matching secret. The verify RPC is rate
limited per identity, so one identity per application and environment keeps
the budget and failure domain predictable.

## Checklist

- [ ] module bumped, binding regenerated, artifacts committed
- [ ] `AllowDefaultMismatch` flag and `DefaultMismatchError` handling removed
- [ ] `Callbacks: configstore.SlogCallbacks(sink, ...)` and `sink.Set(logger)`
- [ ] `kmsmetrics.NewCollector` registered; alert on `default_divergent`
- [ ] `TestReleaseMatchesSourceDefaults` added and skipping locally
- [ ] workflow on `push` to `main` and `v*` tags with per-environment secrets
- [ ] verification identity per environment, no home namespace, verify-only policy, 90-day rotation
