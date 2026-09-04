package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

const (
	testBindingKeyA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testBindingKeyB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testBindingKeyC = "cccccccccccccccccccccccccccccccc"
)

type failFirstAuditStore struct {
	*fakeStore
	calls int
}

func (f *failFirstAuditStore) AppendAudit(ctx context.Context, event domain.AuditEvent) error {
	f.calls++
	if f.calls == 1 {
		return errors.New("one-shot audit failure")
	}
	return f.fakeStore.AppendAudit(ctx, event)
}

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

	val, err := s.GetSecret(ctx, adminPrincipal(), tref("db"), 0, "", "", "")
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

	cur, err := s.GetSecret(ctx, adminPrincipal(), tref("db"), 0, "", "", "")
	if err != nil || string(cur.Value) != "v2" {
		t.Fatalf("current = %q, %v; want v2", cur.Value, err)
	}
	old, err := s.GetSecret(ctx, adminPrincipal(), tref("db"), 1, "", "", "")
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
	v1, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 1, "", "", "")
	if err != nil {
		t.Fatalf("GetSecret(v1): %v", err)
	}
	if string(v1.Value) != "v1" || v1.ContentType != "text/plain" {
		t.Fatalf("v1 = %+v, want original text version", v1)
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 2, "", "", ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("GetSecret(v2 without token) err = %v, want permission denied", err)
	}
	gotV2, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 2, "", v2.AccessToken, "")
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
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 2, "", v2.AccessToken, ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("GetSecret(v2 with rotated-out token) err = %v, want permission denied", err)
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 2, "", v3.AccessToken, ""); err != nil {
		t.Fatalf("GetSecret(v2 with current token): %v", err)
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 1, "", "", ""); err != nil {
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
		_, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 0, "", "", "")
		if !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("err = %v, want ErrPermissionDenied", err)
		}
		if !store.hasAudit("secret.read", "deny") {
			t.Error("token denial not audited")
		}
	})

	t.Run("wrong token denied", func(t *testing.T) {
		if _, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 0, "", "kmss_wrong", ""); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("err = %v, want ErrPermissionDenied", err)
		}
	})

	t.Run("correct token allowed", func(t *testing.T) {
		val, err := s.GetSecret(ctx, adminPrincipal(), tref("api"), 0, "", res.AccessToken, "")
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
		_, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 0, "", "", "")
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
		_, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 1, "", "", "")
		if !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("err = %v, want ErrFailedPrecondition", err)
		}
	})

	t.Run("enabled with destroyed timestamp", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withKeyring(t, s)
		putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain"})
		store.markVersionDestroyedAt(tref("s"), 1)
		_, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 1, "", "", "")
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
		_, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 0, "", "", "")
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

	_, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 0, "", "", "")
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
	val, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 0, "", "", "")
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

	_, err := s.RevealSecret(ctx, clientPrincipal("app"), tref("s"), 0, "", "")
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

	val, err := s.RevealSecret(ctx, adminPrincipal(), tref("s"), 0, "", "")
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

func TestBoundSecretCredentialsAreIndependentAndVersionExact(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)

	// v1 is bound but not token-gated. v2 is unbound and establishes a
	// secret-level access token. v3 is both bound and token-gated by inheritance.
	putSecret(t, s, PutSecretInput{
		Ref: tref("credentials"), Value: []byte("v1"), BindingKey: testBindingKeyA,
	})
	v2 := putSecret(t, s, PutSecretInput{
		Ref: tref("credentials"), Value: []byte("v2"), GenerateToken: true,
	})
	putSecret(t, s, PutSecretInput{
		Ref: tref("credentials"), Value: []byte("v3"), BindingKey: testBindingKeyA,
	})

	if value, err := s.GetSecret(ctx, adminPrincipal(), tref("credentials"), 1, "", "", testBindingKeyA); err != nil || string(value.Value) != "v1" {
		t.Fatalf("bound-only v1 = %q, err=%v", value.Value, err)
	}
	if value, err := s.GetSecret(ctx, adminPrincipal(), tref("credentials"), 2, "", v2.AccessToken, "ignored extra key"); err != nil || string(value.Value) != "v2" {
		t.Fatalf("token-only v2 = %q, err=%v", value.Value, err)
	}

	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("credentials"), 3, "", "", testBindingKeyA); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("v3 without access token err = %v, want permission denied", err)
	}
	missingKeyErr := func() error {
		_, err := s.GetSecret(ctx, adminPrincipal(), tref("credentials"), 3, "", v2.AccessToken, "")
		return err
	}()
	wrongKeyErr := func() error {
		_, err := s.GetSecret(ctx, adminPrincipal(), tref("credentials"), 3, "", v2.AccessToken, testBindingKeyB)
		return err
	}()
	if !errors.Is(missingKeyErr, domain.ErrDecryptFailed) || !errors.Is(wrongKeyErr, domain.ErrDecryptFailed) || missingKeyErr.Error() != wrongKeyErr.Error() {
		t.Fatalf("missing/wrong binding-key errors differ: %v / %v", missingKeyErr, wrongKeyErr)
	}
	if value, err := s.GetSecret(ctx, adminPrincipal(), tref("credentials"), 3, "", v2.AccessToken, testBindingKeyA); err != nil || string(value.Value) != "v3" {
		t.Fatalf("both credentials v3 = %q, err=%v", value.Value, err)
	}

	info, err := s.GetSecretInfo(ctx, adminPrincipal(), tref("credentials"))
	if err != nil {
		t.Fatalf("GetSecretInfo: %v", err)
	}
	if !info.Bound || !info.HasAccessToken || len(info.Versions) != 3 {
		t.Fatalf("current metadata = %+v", info)
	}
	wantFlags := [][2]bool{{true, false}, {false, true}, {true, true}}
	for i, want := range wantFlags {
		if got := [2]bool{info.Versions[i].Bound, info.Versions[i].HasAccessToken}; got != want {
			t.Fatalf("v%d flags = %v, want %v", i+1, got, want)
		}
	}

	if _, err := s.DisableSecret(ctx, adminPrincipal(), tref("credentials"), 3, false); err != nil {
		t.Fatalf("disable v3: %v", err)
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("credentials"), 3, "", "wrong-token", testBindingKeyA); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("wrong token probed disabled state: %v", err)
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("credentials"), 3, "", v2.AccessToken, testBindingKeyB); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("wrong binding key probed disabled state: %v", err)
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("credentials"), 3, "", v2.AccessToken, testBindingKeyA); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("correct credentials did not expose disabled state: %v", err)
	}
}

