package storage

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/Suhaibinator/kms/internal/domain"
)

// InsertKeyMetadata stores a KEK metadata row.
func (s *SQLStore) InsertKeyMetadata(ctx context.Context, km domain.KeyMetadata) error {
	created := km.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	m := keyMetadataModel{
		ID:        km.ID,
		Source:    km.Source,
		KDF:       km.KDF,
		KDFSalt:   km.KDFSalt,
		KeyCheck:  km.KeyCheck,
		State:     zeroOr(km.State, domain.KeyStateActive),
		CreatedAt: fmtTime(created),
	}
	if err := s.db.WithContext(ctx).Create(&m).Error; err != nil {
		if isUniqueErr(err) {
			return domain.Errorf(domain.ErrAlreadyExists, "key %s", km.ID)
		}
		return err
	}
	return nil
}

// GetKeyMetadata returns the KEK metadata for id.
func (s *SQLStore) GetKeyMetadata(ctx context.Context, id string) (domain.KeyMetadata, error) {
	var m keyMetadataModel
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.KeyMetadata{}, domain.Errorf(domain.ErrNotFound, "key %s", id)
		}
		return domain.KeyMetadata{}, err
	}
	return toKeyMetadata(m), nil
}

// ListKeyMetadata returns all KEK metadata rows, oldest first.
func (s *SQLStore) ListKeyMetadata(ctx context.Context) ([]domain.KeyMetadata, error) {
	var rows []keyMetadataModel
	if err := s.db.WithContext(ctx).Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.KeyMetadata, 0, len(rows))
	for _, m := range rows {
		out = append(out, toKeyMetadata(m))
	}
	return out, nil
}

// ActiveKeyMetadata returns the single active KEK.
func (s *SQLStore) ActiveKeyMetadata(ctx context.Context) (domain.KeyMetadata, error) {
	var m keyMetadataModel
	err := s.db.WithContext(ctx).Where("state = ?", domain.KeyStateActive).
		Order("created_at DESC, id DESC").First(&m).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.KeyMetadata{}, domain.Errorf(domain.ErrNotFound, "no active key")
		}
		return domain.KeyMetadata{}, err
	}
	return toKeyMetadata(m), nil
}

// SetKeyState updates the state of a KEK.
func (s *SQLStore) SetKeyState(ctx context.Context, id, state string) error {
	res := s.db.WithContext(ctx).Model(&keyMetadataModel{}).Where("id = ?", id).Update("state", state)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.Errorf(domain.ErrNotFound, "key %s", id)
	}
	return nil
}

// RotateKEK atomically inserts newKM (state "active"), retires every other key,
// and rewraps every non-destroyed secret version by storing rewrap's returned
// encrypted DEK with kek_id = newKM.ID. The whole rotation runs in one
// transaction; on any error the database is unchanged. Returns the number of
// versions rewrapped.
func (s *SQLStore) RotateKEK(ctx context.Context, newKM domain.KeyMetadata,
	rewrap func(rec SecretVersionRecord) (newEncryptedDEK []byte, err error)) (int, error) {
	count := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		created := newKM.CreatedAt
		if created.IsZero() {
			created = time.Now()
		}
		km := keyMetadataModel{
			ID:        newKM.ID,
			Source:    newKM.Source,
			KDF:       newKM.KDF,
			KDFSalt:   newKM.KDFSalt,
			KeyCheck:  newKM.KeyCheck,
			State:     domain.KeyStateActive,
			CreatedAt: fmtTime(created),
		}
		if err := tx.Create(&km).Error; err != nil {
			if isUniqueErr(err) {
				return domain.Errorf(domain.ErrAlreadyExists, "key %s", newKM.ID)
			}
			return err
		}
		if err := tx.Model(&keyMetadataModel{}).Where("id <> ?", newKM.ID).
			Update("state", domain.KeyStateRetired).Error; err != nil {
			return err
		}

		var rows []secretVersionModel
		if err := tx.Where("state <> ?", domain.StateDestroyed).Order("id ASC").Find(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			newDEK, err := rewrap(toSecretVersionRecord(r))
			if err != nil {
				return err
			}
			if err := tx.Model(&secretVersionModel{}).Where("id = ?", r.ID).Updates(map[string]any{
				"encrypted_dek": newDEK,
				"kek_id":        newKM.ID,
			}).Error; err != nil {
				return err
			}
			count++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
