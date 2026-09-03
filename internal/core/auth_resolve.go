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
	PeerCert   *x509.Certificate
	RemoteAddr string
	UserAgent  string
	RequestID  string
}

// ResolvePrincipal verifies the presented credentials and combines them into a
// Principal. A client certificate and a bearer token are verified
// independently and an invalid one is dropped, so a client that also holds a
// valid token can still authenticate where the namespace admits the token
// method. When both are valid they must name the same identity; the result
// then carries Method mtls (proof of possession, the stronger method) and
// retains the token so long-lived streams can re-check it. The admin
// admission rule (admitAdmin) runs last on the combined principal.
//
// Auditing happens once the outcome is known. A credential that failed to
// verify is recorded as auth.failure only when the request is refused; when
// the other credential admitted the caller it is recorded instead as a single
// auth.credential_ignored row with decision allow, naming the admitted
// identity, so a successful request never reads as a failed login. Errors are
// generic and never reveal which credential was wrong.
func (s *Service) ResolvePrincipal(ctx context.Context, in CredentialInput) (Principal, error) {
	token := strings.TrimSpace(in.Token)
	certPresented := in.PeerCert != nil
	tokenPresented := token != ""
	var (
		certID  domain.Identity
		certOK  bool
		tokenID domain.Identity
		tokenOK bool
	)
	if certPresented {
		if id, err := s.verifyClientCert(ctx, in.PeerCert, in.RemoteAddr, in.UserAgent, false); err == nil {
			certID, certOK = id, true
		}
	}
	if tokenPresented {
		if id, err := s.authenticateToken(ctx, token, in.RemoteAddr, in.UserAgent, false); err == nil {
			tokenID, tokenOK = id, true
		}
	}
	// auditDropped records every presented credential that did not verify, in
	// the order the verifiers would have audited it, once the request is known
	// to be refused.
	auditDropped := func() {
		if certPresented && !certOK {
			s.auditAuthFailure(ctx, domain.AuthMethodMTLS, in.RemoteAddr, in.UserAgent)
		}
		if tokenPresented && !tokenOK {
			s.auditAuthFailure(ctx, domain.AuthMethodToken, in.RemoteAddr, in.UserAgent)
		}
	}

	pr := Principal{
		RemoteAddr: in.RemoteAddr,
		UserAgent:  in.UserAgent,
		RequestID:  in.RequestID,
	}
	var ignored domain.AuthMethod // a presented credential that did not verify but was not needed
	switch {
	case certOK && tokenOK:
		if certID.Name != tokenID.Name {
			s.m().AuthFailure(AuthFailureCredentialMismatch)
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
		if tokenPresented {
			ignored = domain.AuthMethodToken
		}
	case tokenOK:
		pr.Identity = tokenID
		pr.Method = domain.AuthMethodToken
		pr.Token = token
		if certPresented {
			ignored = domain.AuthMethodMTLS
		}
	default:
		auditDropped()
		if !certPresented && !tokenPresented {
			s.m().AuthFailure(AuthFailureMissing)
			return Principal{}, domain.Errorf(domain.ErrUnauthenticated, "missing credentials")
		}
		return Principal{}, domain.Errorf(domain.ErrUnauthenticated, "invalid credentials")
	}
	if !s.adminAdmitted(pr) {
		// Refused after all: the dropped credential was a real failure, and
		// admitAdmin records the admission denial itself.
		auditDropped()
		return Principal{}, s.admitAdmin(ctx, pr)
	}
	if ignored != "" {
		s.audit(ctx, domain.AuditEvent{
			EventType:     "auth.credential_ignored",
			ActorIdentity: pr.Identity.Name,
			ActorType:     pr.Identity.Kind,
			Decision:      "allow",
			SourceIP:      in.RemoteAddr,
			UserAgent:     in.UserAgent,
			RequestID:     in.RequestID,
			Metadata:      encodeMeta(map[string]string{"ignored": string(ignored), "method": string(pr.Method)}),
		})
	}
	return pr, nil
}

// adminAdmitted reports whether pr satisfies the admin client-certificate
// requirement: non-admin principals and a disabled requirement always pass;
// an admin passes only when it authenticated by mTLS (the exact enrolled
// certificate, so Serial and Fingerprint are bound) AND also presented its
// bearer token.
func (s *Service) adminAdmitted(pr Principal) bool {
	if !pr.IsAdmin() || !s.adminRequireClientCert.Load() {
		return true
	}
	return pr.Method == domain.AuthMethodMTLS && pr.Serial != "" && pr.Fingerprint != "" && pr.Token != ""
}

// admitAdmin enforces the admin client-certificate requirement (see
// adminAdmitted). Either credential alone is refused: a stolen token is useless
// without the certificate's private key, and a stolen key is useless without
// the token. The denial is audited naming the identity (it was
// cryptographically proven by whichever credential was valid) but never the
// credential material.
func (s *Service) admitAdmin(ctx context.Context, pr Principal) error {
	if s.adminAdmitted(pr) {
		return nil
	}
	s.m().AuthFailure(AuthFailureAdminClientCertRequired)
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
