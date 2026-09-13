package grpcserver

import (
	"context"
	"errors"
	"net"
	"path/filepath"
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

func (s *delayedWatchReleaseLookup) ResolveInstanceRelease(ctx context.Context, ref domain.ReleaseSessionRef) (domain.InstanceReleaseTarget, error) {
	active, err := s.GetActiveConfigurationRelease(ctx, ref.Track)
	if err == nil && active.Release.Version == 1 {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.resume:
		case <-ctx.Done():
			return domain.InstanceReleaseTarget{}, ctx.Err()
		}
	}
	return s.SQLStore.ResolveInstanceRelease(ctx, ref)
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
	if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{Register: registerTestReleaseSession(t, watchCtx, kmsv1.NewConfigurationReleaseServiceClient(conn), &kmsv1.ReleaseWatchRegistration{Namespace: pNS(ns.Env, ns.App), Name: track.Name, SchemaVersion: new(uint64), ClientName: "client", InstanceId: "instance"})}}); err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil || first.GetTarget() == nil || first.GetTarget().GetRelease() != nil {
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
	activate()
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
	if outcome == "retained track" {
		for {
			event, err := stream.Recv()
			if err != nil {
				t.Fatal(err)
			}
			if event.GetTarget() == nil {
				continue
			}
			// An already queued target may precede the coalesced latest target.
			if event.GetTarget().GetRelease().GetVersion() != current.Release.Version {
				continue
			}
			break
		}
		if err := stream.CloseSend(); err != nil {
			t.Fatal(err)
		}
		reconnected, err := kmsv1.NewConfigurationReleaseServiceClient(conn).WatchRelease(watchCtx)
		if err != nil {
			t.Fatal(err)
		}
		if err := reconnected.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{Register: &kmsv1.ReleaseWatchRegistration{Namespace: pNS(ns.Env, ns.App), Name: track.Name, SchemaVersion: &track.SchemaVersion, ClientName: "client", InstanceId: "instance", SessionId: "instance-session"}}}); err != nil {
			t.Fatal(err)
		}
		event, err := reconnected.Recv()
		if err != nil || event.GetTarget().GetRelease().GetVersion() != current.Release.Version || event.GetRevision() != current.ActivationRevision {
			t.Fatalf("reconnect did not resolve retained target: %v %v", event, err)
		}
		return
	}
	for {
		_, err := stream.Recv()
		if err == nil {
			continue
		}
		if status.Code(err) != wantCode {
			t.Fatalf("stream status = %v, want %s", err, wantCode)
		}
		break
	}
}
