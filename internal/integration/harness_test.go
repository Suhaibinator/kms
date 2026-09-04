// Package integration exercises the real service stack end to end. Fast tests
// call core.Service over a real SQLite store and envelope crypto, while the
// loopback harness also crosses TCP, TLS, gRPC interceptors/handlers, watches,
// SDK clients, and CLI subprocess boundaries. It also contains fuzz targets
// for the policy and metadata parsers.
package integration

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/internal/storage"
	// The database/sql driver "sqlite" (modernc) is registered transitively via
	// the storage package's glebarez/sqlite dependency; raw connections below
	// use it for schema inspection and tamper tests.
)

// harness owns a fully wired service over a temp-file database.
type harness struct {
	tb      testing.TB
	dir     string
	dbPath  string
	keyPath string
	store   *storage.SQLStore
	svc     *core.Service
	logBuf  *syncBuffer
	admin   core.Principal

	mu       sync.Mutex
	storeOff bool
	nsSeen   map[domain.NamespaceRef]bool
}

// newHarness builds a store, unseals a file-based master key, and returns a
// ready service plus an admin principal.
func newHarness(tb testing.TB) *harness {
	tb.Helper()
	dir := tb.TempDir()
	h := &harness{
		tb:      tb,
		dir:     dir,
		dbPath:  filepath.Join(dir, "kms.db"),
		keyPath: filepath.Join(dir, "master.key"),
		logBuf:  &syncBuffer{},
		nsSeen:  map[domain.NamespaceRef]bool{},
		admin: core.Principal{
			Identity: domain.Identity{Name: "root", Kind: domain.IdentityKindAdmin},
			// Admin-kind bypasses the per-namespace method gate; Method is set for
			// realism (a browser/CLI admin authenticates with a bearer token).
			Method:     domain.AuthMethodToken,
			RemoteAddr: "127.0.0.1",
			UserAgent:  "integration-test",
			RequestID:  "req-root",
		},
	}
	h.open()
	tb.Cleanup(h.closeStore)
	return h
}

// open (re)opens the store and service, unsealing the same key file each time.
func (h *harness) open() {
	h.tb.Helper()
	store, err := storage.Open(h.dbPath)
	if err != nil {
		h.tb.Fatalf("open store: %v", err)
	}
	svc := core.New(store, newTestLogger(h.logBuf), "test")
	keyring, err := crypto.Unseal(context.Background(), store, crypto.UnsealOptions{
		KeyFilePath:            h.keyPath,
		CreateKeyFileIfMissing: true,
	})
	if err != nil {
		_ = store.Close()
		h.tb.Fatalf("unseal: %v", err)
	}
	svc.SetKeyring(keyring)
	// Bootstrap the built-in CA (idempotent: generates on a fresh DB, loads the
	// stored key on reopen) so the identity-certificate and KEK-rotation flows
	// that touch CA keys are exercised end to end.
	if err := svc.BootstrapCA(context.Background()); err != nil {
		_ = store.Close()
		h.tb.Fatalf("bootstrap CA: %v", err)
	}
	h.store = store
	h.svc = svc
	h.storeOff = false
}

func (h *harness) closeStore() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.storeOff {
		return
	}
	if err := h.store.Close(); err != nil {
		h.tb.Errorf("close store: %v", err)
	}
	h.storeOff = true
}

// reopen closes and reopens the store+service, preserving the key file and DB.
// Used by tamper tests that must write raw rows while nothing else holds the DB.
func (h *harness) reopen(mutate func(db *sql.DB)) {
	h.tb.Helper()
	h.closeStore()
	if mutate != nil {
		h.withRawDB(mutate)
	}
	h.open()
}

// withRawDB opens a raw database/sql connection (the harness store must be
// closed first) and runs fn against it.
func (h *harness) withRawDB(fn func(db *sql.DB)) {
	h.tb.Helper()
	db, err := sql.Open("sqlite", h.dbPath)
	if err != nil {
		h.tb.Fatalf("raw open: %v", err)
	}
	defer func() { _ = db.Close() }()
	fn(db)
}

// scanBytes returns the raw bytes of the database and its WAL sidecar, so tests
// can assert what is (and is not) present on disk regardless of checkpoint state.
func (h *harness) scanBytes() []byte {
	h.tb.Helper()
	var out []byte
	for _, suffix := range []string{"", "-wal"} {
		b, err := os.ReadFile(h.dbPath + suffix)
		if err == nil {
			out = append(out, b...)
		}
	}
	return out
}

// --- refs & namespaces -----------------------------------------------------

// ref parses an "/env/app/key" display path into a domain.Ref. Tests keep using
// display paths for readability; the server never parses them (SplitDisplayPath
// is the SDK/CLI escape hatch).
func (h *harness) ref(path string) domain.Ref {
	h.tb.Helper()
	r, err := keyutil.SplitDisplayPath(path)
	if err != nil {
		h.tb.Fatalf("ref %q: %v", path, err)
	}
	return r
}

