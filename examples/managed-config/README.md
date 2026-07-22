# Minimal managed configuration app

This runnable example uses the in-process `kmsclienttest` server, so it needs
no external KMS, credentials, ports, or network access.

```bash
go run ./examples/managed-config
```

It demonstrates:

- a source-owned, tagged `Config` with defaults and validation;
- generated schema, contract, typed snapshots, and consumer views;
- one atomic hot override and secret rotation with explicit default-divergence
  reporting and redacted secret formatting;
- an old snapshot remaining immutable across activation;
- whole-candidate rejection when a release mixes restart-bound and hot changes;
- last-known-good serving after that rejection;
- a later release restoring source defaults and clearing divergence.

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

The in-process fake exercises the client, release loader, generated decoder,
and publication policy. It deliberately does **not** exercise TLS, auth,
persistent storage, schema registration, or server-side release admission;
cover those paths with a real KMS in deployment and integration tests.

Regenerate the binding and deterministic artifacts after changing the config
contract:

```bash
go generate ./examples/managed-config/config
```
