package httpserver

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

// createNS registers a namespace via the admin API with the given auth methods.
func (e *testEnv) createNS(env, app string, methods ...string) {
	e.t.Helper()
	body := map[string]any{"env": env, "app": app, "description": env + "/" + app}
	if methods != nil {
		body["allowed_auth_methods"] = methods
	}
	w := e.admin(http.MethodPost, "/api/v1/namespaces", body)
	mustStatus(e.t, w, http.StatusOK)
}

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

func TestWhoAmI(t *testing.T) {
	e := newTestEnv(t)
	w := e.admin(http.MethodGet, "/api/v1/whoami", nil)
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	if body["name"] != "admin" || body["kind"] != "admin" {
		t.Fatalf("whoami = %v", body)
	}
	if body["auth_method"] != "token" {
		t.Fatalf("auth_method = %v", body["auth_method"])
	}
	if body["namespace"] != nil {
		t.Fatalf("expected null namespace for unbound admin, got %v", body["namespace"])
	}

	// whoami requires authentication.
	w = e.do(http.MethodGet, "/api/v1/whoami", nil, nil)
	mustStatus(t, w, http.StatusUnauthorized)
}

func TestCANoAuth(t *testing.T) {
	e := newTestEnv(t)
	// No Authorization header: the CA cert is public.
	w := e.do(http.MethodGet, "/api/v1/ca", nil, nil)
	mustStatus(t, w, http.StatusOK)
	pem, _ := decodeBody(t, w)["cert_pem"].(string)
	if !strings.Contains(pem, "BEGIN CERTIFICATE") {
		t.Fatalf("cert_pem = %q", pem)
	}
}

func TestAuthMiddleware(t *testing.T) {
	e := newTestEnv(t)

	w := e.do(http.MethodGet, "/api/v1/namespaces", nil, nil)
	mustStatus(t, w, http.StatusUnauthorized)

	w = e.do(http.MethodGet, "/api/v1/namespaces", nil, map[string]string{"Authorization": "Bearer nope"})
	mustStatus(t, w, http.StatusUnauthorized)

	w = e.admin(http.MethodGet, "/api/v1/namespaces", nil)
	mustStatus(t, w, http.StatusOK)
}

func TestReadinessGating(t *testing.T) {
	e := newTestEnvWith(t, false) // keyring not attached

	w := e.admin(http.MethodGet, "/api/v1/namespaces", nil)
	mustStatus(t, w, http.StatusServiceUnavailable)
	if errCode(t, w) != "unavailable" {
		t.Fatalf("code = %s", errCode(t, w))
	}

	w = e.do(http.MethodGet, "/api/v1/health", nil, nil)
	mustStatus(t, w, http.StatusOK)
	if decodeBody(t, w)["ready"] != false {
		t.Fatalf("expected ready=false")
	}
	w = e.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"token": e.adminToken}, nil)
	mustStatus(t, w, http.StatusOK)
}

func TestNamespacesCRUD(t *testing.T) {
	e := newTestEnv(t)

	// Create (default auth methods -> mtls).
	w := e.admin(http.MethodPost, "/api/v1/namespaces", map[string]any{
		"env": "prod", "app": "gradethis", "description": "backend",
	})
	mustStatus(t, w, http.StatusOK)
	ns, _ := decodeBody(t, w)["namespace"].(map[string]any)
	methods, _ := ns["allowed_auth_methods"].([]any)
	if len(methods) != 1 || methods[0] != "mtls" {
		t.Fatalf("default allowed_auth_methods = %v", ns["allowed_auth_methods"])
	}

	// List reports counts.
	w = e.admin(http.MethodGet, "/api/v1/namespaces", nil)
	mustStatus(t, w, http.StatusOK)
	list, _ := decodeBody(t, w)["namespaces"].([]any)
	if len(list) != 1 {
		t.Fatalf("namespaces = %v", list)
	}
	item := list[0].(map[string]any)
	if item["parameter_count"].(float64) != 0 || item["secret_count"].(float64) != 0 {
		t.Fatalf("counts = %v", item)
	}

	// Update (PATCH) description + auth methods (full replace).
	w = e.admin(http.MethodPatch, "/api/v1/namespaces", map[string]any{
		"env": "prod", "app": "gradethis", "description": "updated",
		"allowed_auth_methods": []string{"mtls", "token"},
	})
	mustStatus(t, w, http.StatusOK)
	ns = decodeBody(t, w)["namespace"].(map[string]any)
	if ns["description"] != "updated" || len(ns["allowed_auth_methods"].([]any)) != 2 {
		t.Fatalf("updated ns = %v", ns)
	}

	// Delete empty namespace succeeds.
	w = e.admin(http.MethodDelete, "/api/v1/namespaces?env=prod&app=gradethis", nil)
	mustStatus(t, w, http.StatusOK)
	w = e.admin(http.MethodGet, "/api/v1/namespaces", nil)
	if len(decodeBody(t, w)["namespaces"].([]any)) != 0 {
		t.Fatalf("namespace not deleted")
	}
}

