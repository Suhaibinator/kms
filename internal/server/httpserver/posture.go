package httpserver

import (
	"net/http"
	"time"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/storage"
)

// The security-posture endpoint and its DTOs. Unlike the rest of the API these
// timestamps are RFC 3339 UTC strings rather than *_unix_ms: the response is
// read by an operator as much as by the console, and the windows beside them
// are Go durations, so the whole snapshot stays in one vocabulary.
//
// The response is metadata about credentials, never credentials. No field here
// carries a secret value, a bearer token or its hash, key material, a private
// key, or a certificate PEM; identities appear by name and certificates by
// serial and expiry. Building it reads metadata rows only — no decrypt path is
// on the way. Anything added here has to hold that line.

// postureMaxWindow bounds the caller's look-ahead. A year is already past the
// point where "expiring soon" means anything, and the cap keeps a query from
// being asked to sort the entire certificate and secret history.
const postureMaxWindow = 365 * 24 * time.Hour

// postureDefaultWindow is the look-ahead when the caller names none. It is the
// window the kms_*_expiring_soon gauges sample on, so an untouched page and the
// dashboard agree.
const postureDefaultWindow = 30 * 24 * time.Hour

type postureWindowsDTO struct {
	Cert      string `json:"cert"`
	Secret    string `json:"secret"`
	AdminCert string `json:"admin_cert"`
}

type postureKEKDTO struct {
	ActiveID   string `json:"active_id"`
	CreatedAt  string `json:"created_at"`
	AgeSeconds int64  `json:"age_seconds"`
	// Generations counts every KEK the store has ever recorded, active and
	// retired: one means the key has never been rotated.
	Generations int `json:"generations"`
}

// postureAuthDTO is the listener's authentication posture, as configured for
// this process. It repeats what /api/v1/health tells an unauthenticated caller
// and adds mtls_enabled, which only an admin has a use for.
type postureAuthDTO struct {
	TLSEnabled              bool `json:"tls_enabled"`
	MTLSEnabled             bool `json:"mtls_enabled"`
	AdminClientCertRequired bool `json:"admin_client_cert_required"`
}

type postureAuditDTO struct {
	Enabled bool `json:"enabled"`
	// RetainDuration is "forever" when nothing is ever retired, matching the
	// word the startup log uses; a bare "0s" would read like a misconfiguration.
	RetainDuration string `json:"retain_duration"`
	ArchiveEnabled bool   `json:"archive_enabled"`
}

type expiringAdminCertDTO struct {
	Identity string `json:"identity"`
	Serial   string `json:"serial"`
	NotAfter string `json:"not_after"`
}

type postureAdminCertsDTO struct {
	// Lacking names enabled admins with no valid client certificate. They
	// cannot authenticate at all while the requirement is enforced.
	Lacking  []string               `json:"lacking"`
	Expiring []expiringAdminCertDTO `json:"expiring"`
}

type expiringIdentityCertDTO struct {
	Identity string `json:"identity"`
	Env      string `json:"env"`
	App      string `json:"app"`
	Serial   string `json:"serial"`
	NotAfter string `json:"not_after"`
}

// postureIdentityCertsDTO is a bounded list plus the true count behind it.
// truncated says the two disagree, so the console can say "showing the first N"
// instead of quietly under-reporting.
type postureIdentityCertsDTO struct {
	Items     []expiringIdentityCertDTO `json:"items"`
	Total     int64                     `json:"total"`
	Truncated bool                      `json:"truncated"`
}

type expiringSecretVersionDTO struct {
	Env       string `json:"env"`
	App       string `json:"app"`
	Key       string `json:"key"`
	Version   uint64 `json:"version"`
	ExpiresAt string `json:"expires_at"`
}

type postureSecretVersionsDTO struct {
	Items     []expiringSecretVersionDTO `json:"items"`
	Total     int64                      `json:"total"`
	Truncated bool                       `json:"truncated"`
}

type postureChangeLogDTO struct {
	Rows           int64 `json:"rows"`
	LastRevision   int64 `json:"last_revision"`
	OldestRevision int64 `json:"oldest_revision"`
}

type postureDTO struct {
	GeneratedAt            string                   `json:"generated_at"`
	Windows                postureWindowsDTO        `json:"windows"`
	KEK                    postureKEKDTO            `json:"kek"`
	Auth                   postureAuthDTO           `json:"auth"`
	Audit                  postureAuditDTO          `json:"audit"`
	MetricsEnabled         bool                     `json:"metrics_enabled"`
	AdminCerts             postureAdminCertsDTO     `json:"admin_certs"`
	IdentityCertsExpiring  postureIdentityCertsDTO  `json:"identity_certs_expiring"`
	SecretVersionsExpiring postureSecretVersionsDTO `json:"secret_versions_expiring"`
	ChangeLog              postureChangeLogDTO      `json:"changelog"`
}

