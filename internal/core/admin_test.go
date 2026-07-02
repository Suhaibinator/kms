package core

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func TestRequireAdminGatesPolicyWrites(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	valid := domain.Policy{Name: "p", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: domain.OpSecretRead, Path: "/prod/*"}}}

	// Client is denied and the denial is audited.
	if _, err := s.CreatePolicy(ctx, clientPrincipal("app"), valid); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client CreatePolicy err = %v, want ErrPermissionDenied", err)
	}
	if !store.hasAudit("policy.write", "deny") {
		t.Error("policy write denial not audited")
	}

	// Admin succeeds and the rules are normalized+stored.
	out, err := s.CreatePolicy(ctx, adminPrincipal(), valid)
	if err != nil {
		t.Fatalf("admin CreatePolicy: %v", err)
	}
	if out.Allow[0].Path != "/prod/*" {
		t.Fatalf("stored path = %q", out.Allow[0].Path)
	}
	if len(store.policies) != 1 {
		t.Fatalf("policies stored = %d, want 1", len(store.policies))
	}
}

func TestCreatePolicyValidatesRules(t *testing.T) {
	ctx := context.Background()
	s := newTestService(newFakeStore())
	bad := domain.Policy{Name: "p", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: "secret:teleport", Path: "/prod/*"}}}
	if _, err := s.CreatePolicy(ctx, adminPrincipal(), bad); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestCreateNamespaceAuthorization(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	// Admin can always create.
	if _, err := s.CreateNamespace(ctx, adminPrincipal(), "/prod", "prod ns"); err != nil {
		t.Fatalf("admin CreateNamespace: %v", err)
	}

	// Client without the dedicated operation is denied.
	if _, err := s.CreateNamespace(ctx, clientPrincipal("app"), "/team", ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}

	// Granting admin:namespace:create lets the client create under the path.
	store.addPolicy(domain.Policy{Name: "ns", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: domain.OpAdminNamespaceCreate, Path: "/team/*"}}})
	if _, err := s.CreateNamespace(ctx, clientPrincipal("app"), "/team/x", ""); err != nil {
		t.Fatalf("authorized CreateNamespace: %v", err)
	}
}

func TestCreateIdentity(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	// Non-admin denied.
	if _, _, err := s.CreateIdentity(ctx, clientPrincipal("app"), "svc", domain.IdentityKindClient); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}

	// Invalid name and kind rejected.
	if _, _, err := s.CreateIdentity(ctx, adminPrincipal(), "bad name!", domain.IdentityKindClient); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("bad name err = %v, want ErrInvalidArgument", err)
	}
	if _, _, err := s.CreateIdentity(ctx, adminPrincipal(), "svc", "robot"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("bad kind err = %v, want ErrInvalidArgument", err)
	}

	// Success returns a usable token exactly once.
	id, token, err := s.CreateIdentity(ctx, adminPrincipal(), "svc", domain.IdentityKindClient)
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	if id.Name != "svc" {
		t.Fatalf("identity name = %q", id.Name)
	}
	if !strings.HasPrefix(token, "kms_") {
		t.Fatalf("token %q missing kms_ prefix", token)
	}
	// The freshly minted token authenticates.
	if _, err := s.Authenticate(ctx, token, "ip", "ua"); err != nil {
		t.Fatalf("Authenticate with new token: %v", err)
	}
}

func TestRotateIdentityToken(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	old := store.addIdentity("svc", domain.IdentityKindClient, "kms_old")
	_ = old

	// Non-admin denied.
	if _, err := s.RotateIdentityToken(ctx, clientPrincipal("svc"), "svc"); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}

	newTok, err := s.RotateIdentityToken(ctx, adminPrincipal(), "svc")
	if err != nil {
		t.Fatalf("RotateIdentityToken: %v", err)
	}
	if _, err := s.Authenticate(ctx, newTok, "ip", "ua"); err != nil {
		t.Fatalf("Authenticate with rotated token: %v", err)
	}
}

func TestRevokeIdentity(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	store.addIdentity("svc", domain.IdentityKindClient, "kms_tok")

	if err := s.RevokeIdentity(ctx, clientPrincipal("svc"), "svc"); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}
	if err := s.RevokeIdentity(ctx, adminPrincipal(), "svc"); err != nil {
		t.Fatalf("RevokeIdentity: %v", err)
	}
	if id := store.identitiesByName["svc"]; !id.Disabled {
		t.Fatal("identity not marked disabled")
	}
}

func TestListKeyMetadataStripsMaterial(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.keys = []domain.KeyMetadata{{
		ID: "kek-1", Source: domain.KeySourceFile, State: domain.KeyStateActive,
		KeyCheck: []byte("verifier"), KDFSalt: []byte("salt"),
	}}
	s := newTestService(store)

	// Non-admin denied.
	if _, err := s.ListKeyMetadata(ctx, clientPrincipal("app")); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}

	keys, err := s.ListKeyMetadata(ctx, adminPrincipal())
	if err != nil {
		t.Fatalf("ListKeyMetadata: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %d, want 1", len(keys))
	}
	if keys[0].KeyCheck != nil || keys[0].KDFSalt != nil {
		t.Fatal("key verifier/salt material was not stripped")
	}
}

func TestListAuditAuthorization(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	// Client without the operation is denied.
	if _, _, err := s.ListAuditEvents(ctx, clientPrincipal("app"), domain.AuditFilter{}, storage.ListPage{}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client err = %v, want ErrPermissionDenied", err)
	}
	// With admin:audit:read granted, it works.
	store.addPolicy(domain.Policy{Name: "a", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: domain.OpAdminAuditRead, Path: "/*"}}})
	if _, _, err := s.ListAuditEvents(ctx, clientPrincipal("app"), domain.AuditFilter{}, storage.ListPage{}); err != nil {
		t.Fatalf("authorized ListAuditEvents: %v", err)
	}
	// Admin always allowed.
	if _, _, err := s.ListAuditEvents(ctx, adminPrincipal(), domain.AuditFilter{}, storage.ListPage{}); err != nil {
		t.Fatalf("admin ListAuditEvents: %v", err)
	}
}

func TestRotateKEKRewrapsAndKeepsSecretsReadable(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)

	putSecret(t, s, PutSecretInput{Path: "/a", Value: []byte("alpha"), ContentType: "text/plain"})
	putSecret(t, s, PutSecretInput{Path: "/b", Value: []byte("bravo"), ContentType: "text/plain"})

	// Non-admin cannot rotate.
	if _, err := s.RotateKEK(ctx, clientPrincipal("app"),
		domain.KeyMetadata{ID: "kek-rotated"}, bytes.Repeat([]byte{0x9}, 32)); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client RotateKEK err = %v, want ErrPermissionDenied", err)
	}

	count, err := s.RotateKEK(ctx, adminPrincipal(),
		domain.KeyMetadata{ID: "kek-rotated"}, bytes.Repeat([]byte{0x9}, 32))
	if err != nil {
		t.Fatalf("RotateKEK: %v", err)
	}
	if count != 2 {
		t.Fatalf("rewrapped %d versions, want 2", count)
	}

	// Secrets still decrypt under the rotated KEK.
	for path, want := range map[string]string{"/a": "alpha", "/b": "bravo"} {
		val, err := s.GetSecret(ctx, adminPrincipal(), path, 0, "")
		if err != nil {
			t.Fatalf("GetSecret(%s) after rotate: %v", path, err)
		}
		if string(val.Value) != want {
			t.Fatalf("GetSecret(%s) = %q, want %q", path, val.Value, want)
		}
	}
}