func TestRevealBoundSecretBypassesTokenOnly(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)

	putSecret(t, s, PutSecretInput{
		Ref: tref("bound-reveal"), Value: []byte("v"), BindingKey: testBindingKeyA, GenerateToken: true,
	})
	_, missingErr := s.RevealSecret(ctx, adminPrincipal(), tref("bound-reveal"), 0, "", "")
	_, wrongErr := s.RevealSecret(ctx, adminPrincipal(), tref("bound-reveal"), 0, "", testBindingKeyB)
	if !errors.Is(missingErr, domain.ErrDecryptFailed) || !errors.Is(wrongErr, domain.ErrDecryptFailed) {
		t.Fatalf("missing/wrong binding-key errors = %v / %v", missingErr, wrongErr)
	}
	if missingErr.Error() != wrongErr.Error() {
		t.Fatalf("missing and wrong keys produced distinguishable errors: %q != %q", missingErr, wrongErr)
	}

	// No secret access token is supplied: Reveal bypasses that independent gate.
	val, err := s.RevealSecret(ctx, adminPrincipal(), tref("bound-reveal"), 0, "", testBindingKeyA)
	if err != nil {
		t.Fatalf("RevealSecret with binding key: %v", err)
	}
	if string(val.Value) != "v" {
		t.Fatalf("value = %q, want v", val.Value)
	}
	if !store.hasAudit("secret.reveal", "allow") || !store.hasAudit("secret.reveal", "error") {
		t.Fatal("bound reveal outcomes were not audited")
	}
}

func TestPutSecretAllowsAlternatingBindingKeys(t *testing.T) {
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	ref := tref("epochs")

	inputs := []PutSecretInput{
		{Ref: ref, Value: []byte("a1"), BindingKey: testBindingKeyA},
		{Ref: ref, Value: []byte("plain")},
		{Ref: ref, Value: []byte("b1"), BindingKey: testBindingKeyB},
		{Ref: ref, Value: []byte("b2"), BindingKey: testBindingKeyB},
		{Ref: ref, Value: []byte("a2"), BindingKey: testBindingKeyA},
	}
	for _, input := range inputs {
		putSecret(t, s, input)
	}

	wantBound := []bool{true, false, true, true, true}
	for i, want := range wantBound {
		if got := store.secrets[ref.String()].versions[uint64(i+1)].Bound; got != want {
			t.Fatalf("v%d bound = %v, want %v", i+1, got, want)
		}
	}
	if !store.secrets[ref.String()].rec.Bound {
		t.Fatal("current summary did not follow bound v5")
	}
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("plain2")})
	if store.secrets[ref.String()].rec.Bound {
		t.Fatal("current summary did not follow unbound v6")
	}
}

func TestPutSecretValidation(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)

	cases := map[string]PutSecretInput{
		"empty value":           {Ref: tref("s"), Value: nil},
		"bad key":               {Ref: domain.Ref{NS: tns, Key: "bad key"}, Value: []byte("v")},
		"invalid metadata":      {Ref: tref("s"), Value: []byte("v"), Metadata: "not-json"},
		"short binding key":     {Ref: tref("short"), Value: []byte("v"), BindingKey: "too-short"},
		"invalid utf8 bind key": {Ref: tref("utf8"), Value: []byte("v"), BindingKey: strings.Repeat("a", 32) + "\xff"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.PutSecret(ctx, adminPrincipal(), in); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestBindingMutationNewKeysRemainInvalidArguments(t *testing.T) {
	badKeys := map[string]string{
		"missing":      "",
		"short":        "too-short",
		"invalid utf8": strings.Repeat("a", 32) + "\xff",
	}
	for name, key := range badKeys {
		t.Run("bind/"+name, func(t *testing.T) {
			store := newFakeStore()
			s := newTestService(store)
			withKeyring(t, s)
			ref := tref("bind-new-key-validation")
			putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("value")})
			before := store.secrets[ref.String()].versions[1]
			beforeRevision := store.revision
			auditsBefore := len(store.audits)

			_, err := s.BindSecret(context.Background(), adminPrincipal(), ref, 1, key)
			if !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("BindSecret error = %v, want ErrInvalidArgument", err)
			}
			if got := store.secrets[ref.String()].versions[1]; !reflect.DeepEqual(got, before) || store.revision != beforeRevision {
				t.Fatal("invalid new bind key mutated the secret")
			}
			if len(store.audits) != auditsBefore+1 {
				t.Fatalf("invalid bind key appended %d audits, want 1", len(store.audits)-auditsBefore)
			}
			audit := store.audits[len(store.audits)-1]
			if audit.EventType != "secret.bind" || audit.Decision != "error" || audit.Metadata != "{}" || key != "" && strings.Contains(fmt.Sprintf("%+v", audit), key) {
				t.Fatalf("invalid bind-key audit was absent or unsafe: %+v", audit)
			}
		})

		t.Run("rotate-new/"+name, func(t *testing.T) {
			store := newFakeStore()
			s := newTestService(store)
			withKeyring(t, s)
			ref := tref("rotate-new-key-validation")
			putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("value"), BindingKey: testBindingKeyA})
			before := store.secrets[ref.String()].versions[1]
			beforeRevision := store.revision
			auditsBefore := len(store.audits)

			_, err := s.RotateSecretBindingKey(context.Background(), adminPrincipal(), ref, 1, testBindingKeyA, key)
			if !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("RotateSecretBindingKey error = %v, want ErrInvalidArgument", err)
			}
			if got := store.secrets[ref.String()].versions[1]; !reflect.DeepEqual(got, before) || store.revision != beforeRevision {
				t.Fatal("invalid new rotation key mutated the secret")
			}
			if len(store.audits) != auditsBefore+1 {
				t.Fatalf("invalid rotation key appended %d audits, want 1", len(store.audits)-auditsBefore)
			}
			audit := store.audits[len(store.audits)-1]
			if audit.EventType != "secret.binding_key.rotate" || audit.Decision != "error" || audit.Metadata != "{}" || key != "" && strings.Contains(fmt.Sprintf("%+v", audit), key) {
				t.Fatalf("invalid rotation-key audit was absent or unsafe: %+v", audit)
			}
		})
	}
}

