package storage

import (
	"context"
	"database/sql"

	"github.com/Suhaibinator/kms/internal/domain"
	"gorm.io/gorm"
)

// ReadReleaseProjection reads sessions and fleet targets from one SQLite
// snapshot. Display pagination must happen after this complete input is reduced.
// The raw acknowledgement-history endpoint intentionally remains separate.
func (s *SQLStore) ReadReleaseProjection(ctx context.Context, filter domain.ReleaseFilter) (rows []domain.ReleaseAcknowledgement, active map[domain.ReleaseTrack]domain.ActiveConfigurationRelease, err error) {
	active = make(map[domain.ReleaseTrack]domain.ActiveConfigurationRelease)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		nsID, err := resolveNamespaceID(tx, filter.Namespace)
		if err != nil {
			return err
		}
		var labels []configurationReleaseLabelModel
		query := tx.Where("namespace_id = ? AND label = ?", nsID, domain.LabelCurrent)
		if filter.Name != "" {
			query = query.Where("release_name = ?", filter.Name)
		}
		if filter.SchemaVersion != nil {
			query = query.Where("schema_version = ?", *filter.SchemaVersion)
		}
		if err := query.Find(&labels).Error; err != nil {
			return err
		}
		reader := &SQLStore{db: tx}
		for _, label := range labels {
			track := domain.ReleaseTrack{Namespace: filter.Namespace, Name: label.ReleaseName, SchemaVersion: uint64(label.SchemaVersion)}
			// Detect damaged or unavailable targets instead of inventing readiness
			// from a dangling label. Ordinary no-current-label is a valid empty track.
			release, err := reader.GetActiveConfigurationRelease(ctx, track)
			if err != nil {
				return err
			}
			active[track] = release
		}
		page := ListPage{Limit: 1000}
		for {
			batch, next, err := reader.ListReleaseAcknowledgements(ctx, filter, page)
			if err != nil {
				return err
			}
			for _, row := range batch {
				if row.SessionID == "" {
					continue
				}
				if row.PinVersion > 0 {
					if _, err := getConfigurationRelease(tx, row.Track(), row.PinVersion); err != nil {
						return err
					}
				}
				rows = append(rows, row)
			}
			if next == "" {
				break
			}
			page.Token = next
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	return rows, active, err
}
