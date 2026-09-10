package httpserver

import (
	"context"
	"net/http"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestReleasePinHTTPRequiresExactGuardAndAllowsDisconnectedUnpin(t *testing.T) {
	e := newReleaseTestEnv(t)
	e.seedConsoleApp("dev")
	e.ship("dev", "rate_limits", "7", false)
	ctx := context.Background()
	ref := domain.ReleaseSessionRef{Track: domain.ReleaseTrack{Namespace: domain.NamespaceRef{Env: "dev", App: "gradethis"}, Name: "runtime", SchemaVersion: 1}, Identity: "admin", ClientName: "api", InstanceID: "stable", SessionID: "process"}
	if err := e.svc.RegisterReleaseSession(ctx, consoleAdmin(), ref, false); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ConnectReleaseSession(ctx, ref, "c", true); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"namespace": map[string]string{"env": "dev", "app": "gradethis"}, "name": "runtime", "schema_version": 1, "identity": "admin", "client_name": "api", "instance_id": "stable", "session_id": "process", "version": 1}
	mustStatus(t, e.admin(http.MethodPost, "/api/v1/release-subscribers/pin", body), http.StatusBadRequest)
	body["expected_pin_revision"] = 0
	mustStatus(t, e.admin(http.MethodPost, "/api/v1/release-subscribers/pin", body), http.StatusOK)
	target, err := e.svc.GetInstanceRelease(ctx, consoleAdmin(), ref)
	if err != nil || !target.Pinned || target.Release.Version != 1 {
		t.Fatalf("assignment not durable: %+v %v", target, err)
	}
	mustStatus(t, e.admin(http.MethodPost, "/api/v1/release-subscribers/pin", body), http.StatusConflict)
	if err := e.svc.ConnectReleaseSession(ctx, ref, "c", false); err != nil {
		t.Fatal(err)
	}
	body["expected_pin_revision"] = target.PinRevision
	mustStatus(t, e.admin(http.MethodPost, "/api/v1/release-subscribers/pin", body), http.StatusPreconditionFailed)
	body["version"] = 0
	mustStatus(t, e.admin(http.MethodPost, "/api/v1/release-subscribers/pin", body), http.StatusOK)
}
