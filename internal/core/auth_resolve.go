package core

import (
	"context"
	"crypto/x509"
	"strings"

	"github.com/Suhaibinator/kms/internal/domain"
)

// CredentialInput carries the raw credentials a transport extracted from one
// request, before any verification. Transports build every Principal through
// ResolvePrincipal so credential combination and the admin client-certificate
// requirement are enforced in exactly one place.
type CredentialInput struct {
	// Token is the bearer token as presented (surrounding whitespace is
	// trimmed); empty when none was sent.
	Token string
	// PeerCert is the leaf client certificate the TLS layer chain-verified
	// against the listener's client-CA pool, or nil when the peer presented
	// none. Transports must only pass a certificate with a verified chain.
	PeerCert *x509.Certificate
	// SecretToken is the optional per-secret access token header.
	SecretToken string
	RemoteAddr  string
	UserAgent   string
	RequestID   string
}

// ResolvePrincipal verifies the presented credentials and combines them into a
// Principal. A client certificate and a bearer token are verified
// independently and an invalid one is dropped (VerifyClientCert and
// Authenticate each audit their own failure), so a client that also holds a
// valid token can still authenticate where the namespace admits the token
// method. When both are valid they must name the same identity; the result
// then carries Method mtls (proof of possession, the stronger method) and
// retains the token so long-lived streams can re-check it. The admin
// admission rule (admitAdmin) runs last on the combined principal. Errors are
// generic and never reveal which credential was wrong.
func (s *Service) ResolvePrincipal(ctx context.Context, in CredentialInput) (Principal, error) {
	token := strings.TrimSpace(in.Token)
	var (
		certID  domain.Identity
		certOK  bool
		tokenID domain.Identity
		tokenOK bool
	)
	if in.PeerCert != nil {
		if id, err := s.VerifyClientCert(ctx, in.PeerCert, in.RemoteAddr, in.UserAgent); err == nil {
			certID, certOK = id, true
		}
	}
	if token != "" {
		if id, err := s.Authenticate(ctx, token, in.RemoteAddr, in.UserAgent); err == nil {
			tokenID, tokenOK = id, true
		}
	}

	pr := Principal{
		SecretToken: in.SecretToken,
		RemoteAddr:  in.RemoteAddr,
		UserAgent:   in.UserAgent,
		RequestID:   in.RequestID,
	}
	switch {
	case certOK && tokenOK:
		if certID.Name != tokenID.Name {
			s.audit(ctx, domain.AuditEvent{
				EventType: "auth.failure",
				ActorType: "unknown",
				Decision:  "deny",
				SourceIP:  in.RemoteAddr,
				UserAgent: in.UserAgent,
				RequestID: in.RequestID,
				Metadata:  encodeMeta(map[string]string{"method": "mtls+token", "reason": "credential_mismatch"}),
			})
			return Principal{}, domain.Errorf(domain.ErrUnauthenticated, "invalid credentials")
		}
		pr.Identity = certID
		pr.Method = domain.AuthMethodMTLS
		pr.Token = token
		pr.Serial = CertSerial(in.PeerCert)
		pr.Fingerprint = CertFingerprint(in.PeerCert)
	case certOK:
		pr.Identity = certID
		pr.Method = domain.AuthMethodMTLS
		pr.Serial = CertSerial(in.PeerCert)
		pr.Fingerprint = CertFingerprint(in.PeerCert)
	case tokenOK:
		pr.Identity = tokenID
		pr.Method = domain.AuthMethodToken
		pr.Token = token
	default:
		if in.PeerCert == nil && token == "" {
			return Principal{}, domain.Errorf(domain.ErrUnauthenticated, "missing credentials")
		}
		return Principal{}, domain.Errorf(domain.ErrUnauthenticated, "invalid credentials")
	}
	if err := s.admitAdmin(ctx, pr); err != nil {
		return Principal{}, err
	}
	return pr, nil
}

// admitAdmin enforces the admin client-certificate requirement. While it is
// enabled, an admin-kind principal is admitted only when it authenticated by
// mTLS (the exact enrolled certificate, so Serial and Fingerprint are bound)
// AND also presented its bearer token. Either credential alone is refused: a
// stolen token is useless without the certificate's private key, and a stolen
// key is useless without the token. The denial is audited naming the identity
// (it was cryptographically proven by whichever credential was valid) but
// never the credential material. Non-admin principals and a disabled
// requirement pass through untouched.
func (s *Service) admitAdmin(ctx context.Context, pr Principal) error {
	if !pr.IsAdmin() || !s.adminRequireClientCert.Load() {
		return nil
	}
	if pr.Method == domain.AuthMethodMTLS && pr.Serial != "" && pr.Fingerprint != "" && pr.Token != "" {
		return nil
	}
	s.audit(ctx, domain.AuditEvent{
		EventType:     "auth.failure",
		ActorIdentity: pr.Identity.Name,
		ActorType:     pr.Identity.Kind,
		Decision:      "deny",
		SourceIP:      pr.RemoteAddr,
		UserAgent:     pr.UserAgent,
		RequestID:     pr.RequestID,
		Metadata:      encodeMeta(map[string]string{"method": string(pr.Method), "reason": "admin_client_cert_required"}),
	})
	return domain.Errorf(domain.ErrUnauthenticated, "invalid credentials")
}