func TestNamespaceDeleteNonEmpty(t *testing.T) {
	e := newTestEnv(t)
	e.createNS("prod", "gradethis")
	w := e.admin(http.MethodPut, "/api/v1/parameters", map[string]any{
		"env": "prod", "app": "gradethis", "key": "rate-limit", "value": "100", "content_type": "integer",
	})
	mustStatus(t, w, http.StatusOK)

	// Non-empty namespace delete -> 412 failed_precondition.
	w = e.admin(http.MethodDelete, "/api/v1/namespaces?env=prod&app=gradethis", nil)
	mustStatus(t, w, http.StatusPreconditionFailed)
	if errCode(t, w) != "failed_precondition" {
		t.Fatalf("code = %s", errCode(t, w))
	}
}

func TestParametersLifecycle(t *testing.T) {
	e := newTestEnv(t)
	e.createNS("prod", "gradethis")

	w := e.admin(http.MethodPut, "/api/v1/parameters", map[string]any{
		"env": "prod", "app": "gradethis", "key": "rate-limit", "value": "100", "content_type": "integer",
	})
	mustStatus(t, w, http.StatusOK)
	if decodeBody(t, w)["version"].(float64) != 1 {
		t.Fatalf("version != 1")
	}

	// Second put bumps version.
	w = e.admin(http.MethodPut, "/api/v1/parameters", map[string]any{
		"env": "prod", "app": "gradethis", "key": "rate-limit", "value": "200", "content_type": "integer",
	})
	mustStatus(t, w, http.StatusOK)
	if decodeBody(t, w)["version"].(float64) != 2 {
		t.Fatalf("version != 2")
	}

	// Get current.
	w = e.admin(http.MethodGet, "/api/v1/parameters/get?env=prod&app=gradethis&key=rate-limit", nil)
	mustStatus(t, w, http.StatusOK)
	p, _ := decodeBody(t, w)["parameter"].(map[string]any)
	if p["value"] != "200" || p["version"].(float64) != 2 || p["env"] != "prod" || p["app"] != "gradethis" || p["key"] != "rate-limit" {
		t.Fatalf("param = %v", p)
	}

	// Get specific version.
	w = e.admin(http.MethodGet, "/api/v1/parameters/get?env=prod&app=gradethis&key=rate-limit&version=1", nil)
	if decodeBody(t, w)["parameter"].(map[string]any)["value"] != "100" {
		t.Fatalf("v1 value mismatch")
	}

	// Metadata.
	w = e.admin(http.MethodGet, "/api/v1/parameters/metadata?env=prod&app=gradethis&key=rate-limit", nil)
	mustStatus(t, w, http.StatusOK)
	md := decodeBody(t, w)
	if md["key"] != "rate-limit" || len(md["versions"].([]any)) != 2 {
		t.Fatalf("metadata = %v", md)
	}

	// List (namespace-scoped).
	w = e.admin(http.MethodGet, "/api/v1/parameters?env=prod&app=gradethis", nil)
	mustStatus(t, w, http.StatusOK)
	if len(decodeBody(t, w)["parameters"].([]any)) != 1 {
		t.Fatalf("list len")
	}

	// Delete.
	w = e.admin(http.MethodDelete, "/api/v1/parameters?env=prod&app=gradethis&key=rate-limit", nil)
	mustStatus(t, w, http.StatusOK)

	// Get after delete -> 404.
	w = e.admin(http.MethodGet, "/api/v1/parameters/get?env=prod&app=gradethis&key=rate-limit", nil)
	mustStatus(t, w, http.StatusNotFound)
	if errCode(t, w) != "not_found" {
		t.Fatalf("code = %s", errCode(t, w))
	}
}

