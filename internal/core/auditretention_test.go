package core

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// pruneStore is an in-memory AuditPruneStore: rows keyed by id, listed oldest
// first, with switches to fail either call.
type pruneStore struct {
	mu        sync.Mutex
	rows      []domain.AuditEvent
	listErr   error
	deleteErr error
	deletes   [][]int64
}

func (p *pruneStore) ListAuditBefore(_ context.Context, before time.Time, limit int) ([]domain.AuditEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listErr != nil {
		return nil, p.listErr
	}
	var out []domain.AuditEvent
	for _, row := range p.rows {
		if row.CreatedAt.Before(before) {
			out = append(out, row)
		}
	}
	slices.SortFunc(out, func(a, b domain.AuditEvent) int { return int(a.ID - b.ID) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (p *pruneStore) DeleteAuditByIDs(_ context.Context, ids []int64) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.deleteErr != nil {
		return 0, p.deleteErr
	}
	p.deletes = append(p.deletes, slices.Clone(ids))
	var n int64
	p.rows = slices.DeleteFunc(p.rows, func(row domain.AuditEvent) bool {
		if slices.Contains(ids, row.ID) {
			n++
			return true
		}
		return false
	})
	return n, nil
}

func (p *pruneStore) CountAuditBefore(_ context.Context, before time.Time) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listErr != nil {
		return 0, p.listErr
	}
	var total int64
	for _, row := range p.rows {
		if row.CreatedAt.Before(before) {
			total++
		}
	}
	return total, nil
}

func (p *pruneStore) remaining() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := make([]int64, 0, len(p.rows))
	for _, row := range p.rows {
		ids = append(ids, row.ID)
	}
	slices.Sort(ids)
	return ids
}

type prunedRecorder struct {
	mu    sync.Mutex
	total int
	calls int
}

func (r *prunedRecorder) AuditPruned(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total += n
	r.calls++
}

func (*prunedRecorder) AuthFailure(string)        {}
func (*prunedRecorder) AuthzDenied(string)        {}
func (*prunedRecorder) AuthzMethodDenied(string)  {}
func (*prunedRecorder) RateLimited(string)        {}
func (*prunedRecorder) AuditEvent(string, string) {}
func (*prunedRecorder) AuditWriteFailed()         {}
func (*prunedRecorder) DecryptFailed()            {}
func (*prunedRecorder) ReleaseOutcome(string)     {}

var retentionNow = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// seedRows writes n rows whose creation times step back one hour each from
// newest, so row i is (n-i) hours old.
func seedRows(n int, newest time.Time) []domain.AuditEvent {
	rows := make([]domain.AuditEvent, n)
	for i := range rows {
		ev := goldenAuditEvent()
		ev.ID = int64(i + 1)
		ev.CreatedAt = newest.Add(-time.Duration(n-i) * time.Hour)
		rows[i] = ev
	}
	return rows
}

func newRetention(store *pruneStore, retain time.Duration, archive string) *AuditRetention {
	return &AuditRetention{
		Store:      store,
		Retain:     retain,
		ArchiveDir: archive,
		Now:        func() time.Time { return retentionNow },
	}
}

func readArchive(t *testing.T, path string) []AuditRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer func() { _ = f.Close() }()
	var out []AuditRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec AuditRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("archive line %q: %v", sc.Text(), err)
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAuditRetentionValidate(t *testing.T) {
	t.Parallel()
	if err := (&AuditRetention{Retain: time.Hour}).Validate(); err == nil {
		t.Error("nil store accepted")
	}
	if err := (&AuditRetention{Store: &pruneStore{}}).Validate(); err == nil {
		t.Error("zero retention accepted")
	}
	if _, err := (&AuditRetention{Store: &pruneStore{}}).RunOnce(context.Background()); err == nil {
		t.Error("RunOnce ran with zero retention")
	}
}

