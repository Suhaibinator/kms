package grpcserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
)

// TestMTLS_ValidCertWhoAmIAndImplicitRead exercises the full mTLS path: a client
// presenting a CA-issued certificate is mapped to its identity (method mtls),
// discovers its bound namespace via WhoAmI, and reads a parameter in that
// mTLS-only namespace under the implicit home-namespace grant — with no bearer
// token and no policy.
func TestMTLS_ValidCertWhoAmIAndImplicitRead(t *testing.T) {
	env := newTLSTestEnv(t)
	ns := domain.NamespaceRef{Env: "prod", App: "svc"}
	env.store.addNamespace(ns, domain.AuthMethodMTLS)
	cert, _ := env.issueClientCert(t, "svc", &ns)

	// Seed a parameter via the admin (token, no client cert — allowed since the
	// admin bypasses the method gate).
	adminConn := env.dial(t, nil)
	if _, err := kmsv1.NewParameterServiceClient(adminConn).PutParameter(adminCtx(),
		&kmsv1.PutParameterRequest{Ref: pRef("prod", "svc", "rate-limit"), Value: "100"}); err != nil {
		t.Fatalf("seed put: %v", err)
	}

	conn := env.dial(t, &cert) // no bearer token; identity comes from the cert
	admin := kmsv1.NewAdminServiceClient(conn)

	who, err := admin.WhoAmI(context.Background(), &kmsv1.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if who.GetName() != "svc" || who.GetAuthMethod() != string(domain.AuthMethodMTLS) {
		t.Fatalf("whoami = %+v, want name=svc method=mtls", who)
	}
	if who.GetNamespace().GetEnv() != "prod" || who.GetNamespace().GetApp() != "svc" {
		t.Fatalf("whoami namespace = %+v", who.GetNamespace())
	}

	resp, err := kmsv1.NewParameterServiceClient(conn).GetParameter(context.Background(),
		&kmsv1.GetParameterRequest{Ref: pRef("prod", "svc", "rate-limit")})
	if err != nil {
		t.Fatalf("cert client read: %v", err)
	}
	if resp.GetParameter().GetValue() != "100" {
		t.Fatalf("value = %q, want 100", resp.GetParameter().GetValue())
	}
}

func TestMTLS_RevokedCertRejected(t *testing.T) {
	env := newTLSTestEnv(t)
	ns := domain.NamespaceRef{Env: "prod", App: "svc"}
	env.store.addNamespace(ns, domain.AuthMethodMTLS)
	cert, serial := env.issueClientCert(t, "svc", &ns)

	if err := env.store.RevokeIdentityCert(context.Background(), serial); err != nil {
		t.Fatalf("revoke cert: %v", err)
	}
	conn := env.dial(t, &cert)
	_, err := kmsv1.NewAdminServiceClient(conn).WhoAmI(context.Background(), &kmsv1.WhoAmIRequest{})
	if codeOf(err) != codes.Unauthenticated {
		t.Fatalf("revoked cert: code = %v, want Unauthenticated", codeOf(err))
	}
}

func TestMTLS_DisabledIdentityRejected(t *testing.T) {
	env := newTLSTestEnv(t)
	ns := domain.NamespaceRef{Env: "prod", App: "svc"}
	env.store.addNamespace(ns, domain.AuthMethodMTLS)
	cert, _ := env.issueClientCert(t, "svc", &ns)

	if err := env.store.SetIdentityDisabled(context.Background(), "svc", true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	conn := env.dial(t, &cert)
	_, err := kmsv1.NewAdminServiceClient(conn).WhoAmI(context.Background(), &kmsv1.WhoAmIRequest{})
	if codeOf(err) != codes.Unauthenticated {
		t.Fatalf("disabled identity: code = %v, want Unauthenticated", codeOf(err))
	}
}

func TestMTLS_GetCACertificateIsPublic(t *testing.T) {
	env := newTLSTestEnv(t)
	conn := env.dial(t, nil)
	// No credentials attached at all.
	resp, err := kmsv1.NewAdminServiceClient(conn).GetCACertificate(context.Background(), &kmsv1.GetCACertificateRequest{})
	if err != nil {
		t.Fatalf("get ca certificate: %v", err)
	}
	if resp.GetCertPem() == "" {
		t.Fatal("expected a CA certificate PEM")
	}
}

// TestPeerCertFromContext covers the interceptor's cert-extraction helper on the
// paths that do not arise over a real mTLS connection: no peer, and a peer with
// no TLS auth info.
func TestPeerCertFromContext(t *testing.T) {
	if got := peerCertFromContext(context.Background()); got != nil {
		t.Fatalf("no peer: got %v, want nil", got)
	}
	ctx := peer.NewContext(context.Background(), &peer.Peer{})
	if got := peerCertFromContext(ctx); got != nil {
		t.Fatalf("peer without TLS: got %v, want nil", got)
	}
	// A TLS peer with no presented certificate (token-only client) yields nil.
	ctx = peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{}}},
	})
	if got := peerCertFromContext(ctx); got != nil {
		t.Fatalf("TLS peer without cert: got %v, want nil", got)
	}
}
