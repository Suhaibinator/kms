package httpserver

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

// Console fixtures (plan §3.3): deterministic backend responses the frontend
// asserts its TypeScript types against. Run
//
//	go test ./internal/core ./internal/server/httpserver -run TestConsoleFixtures -update
//
// to regenerate (readiness-cases.json comes from internal/core); without
// -update the tests fail on any byte-level drift.
var updateFixtures = flag.Bool("update", false, "rewrite frontend/tests/fixtures/backend/*.json")

const fixtureDir = "../../../frontend/tests/fixtures/backend"

// fixtureTime replaces every *_unix_ms value: storage stamps wall-clock
// times, and fixtures only need the shape to stay stable.
const fixtureTime = float64(1755000000000)

func normalizeFixture(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if strings.HasSuffix(k, "_unix_ms") {
				if n, ok := val.(float64); ok && n > 0 {
					x[k] = fixtureTime
					continue
				}
			}
			x[k] = normalizeFixture(val)
		}
		return x
	case []any:
		for i := range x {
			x[i] = normalizeFixture(x[i])
		}
		return x
	default:
		return v
	}
}

func fixtureBytes(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	out, err := json.MarshalIndent(normalizeFixture(generic), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func fixtureFromResponse(t *testing.T, w *httptest.ResponseRecorder) []byte {
	t.Helper()
	mustStatus(t, w, http.StatusOK)
	var generic any
	if err := json.Unmarshal(w.Body.Bytes(), &generic); err != nil {
		t.Fatal(err)
	}
	return fixtureBytes(t, generic)
}

func checkFixture(t *testing.T, name string, data []byte) {
	t.Helper()
	path := filepath.Join(fixtureDir, name+".json")
	if *updateFixtures {
		if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture %s is missing; run with -update: %v", name, err)
	}
	if !bytes.Equal(existing, data) {
		t.Fatalf("fixture %s is stale; run `go test ./internal/core ./internal/server/httpserver -run TestConsoleFixtures -update`", name)
	}
}

// newFixtureEnv is a release test env whose server advertises TLS and a gRPC
// address, so fixtures show the healthy listener shape.
func newFixtureEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newReleaseTestEnv(t)
	srv, err := New(e.svc, Config{Addr: ":0", Version: "test-version", GRPCAddr: "127.0.0.1:8443", TLSEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	e.handler = srv.Handler
	return e
}

func TestConsoleFixtures(t *testing.T) {
	fixtures := map[string][]byte{}
	capture := func(name string, w *httptest.ResponseRecorder) {
		t.Helper()
		fixtures[name] = fixtureFromResponse(t, w)
	}

	// A healthy application: both environments shipped and fully applied.
	{
		e := newFixtureEnv(t)
		e.seedConsoleApp("dev", "prod")
		for _, env := range []string{"dev", "prod"} {
			if r := e.ship(env, "rate_limits", "7", false); r["status"] != "activated" {
				t.Fatalf("%s ship = %v", env, r)
			}
		}
		e.ackInstance("dev", "dev-1", domain.ReleaseStateApplied, "")
		e.ackInstance("prod", "prod-1", domain.ReleaseStateApplied, "")
		e.ackInstance("prod", "prod-2", domain.ReleaseStateApplied, "")
		capture("overview-ready", e.admin(http.MethodGet, "/api/v1/applications/overview?name=gradethis", nil))
		fixtures["ship-preview"] = fixtureFromResponse(t, e.admin(http.MethodPost, "/api/v1/applications/ship", map[string]any{
			"application": "gradethis", "environment": "prod", "dry_run": true, "expected_active_version": 1,
			"changes": []map[string]any{{"alias": "rate_limits", "value": "12"}},
		}))
	}

	// An incident: prod has an unreleased change and one instance rejecting
	// the active release; a second application is in setup and a third ready.
	{
		e := newFixtureEnv(t)
		e.seedConsoleApp("dev", "prod")
		if r := e.ship("dev", "rate_limits", "7", false); r["status"] != "activated" {
			t.Fatalf("dev ship = %v", r)
		}
		if r := e.ship("prod", "rate_limits", "7", false); r["status"] != "activated" {
			t.Fatalf("prod ship v1 = %v", r)
		}
		if r := e.ship("prod", "rate_limits", "12", false); r["status"] != "activated" {
			t.Fatalf("prod ship v2 = %v", r)
		}
		e.putParam("prod", "rate_limits", "20", "integer")
		e.ackInstance("prod", "prod-1", domain.ReleaseStateApplied, "")
		e.ackInstance("prod", "prod-2", domain.ReleaseStateApplied, "")
		e.ackInstance("prod", "prod-3", domain.ReleaseStateRejected, domain.ReleaseRejectConfigValidationFailed)
		e.ackInstance("dev", "dev-1", domain.ReleaseStateApplied, "")
		w := e.admin(http.MethodPost, "/api/v1/applications", map[string]any{
			"name": "billing", "description": "Billing worker", "release_name": "runtime",
			"contract": []map[string]any{{"alias": "queue", "kind": "parameter", "content_type": "string"}},
		})
		mustStatus(t, w, http.StatusOK)
		e.createNS("dev", "billing", "token")
		w = e.admin(http.MethodPost, "/api/v1/applications", map[string]any{
			"name": "reports", "description": "Nightly reports", "release_name": "runtime",
			"contract": []map[string]any{{"alias": "bucket", "kind": "parameter", "content_type": "string"}},
		})
		mustStatus(t, w, http.StatusOK)
		e.createNS("dev", "reports", "token")
		w = e.admin(http.MethodPut, "/api/v1/parameters", map[string]any{"env": "dev", "app": "reports", "key": "bucket", "value": "reports-nightly", "content_type": "string", "metadata_json": "{}"})
		mustStatus(t, w, http.StatusOK)
		w = e.admin(http.MethodPost, "/api/v1/applications/ship", map[string]any{"application": "reports", "environment": "dev", "changes": []map[string]any{{"alias": "bucket", "value": "reports-nightly-v2"}}})
		mustStatus(t, w, http.StatusOK)
		capture("overview-incident", e.admin(http.MethodGet, "/api/v1/applications/overview?name=gradethis", nil))
		fixtures["fleet"] = fixtureFromResponse(t, e.admin(http.MethodGet, "/api/v1/applications/overview", nil))
	}

	// A freshly created application: contract only, no schema, no environments.
	{
		e := newFixtureEnv(t)
		w := e.admin(http.MethodPost, "/api/v1/applications", map[string]any{
			"name": "gradethis", "description": "Grading API", "release_name": "runtime",
			"contract": []map[string]any{
				{"alias": "database", "kind": "parameter", "content_type": "json"},
				{"alias": "rate_limits", "kind": "parameter", "content_type": "integer"},
				{"alias": "db_password", "kind": "secret"},
			},
		})
		mustStatus(t, w, http.StatusOK)
		capture("overview-setup", e.admin(http.MethodGet, "/api/v1/applications/overview?name=gradethis", nil))
	}

	// The conflict outcome needs a race between the CAS pre-check and
	// activation, so its shape is rendered from the domain result directly.
	fixtures["ship-conflict"] = fixtureBytes(t, toShipResultDTO(domain.ShipResult{
		Status: domain.ShipStatusConflict,
		Preview: domain.ShipPreview{BaseVersion: 7, ReleaseName: "runtime", SchemaID: "runtime", SchemaVersion: 1, Entries: []domain.ShipPreviewEntry{
			{Alias: "database", Kind: domain.ReleaseEntryParameter, Key: "database", FromVersion: 3, ToVersion: 3, Change: domain.ShipEntryIncluded},
			{Alias: "db_password", Kind: domain.ReleaseEntrySecret, Key: "db_password", FromVersion: 2, ToVersion: 2, Change: domain.ShipEntryIncluded},
			{Alias: "rate_limits", Kind: domain.ReleaseEntryParameter, Key: "rate_limits", FromVersion: 10, ToVersion: 11, Change: domain.ShipEntryEdited},
		}, Validation: []domain.ReleaseValidationError{}, Warnings: []domain.Finding{}},
		Parameters: []domain.ShipParameterWrite{{Alias: "rate_limits", Key: "rate_limits", Version: 11, Revision: 52}},
		Release:    &domain.ConfigurationRelease{Name: "runtime", Version: 9, Digest: "3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		Error:      &domain.ShipError{Code: "aborted", Message: "the active release changed while shipping; the new release was created but not activated", CurrentVersion: 8},
	}))

	// readiness-cases.json is produced by TestConsoleFixturesReadiness in
	// internal/core, next to the pure functions it exercises.

	for name, data := range fixtures {
		if strings.Contains(string(data), "hunter2") {
			t.Fatalf("fixture %s leaks a secret value", name)
		}
		checkFixture(t, name, data)
	}
}
