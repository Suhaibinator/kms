package storage

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Suhaibinator/kms/internal/domain"
)

// namespaceRefByID loads the (env, app) of a namespace by primary key.
func namespaceRefByID(db *gorm.DB, id int64) (domain.NamespaceRef, error) {
	var m namespaceModel
	if err := db.Select("env", "app").Where("id = ?", id).First(&m).Error; err != nil {
		return domain.NamespaceRef{}, err
	}
	return domain.NamespaceRef{Env: m.Env, App: m.App}, nil
}

// hydrateIdentity fills the namespace binding (and, when withCerts, the issued
// certificate summaries) onto the base identity mapped from m.
func (s *SQLStore) hydrateIdentity(db *gorm.DB, m identityModel, withCerts bool) (domain.Identity, error) {
	id := toIdentity(m)
	if m.NamespaceID != nil {
		ref, err := namespaceRefByID(db, *m.NamespaceID)
		if err != nil {
			return domain.Identity{}, err
		}
		id.Namespace = &ref
	}
	if withCerts {
		certs, err := certsForIdentityIDs(db, []int64{m.ID})
		if err != nil {
			return domain.Identity{}, err
		}
		id.Certs = certs[m.ID]
	}
	return id, nil
}

// CreateIdentity stores a new identity. TokenHash may be nil (cert-only) and
// Namespace may be nil (unbound). A non-nil Namespace must already exist.
func (s *SQLStore) CreateIdentity(ctx context.Context, params CreateIdentityParams) (domain.Identity, error) {
	m := identityModel{
		Name:         params.Name,
		Kind:         params.Kind,
		TokenHash:    params.TokenHash,
		Disabled:     0,
		CreatedAt:    fmtTime(nowUTC()),
		MetadataJSON: "{}",
	}
	var out domain.Identity
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if params.Namespace != nil {
			nsID, err := resolveNamespaceID(tx, *params.Namespace)
			if err != nil {
				return err
			}
			m.NamespaceID = &nsID
		}
		if err := tx.Omit("Namespace").Create(&m).Error; err != nil {
			if isUniqueErr(err) {
				return domain.Errorf(domain.ErrAlreadyExists, "identity %s", params.Name)
			}
			return err
		}
		if params.Cert != nil {
			cert := identityCertModel{
				Serial:      params.Cert.Serial,
				IdentityID:  m.ID,
				Fingerprint: params.Cert.Fingerprint,
				NotAfter:    fmtTime(params.Cert.NotAfter),
				CreatedAt:   fmtTime(params.Cert.CreatedAt),
			}
			if err := tx.Omit(clause.Associations).Create(&cert).Error; err != nil {
				if isUniqueErr(err) {
					return domain.Errorf(domain.ErrAlreadyExists, "certificate %s", params.Cert.Serial)
				}
				return err
			}
		}
		id, err := s.hydrateIdentity(tx, m, false)
		if err != nil {
			return err
		}
		out = id
		return nil
	})
	if err != nil {
		return domain.Identity{}, err
	}
	return out, nil
}

// GetIdentityByTokenHash returns the identity whose token hash matches. It only
// matches identities that carry a token hash (cert-only identities, whose
// token_hash is NULL, are never returned). Certs are omitted (hot auth path).
func (s *SQLStore) GetIdentityByTokenHash(ctx context.Context, tokenHash []byte) (domain.Identity, error) {
	if len(tokenHash) == 0 {
		return domain.Identity{}, domain.Errorf(domain.ErrNotFound, "identity")
	}
	db := s.db.WithContext(ctx)
	var m identityModel
	if err := db.Where("token_hash = ? AND token_hash IS NOT NULL", tokenHash).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.Identity{}, domain.Errorf(domain.ErrNotFound, "identity")
		}
		return domain.Identity{}, err
	}
	return s.hydrateIdentity(db, m, false)
}

// GetIdentityByName returns the identity with the given name, including its
// issued certificate summaries (admin-facing view).
func (s *SQLStore) GetIdentityByName(ctx context.Context, name string) (domain.Identity, error) {
	db := s.db.WithContext(ctx)
	var m identityModel
	if err := db.Where("name = ?", name).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.Identity{}, domain.Errorf(domain.ErrNotFound, "identity %s", name)
		}
		return domain.Identity{}, err
	}
	return s.hydrateIdentity(db, m, true)
}

// ListIdentities returns identities ordered by name, each with its namespace
// binding and issued certificate summaries.
func (s *SQLStore) ListIdentities(ctx context.Context, page ListPage) ([]domain.Identity, string, error) {
	limit := clampLimit(page.Limit)
	after, err := decodeToken(page.Token)
	if err != nil {
		return nil, "", err
	}
	db := s.db.WithContext(ctx)
	q := db.Model(&identityModel{})
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

	// Batch-load namespaces and certs for the page in one query each.
	nsIDs := make([]int64, 0, len(rows))
	idIDs := make([]int64, 0, len(rows))
	for _, m := range rows {
		if m.NamespaceID != nil {
			nsIDs = append(nsIDs, *m.NamespaceID)
		}
		idIDs = append(idIDs, m.ID)
	}
	nsByID, err := namespaceRefsByIDs(db, nsIDs)
	if err != nil {
		return nil, "", err
	}
	certsByID, err := certsForIdentityIDs(db, idIDs)
	if err != nil {
		return nil, "", err
	}

	out := make([]domain.Identity, 0, len(rows))
	for _, m := range rows {
		id := toIdentity(m)
		if m.NamespaceID != nil {
			if ref, ok := nsByID[*m.NamespaceID]; ok {
				id.Namespace = &ref
			}
		}
		id.Certs = certsByID[m.ID]
		out = append(out, id)
	}
	return out, next, nil
}

// namespaceRefsByIDs loads (env, app) for a set of namespace primary keys.
func namespaceRefsByIDs(db *gorm.DB, ids []int64) (map[int64]domain.NamespaceRef, error) {
	out := map[int64]domain.NamespaceRef{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []namespaceModel
	if err := db.Select("id", "env", "app").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, m := range rows {
		out[m.ID] = domain.NamespaceRef{Env: m.Env, App: m.App}
	}
	return out, nil
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

// UpdateIdentityTokenHash replaces an identity's token hash (nil clears it).
func (s *SQLStore) UpdateIdentityTokenHash(ctx context.Context, name string, tokenHash []byte) error {
	// A map with an explicit nil forces a NULL write; a struct update would skip
	// the zero-value field.
	res := s.db.WithContext(ctx).Model(&identityModel{}).Where("name = ?", name).
		Update("token_hash", tokenHash)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.Errorf(domain.ErrNotFound, "identity %s", name)
	}
	return nil
}
