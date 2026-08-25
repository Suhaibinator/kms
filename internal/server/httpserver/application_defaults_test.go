package httpserver

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

func httpDefaultsArtifact(t *testing.T) []byte {
	t.Helper()
	artifact, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev",
		SchemaSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(consoleSchemaJSON))),
		Contract: []configstore.ContractEntry{
			{Alias: "database", Kind: configstore.ContractKindParameter, ContentType: "json"},
			{Alias: "db_password", Kind: configstore.ContractKindSecret},
			{Alias: "rate_limits", Kind: configstore.ContractKindParameter, ContentType: "integer"},
		},
		Parameters: []configstore.DefaultsParameter{
			{Alias: "database", ContentType: "json", Value: `{"host":"db.internal","pool":8}`},
			{Alias: "rate_limits", ContentType: "integer", Value: "7"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func rawDefaultsRequest(e *testEnv, token, target string, artifact []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(artifact))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)
	return w
}

func TestApplicationDefaultsHTTPPreviewExecuteAndAuthorization(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("dev")
	artifact := httpDefaultsArtifact(t)
	preview := rawDefaultsRequest(e, e.adminToken, "/api/v1/applications/defaults?env=dev&app=gradethis&overwrite=true", artifact)
	mustStatus(t, preview, http.StatusOK)
	body := decodeBody(t, preview)
	if body["executed"] != false || body["plan_digest"] == "" || len(body["entries"].([]any)) != 2 {
		t.Fatalf("defaults preview = %v", body)
	}
	for _, raw := range body["entries"].([]any) {
		entry := raw.(map[string]any)
		if entry["alias"] == "rate_limits" && entry["status"] != "update" {
			t.Fatalf("rate_limits preview = %v", entry)
		}
		if _, leaked := entry["value"]; leaked {
			t.Fatalf("defaults response exposed a value field: %v", entry)
		}
	}
	digest := body["plan_digest"].(string)
	executed := rawDefaultsRequest(e, e.adminToken, "/api/v1/applications/defaults?env=dev&app=gradethis&overwrite=true&execute=true&plan_digest="+digest, artifact)
	mustStatus(t, executed, http.StatusOK)
	if got := decodeBody(t, executed); got["executed"] != true {
		t.Fatalf("defaults execute = %v", got)
	}

	authEnv := newTestEnv(t)
	denied := rawDefaultsRequest(authEnv, authEnv.clientToken, "/api/v1/applications/defaults?env=dev&app=gradethis", artifact)
	mustStatus(t, denied, http.StatusForbidden)
	malformed := rawDefaultsRequest(e, e.adminToken, "/api/v1/applications/defaults?env=dev&app=gradethis", []byte("{\"value\":\"do-not-echo\"}"))
	mustStatus(t, malformed, http.StatusBadRequest)
	if bytes.Contains(malformed.Body.Bytes(), []byte("do-not-echo")) {
		t.Fatalf("malformed response leaked artifact bytes: %s", malformed.Body.String())
	}
	badFlag := rawDefaultsRequest(e, e.adminToken, "/api/v1/applications/defaults?env=dev&app=gradethis&execute=yes", artifact)
	mustStatus(t, badFlag, http.StatusBadRequest)
}
