package configstore

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

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
		OnDefaultMismatch: callback,
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

func TestStartReturnsTypedDefaultMismatchAfterLoaderRedactionBoundary(t *testing.T) {
	client, _ := startTestClient(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var reported atomic.Int32
	var aborted atomic.Int32

	manager, err := Start(ctx, client, startTestOptions(func(report DefaultMismatchReport) {
		reported.Add(1)
		if report.Phase() != MismatchStartup || report.Severity() != MismatchFatal {
			t.Errorf("report = %s/%s", report.Phase(), report.Severity())
		}
	}), func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
		return PreparedCandidate{
			Publish: func() { t.Error("mismatched startup candidate published") },
			Abort:   func() { aborted.Add(1) },
			DefaultDifferences: []FieldDifference{{
				Path: "settings.enabled", Expected: false, Actual: true,
			}},
		}, nil
	})
	if manager != nil || err == nil {
		t.Fatalf("Start() = (%v, %v), want typed mismatch", manager, err)
	}
	if _, ok := errors.AsType[*DefaultMismatchError](err); !ok {
		t.Fatalf("errors.As(*DefaultMismatchError) = false: %T %v", err, err)
	}
	if reported.Load() != 1 || aborted.Load() != 1 {
		t.Fatalf("reported=%d aborted=%d", reported.Load(), aborted.Load())
	}
}