// nsRef builds a namespace reference from env and app.
func nsRef(env, app string) domain.NamespaceRef {
	return domain.NamespaceRef{Env: env, App: app}
}

// ensureNS creates the namespace addressed by path's (env, app) if it does not
// already exist, admitting both auth methods so token-authenticated clients pass
// the method gate and policy is the authorization actually exercised. It returns
// the parsed ref. Storage requires the namespace to pre-exist before any
// parameter or secret write.
func (h *harness) ensureNS(path string) domain.Ref {
	h.tb.Helper()
	r := h.ref(path)
	h.ensureNSRef(r.NS, domain.AuthMethodMTLS, domain.AuthMethodToken)
	return r
}

// ensureNSRef creates ns with the given allowed auth methods if absent
// (idempotent within a harness).
func (h *harness) ensureNSRef(ns domain.NamespaceRef, methods ...domain.AuthMethod) {
	h.tb.Helper()
	h.mu.Lock()
	seen := h.nsSeen[ns]
	h.mu.Unlock()
	if seen {
		return
	}
	if len(methods) == 0 {
		methods = []domain.AuthMethod{domain.AuthMethodMTLS, domain.AuthMethodToken}
	}
	if _, err := h.svc.CreateNamespace(context.Background(), h.admin, ns, "", methods); err != nil &&
		!errors.Is(err, domain.ErrAlreadyExists) {
		h.tb.Fatalf("create namespace %s: %v", ns, err)
	}
	h.mu.Lock()
	h.nsSeen[ns] = true
	h.mu.Unlock()
}

// --- principals ------------------------------------------------------------

// createClient mints an unbound token client via the admin path and returns an
// authenticated (token-method) principal plus its bearer token. Unbound clients
// have no implicit home grant, so their access is governed purely by policy.
func (h *harness) createClient(name string) (core.Principal, string) {
	h.tb.Helper()
	return h.createBoundClient(name, nil)
}

// createBoundClient mints a token client optionally bound to a home namespace
// (nil = unbound) and returns an authenticated token-method principal plus its
// bearer token.
func (h *harness) createBoundClient(name string, home *domain.NamespaceRef) (core.Principal, string) {
	h.tb.Helper()
	res, err := h.svc.CreateIdentity(context.Background(), h.admin, core.CreateIdentityInput{
		Name:        name,
		Kind:        domain.IdentityKindClient,
		Namespace:   home,
		AuthMethods: []domain.AuthMethod{domain.AuthMethodToken},
	})
	if err != nil {
		h.tb.Fatalf("create identity %q: %v", name, err)
	}
	id, err := h.svc.Authenticate(context.Background(), res.Token, "10.0.0.9", "client-agent")
	if err != nil {
		h.tb.Fatalf("authenticate %q: %v", name, err)
	}
	return core.Principal{
		Identity:   id,
		Method:     domain.AuthMethodToken,
		Token:      res.Token,
		RemoteAddr: "10.0.0.9",
		UserAgent:  "client-agent",
		RequestID:  "req-" + name,
	}, res.Token
}

// grant creates an allow/deny policy for a subject.
func (h *harness) grant(name, subject string, allow, deny []domain.PolicyRule) {
	h.tb.Helper()
	_, err := h.svc.CreatePolicy(context.Background(), h.admin, domain.Policy{
		Name:    name,
		Subject: subject,
		Allow:   allow,
		Deny:    deny,
	})
	if err != nil {
		h.tb.Fatalf("create policy %q: %v", name, err)
	}
}

// allowRule builds a namespace-level policy rule matching an operation against a
// namespace (env, app — exact or "*").
func allowRule(op, env, app string) domain.PolicyRule {
	return domain.PolicyRule{Operation: op, Env: env, App: app}
}

// stdSecret builds an unbound secret write, ensuring the target
// namespace exists first.
func (h *harness) stdSecret(path, value string) core.PutSecretInput {
	h.tb.Helper()
	return core.PutSecretInput{Ref: h.ensureNS(path), Value: []byte(value)}
}

// mustPutParam writes a string parameter as admin (creating the namespace if
// needed) or fails the test.
func mustPutParam(t *testing.T, h *harness, path, value string) {
	t.Helper()
	if _, _, err := h.svc.PutParameter(context.Background(), h.admin, h.ensureNS(path), value, "string", ""); err != nil {
		t.Fatalf("PutParameter %s: %v", path, err)
	}
}

// --- log buffer ------------------------------------------------------------

// newTestLogger builds a real *zap.Logger writing JSON to w at debug level. The
// security redaction test scans the captured buffer to assert no secret
// plaintext or token ever reaches the logs, so these loggers must emit real
// output rather than discard it (a Nop logger would make that assertion vacuous).
func newTestLogger(w io.Writer) *zap.Logger {
	enc := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	return zap.New(zapcore.NewCore(enc, zapcore.AddSync(w), zapcore.DebugLevel))
}

// syncBuffer is a concurrency-safe io.Writer capturing all log output.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