func TestBindingTransitionsAuditMissingExpectedCurrentVersion(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		run       func(*Service, domain.Ref) error
	}{
		{
			name: "bind", eventType: "secret.bind",
			run: func(s *Service, ref domain.Ref) error {
				_, err := s.BindSecret(context.Background(), adminPrincipal(), ref, 0, testBindingKeyA)
				return err
			},
		},
		{
			name: "unbind", eventType: "secret.unbind",
			run: func(s *Service, ref domain.Ref) error {
				_, err := s.UnbindSecret(context.Background(), adminPrincipal(), ref, 0, testBindingKeyA)
				return err
			},
		},
		{
			name: "rotate", eventType: "secret.binding_key.rotate",
			run: func(s *Service, ref domain.Ref) error {
				_, err := s.RotateSecretBindingKey(context.Background(), adminPrincipal(), ref, 0, testBindingKeyA, testBindingKeyB)
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			s, hub := newTestServiceWithHub(t, store)
			ref := tref("missing-current-guard")
			storeCalls := 0
			store.beforeBindingOperation = func(string) { storeCalls++ }

			err := tc.run(s, ref)
			if !errors.Is(err, domain.ErrInvalidArgument) || !strings.Contains(err.Error(), "expected_current_version is required") {
				t.Fatalf("error = %v, want missing-guard invalid argument", err)
			}
			if storeCalls != 0 || store.revision != 0 || hub.wakes != 0 {
				t.Fatalf("missing guard reached mutation path: store calls=%d revision=%d wakes=%d", storeCalls, store.revision, hub.wakes)
			}
			if len(store.audits) != 1 {
				t.Fatalf("audit count = %d, want 1", len(store.audits))
			}
			audit := store.audits[0]
			if audit.EventType != tc.eventType || audit.Decision != "error" || audit.ResourceVersion != 0 || audit.Metadata != "{}" ||
				audit.ResourceEnv != ref.NS.Env || audit.ResourceApp != ref.NS.App || audit.ResourceKey != ref.Key {
				t.Fatalf("missing-guard audit = %+v", audit)
			}
			for _, key := range []string{testBindingKeyA, testBindingKeyB} {
				if strings.Contains(fmt.Sprintf("%+v", audit), key) || strings.Contains(err.Error(), key) {
					t.Fatal("binding key leaked from missing-guard failure")
				}
			}
		})
	}
}

func TestBindingMutationUnlockKeysCollapseWithoutMutationOrLeakage(t *testing.T) {
	type operation struct {
		name      string
		eventType string
		run       func(*Service, domain.Ref, string, uint64) error
	}
	operations := []operation{
		{name: "unbind", eventType: "secret.unbind", run: func(s *Service, ref domain.Ref, key string, _ uint64) error {
			_, err := s.UnbindSecret(context.Background(), adminPrincipal(), ref, 1, key)
			return err
		}},
		{name: "preview", eventType: "secret.binding_cohort.preview", run: func(s *Service, ref domain.Ref, key string, _ uint64) error {
			_, err := s.PreviewSecretBindingCohort(context.Background(), adminPrincipal(), ref, 1, key)
			return err
		}},
		{name: "rotate-old", eventType: "secret.binding_key.rotate", run: func(s *Service, ref domain.Ref, key string, _ uint64) error {
			_, err := s.RotateSecretBindingKey(context.Background(), adminPrincipal(), ref, 1, key, testBindingKeyC)
			return err
		}},
		{name: "purge", eventType: "secret.binding_cohort.purge", run: func(s *Service, ref domain.Ref, key string, revision uint64) error {
			_, err := s.PurgeSecretBindingCohort(context.Background(), adminPrincipal(), ref, 1, key, new(revision), []uint64{1})
			return err
		}},
	}
	credentials := []struct {
		name string
		key  string
	}{
		{name: "missing", key: ""},
		{name: "short", key: "too-short"},
		{name: "invalid-utf8", key: strings.Repeat("x", 32) + "\xff"},
		{name: "wrong", key: testBindingKeyB},
	}

	for _, op := range operations {
		for _, credential := range credentials {
			t.Run(op.name+"/"+credential.name, func(t *testing.T) {
				store := newFakeStore()
				s, hub := newTestServiceWithHub(t, store)
				observedCore, observed := observer.New(zap.DebugLevel)
				s.log = zap.New(observedCore)
				ref := tref("unlock-key-collapse")
				putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("value"), BindingKey: testBindingKeyA})
				hub.wakes = 0
				before := store.secrets[ref.String()].versions[1]
				beforeRevision := store.revision
				auditsBefore := len(store.audits)

				err := op.run(s, ref, credential.key, beforeRevision)
				if err != domain.ErrDecryptFailed || err.Error() != domain.ErrDecryptFailed.Error() {
					t.Fatalf("error = %#v, want canonical ErrDecryptFailed", err)
				}
				if credential.key != "" && strings.Contains(err.Error(), credential.key) {
					t.Fatal("binding key appeared in returned error")
				}
				if got := store.secrets[ref.String()].versions[1]; !reflect.DeepEqual(got, before) || store.revision != beforeRevision {
					t.Fatalf("%s with an unusable key mutated the secret", op.name)
				}
				if hub.wakes != 0 {
					t.Fatalf("%s with an unusable key woke watchers %d times", op.name, hub.wakes)
				}
				if len(store.audits) != auditsBefore+1 {
					t.Fatalf("audit count = %d, want %d", len(store.audits), auditsBefore+1)
				}
				errorAudit := store.audits[len(store.audits)-1]
				if errorAudit.EventType != op.eventType || errorAudit.Decision != "error" || errorAudit.Metadata != "{}" ||
					errorAudit.ResourceNamespaceID != store.namespaces[tns.String()].ID || errorAudit.ResourceVersion != 1 {
					t.Fatalf("error audit = %+v", errorAudit)
				}
				for _, audit := range store.audits {
					if credential.key != "" && strings.Contains(fmt.Sprintf("%+v", audit), credential.key) {
						t.Fatal("binding key appeared in an audit event")
					}
				}
				if credential.key != "" && observed.FilterMessageSnippet(credential.key).Len() != 0 {
					t.Fatal("binding key appeared in logs")
				}
			})
		}
	}
}

