package httpserver

import (
	"net/http"
	"testing"
)

func TestHTTPParameterCreateOnly(t *testing.T) {
	e := newPostureEnv(t)
	e.createNS("prod", "app", "token")
	body := map[string]any{"env": "prod", "app": "app", "key": "config", "value": "original", "content_type": "string", "metadata_json": `{"owner":"original"}`, "create_only": true}
	w := e.admin(http.MethodPut, "/api/v1/parameters", body)
	mustStatus(t, w, http.StatusOK)
	body["value"] = "replacement"
	body["metadata_json"] = `{"owner":"replacement"}`
	w = e.admin(http.MethodPut, "/api/v1/parameters", body)
	mustStatus(t, w, http.StatusConflict)
	if errCode(t, w) != "already_exists" {
		t.Fatalf("code = %s", errCode(t, w))
	}
	w = e.admin(http.MethodGet, "/api/v1/parameters/get?env=prod&app=app&key=config", nil)
	mustStatus(t, w, http.StatusOK)
	param := decodeBody(t, w)["parameter"].(map[string]any)
	if param["value"] != "original" || param["version"] != float64(1) || param["metadata_json"] != `{"owner":"original"}` {
		t.Fatalf("parameter = %+v", param)
	}
	delete(body, "create_only")
	w = e.admin(http.MethodPut, "/api/v1/parameters", body)
	mustStatus(t, w, http.StatusOK)
	if decodeBody(t, w)["version"] != float64(2) {
		t.Fatal("ordinary put did not append version")
	}
	w = e.admin(http.MethodGet, "/api/v1/namespaces", nil)
	mustStatus(t, w, http.StatusOK)
	ns := decodeBody(t, w)["namespaces"].([]any)[0].(map[string]any)
	if ns["identity_count"] != float64(0) {
		t.Fatalf("identity_count = %v", ns["identity_count"])
	}
}
