package httpserver

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// testEnv wires a real core.Service over the in-memory fakeStore behind the
// HTTP handler, with a seeded admin and client identity.
type testEnv struct {
	t           *testing.T
	store       *fakeStore
	svc         *core.Service
	handler     http.Handler
	adminToken  string
	clientToken string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvWith(t, true)
}

// newReleaseTestEnv uses the real schema-v2 store because the legacy HTTP
// fakeStore intentionally implements only the pre-release storage contract.
func newReleaseTestEnv(t *testing.T) *testEnv {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatalf("open release test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	kek, err := crypto.NewKEKFromMaterial("kek-test", make([]byte, 32))
	if err != nil {
		t.Fatalf("build kek: %v", err)
	}
	keyCheck, err := crypto.NewKeyCheck(kek)
	if err != nil {
		t.Fatalf("build key check: %v", err)
	}
	if err := store.InsertKeyMetadata(ctx, domain.KeyMetadata{ID: "kek-test", Source: domain.KeySourceFile, KeyCheck: keyCheck, State: domain.KeyStateActive, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("seed key metadata: %v", err)
	}
	svc := core.New(store, zap.NewNop(), "test-version")
	// The seeded admin is token-only and every request here carries a bearer
	// token alone; the admin client-certificate requirement has its own suite
	// (admin_cert_test.go), which turns it back on.
	svc.SetAdminRequireClientCert(false)
	svc.SetKeyring(crypto.NewKeyring(kek))
	adminToken, adminHash, err := crypto.GenerateToken("kms")
	if err != nil {
		t.Fatalf("generate admin token: %v", err)
	}
	if _, err := store.CreateIdentity(ctx, storage.CreateIdentityParams{Name: "admin", Kind: domain.IdentityKindAdmin, TokenHash: adminHash}); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	srv, err := New(svc, Config{Addr: ":0", FrontendEnabled: false, Version: "test-version"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &testEnv{t: t, svc: svc, handler: srv.Handler, adminToken: adminToken}
}

// newTestEnvWith builds the environment; ready=false leaves the keyring
// unattached so readiness gating can be exercised.
func newTestEnvWith(t *testing.T, ready bool) *testEnv {
	t.Helper()
	store := newFakeStore()
	logger := zap.NewNop()
	svc := core.New(store, logger, "test-version")
	// The seeded admin is token-only and every request here carries a bearer
	// token alone; the admin client-certificate requirement has its own suite
	// (admin_cert_test.go), which turns it back on.
	svc.SetAdminRequireClientCert(false)

	ctx := context.Background()
	_ = store.InsertKeyMetadata(ctx, domain.KeyMetadata{
		ID: "kek-test", Source: domain.KeySourceFile, State: domain.KeyStateActive, CreatedAt: time.Now().UTC(),
	})
	if ready {
		kek, err := crypto.NewKEKFromMaterial("kek-test", make([]byte, 32))
		if err != nil {
			t.Fatalf("build kek: %v", err)
		}
		svc.SetKeyring(crypto.NewKeyring(kek))
		// Bootstrap the built-in CA so the certificate endpoints work.
		if err := svc.BootstrapCA(ctx); err != nil {
			t.Fatalf("bootstrap CA: %v", err)
		}
	}

	adminToken, adminHash, _ := crypto.GenerateToken("kms")
	if _, err := store.CreateIdentity(ctx, storage.CreateIdentityParams{
		Name: "admin", Kind: domain.IdentityKindAdmin, TokenHash: adminHash,
	}); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	clientToken, clientHash, _ := crypto.GenerateToken("kms")
	if _, err := store.CreateIdentity(ctx, storage.CreateIdentityParams{
		Name: "client", Kind: domain.IdentityKindClient, TokenHash: clientHash,
	}); err != nil {
		t.Fatalf("seed client: %v", err)
	}

	srv, err := New(svc, Config{Addr: ":0", FrontendEnabled: false, Version: "test-version"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &testEnv{t: t, store: store, svc: svc, handler: srv.Handler, adminToken: adminToken, clientToken: clientToken}
}

func (e *testEnv) do(method, target string, body any, headers map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	var req *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal body: %v", err)
		}
		req = httptest.NewRequest(method, target, bytes.NewReader(b))
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)
	return w
}

func (e *testEnv) admin(method, target string, body any) *httptest.ResponseRecorder {
	return e.do(method, target, body, map[string]string{"Authorization": "Bearer " + e.adminToken})
}

func (e *testEnv) client(method, target string, body any) *httptest.ResponseRecorder {
	return e.do(method, target, body, map[string]string{"Authorization": "Bearer " + e.clientToken})
}

// decodeBody unmarshals a JSON response body into a generic map.
func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	return m
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	m := decodeBody(t, w)
	errObj, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error object in %q", w.Body.String())
	}
	code, _ := errObj["code"].(string)
	return code
}

func mustStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, want, w.Body.String())
	}
}

// newReadyService builds a ready core.Service (keyring attached) over store,
// used by tests that only need the service wired, not seeded identities.
func newReadyService(t *testing.T, store *fakeStore) *core.Service {
	t.Helper()
	logger := zap.NewNop()
	svc := core.New(store, logger, "v")
	kek, err := crypto.NewKEKFromMaterial("kek-test", make([]byte, 32))
	if err != nil {
		t.Fatalf("build kek: %v", err)
	}
	svc.SetKeyring(crypto.NewKeyring(kek))
	return svc
}
