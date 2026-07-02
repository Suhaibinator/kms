package storage

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Suhaibinator/kms/internal/domain"
)

func loadSecretLabels(tx *gorm.DB, secretID int64) (map[string]uint64, error) {
	var labels []secretLabelModel
	if err := tx.Where("secret_id = ?", secretID).Find(&labels).Error; err != nil {
		return nil, err
	}
	m := make(map[string]uint64, len(labels))
	for _, l := range labels {
		m[l.Label] = uint64(l.VersionNumber)
	}
	return m, nil
}

func setSecretLabel(tx *gorm.DB, secretID int64, label string, version uint64) error {
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "secret_id"}, {Name: "label"}},
		DoUpdates: clause.AssignmentColumns([]string{"version_number"}),
	}).Omit(clause.Associations).Create(&secretLabelModel{
		SecretID:      secretID,
		Label:         label,
		VersionNumber: int64(version),
	}).Error
}

// CreateSecretVersion atomically creates/updates the secret row, assigns the
// next version number, invokes p.Encrypt(version) inside the transaction,
// stores the payload, moves labels, and appends a metadata-only change-log
// entry.
func (s *SQLStore) CreateSecretVersion(ctx context.Context, p CreateSecretParams) (version, revision uint64, err error) {
	contentType := zeroOr(p.ContentType, "application/octet-stream")
	metadata := zeroOr(p.Metadata, "{}")
	now := fmtTime(time.Now())

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sec secretModel
		e := tx.Where("path = ?", p.Path).First(&sec).Error
		switch {
		case errors.Is(e, gorm.ErrRecordNotFound):
			sec = secretModel{
				Path:            p.Path,
				ClientBound:     b2i(p.ClientBound),
				AccessTokenHash: p.AccessTokenHash,
				ContentType:     contentType,
				MetadataJSON:    metadata,
				CreatedAt:       now,
				UpdatedAt:       now,
			}
			if err := tx.Omit(clause.Associations).Create(&sec).Error; err != nil {
				return err
			}
		case e != nil:
			return e
		default:
			if i2b(sec.ClientBound) != p.ClientBound {
				return domain.Errorf(domain.ErrFailedPrecondition,
					"secret %s wrap-mode mismatch (existing client_bound=%v, requested=%v)", p.Path, i2b(sec.ClientBound), p.ClientBound)
			}
			upd := map[string]any{
				"content_type":  contentType,
				"metadata_json": metadata,
				"updated_at":    now,
			}
			// Keep the existing hash when the caller did not mint a new one.
			if p.AccessTokenHash != nil {
				upd["access_token_hash"] = p.AccessTokenHash
			}
			if err := tx.Model(&secretModel{}).Where("id = ?", sec.ID).Updates(upd).Error; err != nil {
				return err
			}
		}

		// Next version includes destroyed versions so numbers are never reused.
		var maxVer int64
		if err := tx.Model(&secretVersionModel{}).Where("secret_id = ?", sec.ID).
			Select("COALESCE(MAX(version_number), 0)").Scan(&maxVer).Error; err != nil {
			return err
		}
		newVer := uint64(maxVer) + 1

		payload, err := p.Encrypt(newVer)
		if err != nil {
			return err
		}

		sv := secretVersionModel{
			SecretID:      sec.ID,
			VersionNumber: int64(newVer),
			Ciphertext:    payload.Ciphertext,
			EncryptedDEK:  payload.EncryptedDEK,
			KEKID:         payload.KEKID,
			WrapMode:      zeroOr(payload.WrapMode, domain.WrapModeStandard),
			ClientKeySalt: payload.ClientKeySalt,
			Algorithm:     zeroOr(payload.Algorithm, "AES-256-GCM"),
			Nonce:         payload.Nonce,
			AAD:           payload.AAD,
			State:         domain.StateEnabled,
			CreatedBy:     p.CreatedBy,
			CreatedAt:     now,
			ExpiresAt:     fmtTimePtr(p.ExpiresAt),
			MetadataJSON:  metadata,
		}
		if err := tx.Omit(clause.Associations).Create(&sv).Error; err != nil {
			return err
		}

		labels, err := loadSecretLabels(tx, sec.ID)
		if err != nil {
			return err
		}
		if err := setSecretLabel(tx, sec.ID, domain.LabelCurrent, newVer); err != nil {
			return err
		}
		if oldCur, ok := labels[domain.LabelCurrent]; ok && oldCur != newVer {
			if err := setSecretLabel(tx, sec.ID, domain.LabelPrevious, oldCur); err != nil {
				return err
			}
		}

		rev, err := appendChange(tx, &changeLogModel{
			ResourceType:  domain.ResourceSecret,
			Path:          p.Path,
			ChangeType:    domain.ChangePut,
			ContentType:   contentType,
			VersionNumber: int64(newVer),
			Label:         domain.LabelCurrent,
			CreatedAt:     now,
		})
		if err != nil {
			return err
		}
		version, revision = newVer, rev
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return version, revision, nil
}