func TestParameterInvalidArg(t *testing.T) {
	e := newTestEnv(t)
	// Missing env/app is an invalid argument (bad namespace).
	w := e.admin(http.MethodGet, "/api/v1/parameters/get?key=rate-limit", nil)
	mustStatus(t, w, http.StatusBadRequest)
	if errCode(t, w) != "invalid_argument" {
		t.Fatalf("code = %s", errCode(t, w))
	}
}

func TestParameterMissingNamespace(t *testing.T) {
	e := newTestEnv(t)
	// Writing into a namespace that does not exist -> 404.
	w := e.admin(http.MethodPut, "/api/v1/parameters", map[string]any{
		"env": "prod", "app": "ghost", "key": "k", "value": "1", "content_type": "integer",
	})
	mustStatus(t, w, http.StatusNotFound)
}

func TestConfigurationReleaseHTTPLifecycle(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.createNS("prod", "app")

	w := e.admin(http.MethodPut, "/api/v1/parameters", map[string]any{
		"env": "prod", "app": "app", "key": "config/runtime", "value": `{"enabled":true}`, "content_type": "json",
	})
	mustStatus(t, w, http.StatusOK)

	w = e.admin(http.MethodPost, "/api/v1/configuration-schemas", map[string]any{
		"id": "runtime", "schema_json": `{"type":"object","properties":{"settings":{"type":"object"}},"required":["settings"]}`,
	})
	mustStatus(t, w, http.StatusCreated)
	schema := decodeBody(t, w)["schema"].(map[string]any)

	w = e.admin(http.MethodPost, "/api/v1/releases", map[string]any{
		"namespace": map[string]any{"env": "prod", "app": "app"},
		"name":      "runtime", "schema_id": "runtime", "schema_version": schema["version"],
		"entries": []map[string]any{{
			"alias": "settings", "kind": "parameter",
			"ref":   map[string]any{"namespace": map[string]any{"env": "prod", "app": "app"}, "key": "config/runtime"},
			"label": "current",
		}},
	})
	mustStatus(t, w, http.StatusCreated)
	release := decodeBody(t, w)["release"].(map[string]any)
	if release["digest"] == "" || release["version"].(float64) != 1 {
		t.Fatalf("release = %v", release)
	}
	entries := release["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["parameter_digest"] == "" {
		t.Fatalf("release entries = %v", entries)
	}

	w = e.admin(http.MethodPost, "/api/v1/releases/validate", map[string]any{
		"namespace": map[string]any{"env": "prod", "app": "app"}, "name": "runtime", "version": 1,
	})
	mustStatus(t, w, http.StatusOK)
	if decodeBody(t, w)["valid"] != true {
		t.Fatal("release should validate")
	}

	w = e.admin(http.MethodPost, "/api/v1/releases/activate", map[string]any{
		"namespace": map[string]any{"env": "prod", "app": "app"}, "name": "runtime", "version": 1,
		"expected_current_version": 0,
	})
	mustStatus(t, w, http.StatusOK)
	activation := decodeBody(t, w)
	if activation["changed"] != true || activation["activation_revision"].(float64) == 0 {
		t.Fatalf("activation = %v", activation)
	}

	w = e.admin(http.MethodGet, "/api/v1/releases/active?env=prod&app=app&name=runtime", nil)
	mustStatus(t, w, http.StatusOK)
	if decodeBody(t, w)["release"].(map[string]any)["version"].(float64) != 1 {
		t.Fatal("active release version mismatch")
	}

	// Presence-aware CAS distinguishes an omitted guard from expect-no-active.
	w = e.admin(http.MethodPost, "/api/v1/releases/activate", map[string]any{
		"namespace": map[string]any{"env": "prod", "app": "app"}, "name": "runtime", "version": 1,
		"expected_current_version": 0,
	})
	mustStatus(t, w, http.StatusConflict)
	if errCode(t, w) != "aborted" {
		t.Fatalf("code = %s", errCode(t, w))
	}
}

