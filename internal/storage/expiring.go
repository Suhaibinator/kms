package storage

import (
	"context"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// ExpiringIdentityCert names one unrevoked issued certificate whose lifetime
// ends inside the posture window: the owning identity and its namespace, so an
// operator knows what to re-issue, plus the serial and expiry, which is all a
// posture view ever shows of a certificate. There is deliberately no field for
// a PEM, a public key, or a fingerprint — this describes certificates, it does
// not hand them out.
type ExpiringIdentityCert struct {
	Identity string
	// Env and App are the identity's bound namespace, empty for an unbound
	// (admin or tooling) identity.
	Env      string
	App      string
	Serial   string
	NotAfter time.Time
}

// ExpiringSecretVersion addresses one enabled secret version whose expires_at
// falls inside the posture window. An address and an instant only: no
// ciphertext, wrapped DEK, or wrap mode, so nothing here brings a decrypt any
// closer.
type ExpiringSecretVersion struct {
	Env       string
	App       string
	Key       string
	Version   uint64
	ExpiresAt time.Time
}

// ExpiringListStore is the list half of OperationalStatsStore's counts: the
// same two windows and the same filters, resolved to the rows behind the
// numbers so the console can name what is about to expire rather than only
// count it. It is optional for the same reason (see OperationalStatsStore) —
// one read-only endpoint calls it, and requiring it of every implementation
// would force test and fixture stores to grow methods nothing in the request
// path uses. Callers type-assert for it and report empty lists when absent.
//
// Both methods take the cap the caller intends to render. Bounding the query
// rather than the result keeps a store that has drifted into tens of thousands
// of expiring rows from materializing all of them for a page that shows 200.
type ExpiringListStore interface {
	ExpiringIdentityCerts(ctx context.Context, now time.Time, within time.Duration, limit int) ([]ExpiringIdentityCert, error)
	ExpiringSecretVersions(ctx context.Context, now time.Time, within time.Duration, limit int) ([]ExpiringSecretVersion, error)
}

var _ ExpiringListStore = (*SQLStore)(nil)

// expiringDefaultLimit applies when a caller passes a non-positive limit, so a
// mistake there cannot turn into an unbounded scan.
const expiringDefaultLimit = 200

// ExpiringIdentityCerts lists unrevoked certificates with a not_after in
// [now, now+within], soonest first. Already-expired certificates are excluded
// for the reason the matching count excludes them: they are a past-tense
// problem the handshake already enforces, and listing them forever would bury
// the ones an operator can still act on.
//
// The window comparison is a text range filter, exact rather than approximate:
// stored timestamps are fixed-width RFC 3339 UTC (see tsLayout), so
// lexicographic ordering is chronological ordering — which is also what makes
// ORDER BY not_after the true expiry order.
func (s *SQLStore) ExpiringIdentityCerts(ctx context.Context, now time.Time, within time.Duration, limit int) ([]ExpiringIdentityCert, error) {
	if now.IsZero() {
		now = nowUTC()
	}
	if limit <= 0 {
		limit = expiringDefaultLimit
	}
	// Flat row: embedding the models would be read by GORM as associations
	// rather than flattened columns (see GetIdentityCertBySerial).
	var rows []struct {
		Identity string
		Env      string
		App      string
		Serial   string
		NotAfter string
	}
	err := s.db.WithContext(ctx).Table("identity_certs AS c").
		Select("i.name AS identity, COALESCE(n.env, '') AS env, COALESCE(n.app, '') AS app, "+
			"c.serial AS serial, c.not_after AS not_after").
		Joins("JOIN identities i ON i.id = c.identity_id").
		// Unbound identities have no namespace row; they still hold certificates.
		Joins("LEFT JOIN namespaces n ON n.id = i.namespace_id").
		Where("c.revoked_at IS NULL AND c.not_after >= ? AND c.not_after <= ?",
			fmtTime(now), fmtTime(now.Add(within))).
		// The serial breaks ties so the page is stable across reloads.
		Order("c.not_after ASC, c.serial ASC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]ExpiringIdentityCert, 0, len(rows))
	for _, r := range rows {
		out = append(out, ExpiringIdentityCert{
			Identity: r.Identity, Env: r.Env, App: r.App,
			Serial: r.Serial, NotAfter: parseTime(r.NotAfter),
		})
	}
	return out, nil
}

// ExpiringSecretVersions lists enabled secret versions with an expires_at in
// [now, now+within], soonest first. Disabled and destroyed versions are
// excluded: their expiry is no longer anyone's problem.
func (s *SQLStore) ExpiringSecretVersions(ctx context.Context, now time.Time, within time.Duration, limit int) ([]ExpiringSecretVersion, error) {
	if now.IsZero() {
		now = nowUTC()
	}
	if limit <= 0 {
		limit = expiringDefaultLimit
	}
	var rows []struct {
		Env       string
		App       string
		SecretKey string
		Version   int64
		ExpiresAt string
	}
	err := s.db.WithContext(ctx).Table("secret_versions AS v").
		Select("n.env AS env, n.app AS app, s.name AS secret_key, "+
			"v.version_number AS version, v.expires_at AS expires_at").
		Joins("JOIN secrets s ON s.id = v.secret_id").
		Joins("JOIN namespaces n ON n.id = s.namespace_id").
		Where("v.state = ? AND v.expires_at IS NOT NULL AND v.expires_at >= ? AND v.expires_at <= ?",
			domain.StateEnabled, fmtTime(now), fmtTime(now.Add(within))).
		Order("v.expires_at ASC, n.env ASC, n.app ASC, s.name ASC, v.version_number ASC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]ExpiringSecretVersion, 0, len(rows))
	for _, r := range rows {
		out = append(out, ExpiringSecretVersion{
			Env: r.Env, App: r.App, Key: r.SecretKey,
			Version: uint64(r.Version), ExpiresAt: parseTime(r.ExpiresAt),
		})
	}
	return out, nil
}