func TestBindUnbindAuthorizedFailuresAreAuditedAndRedacted(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		invoke    func(*Service, domain.Ref) error
	}{
		{
			name:      "bind",
			eventType: "secret.bind",
			invoke: func(s *Service, ref domain.Ref) error {
				_, err := s.BindSecret(context.Background(), adminPrincipal(), ref, 1, testBindingKeyB)
				return err
			},
		},
		{
			name:      "unbind",
			eventType: "secret.unbind",
			invoke: func(s *Service, ref domain.Ref) error {
				_, err := s.UnbindSecret(context.Background(), adminPrincipal(), ref, 1, testBindingKeyB)
				return err
			},
		},
		{
			name:      "rotate",
			eventType: "secret.binding_key.rotate",
			invoke: func(s *Service, ref domain.Ref) error {
				_, err := s.RotateSecretBindingKey(context.Background(), adminPrincipal(), ref, 1, testBindingKeyB, testBindingKeyB)
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			s := newTestService(store)
			withKeyring(t, s)
			ref := tref(tc.name + "-failure-audit")
			putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("value"), BindingKey: testBindingKeyA})
			before := store.secrets[ref.String()].versions[1]
			beforeRevision := store.revision
			auditsBefore := len(store.audits)

			err := tc.invoke(s, ref)
			if err == nil {
				t.Fatal("mutation unexpectedly succeeded")
			}
			if got := store.secrets[ref.String()].versions[1]; !reflect.DeepEqual(got, before) || store.revision != beforeRevision {
				t.Fatal("failed mutation changed the secret")
			}
			if len(store.audits) != auditsBefore+1 {
				t.Fatalf("audit count = %d, want %d", len(store.audits), auditsBefore+1)
			}
			audit := store.audits[len(store.audits)-1]
			if audit.EventType != tc.eventType || audit.Decision != "error" || audit.ResourceNamespaceID != store.namespaces[tns.String()].ID ||
				audit.ResourceEnv != ref.NS.Env || audit.ResourceApp != ref.NS.App || audit.ResourceKey != ref.Key || audit.ResourceVersion != 1 || audit.Metadata != "{}" {
				t.Fatalf("error audit = %+v", audit)
			}
			for _, key := range []string{testBindingKeyA, testBindingKeyB} {
				if strings.Contains(fmt.Sprintf("%+v", audit), key) || strings.Contains(err.Error(), key) {
					t.Fatalf("binding key leaked from failed %s", tc.name)
				}
			}
		})
	}
}

func TestProtectionTransitionsCreateImmutableVersions(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s, hub := newTestServiceWithHub(t, store)
	ref := tref("versioned-protection")
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("immutable value"), ContentType: "text/plain", Metadata: `{"owner":"ops"}`})
	hub.wakes = 0
	source := store.secrets[ref.String()].versions[1]

	boundResult, err := s.BindSecret(ctx, adminPrincipal(), ref, 1, testBindingKeyA)
	if err != nil {
		t.Fatalf("BindSecret: %v", err)
	}
	if boundResult.CurrentVersion != 2 || boundResult.PreviousVersion != 1 || hub.wakes != 1 {
		t.Fatalf("bind result=%+v wakes=%d", boundResult, hub.wakes)
	}
	if got := store.secrets[ref.String()].versions[1]; !reflect.DeepEqual(got, source) {
		t.Fatal("bind changed its source version")
	}
	bound := store.secrets[ref.String()].versions[2]
	if !bound.Bound || bound.ContentType != source.ContentType || bound.Metadata != source.Metadata || bound.State != source.State || bound.HasAccessToken != source.HasAccessToken {
		t.Fatalf("bound clone did not preserve properties: %+v", bound)
	}
	if bytes.Equal(bound.Ciphertext, source.Ciphertext) || bytes.Equal(bound.EncryptedDEK, source.EncryptedDEK) || bytes.Equal(bound.Nonce, source.Nonce) || bound.AAD == source.AAD {
		t.Fatal("bind reused cryptographic material")
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), ref, 2, "", "", testBindingKeyB); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("bound clone accepted wrong key: %v", err)
	}
	if got, err := s.GetSecret(ctx, adminPrincipal(), ref, 2, "", "", testBindingKeyA); err != nil || string(got.Value) != "immutable value" {
		t.Fatalf("bound clone read=%q err=%v", got.Value, err)
	}
	if got, err := s.GetSecret(ctx, adminPrincipal(), ref, 1, "", "", ""); err != nil || string(got.Value) != "immutable value" {
		t.Fatalf("source read=%q err=%v", got.Value, err)
	}
	if _, err := s.RotateSecretBindingKey(ctx, adminPrincipal(), ref, 2, testBindingKeyB, testBindingKeyB); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("wrong unchanged rotation key err=%v, want decrypt failed", err)
	}
	if _, err := s.RotateSecretBindingKey(ctx, adminPrincipal(), ref, 2, testBindingKeyA, testBindingKeyA); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("authenticated unchanged rotation key err=%v, want invalid argument", err)
	}

	rotatedResult, err := s.RotateSecretBindingKey(ctx, adminPrincipal(), ref, 2, testBindingKeyA, testBindingKeyB)
	if err != nil {
		t.Fatalf("RotateSecretBindingKey: %v", err)
	}
	if rotatedResult.CurrentVersion != 3 || rotatedResult.PreviousVersion != 2 {
		t.Fatalf("rotation result=%+v", rotatedResult)
	}
	if got := store.secrets[ref.String()].versions[2]; !reflect.DeepEqual(got, bound) {
		t.Fatal("rotation changed the historical bound source")
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), ref, 2, "", "", testBindingKeyA); err != nil {
		t.Fatalf("historical version no longer accepts old key: %v", err)
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), ref, 3, "", "", testBindingKeyA); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("rotated version accepted old key: %v", err)
	}

	unboundResult, err := s.UnbindSecret(ctx, adminPrincipal(), ref, 3, testBindingKeyB)
	if err != nil {
		t.Fatalf("UnbindSecret: %v", err)
	}
	if unboundResult.CurrentVersion != 4 || unboundResult.PreviousVersion != 3 || store.secrets[ref.String()].versions[4].Bound {
		t.Fatalf("unbind result=%+v row=%+v", unboundResult, store.secrets[ref.String()].versions[4])
	}
	if got, err := s.GetSecret(ctx, adminPrincipal(), ref, 4, "", "", ""); err != nil || string(got.Value) != "immutable value" {
		t.Fatalf("unbound clone read=%q err=%v", got.Value, err)
	}
	if _, err := s.BindSecret(ctx, adminPrincipal(), ref, 3, testBindingKeyC); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("stale current guard err=%v", err)
	}
}

