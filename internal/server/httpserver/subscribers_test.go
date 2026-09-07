package httpserver

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestSubscriberJSONPreservesReleaseLifecycle(t *testing.T) {
	row := toSubscriberDTO(domain.Subscriber{
		ClientName: "api", InstanceID: "replica-1", Namespaces: []domain.NamespaceRef{{Env: "prod", App: "app"}},
		ConnectedAt: time.Unix(100, 0), ReleaseName: "runtime", ReleaseState: domain.ReleaseStateApplied,
		ReleaseVersion: 2, ReleaseRevision: 7,
	})
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["release_name"] != "runtime" || got["release_state"] != "applied" || got["release_version"] != float64(2) || got["release_revision"] != float64(7) || got["last_acked_revision"] != float64(0) || got["last_heartbeat_unix_ms"] != float64(0) {
		t.Fatalf("release subscriber JSON = %s", data)
	}
}
