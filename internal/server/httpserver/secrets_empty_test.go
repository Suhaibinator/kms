package httpserver

import (
	"net/http"
	"strings"
	"testing"
)

// TestEmptySecretValueOverHTTP stores zero-byte secrets through both spellings
// of an empty value (an absent and an empty value_base64), reveals them as an
// empty base64 string, and rejects an empty value under a JSON content type.
func TestEmptySecretValueOverHTTP(t *testing.T) {
	e := newTestEnv(t)
	e.createNS("prod", "gradethis")

	for key, body := range map[string]map[string]any{
		"absent-value": {"env": "prod", "app": "gradethis", "key": "absent-value"},
		"empty-value":  {"env": "prod", "app": "gradethis", "key": "empty-value", "value_base64": "", "content_type": "text/plain"},
	} {
		t.Run(key, func(t *testing.T) {
			w := e.admin(http.MethodPost, "/api/v1/secrets", body)
			mustStatus(t, w, http.StatusOK)
			if got := decodeBody(t, w)["version"]; got != float64(1) {
				t.Fatalf("version = %v, want 1", got)
			}
			w = e.admin(http.MethodPost, "/api/v1/secrets/reveal", map[string]any{"env": "prod", "app": "gradethis", "key": key})
			mustStatus(t, w, http.StatusOK)
			revealed := decodeBody(t, w)
			if value, ok := revealed["value_base64"].(string); !ok || value != "" {
				t.Fatalf("revealed value_base64 = %#v, want \"\"", revealed["value_base64"])
			}
		})
	}

	for _, contentType := range []string{"application/json", "application/json; charset=utf-8", "application/vnd.foo+json"} {
		t.Run("json/"+contentType, func(t *testing.T) {
			w := e.admin(http.MethodPost, "/api/v1/secrets", map[string]any{
				"env": "prod", "app": "gradethis", "key": "empty-json", "value_base64": "", "content_type": contentType,
			})
			mustStatus(t, w, http.StatusBadRequest)
			if got := errCode(t, w); got != "invalid_argument" {
				t.Fatalf("error code = %q, want invalid_argument", got)
			}
			if !strings.Contains(w.Body.String(), "an empty value is not valid JSON") {
				t.Fatalf("error body = %s, want the empty-JSON explanation", w.Body.String())
			}
		})
	}
}
