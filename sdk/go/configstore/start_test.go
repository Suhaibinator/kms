package configstore

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient/kmsclienttest"
)

func startTestClient(t *testing.T) (*kmsclient.Client, *kmsclienttest.Server) {
	t.Helper()
	server, err := kmsclienttest.New()
	if err != nil {
		t.Fatal(err)
	}
	server.SetParameterVersion("prod/app", "groups/runtime", `{"enabled":true}`, "json", 1)
	_, err = server.SetActiveRelease(kmsclienttest.ReleaseSpec{
		Namespace: "prod/app",
		Name:      "runtime",
		Version:   1,
		Entries: []kmsclienttest.ReleaseEntrySpec{
			{Alias: "settings", Kind: "parameter", Path: "groups/runtime", Version: 1},
		},
	}, 1)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	client, err := kmsclient.NewClient(kmsclient.Config{
		Namespace:   "prod/app",
		ClientName:  "configstore-test",
		DialOptions: server.DialOptions(),
	})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		server.Close()
	})
	return client, server
}

func startTestOptions(callback func(DefaultMismatchReport)) Options {
	return Options{
		SchemaVersion: new(uint64),
		Release:       "runtime",
		Contract: []ContractEntry{{
			Alias: "settings", Kind: ContractKindParameter, ContentType: "json",
		}},
		Callbacks: Callbacks{OnDefaultMismatch: callback},
	}
}

