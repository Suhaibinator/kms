package configstore

import (
	"context"
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
		Release: "runtime",
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
