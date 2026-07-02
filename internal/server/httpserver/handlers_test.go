package httpserver

import (
	"encoding/base64"
	"net/http"
	"testing"
)

func TestLogin(t *testing.T) {
	e := newTestEnv(t)

	w := e.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"token": e.adminToken}, nil)
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	id, _ := body["identity"].(map[string]any)
	if id["name"] != "admin" || id["kind"] != "admin" {
		t.Fatalf("identity = %v", id)
	}

	w = e.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"token": "wrong"}, nil)
	mustStatus(t, w, http.StatusUnauthorized)
	if errCode(t, w) != "unauthenticated" {
		t.Fatalf("code = %s", errCode(t, w))
	}
}

func TestHealthNoAuth(t *testing.T) {
	e := newTestEnv(t)
	w := e.do(http.MethodGet, "/api/v1/health", nil, nil)
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	if body["healthy"] != true || body["ready"] != true {
		t.Fatalf("health body = %v", body)
	}
	if body["version"] != "test-version" {
		t.Fatalf("version = %v", body["version"])
	}
}

func TestAuthMiddleware(t *testing.T) {
	e := newTestEnv(t)

	// No token.
	w := e.do(http.MethodGet, "/api/v1/namespaces", nil, nil)
	mustStatus(t, w, http.StatusUnauthorized)

	// Bad token.
	w = e.do(http.MethodGet, "/api/v1/namespaces", nil, map[string]string{"Authorization": "Bearer nope"})
	mustStatus(t, w, http.StatusUnauthorized)

	// Good token.
	w = e.admin(http.MethodGet, "/api/v1/namespaces", nil)
	mustStatus(t, w, http.StatusOK)
}

func TestReadinessGating(t *testing.T) {
	e := newTestEnvWith(t, false) // keyring not attached

	// Protected route is 503 until ready.
	w := e.admin(http.MethodGet, "/api/v1/namespaces", nil)
	mustStatus(t, w, http.StatusServiceUnavailable)
	if errCode(t, w) != "unavailable" {
		t.Fatalf("code = %s", errCode(t, w))
	}

	// Health and login remain available.
	w = e.do(http.MethodGet, "/api/v1/health", nil, nil)
	mustStatus(t, w, http.StatusOK)
	if decodeBody(t, w)["ready"] != false {
		t.Fatalf("expected ready=false")
	}
	w = e.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"token": e.adminToken}, nil)
	mustStatus(t, w, http.StatusOK)
}

func TestNamespaces(t *testing.T) {
	e := newTestEnv(t)
	w := e.admin(http.MethodPost, "/api/v1/namespaces", map[string]any{"path": "/prod", "description": "prod"})
	mustStatus(t, w, http.StatusOK)

	w = e.admin(http.MethodGet, "/api/v1/namespaces", nil)
	mustStatus(t, w, http.StatusOK)
	list, _ := decodeBody(t, w)["namespaces"].([]any)
	if len(list) != 1 {
		t.Fatalf("namespaces = %v", list)
	}
}

func TestParametersLifecycle(t *testing.T) {
	e := newTestEnv(t)

	// Put.
	w := e.admin(http.MethodPut, "/api/v1/parameters", map[string]any{
		"path": "/prod/rate", "value": "100", "content_type": "integer",
	})
	mustStatus(t, w, http.StatusOK)
	if decodeBody(t, w)["version"].(float64) != 1 {
		t.Fatalf("version != 1")
	}

	// Second put bumps version.
	w = e.admin(http.MethodPut, "/api/v1/parameters", map[string]any{"path": "/prod/rate", "value": "200"})
	mustStatus(t, w, http.StatusOK)
	if decodeBody(t, w)["version"].(float64) != 2 {
		t.Fatalf("version != 2")
	}

	// Get current.
	w = e.admin(http.MethodGet, "/api/v1/parameters/get?path=/prod/rate", nil)
	mustStatus(t, w, http.StatusOK)
	p, _ := decodeBody(t, w)["parameter"].(map[string]any)
	if p["value"] != "200" || p["version"].(float64) != 2 {
		t.Fatalf("param = %v", p)
	}

	// Get specific version.
	w = e.admin(http.MethodGet, "/api/v1/parameters/get?path=/prod/rate&version=1", nil)
	p = decodeBody(t, w)["parameter"].(map[string]any)
	if p["value"] != "100" {
		t.Fatalf("v1 value = %v", p["value"])
	}

	// Metadata.
	w = e.admin(http.MethodGet, "/api/v1/parameters/metadata?path=/prod/rate", nil)
	mustStatus(t, w, http.StatusOK)
	versions, _ := decodeBody(t, w)["versions"].([]any)
	if len(versions) != 2 {
		t.Fatalf("versions = %v", versions)
	}

	// List.
	w = e.admin(http.MethodGet, "/api/v1/parameters?prefix=/prod", nil)
	mustStatus(t, w, http.StatusOK)
	if len(decodeBody(t, w)["parameters"].([]any)) != 1 {
		t.Fatalf("list len")
	}

	// Delete.
	w = e.admin(http.MethodDelete, "/api/v1/parameters?path=/prod/rate", nil)
	mustStatus(t, w, http.StatusOK)

	// Get after delete -> 404.
	w = e.admin(http.MethodGet, "/api/v1/parameters/get?path=/prod/rate", nil)
	mustStatus(t, w, http.StatusNotFound)
	if errCode(t, w) != "not_found" {
		t.Fatalf("code = %s", errCode(t, w))
	}
}

