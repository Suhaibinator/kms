// Package integration exercises the real service stack end to end: a real
// SQLite store, real envelope crypto, and the core.Service business logic, with
// no transport layer. It covers plan §25.2 (integration) and §25.3 (security)
// for the parts that live below the gRPC/HTTP boundary, plus §25.4 fuzz targets
// for the policy and metadata parsers.
package integration

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
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
		admin: core.Principal{
			Identity:   domain.Identity{Name: "root", Kind: domain.IdentityKindAdmin},
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
	logger := slog.New(slog.NewTextHandler(h.logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc := core.New(store, logger, "test")
	keyring, err := crypto.Unseal(context.Background(), store, crypto.UnsealOptions{
		KeyFilePath:            h.keyPath,
		CreateKeyFileIfMissing: true,
	})
	if err != nil {
		_ = store.Close()
		h.tb.Fatalf("unseal: %v", err)
	}
	svc.SetKeyring(keyring)
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

// --- principals ------------------------------------------------------------

// createClient mints a client identity via the admin path and returns an
// authenticated principal plus its bearer token.
func (h *harness) createClient(name string) (core.Principal, string) {
	h.tb.Helper()
	_, token, err := h.svc.CreateIdentity(context.Background(), h.admin, name, domain.IdentityKindClient)
	if err != nil {
		h.tb.Fatalf("create identity %q: %v", name, err)
	}
	id, err := h.svc.Authenticate(context.Background(), token, "10.0.0.9", "client-agent")
	if err != nil {
		h.tb.Fatalf("authenticate %q: %v", name, err)
	}
	return core.Principal{Identity: id, RemoteAddr: "10.0.0.9", UserAgent: "client-agent", RequestID: "req-" + name}, token
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

func allowRule(op, path string) domain.PolicyRule {
	return domain.PolicyRule{Operation: op, Path: path}
}

// putSecret builds a plain (non-client-bound) secret write.
func putSecret(path, value string) core.PutSecretInput {
	return core.PutSecretInput{Path: path, Value: []byte(value)}
}

// mustPutParam writes a string parameter as admin or fails the test.
func mustPutParam(t *testing.T, h *harness, path, value string) {
	t.Helper()
	if _, _, err := h.svc.PutParameter(context.Background(), h.admin, path, value, "string", ""); err != nil {
		t.Fatalf("PutParameter %s: %v", path, err)
	}
}

// --- log buffer ------------------------------------------------------------

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