func TestSecretsLifecycle(t *testing.T) {
	e := newTestEnv(t)
	e.createNS("prod", "gradethis")
	secretValue := []byte("s3cr3t-value")
	b64 := base64.StdEncoding.EncodeToString(secretValue)

	w := e.admin(http.MethodPost, "/api/v1/secrets", map[string]any{
		"env": "prod", "app": "gradethis", "key": "stripe-api-key",
		"value_base64": b64, "content_type": "text/plain", "generate_access_token": true,
	})
	mustStatus(t, w, http.StatusOK)
	if tok := decodeBody(t, w)["access_token"]; tok == nil || tok == "" {
		t.Fatalf("expected access_token")
	}

	// Metadata (no value).
	w = e.admin(http.MethodGet, "/api/v1/secrets/metadata?env=prod&app=gradethis&key=stripe-api-key", nil)
	mustStatus(t, w, http.StatusOK)
	sec, _ := decodeBody(t, w)["secret"].(map[string]any)
	if sec["has_access_token"] != true || sec["key"] != "stripe-api-key" {
		t.Fatalf("secret meta = %v", sec)
	}
	if _, leaked := sec["value"]; leaked {
		t.Fatalf("metadata leaked a value field")
	}

	// List.
	w = e.admin(http.MethodGet, "/api/v1/secrets?env=prod&app=gradethis", nil)
	mustStatus(t, w, http.StatusOK)
	if len(decodeBody(t, w)["secrets"].([]any)) != 1 {
		t.Fatalf("secrets list len")
	}

	// Reveal round-trips the plaintext.
	w = e.admin(http.MethodPost, "/api/v1/secrets/reveal", map[string]any{"env": "prod", "app": "gradethis", "key": "stripe-api-key"})
	mustStatus(t, w, http.StatusOK)
	rev := decodeBody(t, w)
	if rev["key"] != "stripe-api-key" {
		t.Fatalf("reveal ref = %v", rev)
	}
	got, err := base64.StdEncoding.DecodeString(rev["value_base64"].(string))
	if err != nil || string(got) != string(secretValue) {
		t.Fatalf("reveal = %q (%v)", got, err)
	}

	// New version, then promote back to v1.
	w = e.admin(http.MethodPost, "/api/v1/secrets", map[string]any{
		"env": "prod", "app": "gradethis", "key": "stripe-api-key",
		"value_base64": base64.StdEncoding.EncodeToString([]byte("v2")),
	})
	mustStatus(t, w, http.StatusOK)
	w = e.admin(http.MethodPost, "/api/v1/secrets/promote", map[string]any{"env": "prod", "app": "gradethis", "key": "stripe-api-key", "version": 1})
	mustStatus(t, w, http.StatusOK)
	pr := decodeBody(t, w)
	if pr["current_version"].(float64) != 1 || pr["previous_version"].(float64) != 2 {
		t.Fatalf("promote = %v", pr)
	}

	// Disable then reveal -> 412.
	w = e.admin(http.MethodPost, "/api/v1/secrets/disable", map[string]any{"env": "prod", "app": "gradethis", "key": "stripe-api-key", "version": 1})
	mustStatus(t, w, http.StatusOK)
	w = e.admin(http.MethodPost, "/api/v1/secrets/reveal", map[string]any{"env": "prod", "app": "gradethis", "key": "stripe-api-key", "version": 1})
	mustStatus(t, w, http.StatusPreconditionFailed)

	// Destroy v2 then reveal -> 412.
	w = e.admin(http.MethodPost, "/api/v1/secrets/destroy", map[string]any{"env": "prod", "app": "gradethis", "key": "stripe-api-key", "version": 2})
	mustStatus(t, w, http.StatusOK)
	w = e.admin(http.MethodPost, "/api/v1/secrets/reveal", map[string]any{"env": "prod", "app": "gradethis", "key": "stripe-api-key", "version": 2})
	mustStatus(t, w, http.StatusPreconditionFailed)

	// Delete.
	w = e.admin(http.MethodDelete, "/api/v1/secrets?env=prod&app=gradethis&key=stripe-api-key", nil)
	mustStatus(t, w, http.StatusOK)
}

