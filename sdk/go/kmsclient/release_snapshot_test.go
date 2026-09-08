package kmsclient

import (
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestReleaseFormattingIncludesSchemaIdentityWithoutValues(t *testing.T) {
	const metadataCanary = "metadata-canary"
	manifest := ReleaseManifest{
		namespace: "prod/app", name: "runtime", version: 1, activationRevision: 2,
		schemaVersion: 0, metadataJSON: metadataCanary,
	}
	for _, got := range []string{manifest.String(), fmt.Sprintf("%+v", manifest), fmt.Sprintf("%#v", manifest)} {
		if !strings.Contains(got, "ReleaseManifest{prod/app/runtime") ||
			!strings.Contains(got, "schema_version=0") || strings.Contains(got, metadataCanary) {
			t.Fatalf("manifest formatting lost identity or exposed metadata: %q", got)
		}
	}

	const secretCanary = "snapshot-secret-canary"
	const parameterCanary = "snapshot-parameter-canary"
	snapshot := ReleaseSnapshot{
		namespace: "prod/app", name: "runtime", version: 3, activationRevision: 4,
		schemaVersion: 17, metadataJSON: metadataCanary,
		parameters: map[string]ReleaseParameter{"runtime": {value: parameterCanary}},
		secrets:    map[string]Secret{"database": NewSecret([]byte(secretCanary))},
	}
	for _, got := range []string{snapshot.String(), fmt.Sprintf("%+v", snapshot), fmt.Sprintf("%#v", snapshot)} {
		if !strings.Contains(got, "ReleaseSnapshot{prod/app/runtime") ||
			!strings.Contains(got, "schema_version=17") ||
			strings.Contains(got, metadataCanary) || strings.Contains(got, secretCanary) || strings.Contains(got, parameterCanary) {
			t.Fatalf("snapshot formatting lost identity or exposed values: %q", got)
		}
	}
}

func TestReleaseSnapshotJSONPathsExcludeResolvedValues(t *testing.T) {
	const secretCanary = "snapshot-secret-canary"
	const parameterCanary = "snapshot-parameter-canary"
	snapshot := ReleaseSnapshot{
		namespace: "prod/app",
		name:      "runtime",
		version:   7,
		entries: map[string]ReleaseEntryMetadata{
			"database": {Alias: "database", Kind: "secret", Path: "/prod/app/database", Version: 2},
		},
		parameters: map[string]ReleaseParameter{"runtime": {value: parameterCanary}},
		secrets:    map[string]Secret{"database": NewSecret([]byte(secretCanary))},
	}

	streamed, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	buffered, err := snapshot.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	for label, encoded := range map[string][]byte{"streaming": streamed, "buffered": buffered} {
		if strings.Contains(string(encoded), secretCanary) || strings.Contains(string(encoded), parameterCanary) {
			t.Fatalf("%s JSON leaked resolved values: %s", label, encoded)
		}
		if !strings.Contains(string(encoded), `"namespace":"prod/app"`) {
			t.Fatalf("%s JSON omitted safe identity: %s", label, encoded)
		}
		if strings.Contains(string(encoded), "client_bound") || strings.Contains(string(encoded), "has_access_token") {
			t.Fatalf("%s JSON retained removed release protection flags: %s", label, encoded)
		}
	}
}

func TestReleaseLoaderStatusJSONFormatsDurationAsString(t *testing.T) {
	status := ReleaseLoaderStatus{State: "ready", LastResolutionDuration: 1500 * time.Millisecond}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"LastResolutionDuration":"1.5s"`) {
		t.Fatalf("duration was not encoded as a human-readable string: %s", encoded)
	}
	buffered, err := status.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if string(buffered) != string(encoded) {
		t.Fatalf("MarshalJSON = %s, streaming marshal = %s", buffered, encoded)
	}
}

func TestReleaseSnapshotNilEntriesUseEmptyObject(t *testing.T) {
	encoded, err := json.Marshal(ReleaseSnapshot{})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"entries":{}`) || strings.Contains(string(encoded), `"entries":null`) {
		t.Fatalf("nil entries did not use native v2 empty-object encoding: %s", encoded)
	}
}

func BenchmarkReleaseSnapshotMarshalJSONV2(b *testing.B) {
	snapshot := ReleaseSnapshot{
		namespace: "prod/app",
		name:      "runtime",
		version:   7,
		entries: map[string]ReleaseEntryMetadata{
			"runtime":  {Alias: "runtime", Kind: "parameter", Path: "/prod/app/runtime", Version: 11},
			"database": {Alias: "database", Kind: "secret", Path: "/prod/app/database", Version: 2},
		},
		parameters: map[string]ReleaseParameter{"runtime": {value: `{"limit":20}`}},
		secrets:    map[string]Secret{"database": NewSecret([]byte("benchmark-secret"))},
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := json.Marshal(snapshot); err != nil {
			b.Fatal(err)
		}
	}
}
