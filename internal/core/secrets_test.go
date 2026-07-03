package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// putSecret creates/updates a secret as admin and returns the result. It fails
// the test on error.
func putSecret(t *testing.T, s *Service, in PutSecretInput) PutSecretResult {
	t.Helper()
	res, err := s.PutSecret(context.Background(), adminPrincipal(), in)
	if err != nil {
		t.Fatalf("PutSecret(%s): %v", in.Ref, err)
	}
	return res
}

func TestPutGetSecretStandardRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)

	putSecret(t, s, PutSecretInput{Ref: tref("db"), Value: []byte("hunter2"), ContentType: "text/plain"})

	val, err := s.GetSecret(ctx, adminPrincipal(), tref("db"), 0, "")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if string(val.Value) != "hunter2" {
		t.Fatalf("value = %q, want hunter2", val.Value)
	}
	if val.Version != 1 {
		t.Fatalf("version = %d, want 1", val.Version)
	}
	if !store.hasAudit("secret.write", "allow") {
		t.Error("missing secret.write allow audit")
	}
	if !store.hasAudit("secret.read", "allow") {
		t.Error("missing secret.read allow audit")
	}
	// Audit events carry the denormalized namespace + key.
	ev, _ := store.lastAudit()
	if ev.ResourceEnv != "prod" || ev.ResourceApp != "app" || ev.ResourceKey != "db" {
		t.Fatalf("audit ref = %s/%s/%s, want prod/app/db", ev.ResourceEnv, ev.ResourceApp, ev.ResourceKey)
	}
}

func TestPutSecretNewVersionRoundTrips(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)

	putSecret(t, s, PutSecretInput{Ref: tref("db"), Value: []byte("v1"), ContentType: "text/plain"})
	r2 := putSecret(t, s, PutSecretInput{Ref: tref("db"), Value: []byte("v2"), ContentType: "text/plain"})
	if r2.Version != 2 {
		t.Fatalf("second version = %d, want 2", r2.Version)
	}

	cur, err := s.GetSecret(ctx, adminPrincipal(), tref("db"), 0, "")
	if err != nil || string(cur.Value) != "v2" {
		t.Fatalf("current = %q, %v; want v2", cur.Value, err)
	}
	old, err := s.GetSecret(ctx, adminPrincipal(), tref("db"), 1, "")
	if err != nil || string(old.Value) != "v1" {
		t.Fatalf("v1 = %q, %v; want v1", old.Value, err)
	}
}

func TestGetSecretTokenGate(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)

	res := putSecret(t, s, PutSecretInput{
		Ref: tref("api"), Value: []byte("k"), ContentType: "text/plain", GenerateToken: true,
	})
	if res.AccessToken == "" {
		t.Fatal("expected a minted access token")
	}

	t.Run("missing token denied and audited", func(t *testing.T) {
		_, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 0, "")
		if !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("err = %v, want ErrPermissionDenied", err)
		}
		if !store.hasAudit("secret.read", "deny") {
			t.Error("token denial not audited")
		}
	})

	t.Run("wrong token denied", func(t *testing.T) {
		pr := adminPrincipal()
		pr.SecretToken = "kmss_wrong"
		if _, err := s.GetSecret(ctx, pr, tref("api"), 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("err = %v, want ErrPermissionDenied", err)
		}
	})

	t.Run("correct token allowed", func(t *testing.T) {
		pr := adminPrincipal()
		pr.SecretToken = res.AccessToken
		val, err := s.GetSecret(ctx, pr, tref("api"), 0, "")
		if err != nil {
			t.Fatalf("GetSecret: %v", err)
		}
		if string(val.Value) != "k" {
			t.Fatalf("value = %q, want k", val.Value)
		}
	})
}

