package storage

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Suhaibinator/kms/internal/domain"
)

func loadParamLabels(tx *gorm.DB, paramID int64) (map[string]uint64, error) {
	var labels []parameterLabelModel
	if err := tx.Where("parameter_id = ?", paramID).Find(&labels).Error; err != nil {
		return nil, err
	}
	m := make(map[string]uint64, len(labels))
	for _, l := range labels {
		m[l.Label] = uint64(l.VersionNumber)
	}
	return m, nil
}

func setParamLabel(tx *gorm.DB, paramID int64, label string, version uint64) error {
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "parameter_id"}, {Name: "label"}},
		DoUpdates: clause.AssignmentColumns([]string{"version_number"}),
	}).Omit(clause.Associations).Create(&parameterLabelModel{
		ParameterID:   paramID,
		Label:         label,
		VersionNumber: int64(version),
	}).Error
}

// PutParameter atomically upserts the parameter within its namespace, appends
// an immutable version, moves the current/previous labels, and appends a
// change-log entry. The namespace must already exist.
func (s *SQLStore) PutParameter(ctx context.Context, ref domain.Ref, value, contentType, metadata, createdBy string) (version, revision uint64, err error) {
	contentType = zeroOr(contentType, "string")
	metadata = zeroOr(metadata, "{}")
	now := fmtTime(time.Now())

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		nsID, err := resolveNamespaceID(tx, ref.NS)
		if err != nil {
			return err
		}
		var p parameterModel
		e := tx.Where("namespace_id = ? AND name = ?", nsID, ref.Key).First(&p).Error
		switch {
		case errors.Is(e, gorm.ErrRecordNotFound):
			p = parameterModel{NamespaceID: nsID, Name: ref.Key, ContentType: contentType, MetadataJSON: metadata, CreatedAt: now, UpdatedAt: now}
			if err := tx.Omit(clause.Associations).Create(&p).Error; err != nil {
				return err
			}
		case e != nil:
			return e
		default:
			if err := tx.Model(&parameterModel{}).Where("id = ?", p.ID).Updates(map[string]any{
				"content_type":  contentType,
				"metadata_json": metadata,
				"updated_at":    now,
			}).Error; err != nil {
				return err
			}
		}

		var maxVer int64
		if err := tx.Model(&parameterVersionModel{}).Where("parameter_id = ?", p.ID).
			Select("COALESCE(MAX(version_number), 0)").Scan(&maxVer).Error; err != nil {
			return err
		}
		newVer := uint64(maxVer) + 1

		pv := parameterVersionModel{
			ParameterID:   p.ID,
			VersionNumber: int64(newVer),
			Value:         value,
			ContentType:   contentType,
			State:         domain.StateEnabled,
			CreatedBy:     createdBy,
			CreatedAt:     now,
			MetadataJSON:  metadata,
		}
		if err := tx.Omit(clause.Associations).Create(&pv).Error; err != nil {
			return err
		}

		labels, err := loadParamLabels(tx, p.ID)
		if err != nil {
			return err
		}
		if err := setParamLabel(tx, p.ID, domain.LabelCurrent, newVer); err != nil {
			return err
		}
		if oldCur, ok := labels[domain.LabelCurrent]; ok && oldCur != newVer {
			if err := setParamLabel(tx, p.ID, domain.LabelPrevious, oldCur); err != nil {
				return err
			}
		}

		val := value
		rev, err := appendChange(tx, &changeLogModel{
			ResourceType:  domain.ResourceParameter,
			Env:           ref.NS.Env,
			App:           ref.NS.App,
			Key:           ref.Key,
			ChangeType:    domain.ChangePut,
			Value:         &val,
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

// resolveParamVersion returns the version number selected by version (>0) or
// label (default "current").
func resolveParamVersion(tx *gorm.DB, ref domain.Ref, paramID int64, version uint64, label string) (uint64, error) {
	if version > 0 {
		return version, nil
	}
	if label == "" {
		label = domain.LabelCurrent
	}
	var lm parameterLabelModel
	if err := tx.Where("parameter_id = ? AND label = ?", paramID, label).First(&lm).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, domain.Errorf(domain.ErrNotFound, "parameter %s label %q", ref, label)
		}
		return 0, err
	}
	return uint64(lm.VersionNumber), nil
}

// findParameter resolves a parameter row from its ref, returning ErrNotFound
// (naming the ref) when the namespace or the parameter is absent.
func (s *SQLStore) findParameter(db *gorm.DB, ref domain.Ref) (parameterModel, error) {
	nsID, err := resolveNamespaceID(db, ref.NS)
	if err != nil {
		return parameterModel{}, err
	}
	var p parameterModel
	if err := db.Where("namespace_id = ? AND name = ?", nsID, ref.Key).First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return parameterModel{}, domain.Errorf(domain.ErrNotFound, "parameter %s", ref)
		}
		return parameterModel{}, err
	}
	return p, nil
}

// GetParameter resolves version (>0) or label (default "current").
func (s *SQLStore) GetParameter(ctx context.Context, ref domain.Ref, version uint64, label string) (domain.Parameter, error) {
	db := s.db.WithContext(ctx)
	p, err := s.findParameter(db, ref)
	if err != nil {
		return domain.Parameter{}, err
	}
	ver, err := resolveParamVersion(db, ref, p.ID, version, label)
	if err != nil {
		return domain.Parameter{}, err
	}
	var pv parameterVersionModel
	if err := db.Where("parameter_id = ? AND version_number = ?", p.ID, ver).First(&pv).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.Parameter{}, domain.Errorf(domain.ErrNotFound, "parameter %s version %d", ref, ver)
		}
		return domain.Parameter{}, err
	}
	labels, err := loadParamLabels(db, p.ID)
	if err != nil {
		return domain.Parameter{}, err
	}
	return domain.Parameter{
		Ref:         ref,
		Value:       pv.Value,
		ContentType: pv.ContentType,
		Version:     ver,
		Metadata:    pv.MetadataJSON,
		CreatedBy:   pv.CreatedBy,
		CreatedAt:   parseTime(pv.CreatedAt),
		Labels:      labels,
	}, nil
}

