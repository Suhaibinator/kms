package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"go.uber.org/zap"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/fileutil"
	"github.com/Suhaibinator/kms/internal/storage"
)

const (
	// auditPruneBatch is how many rows one archive-then-delete step handles.
	// It is also the store's listing cap, so a larger value would be clamped.
	auditPruneBatch = 1000
	// auditPruneMaxBatches bounds one pass: a backlog of years is drained over
	// several passes rather than in one long-running one that holds the write
	// lock for the duration.
	auditPruneMaxBatches = 100
	// DefaultAuditRetentionInterval is how often the background loop runs.
	DefaultAuditRetentionInterval = 5 * time.Minute
)

// AuditRetention retires audit rows older than Retain, archiving every batch
// as JSON Lines before it is deleted. The invariant it exists to keep is
// archive-before-delete: a row is removed only after the file holding it has
// been synced, so an unwritable archive directory means the rows stay — the
// pass fails and the log says why — never that history quietly disappears.
//
// It works on the store's AuditPruneStore capability directly rather than
// through Service, because the offline `audit prune` command runs it against
// a database no service is serving.
type AuditRetention struct {
	// Store supplies and deletes the rows.
	Store storage.AuditPruneStore
	// Retain is how long a row is kept. It must be positive; a zero retention
	// means "keep forever" and belongs to the caller that decided not to build
	// an AuditRetention at all.
	Retain time.Duration
	// ArchiveDir, when set, receives one audit-<YYYYMMDD>.jsonl file per UTC
	// day of event creation, in the shared AuditRecord format. Empty means
	// delete without archiving.
	ArchiveDir string
	// Interval paces Run; zero means DefaultAuditRetentionInterval.
	Interval time.Duration
	// Metrics receives AuditPruned; nil means no exporter.
	Metrics Metrics
	// Logger is optional.
	Logger *zap.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Validate reports a misconfigured retention before any row is touched.
func (r *AuditRetention) Validate() error {
	if r.Store == nil {
		return errors.New("audit retention: store does not support pruning")
	}
	if r.Retain <= 0 {
		return errors.New("audit retention: retain duration must be positive")
	}
	return nil
}

// Cutoff is the instant before which rows are retired on the next pass.
func (r *AuditRetention) Cutoff() time.Time {
	return r.now().Add(-r.Retain)
}

// RunOnce performs one pass: up to auditPruneMaxBatches batches of
// auditPruneBatch rows, each archived (when ArchiveDir is set), synced, and
// only then deleted. It returns how many rows were deleted. On error the count
// covers the batches that completed; the batch that failed is untouched and
// will be retried on the next pass, so an archive consumer must be prepared to
// see a record twice (records carry their id for exactly that reason).
func (r *AuditRetention) RunOnce(ctx context.Context) (int64, error) {
	if err := r.Validate(); err != nil {
		return 0, err
	}
	cutoff := r.Cutoff()
	var pruned int64
	for range auditPruneMaxBatches {
		if err := ctx.Err(); err != nil {
			return pruned, err
		}
		rows, err := r.Store.ListAuditBefore(ctx, cutoff, auditPruneBatch)
		if err != nil {
			return pruned, fmt.Errorf("audit retention: listing rows before %s: %w", cutoff.UTC().Format(time.RFC3339), err)
		}
		if len(rows) == 0 {
			break
		}
		if r.ArchiveDir != "" {
			if err := r.archive(rows); err != nil {
				return pruned, err
			}
		}
		ids := make([]int64, len(rows))
		for i, row := range rows {
			ids[i] = row.ID
		}
		n, err := r.Store.DeleteAuditByIDs(ctx, ids)
		if err != nil {
			return pruned, fmt.Errorf("audit retention: deleting %d archived rows: %w", len(ids), err)
		}
		pruned += n
		if r.Metrics != nil && n > 0 {
			r.Metrics.AuditPruned(int(n))
		}
		if len(rows) < auditPruneBatch {
			break
		}
	}
	return pruned, nil
}

// Run performs a pass immediately and then every Interval until ctx is done.
// A failed pass is logged and retried at the next tick; it never stops the
// loop, because the condition that made it fail (a full disk, a permissions
// change) is exactly the kind an operator fixes while the server keeps running.
func (r *AuditRetention) Run(ctx context.Context) {
	interval := r.Interval
	if interval <= 0 {
		interval = DefaultAuditRetentionInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		r.runLogged(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *AuditRetention) runLogged(ctx context.Context) {
	pruned, err := r.RunOnce(ctx)
	if r.Logger == nil {
		return
	}
	switch {
	case err != nil && ctx.Err() != nil:
		// Shutting down mid-pass is not an incident.
	case err != nil:
		r.Logger.Error("audit retention pass failed", zap.Int64("pruned", pruned), zap.Error(err))
	case pruned > 0:
		r.Logger.Info("audit retention pruned rows", zap.Int64("pruned", pruned), zap.String("archive_dir", r.ArchiveDir))
	}
}

// archive appends rows to their per-day archive files and syncs each file
// before returning. rows arrive oldest first, so a day's records form one
// contiguous run and each file is opened once per batch.
func (r *AuditRetention) archive(rows []domain.AuditEvent) error {
	for start := 0; start < len(rows); {
		day := archiveDay(rows[start])
		end := start + 1
		for end < len(rows) && archiveDay(rows[end]) == day {
			end++
		}
		if err := r.appendArchive(day, rows[start:end]); err != nil {
			return err
		}
		start = end
	}
	return nil
}

// appendArchive writes one day's records as a single write so that a crash
// leaves either the whole run or none of it; the file is synced before the
// caller may delete the rows it holds.
func (r *AuditRetention) appendArchive(day string, rows []domain.AuditEvent) error {
	records := make([]AuditRecord, len(rows))
	for i, row := range rows {
		records[i] = AuditRecordFrom(row)
	}
	var buf bytes.Buffer
	if err := WriteAuditJSONL(&buf, records); err != nil {
		return fmt.Errorf("audit retention: %w", err)
	}

	path := filepath.Join(r.ArchiveDir, "audit-"+day+".jsonl")
	f, err := fileutil.OpenPrivateAppend(path)
	if err != nil {
		return fmt.Errorf("audit retention: opening archive: %w", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		return fmt.Errorf("audit retention: writing %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("audit retention: syncing %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("audit retention: closing %s: %w", path, err)
	}
	return nil
}

// archiveDay names the archive file a row belongs in: its creation date in UTC.
func archiveDay(row domain.AuditEvent) string {
	return row.CreatedAt.UTC().Format("20060102")
}

func (r *AuditRetention) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