// TestAuditRetentionArchivesThenDeletes is the invariant: every row older than
// the cutoff ends up in a per-day archive, byte-for-byte in the shared record
// format, and only those rows are deleted.
func TestAuditRetentionArchivesThenDeletes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := &pruneStore{rows: seedRows(30, retentionNow)}
	metrics := &prunedRecorder{}
	r := newRetention(store, 12*time.Hour, dir)
	r.Metrics = metrics

	pruned, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	// Rows 1..18 are 30..13 hours old; 19..30 are 12 hours old or newer, and
	// a row exactly at the cutoff is kept (the store bound is strict).
	if pruned != 18 {
		t.Fatalf("pruned = %d, want 18", pruned)
	}
	if got := store.remaining(); !slices.Equal(got, []int64{19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30}) {
		t.Fatalf("remaining rows = %v", got)
	}
	if metrics.total != 18 || metrics.calls != 1 {
		t.Errorf("metrics = %d over %d calls, want 18 over 1", metrics.total, metrics.calls)
	}

	// 30..13 hours before 2026-09-01T12:00Z spans Aug 31 06:00 to Aug 31 23:00.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"audit-20260831.jsonl"}) {
		t.Fatalf("archive files = %v", names)
	}
	path := filepath.Join(dir, names[0])
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("archive mode = %04o, want 0600", info.Mode().Perm())
		}
	}
	records := readArchive(t, path)
	if len(records) != 18 {
		t.Fatalf("archived %d records, want 18", len(records))
	}
	for i, rec := range records {
		if rec.ID != int64(i+1) {
			t.Fatalf("record %d has id %d, want oldest first", i, rec.ID)
		}
	}
	if records[0].Event != "secret.read" || records[0].Resource.Key != "stripe-api-key" {
		t.Errorf("record fields not carried: %+v", records[0])
	}
}

// TestAuditRetentionSplitsArchiveByDay: a batch spanning midnight lands in
// two files, and a second pass appends to an existing day rather than
// replacing it.
func TestAuditRetentionSplitsArchiveByDay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Newest row at 01:00 on Sep 1; 30 rows reach back to 19:00 on Aug 30.
	newest := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	store := &pruneStore{rows: seedRows(30, newest)}
	r := newRetention(store, time.Minute, dir)
	r.Now = func() time.Time { return newest.Add(time.Hour) }

	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	aug30 := readArchive(t, filepath.Join(dir, "audit-20260830.jsonl"))
	aug31 := readArchive(t, filepath.Join(dir, "audit-20260831.jsonl"))
	sep1 := readArchive(t, filepath.Join(dir, "audit-20260901.jsonl"))
	if len(aug30)+len(aug31)+len(sep1) != 30 {
		t.Fatalf("archived %d+%d+%d records, want 30", len(aug30), len(aug31), len(sep1))
	}
	for _, rec := range aug31 {
		if d := rec.CreatedAt.UTC().Format("20060102"); d != "20260831" {
			t.Errorf("record %d created %s filed under Aug 31", rec.ID, d)
		}
	}

	// A later pass appends to the day file the earlier one created.
	store.mu.Lock()
	late := goldenAuditEvent()
	late.ID = 99
	late.CreatedAt = time.Date(2026, 8, 31, 23, 30, 0, 0, time.UTC)
	store.rows = append(store.rows, late)
	store.mu.Unlock()
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	after := readArchive(t, filepath.Join(dir, "audit-20260831.jsonl"))
	if len(after) != len(aug31)+1 || after[len(after)-1].ID != 99 {
		t.Fatalf("second pass did not append: %d records, last id %d", len(after), after[len(after)-1].ID)
	}
}

// TestAuditRetentionKeepsRowsWhenArchiveFails: an archive the retention cannot
// write means nothing is deleted. The file must not be repaired or replaced,
// and the error names the archive.
func TestAuditRetentionKeepsRowsWhenArchiveFails(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		archive func(t *testing.T) string
	}{
		{"missing directory", func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent") }},
		{"day file is a symlink", func(t *testing.T) string {
			dir := t.TempDir()
			target := filepath.Join(dir, "elsewhere.jsonl")
			if err := os.WriteFile(target, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, "audit-20260831.jsonl")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			return dir
		}},
		{"day file is group-readable", func(t *testing.T) string {
			if runtime.GOOS == "windows" {
				t.Skip("POSIX modes do not widen a Windows DACL")
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "audit-20260831.jsonl")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			// Chmod rather than a create mode: the test umask would mask it.
			if err := os.Chmod(path, 0o640); err != nil {
				t.Fatal(err)
			}
			return dir
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &pruneStore{rows: seedRows(30, retentionNow)}
			metrics := &prunedRecorder{}
			r := newRetention(store, 12*time.Hour, tc.archive(t))
			r.Metrics = metrics

			pruned, err := r.RunOnce(context.Background())
			if err == nil {
				t.Fatal("RunOnce succeeded without an archive")
			}
			if !strings.Contains(err.Error(), "archive") {
				t.Errorf("error %v does not mention the archive", err)
			}
			if pruned != 0 || len(store.deletes) != 0 || len(store.remaining()) != 30 {
				t.Fatalf("rows were deleted: pruned=%d deletes=%v remaining=%d", pruned, store.deletes, len(store.remaining()))
			}
			if metrics.calls != 0 {
				t.Errorf("AuditPruned reported %d rows on a failed pass", metrics.total)
			}
		})
	}
}

