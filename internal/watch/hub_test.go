package watch

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// fakeStore is an in-memory storage.Store exercising only the surface the hub
// touches: the change log, current/oldest revision, snapshot, and prune. All
// other methods panic — the hub must never call them.
type fakeStore struct {
	mu       sync.Mutex
	entries  []domain.ChangeLogEntry // append-only, revision-ordered
	oldest   uint64                  // 0 = derive from entries; >0 simulates pruning
	snapshot []domain.Parameter
	snapRev  uint64

	currentRevErr error
	listErr       error
	oldestErr     error
	snapErr       error

	pruneCalls int
	pruneErr   error
}

func (f *fakeStore) append(e ...domain.ChangeLogEntry) {
	f.mu.Lock()
	f.entries = append(f.entries, e...)
	f.mu.Unlock()
}

func (f *fakeStore) CurrentRevision(ctx context.Context) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.currentRevErr != nil {
		return 0, f.currentRevErr
	}
	if len(f.entries) == 0 {
		return 0, nil
	}
	return f.entries[len(f.entries)-1].Revision, nil
}

func (f *fakeStore) OldestRetainedRevision(ctx context.Context) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.oldestErr != nil {
		return 0, f.oldestErr
	}
	if f.oldest != 0 {
		return f.oldest, nil
	}
	if len(f.entries) == 0 {
		return 0, nil
	}
	return f.entries[0].Revision, nil
}

