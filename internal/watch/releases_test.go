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
	a, changed, err := st.ActivateConfigurationRelease(context.Background(), domain.ReleaseTrack{Namespace: ns, Name: "runtime", SchemaVersion: 0}, v, nil)
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
	current, err := st.CurrentRevision(ctx)
	if err != nil || bl.Revision != current || bl.Revision <= a1.ActivationRevision {
		t.Fatalf("snapshot lost global cursor: backlog=%+v current=%d err=%v", bl, current, err)
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

func TestSubscribersIncludesNamespaceAndReleaseStreams(t *testing.T) {
	ctx := context.Background()
	st, ns := releaseWatchStore(t)
	rel := createWatchRelease(t, st, ns, "one")
	active := activateWatchRelease(t, st, ns, rel.Version)
	now := time.Now().UTC()
	hub := NewHub(st, nil, Options{now: func() time.Time { return now }})
	reg := releaseWatchRegistration(t, st, ns)
	reg.ClientName, reg.InstanceID, reg.Identity, reg.RemoteAddr = "client", "instance", "identity", "127.0.0.1"
	namespaceSub, err := hub.Subscribe(ctx, Registration{Namespaces: []domain.NamespaceRef{ns}, NamespaceIDs: map[domain.NamespaceRef]int64{ns: reg.NamespaceID}})
	if err != nil {
		t.Fatal(err)
	}
	defer namespaceSub.Close()
	sub, err := hub.SubscribeRelease(ctx, reg)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	rows := hub.Subscribers()
	if len(rows) != 2 {
		t.Fatalf("subscribers = %+v", rows)
	}
	var row domain.Subscriber
	for _, r := range rows {
		if r.ReleaseName != "" {
			row = r
		}
	}
	if row.ReleaseName != "runtime" || row.ClientName != reg.ClientName || row.InstanceID != reg.InstanceID || row.Identity != reg.Identity || row.RemoteAddr != reg.RemoteAddr || !row.ConnectedAt.Equal(now) || len(row.Namespaces) != 1 || row.Namespaces[0] != ns {
		t.Fatalf("release row = %+v", row)
	}
	if row.ReleaseState != "" || row.LastAckedRevision != 0 || !row.LastHeartbeat.IsZero() {
		t.Fatalf("registration fabricated progress: %+v", row)
	}
	sub.RecordEffectiveAcknowledgement(domain.ReleaseAcknowledgement{Namespace: ns, ReleaseName: reg.Name, SchemaVersion: reg.SchemaVersion, State: domain.ReleaseStateRejected, ReleaseVersion: rel.Version, ActivationRevision: active.ActivationRevision})
	for _, r := range hub.Subscribers() {
		if r.ReleaseName != "" {
			row = r
		}
	}
	if row.ReleaseState != domain.ReleaseStateRejected || row.ReleaseVersion != rel.Version || row.ReleaseRevision != active.ActivationRevision || row.LastAckedRevision != 0 {
		t.Fatalf("release lifecycle = %+v", row)
	}
	sub.Close()
	if rows := hub.Subscribers(); len(rows) != 1 || rows[0].ReleaseName != "" {
		t.Fatalf("closed release remains: %+v", rows)
	}
}

func TestReleaseRegistryPublishesPersistedSnapshotWithoutReduction(t *testing.T) {
	ns := domain.NamespaceRef{App: "app", Env: "prod"}
	sub := &ReleaseSubscription{reg: ReleaseRegistration{Namespace: ns, Name: "runtime", SchemaVersion: 4, SessionID: "session"}}
	applied := domain.ReleaseAcknowledgement{Namespace: ns, ReleaseName: "runtime", SchemaVersion: 4, SessionID: "session", TargetRevision: 153, ActivationRevision: 153, Sequence: 3, State: domain.ReleaseStateApplied, ReleaseVersion: 4, AppliedDivergent: true, DivergentFieldCount: 2}
	sub.RecordEffectiveAcknowledgement(applied)
	if got := sub.acknowledgement; got.Sequence != 3 || !got.AppliedDivergent || got.DivergentFieldCount != 2 {
		t.Fatalf("lost persisted metadata: %+v", got)
	}
	foreign := applied
	foreign.SessionID, foreign.State = "another-session", domain.ReleaseStateRejected
	sub.RecordEffectiveAcknowledgement(foreign)
	if sub.acknowledgement.State != domain.ReleaseStateApplied {
		t.Fatal("foreign session overwrote live state")
	}
	// A new authoritative snapshot may explicitly clear prior fields. There
	// must not be a second lifecycle rank or revision comparator in the hub.
	cleared := applied
	cleared.State, cleared.Sequence, cleared.AppliedDivergent, cleared.DivergentFieldCount = domain.ReleaseStateReceived, 4, false, 0
	sub.RecordEffectiveAcknowledgement(cleared)
	if got := sub.acknowledgement; got.Sequence != 4 || got.AppliedDivergent || got.DivergentFieldCount != 0 {
		t.Fatalf("metadata not replaced atomically: %+v", got)
	}
}

func TestReleaseQueueRejectsForeignTrackBeforeCoalescing(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "live"}[ready], func(t *testing.T) {
			ns := domain.NamespaceRef{Env: "prod", App: "app"}
			sub := &ReleaseSubscription{reg: ReleaseRegistration{Namespace: ns, NamespaceID: 7, Name: "runtime", SchemaVersion: 1}, ready: ready, events: make(chan ReleaseEvent, 1)}
			matching := domain.ChangeLogEntry{ResourceType: domain.ResourceConfigurationRelease, Ref: domain.Ref{NS: ns, Key: "runtime"}, NamespaceID: 7, SchemaVersion: 1, Version: 2, Revision: 10}
			sub.offer(matching)
			foreign := matching
			foreign.SchemaVersion = 2
			foreign.Revision = 11
			sub.offer(foreign)
			foreign = matching
			foreign.NamespaceID = 8
			foreign.Revision = 12
			sub.offer(foreign)
			if !ready {
				sub.activate(ReleaseBacklog{})
			}
			select {
			case event := <-sub.Events():
				if event.SchemaVersion != 1 || event.NamespaceID != 7 || event.Revision != 10 {
					t.Fatalf("foreign event superseded matching candidate: %+v", event)
				}
			default:
				t.Fatal("matching candidate was lost")
			}
		})
	}
}

