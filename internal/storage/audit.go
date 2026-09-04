package storage

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/Suhaibinator/kms/internal/domain"
)

// AppendAudit stores one audit event.
func (s *SQLStore) AppendAudit(ctx context.Context, ev domain.AuditEvent) error {
	return appendAudit(s.db.WithContext(ctx), ev)
}

// appendAudit inserts an audit row through db, which may be the caller's
// already-open transaction. Cohort purge uses this so its tombstones,
// changelog entry, and allow audit are indivisible.
func appendAudit(db *gorm.DB, ev domain.AuditEvent) error {
	created := ev.CreatedAt
	if created.IsZero() {
		created = nowUTC()
	}
	m := auditEventModel{
		EventType:           ev.EventType,
		ActorIdentity:       ev.ActorIdentity,
		ActorType:           ev.ActorType,
		ResourceType:        ev.ResourceType,
		ResourceNamespaceID: ev.ResourceNamespaceID,
		ResourceEnv:         ev.ResourceEnv,
		ResourceApp:         ev.ResourceApp,
		ResourceKey:         ev.ResourceKey,
		ResourceVersion:     int64(ev.ResourceVersion),
		Decision:            ev.Decision,
		SourceIP:            ev.SourceIP,
		UserAgent:           ev.UserAgent,
		RequestID:           ev.RequestID,
		CreatedAt:           fmtTime(created),
		MetadataJSON:        zeroOr(ev.Metadata, "{}"),
	}
	return db.Create(&m).Error
}

// ListAudit returns audit events matching f, newest first (ordered by id DESC).
func (s *SQLStore) ListAudit(ctx context.Context, f domain.AuditFilter, page ListPage) ([]domain.AuditEvent, string, error) {
	limit := clampLimit(page.Limit)
	q := s.db.WithContext(ctx).Model(&auditEventModel{})
	if page.Token != "" {
		id, err := decodeIntToken(page.Token)
		if err != nil {
			return nil, "", err
		}
		q = q.Where("id < ?", id)
	}
	if f.Env != "" {
		q = q.Where("resource_env = ?", f.Env)
	}
	if f.App != "" {
		q = q.Where("resource_app = ?", f.App)
	}
	q = applyKeyPrefix(q, "resource_key", f.KeyPrefix)
	if f.ActorIdentity != "" {
		q = q.Where("actor_identity = ?", f.ActorIdentity)
	}
	if f.EventType != "" {
		q = q.Where("event_type = ?", f.EventType)
	}
	if f.Decision != "" {
		q = q.Where("decision = ?", f.Decision)
	}
	// Fixed-width timestamps sort lexicographically in chronological order, so
	// text range comparisons are correct.
	if !f.From.IsZero() {
		q = q.Where("created_at >= ?", fmtTime(f.From))
	}
	if !f.To.IsZero() {
		q = q.Where("created_at <= ?", fmtTime(f.To))
	}

	var rows []auditEventModel
	if err := q.Order("id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeIntToken(rows[len(rows)-1].ID)
	}
	out := make([]domain.AuditEvent, 0, len(rows))
	for _, m := range rows {
		out = append(out, toAuditEvent(m))
	}
	return out, next, nil
}

// AuditPageToken returns the storage cursor immediately after id. The service
// uses it when authorization filtering happens above storage so empty filtered
// pages and cursors for hidden rows are not exposed to callers.
func AuditPageToken(id int64) string {
	return encodeIntToken(id)
}

// AuditPruneStore is implemented by stores that can retire audit rows. It is
// deliberately not part of Store: retiring audit history is an operator-driven
// capability of the durable store, not something every Store implementation
// (including the in-memory test fakes) has to answer for. Callers type-assert
// for it.
type AuditPruneStore interface {
	// ListAuditBefore returns up to limit rows with created_at < before, oldest
	// first (ascending id).
	ListAuditBefore(ctx context.Context, before time.Time, limit int) ([]domain.AuditEvent, error)
	// DeleteAuditByIDs deletes exactly the given rows and returns how many were
	// deleted.
	DeleteAuditByIDs(ctx context.Context, ids []int64) (int64, error)
	// CountAuditBefore reports how many rows a full retirement pass would
	// cover. ListAuditBefore cannot answer that on its own — it has no cursor,
	// so the only way past its first batch is to delete one — which is exactly
	// what "audit prune --dry-run" must not do.
	CountAuditBefore(ctx context.Context, before time.Time) (int64, error)
}

var _ AuditPruneStore = (*SQLStore)(nil)

// ListAuditBefore returns the oldest rows written strictly before the cutoff,
// so a caller can archive a batch before deleting it. limit is clamped the way
// every other listing is; a zero cutoff means "retain everything" and selects
// no rows.
func (s *SQLStore) ListAuditBefore(ctx context.Context, before time.Time, limit int) ([]domain.AuditEvent, error) {
	if before.IsZero() {
		return nil, nil
	}
	var rows []auditEventModel
	// Fixed-width timestamps sort lexicographically in chronological order, so
	// the text comparison is the chronological one. The bound is strict: a row
	// stamped at exactly the cutoff is still inside the retention window.
	err := s.db.WithContext(ctx).Model(&auditEventModel{}).
		Where("created_at < ?", fmtTime(before)).
		Order("id ASC").
		Limit(clampLimit(limit)).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]domain.AuditEvent, 0, len(rows))
	for _, m := range rows {
		out = append(out, toAuditEvent(m))
	}
	return out, nil
}

// CountAuditBefore counts the rows ListAuditBefore would eventually return for
// the same cutoff, applying the identical strict text comparison so the count
// and the pass agree on which rows are inside the retention window. A zero
// cutoff means "retain everything" and counts nothing.
func (s *SQLStore) CountAuditBefore(ctx context.Context, before time.Time) (int64, error) {
	if before.IsZero() {
		return 0, nil
	}
	var total int64
	err := s.db.WithContext(ctx).Model(&auditEventModel{}).
		Where("created_at < ?", fmtTime(before)).
		Count(&total).Error
	if err != nil {
		return 0, err
	}
	return total, nil
}

// DeleteAuditByIDs deletes exactly the listed rows and reports how many were
// removed; ids that no longer exist are simply absent from the count, so a
// second pass over the same batch is harmless. Callers pass a batch obtained
// from ListAuditBefore, which keeps the id list well inside SQLite's bound
// parameter limit.
func (s *SQLStore) DeleteAuditByIDs(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := s.db.WithContext(ctx).Where("id IN ?", ids).Delete(&auditEventModel{})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
