package storage

import (
	"context"
	"math"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/Suhaibinator/kms/internal/domain"
)

// CurrentRevision returns the latest assigned revision (0 on a fresh DB). It
// reads sqlite_sequence so it survives pruning of all change_log rows.
func (s *SQLStore) CurrentRevision(ctx context.Context) (uint64, error) {
	return s.currentRevision(s.db.WithContext(ctx))
}

func (s *SQLStore) currentRevision(db *gorm.DB) (uint64, error) {
	var seq []int64
	if err := db.Raw("SELECT seq FROM sqlite_sequence WHERE name = 'change_log'").Scan(&seq).Error; err != nil {
		return 0, err
	}
	if len(seq) == 0 || seq[0] < 0 {
		return 0, nil
	}
	return uint64(seq[0]), nil
}

// OldestRetainedRevision returns the smallest revision still in the change log,
// or 0 when the log is empty.
func (s *SQLStore) OldestRetainedRevision(ctx context.Context) (uint64, error) {
	var v int64
	if err := s.db.WithContext(ctx).Raw("SELECT COALESCE(MIN(revision), 0) FROM change_log").Scan(&v).Error; err != nil {
		return 0, err
	}
	if v < 0 {
		return 0, nil
	}
	return uint64(v), nil
}

// ListChangesSince returns entries with revision > since, ascending, up to
// limit.
func (s *SQLStore) ListChangesSince(ctx context.Context, since uint64, limit int) ([]domain.ChangeLogEntry, error) {
	limit = clampLimit(limit)
	var rows []changeLogModel
	if err := s.db.WithContext(ctx).Where("revision > ?", since).
		Order("revision ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.ChangeLogEntry, 0, len(rows))
	for _, m := range rows {
		out = append(out, toChangeEntry(m))
	}
	return out, nil
}

// PruneChangeLog deletes entries older than keepDuration AND beyond maxRows most
// recent — i.e. an entry survives only if it is both within the retention
// window and among the maxRows newest. Revisions are never reused after
// pruning. It returns the number of rows deleted.
func (s *SQLStore) PruneChangeLog(ctx context.Context, keepDuration time.Duration, maxRows int) (int, error) {
	var deleted int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// keepThreshold: the smallest revision retained by the maxRows policy.
		// Rows with revision < keepThreshold are beyond the maxRows newest.
		var keepThreshold int64
		if maxRows <= 0 {
			// Retain zero rows by count: delete everything.
			keepThreshold = math.MaxInt64
		} else {
			var rev []int64
			if err := tx.Raw(
				"SELECT revision FROM change_log ORDER BY revision DESC LIMIT 1 OFFSET ?",
				maxRows-1,
			).Scan(&rev).Error; err != nil {
				return err
			}
			if len(rev) > 0 {
				keepThreshold = rev[0]
			}
			// Fewer than maxRows rows: keepThreshold stays 0 (delete none by count).
		}
		cutoff := fmtTime(time.Now().Add(-keepDuration))
		res := tx.Exec("DELETE FROM change_log WHERE created_at < ? OR revision < ?", cutoff, keepThreshold)
		if res.Error != nil {
			return res.Error
		}
		deleted = res.RowsAffected
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int(deleted), nil
}

// snapshotNamespaceIDs resolves the given namespaces to their row IDs in one
// query. Namespaces that do not exist contribute nothing. matchNone is true when
// no namespace resolves to a row, so the caller can skip the join entirely while
// still returning the current revision.
func snapshotNamespaceIDs(tx *gorm.DB, namespaces []domain.NamespaceRef) (ids []int64, matchNone bool, err error) {
	if len(namespaces) == 0 {
		return nil, true, nil
	}
	var where []string
	var whereArgs []any
	seen := map[domain.NamespaceRef]bool{}
	for _, ns := range namespaces {
		if seen[ns] {
			continue
		}
		seen[ns] = true
		where = append(where, "(env = ? AND app = ?)")
		whereArgs = append(whereArgs, ns.Env, ns.App)
	}
	var nsRows []namespaceModel
	if err := tx.Select("id").Where(strings.Join(where, " OR "), whereArgs...).Find(&nsRows).Error; err != nil {
		return nil, false, err
	}
	if len(nsRows) == 0 {
		return nil, true, nil
	}
	ids = make([]int64, len(nsRows))
	for i, m := range nsRows {
		ids[i] = m.ID
	}
	return ids, false, nil
}

// snapshotRow is the flattened result of the set-based snapshot join: one row
// per matching parameter resolved at its "current" label, joined to that
// label's version and its namespace.
type snapshotRow struct {
	ID          int64
	Env         string
	App         string
	Key         string
	Value       string
	ContentType string
	Version     int64
	Metadata    string
	CreatedBy   string
	CreatedAt   string
}

// SnapshotParameters returns, in one consistent read transaction, the current
// revision and the "current" value of every parameter in the given namespaces
// (WHERE namespace_id IN (...)). Authorization is namespace-level and checked
// once at subscribe time, so the snapshot is the whole authorized namespace.
//
// The DSN sets _txlock=immediate, so this transaction acquires SQLite's global
// write lock the instant it begins (BEGIN IMMEDIATE) and holds it until commit.
// This runs on the hot watch-subscribe path; under a reconnect storm many
// snapshots fire at once and would serialize against each other and every
// writer. To keep the lock held for as short a time as possible, the body does
// a fixed, small number of SET-BASED queries (revision + one join + one label
// fetch) rather than an N+1 loop over each matching parameter.
func (s *SQLStore) SnapshotParameters(ctx context.Context, namespaces []domain.NamespaceRef) ([]domain.Parameter, uint64, error) {
	var out []domain.Parameter
	var revision uint64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rev, err := s.currentRevision(tx)
		if err != nil {
			return err
		}
		revision = rev

		nsIDs, matchNone, err := snapshotNamespaceIDs(tx, namespaces)
		if err != nil {
			return err
		}
		if matchNone {
			return nil
		}

		// One join resolves every parameter in the namespaces at its "current"
		// label and pulls the version row and namespace in a single statement. The
		// inner joins reproduce the per-parameter contract exactly: a parameter is
		// dropped when it has no "current" label, or no version row for that
		// label. Ordering by (env, app, name) gives a stable display order.
		q := tx.Table("parameters AS p").
			Select("p.id AS id, n.env AS env, n.app AS app, p.name AS key, "+
				"pv.value AS value, pv.content_type AS content_type, "+
				"pv.version_number AS version, pv.metadata_json AS metadata, "+
				"pv.created_by AS created_by, pv.created_at AS created_at").
			Joins("JOIN namespaces n ON n.id = p.namespace_id").
			Joins("JOIN parameter_labels pl ON pl.parameter_id = p.id AND pl.label = ?", domain.LabelCurrent).
			Joins("JOIN parameter_versions pv ON pv.parameter_id = p.id AND pv.version_number = pl.version_number").
			Where("p.namespace_id IN ?", nsIDs)
		var rows []snapshotRow
		if err := q.Order("n.env ASC, n.app ASC, p.name ASC").Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}

		// One more query loads every label of exactly the matched parameters, so
		// the full Labels map is assembled without a per-parameter round trip.
		ids := make([]int64, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		var lbls []parameterLabelModel
		if err := tx.Where("parameter_id IN ?", ids).Find(&lbls).Error; err != nil {
			return err
		}
		labelsByParam := make(map[int64]map[string]uint64, len(rows))
		for _, l := range lbls {
			m := labelsByParam[l.ParameterID]
			if m == nil {
				m = make(map[string]uint64)
				labelsByParam[l.ParameterID] = m
			}
			m[l.Label] = uint64(l.VersionNumber)
		}

		out = make([]domain.Parameter, 0, len(rows))
		for _, r := range rows {
			out = append(out, domain.Parameter{
				Ref:         domain.Ref{NS: domain.NamespaceRef{Env: r.Env, App: r.App}, Key: r.Key},
				Value:       r.Value,
				ContentType: r.ContentType,
				Version:     uint64(r.Version),
				Metadata:    r.Metadata,
				CreatedBy:   r.CreatedBy,
				CreatedAt:   parseTime(r.CreatedAt),
				Labels:      labelsByParam[r.ID],
			})
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return out, revision, nil
}