func TestReleaseWatchKnownInactiveTrackWaits(t *testing.T) {
	st, ns := releaseWatchStore(t)
	ctx := context.Background()
	schema, err := st.CreateConfigurationSchema(ctx, domain.ConfigurationSchema{Application: ns.App, ReleaseName: "runtime", Schema: `{"type":"object"}`, Digest: "inactive-schema", Metadata: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub(st, nil, Options{})
	hubCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = hub.Run(hubCtx) }()
	<-hub.Started()
	reg := releaseWatchRegistration(t, st, ns)
	reg.SchemaVersion = schema.Version
	if _, _, err := st.PutParameter(ctx, domain.Ref{NS: ns, Key: "unrelated"}, "1", "integer", "{}", "test"); err != nil {
		t.Fatal(err)
	}
	sub, err := hub.SubscribeRelease(ctx, reg)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if len(sub.Backlog().Events) != 0 {
		t.Fatal("inactive track fabricated a release")
	}
	current, err := st.CurrentRevision(ctx)
	if err != nil || sub.Backlog().Revision != current {
		t.Fatalf("inactive snapshot lost global cursor: backlog=%+v current=%d err=%v", sub.Backlog(), current, err)
	}
	select {
	case <-sub.Done():
		t.Fatal("inactive track closed")
	default:
	}
	release, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ns, Name: reg.Name, SchemaVersion: reg.SchemaVersion, Digest: "first-active", Metadata: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	active, _, err := st.ActivateConfigurationRelease(ctx, reg.Track(), release.Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	hub.Wake()
	select {
	case event := <-sub.Events():
		if event.SchemaVersion != reg.SchemaVersion || event.Revision != active.ActivationRevision {
			t.Fatalf("first activation: %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("inactive stream did not receive its first activation")
	}
	reg.SchemaVersion++
	if _, err := hub.SubscribeRelease(ctx, reg); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown track: %v", err)
	}
}

// The active read can observe a commit newer than the captured global cursor.
type releaseSnapshotRaceStore struct {
	*storage.SQLStore
	beforeActive func()
}

func (s *releaseSnapshotRaceStore) GetActiveConfigurationRelease(ctx context.Context, track domain.ReleaseTrack) (domain.ActiveConfigurationRelease, error) {
	s.beforeActive()
	return s.SQLStore.GetActiveConfigurationRelease(ctx, track)
}
func TestReleaseSnapshotIncludesActivationAfterCursorCapture(t *testing.T) {
	st, ns := releaseWatchStore(t)
	rel := createWatchRelease(t, st, ns, "first")
	var active domain.ActiveConfigurationRelease
	store := &releaseSnapshotRaceStore{SQLStore: st, beforeActive: func() {
		active = activateWatchRelease(t, st, ns, rel.Version)
	}}
	hub := NewHub(store, nil, Options{})
	sub, err := hub.SubscribeRelease(context.Background(), releaseWatchRegistration(t, st, ns))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	bl := sub.Backlog()
	if len(bl.Events) != 1 || bl.Revision != active.ActivationRevision || bl.Events[0].Revision != active.ActivationRevision {
		t.Fatalf("racing activation identity/cursor = %+v, want %+v", bl, active)
	}
}
