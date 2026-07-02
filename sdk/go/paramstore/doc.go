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
// # Quick start
//
//	client, err := paramstore.NewClient(paramstore.Config{
//	    Endpoint: "parameter-store.prod.internal:8443",
//	    TLS:      paramstore.MTLSFromFiles("client.crt", "client.key", "ca.crt"),
//	    CacheTTL: time.Minute,
//	})
//	if err != nil {
//	    return err
//	}
//	defer client.Close()
//
//	dbPassword, err := client.GetSecret(ctx, "/prod/payments/postgres/password")
//	if err != nil {
//	    return err
//	}
//	_ = dbPassword.Value() // []byte plaintext, never logged
package paramstore