func TestUnboundVersionPurgeRequiresAdminAndExactPreview(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	s, hub := newTestServiceWithHub(t, store)
	ref := tref("purge-unbound")
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("plain-1")})
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("bound"), BindingKey: testBindingKeyA})
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("plain-2")})
	store.addPolicy(domain.Policy{Name: "delegated-destroy", Subject: "app", Allow: []domain.PolicyRule{{Operation: domain.OpSecretDestroy, Env: "prod", App: "app"}}})

	storeCalls := 0
	store.beforeBindingOperation = func(string) { storeCalls++ }
	if _, err := s.PreviewSecretUnboundVersions(ctx, clientPrincipal("app"), ref); !errors.Is(err, domain.ErrPermissionDenied) || storeCalls != 0 {
		t.Fatalf("non-admin preview err=%v store-calls=%d", err, storeCalls)
	}
	preview, err := s.PreviewSecretUnboundVersions(ctx, adminPrincipal(), ref)
	if err != nil || !slices.Equal(preview.AffectedVersions, []uint64{1, 3}) {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	putSecret(t, s, PutSecretInput{Ref: tref("revision-change"), Value: []byte("x")})
	if _, err := s.PurgeSecretUnboundVersions(ctx, adminPrincipal(), ref, preview.Revision, preview.AffectedVersions); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("stale purge err=%v", err)
	}
	preview, err = s.PreviewSecretUnboundVersions(ctx, adminPrincipal(), ref)
	if err != nil {
		t.Fatal(err)
	}
	hub.wakes = 0
	result, err := s.PurgeSecretUnboundVersions(ctx, adminPrincipal(), ref, preview.Revision, preview.AffectedVersions)
	if err != nil || !slices.Equal(result.AffectedVersions, []uint64{1, 3}) || hub.wakes != 1 {
		t.Fatalf("purge=%+v err=%v wakes=%d", result, err, hub.wakes)
	}
	if store.secrets[ref.String()].versions[2].State == domain.StateDestroyed {
		t.Fatal("unbound purge destroyed a bound version")
	}
	if got := store.secrets[ref.String()].rec.Labels[domain.LabelCurrent]; got != 3 {
		t.Fatalf("purge changed current label to %d", got)
	}
}

func TestUnboundVersionPurgeRollsBackWhenTransactionalAuditUnavailable(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s, hub := newTestServiceWithHub(t, store)
	ref := tref("purge-unbound-audit-rollback")
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("plain")})
	preview, err := s.PreviewSecretUnboundVersions(ctx, adminPrincipal(), ref)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	before := store.secrets[ref.String()].versions[1]
	beforeRevision := store.revision
	auditsBefore := len(store.audits)
	hub.wakes = 0
	store.auditErr = errors.New("audit unavailable")

	if _, err := s.PurgeSecretUnboundVersions(ctx, adminPrincipal(), ref, preview.Revision, preview.AffectedVersions); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("purge audit failure = %v, want failed precondition", err)
	}
	if got := store.secrets[ref.String()].versions[1]; !reflect.DeepEqual(got, before) || store.revision != beforeRevision {
		t.Fatal("unbound purge audit failure changed the version or revision")
	}
	if len(store.audits) != auditsBefore || hub.wakes != 0 {
		t.Fatalf("failed purge appended audits or woke watchers: audits=%d wakes=%d", len(store.audits)-auditsBefore, hub.wakes)
	}
}