func TestStartWaitsForInitialPublicationAndWaitNormalizesCancellation(t *testing.T) {
	client, _ := startTestClient(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var published atomic.Bool

	manager, err := Start(ctx, client, startTestOptions(func(DefaultMismatchReport) {
		t.Error("unexpected mismatch callback")
	}), func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
		return PreparedCandidate{Publish: func() { published.Store(true) }}, nil
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !published.Load() || !manager.Status().Ready {
		t.Fatalf("Start returned before readiness: published=%v status=%#v", published.Load(), manager.Status())
	}
	cancel()
	if err := manager.Wait(); err != nil {
		t.Fatalf("Wait() after context cancellation = %v, want nil", err)
	}
}

func TestStartAppliesStartupMismatchAndAcknowledgesDivergence(t *testing.T) {
	client, server := startTestClient(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var reported atomic.Int32
	var aborted atomic.Int32
	var published atomic.Int32

	manager, err := Start(ctx, client, startTestOptions(func(report DefaultMismatchReport) {
		reported.Add(1)
		if report.Phase() != PhaseStartup || report.Severity() != MismatchError {
			t.Errorf("report = %s/%s", report.Phase(), report.Severity())
		}
	}), func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
		return PreparedCandidate{
			Publish: func() { published.Add(1) },
			Abort:   func() { aborted.Add(1) },
			DefaultDifferences: []FieldDifference{
				{Path: "settings.enabled", Expected: false, Actual: true},
				{Path: "settings.limit", Expected: 1, Actual: 2},
			},
		}, nil
	})
	if err != nil || manager == nil {
		t.Fatalf("Start() = (%v, %v), want manager despite startup divergence", manager, err)
	}
	status := manager.Status()
	if !status.Ready || !status.DefaultDivergent || status.Applied.Version() != 1 {
		t.Fatalf("Status() = %+v", status)
	}
	if reported.Load() != 1 || aborted.Load() != 0 || published.Load() != 1 {
		t.Fatalf("reported=%d aborted=%d published=%d", reported.Load(), aborted.Load(), published.Load())
	}

	subscription, err := server.WaitForReleaseSubscribe(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var applied *kmsv1.ReleaseAcknowledgement
	for applied == nil {
		ack, ackErr := subscription.WaitAcknowledgement(2 * time.Second)
		if ackErr != nil {
			t.Fatal(ackErr)
		}
		if ack.GetAppliedDivergent() && ack.GetState() != kmsclient.ReleaseStateApplied {
			t.Fatalf("%s ack carried applied_divergent", ack.GetState())
		}
		if ack.GetState() == kmsclient.ReleaseStateApplied {
			applied = ack
		}
	}
	if !applied.GetAppliedDivergent() || applied.GetDivergentFieldCount() != 2 {
		t.Fatalf("applied ack = %+v, want applied_divergent=true divergent_field_count=2", applied)
	}
	if applied.GetDiagnostic() != "" {
		t.Fatalf("applied ack carried a diagnostic: %q", applied.GetDiagnostic())
	}
	cancel()
	if err := manager.Wait(); err != nil {
		t.Fatalf("Wait() = %v", err)
	}
}

func TestStatusPreservesSelectedTrackWhileNewerActivationIsQueued(t *testing.T) {
	for _, schemaVersion := range []uint64{0, 7} {
		t.Run(fmt.Sprintf("schema_%d", schemaVersion), func(t *testing.T) {
			server, err := kmsclienttest.New()
			if err != nil {
				t.Fatal(err)
			}
			for version := uint64(1); version <= 3; version++ {
				server.SetParameterVersion("prod/app", "groups/runtime", fmt.Sprintf(`{"version":%d}`, version), "json", version)
			}
			setRelease := func(version uint64) {
				t.Helper()
				_, setErr := server.SetActiveRelease(kmsclienttest.ReleaseSpec{
					Namespace:     "prod/app",
					Name:          "runtime",
					Version:       version,
					SchemaVersion: schemaVersion,
					Entries: []kmsclienttest.ReleaseEntrySpec{{
						Alias: "settings", Kind: "parameter", Path: "groups/runtime", Version: version,
					}},
				}, version)
				if setErr != nil {
					t.Fatal(setErr)
				}
			}
			setRelease(1)
			client, err := kmsclient.NewClient(kmsclient.Config{
				Namespace: "prod/app", ClientName: "configstore-status-test", DialOptions: server.DialOptions(),
			})
			if err != nil {
				server.Close()
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = client.Close()
				server.Close()
			})

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			secondPreparing := make(chan struct{})
			releaseSecond := make(chan struct{})
			releaseSecondOnce := sync.OnceFunc(func() { close(releaseSecond) })
			manager, err := Start(ctx, client, Options{
				Release: "runtime", SchemaVersion: &schemaVersion,
				Contract:  []ContractEntry{{Alias: "settings", Kind: ContractKindParameter, ContentType: "json"}},
				Callbacks: Callbacks{OnDefaultMismatch: func(DefaultMismatchReport) {}},
			}, func(_ context.Context, snapshot kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
				if snapshot.Version() == 2 {
					close(secondPreparing)
					<-releaseSecond // Deliberately hold after supersession to keep release 3 queued.
				}
				return PreparedCandidate{Publish: func() {}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				releaseSecondOnce()
				cancel()
				if waitErr := manager.Wait(); waitErr != nil {
					t.Errorf("Wait() during cleanup = %v", waitErr)
				}
			})
			if _, err := server.WaitForReleaseSubscribe(2 * time.Second); err != nil {
				t.Fatal(err)
			}

			activate := func(version uint64) {
				t.Helper()
				_, activateErr := server.ActivateConfigurationRelease(kmsclienttest.ReleaseSpec{
					Namespace:     "prod/app",
					Name:          "runtime",
					Version:       version,
					SchemaVersion: schemaVersion,
					Entries: []kmsclienttest.ReleaseEntrySpec{{
						Alias: "settings", Kind: "parameter", Path: "groups/runtime", Version: version,
					}},
				}, version)
				if activateErr != nil {
					t.Fatal(activateErr)
				}
			}
			activate(2)
			select {
			case <-secondPreparing:
			case <-time.After(2 * time.Second):
				t.Fatal("release 2 did not reach prepare")
			}
			activate(3)
			deadline := time.Now().Add(2 * time.Second)
			for manager.Status().Observed.Version() != 3 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			observed := manager.Status().Observed
			if observed.Namespace() != "prod/app" || observed.Name() != "runtime" ||
				observed.Version() != 3 || observed.ActivationRevision() != 3 ||
				observed.SchemaVersion() != schemaVersion || observed.Digest() != "" {
				t.Fatalf("queued observed identity = %+v", observed)
			}

			releaseSecondOnce()
			deadline = time.Now().Add(2 * time.Second)
			for manager.Status().Applied.Version() != 3 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if got := manager.Status().Applied.Version(); got != 3 {
				t.Fatalf("applied version = %d, want 3", got)
			}
		})
	}
}