func TestParameterInvalidPath(t *testing.T) {
	e := newTestEnv(t)
	w := e.admin(http.MethodGet, "/api/v1/parameters/get?path=no-leading-slash", nil)
	mustStatus(t, w, http.StatusBadRequest)
	if errCode(t, w) != "invalid_argument" {
		t.Fatalf("code = %s", errCode(t, w))
	}
}

func TestSecretsLifecycle(t *testing.T) {
	e := newTestEnv(t)
	secretValue := []byte("s3cr3t-value")
	b64 := base64.StdEncoding.EncodeToString(secretValue)

	// Create with a generated access token.
	w := e.admin(http.MethodPost, "/api/v1/secrets", map[string]any{
		"path": "/prod/api-key", "value_base64": b64, "content_type": "text/plain",
		"generate_access_token": true,
	})
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	if body["access_token"] == nil || body["access_token"] == "" {
		t.Fatalf("expected access_token, got %v", body)
	}

	// Metadata (no value).
	w = e.admin(http.MethodGet, "/api/v1/secrets/metadata?path=/prod/api-key", nil)
	mustStatus(t, w, http.StatusOK)
	sec, _ := decodeBody(t, w)["secret"].(map[string]any)
	if sec["has_access_token"] != true {
		t.Fatalf("has_access_token = %v", sec["has_access_token"])
	}
	if _, leaked := sec["value"]; leaked {
		t.Fatalf("metadata leaked a value field")
	}

	// List.
	w = e.admin(http.MethodGet, "/api/v1/secrets?prefix=/prod", nil)
	mustStatus(t, w, http.StatusOK)
	if len(decodeBody(t, w)["secrets"].([]any)) != 1 {
		t.Fatalf("secrets list len")
	}

	// Reveal round-trips the plaintext.
	w = e.admin(http.MethodPost, "/api/v1/secrets/reveal", map[string]any{"path": "/prod/api-key"})
	mustStatus(t, w, http.StatusOK)
	rev := decodeBody(t, w)
	got, err := base64.StdEncoding.DecodeString(rev["value_base64"].(string))
	if err != nil || string(got) != string(secretValue) {
		t.Fatalf("reveal = %q (%v)", got, err)
	}

	// New version, then promote back to v1.
	w = e.admin(http.MethodPost, "/api/v1/secrets", map[string]any{
		"path": "/prod/api-key", "value_base64": base64.StdEncoding.EncodeToString([]byte("v2")),
	})
	mustStatus(t, w, http.StatusOK)
	w = e.admin(http.MethodPost, "/api/v1/secrets/promote", map[string]any{"path": "/prod/api-key", "version": 1})
	mustStatus(t, w, http.StatusOK)
	pr := decodeBody(t, w)
	if pr["current_version"].(float64) != 1 || pr["previous_version"].(float64) != 2 {
		t.Fatalf("promote = %v", pr)
	}

	// Disable then reveal -> failed_precondition (disabled).
	w = e.admin(http.MethodPost, "/api/v1/secrets/disable", map[string]any{"path": "/prod/api-key", "version": 1})
	mustStatus(t, w, http.StatusOK)
	w = e.admin(http.MethodPost, "/api/v1/secrets/reveal", map[string]any{"path": "/prod/api-key", "version": 1})
	mustStatus(t, w, http.StatusPreconditionFailed)

	// Destroy v2 then reveal it -> failed_precondition (destroyed).
	w = e.admin(http.MethodPost, "/api/v1/secrets/destroy", map[string]any{"path": "/prod/api-key", "version": 2})
	mustStatus(t, w, http.StatusOK)
	w = e.admin(http.MethodPost, "/api/v1/secrets/reveal", map[string]any{"path": "/prod/api-key", "version": 2})
	mustStatus(t, w, http.StatusPreconditionFailed)

	// Delete.
	w = e.admin(http.MethodDelete, "/api/v1/secrets?path=/prod/api-key", nil)
	mustStatus(t, w, http.StatusOK)
}

func TestClientBoundRevealRejected(t *testing.T) {
	e := newTestEnv(t)
	w := e.admin(http.MethodPost, "/api/v1/secrets", map[string]any{
		"path": "/prod/bound", "value_base64": base64.StdEncoding.EncodeToString([]byte("v")),
		"client_bound": true, "generate_access_token": true,
	})
	mustStatus(t, w, http.StatusOK)

	// Client-bound secrets have no reveal flow (412).
	w = e.admin(http.MethodPost, "/api/v1/secrets/reveal", map[string]any{"path": "/prod/bound"})
	mustStatus(t, w, http.StatusPreconditionFailed)
	if errCode(t, w) != "failed_precondition" {
		t.Fatalf("code = %s", errCode(t, w))
	}
}

