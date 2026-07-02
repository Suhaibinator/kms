package storage

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/Suhaibinator/kms/internal/domain"
)

// CreateIdentity stores a new identity.
func (s *SQLStore) CreateIdentity(ctx context.Context, name, kind string, tokenHash []byte) (domain.Identity, error) {
	m := identityModel{
		Name:         name,
		Kind:         kind,
		TokenHash:    tokenHash,
		Disabled:     0,
		CreatedAt:    fmtTime(nowUTC()),
		MetadataJSON: "{}",
	}
	if err := s.db.WithContext(ctx).Create(&m).Error; err != nil {
		if isUniqueErr(err) {
			return domain.Identity{}, domain.Errorf(domain.ErrAlreadyExists, "identity %s", name)
		}
		return domain.Identity{}, err
	}
	return toIdentity(m), nil
}

// GetIdentityByTokenHash returns the identity whose token hash matches.
func (s *SQLStore) GetIdentityByTokenHash(ctx context.Context, tokenHash []byte) (domain.Identity, error) {
	var m identityModel
	if err := s.db.WithContext(ctx).Where("token_hash = ?", tokenHash).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.Identity{}, domain.Errorf(domain.ErrNotFound, "identity")
		}
		return domain.Identity{}, err
	}
	return toIdentity(m), nil
}

// GetIdentityByName returns the identity with the given name.
func (s *SQLStore) GetIdentityByName(ctx context.Context, name string) (domain.Identity, error) {
	var m identityModel
	if err := s.db.WithContext(ctx).Where("name = ?", name).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.Identity{}, domain.Errorf(domain.ErrNotFound, "identity %s", name)
		}
		return domain.Identity{}, err
	}
	return toIdentity(m), nil
}

// ListIdentities returns identities ordered by name.
func (s *SQLStore) ListIdentities(ctx context.Context, page ListPage) ([]domain.Identity, string, error) {
	limit := clampLimit(page.Limit)
	after, err := decodeToken(page.Token)
	if err != nil {
		return nil, "", err
	}
	q := s.db.WithContext(ctx).Model(&identityModel{})
	if after != "" {
		q = q.Where("name > ?", after)
	}
	var rows []identityModel
	if err := q.Order("name ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeToken(rows[len(rows)-1].Name)
	}
	out := make([]domain.Identity, 0, len(rows))
	for _, m := range rows {
		out = append(out, toIdentity(m))
	}
	return out, next, nil
}

// SetIdentityDisabled toggles an identity's disabled flag.
func (s *SQLStore) SetIdentityDisabled(ctx context.Context, name string, disabled bool) error {
	res := s.db.WithContext(ctx).Model(&identityModel{}).Where("name = ?", name).Update("disabled", b2i(disabled))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.Errorf(domain.ErrNotFound, "identity %s", name)
	}
	return nil
}

// UpdateIdentityTokenHash replaces an identity's token hash.
func (s *SQLStore) UpdateIdentityTokenHash(ctx context.Context, name string, tokenHash []byte) error {
	res := s.db.WithContext(ctx).Model(&identityModel{}).Where("name = ?", name).Update("token_hash", tokenHash)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.Errorf(domain.ErrNotFound, "identity %s", name)
	}
	return nil
}