// handlePosture renders the security-posture snapshot: what is about to
// expire, how old the active KEK is, and whether admin authentication is in
// its strong posture. Admin-only (core.SecurityPosture gates it), so an
// unauthenticated caller gets 401 from the API pipeline and a non-admin
// identity 403 from the gate.
//
// cert_window and secret_window take a Go duration ("720h") or a bare day
// count ("30d"); both default to 30 days. The admin-certificate window is not
// a parameter — it is pinned to the value serve warns on.
func (s *server) handlePosture(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	certWindow, err := parseWindow("cert_window", q.Get("cert_window"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	secretWindow, err := parseWindow("secret_window", q.Get("secret_window"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	posture, err := s.svc.SecurityPosture(r.Context(), principalFrom(r.Context()), certWindow, secretWindow)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toPostureDTO(posture))
}

func (s *server) toPostureDTO(p core.SecurityPosture) postureDTO {
	return postureDTO{
		GeneratedAt: rfc3339(p.GeneratedAt),
		Windows: postureWindowsDTO{
			Cert:      p.Windows.Cert.String(),
			Secret:    p.Windows.Secret.String(),
			AdminCert: p.Windows.AdminCert.String(),
		},
		KEK: postureKEKDTO{
			ActiveID:    p.KEK.ActiveID,
			CreatedAt:   rfc3339(p.KEK.CreatedAt),
			AgeSeconds:  ageSeconds(p.KEK.CreatedAt, p.GeneratedAt),
			Generations: p.KEK.Generations,
		},
		Auth: postureAuthDTO{
			TLSEnabled:              s.cfg.TLSEnabled,
			MTLSEnabled:             s.cfg.MTLSEnabled,
			AdminClientCertRequired: s.cfg.AdminClientCertRequired,
		},
		Audit: postureAuditDTO{
			Enabled:        s.cfg.AuditEnabled,
			RetainDuration: retainDescription(s.cfg.AuditRetainDuration),
			ArchiveEnabled: s.cfg.AuditArchiveEnabled,
		},
		// The exporter exists exactly when metrics are configured on, so its
		// presence is the effective setting rather than a second copy of it.
		MetricsEnabled: s.cfg.Metrics != nil,
		AdminCerts: postureAdminCertsDTO{
			Lacking:  nonNilStrings(p.AdminCertsLacking),
			Expiring: toExpiringAdminCertDTOs(p.AdminCertsExpiring),
		},
		IdentityCertsExpiring: postureIdentityCertsDTO{
			Items:     toExpiringIdentityCertDTOs(p.IdentityCertsExpiring.Items),
			Total:     p.IdentityCertsExpiring.Total,
			Truncated: p.IdentityCertsExpiring.Truncated,
		},
		SecretVersionsExpiring: postureSecretVersionsDTO{
			Items:     toExpiringSecretVersionDTOs(p.SecretVersionsExpiring.Items),
			Total:     p.SecretVersionsExpiring.Total,
			Truncated: p.SecretVersionsExpiring.Truncated,
		},
		ChangeLog: postureChangeLogDTO{
			Rows:           p.ChangeLog.Rows,
			LastRevision:   p.ChangeLog.LastRevision,
			OldestRevision: p.ChangeLog.OldestRevision,
		},
	}
}

func toExpiringAdminCertDTOs(in []core.ExpiringAdminCert) []expiringAdminCertDTO {
	out := make([]expiringAdminCertDTO, 0, len(in))
	for _, c := range in {
		out = append(out, expiringAdminCertDTO{Identity: c.Name, Serial: c.Serial, NotAfter: rfc3339(c.NotAfter)})
	}
	return out
}

func toExpiringIdentityCertDTOs(in []storage.ExpiringIdentityCert) []expiringIdentityCertDTO {
	out := make([]expiringIdentityCertDTO, 0, len(in))
	for _, c := range in {
		out = append(out, expiringIdentityCertDTO{
			Identity: c.Identity, Env: c.Env, App: c.App,
			Serial: c.Serial, NotAfter: rfc3339(c.NotAfter),
		})
	}
	return out
}

func toExpiringSecretVersionDTOs(in []storage.ExpiringSecretVersion) []expiringSecretVersionDTO {
	out := make([]expiringSecretVersionDTO, 0, len(in))
	for _, v := range in {
		out = append(out, expiringSecretVersionDTO{
			Env: v.Env, App: v.App, Key: v.Key,
			Version: v.Version, ExpiresAt: rfc3339(v.ExpiresAt),
		})
	}
	return out
}

// nonNilStrings guarantees the JSON renders an empty array rather than null.
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// rfc3339 renders an instant for the posture wire; the zero time is "".
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ageSeconds is how long ago t was as of now, floored at 0. An unknown or
// future instant is 0 rather than a negative age.
func ageSeconds(t, now time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	d := now.Sub(t)
	if d <= 0 {
		return 0
	}
	return int64(d.Seconds())
}

// retainDescription spells an audit retention window, matching the wording of
// serve's startup line: zero keeps history forever.
func retainDescription(retain time.Duration) string {
	if retain <= 0 {
		return "forever"
	}
	return retain.String()
}
