package grpcserver

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/internal/watch"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type delayedWatchReleaseLookup struct {
	*storage.SQLStore
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (s *delayedWatchReleaseLookup) GetConfigurationRelease(ctx context.Context, track domain.ReleaseTrack, version uint64) (domain.ConfigurationRelease, error) {
	if version == 1 {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.resume:
		case <-ctx.Done():
			return domain.ConfigurationRelease{}, ctx.Err()
		}
	}
	return s.SQLStore.GetConfigurationRelease(ctx, track, version)
}

func TestWatchReleaseRetentionLookupRace(t *testing.T) {
	for _, outcome := range []string{"retained track", "namespace deleted", "namespace recreated", "credentials revoked"} {
		t.Run(outcome, func(t *testing.T) { testWatchReleaseRetentionLookupRace(t, outcome) })
	}
}

func testWatchReleaseRetentionLookupRace(t *testing.T, outcome string) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ns := domain.NamespaceRef{Env: "prod", App: "retention-test"}
	if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIdentity(ctx, storage.CreateIdentityParams{Name: "admin", Kind: domain.IdentityKindAdmin, TokenHash: crypto.TokenHash(adminToken)}); err != nil {
		t.Fatal(err)
	}
	wrapped := &delayedWatchReleaseLookup{SQLStore: st, entered: make(chan struct{}), resume: make(chan struct{})}
	svc := core.New(wrapped, nil, "test")
	svc.SetAdminRequireClientCert(false)
	kek, err := crypto.NewKEKFromMaterial("test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc.SetKeyring(crypto.NewKeyring(kek))
	hub := watch.NewHub(st, nil, watch.Options{HeartbeatInterval: 20 * time.Millisecond, PruneInterval: time.Hour})
	svc.SetHub(hub)
	hubCtx, stopHub := context.WithCancel(ctx)
	t.Cleanup(stopHub)
	go func() { _ = hub.Run(hubCtx) }()
	<-hub.Started()
	_, lis := serveBufconn(t, svc, hub, Config{})
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	watchCtx, cancel := context.WithTimeout(adminCtx(), 5*time.Second)
	defer cancel()
	stream, err := kmsv1.NewConfigurationReleaseServiceClient(conn).WatchRelease(watchCtx)
	if err != nil {
		t.Fatal(err)
	}
	track := domain.ReleaseTrack{Namespace: ns, Name: "runtime"}
	if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{Register: &kmsv1.ReleaseWatchRegistration{Namespace: pNS(ns.Env, ns.App), Name: track.Name, SchemaVersion: new(uint64), ClientName: "client", InstanceId: "instance"}}}); err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil || first.GetHeartbeat() == nil {
		t.Fatalf("inactive known track heartbeat: %v %v", first, err)
	}
	activate := func() domain.ActiveConfigurationRelease {
		t.Helper()
		r, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ns, Name: track.Name, Digest: "empty"})
		if err != nil {
			t.Fatal(err)
		}
		a, _, err := st.ActivateConfigurationRelease(ctx, track, r.Version, nil)
		if err != nil {
			t.Fatal(err)
		}
		hub.Wake()
		return a
	}
	firstActivation := activate()
	select {
	case <-wrapped.entered:
	case <-watchCtx.Done():
		t.Fatal("live activation never reached delayed release lookup")
	}
	activate()
	activate()
	current := activate()
	if _, err := st.PruneChangeLog(ctx, time.Nanosecond, 1); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PruneConfigurationReleases(ctx, time.Nanosecond, 1); err != nil || n != 1 {
		t.Fatalf("prune oldest: n=%d err=%v", n, err)
	}
	if _, err := st.GetConfigurationRelease(ctx, track, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("old release must be pruned: %v", err)
	}
	active, err := st.GetActiveConfigurationRelease(ctx, track)
	if err != nil || active.Release.Version != current.Release.Version {
		t.Fatalf("known track current remains readable: %+v %v", active, err)
	}
	wantCode := codes.Aborted
	switch outcome {
	case "namespace deleted", "namespace recreated":
		if err := st.DeleteNamespace(ctx, ns); err != nil {
			t.Fatal(err)
		}
		if outcome == "namespace recreated" {
			if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns}); err != nil {
				t.Fatal(err)
			}
			activate()
		}
	case "credentials revoked":
		if err := st.SetIdentityDisabled(ctx, "admin", true); err != nil {
			t.Fatal(err)
		}
		wantCode = codes.Unauthenticated
	}
	close(wrapped.resume)
	for {
		_, err := stream.Recv()
		if err == nil {
			continue
		}
		if status.Code(err) != wantCode {
			t.Fatalf("stream status = %v, want %s", err, wantCode)
		}
		if recoverableHistory := strings.Contains(status.Convert(err).Message(), "delivery history changed"); recoverableHistory != (outcome == "retained track") {
			t.Fatalf("stream misclassified retention versus fencing: %v", err)
		}
		break
	}
	if outcome != "retained track" {
		return
	}

	// An SDK retries Aborted with the same numeric track and last accepted
	// cursor. Pruned history must then yield that track's current snapshot.
	reconnected, err := kmsv1.NewConfigurationReleaseServiceClient(conn).WatchRelease(watchCtx)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconnected.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{Register: &kmsv1.ReleaseWatchRegistration{
		Namespace: pNS(ns.Env, ns.App), Name: track.Name, SchemaVersion: &track.SchemaVersion,
		ClientName: "client", InstanceId: "instance", LastSeenRevision: firstActivation.ActivationRevision - 1,
	}}}); err != nil {
		t.Fatal(err)
	}
	event, err := reconnected.Recv()
	if err != nil || event.GetSnapshot().GetRelease().GetVersion() != current.Release.Version || event.GetSnapshot().GetRelease().GetSchemaVersion() != track.SchemaVersion || event.GetRevision() != current.ActivationRevision {
		t.Fatalf("reconnect snapshot did not recover the exact track: %v %v", event, err)
	}
}
