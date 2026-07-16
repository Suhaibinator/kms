// Package paramstore is the Go client SDK for the KMS parameter store and
// secret management service.
//
// It hides gRPC boilerplate behind a small ergonomic surface:
//
//   - Simple reads: [Client.GetParameter] and [Client.GetSecret].
//   - Declarative, store-backed config fields: [SecretValue] and
//     [ParameterValue], resolved with a single [Client.Resolve] call.
//   - Hot reload of non-secret parameters over the Subscribe stream, with
//     live [ParameterValue.Get] handles and [ParameterValue.OnChange]
//     callbacks.
//   - TLS / mTLS configuration, per-RPC timeouts, an optional in-memory read
//     cache, and typed sentinel errors.
//
// Secret plaintext never appears in logs, errors, or the default string/JSON
// representation of any type in this package. Access to plaintext always
// requires an explicit call ([Secret.Value], [SecretValue.Value]).
//
// # Namespaces and keys
//
// A client operates in a namespace — a fixed (env, app) pair such as
// "prod/gradethis". Keys are relative to that namespace ("postgres/password");
// interior slashes are part of the key name, not namespace structure. Set the
// namespace with [Config.Namespace], or leave it empty to discover it from the
// identity at first use (via WhoAmI); a relative key on an unbound identity then
// fails with [ErrNoNamespace]. A leading-slash key is an absolute "/env/app/key"
// display path, split in the SDK to reach another namespace.
//
// # Hot reload
//
// Non-secret [ParameterValue] fields hot-reload by default: they track the store
// over a shared Subscribe stream and [ParameterValue.Get] always returns the
// latest value. Set [ParameterValue.Static] to resolve once at Init instead.
//
// # Quick start
//
//	client, err := paramstore.NewClient(paramstore.Config{
//	    Endpoint:  "parameter-store.prod.internal:8443",
//	    Namespace: "prod/payments", // or "" to discover via WhoAmI
//	    TLS:       paramstore.MTLSFromFiles("client.crt", "client.key", "server-ca.crt"),
//	    CacheTTL:  time.Minute,
//	})
//	if err != nil {
//	    return err
//	}
//	defer client.Close()
//
//	dbPassword, err := client.GetSecret(ctx, "postgres/password")
//	if err != nil {
//	    return err
//	}
//	_ = dbPassword.Value() // []byte plaintext, never logged
package paramstore
