package integration

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

func TestProcessScopedReleasePinsOverRealKMS(t *testing.T) {
	env := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	admin := core.Principal{Identity: domain.Identity{Name: "network-root", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken}
	ns := domain.NamespaceRef{Env: "prod", App: "process-pins"}
	_, schema, err := env.svc.CreateApplicationWithSchema(ctx, admin, domain.Application{Name: ns.App, ReleaseName: "runtime"}, `{"type":"object"}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.CreateNamespace(ctx, admin, ns, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatal(err)
	}
	track := domain.ReleaseTrack{Namespace: ns, Name: "runtime", SchemaVersion: schema.Version}
	parameter := domain.Ref{NS: ns, Key: "setting"}
	if _, _, err := env.svc.PutParameter(ctx, admin, parameter, "1", "integer", "{}"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := env.svc.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{Namespace: ns, Name: track.Name, SchemaVersion: schema.Version, Entries: []domain.ReleaseEntrySelector{{Alias: "setting", Kind: domain.ReleaseEntryParameter, Ref: parameter, Version: 1}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := env.svc.ActivateConfigurationRelease(ctx, admin, track, 1, nil); err != nil {
		t.Fatal(err)
	}
	sdk, err := kmsclient.NewClient(kmsclient.Config{Endpoint: env.endpoint(), Namespace: ns.Env + "/" + ns.App, Token: env.adminToken, TLS: env.clientTLS(nil), ClientName: "pin-test", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sdk.Close() }()
	start := func(instance string) (*atomic.Uint64, context.CancelFunc, <-chan error) {
		loader, err := kmsclient.NewReleaseLoader(sdk, kmsclient.ReleaseLoaderConfig{Name: track.Name, SchemaVersion: &schema.Version, InstanceID: instance, ReconcileInterval: 20 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		run, stop := context.WithCancel(ctx)
		applied := new(atomic.Uint64)
		done := make(chan error, 1)
		go func() {
			done <- loader.Run(run, func(_ context.Context, snapshot kmsclient.ReleaseSnapshot) (kmsclient.PreparedRelease, error) {
				if snapshot.Version() == 4 {
					return nil, errors.New("application rejected test candidate")
				}
				return schemaTrackPrepared{commit: func() { applied.Store(snapshot.Version()) }}, nil
			})
		}()
		return applied, stop, done
	}
	a, stopA, doneA := start("replica-a")
	defer stopA()
	b, stopB, doneB := start("replica-b")
	defer stopB()
	waitForManagedState(t, func() bool { return a.Load() == 1 && b.Load() == 1 }, "initial fleet")
	rpc := kmsv1.NewConfigurationReleaseServiceClient(env.adminConn)
	adminRPC := kmsv1.NewAdminServiceClient(env.adminConn)
	auth := networkAuthContext(ctx, env.adminToken)
	var row *kmsv1.ReleaseSubscriberState
	waitForManagedState(t, func() bool {
		rows, e := adminRPC.ListReleaseSubscribers(auth, &kmsv1.ListReleaseSubscribersRequest{Namespace: networkNS(ns.Env, ns.App), ReleaseName: track.Name, SchemaVersion: &schema.Version})
		if e != nil {
			return false
		}
		for _, r := range rows.Subscribers {
			if r.InstanceId == "replica-a" && r.Connected && r.State == "applied" {
				row = r
				return r.SessionId != ""
			}
		}
		return false
	}, "pin-capable session")
	ref := &kmsv1.ReleaseSessionRef{Namespace: networkNS(ns.Env, ns.App), Name: track.Name, SchemaVersion: &schema.Version, Identity: row.Identity, ClientName: row.ClientName, InstanceId: row.InstanceId, SessionId: row.SessionId}
	pin, e := rpc.SetReleasePin(auth, &kmsv1.SetReleasePinRequest{Session: ref, Version: 2, ExpectedPinRevision: new(uint64(0))})
	if e != nil {
		t.Fatal(e)
	}
	if pin.ActivationRevision != 0 || pin.TargetRevision == 0 {
		t.Fatalf("unpublished pin fabricated activation: %v", pin)
	}
	waitForManagedState(t, func() bool { return a.Load() == 2 }, "unpublished pinned release")
	if _, _, e := env.svc.ActivateConfigurationRelease(ctx, admin, track, 3, nil); e != nil {
		t.Fatal(e)
	}
	waitForManagedState(t, func() bool { return b.Load() == 3 }, "following replica")
	if a.Load() != 2 {
		t.Fatal("fleet activation escaped pin")
	}
	failed, e := rpc.SetReleasePin(auth, &kmsv1.SetReleasePinRequest{Session: ref, Version: 4, ExpectedPinRevision: &pin.PinRevision})
	if e != nil {
		t.Fatal(e)
	}
	waitForManagedState(t, func() bool {
		rows, e := adminRPC.ListReleaseSubscribers(auth, &kmsv1.ListReleaseSubscribersRequest{Namespace: ref.Namespace, ReleaseName: track.Name, SchemaVersion: &schema.Version})
		if e != nil {
			return false
		}
		for _, r := range rows.Subscribers {
			if r.SessionId == ref.SessionId {
				return r.PinVersion == 4 && r.State == "rejected" && r.LastAppliedVersion == 2
			}
		}
		return false
	}, "rejected pin retains applied version")
	if a.Load() != 2 {
		t.Fatal("rejection lost last-known-good")
	}
	if _, e := rpc.SetReleasePin(auth, &kmsv1.SetReleasePinRequest{Session: ref, Version: 0, ExpectedPinRevision: &failed.PinRevision}); e != nil {
		t.Fatal(e)
	}
	waitForManagedState(t, func() bool { return a.Load() == 3 }, "unpin follows current")
	// Pin again, stop only the client, and replace it under the same instance name.
	follow, e := rpc.GetInstanceRelease(auth, &kmsv1.GetInstanceReleaseRequest{Session: ref})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := rpc.SetReleasePin(auth, &kmsv1.SetReleasePinRequest{Session: ref, Version: 2, ExpectedPinRevision: &follow.PinRevision}); e != nil {
		t.Fatal(e)
	}
	waitForManagedState(t, func() bool { return a.Load() == 2 }, "repin")
	stopA()
	if e := <-doneA; e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	replacement, stopReplacement, doneReplacement := start("replica-a")
	waitForManagedState(t, func() bool { return replacement.Load() == 3 }, "new client process follows active")
	stopReplacement()
	<-doneReplacement
	stopB()
	<-doneB
}
