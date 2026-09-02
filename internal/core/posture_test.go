package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

const postureWindow = 30 * 24 * time.Hour

// TestSecurityPostureRequiresAdmin: the snapshot spans every namespace at
// once, which no delegated grant scopes, so there is no non-admin path to it.
func TestSecurityPostureRequiresAdmin(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	s := newTestService(store)

	_, err := s.SecurityPosture(ctx, clientPrincipal("app"), postureWindow, postureWindow)
	if !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client SecurityPosture error = %v, want permission denied", err)
	}
	if _, err := s.SecurityPosture(ctx, adminPrincipal(), postureWindow, postureWindow); err != nil {
		t.Fatalf("admin SecurityPosture: %v", err)
	}
}

// TestSecurityPostureNeedsNoKeyring is the "never triggers a decrypt" invariant
// made mechanical: the service here has no keyring, so any path that unwrapped
// a DEK would fail with not-ready. The snapshot is metadata rows only, and it
// reports the key generations without ever holding a key.
func TestSecurityPostureNeedsNoKeyring(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.keys = []domain.KeyMetadata{
		{ID: "kek-old", Source: domain.KeySourceFile, State: domain.KeyStateRetired, CreatedAt: time.Now().Add(-72 * time.Hour)},
		{ID: "kek-live", Source: domain.KeySourceFile, State: domain.KeyStateActive, CreatedAt: time.Now().Add(-24 * time.Hour)},
	}
	s := newTestService(store)
	if err := s.Ready(ctx); err == nil {
		t.Fatal("service is ready; this test only proves something while the keyring is detached")
	}

	posture, err := s.SecurityPosture(ctx, adminPrincipal(), postureWindow, postureWindow)
	if err != nil {
		t.Fatalf("SecurityPosture without a keyring: %v", err)
	}
	if posture.KEK.ActiveID != "kek-live" {
		t.Errorf("active KEK = %q, want kek-live", posture.KEK.ActiveID)
	}
	if posture.KEK.Generations != 2 {
		t.Errorf("KEK generations = %d, want 2", posture.KEK.Generations)
	}
	if posture.Windows.AdminCert != AdminCertPostureWindow {
		t.Errorf("admin cert window = %s, want the fixed %s", posture.Windows.AdminCert, AdminCertPostureWindow)
	}
}

// TestSecurityPostureAdminCerts pins the two admin-certificate lists and their
// order: an admin with no valid certificate cannot authenticate at all, and the
// ones that expire soonest lead.
func TestSecurityPostureAdminCerts(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	now := time.Now().UTC()

	store.addIdentity("no-cert", domain.IdentityKindAdmin, "kms_a")
	store.addIdentity("also-no-cert", domain.IdentityKindAdmin, "kms_b")
	store.addIdentity("expiring-later", domain.IdentityKindAdmin, "kms_c")
	store.addIdentity("expiring-sooner", domain.IdentityKindAdmin, "kms_d")
	store.addIdentity("client", domain.IdentityKindClient, "kms_e")
	for name, notAfter := range map[string]time.Time{
		"expiring-later":  now.Add(10 * 24 * time.Hour),
		"expiring-sooner": now.Add(2 * 24 * time.Hour),
	} {
		if err := store.InsertIdentityCert(ctx, name, domain.IdentityCert{
			Serial: "serial-" + name, NotAfter: notAfter, CreatedAt: now,
		}); err != nil {
			t.Fatalf("InsertIdentityCert(%s): %v", name, err)
		}
	}

	posture, err := s.SecurityPosture(ctx, adminPrincipal(), postureWindow, postureWindow)
	if err != nil {
		t.Fatalf("SecurityPosture: %v", err)
	}
	wantLacking := []string{"also-no-cert", "no-cert"}
	if len(posture.AdminCertsLacking) != len(wantLacking) {
		t.Fatalf("lacking = %v, want %v", posture.AdminCertsLacking, wantLacking)
	}
	for i, name := range wantLacking {
		if posture.AdminCertsLacking[i] != name {
			t.Errorf("lacking[%d] = %q, want %q", i, posture.AdminCertsLacking[i], name)
		}
	}
	if len(posture.AdminCertsExpiring) != 2 {
		t.Fatalf("expiring = %+v, want two entries", posture.AdminCertsExpiring)
	}
	if posture.AdminCertsExpiring[0].Name != "expiring-sooner" {
		t.Errorf("expiring[0] = %q, want the soonest (expiring-sooner)", posture.AdminCertsExpiring[0].Name)
	}
}

// TestSecurityPostureWithoutOptionalCapabilities: a store that implements
// neither optional capability contributes empty lists and zero counts rather
// than failing the whole snapshot — the same way the metrics sampler treats it.
func TestSecurityPostureWithoutOptionalCapabilities(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	posture, err := s.SecurityPosture(ctx, adminPrincipal(), postureWindow, postureWindow)
	if err != nil {
		t.Fatalf("SecurityPosture: %v", err)
	}
	if len(posture.IdentityCertsExpiring.Items) != 0 || posture.IdentityCertsExpiring.Total != 0 ||
		posture.IdentityCertsExpiring.Truncated {
		t.Errorf("identity certs = %+v, want an empty, untruncated list", posture.IdentityCertsExpiring)
	}
	if len(posture.SecretVersionsExpiring.Items) != 0 || posture.SecretVersionsExpiring.Total != 0 ||
		posture.SecretVersionsExpiring.Truncated {
		t.Errorf("secret versions = %+v, want an empty, untruncated list", posture.SecretVersionsExpiring)
	}
	if posture.ChangeLog != (ChangeLogPosture{}) {
		t.Errorf("change log = %+v, want zero", posture.ChangeLog)
	}
}