func TestSecretInvalidBase64(t *testing.T) {
	e := newTestEnv(t)
	w := e.admin(http.MethodPost, "/api/v1/secrets", map[string]any{"path": "/prod/x", "value_base64": "!!!not base64!!!"})
	mustStatus(t, w, http.StatusBadRequest)
	if errCode(t, w) != "invalid_argument" {
		t.Fatalf("code = %s", errCode(t, w))
	}
}

func TestPolicies(t *testing.T) {
	e := newTestEnv(t)
	policy := map[string]any{
		"policy": map[string]any{
			"name": "reader", "subject": "client",
			"allow": []map[string]any{{"operation": "secret:read", "path": "/prod/*"}},
		},
	}
	w := e.admin(http.MethodPost, "/api/v1/policies", policy)
	mustStatus(t, w, http.StatusOK)
	created, _ := decodeBody(t, w)["policy"].(map[string]any)
	if created["name"] != "reader" {
		t.Fatalf("policy = %v", created)
	}
	if _, ok := created["created_at_unix_ms"]; !ok {
		t.Fatalf("missing created_at_unix_ms")
	}

	w = e.admin(http.MethodGet, "/api/v1/policies", nil)
	mustStatus(t, w, http.StatusOK)
	if len(decodeBody(t, w)["policies"].([]any)) != 1 {
		t.Fatalf("policies len")
	}

	// Client (non-admin) cannot list policies -> 403.
	w = e.client(http.MethodGet, "/api/v1/policies", nil)
	mustStatus(t, w, http.StatusForbidden)
	if errCode(t, w) != "permission_denied" {
		t.Fatalf("code = %s", errCode(t, w))
	}

	w = e.admin(http.MethodDelete, "/api/v1/policies?name=reader", nil)
	mustStatus(t, w, http.StatusOK)
}

func TestIdentities(t *testing.T) {
	e := newTestEnv(t)
	w := e.admin(http.MethodPost, "/api/v1/identities", map[string]any{"name": "svc-a", "kind": "client"})
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	if body["token"] == nil || body["token"] == "" {
		t.Fatalf("expected token, got %v", body)
	}

	w = e.admin(http.MethodPost, "/api/v1/identities/rotate", map[string]any{"name": "svc-a"})
	mustStatus(t, w, http.StatusOK)
	if decodeBody(t, w)["token"] == "" {
		t.Fatalf("rotate returned empty token")
	}

	w = e.admin(http.MethodPost, "/api/v1/identities/revoke", map[string]any{"name": "svc-a"})
	mustStatus(t, w, http.StatusOK)

	w = e.admin(http.MethodGet, "/api/v1/identities", nil)
	mustStatus(t, w, http.StatusOK)
	if len(decodeBody(t, w)["identities"].([]any)) < 3 { // admin, client, svc-a
		t.Fatalf("identities len")
	}
}

func TestAuditAndKeys(t *testing.T) {
	e := newTestEnv(t)
	// Generate an audit event.
	e.admin(http.MethodPut, "/api/v1/parameters", map[string]any{"path": "/prod/x", "value": "1"})

	w := e.admin(http.MethodGet, "/api/v1/audit?event_type=parameter.write", nil)
	mustStatus(t, w, http.StatusOK)
	events, _ := decodeBody(t, w)["events"].([]any)
	if len(events) == 0 {
		t.Fatalf("expected audit events")
	}

	w = e.admin(http.MethodGet, "/api/v1/keys", nil)
	mustStatus(t, w, http.StatusOK)
	keys, _ := decodeBody(t, w)["keys"].([]any)
	if len(keys) != 1 {
		t.Fatalf("keys = %v", keys)
	}
	k := keys[0].(map[string]any)
	if k["id"] != "kek-test" {
		t.Fatalf("key id = %v", k["id"])
	}
	for _, forbidden := range []string{"key_check", "kdf_salt", "material"} {
		if _, leaked := k[forbidden]; leaked {
			t.Fatalf("key metadata leaked %s", forbidden)
		}
	}
}

func TestSubscribers(t *testing.T) {
	e := newTestEnv(t)
	// Advance the revision so current_revision is meaningful.
	e.admin(http.MethodPut, "/api/v1/parameters", map[string]any{"path": "/prod/x", "value": "1"})
	w := e.admin(http.MethodGet, "/api/v1/subscribers", nil)
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	if _, ok := body["current_revision"]; !ok {
		t.Fatalf("missing current_revision")
	}
	if subs, ok := body["subscribers"].([]any); !ok || len(subs) != 0 {
		t.Fatalf("subscribers = %v (noop hub expected empty)", body["subscribers"])
	}
}

func TestMethodNotAllowed(t *testing.T) {
	e := newTestEnv(t)
	// PATCH is not registered on parameters.
	w := e.admin(http.MethodPatch, "/api/v1/parameters", nil)
	mustStatus(t, w, http.StatusMethodNotAllowed)
}
