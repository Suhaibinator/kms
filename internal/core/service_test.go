package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
)

func newTestService(store *fakeStore) *Service {
	return New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
}

// withKeyring attaches a working keyring so secret operations can run.
func withKeyring(t *testing.T, s *Service) *crypto.Keyring {
	t.Helper()
	kek, err := crypto.NewKEKFromMaterial("kek-test", bytes.Repeat([]byte{0x7}, 32))
	if err != nil {
		t.Fatalf("NewKEKFromMaterial: %v", err)
	}
	ring := crypto.NewKeyring(kek)
	s.SetKeyring(ring)
	return ring
}

func adminPrincipal() Principal {
	return Principal{Identity: domain.Identity{Name: "root", Kind: domain.IdentityKindAdmin}}
}

func clientPrincipal(name string) Principal {
	return Principal{Identity: domain.Identity{Name: name, Kind: domain.IdentityKindClient}}
}

// clientPrincipalTok carries the bearer token too, as the transports do, so
// ReauthorizeWatch (which re-authenticates the token) can validate it.
func clientPrincipalTok(name, token string) Principal {
	return Principal{Identity: domain.Identity{Name: name, Kind: domain.IdentityKindClient}, Token: token}
}

func TestAuthenticate(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.addIdentity("app", domain.IdentityKindClient, "kms_good-token")
	s := newTestService(store)

	t.Run("valid token", func(t *testing.T) {
		id, err := s.Authenticate(ctx, "kms_good-token", "1.2.3.4", "agent")
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		if id.Name != "app" {
			t.Fatalf("identity = %q, want app", id.Name)
		}
	})

	t.Run("empty token", func(t *testing.T) {
		_, err := s.Authenticate(ctx, "   ", "ip", "ua")
		if !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
	})

	t.Run("unknown token audits failure", func(t *testing.T) {
		before := len(store.audits)
		_, err := s.Authenticate(ctx, "kms_wrong", "9.9.9.9", "ua")
		if !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
		if len(store.audits) != before+1 {
			t.Fatalf("expected an audit event for the failed auth")
		}
		ev, _ := store.lastAudit()
		if ev.EventType != "auth.failure" || ev.Decision != "deny" {
			t.Fatalf("audit = %+v, want auth.failure/deny", ev)
		}
	})

	t.Run("disabled identity is rejected", func(t *testing.T) {
		store.identitiesByHash[string(crypto.TokenHash("kms_disabled"))] =
			domain.Identity{Name: "old", Kind: domain.IdentityKindClient, Disabled: true}
		_, err := s.Authenticate(ctx, "kms_disabled", "ip", "ua")
		if !errors.Is(err, domain.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
	})
}

func TestAuthorizeAdminBypass(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	// Seed a parameter to read.
	if _, _, err := store.PutParameter(ctx, "/prod/x", "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := newTestService(store)

	// Admin reads without any policy present.
	got, err := s.GetParameter(ctx, adminPrincipal(), "/prod/x", 0, "")
	if err != nil {
		t.Fatalf("admin GetParameter: %v", err)
	}
	if got.Value != "1" {
		t.Fatalf("value = %q, want 1", got.Value)
	}
}

func TestAuthorizeClientDeniedByDefault(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	if _, _, err := store.PutParameter(ctx, "/prod/x", "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := newTestService(store)

	_, err := s.GetParameter(ctx, clientPrincipal("app"), "/prod/x", 0, "")
	if !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
	if !store.hasAudit("authz.denial", "deny") {
		t.Fatal("authorization denial was not audited as authz.denial/deny")
	}
}

func TestAuthorizeClientAllowedByPolicy(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	if _, _, err := store.PutParameter(ctx, "/prod/x", "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	store.addPolicy(domain.Policy{
		Name: "p", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: domain.OpParameterRead, Path: "/prod/*"}},
	})
	s := newTestService(store)

	got, err := s.GetParameter(ctx, clientPrincipal("app"), "/prod/x", 0, "")
	if err != nil {
		t.Fatalf("GetParameter: %v", err)
	}
	if got.Value != "1" {
		t.Fatalf("value = %q, want 1", got.Value)
	}
}

func TestAuthorizePolicyLoadFailureDenies(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.policiesErr = errors.New("db down")
	s := newTestService(store)

	// Fail closed: if policy retrieval fails, access is denied, not granted.
	_, err := s.GetParameter(ctx, clientPrincipal("app"), "/prod/x", 0, "")
	if !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
}

func TestReady(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	// No keyring yet: not ready.
	if err := s.Ready(ctx); !errors.Is(err, domain.ErrNotReady) {
		t.Fatalf("Ready(no keyring) = %v, want ErrNotReady", err)
	}
	withKeyring(t, s)
	if err := s.Ready(ctx); err != nil {
		t.Fatalf("Ready(ready) = %v, want nil", err)
	}
	// Database unreachable: not ready.
	store.pingErr = errors.New("unreachable")
	if err := s.Ready(ctx); !errors.Is(err, domain.ErrNotReady) {
		t.Fatalf("Ready(ping fail) = %v, want ErrNotReady", err)
	}
}

func TestSecretOpsRequireKeyring(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store) // no keyring attached

	_, err := s.GetSecret(ctx, adminPrincipal(), "/prod/s", 0, "")
	if !errors.Is(err, domain.ErrNotReady) {
		t.Fatalf("GetSecret without keyring err = %v, want ErrNotReady", err)
	}
	_, err = s.PutSecret(ctx, adminPrincipal(), PutSecretInput{Path: "/prod/s", Value: []byte("v")})
	if !errors.Is(err, domain.ErrNotReady) {
		t.Fatalf("PutSecret without keyring err = %v, want ErrNotReady", err)
	}
}