func TestUnboundVersionPurgeCleanupPendingReturnsCommittedResultAndWakes(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s, hub := newTestServiceWithHub(t, store)
	ref := tref("purge-unbound-cleanup-pending")
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("plain")})
	preview, err := s.PreviewSecretUnboundVersions(ctx, adminPrincipal(), ref)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	store.purgeResultErr = storage.ErrPurgeCleanupPending
	auditsBefore := len(store.audits)
	hub.wakes = 0

	result, err := s.PurgeSecretUnboundVersions(ctx, adminPrincipal(), ref, preview.Revision, preview.AffectedVersions)
	if err != storage.ErrPurgeCleanupPending {
		t.Fatalf("error = %#v, want canonical ErrPurgeCleanupPending", err)
	}
	if !slices.Equal(result.AffectedVersions, []uint64{1}) || result.Revision <= preview.Revision {
		t.Fatalf("committed result = %+v", result)
	}
	if got := store.secrets[ref.String()].versions[1]; got.State != domain.StateDestroyed {
		t.Fatalf("logical purge did not commit: %+v", got)
	}
	if len(store.audits) != auditsBefore+1 || hub.wakes != 1 {
		t.Fatalf("cleanup-pending purge audits=%d wakes=%d", len(store.audits)-auditsBefore, hub.wakes)
	}
	if audit := store.audits[len(store.audits)-1]; audit.EventType != "secret.unbound_versions.purge" || audit.Decision != "allow" {
		t.Fatalf("cleanup-pending audit = %+v", audit)
	}
}

func TestPurgeBindingCohortAdminOnlyAtomicAndRedacted(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	s := newTestService(store)
	withKeyring(t, s)
	observedCore, observed := observer.New(zap.DebugLevel)
	s.log = zap.New(observedCore)
	ref := tref("purge")

	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("a1"), BindingKey: testBindingKeyA})
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("plain")})
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("b1"), Metadata: `{"sensitive":"operator note"}`, BindingKey: testBindingKeyB})
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("b2"), BindingKey: testBindingKeyB})
	store.addPolicy(domain.Policy{Name: "delegated-destroy", Subject: "app", Allow: []domain.PolicyRule{{Operation: domain.OpSecretDestroy, Env: "prod", App: "app"}}})

	storeCalls := 0
	store.beforeBindingOperation = func(string) { storeCalls++ }
	if _, err := s.PurgeSecretBindingCohort(ctx, clientPrincipal("app"), ref, 4, "short", nil, nil); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("delegated non-admin purge err = %v", err)
	}
	if storeCalls != 0 {
		t.Fatalf("non-admin purge reached cohort storage %d times", storeCalls)
	}

	preview, err := s.PreviewSecretBindingCohort(ctx, adminPrincipal(), ref, 4, testBindingKeyB)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	auditsBefore := len(store.audits)
	result, err := s.PurgeSecretBindingCohort(ctx, adminPrincipal(), ref, 4, testBindingKeyB, new(preview.Revision), preview.AffectedVersions)
	if err != nil {
		t.Fatalf("PurgeSecretBindingCohort: %v", err)
	}
	if !slices.Equal(result.AffectedVersions, []uint64{3, 4}) {
		t.Fatalf("purged versions = %v", result.AffectedVersions)
	}
	if len(store.audits) != auditsBefore+1 {
		t.Fatalf("purge appended %d audit rows, want exactly 1", len(store.audits)-auditsBefore)
	}
	for _, version := range []uint64{3, 4} {
		rec := store.secrets[ref.String()].versions[version]
		if rec.State != domain.StateDestroyed || len(rec.Ciphertext) != 0 || len(rec.EncryptedDEK) != 0 || len(rec.Nonce) != 0 || len(rec.BindingKeySalt) != 0 || rec.Metadata != "" {
			t.Fatalf("v%d was not reduced to a tombstone: %+v", version, rec)
		}
	}
	if store.secrets[ref.String()].versions[1].State == domain.StateDestroyed || store.secrets[ref.String()].versions[2].State == domain.StateDestroyed {
		t.Fatal("purge crossed a cohort boundary")
	}
	if got := store.secrets[ref.String()].rec.Labels[domain.LabelCurrent]; got != 4 {
		t.Fatalf("current label moved to %d during purge", got)
	}

	for _, audit := range store.audits {
		if strings.Contains(fmt.Sprintf("%+v", audit), testBindingKeyB) {
			t.Fatal("binding key appeared in audit event")
		}
	}
	if observed.FilterMessageSnippet(testBindingKeyB).Len() != 0 {
		t.Fatal("binding key appeared in logs")
	}
	if _, err := s.PreviewSecretBindingCohort(ctx, adminPrincipal(), ref, 3, testBindingKeyB); err == nil || strings.Contains(err.Error(), testBindingKeyB) {
		t.Fatalf("post-purge error was absent or leaked key: %v", err)
	}
}

func TestPurgeBindingCohortRollsBackWhenAuditUnavailable(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	ref := tref("purge-rollback")
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("v"), BindingKey: testBindingKeyA})
	before := store.secrets[ref.String()].versions[1]
	beforeRevision := store.revision
	preview, err := s.PreviewSecretBindingCohort(ctx, adminPrincipal(), ref, 1, testBindingKeyA)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	store.auditErr = errors.New("audit unavailable")

	if _, err := s.PurgeSecretBindingCohort(ctx, adminPrincipal(), ref, 1, testBindingKeyA, new(preview.Revision), preview.AffectedVersions); err == nil {
		t.Fatal("purge succeeded with unavailable transactional audit")
	}
	after := store.secrets[ref.String()].versions[1]
	if after.State != before.State || !bytes.Equal(after.Ciphertext, before.Ciphertext) || !bytes.Equal(after.EncryptedDEK, before.EncryptedDEK) || store.revision != beforeRevision {
		t.Fatal("failed purge changed the version or revision")
	}
}