func TestGetSecretRejectsUnreadableVersions(t *testing.T) {
	ctx := context.Background()

	t.Run("disabled", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withKeyring(t, s)
		putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain"})
		if _, err := s.DisableSecret(ctx, adminPrincipal(), tref("s"), 1, false); err != nil {
			t.Fatalf("DisableSecret: %v", err)
		}
		_, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 0, "")
		if !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("err = %v, want ErrFailedPrecondition", err)
		}
	})

	t.Run("destroyed", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withKeyring(t, s)
		putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain"})
		if _, err := s.DestroySecretVersion(ctx, adminPrincipal(), tref("s"), 1); err != nil {
			t.Fatalf("DestroySecretVersion: %v", err)
		}
		_, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 1, "")
		if !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("err = %v, want ErrFailedPrecondition", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withKeyring(t, s)
		future := time.Now().Add(time.Hour).UnixMilli()
		putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain", ExpiresAt: future})
		store.expireVersion(tref("s"), 1)
		_, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 0, "")
		if !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("err = %v, want ErrFailedPrecondition", err)
		}
	})
}

func TestGetSecretDecryptFailureAudited(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain"})

	store.tamperCiphertext(tref("s"), 1)

	_, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 0, "")
	if !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("err = %v, want ErrDecryptFailed", err)
	}
	if !store.hasAudit("secret.read", "error") {
		t.Error("decrypt failure not audited as error")
	}
}

func TestGetSecretFailsClosedWhenAuditUnavailable(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("topsecret"), ContentType: "text/plain"})

	store.auditErr = errors.New("audit sink down")
	val, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 0, "")
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("err = %v, want ErrFailedPrecondition (fail closed)", err)
	}
	if len(val.Value) != 0 {
		t.Fatal("plaintext returned despite fail-closed audit")
	}
}

func TestRevealSecretNonAdminDenied(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	s := newTestService(store)
	withKeyring(t, s)
	putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain"})

	_, err := s.RevealSecret(ctx, clientPrincipal("app"), tref("s"), 0, "")
	if !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
	if !store.hasAudit("secret.reveal", "deny") {
		t.Error("non-admin reveal not audited as deny")
	}
}

func TestRevealSecretBypassesTokenGate(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain", GenerateToken: true})

	val, err := s.RevealSecret(ctx, adminPrincipal(), tref("s"), 0, "")
	if err != nil {
		t.Fatalf("RevealSecret: %v", err)
	}
	if string(val.Value) != "v" {
		t.Fatalf("value = %q, want v", val.Value)
	}
	if !store.hasAudit("secret.reveal", "allow") {
		t.Error("reveal not audited as allow")
	}
}

func TestRevealClientBoundRejected(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)

	res := putSecret(t, s, PutSecretInput{
		Ref: tref("cb"), Value: []byte("v"), ContentType: "text/plain",
		ClientBound: true, GenerateToken: true,
	})
	if res.AccessToken == "" {
		t.Fatal("client-bound creation should mint a token")
	}

	_, err := s.RevealSecret(ctx, adminPrincipal(), tref("cb"), 0, "")
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("err = %v, want ErrFailedPrecondition", err)
	}
	if !strings.Contains(err.Error(), "client-bound") {
		t.Fatalf("error should explain client-bound: %v", err)
	}
}

func TestClientBoundLifecycle(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)

	_, err := s.PutSecret(ctx, adminPrincipal(), PutSecretInput{
		Ref: tref("cb"), Value: []byte("v"), ContentType: "text/plain", ClientBound: true,
	})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("create without token err = %v, want ErrInvalidArgument", err)
	}

	res := putSecret(t, s, PutSecretInput{
		Ref: tref("cb"), Value: []byte("secret-value"), ContentType: "text/plain",
		ClientBound: true, GenerateToken: true,
	})
	token := res.AccessToken

	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("cb"), 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("read without token err = %v, want ErrPermissionDenied", err)
	}
	pr := adminPrincipal()
	pr.SecretToken = token
	val, err := s.GetSecret(ctx, pr, tref("cb"), 0, "")
	if err != nil {
		t.Fatalf("read with token: %v", err)
	}
	if string(val.Value) != "secret-value" {
		t.Fatalf("value = %q, want secret-value", val.Value)
	}

	if _, err := s.PutSecret(ctx, adminPrincipal(), PutSecretInput{
		Ref: tref("cb"), Value: []byte("v2"), ContentType: "text/plain", ClientBound: true,
	}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("update without token err = %v, want ErrPermissionDenied", err)
	}
	upd := adminPrincipal()
	upd.SecretToken = token
	if _, err := s.PutSecret(ctx, upd, PutSecretInput{
		Ref: tref("cb"), Value: []byte("v2"), ContentType: "text/plain", ClientBound: true,
	}); err != nil {
		t.Fatalf("update with token: %v", err)
	}
}

