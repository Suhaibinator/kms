package storage

import (
	"context"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// OperationalStats is the store's contribution to the operational metrics: the
// counts a monitoring backend needs and no request path produces. Every field
// is a plain number — the exporter that publishes them must never learn which
// namespace, identity, or key is behind one.
type OperationalStats struct {
	// ChangeLogRows is how many change-log entries are currently retained.
	ChangeLogRows int64
	// ChangeLogLastRevision is the highest revision ever assigned. It comes
	// from the autoincrement sequence rather than MAX(revision), so it keeps
	// advancing after a prune removes every row and never reads as a reset.
	ChangeLogLastRevision int64
	// ChangeLogOldestRevision is the lowest revision still retained, 0 when the
	// log is empty. With ChangeLogLastRevision it describes the replay window a
	// reconnecting subscriber can be served from.
	ChangeLogOldestRevision int64
	// IdentityCertsExpiringSoon counts unrevoked issued certificates whose
	// not_after falls in [now, now+certWindow]. Certificates that have already
	// expired are excluded: they are a past-tense problem, and counting them
	// would leave the number pinned above zero forever.
	IdentityCertsExpiringSoon int64
	// SecretVersionsExpiringSoon counts enabled secret versions with an
	// expires_at in [now, now+secretWindow]. Disabled and destroyed versions
	// are excluded — their expiry is no longer anyone's problem.
	SecretVersionsExpiringSoon int64
}

// OperationalStatsStore is an optional store capability, deliberately outside
// the Store interface: it exists for the metrics sampler alone, and requiring
// it of every implementation would force test and fixture stores to grow a
// method nothing in the request path calls. Callers type-assert for it and do
// without the numbers when it is absent.
type OperationalStatsStore interface {
	OperationalStats(ctx context.Context, now time.Time, certWindow, secretWindow time.Duration) (OperationalStats, error)
}

var _ OperationalStatsStore = (*SQLStore)(nil)

// OperationalStats gathers the sampled counts in a handful of aggregate
// queries. now is passed in rather than read here so a caller can sample
// against a fixed clock; a zero now means "right now".
//
// The expiry comparisons are text range filters. That is exact rather than
// approximate: every stored timestamp is fixed-width RFC 3339 UTC (see
// tsLayout), so lexicographic ordering is chronological ordering, and both
// window bounds are inclusive.
func (s *SQLStore) OperationalStats(ctx context.Context, now time.Time, certWindow, secretWindow time.Duration) (OperationalStats, error) {
	if now.IsZero() {
		now = nowUTC()
	}
	db := s.db.WithContext(ctx)
	var stats OperationalStats

	if err := db.Model(&changeLogModel{}).Count(&stats.ChangeLogRows).Error; err != nil {
		return OperationalStats{}, err
	}
	last, err := s.currentRevision(db)
	if err != nil {
		return OperationalStats{}, err
	}
	stats.ChangeLogLastRevision = int64(last)
	if err := db.Raw("SELECT COALESCE(MIN(revision), 0) FROM change_log").
		Scan(&stats.ChangeLogOldestRevision).Error; err != nil {
		return OperationalStats{}, err
	}

	from := fmtTime(now)
	if err := db.Model(&identityCertModel{}).
		Where("revoked_at IS NULL AND not_after >= ? AND not_after <= ?", from, fmtTime(now.Add(certWindow))).
		Count(&stats.IdentityCertsExpiringSoon).Error; err != nil {
		return OperationalStats{}, err
	}

	if err := db.Model(&secretVersionModel{}).
		Where("state = ? AND expires_at IS NOT NULL AND expires_at >= ? AND expires_at <= ?",
			domain.StateEnabled, from, fmtTime(now.Add(secretWindow))).
		Count(&stats.SecretVersionsExpiringSoon).Error; err != nil {
		return OperationalStats{}, err
	}

	return stats, nil
}