// GetSecretRecord returns the secret-level row.
func (s *SQLStore) GetSecretRecord(ctx context.Context, path string) (SecretRecord, error) {
	db := s.db.WithContext(ctx)
	var sec secretModel
	if err := db.Where("path = ?", path).First(&sec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SecretRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s", path)
		}
		return SecretRecord{}, err
	}
	labels, err := loadSecretLabels(db, sec.ID)
	if err != nil {
		return SecretRecord{}, err
	}
	return toSecretRecord(sec, labels), nil
}

// GetSecretVersion resolves version (>0) or label (default "current") and
// returns both the secret row and the version row. It does not filter by state.
func (s *SQLStore) GetSecretVersion(ctx context.Context, path string, version uint64, label string) (SecretRecord, SecretVersionRecord, error) {
	db := s.db.WithContext(ctx)
	var sec secretModel
	if err := db.Where("path = ?", path).First(&sec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SecretRecord{}, SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s", path)
		}
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	ver := version
	if ver == 0 {
		lbl := zeroOr(label, domain.LabelCurrent)
		var lm secretLabelModel
		if err := db.Where("secret_id = ? AND label = ?", sec.ID, lbl).First(&lm).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return SecretRecord{}, SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s label %q", path, lbl)
			}
			return SecretRecord{}, SecretVersionRecord{}, err
		}
		ver = uint64(lm.VersionNumber)
	}
	var sv secretVersionModel
	if err := db.Where("secret_id = ? AND version_number = ?", sec.ID, ver).First(&sv).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SecretRecord{}, SecretVersionRecord{}, domain.Errorf(domain.ErrNotFound, "secret %s version %d", path, ver)
		}
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	labels, err := loadSecretLabels(db, sec.ID)
	if err != nil {
		return SecretRecord{}, SecretVersionRecord{}, err
	}
	return toSecretRecord(sec, labels), toSecretVersionRecord(sv), nil
}

func (s *SQLStore) secretInfo(ctx context.Context, sec secretModel) (domain.Secret, error) {
	db := s.db.WithContext(ctx)
	labels, err := loadSecretLabels(db, sec.ID)
	if err != nil {
		return domain.Secret{}, err
	}
	var vers []secretVersionModel
	if err := db.Where("secret_id = ?", sec.ID).Order("version_number ASC").Find(&vers).Error; err != nil {
		return domain.Secret{}, err
	}
	vinfos := make([]domain.SecretVersionInfo, 0, len(vers))
	for _, v := range vers {
		vinfos = append(vinfos, domain.SecretVersionInfo{
			Version:     uint64(v.VersionNumber),
			State:       v.State,
			CreatedBy:   v.CreatedBy,
			CreatedAt:   parseTime(v.CreatedAt),
			DestroyedAt: parseTimePtr(v.DestroyedAt),
			ExpiresAt:   parseTimePtr(v.ExpiresAt),
			Metadata:    v.MetadataJSON,
		})
	}
	return domain.Secret{
		Path:           sec.Path,
		ContentType:    sec.ContentType,
		ClientBound:    i2b(sec.ClientBound),
		HasAccessToken: sec.AccessTokenHash != nil,
		Metadata:       sec.MetadataJSON,
		CreatedAt:      parseTime(sec.CreatedAt),
		UpdatedAt:      parseTime(sec.UpdatedAt),
		Labels:         labels,
		Versions:       vinfos,
	}, nil
}

