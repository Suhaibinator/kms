package kmsclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

const (
	mdAuthorization     = "authorization"
	legacyMDSecretToken = "x-kms-secret-token"

	defaultTimeout = 5 * time.Second

	// callbackQueueSize bounds the OnChange dispatch queue. Value updates are
	// applied synchronously and independently of this queue, so a full queue
	// only ever drops (best-effort) change notifications, never a value.
	callbackQueueSize = 1024
)

// Config configures a Client.
type Config struct {
	// Endpoint is the server address as host:port. For custom transports it
	// may be any gRPC target; see DialOptions.
	Endpoint string

	// Namespace is the client's home namespace in "env/app" form (e.g.
	// "prod/gradethis"). Relative keys are resolved against it. Leave it empty
	// to discover the namespace from the identity at first use via WhoAmI; a
	// relative key on an unbound identity then fails with ErrNoNamespace.
	// Absolute "/env/app/key" display paths never require a namespace.
	Namespace string

	// Token is the per-client identity token, sent as
	// "authorization: Bearer <token>" on every RPC. It is optional when TLS
	// carries a client certificate (the identity then derives from the cert
	// server-side); required only for token-method identities. Empty is also
	// allowed for unauthenticated/dev servers.
	Token string

	// TLS configures transport security. Use TLSFromFiles or MTLSFromFiles to
	// build one. When TLS is nil, NewClient fails unless Insecure is explicitly
	// set or DialOptions supplies custom transport credentials.
	TLS *tls.Config

	// Insecure explicitly permits a cleartext connection. Use it only for local
	// development on a trusted network. It is mutually exclusive with TLS.
	Insecure bool

	// CacheTTL enables an in-memory read cache for GetParameter when greater
	// than zero. Secret plaintext is never cached because a version's live
	// protection requirements may change independently of its value.
	CacheTTL time.Duration

	// FallbackToDefaultsOnError controls whether declarative SecretValue /
	// ParameterValue fields fall back to their Default on ANY store-fetch error.
	//
	// By default (false) a Default is used only when the store affirmatively
	// reports the value absent (ErrNotFound). Every other error — unavailable,
	// timeout, unauthenticated, permission denied — fails Init/Resolve, so a
	// process cannot silently boot on a dev Default (e.g. "sk_test_...") merely
	// because the store was briefly unreachable at startup.
	//
	// Set true to restore the permissive any-error → Default behavior (plan
	// 9.2.9, "fallback behavior where explicitly configured"). Use it only where
	// booting on a stale or dev default is genuinely acceptable.
	FallbackToDefaultsOnError bool

	// Timeout is the default per-RPC deadline applied when the caller's context
	// has no earlier deadline. Defaults to 5s. Does not apply to the long-lived
	// Subscribe stream.
	Timeout time.Duration

	// ClientName identifies this client to the subscription registry (plan
	// 8.4). Defaults to the base name of os.Args[0].
	ClientName string

	// Logger receives operational log lines. It never receives secret
	// plaintext. Defaults to the standard library logger.
	Logger Logger

	// DialOptions are appended to the dial options the SDK builds, allowing
	// advanced transport tuning or injection of a custom dialer. Options here
	// override earlier ones (e.g. transport credentials). When neither TLS nor
	// Insecure is set, DialOptions must supply transport credentials; this is how
	// tests explicitly dial an in-process cleartext server.
	DialOptions []grpc.DialOption
}

// Client is a connection to the parameter store. It is safe for concurrent use
// and should be shared for the lifetime of the process. Call Close to release
// resources.
type Client struct {
	cfg        Config
	clientName string
	timeout    time.Duration
	logger     Logger

	cc       *grpc.ClientConn
	params   kmsv1.ParameterServiceClient
	secrets  kmsv1.SecretServiceClient
	watch    kmsv1.WatchServiceClient
	releases kmsv1.ConfigurationReleaseServiceClient
	schemas  kmsv1.ConfigurationSchemaServiceClient
	admin    kmsv1.AdminServiceClient

	// cfgNamespace is the parsed Config.Namespace (zero when unset). nsMu guards
	// the WhoAmI-discovered namespace, resolved lazily once for the client's
	// lifetime when cfgNamespace is unset and a relative key needs a namespace.
	cfgNamespace namespaceRef
	nsMu         sync.Mutex
	nsResolved   bool
	nsDiscovered namespaceRef

	cache *cache

	// rootCtx is the base context for background work (the Subscribe stream).
	// Cancelling it via Close tears everything down.
	rootCtx    context.Context
	rootCancel context.CancelFunc

	// callback dispatch: a single goroutine drains cbQueue so a slow or
	// panicking OnChange handler cannot stall the watch stream.
	cbQueue chan func()

	// subMu guards sub. Close reads it concurrently with the first subs() call,
	// so both accesses must be synchronized (defect L5).
	subMu sync.Mutex
	sub   *subManager

	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error
}

