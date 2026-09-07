package storage

import (
	"context"

	"gorm.io/gorm"

	"github.com/Suhaibinator/kms/internal/domain"
)

// DeleteWithAudit applies one irreversible removal and its audit row in a
// single transaction. A destructive change and the evidence that it happened
// are therefore indivisible: an audit insert that fails rolls the removal back
// and surfaces ErrRequiredAuditUnavailable, and the mutation's own refusals
// (not found, non-empty namespace, protected release reference) are returned
// unchanged with no audit row written.
func (s *SQLStore) DeleteWithAudit(ctx context.Context, m DestructiveMutation, audit domain.AuditEvent) (uint64, error) {
	if m.Kind == DestructiveSecretVersion && m.Version == 0 {
		return 0, domain.Errorf(domain.ErrInvalidArgument, "version is required")
	}
	var revision uint64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var (
			rev uint64
			err error
		)
		switch m.Kind {
		case DestructiveParameter:
			rev, err = s.deleteParameterTx(tx, m.Ref)
		case DestructiveSecret:
			rev, err = s.deleteSecretTx(tx, m.Ref)
		case DestructiveSecretVersion:
			rev, err = s.destroySecretVersionTx(tx, m.Ref, m.Version)
		case DestructiveNamespace:
			err = deleteNamespaceTx(tx, m.Ref.NS)
		case DestructivePolicy:
			err = deletePolicyTx(tx, m.Name)
		case DestructiveApplication:
			err = deleteApplicationTx(tx, m.Name)
		default:
			err = domain.Errorf(domain.ErrInvalidArgument, "unknown destructive mutation kind %q", m.Kind)
		}
		if err != nil {
			return err
		}
		if err := appendAudit(tx, audit); err != nil {
			return ErrRequiredAuditUnavailable
		}
		revision = rev
		return nil
	})
	if err != nil {
		return 0, err
	}
	return revision, nil
}