// GetSecretInfo returns secret-level metadata and version history.
func (s *SQLStore) GetSecretInfo(ctx context.Context, path string) (domain.Secret, error) {
	var sec secretModel
	if err := s.db.WithContext(ctx).Where("path = ?", path).First(&sec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.Secret{}, domain.Errorf(domain.ErrNotFound, "secret %s", path)
		}
		return domain.Secret{}, err
	}
	return s.secretInfo(ctx, sec)
}

// ListSecrets returns matching secrets ordered by path.
func (s *SQLStore) ListSecrets(ctx context.Context, prefix string, page ListPage) ([]domain.Secret, string, error) {
	limit := clampLimit(page.Limit)
	after, err := decodeToken(page.Token)
	if err != nil {
		return nil, "", err
	}
	q := s.db.WithContext(ctx).Model(&secretModel{})
	if after != "" {
		q = q.Where("path > ?", after)
	}
	q = applyPrefix(q, "path", prefix)

	var rows []secretModel
	if err := q.Order("path ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeToken(rows[len(rows)-1].Path)
	}
	out := make([]domain.Secret, 0, len(rows))
	for _, sec := range rows {
		info, err := s.secretInfo(ctx, sec)
		if err != nil {
			return nil, "", err
		}
		out = append(out, info)
	}
	return out, next, nil
}

// DeleteSecret removes the secret and all versions and appends a change-log
// entry.
func (s *SQLStore) DeleteSecret(ctx context.Context, path string) (uint64, error) {
	var revision uint64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sec secretModel
		if err := tx.Where("path = ?", path).First(&sec).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.Errorf(domain.ErrNotFound, "secret %s", path)
			}
			return err
		}
		if err := tx.Where("secret_id = ?", sec.ID).Delete(&secretLabelModel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("secret_id = ?", sec.ID).Delete(&secretVersionModel{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&secretModel{}, sec.ID).Error; err != nil {
			return err
		}
		rev, err := appendChange(tx, &changeLogModel{
			ResourceType: domain.ResourceSecret,
			Path:         path,
			ChangeType:   domain.ChangeDelete,
		})
		if err != nil {
			return err
		}
		revision = rev
		return nil
	})
	if err != nil {
		return 0, err
	}
	return revision, nil
}

// SetSecretVersionState enables/disables version (0 = all non-destroyed
// versions). Destroyed versions are never resurrected.
func (s *SQLStore) SetSecretVersionState(ctx context.Context, path string, version uint64, state string) (uint64, error) {
	if state != domain.StateEnabled && state != domain.StateDisabled {
		return 0, domain.Errorf(domain.ErrInvalidArgument, "invalid state %q", state)
	}
	var revision uint64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sec secretModel
		if err := tx.Where("path = ?", path).First(&sec).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.Errorf(domain.ErrNotFound, "secret %s", path)
			}
			return err
		}
		if version > 0 {
			var sv secretVersionModel
			if err := tx.Where("secret_id = ? AND version_number = ?", sec.ID, version).First(&sv).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return domain.Errorf(domain.ErrNotFound, "secret %s version %d", path, version)
				}
				return err
			}
			if sv.State == domain.StateDestroyed {
				return domain.Errorf(domain.ErrFailedPrecondition, "secret %s version %d is destroyed", path, version)
			}
			if err := tx.Model(&secretVersionModel{}).Where("id = ?", sv.ID).Update("state", state).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Model(&secretVersionModel{}).
				Where("secret_id = ? AND state <> ?", sec.ID, domain.StateDestroyed).
				Update("state", state).Error; err != nil {
				return err
			}
		}
		changeType := domain.ChangeEnable
		if state == domain.StateDisabled {
			changeType = domain.ChangeDisable
		}
		rev, err := appendChange(tx, &changeLogModel{
			ResourceType:  domain.ResourceSecret,
			Path:          path,
			ChangeType:    changeType,
			VersionNumber: int64(version),
		})
		if err != nil {
			return err
		}
		revision = rev
		return nil
	})
	if err != nil {
		return 0, err
	}
	return revision, nil
}

