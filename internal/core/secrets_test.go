package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
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
	seedTokenNS(store)
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
	if ev.ResourceNamespaceID != store.namespaces[tns.String()].ID {
		t.Fatalf("secret.read audit namespace ID = %d, want %d", ev.ResourceNamespaceID, store.namespaces[tns.String()].ID)
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

func TestPutSecretRejectsDeleteRecreateABA(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	r := tref("aba")

	putSecret(t, s, PutSecretInput{Ref: r, Value: []byte("original")})
	originalID := store.secrets[r.String()].rec.ID
	store.beforeCreateSecretVersion = func(p storage.CreateSecretParams) {
		// Simulate a different writer deleting and recreating the same ref after
		// core read the record but before storage validates its expectation.
		store.nextSecretID++
		store.secrets[r.String()] = &fakeSecret{
			rec: storage.SecretRecord{
				ID: store.nextSecretID, Ref: r, Labels: map[string]uint64{domain.LabelCurrent: 1},
			},
			versions: map[uint64]storage.SecretVersionRecord{1: {Version: 1, State: domain.StateEnabled}},
			next:     1,
			current:  1,
		}
	}

	_, err := s.PutSecret(ctx, adminPrincipal(), PutSecretInput{Ref: r, Value: []byte("stale-write")})
	if !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("PutSecret after delete/recreate err = %v, want ErrAborted", err)
	}
	if got := store.secrets[r.String()].rec.ID; got == originalID {
		t.Fatalf("test did not install replacement row: ID = %d", got)
	}
	if got := store.secrets[r.String()].next; got != 1 {
		t.Fatalf("replacement version advanced to %d after rejected stale write", got)
	}
}

func TestExactSecretVersionPinsContentTypeAndTokenProtection(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)

	putSecret(t, s, PutSecretInput{Ref: tref("api"), Value: []byte("v1"), ContentType: "text/plain"})
	v2 := putSecret(t, s, PutSecretInput{
		Ref: tref("api"), Value: []byte(`{"version":2}`), ContentType: "application/json", GenerateToken: true,
	})

	// Adding protection to v2 must not retroactively alter v1. A pinned release
	// can continue resolving v1 without a token and sees its original type.
	v1, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 1, "")
	if err != nil {
		t.Fatalf("GetSecret(v1): %v", err)
	}
	if string(v1.Value) != "v1" || v1.ContentType != "text/plain" {
		t.Fatalf("v1 = %+v, want original text version", v1)
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 2, ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("GetSecret(v2 without token) err = %v, want permission denied", err)
	}
	gotV2, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 2, "", v2.AccessToken)
	if err != nil {
		t.Fatalf("GetSecret(v2): %v", err)
	}
	if gotV2.ContentType != "application/json" {
		t.Fatalf("v2 content type = %q", gotV2.ContentType)
	}

	// Token rotation remains secret-scoped: it changes the credential used by
	// every version that was born protected, without changing whether v1 is
	// protected.
	v3 := putSecret(t, s, PutSecretInput{
		Ref: tref("api"), Value: []byte("v3"), ContentType: "text/plain", GenerateToken: true,
	})
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 2, "", v2.AccessToken); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("GetSecret(v2 with rotated-out token) err = %v, want permission denied", err)
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 2, "", v3.AccessToken); err != nil {
		t.Fatalf("GetSecret(v2 with current token): %v", err)
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 1, ""); err != nil {
		t.Fatalf("GetSecret(v1 after token rotation): %v", err)
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
		if _, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 0, "", "kmss_wrong"); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("err = %v, want ErrPermissionDenied", err)
		}
	})

	t.Run("correct token allowed", func(t *testing.T) {
		val, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 0, "", res.AccessToken)
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
	seedTokenNS(store)
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
	ev, ok := store.lastAudit()
	if !ok || ev.EventType != "secret.reveal" || ev.ResourceNamespaceID != store.namespaces[tns.String()].ID {
		t.Fatalf("secret.reveal audit = %+v, want bound namespace incarnation", ev)
	}
}

func TestRevealClientBoundUsesVersionToken(t *testing.T) {
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

	withoutToken := adminPrincipal()
	_, missingErr := s.RevealSecret(ctx, withoutToken, tref("cb"), 0, "")
	if !errors.Is(missingErr, domain.ErrDecryptFailed) {
		t.Fatalf("missing token err = %v, want ErrDecryptFailed", missingErr)
	}

	_, wrongErr := s.RevealSecret(ctx, adminPrincipal(), tref("cb"), 0, "", "kmss_wrongwrongwrongwrongwrongwrong")
	if !errors.Is(wrongErr, domain.ErrDecryptFailed) {
		t.Fatalf("wrong token err = %v, want ErrDecryptFailed", wrongErr)
	}
	if missingErr.Error() != wrongErr.Error() {
		t.Fatalf("missing and wrong tokens produced distinguishable errors: %q != %q", missingErr, wrongErr)
	}

	val, err := s.RevealSecret(ctx, adminPrincipal(), tref("cb"), 0, "", res.AccessToken)
	if err != nil {
		t.Fatalf("RevealSecret with token: %v", err)
	}
	if string(val.Value) != "v" {
		t.Fatalf("value = %q, want v", val.Value)
	}
	if !store.hasAudit("secret.reveal", "allow") || !store.hasAudit("secret.reveal", "error") {
		t.Fatal("client-bound reveal outcomes were not audited")
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
	val, err := s.GetSecret(ctx, adminPrincipal(), tref("cb"), 0, "", token)
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
	if _, err := s.PutSecret(ctx, adminPrincipal(), PutSecretInput{
		Ref: tref("cb"), Value: []byte("v2"), ContentType: "text/plain", ClientBound: true, SecretToken: token,
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
		"empty value":               {Ref: tref("s"), Value: nil},
		"bad key":                   {Ref: domain.Ref{NS: tns, Key: "bad key"}, Value: []byte("v")},
		"invalid metadata":          {Ref: tref("s"), Value: []byte("v"), Metadata: "not-json"},
		"token on new standard":     {Ref: tref("new-standard"), Value: []byte("v"), SecretToken: "unused"},
		"token on new client-bound": {Ref: tref("new-client-bound"), Value: []byte("v"), ClientBound: true, GenerateToken: true, SecretToken: "unused"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.PutSecret(ctx, adminPrincipal(), in); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
		})
	}

	putSecret(t, s, PutSecretInput{Ref: tref("standard"), Value: []byte("v")})
	if _, err := s.PutSecret(ctx, adminPrincipal(), PutSecretInput{
		Ref: tref("standard"), Value: []byte("v2"), SecretToken: "unused",
	}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("token on existing standard secret err = %v, want ErrInvalidArgument", err)
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
