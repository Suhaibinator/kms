package httpserver

import (
	"net/http"
	"time"

	"github.com/Suhaibinator/kms/internal/ca"
	"github.com/Suhaibinator/kms/internal/core"
)

type connectionCertificate struct {
	IdentityURI       *string `json:"identity_uri"`
	FingerprintSHA256 string  `json:"fingerprint_sha256"`
	NotAfter          string  `json:"not_after"`
}

// handleConnection reflects transport evidence only, never credential acceptance
// or account state. It must remain independent of readiness and authentication.
func (s *server) handleConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeErrorCode(w, http.StatusMethodNotAllowed, "invalid_argument", "method not allowed")
		return
	}
	var details *connectionCertificate
	if cert := peerCertFromRequest(r); cert != nil {
		details = &connectionCertificate{
			FingerprintSHA256: core.CertFingerprint(cert),
			NotAfter:          cert.NotAfter.UTC().Format(time.RFC3339),
		}
		if name, err := ca.IdentityFromCert(cert); err == nil {
			uri := "kms://identity/" + name
			details.IdentityURI = &uri
		}
	}
	writeJSON(w, http.StatusOK, struct {
		TLSEnabled        bool                   `json:"tls_enabled"`
		ClientCertificate *connectionCertificate `json:"client_certificate"`
	}{r.TLS != nil, details})
}
