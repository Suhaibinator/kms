package watch

import (
	"context"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestStats_EmptyHub(t *testing.T) {
	h := newTestHub(t, &fakeStore{}, Options{})
	if got := h.Stats(); got != (Stats{}) {
		t.Fatalf("Stats on an idle hub = %+v, want the zero value", got)
	}
}

// TestStats_CountsBothRegistries keeps the two subscriber kinds apart and makes
// sure both shrink as streams end — a registry that only grows would show
// phantom clients on a dashboard forever.
func TestStats_CountsBothRegistries(t *testing.T) {
	ctx := context.Background()
	st, ns := releaseWatchStore(t)
	release := createWatchRelease(t, st, ns, "one")
	activateWatchRelease(t, st, ns, release.Version)
	reg := releaseWatchRegistration(t, st, ns)

	h := NewHub(st, nil, Options{})
	namespaceIDs := map[domain.NamespaceRef]int64{ns: reg.NamespaceID}
	first, err := h.Subscribe(ctx, Registration{Namespaces: []domain.NamespaceRef{ns}, NamespaceIDs: namespaceIDs})
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.Subscribe(ctx, Registration{Namespaces: []domain.NamespaceRef{ns}, NamespaceIDs: namespaceIDs})
	if err != nil {
		t.Fatal(err)
	}
	releaseSub, err := h.SubscribeRelease(ctx, reg)
	if err != nil {
		t.Fatal(err)
	}

	stats := h.Stats()
	if stats.Subscribers != 2 || stats.ReleaseSubscribers != 1 {
		t.Fatalf("Stats = %+v, want 2 subscribers and 1 release subscriber", stats)
	}

	first.Close()
	releaseSub.Close()
	stats = h.Stats()
	if stats.Subscribers != 1 || stats.ReleaseSubscribers != 0 {
		t.Fatalf("after closing = %+v, want 1 subscriber and 0 release subscribers", stats)
	}
	second.Close()
	if stats := h.Stats(); stats.Subscribers != 0 {
		t.Fatalf("after closing everything = %+v", stats)
	}
}

// TestStats_LagIsMeasuredFromTheSlowestSubscriber is the point of the lag
// gauge: it has to track the worst client, because that is the one about to be
// dropped, not the average.
func TestStats_LagIsMeasuredFromTheSlowestSubscriber(t *testing.T) {
	store := &fakeStore{}
	h := newTestHub(t, store, Options{})
	stop := runHub(t, h)
	defer stop()

	namespace := nsr("prod", "app")
	fast, err := h.Subscribe(context.Background(), Registration{Namespaces: []domain.NamespaceRef{namespace}})
	if err != nil {
		t.Fatal(err)
	}
	defer fast.Close()
	slow, err := h.Subscribe(context.Background(), Registration{Namespaces: []domain.NamespaceRef{namespace}})
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()

	var entries []domain.ChangeLogEntry
	for i := uint64(1); i <= 10; i++ {
		entries = append(entries, paramPut(i, ref("prod", "app", "alpha/x"), "v"))
	}
	store.append(entries...)
	h.Wake()
	waitFor(t, func() bool { return h.Stats().LastDispatchedRevision == 10 }, 2*time.Second)

	// No acks yet: both subscribers trail the whole log.
	if got := h.Stats().MaxLagRevisions; got != 10 {
		t.Fatalf("MaxLagRevisions before any ack = %d, want 10", got)
	}

	fast.Ack(10)
	slow.Ack(4)
	if got := h.Stats().MaxLagRevisions; got != 6 {
		t.Fatalf("MaxLagRevisions = %d, want 6 (cursor 10 - slowest ack 4)", got)
	}

	// The lag follows the slowest, so it only clears when the laggard catches
	// up — not when the fast one does.
	slow.Ack(10)
	if got := h.Stats().MaxLagRevisions; got != 0 {
		t.Fatalf("MaxLagRevisions after both caught up = %d, want 0", got)
	}
}

// TestStats_LagNeverGoesNegative covers a subscriber acking a backlog revision
// the dispatch loop has not reached: the gauge must read 0, not wrap around.
func TestStats_LagNeverGoesNegative(t *testing.T) {
	store := &fakeStore{}
	h := newTestHub(t, store, Options{})
	sub, err := h.Subscribe(context.Background(), Registration{
		Namespaces: []domain.NamespaceRef{nsr("prod", "app")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	sub.Ack(500)
	if got := h.Stats(); got.MaxLagRevisions != 0 {
		t.Fatalf("MaxLagRevisions = %d, want 0 with an ack ahead of the cursor", got.MaxLagRevisions)
	}
}

// TestStats_CursorTracksDispatch pins the cursor to the dispatch position: it
// starts at the revision the log was already at (earlier entries are backlog,
// not live events) and advances as entries are fanned out.
func TestStats_CursorTracksDispatch(t *testing.T) {
	store := &fakeStore{}
	store.append(paramPut(1, ref("prod", "app", "alpha/x"), "v"), paramPut(2, ref("prod", "app", "alpha/y"), "v"))
	h := newTestHub(t, store, Options{})
	stop := runHub(t, h)
	defer stop()

	if got := h.Stats().LastDispatchedRevision; got != 2 {
		t.Fatalf("LastDispatchedRevision at start = %d, want 2 (history is backlog)", got)
	}

	store.append(paramPut(3, ref("prod", "app", "alpha/z"), "v"))
	h.Wake()
	waitFor(t, func() bool { return h.Stats().LastDispatchedRevision == 3 }, 2*time.Second)
}

// TestStats_CountsSlowDrops forces the buffer-overflow path and checks the drop
// is attributed to "slow", not to liveness.
func TestStats_CountsSlowDrops(t *testing.T) {
	store := &fakeStore{}
	h := newTestHub(t, store, Options{SubscriberBuffer: 2})
	stop := runHub(t, h)
	defer stop()

	sub, err := h.Subscribe(context.Background(), Registration{
		Namespaces: []domain.NamespaceRef{nsr("prod", "app")},
	})
	if err != nil {
		t.Fatal(err)
	}

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
	waitFor(t, func() bool { return h.Stats().DroppedSlow == 1 }, time.Second)
	if got := h.Stats(); got.DroppedStale != 0 {
		t.Fatalf("DroppedStale = %d, want 0 (this was a buffer overflow)", got.DroppedStale)
	}
}

// TestStats_CountsStaleDrops forces the liveness path and checks the drop is
// attributed to "stale".
func TestStats_CountsStaleDrops(t *testing.T) {
	clock := &fakeClock{t: time.Now().UTC()}
	store := &fakeStore{}
	h := newTestHub(t, store, Options{
		HeartbeatInterval: 20 * time.Millisecond,
		MissedHeartbeats:  3,
		now:               clock.now,
	})
	stop := runHub(t, h)
	defer stop()

	sub, err := h.Subscribe(context.Background(), Registration{
		Namespaces: []domain.NamespaceRef{nsr("prod", "app")},
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(200 * time.Millisecond)

	select {
	case <-sub.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("stale subscriber not expired")
	}
	waitFor(t, func() bool { return h.Stats().DroppedStale == 1 }, time.Second)
	if got := h.Stats(); got.DroppedSlow != 0 {
		t.Fatalf("DroppedSlow = %d, want 0 (this was a missed heartbeat)", got.DroppedSlow)
	}
}

// TestStats_ClientDisconnectIsNotADrop keeps the counters meaningful as an
// alert: a client that hangs up on its own is not a subscriber the hub had to
// tear down, even though dispatch sees the same "offer failed" result.
func TestStats_ClientDisconnectIsNotADrop(t *testing.T) {
	store := &fakeStore{}
	h := newTestHub(t, store, Options{})
	stop := runHub(t, h)
	defer stop()

	sub, err := h.Subscribe(context.Background(), Registration{
		Namespaces: []domain.NamespaceRef{nsr("prod", "app")},
	})
	if err != nil {
		t.Fatal(err)
	}
	sub.Close()

	store.append(paramPut(1, ref("prod", "app", "alpha/x"), "v"))
	h.Wake()
	waitFor(t, func() bool { return h.Stats().LastDispatchedRevision == 1 }, 2*time.Second)

	if got := h.Stats(); got.DroppedSlow != 0 || got.DroppedStale != 0 {
		t.Fatalf("Stats = %+v, want no drops after a client-initiated close", got)
	}
}
