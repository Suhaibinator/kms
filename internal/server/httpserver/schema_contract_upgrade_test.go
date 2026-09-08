package httpserver

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestSchemaContractHTTPDrivesSecretOnlyUpgrade(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("dev")
	e.ship("dev", "rate_limits", "7", false)
	ctx, pr := context.Background(), consoleAdmin()
	sourceTrack := domain.ReleaseTrack{Namespace: domain.NamespaceRef{Env: "dev", App: "gradethis"}, Name: "runtime", SchemaVersion: 1}
	source, err := e.svc.GetActiveConfigurationRelease(ctx, pr, sourceTrack)
	if err != nil {
		t.Fatal(err)
	}

	// The parameter schema stays identical; only the secret wire alias changes.
	var schema map[string]any
	if err := json.Unmarshal([]byte(consoleSchemaJSON), &schema); err != nil {
		t.Fatal(err)
	}
	schema["x-kms-contract"] = []domain.ApplicationContractField{
		{Alias: "database", Kind: "parameter", ContentType: "json"},
		{Alias: "new_password", Kind: "secret"},
		{Alias: "rate_limits", Kind: "parameter", ContentType: "integer"},
	}
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	w := e.admin(http.MethodPost, "/api/v1/configuration-schemas", map[string]any{
		"application": "gradethis", "schema_json": string(schemaJSON),
	})
	mustStatus(t, w, http.StatusCreated)
	registered := decodeBody(t, w)["schema"].(map[string]any)
	contract, ok := registered["contract"].([]any)
	if !ok || len(contract) != 3 {
		t.Fatalf("registered schema omitted its established destination contract: %v", registered)
	}
	w = e.admin(http.MethodGet, "/api/v1/configuration-schemas?application=gradethis&release_name=runtime", nil)
	mustStatus(t, w, http.StatusOK)
	listed := decodeBody(t, w)["schemas"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(listed["contract"], contract) {
		t.Fatalf("registry contract differs from registration: %v", listed)
	}

	// Feed the returned contract straight into the same preview/execute API as
	// the console, mapping the renamed alias to the original immutable pin.
	body := map[string]any{
		"environment": "dev", "source_schema_version": 1, "schema_version": registered["version"],
		"contract": contract,
		"changes":  []map[string]any{{"alias": "new_password", "from_alias": "db_password"}},
	}
	w = e.admin(http.MethodPost, migrationHTTPPath, body)
	mustStatus(t, w, http.StatusOK)
	preview := decodeBody(t, w)
	if preview["valid"] != true || preview["plan_digest"] == "" {
		t.Fatalf("destination contract could not preview: %v", preview)
	}
	body["execute"], body["plan_digest"] = true, preview["plan_digest"]
	mustStatus(t, e.admin(http.MethodPost, migrationHTTPPath, body), http.StatusOK)

	targetTrack := sourceTrack
	targetTrack.SchemaVersion = uint64(registered["version"].(float64))
	target, err := e.svc.GetActiveConfigurationRelease(ctx, pr, targetTrack)
	if err != nil || target.Release.Version != 1 || target.ActivationRevision <= source.ActivationRevision {
		t.Fatalf("destination activation: %+v, error: %v", target, err)
	}
	if len(target.Release.Entries) != len(source.Release.Entries) {
		t.Fatalf("destination entries: %+v", target.Release.Entries)
	}
	for _, entry := range target.Release.Entries {
		alias := entry.Alias
		if alias == "db_password" {
			t.Fatal("destination retained obsolete secret alias")
		}
		if alias == "new_password" {
			alias = "db_password"
		}
		matched := false
		for _, old := range source.Release.Entries {
			if old.Alias == alias {
				matched = entry.Ref == old.Ref && entry.Version == old.Version
			}
		}
		if !matched {
			t.Fatalf("destination changed immutable resource pin: %+v", entry)
		}
	}
	// Audit JSON is consumed directly by the console; schema/revision metadata
	// must remain decimal strings and distinguish the two release-1 tracks.
	w = e.admin(http.MethodGet, "/api/v1/audit?event_type=configuration_release.activate", nil)
	mustStatus(t, w, http.StatusOK)
	seenTracks := map[string]string{}
	for _, raw := range decodeBody(t, w)["events"].([]any) {
		event := raw.(map[string]any)
		if event["resource_env"] != "dev" || event["resource_app"] != "gradethis" {
			continue
		}
		if event["resource_version"] != float64(1) {
			t.Fatalf("expected colliding release versions: %v", event)
		}
		var metadata map[string]string
		if err := json.Unmarshal([]byte(event["metadata_json"].(string)), &metadata); err != nil {
			t.Fatal(err)
		}
		seenTracks[metadata["schema_version"]] = metadata["activation_revision"]
	}
	if seenTracks["1"] != strconv.FormatUint(source.ActivationRevision, 10) || seenTracks[strconv.FormatUint(targetTrack.SchemaVersion, 10)] != strconv.FormatUint(target.ActivationRevision, 10) {
		t.Fatalf("HTTP audit lost track identity: %v", seenTracks)
	}
	sourceAfter, err := e.svc.GetActiveConfigurationRelease(ctx, pr, sourceTrack)
	if err != nil || !reflect.DeepEqual(sourceAfter, source) {
		t.Fatalf("source activation changed: before=%+v after=%+v error=%v", source, sourceAfter, err)
	}
}
