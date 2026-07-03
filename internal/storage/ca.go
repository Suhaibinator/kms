package storage

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/Suhaibinator/kms/internal/domain"
)

// ---- built-in CA keys -----------------------------------------------------

// InsertCAKey stores a CA key as the active one and retires every other CA key
// in the same transaction (retire-on-rotate). Private key material is stored
// envelope-encrypted; this layer never sees plaintext.
func (s *SQLStore) InsertCAKey(ctx context.Context, ca CAKeyRecord) error {
	created := ca.CreatedAt
	if created.IsZero() {
		created = nowUTC()
	}
	m := caKeyModel{
		ID:           ca.ID,
		CertPEM:      ca.CertPEM,
		EncryptedKey: ca.EncryptedKey,
		EncryptedDEK: ca.EncryptedDEK,
		KEKID:        ca.KEKID,
		State:        domain.KeyStateActive,
		CreatedAt:    fmtTime(created),
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&m).Error; err != nil {
			if isUniqueErr(err) {
				return domain.Errorf(domain.ErrAlreadyExists, "ca key %s", ca.ID)
			}
			return err
		}
		return tx.Model(&caKeyModel{}).Where("id <> ?", ca.ID).
			Update("state", domain.KeyStateRetired).Error
	})
}

// ActiveCAKey returns the single active CA key, or domain.ErrNotFound when the
// CA has not been generated yet.
func (s *SQLStore) ActiveCAKey(ctx context.Context) (CAKeyRecord, error) {
	var m caKeyModel
	err := s.db.WithContext(ctx).Where("state = ?", domain.KeyStateActive).
		Order("created_at DESC, id DESC").First(&m).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return CAKeyRecord{}, domain.Errorf(domain.ErrNotFound, "no active ca key")
		}
		return CAKeyRecord{}, err
	}
	return toCAKeyRecord(m), nil
}

// ---- issued client certificates -------------------------------------------

// InsertIdentityCert records a certificate issued to identityName. The identity
// must exist.
func (s *SQLStore) InsertIdentityCert(ctx context.Context, identityName string, cert domain.IdentityCert) error {
	created := cert.CreatedAt
	if created.IsZero() {
		created = nowUTC()
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var idn identityModel
		if err := tx.Select("id").Where("name = ?", identityName).First(&idn).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.Errorf(domain.ErrNotFound, "identity %s", identityName)
			}
			return err
		}
		m := identityCertModel{
			Serial:      cert.Serial,
			IdentityID:  idn.ID,
			Fingerprint: cert.Fingerprint,
			NotAfter:    fmtTime(cert.NotAfter),
			RevokedAt:   fmtTimePtr(cert.RevokedAt),
			CreatedAt:   fmtTime(created),
		}
		if err := tx.Omit("Identity").Create(&m).Error; err != nil {
			if isUniqueErr(err) {
				return domain.Errorf(domain.ErrAlreadyExists, "certificate %s", cert.Serial)
			}
			return err
		}
		return nil
	})
}

// ListIdentityCerts returns every certificate issued to identityName, oldest
// first. The identity must exist.
func (s *SQLStore) ListIdentityCerts(ctx context.Context, identityName string) ([]domain.IdentityCert, error) {
	db := s.db.WithContext(ctx)
	var idn identityModel
	if err := db.Select("id").Where("name = ?", identityName).First(&idn).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.Errorf(domain.ErrNotFound, "identity %s", identityName)
		}
		return nil, err
	}
	byID, err := certsForIdentityIDs(db, []int64{idn.ID})
	if err != nil {
		return nil, err
	}
	return byID[idn.ID], nil
}

// GetIdentityCertBySerial returns the certificate with the given serial along
// with its owning identity's name and disabled flag — everything the auth
// interceptor needs to accept or reject a presented client certificate.
func (s *SQLStore) GetIdentityCertBySerial(ctx context.Context, serial string) (IdentityCertRecord, error) {
	// Flat row: embedding identityCertModel would be read by GORM as an
	// association, not flattened columns, so the fields are listed explicitly.
	var row struct {
		Serial           string
		Fingerprint      string
		NotAfter         string
		RevokedAt        *string
		CreatedAt        string
		IdentityName     string
		IdentityDisabled int64
	}
	err := s.db.WithContext(ctx).Table("identity_certs AS c").
		Select("c.serial, c.fingerprint, c.not_after, c.revoked_at, c.created_at, "+
			"i.name AS identity_name, i.disabled AS identity_disabled").
		Joins("JOIN identities i ON i.id = c.identity_id").
		Where("c.serial = ?", serial).
		Scan(&row).Error
	if err != nil {
		return IdentityCertRecord{}, err
	}
	if row.Serial == "" {
		return IdentityCertRecord{}, domain.Errorf(domain.ErrNotFound, "certificate %s", serial)
	}
	return IdentityCertRecord{
		Cert: toIdentityCert(identityCertModel{
			Serial:      row.Serial,
			Fingerprint: row.Fingerprint,
			NotAfter:    row.NotAfter,
			RevokedAt:   row.RevokedAt,
			CreatedAt:   row.CreatedAt,
		}),
		IdentityName:     row.IdentityName,
		IdentityDisabled: i2b(row.IdentityDisabled),
	}, nil
}

// RevokeIdentityCert marks a certificate revoked. It is idempotent: revoking an
// already-revoked certificate is a no-op. Unknown serials are ErrNotFound.
func (s *SQLStore) RevokeIdentityCert(ctx context.Context, serial string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var m identityCertModel
		if err := tx.Where("serial = ?", serial).First(&m).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.Errorf(domain.ErrNotFound, "certificate %s", serial)
			}
			return err
		}
		if m.RevokedAt != nil {
			return nil
		}
		return tx.Model(&identityCertModel{}).Where("serial = ?", serial).
			Update("revoked_at", fmtTime(time.Now())).Error
	})
}

// certsForIdentityIDs batch-loads certificate summaries for a set of identity
// primary keys, grouped by identity id and ordered oldest first.
func certsForIdentityIDs(db *gorm.DB, ids []int64) (map[int64][]domain.IdentityCert, error) {
	out := map[int64][]domain.IdentityCert{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []identityCertModel
	if err := db.Where("identity_id IN ?", ids).Order("created_at ASC, serial ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, m := range rows {
		out[m.IdentityID] = append(out[m.IdentityID], toIdentityCert(m))
	}
	return out, nil
}
