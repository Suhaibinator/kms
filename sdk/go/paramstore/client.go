package paramstore

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	mdAuthorization = "authorization"
	mdSecretToken   = "x-kms-secret-token"

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

	// Token is the per-client identity token, sent as
	// "authorization: Bearer <token>" on every RPC. Empty is allowed for
	// unauthenticated/dev servers.
	Token string

	// TLS configures transport security. Nil means an insecure connection
	// (development only). Use TLSFromFiles or MTLSFromFiles to build one.
	TLS *tls.Config

	// CacheTTL enables an in-memory read cache for GetParameter/GetSecret when
	// greater than zero. Cached parameter and secret entries are invalidated by
	// watch events when a subscription is active.
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
	// override earlier ones (e.g. transport credentials), which is how tests
	// dial an in-process server.
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

	cc      *grpc.ClientConn
	params  kmsv1.ParameterServiceClient
	secrets kmsv1.SecretServiceClient
	watch   kmsv1.WatchServiceClient

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

// NewClient dials the parameter store and returns a ready Client.
func NewClient(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" && len(cfg.DialOptions) == 0 {
		return nil, errors.New("paramstore: Config.Endpoint is required")
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

	opts := make([]grpc.DialOption, 0, 4+len(cfg.DialOptions))
	if cfg.TLS != nil {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(cfg.TLS)))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
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
		target = "passthrough:///paramstore"
	}
	cc, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("paramstore: dial %q: %w", target, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		cfg:        cfg,
		clientName: cfg.ClientName,
		timeout:    cfg.Timeout,
		logger:     cfg.Logger,
		cc:         cc,
		params:     kmsv1.NewParameterServiceClient(cc),
		secrets:    kmsv1.NewSecretServiceClient(cc),
		watch:      kmsv1.NewWatchServiceClient(cc),
		cache:      newCache(cfg.CacheTTL),
		rootCtx:    ctx,
		rootCancel: cancel,
		cbQueue:    make(chan func(), callbackQueueSize),
		closed:     make(chan struct{}),
	}
	go c.dispatchCallbacks()
	return c, nil
}

func defaultClientName() string {
	if len(os.Args) > 0 && os.Args[0] != "" {
		return filepath.Base(os.Args[0])
	}
	return "paramstore-client"
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
// caller already set an earlier deadline) and attaches identity + secret-token
// metadata.
func (c *Client) callCtx(ctx context.Context, secretToken string) (context.Context, context.CancelFunc) {
	ctx = c.withAuth(ctx, secretToken)
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.timeout)
}

// withAuth attaches identity and optional secret-token metadata to an outgoing
// context. Using AppendToOutgoingContext preserves any metadata a caller may
// have set.
func (c *Client) withAuth(ctx context.Context, secretToken string) context.Context {
	kv := make([]string, 0, 4)
	if c.cfg.Token != "" {
		kv = append(kv, mdAuthorization, "Bearer "+c.cfg.Token)
	}
	if secretToken != "" {
		kv = append(kv, mdSecretToken, secretToken)
	}
	if len(kv) == 0 {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, kv...)
}

// GetParameter returns the value of a non-secret parameter. By default it reads
// the "current" label; use WithVersion or WithLabel to read another.
func (c *Client) GetParameter(ctx context.Context, path string, opts ...GetOption) (string, error) {
	o := applyGetOptions(opts)
	if v, ok := c.cache.getParam(path, o.version, o.label); ok {
		return v, nil
	}
	return c.fetchParameter(ctx, path, o)
}

// fetchParameter issues the GetParameter RPC unconditionally (skipping the cache
// read) and refreshes the cache with the result. The subscription reconciler
// uses it to detect drift the cache would otherwise hide.
func (c *Client) fetchParameter(ctx context.Context, path string, o getOptions) (string, error) {
	cctx, cancel := c.callCtx(ctx, o.secretToken)
	defer cancel()
	resp, err := c.params.GetParameter(cctx, &kmsv1.GetParameterRequest{
		Path:    path,
		Version: o.version,
		Label:   o.label,
	})
	if err != nil {
		return "", mapError(err)
	}
	value := resp.GetParameter().GetValue()
	c.cache.putParam(path, o.version, o.label, value)
	return value, nil
}