func (f *fakeStore) ListChangesSince(ctx context.Context, since uint64, limit int) ([]domain.ChangeLogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []domain.ChangeLogEntry
	for _, e := range f.entries {
		if e.Revision > since {
			out = append(out, e)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeStore) SnapshotParameters(ctx context.Context, selectors []domain.WatchSelector) ([]domain.Parameter, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snapErr != nil {
		return nil, 0, f.snapErr
	}
	rev := f.snapRev
	if rev == 0 && len(f.entries) > 0 {
		rev = f.entries[len(f.entries)-1].Revision
	}
	// Filter snapshot by the requested selectors, mirroring the real store.
	var out []domain.Parameter
	for _, p := range f.snapshot {
		if selectorMatchAny(selectors, p.Ref) {
			out = append(out, p)
		}
	}
	return out, rev, nil
}

func (f *fakeStore) PruneChangeLog(ctx context.Context, keepDuration time.Duration, maxRows int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruneCalls++
	if f.pruneErr != nil {
		return 0, f.pruneErr
	}
	return 0, nil
}

// --- unused Store methods (hub must never call these) ---

func (f *fakeStore) Ping(context.Context) error           { panic("unused") }
func (f *fakeStore) Close() error                         { panic("unused") }
func (f *fakeStore) Backup(context.Context, string) error { panic("unused") }
func (f *fakeStore) InsertKeyMetadata(context.Context, domain.KeyMetadata) error {
	panic("unused")
}
func (f *fakeStore) GetKeyMetadata(context.Context, string) (domain.KeyMetadata, error) {
	panic("unused")
}
func (f *fakeStore) ListKeyMetadata(context.Context) ([]domain.KeyMetadata, error) {
	panic("unused")
}
func (f *fakeStore) ActiveKeyMetadata(context.Context) (domain.KeyMetadata, error) {
	panic("unused")
}
func (f *fakeStore) SetKeyState(context.Context, string, string) error { panic("unused") }
func (f *fakeStore) RotateKEK(context.Context, domain.KeyMetadata,
	func(storage.SecretVersionRecord) ([]byte, error),
	func(storage.CAKeyRecord) ([]byte, error)) (int, int, error) {
	panic("unused")
}
func (f *fakeStore) CreateNamespace(context.Context, domain.Namespace) (domain.Namespace, error) {
	panic("unused")
}
func (f *fakeStore) GetNamespace(context.Context, domain.NamespaceRef) (domain.Namespace, error) {
	panic("unused")
}
func (f *fakeStore) UpdateNamespace(context.Context, domain.NamespaceRef, string, []domain.AuthMethod) (domain.Namespace, error) {
	panic("unused")
}
func (f *fakeStore) DeleteNamespace(context.Context, domain.NamespaceRef) error { panic("unused") }
func (f *fakeStore) ListNamespaces(context.Context, storage.ListPage) ([]domain.Namespace, string, error) {
	panic("unused")
}
func (f *fakeStore) PutParameter(context.Context, domain.Ref, string, string, string, string) (uint64, uint64, error) {
	panic("unused")
}
func (f *fakeStore) GetParameter(context.Context, domain.Ref, uint64, string) (domain.Parameter, error) {
	panic("unused")
}
func (f *fakeStore) GetParameterInfo(context.Context, domain.Ref) (domain.ParameterInfo, error) {
	panic("unused")
}
func (f *fakeStore) ListParameters(context.Context, domain.NamespaceRef, string, storage.ListPage) ([]domain.Parameter, string, error) {
	panic("unused")
}
func (f *fakeStore) DeleteParameter(context.Context, domain.Ref) (uint64, error) { panic("unused") }
func (f *fakeStore) CreateSecretVersion(context.Context, storage.CreateSecretParams) (uint64, uint64, error) {
	panic("unused")
}
func (f *fakeStore) GetSecretRecord(context.Context, domain.Ref) (storage.SecretRecord, error) {
	panic("unused")
}
func (f *fakeStore) GetSecretVersion(context.Context, domain.Ref, uint64, string) (storage.SecretRecord, storage.SecretVersionRecord, error) {
	panic("unused")
}
func (f *fakeStore) GetSecretInfo(context.Context, domain.Ref) (domain.Secret, error) {
	panic("unused")
}
func (f *fakeStore) ListSecrets(context.Context, domain.NamespaceRef, string, storage.ListPage) ([]domain.Secret, string, error) {
	panic("unused")
}
func (f *fakeStore) DeleteSecret(context.Context, domain.Ref) (uint64, error) { panic("unused") }
func (f *fakeStore) SetSecretVersionState(context.Context, domain.Ref, uint64, string) (uint64, error) {
	panic("unused")
}
func (f *fakeStore) DestroySecretVersion(context.Context, domain.Ref, uint64) (uint64, error) {
	panic("unused")
}
func (f *fakeStore) PromoteSecretVersion(context.Context, domain.Ref, uint64) (uint64, uint64, uint64, error) {
	panic("unused")
}
func (f *fakeStore) UpdateSecretAccessTokenHash(context.Context, domain.Ref, []byte) error {
	panic("unused")
}
func (f *fakeStore) CreateIdentity(context.Context, storage.CreateIdentityParams) (domain.Identity, error) {
	panic("unused")
}
func (f *fakeStore) GetIdentityByTokenHash(context.Context, []byte) (domain.Identity, error) {
	panic("unused")
}
func (f *fakeStore) GetIdentityByName(context.Context, string) (domain.Identity, error) {
	panic("unused")
}
func (f *fakeStore) ListIdentities(context.Context, storage.ListPage) ([]domain.Identity, string, error) {
	panic("unused")
}
func (f *fakeStore) SetIdentityDisabled(context.Context, string, bool) error { panic("unused") }
func (f *fakeStore) UpdateIdentityTokenHash(context.Context, string, []byte) error {
	panic("unused")
}
func (f *fakeStore) InsertCAKey(context.Context, storage.CAKeyRecord) error { panic("unused") }
func (f *fakeStore) ActiveCAKey(context.Context) (storage.CAKeyRecord, error) {
	panic("unused")
}
func (f *fakeStore) InsertIdentityCert(context.Context, string, domain.IdentityCert) error {
	panic("unused")
}
func (f *fakeStore) ListIdentityCerts(context.Context, string) ([]domain.IdentityCert, error) {
	panic("unused")
}
func (f *fakeStore) GetIdentityCertBySerial(context.Context, string) (storage.IdentityCertRecord, error) {
	panic("unused")
}
func (f *fakeStore) RevokeIdentityCert(context.Context, string) error { panic("unused") }
func (f *fakeStore) CreatePolicy(context.Context, domain.Policy) (domain.Policy, error) {
	panic("unused")
}
func (f *fakeStore) UpdatePolicy(context.Context, domain.Policy) (domain.Policy, error) {
	panic("unused")
}
func (f *fakeStore) DeletePolicy(context.Context, string) error { panic("unused") }
func (f *fakeStore) ListPolicies(context.Context, storage.ListPage) ([]domain.Policy, string, error) {
	panic("unused")
}
func (f *fakeStore) PoliciesForSubject(context.Context, string) ([]domain.Policy, error) {
	panic("unused")
}
func (f *fakeStore) AppendAudit(context.Context, domain.AuditEvent) error { panic("unused") }
func (f *fakeStore) ListAudit(context.Context, domain.AuditFilter, storage.ListPage) ([]domain.AuditEvent, string, error) {
	panic("unused")
}

// --- helpers ---

// ref and sel build a resource ref and a watch selector in the standard test
// namespace "prod/app" unless another namespace is given.
func ref(env, app, key string) domain.Ref {
	return domain.Ref{NS: domain.NamespaceRef{Env: env, App: app}, Key: key}
}

func sel(env, app, keyPattern string) domain.WatchSelector {
	return domain.WatchSelector{NS: domain.NamespaceRef{Env: env, App: app}, KeyPattern: keyPattern}
}

func paramPut(rev uint64, r domain.Ref, value string) domain.ChangeLogEntry {
	return domain.ChangeLogEntry{
		Revision:     rev,
		ResourceType: domain.ResourceParameter,
		Ref:          r,
		ChangeType:   domain.ChangePut,
		Value:        value,
		ContentType:  "string",
		Version:      rev,
		CreatedAt:    time.Unix(int64(rev), 0).UTC(),
	}
}

func secretPut(rev uint64, r domain.Ref) domain.ChangeLogEntry {
	return domain.ChangeLogEntry{
		Revision:     rev,
		ResourceType: domain.ResourceSecret,
		Ref:          r,
		ChangeType:   domain.ChangePut,
		Version:      rev,
		CreatedAt:    time.Unix(int64(rev), 0).UTC(),
	}
}

func newTestHub(t *testing.T, store storage.Store, opts Options) *Hub {
	t.Helper()
	return NewHub(store, zap.NewNop(), opts)
}

// runHub starts the hub loop and returns a stop func that cancels and waits.
func runHub(t *testing.T, h *Hub) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = h.Run(ctx)
		close(done)
	}()
	// Wait until the loop has captured its initial cursor so that entries
	// appended afterward are delivered live rather than treated as history.
	select {
	case <-h.Started():
	case <-time.After(2 * time.Second):
		t.Fatal("hub did not start")
	}
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("hub Run did not return after cancel")
		}
	}
}

