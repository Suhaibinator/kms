package watch

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func releaseWatchStore(t *testing.T) (*storage.SQLStore, domain.NamespaceRef) {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	if _, err := st.CreateNamespace(context.Background(), domain.Namespace{NamespaceRef: ns}); err != nil {
		t.Fatal(err)
	}
	return st, ns
}
func createWatchRelease(t *testing.T, st *storage.SQLStore, ns domain.NamespaceRef, digest string) domain.ConfigurationRelease {
	t.Helper()
	r, err := st.CreateConfigurationRelease(context.Background(), domain.ConfigurationRelease{Namespace: ns, Name: "runtime", Digest: digest, Metadata: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func activateWatchRelease(t *testing.T, st *storage.SQLStore, ns domain.NamespaceRef, v uint64) domain.ActiveConfigurationRelease {
	t.Helper()
	a, changed, err := st.ActivateConfigurationRelease(context.Background(), ns, "runtime", v, nil)
	if err != nil || !changed {
		t.Fatalf("activate v%d changed=%v err=%v", v, changed, err)
	}
	return a
}

func TestReleaseSubscriptionInitialSnapshotAndReplay(t *testing.T) {
	ctx := context.Background()
	st, ns := releaseWatchStore(t)
	r1 := createWatchRelease(t, st, ns, "one")
	a1 := activateWatchRelease(t, st, ns, r1.Version)
	hub := NewHub(st, nil, Options{})
	snap, err := hub.SubscribeRelease(ctx, ReleaseRegistration{Namespace: ns, Name: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	bl := snap.Backlog()
	if !bl.IsSnapshot || len(bl.Events) != 1 || bl.Events[0].Release.Version != 1 || bl.Events[0].Revision != a1.ActivationRevision {
		t.Fatalf("snapshot=%+v", bl)
	}
	r2 := createWatchRelease(t, st, ns, "two")
	a2 := activateWatchRelease(t, st, ns, r2.Version)
	replay, err := hub.SubscribeRelease(ctx, ReleaseRegistration{Namespace: ns, Name: "runtime", LastSeenRevision: a1.ActivationRevision})
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	bl = replay.Backlog()
	if bl.IsSnapshot || len(bl.Events) != 1 || bl.Events[0].Release.Version != 2 || bl.Events[0].Revision != a2.ActivationRevision {
		t.Fatalf("replay=%+v", bl)
	}
}

func TestReleaseSubscriptionPrunedReplayFallsBackToSnapshot(t *testing.T) {
	ctx := context.Background()
	st, ns := releaseWatchStore(t)
	r1 := createWatchRelease(t, st, ns, "one")
	a1 := activateWatchRelease(t, st, ns, r1.Version)
	r2 := createWatchRelease(t, st, ns, "two")
	a2 := activateWatchRelease(t, st, ns, r2.Version)
	if _, err := st.PruneChangeLog(ctx, time.Nanosecond, 0); err != nil {
		t.Fatal(err)
	}
	hub := NewHub(st, nil, Options{})
	sub, err := hub.SubscribeRelease(ctx, ReleaseRegistration{Namespace: ns, Name: "runtime", LastSeenRevision: a1.ActivationRevision})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	bl := sub.Backlog()
	if !bl.IsSnapshot || len(bl.Events) != 1 || bl.Events[0].Release.Version != 2 || bl.Events[0].Revision != a2.ActivationRevision {
		t.Fatalf("fallback=%+v", bl)
	}
}

func TestReleaseSubscriptionEmptyFilteredReplayReturnsActiveSnapshot(t *testing.T) {
	ctx := context.Background()
	st, ns := releaseWatchStore(t)
	r1 := createWatchRelease(t, st, ns, "one")
	a1 := activateWatchRelease(t, st, ns, r1.Version)
	if _, _, err := st.PutParameter(ctx, domain.Ref{NS: ns, Key: "unrelated"}, "1", "integer", "{}", "test"); err != nil {
		t.Fatal(err)
	}
	hub := NewHub(st, nil, Options{})
	sub, err := hub.SubscribeRelease(ctx, ReleaseRegistration{Namespace: ns, Name: "runtime", LastSeenRevision: a1.ActivationRevision})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	bl := sub.Backlog()
	if !bl.IsSnapshot || len(bl.Events) != 1 || bl.Events[0].Release.Version != r1.Version || bl.Events[0].Revision != a1.ActivationRevision {
		t.Fatalf("empty filtered replay fallback=%+v", bl)
	}
}

func TestReleaseSubscriptionSlowConsumerCoalescesLatest(t *testing.T) {
	ctx := context.Background()
	st, ns := releaseWatchStore(t)
	r1 := createWatchRelease(t, st, ns, "one")
	activateWatchRelease(t, st, ns, r1.Version)
	hub := NewHub(st, nil, Options{})
	sub, err := hub.SubscribeRelease(ctx, ReleaseRegistration{Namespace: ns, Name: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	r2 := createWatchRelease(t, st, ns, "two")
	a2 := activateWatchRelease(t, st, ns, r2.Version)
	r3 := createWatchRelease(t, st, ns, "three")
	a3 := activateWatchRelease(t, st, ns, r3.Version)
	hub.dispatch(domain.ChangeLogEntry{Revision: a2.ActivationRevision, ResourceType: domain.ResourceConfigurationRelease, Ref: domain.Ref{NS: ns, Key: "runtime"}, ChangeType: "activate", Version: r2.Version})
	hub.dispatch(domain.ChangeLogEntry{Revision: a3.ActivationRevision, ResourceType: domain.ResourceConfigurationRelease, Ref: domain.Ref{NS: ns, Key: "runtime"}, ChangeType: "activate", Version: r3.Version})
	select {
	case e := <-sub.Events():
		if e.Version != r3.Version || e.Revision != a3.ActivationRevision {
			t.Fatalf("coalesced event=%+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no coalesced release event")
	}
}