// NewClient dials the parameter store and returns a ready Client. Transport
// security must be selected explicitly with Config.TLS, Config.Insecure, or
// custom transport credentials in Config.DialOptions.
func NewClient(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" && len(cfg.DialOptions) == 0 {
		return nil, errors.New("kmsclient: Config.Endpoint is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.ClientName == "" {
		cfg.ClientName = defaultClientName()
	}
	if cfg.Logger == nil {
		cfg.Logger = stdLogger{}
	}

	var nsRef namespaceRef
	if cfg.Namespace != "" {
		var err error
		nsRef, err = parseNamespace(cfg.Namespace)
		if err != nil {
			return nil, err
		}
	}

	opts := make([]grpc.DialOption, 0, 4+len(cfg.DialOptions))
	switch {
	case cfg.TLS != nil && cfg.Insecure:
		return nil, errors.New("kmsclient: Config.TLS and Config.Insecure are mutually exclusive")
	case cfg.TLS != nil:
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(cfg.TLS)))
	case cfg.Insecure:
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	case len(cfg.DialOptions) == 0:
		return nil, errors.New("kmsclient: transport security is required; set Config.TLS, or set Config.Insecure only for local development")
	}
	opts = append(opts, grpc.WithKeepaliveParams(keepalive.ClientParameters{
		Time:                30 * time.Second,
		Timeout:             10 * time.Second,
		PermitWithoutStream: true,
	}))
	// User-supplied options come last so they can override the defaults above
	// (e.g. swap in insecure creds and a bufconn dialer for tests).
	opts = append(opts, cfg.DialOptions...)

	target := cfg.Endpoint
	if target == "" {
		target = "passthrough:///kmsclient"
	}
	cc, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("kmsclient: dial %q: %w", target, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		cfg:          cfg,
		clientName:   cfg.ClientName,
		timeout:      cfg.Timeout,
		logger:       cfg.Logger,
		cc:           cc,
		params:       kmsv1.NewParameterServiceClient(cc),
		secrets:      kmsv1.NewSecretServiceClient(cc),
		watch:        kmsv1.NewWatchServiceClient(cc),
		releases:     kmsv1.NewConfigurationReleaseServiceClient(cc),
		schemas:      kmsv1.NewConfigurationSchemaServiceClient(cc),
		admin:        kmsv1.NewAdminServiceClient(cc),
		cfgNamespace: nsRef,
		cache:        newCache(cfg.CacheTTL),
		rootCtx:      ctx,
		rootCancel:   cancel,
		cbQueue:      make(chan func(), callbackQueueSize),
		closed:       make(chan struct{}),
	}
	go c.dispatchCallbacks()
	return c, nil
}

func defaultClientName() string {
	if len(os.Args) > 0 && os.Args[0] != "" {
		return filepath.Base(os.Args[0])
	}
	return "kmsclient-client"
}

// Close releases the connection and stops all background goroutines. It is safe
// to call multiple times.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.subMu.Lock()
		sub := c.sub
		c.subMu.Unlock()
		if sub != nil {
			sub.stop()
		}
		c.rootCancel()
		close(c.closed)
		c.closeErr = c.cc.Close()
	})
	return c.closeErr
}

// defaultAllowedForErr reports whether a declarative field may fall back to its
// Default given a store-fetch error. Only an affirmative "not found" qualifies
// unless the caller has explicitly opted into permissive fallback.
func (c *Client) defaultAllowedForErr(err error) bool {
	return errors.Is(err, ErrNotFound) || c.cfg.FallbackToDefaultsOnError
}

func (c *Client) logf(format string, args ...any) {
	if c.logger != nil {
		c.logger.Printf(format, args...)
	}
}

// callCtx derives a per-RPC context: it applies the default timeout (unless the
// caller already set an earlier deadline) and attaches identity metadata.
func (c *Client) callCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx = c.withAuth(ctx)
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.timeout)
}