func TestBindingMutationsRollBackWhenTransactionalAuditUnavailable(t *testing.T) {
	tests := []struct {
		name  string
		bound bool
		run   func(*Service, domain.Ref) error
	}{
		{
			name: "bind",
			run: func(s *Service, ref domain.Ref) error {
				_, err := s.BindSecret(context.Background(), adminPrincipal(), ref, 1, testBindingKeyA)
				return err
			},
		},
		{
			name: "unbind", bound: true,
			run: func(s *Service, ref domain.Ref) error {
				_, err := s.UnbindSecret(context.Background(), adminPrincipal(), ref, 1, testBindingKeyA)
				return err
			},
		},
		{
			name: "rotate", bound: true,
			run: func(s *Service, ref domain.Ref) error {
				_, err := s.RotateSecretBindingKey(context.Background(), adminPrincipal(), ref, 1, testBindingKeyA, testBindingKeyB)
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			s, hub := newTestServiceWithHub(t, store)
			ref := tref("audit-rollback-" + tc.name)
			in := PutSecretInput{Ref: ref, Value: []byte("value")}
			if tc.bound {
				in.BindingKey = testBindingKeyA
			}
			putSecret(t, s, in)
			hub.wakes = 0
			before := store.secrets[ref.String()].versions[1]
			beforeRevision := store.revision
			auditsBefore := len(store.audits)
			store.auditErr = errors.New("audit unavailable")

			if err := tc.run(s, ref); err == nil {
				t.Fatal("mutation succeeded with unavailable transactional audit")
			}
			if got := store.secrets[ref.String()].versions[1]; !reflect.DeepEqual(got, before) {
				t.Fatal("audit failure changed the version")
			}
			if store.revision != beforeRevision {
				t.Fatalf("audit failure revision = %d, want %d", store.revision, beforeRevision)
			}
			if len(store.audits) != auditsBefore {
				t.Fatalf("audit failure appended %d rows", len(store.audits)-auditsBefore)
			}
			if hub.wakes != 0 {
				t.Fatalf("audit failure woke watchers %d times", hub.wakes)
			}
		})
	}
}

func TestBindingCohortPreviewFailsClosedWhenAuditUnavailable(t *testing.T) {
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	ref := tref("preview-audit-fail-closed")
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("value"), BindingKey: testBindingKeyA})
	beforeRevision := store.revision
	store.auditErr = errors.New("audit unavailable")

	result, err := s.PreviewSecretBindingCohort(context.Background(), adminPrincipal(), ref, 1, testBindingKeyA)
	if !errors.Is(err, domain.ErrFailedPrecondition) || result.AnchorVersion != 0 || result.Revision != 0 || len(result.AffectedVersions) != 0 {
		t.Fatalf("preview = %+v err=%v, want empty failed-precondition response", result, err)
	}
	if store.revision != beforeRevision {
		t.Fatalf("read-only preview changed revision to %d, want %d", store.revision, beforeRevision)
	}
}

func TestUnboundVersionPreviewRecordsErrorWhenRequiredAllowAuditFails(t *testing.T) {
	base := newFakeStore()
	seedTokenNS(base)
	seedService := newTestService(base)
	withKeyring(t, seedService)
	ref := tref("unbound-preview-audit-fallback")
	putSecret(t, seedService, PutSecretInput{Ref: ref, Value: []byte("value")})
	base.audits = nil
	beforeRevision := base.revision

	store := &failFirstAuditStore{fakeStore: base}
	s := New(store, zap.NewNop(), "test")
	result, err := s.PreviewSecretUnboundVersions(context.Background(), adminPrincipal(), ref)
	if !errors.Is(err, domain.ErrFailedPrecondition) || result.Revision != 0 || len(result.AffectedVersions) != 0 {
		t.Fatalf("preview = %+v err=%v, want empty failed-precondition response", result, err)
	}
	if store.calls != 2 {
		t.Fatalf("audit append calls = %d, want failed allow plus error fallback", store.calls)
	}
	if base.revision != beforeRevision || len(base.audits) != 1 {
		t.Fatalf("preview revision=%d audits=%+v", base.revision, base.audits)
	}
	audit := base.audits[0]
	if audit.EventType != "secret.unbound_versions.preview" || audit.Decision != "error" || audit.ResourceNamespaceID != base.namespaces[tns.String()].ID ||
		audit.ResourceVersion != 0 || audit.Metadata != "{}" {
		t.Fatalf("fallback audit = %+v", audit)
	}
}

func TestBindingLifecycleAllowAuditsRemainMandatoryWhenGeneralAuditDisabled(t *testing.T) {
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	ref := tref("binding-required-audit")
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("value")})
	store.audits = nil
	s.SetAuditEnabled(false)

	if _, err := s.BindSecret(context.Background(), adminPrincipal(), ref, 1, testBindingKeyA); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, err := s.PreviewSecretBindingCohort(context.Background(), adminPrincipal(), ref, 2, testBindingKeyA); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if _, err := s.RotateSecretBindingKey(context.Background(), adminPrincipal(), ref, 2, testBindingKeyA, testBindingKeyB); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := s.UnbindSecret(context.Background(), adminPrincipal(), ref, 3, testBindingKeyB); err != nil {
		t.Fatalf("unbind: %v", err)
	}
	wantEvents := []string{"secret.bind", "secret.binding_cohort.preview", "secret.binding_key.rotate", "secret.unbind"}
	if len(store.audits) != len(wantEvents) {
		t.Fatalf("mandatory binding audits = %+v", store.audits)
	}
	for i, want := range wantEvents {
		if audit := store.audits[i]; audit.EventType != want || audit.Decision != "allow" {
			t.Fatalf("mandatory audit %d = %+v, want %s/allow", i, audit, want)
		}
	}
}