// collect reads up to n events (or times out) from a subscription.
func collect(t *testing.T, sub *Subscription, n int, timeout time.Duration) []domain.ChangeLogEntry {
	t.Helper()
	var out []domain.ChangeLogEntry
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case e := <-sub.Events():
			out = append(out, e)
		case <-sub.Done():
			t.Fatalf("subscription closed after %d/%d events", len(out), n)
		case <-deadline:
			t.Fatalf("timed out after %d/%d events", len(out), n)
		}
	}
	return out
}

func allow(string, domain.Ref) bool { return true }

// --- tests ---

func TestSubscribe_SnapshotForFreshSubscriber(t *testing.T) {
	store := &fakeStore{
		snapshot: []domain.Parameter{
			{Ref: ref("prod", "app", "alpha/x"), Value: "1"},
			{Ref: ref("prod", "app", "beta/y"), Value: "2"},
		},
		snapRev: 7,
	}
	h := newTestHub(t, store, Options{})
	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors: []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		Allowed:   allow,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	bl := sub.Backlog()
	if !bl.IsSnapshot {
		t.Fatal("expected snapshot backlog for fresh subscriber (last_seen=0)")
	}
	if bl.Revision != 7 {
		t.Fatalf("snapshot revision = %d, want 7", bl.Revision)
	}
	if len(bl.Snapshot) != 1 || bl.Snapshot[0].Ref.Key != "alpha/x" {
		t.Fatalf("snapshot = %+v, want only alpha/x", bl.Snapshot)
	}
}

func TestSubscribe_SnapshotFiltersByNamespace(t *testing.T) {
	// Same key pattern, different namespace: the selector must not match.
	store := &fakeStore{
		snapshot: []domain.Parameter{
			{Ref: ref("prod", "app", "alpha/x"), Value: "1"},
			{Ref: ref("prod", "other", "alpha/x"), Value: "2"},
		},
		snapRev: 4,
	}
	h := newTestHub(t, store, Options{})
	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors: []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		Allowed:   allow,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	bl := sub.Backlog()
	if len(bl.Snapshot) != 1 || bl.Snapshot[0].Ref.NS.App != "app" {
		t.Fatalf("snapshot = %+v, want only prod/app/alpha/x", bl.Snapshot)
	}
}