// withAuth attaches standard identity metadata to an outgoing context. Using
// AppendToOutgoingContext preserves any metadata a caller may have set.
func (c *Client) withAuth(ctx context.Context) context.Context {
	// A caller may reuse a context created for an older SDK version. Strip the
	// deprecated credential key so it cannot escape on any RPC.
	if md, ok := metadata.FromOutgoingContext(ctx); ok && len(md.Get(legacyMDSecretToken)) > 0 {
		clean := md.Copy()
		clean.Delete(legacyMDSecretToken)
		ctx = metadata.NewOutgoingContext(ctx, clean)
	}
	kv := make([]string, 0, 2)
	if c.cfg.Token != "" {
		kv = append(kv, mdAuthorization, "Bearer "+c.cfg.Token)
	}
	if len(kv) == 0 {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, kv...)
}

// resolveNamespace returns the client's namespace. Config.Namespace wins; when
// it is empty the namespace is discovered once from the identity via WhoAmI and
// cached for the client's lifetime. The bool reports whether a namespace is
// bound at all (false = unbound identity with no Config.Namespace). A transient
// WhoAmI error is returned without caching so a later call retries.
func (c *Client) resolveNamespace(ctx context.Context) (namespaceRef, bool, error) {
	if !c.cfgNamespace.isZero() {
		return c.cfgNamespace, true, nil
	}
	c.nsMu.Lock()
	defer c.nsMu.Unlock()
	if c.nsResolved {
		return c.nsDiscovered, !c.nsDiscovered.isZero(), nil
	}
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.admin.WhoAmI(cctx, &kmsv1.WhoAmIRequest{})
	if err != nil {
		return namespaceRef{}, false, mapError(err)
	}
	n := resp.GetNamespace()
	c.nsDiscovered = namespaceRef{env: n.GetEnv(), app: n.GetApp()}
	c.nsResolved = true
	return c.nsDiscovered, !c.nsDiscovered.isZero(), nil
}

// resolveRef turns a user-supplied key into a fully-qualified ref. A leading
// "/" is an absolute display path ("/env/app/key...") split in the SDK for
// cross-namespace access; any other key is relative to the client namespace. A
// relative key on a client with no namespace fails with ErrNoNamespace, naming
// the key.
func (c *Client) resolveRef(ctx context.Context, key string) (ref, error) {
	if strings.HasPrefix(key, "/") {
		return splitDisplayPath(key)
	}
	ns, bound, err := c.resolveNamespace(ctx)
	if err != nil {
		return ref{}, err
	}
	if !bound {
		return ref{}, fmt.Errorf("%w: relative key %q needs a namespace (set Config.Namespace or bind the identity)", ErrNoNamespace, key)
	}
	return ref{ns: ns, key: key}, nil
}

// GetParameter returns the value of a non-secret parameter. key is relative to
// the client namespace, or an absolute "/env/app/key" display path. By default
// it reads the "current" label; use WithVersion or WithLabel to read another.
func (c *Client) GetParameter(ctx context.Context, key string, opts ...GetOption) (string, error) {
	o := applyGetOptions(opts)
	r, err := c.resolveRef(ctx, key)
	if err != nil {
		return "", err
	}
	return c.getParameter(ctx, r, o)
}

// getParameter reads a resolved ref through the cache, falling back to a fetch.
func (c *Client) getParameter(ctx context.Context, r ref, o getOptions) (string, error) {
	if v, ok := c.cache.getParam(r.display(), o.version, o.label); ok {
		return v, nil
	}
	return c.fetchParameter(ctx, r, o)
}

// fetchParameter issues the GetParameter RPC unconditionally (skipping the cache
// read) and refreshes the cache with the result. The subscription reconciler
// uses it to detect drift the cache would otherwise hide.
func (c *Client) fetchParameter(ctx context.Context, r ref, o getOptions) (string, error) {
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.params.GetParameter(cctx, &kmsv1.GetParameterRequest{
		Ref:     r.resourceProto(),
		Version: o.version,
		Label:   o.label,
	})
	if err != nil {
		return "", mapError(err)
	}
	value := resp.GetParameter().GetValue()
	c.cache.putParam(r.display(), o.version, o.label, value)
	return value, nil
}