func TestPutSecretModeCannotChange(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain"})

	_, err := s.PutSecret(ctx, adminPrincipal(), PutSecretInput{
		Ref: tref("s"), Value: []byte("v2"), ContentType: "text/plain", ClientBound: true, GenerateToken: true,
	})
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("err = %v, want ErrFailedPrecondition", err)
	}
}

func TestPutSecretValidation(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)

	cases := map[string]PutSecretInput{
		"empty value":      {Ref: tref("s"), Value: nil},
		"bad key":          {Ref: domain.Ref{NS: tns, Key: "bad key"}, Value: []byte("v")},
		"invalid metadata": {Ref: tref("s"), Value: []byte("v"), Metadata: "not-json"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.PutSecret(ctx, adminPrincipal(), in); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestPutSecretAuthorization(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	s := newTestService(store)
	withKeyring(t, s)

	_, err := s.PutSecret(ctx, clientPrincipal("app"), PutSecretInput{
		Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain",
	})
	if !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}

	store.addPolicy(domain.Policy{Name: "w", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: domain.OpSecretWrite, Env: "prod", App: "app"}}})
	if _, err := s.PutSecret(ctx, clientPrincipal("app"), PutSecretInput{
		Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain",
	}); err != nil {
		t.Fatalf("authorized PutSecret: %v", err)
	}
}

func TestDisableEnableAndDestroyFlow(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain"})

	if _, err := s.DisableSecret(ctx, adminPrincipal(), tref("s"), 1, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 1, ""); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("disabled read err = %v, want ErrFailedPrecondition", err)
	}
	if _, err := s.DisableSecret(ctx, adminPrincipal(), tref("s"), 1, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 1, ""); err != nil {
		t.Fatalf("re-enabled read: %v", err)
	}

	if _, err := s.DestroySecretVersion(ctx, adminPrincipal(), tref("s"), 1); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if _, err := s.DisableSecret(ctx, adminPrincipal(), tref("s"), 1, true); err != nil {
		t.Fatalf("enable after destroy (store call): %v", err)
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 1, ""); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("destroyed read err = %v, want ErrFailedPrecondition", err)
	}
}

func TestDestroyAndPromoteRequireVersion(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)

	if _, err := s.DestroySecretVersion(ctx, adminPrincipal(), tref("s"), 0); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("destroy v0 err = %v, want ErrInvalidArgument", err)
	}
	if _, _, _, err := s.PromoteSecretVersion(ctx, adminPrincipal(), tref("s"), 0); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("promote v0 err = %v, want ErrInvalidArgument", err)
	}
}

func TestPromoteSecretVersion(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v1"), ContentType: "text/plain"})
	putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v2"), ContentType: "text/plain"})

	cur, prev, _, err := s.PromoteSecretVersion(ctx, adminPrincipal(), tref("s"), 1)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if cur != 1 || prev != 2 {
		t.Fatalf("promote returned current=%d previous=%d, want 1/2", cur, prev)
	}
	val, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 0, "")
	if err != nil || string(val.Value) != "v1" {
		t.Fatalf("current after promote = %q, %v; want v1", val.Value, err)
	}
}

func TestSecretAADBindsToRef(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	putSecret(t, s, PutSecretInput{Ref: tref("db"), Value: []byte("v"), ContentType: "text/plain"})

	// Relocate the stored version under a different key: decryption must fail
	// because the AAD is recomputed from the row's (env, app, key, version).
	sec := store.secrets[tref("db").String()]
	moved := &fakeSecret{
		rec:      sec.rec,
		versions: sec.versions,
		next:     sec.next,
		current:  sec.current,
	}
	moved.rec.Ref = tref("other")
	store.secrets[tref("other").String()] = moved

	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("other"), 0, ""); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("relocated read err = %v, want ErrDecryptFailed", err)
	}
	// The original location still decrypts.
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("db"), 0, ""); err != nil {
		t.Fatalf("original read: %v", err)
	}
}
