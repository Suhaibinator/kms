package watch

import (
	"context"
	"errors"
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

func releaseWatchRegistration(t *testing.T, st *storage.SQLStore, ns domain.NamespaceRef) ReleaseRegistration {
	t.Helper()
	namespace, err := st.GetNamespace(context.Background(), ns)
	if err != nil {
		t.Fatal(err)
	}
	return ReleaseRegistration{Namespace: ns, NamespaceID: namespace.ID, Name: "runtime"}
}

// syntheticReleaseReplayStore keeps real release/snapshot reads while exposing
// a controlled change-log tail for incarnation-bound replay tests.
type syntheticReleaseReplayStore struct {
	*storage.SQLStore
	entries []domain.ChangeLogEntry
}

func (s *syntheticReleaseReplayStore) CurrentRevision(context.Context) (uint64, error) {
	if len(s.entries) == 0 {
		return 0, nil
	}
	return s.entries[len(s.entries)-1].Revision, nil
}

func (s *syntheticReleaseReplayStore) OldestRetainedRevision(context.Context) (uint64, error) {
	if len(s.entries) == 0 {
		return 0, nil
	}
	return s.entries[0].Revision, nil
}

func (s *syntheticReleaseReplayStore) ListChangesSince(_ context.Context, since uint64, limit int) ([]domain.ChangeLogEntry, error) {
	out := make([]domain.ChangeLogEntry, 0, limit)
	for _, entry := range s.entries {
		if entry.Revision <= since {
			continue
		}
		out = append(out, entry)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func TestReleaseSubscriptionRejectsMissingNamespaceIncarnation(t *testing.T) {
	st, ns := releaseWatchStore(t)
	hub := NewHub(st, nil, Options{})
	_, err := hub.SubscribeRelease(context.Background(), ReleaseRegistration{Namespace: ns, Name: "runtime"})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("missing namespace incarnation err = %v, want ErrInvalidArgument", err)
	}
}

func TestReleaseSubscriptionLegacyNamespaceReplayFallsBackToSnapshot(t *testing.T) {
	ctx := context.Background()
	st, ns := releaseWatchStore(t)
	release := createWatchRelease(t, st, ns, "one")
	active := activateWatchRelease(t, st, ns, release.Version)
	reg := releaseWatchRegistration(t, st, ns)
	reg.LastSeenRevision = 1
	store := &syntheticReleaseReplayStore{SQLStore: st, entries: []domain.ChangeLogEntry{
		{Revision: 1, ResourceType: domain.ResourceParameter, NamespaceID: reg.NamespaceID, Ref: domain.Ref{NS: ns, Key: "cursor"}},
		{Revision: 2, ResourceType: domain.ResourceConfigurationRelease, NamespaceID: 0, Ref: domain.Ref{NS: ns, Key: "runtime"}, ChangeType: "activate", Version: release.Version},
	}}
	hub := NewHub(store, nil, Options{})
	sub, err := hub.SubscribeRelease(ctx, reg)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	bl := sub.Backlog()
	if !bl.IsSnapshot || len(bl.Events) != 1 || bl.Events[0].Version != active.Release.Version {
		t.Fatalf("legacy release row did not force a safe active snapshot: %+v", bl)
	}
}

func TestReleaseSubscriptionNamespaceIncarnationIsolatesReplayAndLive(t *testing.T) {
	ctx := context.Background()
	st, ns := releaseWatchStore(t)
	release := createWatchRelease(t, st, ns, "one")
	activateWatchRelease(t, st, ns, release.Version)
	reg := releaseWatchRegistration(t, st, ns)
	const firstRevision uint64 = 1
	recreatedID := reg.NamespaceID + 1
	store := &syntheticReleaseReplayStore{SQLStore: st, entries: []domain.ChangeLogEntry{
		{Revision: firstRevision, ResourceType: domain.ResourceParameter, NamespaceID: reg.NamespaceID, Ref: domain.Ref{NS: ns, Key: "cursor"}},
		{Revision: 2, ResourceType: domain.ResourceConfigurationRelease, NamespaceID: reg.NamespaceID, Ref: domain.Ref{NS: ns, Key: "runtime"}, ChangeType: "activate", Version: release.Version},
		{Revision: 3, ResourceType: domain.ResourceConfigurationRelease, NamespaceID: recreatedID, Ref: domain.Ref{NS: ns, Key: "runtime"}, ChangeType: "activate", Version: release.Version},
	}}
	hub := NewHub(store, nil, Options{})
	reg.LastSeenRevision = firstRevision
	sub, err := hub.SubscribeRelease(ctx, reg)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	bl := sub.Backlog()
	if bl.IsSnapshot || len(bl.Events) != 1 || bl.Events[0].Revision != 2 {
		t.Fatalf("release replay crossed namespace incarnations: %+v", bl)
	}

	hub.dispatch(domain.ChangeLogEntry{Revision: 4, ResourceType: domain.ResourceConfigurationRelease, NamespaceID: recreatedID, Ref: domain.Ref{NS: ns, Key: "runtime"}, ChangeType: "activate", Version: release.Version})
	select {
	case event := <-sub.Events():
		t.Fatalf("recreated namespace reached old release subscriber: %+v", event)
	default:
	}
	hub.dispatch(domain.ChangeLogEntry{Revision: 5, ResourceType: domain.ResourceConfigurationRelease, NamespaceID: reg.NamespaceID, Ref: domain.Ref{NS: ns, Key: "runtime"}, ChangeType: "activate", Version: release.Version})
	select {
	case event := <-sub.Events():
		if event.Revision != 5 || event.Version != release.Version {
			t.Fatalf("exact-incarnation live release event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("exact-incarnation live release event was not delivered")
	}
}

func TestReleaseSubscriptionInitialSnapshotAndReplay(t *testing.T) {
	ctx := context.Background()
	st, ns := releaseWatchStore(t)
	r1 := createWatchRelease(t, st, ns, "one")
	a1 := activateWatchRelease(t, st, ns, r1.Version)
	hub := NewHub(st, nil, Options{})
	reg := releaseWatchRegistration(t, st, ns)
	snap, err := hub.SubscribeRelease(ctx, reg)
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
	reg.LastSeenRevision = a1.ActivationRevision
	replay, err := hub.SubscribeRelease(ctx, reg)
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
	reg := releaseWatchRegistration(t, st, ns)
	reg.LastSeenRevision = a1.ActivationRevision
	sub, err := hub.SubscribeRelease(ctx, reg)
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
	reg := releaseWatchRegistration(t, st, ns)
	reg.LastSeenRevision = a1.ActivationRevision
	sub, err := hub.SubscribeRelease(ctx, reg)
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
	reg := releaseWatchRegistration(t, st, ns)
	sub, err := hub.SubscribeRelease(ctx, reg)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	r2 := createWatchRelease(t, st, ns, "two")
	a2 := activateWatchRelease(t, st, ns, r2.Version)
	r3 := createWatchRelease(t, st, ns, "three")
	a3 := activateWatchRelease(t, st, ns, r3.Version)
	hub.dispatch(domain.ChangeLogEntry{Revision: a2.ActivationRevision, ResourceType: domain.ResourceConfigurationRelease, NamespaceID: reg.NamespaceID, Ref: domain.Ref{NS: ns, Key: "runtime"}, ChangeType: "activate", Version: r2.Version})
	hub.dispatch(domain.ChangeLogEntry{Revision: a3.ActivationRevision, ResourceType: domain.ResourceConfigurationRelease, NamespaceID: reg.NamespaceID, Ref: domain.Ref{NS: ns, Key: "runtime"}, ChangeType: "activate", Version: r3.Version})
	select {
	case e := <-sub.Events():
		if e.Version != r3.Version || e.Revision != a3.ActivationRevision {
			t.Fatalf("coalesced event=%+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no coalesced release event")
	}
}