// GetSecret returns a secret. The returned Secret redacts itself in logs and
// string/JSON formatting; call Value or StringValue for plaintext. Use
// WithSecretToken for token-protected secrets and WithBindingKey for bound
// secrets. The credentials are independent and a version may require both.
// Secret plaintext is never cached because protection is live mutable state.
func (c *Client) GetSecret(ctx context.Context, key string, opts ...GetOption) (Secret, error) {
	o := applyGetOptions(opts)
	r, err := c.resolveRef(ctx, key)
	if err != nil {
		return Secret{}, err
	}
	display := r.display()

	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.secrets.GetSecret(cctx, &kmsv1.GetSecretRequest{
		Ref:         r.resourceProto(),
		Version:     o.version,
		Label:       o.label,
		SecretToken: o.secretToken,
		BindingKey:  o.bindingKey,
	})
	if err != nil {
		return Secret{}, mapSecretError(err)
	}
	path := display
	if resp.GetRef() != nil {
		path = refFromProto(resp.GetRef()).display()
	}
	s := Secret{
		value:       resp.GetValue(),
		path:        path,
		version:     resp.GetVersion(),
		contentType: resp.GetContentType(),
	}
	return s, nil
}

// PutParameterResult reports the outcome of a parameter write.
type PutParameterResult struct {
	Version  uint64
	Revision uint64
}

// PutParameter creates a new immutable version of a parameter. key is relative
// to the client namespace, or an absolute "/env/app/key" display path. It is
// intended for tooling; most applications only read.
func (c *Client) PutParameter(ctx context.Context, key, value string, opts ...PutOption) (PutParameterResult, error) {
	o := applyPutOptions(opts)
	r, err := c.resolveRef(ctx, key)
	if err != nil {
		return PutParameterResult{}, err
	}
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.params.PutParameter(cctx, &kmsv1.PutParameterRequest{
		Ref:          r.resourceProto(),
		Value:        value,
		ContentType:  o.contentType,
		MetadataJson: o.metadataJSON,
	})
	if err != nil {
		return PutParameterResult{}, mapError(err)
	}
	c.cache.invalidateParam(r.display())
	return PutParameterResult{Version: resp.GetVersion(), Revision: resp.GetRevision()}, nil
}

// PutSecretResult reports the outcome of a secret write. AccessToken is set only
// when WithGenerateAccessToken was supplied, and is never retrievable again.
type PutSecretResult struct {
	Version     uint64
	Revision    uint64
	AccessToken string
}

// PutSecret creates a new immutable version of a secret. key is relative to the
// client namespace, or an absolute "/env/app/key" display path. It is intended
// for tooling.
func (c *Client) PutSecret(ctx context.Context, key string, value []byte, opts ...PutSecretOption) (PutSecretResult, error) {
	o := applyPutSecretOptions(opts)
	r, err := c.resolveRef(ctx, key)
	if err != nil {
		return PutSecretResult{}, err
	}
	cctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.secrets.PutSecret(cctx, &kmsv1.PutSecretRequest{
		Ref:                 r.resourceProto(),
		Value:               value,
		ContentType:         o.contentType,
		MetadataJson:        o.metadataJSON,
		BindingKey:          o.bindingKey,
		GenerateAccessToken: o.generateAccessToken,
		ExpiresAtUnixMs:     o.expiresAtUnixMS,
	})
	if err != nil {
		return PutSecretResult{}, mapSecretError(err)
	}
	return PutSecretResult{
		Version:     resp.GetVersion(),
		Revision:    resp.GetRevision(),
		AccessToken: resp.GetAccessToken(),
	}, nil
}

// subs lazily starts and returns the subscription manager.
func (c *Client) subs() *subManager {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	if c.sub == nil {
		c.sub = newSubManager(c)
	}
	return c.sub
}

// enqueueCallback submits a callback invocation to the shared dispatch
// goroutine. It never blocks: if the queue is full the notification is dropped
// (values are already updated) and a warning is logged.
func (c *Client) enqueueCallback(path string, fn func()) {
	select {
	case c.cbQueue <- fn:
	case <-c.closed:
	default:
		c.logf("kmsclient: callback queue full, dropping change notification for %s", path)
	}
}

func (c *Client) dispatchCallbacks() {
	for {
		select {
		case <-c.closed:
			return
		case fn := <-c.cbQueue:
			c.runCallback(fn)
		}
	}
}

func (c *Client) runCallback(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			c.logf("kmsclient: recovered panic in change callback: %v", r)
		}
	}()
	fn()
}