// GetSecret returns a secret. The returned Secret redacts itself in logs and
// string/JSON formatting; call Value or StringValue for plaintext. Use
// WithSecretToken for token-protected or client-bound secrets.
//
// Token-gated reads (WithSecretToken) bypass the client cache entirely: caching
// them under the token-less key would let later calls without the token read
// the plaintext from cache, skipping the server's per-secret token check, and
// would keep serving after a token rotation until the TTL expired.
func (c *Client) GetSecret(ctx context.Context, path string, opts ...GetOption) (Secret, error) {
	o := applyGetOptions(opts)
	if o.secretToken == "" {
		if s, ok := c.cache.getSecret(path, o.version, o.label); ok {
			return s, nil
		}
	}

	cctx, cancel := c.callCtx(ctx, o.secretToken)
	defer cancel()
	resp, err := c.secrets.GetSecret(cctx, &kmsv1.GetSecretRequest{
		Path:    path,
		Version: o.version,
		Label:   o.label,
	})
	if err != nil {
		return Secret{}, mapError(err)
	}
	s := Secret{
		value:       resp.GetValue(),
		path:        resp.GetPath(),
		version:     resp.GetVersion(),
		contentType: resp.GetContentType(),
	}
	if s.path == "" {
		s.path = path
	}
	if o.secretToken == "" {
		c.cache.putSecret(path, o.version, o.label, s)
	}
	return s, nil
}

// PutParameterResult reports the outcome of a parameter write.
type PutParameterResult struct {
	Version  uint64
	Revision uint64
}

// PutParameter creates a new immutable version of a parameter. It is intended
// for tooling; most applications only read.
func (c *Client) PutParameter(ctx context.Context, path, value string, opts ...PutOption) (PutParameterResult, error) {
	o := applyPutOptions(opts)
	cctx, cancel := c.callCtx(ctx, "")
	defer cancel()
	resp, err := c.params.PutParameter(cctx, &kmsv1.PutParameterRequest{
		Path:         path,
		Value:        value,
		ContentType:  o.contentType,
		MetadataJson: o.metadataJSON,
	})
	if err != nil {
		return PutParameterResult{}, mapError(err)
	}
	c.cache.invalidateParam(path)
	return PutParameterResult{Version: resp.GetVersion(), Revision: resp.GetRevision()}, nil
}

// PutSecretResult reports the outcome of a secret write. AccessToken is set only
// when WithGenerateAccessToken was supplied, and is never retrievable again.
type PutSecretResult struct {
	Version     uint64
	Revision    uint64
	AccessToken string
}

// PutSecret creates a new immutable version of a secret. It is intended for
// tooling.
func (c *Client) PutSecret(ctx context.Context, path string, value []byte, opts ...PutSecretOption) (PutSecretResult, error) {
	o := applyPutSecretOptions(opts)
	cctx, cancel := c.callCtx(ctx, o.secretToken)
	defer cancel()
	resp, err := c.secrets.PutSecret(cctx, &kmsv1.PutSecretRequest{
		Path:                path,
		Value:               value,
		ContentType:         o.contentType,
		MetadataJson:        o.metadataJSON,
		ClientBound:         o.clientBound,
		GenerateAccessToken: o.generateAccessToken,
		ExpiresAtUnixMs:     o.expiresAtUnixMS,
	})
	if err != nil {
		return PutSecretResult{}, mapError(err)
	}
	c.cache.invalidateSecret(path)
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
		c.logf("paramstore: callback queue full, dropping change notification for %s", path)
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
			c.logf("paramstore: recovered panic in change callback: %v", r)
		}
	}()
	fn()
}
