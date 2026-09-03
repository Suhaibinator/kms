package kmsclient

// getOptions is the resolved set of per-call read options.
type getOptions struct {
	version     uint64
	label       string
	secretToken string
}

// GetOption customizes a single GetParameter / GetSecret call.
type GetOption func(*getOptions)

// WithVersion pins the read to a specific immutable version. It takes
// precedence over WithLabel.
func WithVersion(n uint64) GetOption {
	return func(o *getOptions) { o.version = n }
}

// WithLabel reads the version currently pointed at by the given label
// (e.g. "current", "previous"). Ignored if WithVersion is also supplied.
func WithLabel(label string) GetOption {
	return func(o *getOptions) { o.label = label }
}

// WithSecretToken supplies the credential in a GetSecret request. It is
// required for token-protected and client-bound secrets; for client-bound
// secrets the token also carries the client key share. GetParameter accepts
// this shared option for compatibility but never transmits the token.
func WithSecretToken(token string) GetOption {
	return func(o *getOptions) { o.secretToken = token }
}

func applyGetOptions(opts []GetOption) getOptions {
	var o getOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// putOptions is the resolved set of options for a parameter write.
type putOptions struct {
	contentType  string
	metadataJSON string
}

// PutOption customizes a PutParameter call.
type PutOption func(*putOptions)

// WithContentType sets the parameter content type (string, integer, float,
// boolean, json, binary). Defaults to "string" on the server.
func WithContentType(ct string) PutOption {
	return func(o *putOptions) { o.contentType = ct }
}

// WithMetadataJSON attaches an opaque JSON metadata blob to the parameter.
func WithMetadataJSON(j string) PutOption {
	return func(o *putOptions) { o.metadataJSON = j }
}

func applyPutOptions(opts []PutOption) putOptions {
	var o putOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// putSecretOptions is the resolved set of options for a secret write.
type putSecretOptions struct {
	contentType         string
	metadataJSON        string
	clientBound         bool
	generateAccessToken bool
	expiresAtUnixMS     int64
	secretToken         string
}

// PutSecretOption customizes a PutSecret call.
type PutSecretOption func(*putSecretOptions)

// WithSecretContentType sets the secret content type.
func WithSecretContentType(ct string) PutSecretOption {
	return func(o *putSecretOptions) { o.contentType = ct }
}

// WithSecretMetadataJSON attaches an opaque JSON metadata blob to the secret.
func WithSecretMetadataJSON(j string) PutSecretOption {
	return func(o *putSecretOptions) { o.metadataJSON = j }
}

// WithClientBound opts a secret into client-bound double wrapping. New secrets
// also require WithGenerateAccessToken; updates must keep this option set and
// supply the current token with WithPutSecretToken.
func WithClientBound() PutSecretOption {
	return func(o *putSecretOptions) { o.clientBound = true }
}

// WithGenerateAccessToken asks the server to mint a per-secret access token,
// returned exactly once in PutSecretResult.AccessToken.
func WithGenerateAccessToken() PutSecretOption {
	return func(o *putSecretOptions) { o.generateAccessToken = true }
}

// WithExpiresAt sets an expiry (unix milliseconds) for the new secret version;
// 0 means never.
func WithExpiresAt(unixMS int64) PutSecretOption {
	return func(o *putSecretOptions) { o.expiresAtUnixMS = unixMS }
}

// WithPutSecretToken supplies the current credential in a PutSecret request,
// needed when updating an existing client-bound secret.
func WithPutSecretToken(token string) PutSecretOption {
	return func(o *putSecretOptions) { o.secretToken = token }
}

func applyPutSecretOptions(opts []PutSecretOption) putSecretOptions {
	var o putSecretOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}