func TestSubscribe_SnapshotFiltersByAuthz(t *testing.T) {
	store := &fakeStore{
		snapshot: []domain.Parameter{
			{Ref: ref("prod", "app", "alpha/x"), Value: "1"},
			{Ref: ref("prod", "app", "alpha/secret"), Value: "2"},
		},
		snapRev: 3,
	}
	h := newTestHub(t, store, Options{})
	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors: []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		Allowed: func(_ string, r domain.Ref) bool {
			return r.Key != "alpha/secret"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	bl := sub.Backlog()
	if len(bl.Snapshot) != 1 || bl.Snapshot[0].Ref.Key != "alpha/x" {
		t.Fatalf("snapshot = %+v, want only alpha/x (authz filtered)", bl.Snapshot)
	}
}

func TestSubscribe_ReplayForRecentSubscriber(t *testing.T) {
	store := &fakeStore{}
	store.append(
		paramPut(1, ref("prod", "app", "alpha/x"), "1"),
		paramPut(2, ref("prod", "app", "alpha/y"), "2"),
		paramPut(3, ref("prod", "app", "beta/z"), "3"),
	)
	h := newTestHub(t, store, Options{})
	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors:        []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		LastSeenRevision: 1,
		Allowed:          allow,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	bl := sub.Backlog()
	if bl.IsSnapshot {
		t.Fatal("expected replay backlog")
	}
	if bl.Revision != 3 {
		t.Fatalf("replay revision = %d, want 3", bl.Revision)
	}
	if len(bl.Replay) != 1 || bl.Replay[0].Ref.Key != "alpha/y" {
		t.Fatalf("replay = %+v, want only alpha/y (rev>1, matches alpha/*)", bl.Replay)
	}
}

func TestSubscribe_PrunedLogFallsBackToSnapshot(t *testing.T) {
	store := &fakeStore{
		oldest:   50, // entries 1..49 pruned
		snapshot: []domain.Parameter{{Ref: ref("prod", "app", "alpha/x"), Value: "1"}},
		snapRev:  60,
	}
	store.append(paramPut(60, ref("prod", "app", "alpha/x"), "1"))
	h := newTestHub(t, store, Options{})
	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors:        []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		LastSeenRevision: 10, // older than oldest retained (50)
		Allowed:          allow,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if !sub.Backlog().IsSnapshot {
		t.Fatal("expected snapshot fallback for pruned log")
	}
}

// TestSubscribe_PruneRacingReplayFallsBackToSnapshot exercises the TOCTOU
// window: canReplay sees a retention boundary that permits replay (oldest ==
// lastSeen+1), but by the time the log is read a prune has removed
// lastSeen+1..14, so the first available entry (revision 15) is beyond
// lastSeen+1. Replaying would silently skip 11..14, so the hub must fall back
// to a snapshot instead.
func TestSubscribe_PruneRacingReplayFallsBackToSnapshot(t *testing.T) {
	store := &fakeStore{
		oldest:   11, // canReplay: oldest(11) <= lastSeen+1(11) -> replay permitted
		snapshot: []domain.Parameter{{Ref: ref("prod", "app", "alpha/x"), Value: "1"}},
		snapRev:  20,
	}
	// The retained log starts at 15 (11..14 were pruned after canReplay checked).
	store.append(
		paramPut(15, ref("prod", "app", "alpha/x"), "1"),
		paramPut(20, ref("prod", "app", "alpha/y"), "2"),
	)
	h := newTestHub(t, store, Options{})
	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors:        []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		LastSeenRevision: 10,
		Allowed:          allow,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if !sub.Backlog().IsSnapshot {
		t.Fatal("expected snapshot fallback when a prune creates a replay gap")
	}
}

func TestSubscribe_TooManyToReplayFallsBackToSnapshot(t *testing.T) {
	store := &fakeStore{
		oldest:   1,
		snapshot: []domain.Parameter{{Ref: ref("prod", "app", "alpha/x"), Value: "1"}},
		snapRev:  10000,
	}
	store.append(paramPut(10000, ref("prod", "app", "alpha/x"), "1"))
	h := newTestHub(t, store, Options{SnapshotMaxReplay: 100})
	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors:        []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		LastSeenRevision: 5, // 10000-5 > 100
		Allowed:          allow,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if !sub.Backlog().IsSnapshot {
		t.Fatal("expected snapshot fallback when replay window exceeded")
	}
}

func TestSubscribe_UpToDateSubscriberGetsEmptyReplay(t *testing.T) {
	store := &fakeStore{}
	store.append(
		paramPut(1, ref("prod", "app", "alpha/x"), "1"),
		paramPut(2, ref("prod", "app", "alpha/y"), "2"),
	)
	h := newTestHub(t, store, Options{})
	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors:        []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		LastSeenRevision: 2, // current
		Allowed:          allow,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	bl := sub.Backlog()
	if bl.IsSnapshot {
		t.Fatal("up-to-date subscriber should replay (empty), not snapshot")
	}
	if len(bl.Replay) != 0 {
		t.Fatalf("expected empty replay, got %+v", bl.Replay)
	}
	if bl.Revision != 2 {
		t.Fatalf("replay revision = %d, want 2", bl.Revision)
	}
}

func TestSubscribe_EmptyLogFreshSubscriberSnapshots(t *testing.T) {
	store := &fakeStore{} // empty log, oldest = 0
	h := newTestHub(t, store, Options{})
	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors:        []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		LastSeenRevision: 5, // claims a revision but log is empty
		Allowed:          allow,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if !sub.Backlog().IsSnapshot {
		t.Fatal("empty log must fall back to snapshot")
	}
}

func TestDispatch_DeliversInRevisionOrder(t *testing.T) {
	store := &fakeStore{snapRev: 0}
	h := newTestHub(t, store, Options{})
	stop := runHub(t, h)
	defer stop()

	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors: []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		Allowed:   allow,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	store.append(
		paramPut(1, ref("prod", "app", "alpha/x"), "1"),
		paramPut(2, ref("prod", "app", "alpha/y"), "2"),
		paramPut(3, ref("prod", "app", "alpha/z"), "3"),
	)
	h.Wake()

	got := collect(t, sub, 3, time.Second)
	for i, e := range got {
		if e.Revision != uint64(i+1) {
			t.Fatalf("event %d revision = %d, want %d", i, e.Revision, i+1)
		}
	}
}

func TestDispatch_FiltersBySelectorAndAuthz(t *testing.T) {
	store := &fakeStore{}
	h := newTestHub(t, store, Options{})
	stop := runHub(t, h)
	defer stop()

	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors: []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		Allowed: func(_ string, r domain.Ref) bool {
			return r.Key != "alpha/denied"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	store.append(
		paramPut(1, ref("prod", "app", "beta/x"), "1"),      // key pattern miss
		paramPut(2, ref("prod", "other", "alpha/x"), "2"),   // namespace miss
		paramPut(3, ref("prod", "app", "alpha/denied"), ""), // authz miss
		paramPut(4, ref("prod", "app", "alpha/ok"), "4"),    // delivered
	)
	h.Wake()

	got := collect(t, sub, 1, time.Second)
	if got[0].Ref.Key != "alpha/ok" || got[0].Revision != 4 {
		t.Fatalf("delivered %+v, want alpha/ok rev 4", got[0])
	}
}

func TestDispatch_SecretEventsDelivered(t *testing.T) {
	store := &fakeStore{}
	h := newTestHub(t, store, Options{})
	stop := runHub(t, h)
	defer stop()

	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors: []domain.WatchSelector{sel("prod", "svc", "*")},
		Allowed:   allow,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	store.append(secretPut(1, ref("prod", "svc", "db")))
	h.Wake()

	got := collect(t, sub, 1, time.Second)
	if got[0].ResourceType != domain.ResourceSecret || got[0].Value != "" {
		t.Fatalf("secret event = %+v, want secret type and no value", got[0])
	}
}

func TestWake_Coalesces(t *testing.T) {
	store := &fakeStore{}
	h := newTestHub(t, store, Options{})
	// Fill the wake channel, then hammer Wake; must never block.
	for i := 0; i < 1000; i++ {
		h.Wake()
	}
	// The buffered channel holds exactly one pending wake.
	select {
	case <-h.wake:
	default:
		t.Fatal("expected one coalesced wake pending")
	}
	select {
	case <-h.wake:
		t.Fatal("expected only one coalesced wake")
	default:
	}
}

func TestDispatch_NoDuplicateAcrossBacklogBoundary(t *testing.T) {
	// Subscriber replays up to current, then live events must not repeat any
	// revision already in the backlog.
	store := &fakeStore{}
	store.append(
		paramPut(1, ref("prod", "app", "alpha/x"), "1"),
		paramPut(2, ref("prod", "app", "alpha/y"), "2"),
	)
	h := newTestHub(t, store, Options{})
	stop := runHub(t, h)
	defer stop()

	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors:        []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		LastSeenRevision: 0,
		Allowed:          allow,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	// Fresh subscriber -> snapshot at rev 2. Now push rev 3 live.
	bl := sub.Backlog()
	if bl.Revision != 2 {
		t.Fatalf("backlog revision = %d, want 2", bl.Revision)
	}
	store.append(paramPut(3, ref("prod", "app", "alpha/z"), "3"))
	h.Wake()

	got := collect(t, sub, 1, time.Second)
	if got[0].Revision != 3 {
		t.Fatalf("first live event revision = %d, want 3 (no replay of <=2)", got[0].Revision)
	}
}

func TestSlowSubscriberDropped(t *testing.T) {
	store := &fakeStore{}
	h := newTestHub(t, store, Options{SubscriberBuffer: 2})
	stop := runHub(t, h)
	defer stop()

	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors: []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		Allowed:   allow,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Push more events than the buffer holds without consuming any.
	var entries []domain.ChangeLogEntry
	for i := uint64(1); i <= 20; i++ {
		entries = append(entries, paramPut(i, ref("prod", "app", "alpha/x"), "v"))
	}
	store.append(entries...)
	h.Wake()

	select {
	case <-sub.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("slow subscriber was not dropped")
	}
	// Dropped subscribers must leave the registry.
	waitFor(t, func() bool { return len(h.Subscribers()) == 0 }, time.Second)
}

func TestLivenessExpiry(t *testing.T) {
	now := time.Now().UTC()
	clock := &fakeClock{t: now}
	store := &fakeStore{}
	h := newTestHub(t, store, Options{
		HeartbeatInterval: 20 * time.Millisecond,
		MissedHeartbeats:  3,
		now:               clock.now,
	})
	stop := runHub(t, h)
	defer stop()

	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors: []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		Allowed:   allow,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Advance the clock past the liveness window without any ack.
	clock.advance(200 * time.Millisecond)

	select {
	case <-sub.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("stale subscriber not expired")
	}
}

func TestLiveness_AckKeepsAlive(t *testing.T) {
	now := time.Now().UTC()
	clock := &fakeClock{t: now}
	store := &fakeStore{}
	h := newTestHub(t, store, Options{
		HeartbeatInterval: 20 * time.Millisecond,
		MissedHeartbeats:  3,
		now:               clock.now,
	})
	stop := runHub(t, h)
	defer stop()

	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors: []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		Allowed:   allow,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	// Keep acking within the window; must stay alive.
	for i := 0; i < 5; i++ {
		clock.advance(10 * time.Millisecond)
		sub.Ack(uint64(i))
		time.Sleep(10 * time.Millisecond)
		select {
		case <-sub.Done():
			t.Fatal("subscriber dropped despite acks")
		default:
		}
	}
}

func TestPruneLoop(t *testing.T) {
	store := &fakeStore{}
	h := newTestHub(t, store, Options{PruneInterval: 15 * time.Millisecond})
	stop := runHub(t, h)
	defer stop()
	waitFor(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.pruneCalls >= 2
	}, time.Second)
}

func TestSubscribersRegistry(t *testing.T) {
	store := &fakeStore{}
	h := newTestHub(t, store, Options{})
	selector := sel("prod", "app", "alpha/*")
	sub, err := h.Subscribe(context.Background(), Registration{
		ClientName: "app",
		InstanceID: "app-abcd",
		Identity:   "id-1",
		RemoteAddr: "1.2.3.4:5",
		Selectors:  []domain.WatchSelector{selector},
		Allowed:    allow,
	})
	if err != nil {
		t.Fatal(err)
	}
	subs := h.Subscribers()
	if len(subs) != 1 {
		t.Fatalf("registry size = %d, want 1", len(subs))
	}
	got := subs[0]
	if got.ClientName != "app" || got.InstanceID != "app-abcd" || got.Identity != "id-1" {
		t.Fatalf("registry record = %+v", got)
	}
	if len(got.Selectors) != 1 || got.Selectors[0] != selector {
		t.Fatalf("selectors = %+v", got.Selectors)
	}
	// Mutating the returned copy must not affect the registry.
	got.Selectors[0] = sel("prod", "app", "mutated")
	if h.Subscribers()[0].Selectors[0] != selector {
		t.Fatal("Subscribers() returned an aliased slice")
	}
	sub.Ack(42)
	if h.Subscribers()[0].LastAckedRevision != 42 {
		t.Fatal("ack not reflected in registry")
	}
	sub.Close()
	if len(h.Subscribers()) != 0 {
		t.Fatal("closed subscriber still in registry")
	}
}

func TestSubscribe_BacklogStoreError(t *testing.T) {
	store := &fakeStore{currentRevErr: errors.New("db down")}
	h := newTestHub(t, store, Options{})
	_, err := h.Subscribe(context.Background(), Registration{
		Selectors: []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		Allowed:   allow,
	})
	if err == nil {
		t.Fatal("expected error when backlog computation fails")
	}
	// The failed subscriber must not linger in the registry.
	if len(h.Subscribers()) != 0 {
		t.Fatal("failed subscribe left a registry entry")
	}
}

func TestRun_ReturnsOnContextCancel(t *testing.T) {
	store := &fakeStore{}
	h := newTestHub(t, store, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return on cancel")
	}
}

func TestUpdateAllowedSwapsPredicate(t *testing.T) {
	store := &fakeStore{}
	h := newTestHub(t, store, Options{})
	stop := runHub(t, h)
	defer stop()

	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors: []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		Allowed:   allow,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	// Initially authorized: the first event is delivered.
	store.append(paramPut(1, ref("prod", "app", "alpha/x"), "1"))
	h.Wake()
	got := collect(t, sub, 1, time.Second)
	if got[0].Revision != 1 {
		t.Fatalf("first event revision = %d, want 1", got[0].Revision)
	}

	// Revoke authorization; subsequent events must be filtered out.
	sub.UpdateAllowed(func(string, domain.Ref) bool { return false })
	store.append(paramPut(2, ref("prod", "app", "alpha/y"), "2"))
	h.Wake()
	select {
	case e := <-sub.Events():
		t.Fatalf("received %+v after authorization revoked", e)
	case <-time.After(150 * time.Millisecond):
	}

	// Restore authorization; delivery resumes.
	sub.UpdateAllowed(allow)
	store.append(paramPut(3, ref("prod", "app", "alpha/z"), "3"))
	h.Wake()
	got = collect(t, sub, 1, time.Second)
	if got[0].Revision != 3 {
		t.Fatalf("resumed event revision = %d, want 3", got[0].Revision)
	}
}

func TestNoGoroutineLeak(t *testing.T) {
	baseline := runtime.NumGoroutine()
	store := &fakeStore{}
	h := newTestHub(t, store, Options{HeartbeatInterval: 5 * time.Millisecond, PruneInterval: 5 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { _ = h.Run(ctx); close(runDone) }()
	<-h.Started()

	// Churn subscribers: subscribe, deliver, then drop them.
	for i := 0; i < 25; i++ {
		sub, err := h.Subscribe(context.Background(), Registration{
			Selectors: []domain.WatchSelector{sel("prod", "app", "alpha/*")},
			Allowed:   allow,
		})
		if err != nil {
			t.Fatal(err)
		}
		store.append(paramPut(uint64(i+1), ref("prod", "app", "alpha/x"), "v"))
		h.Wake()
		sub.Close()
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}

	// Allow scheduler to reap. Goroutine count should return near baseline.
	waitFor(t, func() bool { return runtime.NumGoroutine() <= baseline+2 }, 2*time.Second)
}

func TestNilAllowedDeniesEverything(t *testing.T) {
	store := &fakeStore{}
	h := newTestHub(t, store, Options{})
	stop := runHub(t, h)
	defer stop()
	sub, err := h.Subscribe(context.Background(), Registration{
		Selectors: []domain.WatchSelector{sel("prod", "app", "alpha/*")},
		Allowed:   nil, // must be treated as deny-all
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	store.append(paramPut(1, ref("prod", "app", "alpha/x"), "1"))
	h.Wake()
	select {
	case e := <-sub.Events():
		t.Fatalf("nil predicate should deny, but delivered %+v", e)
	case <-time.After(150 * time.Millisecond):
	}
}

// --- test utilities ---

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
