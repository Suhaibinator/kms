package integration

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"google.golang.org/grpc"
)

// Delay only the service's lookup of an already-dequeued live release. The
// watch hub, retention, and resumed lookup all use the real SQLite store.
type heldReleaseDelivery struct {
	*storage.SQLStore
	track   domain.ReleaseTrack
	armed   atomic.Bool
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (s *heldReleaseDelivery) GetConfigurationRelease(ctx context.Context, track domain.ReleaseTrack, version uint64) (domain.ConfigurationRelease, error) {
	if s.armed.Load() && track == s.track && version == 1 {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.resume:
		case <-ctx.Done():
			return domain.ConfigurationRelease{}, ctx.Err()
		}
	}
	return s.SQLStore.GetConfigurationRelease(ctx, track, version)
}

func TestSDKRecoversWhenRetentionPrunesQueuedRelease(t *testing.T) {
	held := &heldReleaseDelivery{entered: make(chan struct{}), resume: make(chan struct{})}
	env := newLoopbackTLSEnvWithStoreWrapper(t, func(st *storage.SQLStore) storage.Store {
		held.SQLStore = st
		return held
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	principal := core.Principal{Identity: domain.Identity{Name: "network-root", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken}
	ns := domain.NamespaceRef{Env: "prod", App: "delivery-retention"}
	_, schema, err := env.svc.CreateApplicationWithSchema(ctx, principal, domain.Application{Name: ns.App, ReleaseName: "runtime"},
		`{"type":"object","properties":{"setting":{"type":"integer"}},"required":["setting"],"additionalProperties":false}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	newer, err := env.svc.CreateConfigurationSchema(ctx, principal, ns.App,
		`{"type":"object","properties":{"setting":{"type":"integer","minimum":0}},"required":["setting"],"additionalProperties":false}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.CreateNamespace(ctx, principal, ns, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatal(err)
	}
	ref := domain.Ref{NS: ns, Key: "setting"}
	if _, _, err := env.svc.PutParameter(ctx, principal, ref, "1", "integer", "{}"); err != nil {
		t.Fatal(err)
	}
	track := domain.ReleaseTrack{Namespace: ns, Name: "runtime", SchemaVersion: schema.Version}
	held.track = track
	create := func(schemaVersion uint64) domain.ConfigurationRelease {
		t.Helper()
		release, err := env.svc.CreateConfigurationRelease(ctx, principal, domain.CreateConfigurationReleaseInput{
			Namespace: ns, Name: track.Name, SchemaVersion: schemaVersion,
			Entries: []domain.ReleaseEntrySelector{{Alias: "setting", Kind: domain.ReleaseEntryParameter, Ref: ref, Version: 1}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return release
	}
	for range 4 {
		create(track.SchemaVersion)
	}
	foreign := create(newer.Version)
	activate := func(selected domain.ReleaseTrack, version uint64) {
		t.Helper()
		if _, _, err := env.store.ActivateConfigurationRelease(ctx, selected, version, nil); err != nil {
			t.Fatal(err)
		}
		env.hub.Wake()
	}
	activate(domain.ReleaseTrack{Namespace: ns, Name: track.Name, SchemaVersion: foreign.SchemaVersion}, foreign.Version)
	held.armed.Store(true)
	probe := &retentionWatchProbe{reset: make(chan struct{}), heartbeats: make(chan int64, 64)}
	sdk, err := kmsclient.NewClient(kmsclient.Config{
		Endpoint: env.endpoint(), Namespace: ns.Env + "/" + ns.App, Token: env.adminToken,
		TLS: env.clientTLS(nil), ClientName: "delivery-retention-sdk", Timeout: 2 * time.Second,
		DialOptions: []grpc.DialOption{grpc.WithStreamInterceptor(probe.intercept), grpc.WithUnaryInterceptor(legacyReleaseProtocol)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sdk.Close() }()
	loader, err := kmsclient.NewReleaseLoader(sdk, kmsclient.ReleaseLoaderConfig{
		Name: track.Name, SchemaSHA256: schema.Digest, ReconcileInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	var applied atomic.Uint64
	go func() {
		done <- loader.Run(runCtx, func(_ context.Context, snapshot kmsclient.ReleaseSnapshot) (kmsclient.PreparedRelease, error) {
			if snapshot.SchemaVersion() != track.SchemaVersion {
				return nil, errors.New("SDK crossed tracks during retention recovery")
			}
			return schemaTrackPrepared{commit: func() { applied.Store(snapshot.Version()) }}, nil
		})
	}()
	defer func() {
		stop()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("loader stopped unexpectedly: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("loader did not stop")
		}
	}()
	select {
	case <-probe.heartbeats:
	case <-ctx.Done():
		t.Fatal("SDK did not subscribe to the inactive older schema")
	}
	activate(track, 1)
	select {
	case <-held.entered:
	case <-ctx.Done():
		t.Fatal("activation did not reach delayed delivery lookup")
	}
	for version := uint64(2); version <= 4; version++ {
		activate(track, version)
	}
	if _, err := env.store.PruneChangeLog(ctx, time.Nanosecond, 1); err != nil {
		t.Fatal(err)
	}
	if n, err := env.store.PruneConfigurationReleases(ctx, time.Nanosecond, 1); err != nil || n != 1 {
		t.Fatalf("prune queued release: count=%d error=%v", n, err)
	}
	if _, err := env.store.GetConfigurationRelease(ctx, track, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("queued release was not pruned: %v", err)
	}
	close(held.resume)
	// Reconciliation is disabled for this test's lifetime. The actual SDK must
	// retry the server's Aborted status and recover the exact track snapshot.
	waitForManagedState(t, func() bool { return applied.Load() == 4 }, "SDK automatic recovery after queued release retention")
	if probe.opened.Load() < 2 {
		t.Fatal("SDK applied current release without opening a replacement watch")
	}
}
