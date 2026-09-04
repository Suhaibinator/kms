package integration

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// §7 — the per-namespace auth-method gate is enforced end to end for a
// token-authenticated client assigned to an mTLS-only namespace: it is denied every
// operation (even ones its implicit home grant would otherwise allow) until the
// namespace admits tokens. Admins bypass the gate throughout.
func TestNamespaceMethodGateEndToEnd(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	ns := nsRef("prod", "locked")
	h.ensureNSRef(ns, domain.AuthMethodMTLS) // mTLS only

	// Admin (gate-exempt) seeds a parameter so the denial below is the gate, not
	// a not-found.
	ref := domain.Ref{NS: ns, Key: "rate"}
	if _, _, err := h.svc.PutParameter(ctx, h.admin, ref, "10", "integer", ""); err != nil {
		t.Fatalf("admin seed: %v", err)
	}

	// A token client assigned to this namespace would have the implicit home grant,
	// but the method gate rejects it before authorization even runs.
	client, _ := h.createBoundClient("locked-client", &ns)
	if _, err := h.svc.GetParameter(ctx, client, ref, 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("token read of mtls-only namespace err = %v, want ErrPermissionDenied (method gate)", err)
	}
	if _, _, err := h.svc.ListParameters(ctx, client, ns, "", storage.ListPage{}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("token list of mtls-only namespace err = %v, want ErrPermissionDenied (method gate)", err)
	}

	// Admit tokens; the implicit home grant now covers reads and lists.
	if _, err := h.svc.UpdateNamespace(ctx, h.admin, ns, "",
		[]domain.AuthMethod{domain.AuthMethodMTLS, domain.AuthMethodToken}); err != nil {
		t.Fatalf("UpdateNamespace: %v", err)
	}
	if got, err := h.svc.GetParameter(ctx, client, ref, 0, ""); err != nil || got.Value != "10" {
		t.Errorf("token read after admitting token = %q err=%v, want 10", got.Value, err)
	}
	if params, _, err := h.svc.ListParameters(ctx, client, ns, "", storage.ListPage{}); err != nil || len(params) != 1 {
		t.Errorf("token list after admitting token = %d items err=%v, want 1", len(params), err)
	}
}

// §3 / §7 — the implicit home-namespace grant covers reads and lists in the
// caller's own namespace with no policy, but writes and cross-namespace access
// still require an explicit policy.
func TestImplicitHomeGrantVsExplicitPolicy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	home := nsRef("prod", "home")
	other := nsRef("prod", "other")
	h.ensureNSRef(home, domain.AuthMethodMTLS, domain.AuthMethodToken)
	h.ensureNSRef(other, domain.AuthMethodMTLS, domain.AuthMethodToken)

	homeRef := domain.Ref{NS: home, Key: "setting"}
	otherRef := domain.Ref{NS: other, Key: "setting"}
	if _, _, err := h.svc.PutParameter(ctx, h.admin, homeRef, "home-value", "string", ""); err != nil {
		t.Fatalf("seed home: %v", err)
	}
	if _, _, err := h.svc.PutParameter(ctx, h.admin, otherRef, "other-value", "string", ""); err != nil {
		t.Fatalf("seed other: %v", err)
	}

	client, _ := h.createBoundClient("home-client", &home)

	// Read + list in the home namespace: allowed by the implicit grant, no policy.
	if got, err := h.svc.GetParameter(ctx, client, homeRef, 0, ""); err != nil || got.Value != "home-value" {
		t.Errorf("home read = %q err=%v, want home-value (implicit grant)", got.Value, err)
	}
	if params, _, err := h.svc.ListParameters(ctx, client, home, "", storage.ListPage{}); err != nil || len(params) != 1 {
		t.Errorf("home list = %d err=%v, want 1 (implicit grant)", len(params), err)
	}

	// A write in the home namespace is NOT covered by the implicit grant.
	if _, _, err := h.svc.PutParameter(ctx, client, domain.Ref{NS: home, Key: "new"}, "v", "string", ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("home write without policy err = %v, want ErrPermissionDenied (writes need explicit allow)", err)
	}
	// Cross-namespace read is NOT covered by the implicit grant.
	if _, err := h.svc.GetParameter(ctx, client, otherRef, 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("cross-namespace read err = %v, want ErrPermissionDenied", err)
	}

	// An explicit policy unlocks the home write and the cross-namespace read.
	h.grant("home-client-extra", "home-client",
		[]domain.PolicyRule{
			allowRule(domain.OpParameterWrite, "prod", "home"),
			allowRule(domain.OpParameterRead, "prod", "other"),
		}, nil)
	if _, _, err := h.svc.PutParameter(ctx, client, domain.Ref{NS: home, Key: "new"}, "v", "string", ""); err != nil {
		t.Errorf("home write with policy err = %v, want ok", err)
	}
	if got, err := h.svc.GetParameter(ctx, client, otherRef, 0, ""); err != nil || got.Value != "other-value" {
		t.Errorf("cross-namespace read with policy = %q err=%v, want other-value", got.Value, err)
	}
}