// DestroySecretVersion irreversibly nulls ciphertext, encrypted DEK, and nonce,
// sets state destroyed + destroyed_at, and appends a change-log entry.
func (s *SQLStore) DestroySecretVersion(ctx context.Context, path string, version uint64) (uint64, error) {
	if version == 0 {
		return 0, domain.Errorf(domain.ErrInvalidArgument, "version is required")
	}
	var revision uint64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sec secretModel
		if err := tx.Where("path = ?", path).First(&sec).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.Errorf(domain.ErrNotFound, "secret %s", path)
			}
			return err
		}
		var sv secretVersionModel
		if err := tx.Where("secret_id = ? AND version_number = ?", sec.ID, version).First(&sv).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.Errorf(domain.ErrNotFound, "secret %s version %d", path, version)
			}
			return err
		}
		if sv.State == domain.StateDestroyed {
			return domain.Errorf(domain.ErrFailedPrecondition, "secret %s version %d already destroyed", path, version)
		}
		if err := tx.Model(&secretVersionModel{}).Where("id = ?", sv.ID).Updates(map[string]any{
			"ciphertext":    nil,
			"encrypted_dek": nil,
			"nonce":         nil,
			"state":         domain.StateDestroyed,
			"destroyed_at":  fmtTime(time.Now()),
		}).Error; err != nil {
			return err
		}
		rev, err := appendChange(tx, &changeLogModel{
			ResourceType:  domain.ResourceSecret,
			Path:          path,
			ChangeType:    domain.ChangeDestroy,
			VersionNumber: int64(version),
		})
		if err != nil {
			return err
		}
		revision = rev
		return nil
	})
	if err != nil {
		return 0, err
	}
	return revision, nil
}

// PromoteSecretVersion points "current" at version and "previous" at the old
// current (if different). The target version must exist and be enabled.
func (s *SQLStore) PromoteSecretVersion(ctx context.Context, path string, version uint64) (current, previous, revision uint64, err error) {
	if version == 0 {
		return 0, 0, 0, domain.Errorf(domain.ErrInvalidArgument, "version is required")
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sec secretModel
		if err := tx.Where("path = ?", path).First(&sec).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.Errorf(domain.ErrNotFound, "secret %s", path)
			}
			return err
		}
		var sv secretVersionModel
		if err := tx.Where("secret_id = ? AND version_number = ?", sec.ID, version).First(&sv).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.Errorf(domain.ErrNotFound, "secret %s version %d", path, version)
			}
			return err
		}
		if sv.State != domain.StateEnabled {
			return domain.Errorf(domain.ErrFailedPrecondition, "secret %s version %d is not enabled", path, version)
		}
		labels, err := loadSecretLabels(tx, sec.ID)
		if err != nil {
			return err
		}
		if err := setSecretLabel(tx, sec.ID, domain.LabelCurrent, version); err != nil {
			return err
		}
		prev := labels[domain.LabelPrevious]
		if oldCur, ok := labels[domain.LabelCurrent]; ok && oldCur != version {
			if err := setSecretLabel(tx, sec.ID, domain.LabelPrevious, oldCur); err != nil {
				return err
			}
			prev = oldCur
		}
		rev, err := appendChange(tx, &changeLogModel{
			ResourceType:  domain.ResourceSecret,
			Path:          path,
			ChangeType:    domain.ChangePromote,
			VersionNumber: int64(version),
			Label:         domain.LabelCurrent,
		})
		if err != nil {
			return err
		}
		current, previous, revision = version, prev, rev
		return nil
	})
	if err != nil {
		return 0, 0, 0, err
	}
	return current, previous, revision, nil
}

// UpdateSecretAccessTokenHash replaces the per-secret token hash.
func (s *SQLStore) UpdateSecretAccessTokenHash(ctx context.Context, path string, hash []byte) error {
	res := s.db.WithContext(ctx).Model(&secretModel{}).Where("path = ?", path).Updates(map[string]any{
		"access_token_hash": hash,
		"updated_at":        fmtTime(time.Now()),
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.Errorf(domain.ErrNotFound, "secret %s", path)
	}
	return nil
}