func TestPurgeCleanupPendingReturnsCommittedResultAndWakes(t *testing.T) {
	store := newFakeStore()
	s, hub := newTestServiceWithHub(t, store)
	ref := tref("purge-cleanup-pending")
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("value"), BindingKey: testBindingKeyA})
	preview, err := s.PreviewSecretBindingCohort(context.Background(), adminPrincipal(), ref, 1, testBindingKeyA)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	hub.wakes = 0
	store.purgeResultErr = storage.ErrPurgeCleanupPending
	auditsBefore := len(store.audits)

	result, err := s.PurgeSecretBindingCohort(context.Background(), adminPrincipal(), ref, 1, testBindingKeyA, new(preview.Revision), preview.AffectedVersions)
	if err != storage.ErrPurgeCleanupPending {
		t.Fatalf("error = %#v, want canonical ErrPurgeCleanupPending", err)
	}
	if result.AnchorVersion != 1 || !slices.Equal(result.AffectedVersions, []uint64{1}) || result.Revision == 0 {
		t.Fatalf("committed result = %+v", result)
	}
	if got := store.secrets[ref.String()].versions[1]; got.State != domain.StateDestroyed {
		t.Fatalf("logical purge did not commit: %+v", got)
	}
	if hub.wakes != 1 {
		t.Fatalf("watch wakes = %d, want 1", hub.wakes)
	}
	if len(store.audits) != auditsBefore+1 {
		t.Fatalf("audit count = %d, want one transactional allow row", len(store.audits)-auditsBefore)
	}
	if audit := store.audits[len(store.audits)-1]; audit.EventType != "secret.binding_cohort.purge" || audit.Decision != "allow" {
		t.Fatalf("cleanup-pending audit = %+v", audit)
	}
}

func TestBindingMutationAbortsWhenConcurrentPutAdvancesCurrent(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	ref := tref("serialized")
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("v1")})

	enteredMutation := make(chan struct{})
	releaseMutation := make(chan struct{})
	store.beforeBindingOperation = func(operation string) {
		if operation == "bind" {
			close(enteredMutation)
			<-releaseMutation
		}
	}
	bindDone := make(chan error, 1)
	go func() {
		_, err := s.BindSecret(ctx, adminPrincipal(), ref, 1, testBindingKeyA)
		bindDone <- err
	}()
	<-enteredMutation

	enteredPut := make(chan struct{})
	store.beforeCreateSecretVersion = func(storage.CreateSecretParams) { close(enteredPut) }
	putDone := make(chan error, 1)
	go func() {
		_, err := s.PutSecret(ctx, adminPrincipal(), PutSecretInput{Ref: ref, Value: []byte("v2")})
		putDone <- err
	}()
	select {
	case <-enteredPut:
	case <-time.After(time.Second):
		t.Fatal("put did not enter storage while transition was preparing")
	}
	if err := <-putDone; err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	close(releaseMutation)
	if err := <-bindDone; !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("BindSecret error = %v, want aborted stale-current guard", err)
	}
}

func TestBindingLifecycleRequiresExplicitBindingManageAuthorization(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	s := newTestService(store)
	withKeyring(t, s)
	ref := tref("binding-authz")
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("v")})
	client := clientPrincipal("app")

	if _, err := s.BindSecret(ctx, client, ref, 1, testBindingKeyA); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("unauthorized bind err = %v", err)
	}
	store.addPolicy(domain.Policy{Name: "writer", Subject: "app", Allow: []domain.PolicyRule{{Operation: domain.OpSecretWrite, Env: "prod", App: "app"}}})
	if _, err := s.BindSecret(ctx, client, ref, 1, testBindingKeyA); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("secret:write unexpectedly authorized bind: %v", err)
	}
	store.addPolicy(domain.Policy{Name: "binding-manager", Subject: "app", Allow: []domain.PolicyRule{{Operation: domain.OpSecretBindingManage, Env: "prod", App: "app"}}})
	if _, err := s.BindSecret(ctx, client, ref, 1, testBindingKeyA); err != nil {
		t.Fatalf("authorized bind: %v", err)
	}
	if _, err := s.PreviewSecretBindingCohort(ctx, client, ref, 2, testBindingKeyA); err != nil {
		t.Fatalf("authorized preview: %v", err)
	}
	if _, err := s.RotateSecretBindingKey(ctx, client, ref, 2, testBindingKeyA, testBindingKeyB); err != nil {
		t.Fatalf("authorized rotate: %v", err)
	}
	if _, err := s.UnbindSecret(ctx, client, ref, 3, testBindingKeyB); err != nil {
		t.Fatalf("authorized unbind: %v", err)
	}
}

func TestBindingLifecycleAcceptsSecretWildcardAuthorization(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	s := newTestService(store)
	withKeyring(t, s)
	ref := tref("binding-wildcard-authz")
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("v")})
	client := clientPrincipal("app")
	store.addPolicy(domain.Policy{Name: "secret-manager", Subject: "app", Allow: []domain.PolicyRule{{Operation: "secret:*", Env: "prod", App: "app"}}})

	if _, err := s.BindSecret(ctx, client, ref, 1, testBindingKeyA); err != nil {
		t.Fatalf("wildcard-authorized bind: %v", err)
	}
	if _, err := s.PreviewSecretBindingCohort(ctx, client, ref, 2, testBindingKeyA); err != nil {
		t.Fatalf("wildcard-authorized preview: %v", err)
	}
	if _, err := s.RotateSecretBindingKey(ctx, client, ref, 2, testBindingKeyA, testBindingKeyB); err != nil {
		t.Fatalf("wildcard-authorized rotate: %v", err)
	}
	if _, err := s.UnbindSecret(ctx, client, ref, 3, testBindingKeyB); err != nil {
		t.Fatalf("wildcard-authorized unbind: %v", err)
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
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 1, "", "", ""); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("disabled read err = %v, want ErrFailedPrecondition", err)
	}
	if _, err := s.DisableSecret(ctx, adminPrincipal(), tref("s"), 1, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 1, "", "", ""); err != nil {
		t.Fatalf("re-enabled read: %v", err)
	}

	if _, err := s.DestroySecretVersion(ctx, adminPrincipal(), tref("s"), 1); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if _, err := s.DisableSecret(ctx, adminPrincipal(), tref("s"), 1, true); err != nil {
		t.Fatalf("enable after destroy (store call): %v", err)
	}
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 1, "", "", ""); !errors.Is(err, domain.ErrFailedPrecondition) {
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
	val, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 0, "", "", "")
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

	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("other"), 0, "", "", ""); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("relocated read err = %v, want ErrDecryptFailed", err)
	}
	// The original location still decrypts.
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("db"), 0, "", "", ""); err != nil {
		t.Fatalf("original read: %v", err)
	}
}
