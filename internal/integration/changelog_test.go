package integration

import (
	"context"
	"testing"
	"time"
)

// §8.3 / §25.2 — writes produce monotonically increasing revisions, changes are
// replayable in order, and pruning respects retention without ever reusing a
// revision number.
func TestChangeLog(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	start, err := h.store.CurrentRevision(ctx)
	if err != nil {
		t.Fatalf("CurrentRevision: %v", err)
	}

	var revs []uint64
	for _, p := range []string{"/prod/app/a", "/prod/app/b", "/prod/app/c", "/prod/app/d"} {
		_, rev, err := h.svc.PutParameter(ctx, h.admin, h.ensureNS(p), "v", "string", "")
		if err != nil {
			t.Fatalf("PutParameter %s: %v", p, err)
		}
		revs = append(revs, rev)
	}
	for i := 1; i < len(revs); i++ {
		if revs[i] <= revs[i-1] {
			t.Errorf("revisions not strictly increasing: %v", revs)
		}
	}

	// ListChangesSince returns entries after `start`, ascending.
	changes, err := h.store.ListChangesSince(ctx, start, 1000)
	if err != nil {
		t.Fatalf("ListChangesSince: %v", err)
	}
	if len(changes) < len(revs) {
		t.Fatalf("got %d changes, want >= %d", len(changes), len(revs))
	}
	for i := 1; i < len(changes); i++ {
		if changes[i].Revision <= changes[i-1].Revision {
			t.Errorf("change revisions not ascending at %d: %d then %d", i, changes[i-1].Revision, changes[i].Revision)
		}
	}

	maxRev, err := h.store.CurrentRevision(ctx)
	if err != nil {
		t.Fatalf("CurrentRevision: %v", err)
	}

	// Prune keeping only the single newest entry (retention window is generous,
	// so the maxRows policy is what bites).
	total := len(changes)
	deleted, err := h.store.PruneChangeLog(ctx, time.Hour, 1)
	if err != nil {
		t.Fatalf("PruneChangeLog: %v", err)
	}
	if deleted != total-1 {
		t.Errorf("pruned %d rows, want %d", deleted, total-1)
	}
	// CurrentRevision survives pruning (read from sqlite_sequence).
	if got, _ := h.store.CurrentRevision(ctx); got != maxRev {
		t.Errorf("CurrentRevision after prune = %d, want %d", got, maxRev)
	}
	oldest, err := h.store.OldestRetainedRevision(ctx)
	if err != nil {
		t.Fatalf("OldestRetainedRevision: %v", err)
	}
	if oldest != maxRev {
		t.Errorf("oldest retained = %d, want %d (only newest kept)", oldest, maxRev)
	}

	// A subsequent write must not reuse a pruned revision.
	_, nextRev, err := h.svc.PutParameter(ctx, h.admin, h.ensureNS("/prod/app/e"), "v", "string", "")
	if err != nil {
		t.Fatalf("PutParameter after prune: %v", err)
	}
	if nextRev <= maxRev {
		t.Errorf("post-prune revision = %d, want > %d (no reuse)", nextRev, maxRev)
	}
}