// GetParameterInfo returns parameter-level metadata plus version history.
func (s *SQLStore) GetParameterInfo(ctx context.Context, ref domain.Ref) (domain.ParameterInfo, error) {
	db := s.db.WithContext(ctx)
	p, err := s.findParameter(db, ref)
	if err != nil {
		return domain.ParameterInfo{}, err
	}
	var vers []parameterVersionModel
	if err := db.Where("parameter_id = ?", p.ID).Order("version_number ASC").Find(&vers).Error; err != nil {
		return domain.ParameterInfo{}, err
	}
	labels, err := loadParamLabels(db, p.ID)
	if err != nil {
		return domain.ParameterInfo{}, err
	}
	vinfos := make([]domain.ParameterVersionInfo, 0, len(vers))
	for _, v := range vers {
		vinfos = append(vinfos, domain.ParameterVersionInfo{
			Version:     uint64(v.VersionNumber),
			ContentType: v.ContentType,
			State:       v.State,
			CreatedBy:   v.CreatedBy,
			CreatedAt:   parseTime(v.CreatedAt),
			Metadata:    v.MetadataJSON,
		})
	}
	return domain.ParameterInfo{
		Ref:         ref,
		ContentType: p.ContentType,
		Metadata:    p.MetadataJSON,
		CreatedAt:   parseTime(p.CreatedAt),
		UpdatedAt:   parseTime(p.UpdatedAt),
		Labels:      labels,
		Versions:    vinfos,
	}, nil
}

// currentParameter loads a parameter at its "current" label. It returns
// (_, false, nil) when the parameter has no current label or version row.
func (s *SQLStore) currentParameter(ctx context.Context, ns domain.NamespaceRef, p parameterModel) (domain.Parameter, bool, error) {
	db := s.db.WithContext(ctx)
	var lm parameterLabelModel
	err := db.Where("parameter_id = ? AND label = ?", p.ID, domain.LabelCurrent).First(&lm).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Parameter{}, false, nil
	}
	if err != nil {
		return domain.Parameter{}, false, err
	}
	var pv parameterVersionModel
	err = db.Where("parameter_id = ? AND version_number = ?", p.ID, lm.VersionNumber).First(&pv).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Parameter{}, false, nil
	}
	if err != nil {
		return domain.Parameter{}, false, err
	}
	labels, err := loadParamLabels(db, p.ID)
	if err != nil {
		return domain.Parameter{}, false, err
	}
	return domain.Parameter{
		Ref:         domain.Ref{NS: ns, Key: p.Name},
		Value:       pv.Value,
		ContentType: pv.ContentType,
		Version:     uint64(lm.VersionNumber),
		Metadata:    pv.MetadataJSON,
		CreatedBy:   pv.CreatedBy,
		CreatedAt:   parseTime(pv.CreatedAt),
		Labels:      labels,
	}, true, nil
}

// ListParameters returns each parameter in ns matching keyPrefix at its
// "current" label, ordered by key. The namespace must exist.
func (s *SQLStore) ListParameters(ctx context.Context, ns domain.NamespaceRef, keyPrefix string, page ListPage) ([]domain.Parameter, string, error) {
	limit := clampLimit(page.Limit)
	after, err := decodeToken(page.Token)
	if err != nil {
		return nil, "", err
	}
	nsID, err := resolveNamespaceID(s.db.WithContext(ctx), ns)
	if err != nil {
		return nil, "", err
	}
	q := s.db.WithContext(ctx).Model(&parameterModel{}).Where("namespace_id = ?", nsID)
	if after != "" {
		q = q.Where("name > ?", after)
	}
	q = applyKeyPrefix(q, "name", keyPrefix)

	var rows []parameterModel
	if err := q.Order("name ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeToken(rows[len(rows)-1].Name)
	}

	out := make([]domain.Parameter, 0, len(rows))
	for _, p := range rows {
		param, ok, err := s.currentParameter(ctx, ns, p)
		if err != nil {
			return nil, "", err
		}
		if ok {
			out = append(out, param)
		}
	}
	return out, next, nil
}

// DeleteParameter removes the parameter and all versions and appends a
// change-log entry.
func (s *SQLStore) DeleteParameter(ctx context.Context, ref domain.Ref) (uint64, error) {
	var revision uint64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		p, err := s.findParameter(tx, ref)
		if err != nil {
			return err
		}
		if err := rejectProtectedReleaseReference(tx, ref, domain.ReleaseEntryParameter, 0); err != nil {
			return err
		}
		if err := tx.Where("parameter_id = ?", p.ID).Delete(&parameterLabelModel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("parameter_id = ?", p.ID).Delete(&parameterVersionModel{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&parameterModel{}, p.ID).Error; err != nil {
			return err
		}
		rev, err := appendChange(tx, &changeLogModel{
			ResourceType: domain.ResourceParameter,
			Env:          ref.NS.Env,
			App:          ref.NS.App,
			Key:          ref.Key,
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