// §7 / §12 — a client certificate issued by the built-in CA authenticates via
// VerifyClientCert, and revoking it (by serial, and via full identity revoke)
// takes effect immediately while other certs keep working.
func TestClientCertIssueVerifyRevoke(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	ns := nsRef("prod", "certs")
	h.ensureNSRef(ns, domain.AuthMethodMTLS)

	// Create an mTLS identity; the create-time bundle is the first certificate.
	res, err := h.svc.CreateIdentity(ctx, h.admin, core.CreateIdentityInput{
		Name: "cert-svc", Kind: domain.IdentityKindClient, Namespace: &ns,
		AuthMethods: []domain.AuthMethod{domain.AuthMethodMTLS},
	})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	if res.Cert == nil {
		t.Fatal("expected a client-certificate bundle")
	}
	if res.Token != "" {
		t.Error("mtls-only identity should not receive a bearer token")
	}
	firstCert := mustParseCert(t, res.Cert.CertPEM)

	// The issued certificate verifies to its identity.
	id, err := h.svc.VerifyClientCert(ctx, firstCert, "10.0.0.9", "mtls-agent")
	if err != nil || id.Name != "cert-svc" {
		t.Fatalf("VerifyClientCert(first) = %+v err=%v, want cert-svc", id, err)
	}

	// Issue a second certificate for zero-downtime rollover; both verify.
	bundle2, err := h.svc.IssueIdentityCertificate(ctx, h.admin, "cert-svc", 0)
	if err != nil {
		t.Fatalf("IssueIdentityCertificate: %v", err)
	}
	secondCert := mustParseCert(t, bundle2.CertPEM)
	if _, err := h.svc.VerifyClientCert(ctx, secondCert, "10.0.0.9", "mtls-agent"); err != nil {
		t.Fatalf("VerifyClientCert(second): %v", err)
	}

	// Revoke the second by serial: it stops verifying, the first keeps working.
	if err := h.svc.RevokeIdentityCertificate(ctx, h.admin, "cert-svc", bundle2.Serial); err != nil {
		t.Fatalf("RevokeIdentityCertificate: %v", err)
	}
	if _, err := h.svc.VerifyClientCert(ctx, secondCert, "10.0.0.9", "mtls-agent"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("verify revoked cert err = %v, want ErrUnauthenticated", err)
	}
	if _, err := h.svc.VerifyClientCert(ctx, firstCert, "10.0.0.9", "mtls-agent"); err != nil {
		t.Errorf("first cert should still verify after revoking the second: %v", err)
	}

	// Revoking the whole identity invalidates its remaining certificate too.
	if err := h.svc.RevokeIdentity(ctx, h.admin, "cert-svc"); err != nil {
		t.Fatalf("RevokeIdentity: %v", err)
	}
	if _, err := h.svc.VerifyClientCert(ctx, firstCert, "10.0.0.9", "mtls-agent"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("verify cert of revoked identity err = %v, want ErrUnauthenticated", err)
	}
}

// mustParseCert decodes a PEM leaf certificate for VerifyClientCert.
func mustParseCert(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		t.Fatal("no PEM block in certificate")
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return cert
}
