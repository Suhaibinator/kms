package httpserver

import (
	"context"
	"encoding/base64"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// newPostureEnv wires the handler over the real SQL store, which is the only
// implementation with the optional expiry-listing capabilities, and over a
// Config in the strong posture so every advertised boolean has a value to be
// wrong about. The seeded admin is token-only (see newTestEnvWith).
func newPostureEnv(t *testing.T) *testEnv {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatalf("open posture test store: %v", err)
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
	if err := store.InsertKeyMetadata(ctx, domain.KeyMetadata{
		ID: "kek-test", Source: domain.KeySourceFile, KeyCheck: keyCheck,
		State: domain.KeyStateActive, CreatedAt: time.Now().UTC().Add(-48 * time.Hour),
	}); err != nil {
		t.Fatalf("seed key metadata: %v", err)
	}

	svc := core.New(store, zap.NewNop(), "test-version")
	svc.SetAdminRequireClientCert(false)
	svc.SetKeyring(crypto.NewKeyring(kek))
	if err := svc.BootstrapCA(ctx); err != nil {
		t.Fatalf("bootstrap CA: %v", err)
	}

	adminToken, adminHash, err := crypto.GenerateToken("kms")
	if err != nil {
		t.Fatalf("generate admin token: %v", err)
	}
	if _, err := store.CreateIdentity(ctx, storage.CreateIdentityParams{
		Name: "admin", Kind: domain.IdentityKindAdmin, TokenHash: adminHash,
	}); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	clientToken, clientHash, err := crypto.GenerateToken("kms")
	if err != nil {
		t.Fatalf("generate client token: %v", err)
	}
	if _, err := store.CreateIdentity(ctx, storage.CreateIdentityParams{
		Name: "client", Kind: domain.IdentityKindClient, TokenHash: clientHash,
	}); err != nil {
		t.Fatalf("seed client: %v", err)
	}

	srv, err := New(svc, Config{
		Addr: ":0", Version: "test-version",
		TLSEnabled: true, MTLSEnabled: true, AdminClientCertRequired: true,
		AuditEnabled: true, AuditRetainDuration: 90 * 24 * time.Hour, AuditArchiveEnabled: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &testEnv{t: t, svc: svc, handler: srv.Handler, adminToken: adminToken, clientToken: clientToken}
}

func (e *testEnv) posture(query string) map[string]any {
	e.t.Helper()
	w := e.admin(http.MethodGet, "/api/v1/posture"+query, nil)
	mustStatus(e.t, w, http.StatusOK)
	return decodeBody(e.t, w)
}

// postureObject descends one level of the response, failing rather than
// silently reading a zero value out of a missing object.
func postureObject(t *testing.T, body map[string]any, key string) map[string]any {
	t.Helper()
	obj, ok := body[key].(map[string]any)
	if !ok {
		t.Fatalf("posture.%s is %T, want an object", key, body[key])
	}
	return obj
}

func postureItems(t *testing.T, body map[string]any, key string) []any {
	t.Helper()
	items, ok := postureObject(t, body, key)["items"].([]any)
	if !ok {
		t.Fatalf("posture.%s.items is not an array", key)
	}
	return items
}

// TestPostureAdminOnly: the API pipeline refuses an anonymous caller before the
// handler runs, and core's admin gate refuses an authenticated non-admin.
func TestPostureAdminOnly(t *testing.T) {
	e := newPostureEnv(t)

	anon := e.do(http.MethodGet, "/api/v1/posture", nil, nil)
	mustStatus(t, anon, http.StatusUnauthorized)
	if code := errCode(t, anon); code != "unauthenticated" {
		t.Errorf("anonymous error code = %q, want unauthenticated", code)
	}

	client := e.client(http.MethodGet, "/api/v1/posture", nil)
	mustStatus(t, client, http.StatusForbidden)
	if code := errCode(t, client); code != "permission_denied" {
		t.Errorf("client error code = %q, want permission_denied", code)
	}

	mustStatus(t, e.admin(http.MethodGet, "/api/v1/posture", nil), http.StatusOK)
}

// TestPostureWindows pins the window contract: 30 days by default, both
// spellings accepted, the admin-certificate window fixed whatever is asked for,
// and anything meaningless refused rather than clamped.
func TestPostureWindows(t *testing.T) {
	e := newPostureEnv(t)

	windows := postureObject(t, e.posture(""), "windows")
	if windows["cert"] != "720h0m0s" || windows["secret"] != "720h0m0s" {
		t.Errorf("default windows = %v, want 720h0m0s each", windows)
	}
	if windows["admin_cert"] != "336h0m0s" {
		t.Errorf("admin cert window = %v, want the fixed 336h0m0s", windows["admin_cert"])
	}

	windows = postureObject(t, e.posture("?cert_window=7d&secret_window=48h"), "windows")
	if windows["cert"] != "168h0m0s" || windows["secret"] != "48h0m0s" {
		t.Errorf("windows = %v, want 168h0m0s and 48h0m0s", windows)
	}
	// Not a parameter: asking for a different admin window changes nothing.
	if windows["admin_cert"] != "336h0m0s" {
		t.Errorf("admin cert window = %v, want the fixed 336h0m0s", windows["admin_cert"])
	}

	for _, bad := range []string{
		"?cert_window=0", "?cert_window=0d", "?cert_window=-1h", "?cert_window=-3d",
		"?cert_window=soon", "?cert_window=366d", "?cert_window=9000h",
		"?secret_window=0", "?secret_window=-1h", "?secret_window=later", "?secret_window=400d",
	} {
		w := e.admin(http.MethodGet, "/api/v1/posture"+bad, nil)
		mustStatus(t, w, http.StatusBadRequest)
		if code := errCode(t, w); code != "invalid_argument" {
			t.Errorf("%s error code = %q, want invalid_argument", bad, code)
		}
	}
}

// seedPostureFixtures creates the namespace, identities and secrets the content
// and leak tests read back, and returns the credential material the response
// must never contain.
func seedPostureFixtures(t *testing.T, e *testEnv) (secrets []string) {
	t.Helper()

	mustStatus(t, e.admin(http.MethodPost, "/api/v1/namespaces",
		map[string]any{"env": "prod", "app": "payments", "allowed_auth_methods": []string{"mtls", "token"}}),
		http.StatusOK)

	// Two mTLS identities whose certificates expire at different times, plus a
	// token identity that holds no certificate at all.
	for name, ttl := range map[string]int64{
		"svc-later":  int64((5 * 24 * time.Hour).Seconds()),
		"svc-sooner": int64((25 * time.Hour).Seconds()),
	} {
		w := e.admin(http.MethodPost, "/api/v1/identities", map[string]any{
			"name": name, "kind": "client", "namespace": map[string]string{"env": "prod", "app": "payments"},
			"auth_methods": []string{"mtls"}, "cert_ttl_seconds": ttl,
		})
		mustStatus(t, w, http.StatusOK)
		cert, ok := decodeBody(t, w)["cert"].(map[string]any)
		if !ok {
			t.Fatalf("create %s returned no cert bundle", name)
		}
		secrets = append(secrets, cert["cert_pem"].(string), cert["key_pem"].(string))
	}

	// A client-bound secret: its value, its ciphertext, and its per-secret
	// access token are all things the posture must never echo.
	value := "s3cr3t-database-password"
	w := e.admin(http.MethodPost, "/api/v1/secrets", map[string]any{
		"env": "prod", "app": "payments", "key": "db/password",
		"value_base64": base64.StdEncoding.EncodeToString([]byte(value)),
		"client_bound": true, "generate_access_token": true,
		"expires_at_unix_ms": time.Now().Add(36 * time.Hour).UnixMilli(),
	})
	mustStatus(t, w, http.StatusOK)
	if token, ok := decodeBody(t, w)["access_token"].(string); ok && token != "" {
		secrets = append(secrets, token)
	}
	secrets = append(secrets, value)

	mustStatus(t, e.admin(http.MethodPost, "/api/v1/secrets", map[string]any{
		"env": "prod", "app": "payments", "key": "stripe/api-key",
		"value_base64":       base64.StdEncoding.EncodeToString([]byte("sk_live_never_shown")),
		"expires_at_unix_ms": time.Now().Add(12 * time.Hour).UnixMilli(),
	}), http.StatusOK)
	secrets = append(secrets, "sk_live_never_shown")

	// One secret with no expiry, which must not appear in the list at all.
	mustStatus(t, e.admin(http.MethodPost, "/api/v1/secrets", map[string]any{
		"env": "prod", "app": "payments", "key": "no-expiry",
		"value_base64": base64.StdEncoding.EncodeToString([]byte("v")),
	}), http.StatusOK)

	return secrets
}

// TestPostureContent reads the snapshot back: the lists name what expires
// soonest first, the totals agree with them, and every advertised setting is
// the server's, not a default.
func TestPostureContent(t *testing.T) {
	e := newPostureEnv(t)
	seedPostureFixtures(t, e)

	body := e.posture("?cert_window=7d&secret_window=7d")

	if generated, _ := body["generated_at"].(string); generated == "" {
		t.Error("generated_at is empty")
	} else if _, err := time.Parse(time.RFC3339, generated); err != nil {
		t.Errorf("generated_at %q is not RFC 3339: %v", generated, err)
	}

	auth := postureObject(t, body, "auth")
	if auth["tls_enabled"] != true || auth["mtls_enabled"] != true || auth["admin_client_cert_required"] != true {
		t.Errorf("auth = %v, want every listener setting reported true", auth)
	}
	audit := postureObject(t, body, "audit")
	if audit["enabled"] != true || audit["retain_duration"] != "2160h0m0s" || audit["archive_enabled"] != true {
		t.Errorf("audit = %v, want the configured retention", audit)
	}
	// The exporter is nil in this env, so metrics read as off.
	if body["metrics_enabled"] != false {
		t.Errorf("metrics_enabled = %v, want false with no exporter attached", body["metrics_enabled"])
	}

	kek := postureObject(t, body, "kek")
	if kek["active_id"] != "kek-test" {
		t.Errorf("kek.active_id = %v, want kek-test", kek["active_id"])
	}
	if age, _ := kek["age_seconds"].(float64); age < (47 * time.Hour).Seconds() {
		t.Errorf("kek.age_seconds = %v, want roughly two days", kek["age_seconds"])
	}
	if generations, _ := kek["generations"].(float64); generations != 1 {
		t.Errorf("kek.generations = %v, want 1", kek["generations"])
	}

	certs := postureItems(t, body, "identity_certs_expiring")
	if len(certs) != 2 {
		t.Fatalf("identity certs = %v, want two", certs)
	}
	first := certs[0].(map[string]any)
	if first["identity"] != "svc-sooner" {
		t.Errorf("identity_certs_expiring[0].identity = %v, want the soonest (svc-sooner)", first["identity"])
	}
	if first["env"] != "prod" || first["app"] != "payments" {
		t.Errorf("identity_certs_expiring[0] namespace = %v/%v, want prod/payments", first["env"], first["app"])
	}
	if serial, _ := first["serial"].(string); serial == "" {
		t.Error("identity_certs_expiring[0].serial is empty")
	}
	if total, _ := postureObject(t, body, "identity_certs_expiring")["total"].(float64); total != 2 {
		t.Errorf("identity_certs_expiring.total = %v, want 2", total)
	}
	if postureObject(t, body, "identity_certs_expiring")["truncated"] != false {
		t.Error("identity_certs_expiring reports truncation with two rows")
	}

	versions := postureItems(t, body, "secret_versions_expiring")
	if len(versions) != 2 {
		t.Fatalf("secret versions = %v, want two (the third has no expiry)", versions)
	}
	if key := versions[0].(map[string]any)["key"]; key != "stripe/api-key" {
		t.Errorf("secret_versions_expiring[0].key = %v, want the soonest (stripe/api-key)", key)
	}
	if version, _ := versions[0].(map[string]any)["version"].(float64); version != 1 {
		t.Errorf("secret_versions_expiring[0].version = %v, want 1", version)
	}
	if total, _ := postureObject(t, body, "secret_versions_expiring")["total"].(float64); total != 2 {
		t.Errorf("secret_versions_expiring.total = %v, want 2", total)
	}

	// The seeded admin is token-only, so it is exactly the posture's "lacking".
	adminCerts := postureObject(t, body, "admin_certs")
	lacking, ok := adminCerts["lacking"].([]any)
	if !ok || len(lacking) != 1 || lacking[0] != "admin" {
		t.Errorf("admin_certs.lacking = %v, want [admin]", adminCerts["lacking"])
	}

	changelog := postureObject(t, body, "changelog")
	if rows, _ := changelog["rows"].(float64); rows == 0 {
		t.Errorf("changelog = %v, want the seeded writes counted", changelog)
	}
}

// TestPostureWindowExcludesLaterExpiries: the window is a filter, not a label —
// narrowing it drops the rows outside it from both the list and the total.
func TestPostureWindowExcludesLaterExpiries(t *testing.T) {
	e := newPostureEnv(t)
	seedPostureFixtures(t, e)

	body := e.posture("?cert_window=2d&secret_window=24h")
	certs := postureItems(t, body, "identity_certs_expiring")
	if len(certs) != 1 || certs[0].(map[string]any)["identity"] != "svc-sooner" {
		t.Errorf("identity certs within 2d = %v, want only svc-sooner", certs)
	}
	versions := postureItems(t, body, "secret_versions_expiring")
	if len(versions) != 1 || versions[0].(map[string]any)["key"] != "stripe/api-key" {
		t.Errorf("secret versions within 24h = %v, want only stripe/api-key", versions)
	}
	if total, _ := postureObject(t, body, "secret_versions_expiring")["total"].(float64); total != 1 {
		t.Errorf("secret_versions_expiring.total = %v, want 1", total)
	}
}

// TestPostureRevealsNoMaterial is the endpoint's whole premise: it describes
// credentials without ever handing one over. The response is checked as raw
// bytes, so a leak through any nesting or field name is caught.
func TestPostureRevealsNoMaterial(t *testing.T) {
	e := newPostureEnv(t)
	material := seedPostureFixtures(t, e)

	w := e.admin(http.MethodGet, "/api/v1/posture?cert_window=365d&secret_window=365d", nil)
	mustStatus(t, w, http.StatusOK)
	raw := w.Body.String()

	for _, secret := range material {
		if secret == "" {
			continue
		}
		if strings.Contains(raw, secret) {
			t.Errorf("posture response contains credential material %q", truncateForLog(secret))
		}
	}
	// The admin's own bearer token is in scope for the same reason.
	if strings.Contains(raw, e.adminToken) || strings.Contains(raw, e.clientToken) {
		t.Error("posture response contains a bearer token")
	}
	for _, marker := range []string{
		"BEGIN CERTIFICATE", "BEGIN PRIVATE KEY", "PRIVATE KEY", "cert_pem", "key_pem",
		"token", "hash", "ciphertext", "value", "fingerprint", "dek",
	} {
		if strings.Contains(strings.ToLower(raw), strings.ToLower(marker)) {
			t.Errorf("posture response mentions %q; the snapshot is metadata only", marker)
		}
	}
}

// TestPostureAcceptsGetOnly guards the route registration: a write verb on a
// read-only snapshot is a 405, not a silent 200.
func TestPostureAcceptsGetOnly(t *testing.T) {
	e := newPostureEnv(t)
	mustStatus(t, e.admin(http.MethodPost, "/api/v1/posture", map[string]any{}), http.StatusMethodNotAllowed)
}

func truncateForLog(s string) string {
	if len(s) <= 24 {
		return s
	}
	return s[:24] + "…"
}