func TestClientBoundRevealRejected(t *testing.T) {
	e := newTestEnv(t)
	e.createNS("prod", "gradethis")
	w := e.admin(http.MethodPost, "/api/v1/secrets", map[string]any{
		"env": "prod", "app": "gradethis", "key": "bound",
		"value_base64": base64.StdEncoding.EncodeToString([]byte("v")),
		"client_bound": true, "generate_access_token": true,
	})
	mustStatus(t, w, http.StatusOK)

	// Client-bound secrets have no reveal flow (412).
	w = e.admin(http.MethodPost, "/api/v1/secrets/reveal", map[string]any{"env": "prod", "app": "gradethis", "key": "bound"})
	mustStatus(t, w, http.StatusPreconditionFailed)
	if errCode(t, w) != "failed_precondition" {
		t.Fatalf("code = %s", errCode(t, w))
	}
}

func TestSecretInvalidBase64(t *testing.T) {
	e := newTestEnv(t)
	e.createNS("prod", "gradethis")
	w := e.admin(http.MethodPost, "/api/v1/secrets", map[string]any{
		"env": "prod", "app": "gradethis", "key": "x", "value_base64": "!!!not base64!!!",
	})
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
			"allow": []map[string]any{{"operation": "secret:read", "env": "prod", "app": "gradethis"}},
		},
	}
	w := e.admin(http.MethodPost, "/api/v1/policies", policy)
	mustStatus(t, w, http.StatusOK)
	created, _ := decodeBody(t, w)["policy"].(map[string]any)
	if created["name"] != "reader" {
		t.Fatalf("policy = %v", created)
	}
	rule := created["allow"].([]any)[0].(map[string]any)
	if rule["operation"] != "secret:read" || rule["env"] != "prod" || rule["app"] != "gradethis" {
		t.Fatalf("rule = %v", rule)
	}
	if _, ok := rule["key"]; ok {
		t.Fatalf("policy rule unexpectedly carries a key field: %v", rule)
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
	e.createNS("prod", "gradethis", "mtls", "token")

	// Create a client identity with both credentials.
	w := e.admin(http.MethodPost, "/api/v1/identities", map[string]any{
		"name": "svc-a", "kind": "client",
		"namespace":        map[string]any{"env": "prod", "app": "gradethis"},
		"auth_methods":     []string{"token", "mtls"},
		"cert_ttl_seconds": 3600,
	})
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	if tok := body["token"]; tok == nil || tok == "" {
		t.Fatalf("expected token, got %v", body)
	}
	cert, _ := body["cert"].(map[string]any)
	if cert == nil || !strings.Contains(cert["cert_pem"].(string), "BEGIN CERTIFICATE") || !strings.Contains(cert["key_pem"].(string), "BEGIN") {
		t.Fatalf("cert bundle = %v", cert)
	}
	id, _ := body["identity"].(map[string]any)
	nsRef, _ := id["namespace"].(map[string]any)
	if nsRef == nil || nsRef["env"] != "prod" || nsRef["app"] != "gradethis" {
		t.Fatalf("identity namespace = %v", id["namespace"])
	}
	if id["has_token"] != true {
		t.Fatalf("has_token = %v", id["has_token"])
	}

	// Rotate the bearer token.
	w = e.admin(http.MethodPost, "/api/v1/identities/rotate", map[string]any{"name": "svc-a"})
	mustStatus(t, w, http.StatusOK)
	if decodeBody(t, w)["token"] == "" {
		t.Fatalf("rotate returned empty token")
	}

	// Issue an additional certificate, then revoke it by serial.
	w = e.admin(http.MethodPost, "/api/v1/identities/issue-cert", map[string]any{"name": "svc-a", "ttl_seconds": 7200})
	mustStatus(t, w, http.StatusOK)
	issued, _ := decodeBody(t, w)["cert"].(map[string]any)
	serial, _ := issued["serial"].(string)
	if serial == "" {
		t.Fatalf("issue-cert serial empty: %v", issued)
	}
	w = e.admin(http.MethodPost, "/api/v1/identities/revoke-cert", map[string]any{"name": "svc-a", "serial": serial})
	mustStatus(t, w, http.StatusOK)

	// Identity view now shows certs with the revoked one carrying revoked_at.
	w = e.admin(http.MethodGet, "/api/v1/identities", nil)
	mustStatus(t, w, http.StatusOK)
	var found map[string]any
	for _, raw := range decodeBody(t, w)["identities"].([]any) {
		m := raw.(map[string]any)
		if m["name"] == "svc-a" {
			found = m
		}
	}
	if found == nil {
		t.Fatalf("svc-a not listed")
	}
	var sawRevoked bool
	for _, raw := range found["certs"].([]any) {
		c := raw.(map[string]any)
		if c["serial"] == serial && c["revoked_at_unix_ms"].(float64) > 0 {
			sawRevoked = true
		}
	}
	if !sawRevoked {
		t.Fatalf("expected revoked cert in %v", found["certs"])
	}

	// Revoke the whole identity.
	w = e.admin(http.MethodPost, "/api/v1/identities/revoke", map[string]any{"name": "svc-a"})
	mustStatus(t, w, http.StatusOK)
}