// TestAuditRetentionWithoutArchiveDeletes: no archive directory means the
// rows are simply retired — the operator chose that at configuration time.
func TestAuditRetentionWithoutArchiveDeletes(t *testing.T) {
	t.Parallel()
	store := &pruneStore{rows: seedRows(30, retentionNow)}
	r := newRetention(store, 12*time.Hour, "")
	pruned, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if pruned != 18 || len(store.remaining()) != 12 {
		t.Fatalf("pruned=%d remaining=%d", pruned, len(store.remaining()))
	}
}

// TestAuditRetentionBatches: a backlog larger than one batch is drained in
// batch-sized steps within a single pass, each archived before it is deleted,
// and a pass stops at the batch cap so the write lock is not held for a
// backlog of years.
func TestAuditRetentionBatches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const rows = auditPruneBatch*2 + 5
	// One row per minute keeps every row well past a one-hour retention. The
	// store deletes in place, so each store gets its own slice.
	backlog := func() []domain.AuditEvent {
		all := make([]domain.AuditEvent, rows)
		for i := range all {
			ev := goldenAuditEvent()
			ev.ID = int64(i + 1)
			ev.CreatedAt = retentionNow.Add(-time.Duration(rows-i+120) * time.Minute)
			all[i] = ev
		}
		return all
	}
	store := &pruneStore{rows: backlog()}
	r := newRetention(store, time.Hour, dir)

	pruned, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if pruned != rows {
		t.Fatalf("pruned = %d, want %d", pruned, rows)
	}
	if got := []int{len(store.deletes[0]), len(store.deletes[1]), len(store.deletes[2])}; !slices.Equal(got, []int{auditPruneBatch, auditPruneBatch, 5}) {
		t.Fatalf("delete batches = %v", got)
	}
	var archived int
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		archived += len(readArchive(t, filepath.Join(dir, e.Name())))
	}
	if archived != rows {
		t.Fatalf("archived %d records, want %d", archived, rows)
	}

	// A failure on the second batch keeps the first batch's count and the
	// second batch's rows.
	store = &pruneStore{rows: backlog()}
	r = newRetention(store, time.Hour, "")
	calls := 0
	failing := &flakyPruneStore{pruneStore: store, failOn: 2, calls: &calls}
	r.Store = failing
	pruned, err = r.RunOnce(context.Background())
	if err == nil {
		t.Fatal("RunOnce ignored a delete failure")
	}
	if pruned != auditPruneBatch || len(store.remaining()) != rows-auditPruneBatch {
		t.Fatalf("pruned=%d remaining=%d after a failed second batch", pruned, len(store.remaining()))
	}
}

// flakyPruneStore fails the failOn-th delete.
type flakyPruneStore struct {
	*pruneStore
	failOn int
	calls  *int
}

func (f *flakyPruneStore) DeleteAuditByIDs(ctx context.Context, ids []int64) (int64, error) {
	*f.calls++
	if *f.calls == f.failOn {
		return 0, errors.New("disk full")
	}
	return f.pruneStore.DeleteAuditByIDs(ctx, ids)
}

// TestAuditRetentionRunStopsWithContext: Run performs a pass right away and
// returns once its context ends.
func TestAuditRetentionRunStopsWithContext(t *testing.T) {
	t.Parallel()
	store := &pruneStore{rows: seedRows(3, retentionNow)}
	r := newRetention(store, time.Minute, "")
	r.Interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(store.remaining()) != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(store.remaining()) != 0 {
		t.Fatal("Run did not perform its first pass immediately")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}
