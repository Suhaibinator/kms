package kmsclient

// getOptions is the resolved set of per-call read options.
type getOptions struct {
	version     uint64
	label       string
	secretToken string
	bindingKey  BindingKey
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

// WithSecretToken supplies the per-secret access token in a GetSecret request.
// GetParameter accepts this shared option for compatibility but never
// transmits the token.
func WithSecretToken(token string) GetOption {
	return func(o *getOptions) { o.secretToken = token }
}

// WithBindingKey supplies the independent operator-owned binding key in a
// GetSecret request. GetParameter accepts this shared option but never
// transmits the key.
func WithBindingKey(key string) GetOption {
	return WithBindingKeyValue(NewBindingKey(key))
}

// WithBindingKeyValue supplies an opaque binding credential in a GetSecret
// request without exposing its plaintext. GetParameter never transmits it.
func WithBindingKeyValue(key BindingKey) GetOption {
	return func(o *getOptions) { o.bindingKey = key }
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
	bindingKey          BindingKey
	generateAccessToken bool
	expiresAtUnixMS     int64
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

// WithPutBindingKey creates the new version bound to key. An empty key creates
// an unbound version; the server validates non-empty keys.
func WithPutBindingKey(key string) PutSecretOption {
	return WithPutBindingKeyValue(NewBindingKey(key))
}

// WithPutBindingKeyValue creates the new version bound to an opaque credential.
// The zero BindingKey creates an unbound version; the server validates keys.
func WithPutBindingKeyValue(key BindingKey) PutSecretOption {
	return func(o *putSecretOptions) { o.bindingKey = key }
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

func applyPutSecretOptions(opts []PutSecretOption) putSecretOptions {
	var o putSecretOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}