func TestIdentityMTLSOnlyHasNoToken(t *testing.T) {
	e := newTestEnv(t)
	w := e.admin(http.MethodPost, "/api/v1/identities", map[string]any{
		"name": "cert-only", "kind": "client", "namespace": nil, "auth_methods": []string{"mtls"},
	})
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	if _, hasToken := body["token"]; hasToken {
		t.Fatalf("mtls-only identity should not receive a token: %v", body)
	}
	if body["cert"] == nil {
		t.Fatalf("expected a cert bundle")
	}
	// Rotate on a cert-only identity -> 412 (no token to rotate).
	w = e.admin(http.MethodPost, "/api/v1/identities/rotate", map[string]any{"name": "cert-only"})
	mustStatus(t, w, http.StatusPreconditionFailed)
}

func TestAuditAndKeys(t *testing.T) {
	e := newTestEnv(t)
	e.createNS("prod", "gradethis")
	e.admin(http.MethodPut, "/api/v1/parameters", map[string]any{
		"env": "prod", "app": "gradethis", "key": "x", "value": "1", "content_type": "integer",
	})

	// Filter by namespace + event type.
	w := e.admin(http.MethodGet, "/api/v1/audit?env=prod&app=gradethis&event_type=parameter.write", nil)
	mustStatus(t, w, http.StatusOK)
	events, _ := decodeBody(t, w)["events"].([]any)
	if len(events) == 0 {
		t.Fatalf("expected audit events")
	}
	ev := events[0].(map[string]any)
	if ev["resource_env"] != "prod" || ev["resource_app"] != "gradethis" || ev["resource_key"] != "x" {
		t.Fatalf("audit event ref = %v", ev)
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

func TestPartialAuditFilterAppliesNamespaceMethodEligibility(t *testing.T) {
	e := newTestEnv(t)
	e.createNS("stage", "token", "token")
	e.createNS("stage", "mtls")

	policy := map[string]any{"policy": map[string]any{
		"name": "audit-stage", "subject": "client",
		"allow": []map[string]any{{"operation": "admin:audit:read", "env": "stage", "app": "*"}},
	}}
	w := e.admin(http.MethodPost, "/api/v1/policies", policy)
	mustStatus(t, w, http.StatusOK)

	for _, app := range []string{"token", "mtls"} {
		w = e.admin(http.MethodPut, "/api/v1/parameters", map[string]any{
			"env": "stage", "app": app, "key": "x", "value": "1", "content_type": "integer",
		})
		mustStatus(t, w, http.StatusOK)
	}

	// The partial env filter remains authorized, but each returned row must also
	// admit the token method used by the delegated caller.
	w = e.client(http.MethodGet, "/api/v1/audit?env=stage&event_type=parameter.write", nil)
	mustStatus(t, w, http.StatusOK)
	events, _ := decodeBody(t, w)["events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["resource_app"] != "token" {
		t.Fatalf("partial audit events = %v, want only stage/token", events)
	}
}

func TestSubscribers(t *testing.T) {
	e := newTestEnv(t)
	e.createNS("prod", "gradethis")
	e.admin(http.MethodPut, "/api/v1/parameters", map[string]any{
		"env": "prod", "app": "gradethis", "key": "x", "value": "1", "content_type": "integer",
	})
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

// TestNamespaceMethodGate exercises the per-namespace auth-method gate for a
// token-authenticated client bound to an mTLS-only namespace: it is denied until
// the namespace is updated to admit tokens.
func TestNamespaceMethodGate(t *testing.T) {
	e := newTestEnv(t)
	e.createNS("stage", "svc") // default: mtls only

	// Mint a token identity bound to the mtls-only namespace.
	w := e.admin(http.MethodPost, "/api/v1/identities", map[string]any{
		"name": "gated", "kind": "client",
		"namespace":    map[string]any{"env": "stage", "app": "svc"},
		"auth_methods": []string{"token"},
	})
	mustStatus(t, w, http.StatusOK)
	gatedToken, _ := decodeBody(t, w)["token"].(string)
	gatedHdr := map[string]string{"Authorization": "Bearer " + gatedToken}

	// Namespace enumeration is a multi-namespace read: it succeeds but omits
	// metadata for namespaces that do not admit the caller's method.
	w = e.do(http.MethodGet, "/api/v1/namespaces", nil, gatedHdr)
	mustStatus(t, w, http.StatusOK)
	if got := decodeBody(t, w)["namespaces"].([]any); len(got) != 0 {
		t.Fatalf("token caller listed mTLS-only namespace: %v", got)
	}

	// Token method not admitted -> 403 with a method-gate message.
	w = e.do(http.MethodGet, "/api/v1/parameters?env=stage&app=svc", nil, gatedHdr)
	mustStatus(t, w, http.StatusForbidden)
	if errCode(t, w) != "permission_denied" {
		t.Fatalf("code = %s", errCode(t, w))
	}

	// Admit tokens; the home-namespace grant now covers the list.
	w = e.admin(http.MethodPatch, "/api/v1/namespaces", map[string]any{
		"env": "stage", "app": "svc", "description": "", "allowed_auth_methods": []string{"mtls", "token"},
	})
	mustStatus(t, w, http.StatusOK)

	w = e.do(http.MethodGet, "/api/v1/parameters?env=stage&app=svc", nil, gatedHdr)
	mustStatus(t, w, http.StatusOK)
	w = e.do(http.MethodGet, "/api/v1/namespaces", nil, gatedHdr)
	mustStatus(t, w, http.StatusOK)
	if got := decodeBody(t, w)["namespaces"].([]any); len(got) != 1 {
		t.Fatalf("token-admitting namespace list = %v, want one namespace", got)
	}
}

func TestApplicationDashboardHTTPWorkflow(t *testing.T) {
	e := newReleaseTestEnv(t)
	w := e.admin(http.MethodPost, "/api/v1/applications", map[string]any{
		"name": "payments-api", "description": "Payments", "release_name": "runtime",
		"schema_id": "", "schema_version": 0, "contract": []any{},
	})
	mustStatus(t, w, http.StatusOK)
	for _, env := range []string{"dev", "prod-gcp"} {
		w = e.admin(http.MethodPost, "/api/v1/namespaces", map[string]any{
			"env": env, "app": "payments-api", "description": "", "allowed_auth_methods": []string{"mtls"},
		})
		mustStatus(t, w, http.StatusOK)
	}
	w = e.admin(http.MethodPut, "/api/v1/applications/parameters", map[string]any{
		"application": "payments-api", "key": "rate-limit", "value": "100",
		"content_type": "integer", "metadata_json": "{}", "environments": []string{"dev", "prod-gcp"},
	})
	mustStatus(t, w, http.StatusOK)
	results, ok := decodeBody(t, w)["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("bulk results = %s", w.Body.String())
	}
	w = e.admin(http.MethodGet, "/api/v1/applications/dashboard?name=payments-api", nil)
	mustStatus(t, w, http.StatusOK)
	body := decodeBody(t, w)
	rows, ok := body["rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("dashboard = %s", w.Body.String())
	}
	row := rows[0].(map[string]any)
	cells := row["environments"].(map[string]any)
	if cells["dev"].(map[string]any)["value"] != "100" || cells["prod-gcp"].(map[string]any)["value"] != "100" {
		t.Fatalf("dashboard cells = %s", w.Body.String())
	}
}

func TestMethodNotAllowed(t *testing.T) {
	e := newTestEnv(t)
	// POST is not registered on /parameters (PUT is).
	w := e.admin(http.MethodPost, "/api/v1/parameters", nil)
	mustStatus(t, w, http.StatusMethodNotAllowed)
}
