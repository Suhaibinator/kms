package storage

import (
	"context"

	"github.com/Suhaibinator/kms/internal/domain"
)

// AppendAudit stores one audit event.
func (s *SQLStore) AppendAudit(ctx context.Context, ev domain.AuditEvent) error {
	created := ev.CreatedAt
	if created.IsZero() {
		created = nowUTC()
	}
	m := auditEventModel{
		EventType:       ev.EventType,
		ActorIdentity:   ev.ActorIdentity,
		ActorType:       ev.ActorType,
		ResourceType:    ev.ResourceType,
		ResourcePath:    ev.ResourcePath,
		ResourceVersion: int64(ev.ResourceVersion),
		Decision:        ev.Decision,
		SourceIP:        ev.SourceIP,
		UserAgent:       ev.UserAgent,
		RequestID:       ev.RequestID,
		CreatedAt:       fmtTime(created),
		MetadataJSON:    zeroOr(ev.Metadata, "{}"),
	}
	return s.db.WithContext(ctx).Create(&m).Error
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
	q = applyPrefix(q, "resource_path", f.PathPrefix)
	if f.ActorIdentity != "" {
		q = q.Where("actor_identity = ?", f.ActorIdentity)
	}
	if f.EventType != "" {
		q = q.Where("event_type = ?", f.EventType)
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
