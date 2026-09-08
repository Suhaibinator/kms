package httpserver

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

const migrationHTTPPath = "/api/v1/applications/gradethis/schema-migration"

func migrationHTTPBody(t *testing.T, e *testEnv) map[string]any {
	t.Helper()
	w := e.admin(http.MethodPost, "/api/v1/configuration-schemas", map[string]any{
		"application": "gradethis", "schema_json": strings.Replace(consoleSchemaJSON, `"minimum":0`, `"minimum":1`, 1),
	})
	mustStatus(t, w, http.StatusCreated)
	version := decodeBody(t, w)["schema"].(map[string]any)["version"]
	return map[string]any{
		"environment": "dev", "source_schema_version": 1, "schema_version": version,
		"contract": []map[string]any{
			{"alias": "database", "kind": "parameter", "content_type": "json"},
			{"alias": "rate_limits", "kind": "parameter", "content_type": "integer"},
			{"alias": "db_password", "kind": "secret"},
		},
	}
}

func TestApplicationMigrationHTTPPreservesPinsAndActivates(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("dev", "prod")
	e.ship("dev", "rate_limits", "7", false)
	e.ship("prod", "rate_limits", "9", false)
	body := migrationHTTPBody(t, e)
	ctx, pr := context.Background(), consoleAdmin()
	ns := domain.NamespaceRef{App: "gradethis", Env: "dev"}
	source, err := e.svc.GetActiveConfigurationRelease(ctx, pr, domain.ReleaseTrack{Namespace: ns, Name: "runtime", SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	e.putParam("dev", "rate_limits", "99", "integer")
	w := e.admin(http.MethodPost, migrationHTTPPath, body)
	mustStatus(t, w, http.StatusOK)
	preview := decodeBody(t, w)
	if preview["valid"] != true || preview["executed"] != false || preview["plan_digest"] == "" || preview["definition_changed"] != false {
		t.Fatalf("unexpected preview: %v", preview)
	}
	if len(preview["affected_environments"].([]any)) != 1 {
		t.Fatalf("expected prod impact: %v", preview)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("hunter2")) || bytes.Contains(w.Body.Bytes(), []byte("db.internal")) {
		t.Fatal("preview exposed resource values")
	}
	body["execute"], body["plan_digest"] = true, preview["plan_digest"]
	w = e.admin(http.MethodPost, migrationHTTPPath, body)
	mustStatus(t, w, http.StatusOK)
	result := decodeBody(t, w)
	if result["executed"] != true || result["release"] == nil || result["activation"] == nil {
		t.Fatalf("unexpected apply: %v", result)
	}
	active, err := e.svc.GetActiveConfigurationRelease(ctx, pr, domain.ReleaseTrack{Namespace: ns, Name: "runtime", SchemaVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	if active.Release.Version == source.Release.Version || active.Release.SchemaVersion == source.Release.SchemaVersion {
		t.Fatal("migration did not activate a new schema/release")
	}
	sourceAfter, err := e.svc.GetActiveConfigurationRelease(ctx, pr, domain.ReleaseTrack{Namespace: ns, Name: "runtime", SchemaVersion: 1})
	if err != nil || sourceAfter.Release.Version != source.Release.Version || sourceAfter.ActivationRevision != source.ActivationRevision {
		t.Fatalf("migration changed source activation: before=%+v after=%+v err=%v", source, sourceAfter, err)
	}
	for _, entry := range active.Release.Entries {
		for _, old := range source.Release.Entries {
			if entry.Alias == old.Alias && (entry.Version != old.Version || entry.Ref != old.Ref) {
				t.Fatalf("migration changed preserved pin %s", entry.Alias)
			}
		}
	}
	// The same reviewed request cannot create a second release.
	w = e.admin(http.MethodPost, migrationHTTPPath, body)
	mustStatus(t, w, http.StatusConflict)
	// A subsequent environment can migrate to the now-current app schema.
	body["environment"], body["execute"], body["plan_digest"] = "prod", false, ""
	w = e.admin(http.MethodPost, migrationHTTPPath, body)
	mustStatus(t, w, http.StatusOK)
	next := decodeBody(t, w)
	if next["valid"] != true || next["definition_changed"] != false {
		t.Fatalf("follow-up preview: %v", next)
	}
	body["execute"], body["plan_digest"] = true, next["plan_digest"]
	mustStatus(t, e.admin(http.MethodPost, migrationHTTPPath, body), http.StatusOK)
}

func TestApplicationMigrationHTTPValidationAndConflict(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("dev")
	e.ship("dev", "rate_limits", "7", false)
	body := migrationHTTPBody(t, e)
	body["changes"] = []map[string]any{{"alias": "rate_limits", "value": "0"}}
	w := e.admin(http.MethodPost, migrationHTTPPath, body)
	mustStatus(t, w, http.StatusOK)
	preview := decodeBody(t, w)
	if preview["valid"] != false || len(preview["validation"].([]any)) == 0 {
		t.Fatalf("invalid candidate accepted: %v", preview)
	}
	delete(body, "changes")
	w = e.admin(http.MethodPost, migrationHTTPPath, body)
	mustStatus(t, w, http.StatusOK)
	preview = decodeBody(t, w)
	body["execute"] = true
	w = e.admin(http.MethodPost, migrationHTTPPath, body)
	mustStatus(t, w, http.StatusBadRequest)
	if code := decodeBody(t, w)["error"].(map[string]any)["code"]; code != "invalid_argument" {
		t.Fatalf("missing plan digest returned %v", code)
	}
	delete(body, "execute")
	e.ship("dev", "rate_limits", "8", false)
	body["expected_source_version"] = preview["source_version"]
	body["expected_source_activation_revision"] = preview["source_activation_revision"]
	mustStatus(t, e.admin(http.MethodPost, migrationHTTPPath, body), http.StatusConflict)
	delete(body, "expected_source_version")
	delete(body, "expected_source_activation_revision")
	body["execute"], body["plan_digest"] = true, preview["plan_digest"]
	mustStatus(t, e.admin(http.MethodPost, migrationHTTPPath, body), http.StatusConflict)
}

func TestApplicationMigrationHTTPInputAndAuthorization(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("dev")
	body := migrationHTTPBody(t, e)
	// An existing namespace without an active release uses setup, not migration.
	mustStatus(t, e.admin(http.MethodPost, migrationHTTPPath, body), http.StatusPreconditionFailed)
	mustStatus(t, e.do(http.MethodPost, migrationHTTPPath, body, nil), http.StatusUnauthorized)
	authEnv := newTestEnv(t)
	w := rawDefaultsRequest(authEnv, authEnv.clientToken, migrationHTTPPath, []byte(`{"environment":"dev","schema_version":2}`))
	mustStatus(t, w, http.StatusForbidden)
	for _, raw := range []string{
		`{"environment":"dev","schema_version":-1}`,
		`{"environment":"dev","unknown":"do-not-echo"}`,
		`{"environment":"dev","changes":[{"alias":"db_password","secret_value":"do-not-echo"}]}`,
	} {
		w = rawDefaultsRequest(e, e.adminToken, migrationHTTPPath, []byte(raw))
		mustStatus(t, w, http.StatusBadRequest)
		if bytes.Contains(w.Body.Bytes(), []byte("do-not-echo")) {
			t.Fatal("invalid request contents reflected")
		}
	}
	mustStatus(t, e.admin(http.MethodGet, migrationHTTPPath, nil), http.StatusMethodNotAllowed)
}

func TestApplicationMigrationHTTPStandardAuditEvents(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("dev")
	e.ship("dev", "rate_limits", "7", false)
	body := migrationHTTPBody(t, e)
	body["changes"] = []map[string]any{{"alias": "rate_limits", "value": "11"}}
	w := e.admin(http.MethodPost, migrationHTTPPath, body)
	mustStatus(t, w, http.StatusOK)
	body["execute"], body["plan_digest"] = true, decodeBody(t, w)["plan_digest"]
	w = e.do(http.MethodPost, migrationHTTPPath, body, map[string]string{
		"Authorization": "Bearer " + e.adminToken,
		"User-Agent":    "migration-audit-test",
	})
	mustStatus(t, w, http.StatusOK)
	requestID := w.Header().Get("X-Request-ID")
	if requestID == "" {
		t.Fatal("migration response missing request ID")
	}
	for _, eventType := range []string{"parameter.write", "configuration_release.create", "configuration_release.activate", "application.release.migrate"} {
		w = e.admin(http.MethodGet, "/api/v1/audit?env=dev&app=gradethis&event_type="+eventType, nil)
		mustStatus(t, w, http.StatusOK)
		found := false
		for _, raw := range decodeBody(t, w)["events"].([]any) {
			event := raw.(map[string]any)
			if event["request_id"] != requestID {
				continue
			}
			found = true
			if event["source_ip"] == "" || event["user_agent"] != "migration-audit-test" || event["resource_version"].(float64) == 0 {
				t.Fatalf("migration audit context missing: %v", event)
			}
		}
		if !found {
			t.Fatalf("no correlated %s event for migration", eventType)
		}
	}
}
