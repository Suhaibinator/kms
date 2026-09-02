package core

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// PostureListLimit caps each expiring list in a SecurityPosture. The posture
// view answers "what needs attention", not "what exists": past a couple of
// hundred rows the answer is the identity or secret listing, not a longer
// card. Each list still carries the true count beside it, so a capped list can
// say that it is capped.
const PostureListLimit = 200

// AdminCertPostureWindow is how far ahead the posture looks for expiring admin
// certificates. It is fixed rather than caller-chosen so the page, the startup
// warning, and the kms_admin_certs_expiring_soon gauge always agree about
// which certificates count as expiring (serve's adminCertExpiryWarning).
const AdminCertPostureWindow = 14 * 24 * time.Hour

// PostureWindows records the look-ahead each part of the snapshot used, so a
// reader never has to guess what "expiring" meant.
type PostureWindows struct {
	Cert      time.Duration
	Secret    time.Duration
	AdminCert time.Duration
}

// KEKPosture describes the active key-encryption key by metadata alone: which
// generation is active, when it was minted, and how many generations the store
// has ever held. Never any key material — this is read straight from the
// metadata rows, without touching a keyring.
type KEKPosture struct {
	ActiveID    string
	CreatedAt   time.Time
	Generations int
}

// ExpiringIdentityCerts is a bounded list plus the full count behind it.
// Truncated says the two disagree, so a caller can render "showing the first
// N" rather than silently under-reporting.
type ExpiringIdentityCerts struct {
	Items     []storage.ExpiringIdentityCert
	Total     int64
	Truncated bool
}

// ExpiringSecretVersions is the same bounded-list shape for secret versions.
type ExpiringSecretVersions struct {
	Items     []storage.ExpiringSecretVersion
	Total     int64
	Truncated bool
}

// ChangeLogPosture is the replay window a reconnecting subscriber can be served
// from: how many rows are retained and the revision range they span.
type ChangeLogPosture struct {
	Rows           int64
	LastRevision   int64
	OldestRevision int64
}

// SecurityPosture is the expiry-and-key snapshot behind GET /api/v1/posture.
//
// Every field is metadata: counts, identity names, certificate serials and
// expiry instants, secret addresses. No secret value, bearer token, token
// hash, key material, private key, or certificate PEM is reachable from it,
// and gathering one reads metadata rows only — it never unwraps a DEK or
// enters a decrypt path. Keep that true of anything added here: the page is
// shown to every admin, and its whole value is that it can be.
type SecurityPosture struct {
	GeneratedAt            time.Time
	Windows                PostureWindows
	KEK                    KEKPosture
	AdminCertsLacking      []string
	AdminCertsExpiring     []ExpiringAdminCert
	IdentityCertsExpiring  ExpiringIdentityCerts
	SecretVersionsExpiring ExpiringSecretVersions
	ChangeLog              ChangeLogPosture
}

// SecurityPosture gathers the snapshot above. Admin-only: it aggregates the
// whole store's certificate and expiry state across every namespace, which no
// delegated policy grant scopes, so there is no non-admin path to it.
//
// certWindow and secretWindow are the caller's look-ahead for identity
// certificates and secret versions; the admin-certificate window is fixed
// (AdminCertPostureWindow). The store's list and count capabilities are
// optional (storage.ExpiringListStore, storage.OperationalStatsStore): a store
// without them contributes empty lists and zero counts rather than an error,
// exactly as the metrics sampler treats them.
func (s *Service) SecurityPosture(ctx context.Context, pr Principal, certWindow, secretWindow time.Duration) (SecurityPosture, error) {
	if err := s.requireAdmin(ctx, pr, "posture.read", "posture", ""); err != nil {
		return SecurityPosture{}, err
	}
	now := s.now()
	posture := SecurityPosture{
		GeneratedAt: now,
		Windows:     PostureWindows{Cert: certWindow, Secret: secretWindow, AdminCert: AdminCertPostureWindow},
	}

	keys, err := s.store.ListKeyMetadata(ctx)
	if err != nil {
		return SecurityPosture{}, err
	}
	posture.KEK.Generations = len(keys)
	for _, k := range keys {
		if k.State == domain.KeyStateActive {
			posture.KEK.ActiveID = k.ID
			posture.KEK.CreatedAt = k.CreatedAt
		}
	}

	// AdminCertReport rather than OperationalReport: the page needs the names
	// and serials themselves, and the report's counts are len() of these.
	lacking, expiring, err := s.AdminCertReport(ctx, AdminCertPostureWindow)
	if err != nil {
		return SecurityPosture{}, err
	}
	// Both lists arrive in identity-listing order; the page reads them as
	// "act on this one first", so soonest expiry leads and names break ties.
	slices.SortFunc(expiring, func(a, b ExpiringAdminCert) int {
		if c := a.NotAfter.Compare(b.NotAfter); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	slices.Sort(lacking)
	posture.AdminCertsLacking = lacking
	posture.AdminCertsExpiring = expiring

	if stats, ok := s.store.(storage.OperationalStatsStore); ok {
		out, serr := stats.OperationalStats(ctx, now, certWindow, secretWindow)
		if serr != nil {
			return SecurityPosture{}, serr
		}
		posture.ChangeLog = ChangeLogPosture{
			Rows:           out.ChangeLogRows,
			LastRevision:   out.ChangeLogLastRevision,
			OldestRevision: out.ChangeLogOldestRevision,
		}
		posture.IdentityCertsExpiring.Total = out.IdentityCertsExpiringSoon
		posture.SecretVersionsExpiring.Total = out.SecretVersionsExpiringSoon
	}

	if lists, ok := s.store.(storage.ExpiringListStore); ok {
		certs, cerr := lists.ExpiringIdentityCerts(ctx, now, certWindow, PostureListLimit)
		if cerr != nil {
			return SecurityPosture{}, cerr
		}
		posture.IdentityCertsExpiring.Items = certs
		versions, verr := lists.ExpiringSecretVersions(ctx, now, secretWindow, PostureListLimit)
		if verr != nil {
			return SecurityPosture{}, verr
		}
		posture.SecretVersionsExpiring.Items = versions
	}
	// The count and the list run as separate statements, so a write landing
	// between them can leave the list longer than the total. Trust the list
	// for the floor and let the count only ever raise it.
	posture.IdentityCertsExpiring.Total = max(posture.IdentityCertsExpiring.Total, int64(len(posture.IdentityCertsExpiring.Items)))
	posture.IdentityCertsExpiring.Truncated = posture.IdentityCertsExpiring.Total > int64(len(posture.IdentityCertsExpiring.Items))
	posture.SecretVersionsExpiring.Total = max(posture.SecretVersionsExpiring.Total, int64(len(posture.SecretVersionsExpiring.Items)))
	posture.SecretVersionsExpiring.Truncated = posture.SecretVersionsExpiring.Total > int64(len(posture.SecretVersionsExpiring.Items))

	return posture, nil
}
