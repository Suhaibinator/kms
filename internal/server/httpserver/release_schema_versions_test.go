package httpserver

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

func TestReleaseSchemaVersionDiscoveryForPolicyGrantedClient(t *testing.T) {
	e := newReleaseTestEnv(t)
	for _, pair := range [][2]string{{"dev", "discovery"}, {"prod", "discovery"}, {"dev", "other"}, {"dev", "schema-free"}} {
		e.createNS(pair[0], pair[1], "token")
	}
	for _, app := range []string{"discovery", "other"} {
		for _, schema := range []string{`{"type":"object"}`, `{"type":"object","description":"inactive newest"}`} {
			mustStatus(t, e.admin(http.MethodPost, "/api/v1/configuration-schemas", map[string]any{"application": app, "schema_json": schema}), http.StatusCreated)
		}
	}
	w := e.admin(http.MethodPost, "/api/v1/identities", map[string]any{"name": "discovery-reader", "kind": "client", "auth_methods": []string{"token"}})
	mustStatus(t, w, http.StatusOK)
	headers := map[string]string{"Authorization": "Bearer " + decodeBody(t, w)["token"].(string)}
	endpoint := "/api/v1/releases/schema-versions?env=dev&app=discovery"
	mustStatus(t, e.do(http.MethodGet, endpoint, nil, headers), http.StatusForbidden)
	mustStatus(t, e.admin(http.MethodPost, "/api/v1/policies", map[string]any{"policy": map[string]any{"name": "discovery-list", "subject": "discovery-reader", "allow": []map[string]any{{"operation": "configuration-release:list", "env": "dev", "app": "discovery"}}, "deny": []any{}}}), http.StatusOK)
	for _, name := range []string{"", "runtime"} {
		w = e.do(http.MethodGet, endpoint+"&name="+name+"&page_size=1", nil, headers)
		mustStatus(t, w, http.StatusOK)
		body := decodeBody(t, w)
		if len(body) != 2 || !reflect.DeepEqual(body["schema_versions"], []any{float64(2)}) {
			t.Fatalf("numeric-only discovery: %v", body)
		}
		token := body["next_page_token"].(string)
		if token == "" {
			t.Fatal("missing next page")
		}
		w = e.do(http.MethodGet, endpoint+"&name="+name+"&page_size=1&page_token="+url.QueryEscape(token), nil, headers)
		mustStatus(t, w, http.StatusOK)
		body = decodeBody(t, w)
		if !reflect.DeepEqual(body["schema_versions"], []any{float64(1)}) || body["next_page_token"] != "" {
			t.Fatalf("second page: %v", body)
		}
	}
	for _, path := range []string{"/api/v1/configuration-schemas?application=discovery", "/api/v1/releases/schema-versions?env=prod&app=discovery", "/api/v1/releases/schema-versions?env=dev&app=other"} {
		mustStatus(t, e.do(http.MethodGet, path, nil, headers), http.StatusForbidden)
	}
	w = e.do(http.MethodGet, endpoint+"&name=unknown", nil, headers)
	mustStatus(t, w, http.StatusOK)
	if !reflect.DeepEqual(decodeBody(t, w)["schema_versions"], []any{}) {
		t.Fatalf("unknown name: %s", w.Body.String())
	}
	w = e.admin(http.MethodGet, "/api/v1/releases/schema-versions?env=dev&app=schema-free", nil)
	mustStatus(t, w, http.StatusOK)
	if !reflect.DeepEqual(decodeBody(t, w)["schema_versions"], []any{}) {
		t.Fatalf("schema zero was registered: %s", w.Body.String())
	}
	for _, path := range []string{"/api/v1/releases/schema-versions?app=discovery", endpoint + "&name=../bad", endpoint + "&page_token=invalid"} {
		mustStatus(t, e.do(http.MethodGet, path, nil, headers), http.StatusBadRequest)
	}
	mustStatus(t, e.admin(http.MethodPatch, "/api/v1/namespaces", map[string]any{"env": "dev", "app": "discovery", "allowed_auth_methods": []string{"mtls"}}), http.StatusOK)
	mustStatus(t, e.do(http.MethodGet, endpoint, nil, headers), http.StatusForbidden)
}
