package core

import (
	"context"
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestClientBoundTokenRotation(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)

	// Create with a minted token.
	res := putSecret(t, s, PutSecretInput{
		Path: "/cb", Value: []byte("v1"), ContentType: "text/plain",
		ClientBound: true, GenerateToken: true,
	})
	t1 := res.AccessToken

	// Rotate: new version, supply the current token, request a fresh one.
	upd := adminPrincipal()
	upd.SecretToken = t1
	res2, err := s.PutSecret(ctx, upd, PutSecretInput{
		Path: "/cb", Value: []byte("v2"), ContentType: "text/plain",
		ClientBound: true, GenerateToken: true,
	})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	t2 := res2.AccessToken
	if t2 == "" || t2 == t1 {
		t.Fatalf("rotation did not mint a distinct token (t1=%q t2=%q)", t1, t2)
	}

	// The new token reads the current version.
	prNew := adminPrincipal()
	prNew.SecretToken = t2
	val, err := s.GetSecret(ctx, prNew, "/cb", 0, "")
	if err != nil {
		t.Fatalf("read with new token: %v", err)
	}
	if string(val.Value) != "v2" {
		t.Fatalf("value = %q, want v2", val.Value)
	}

	// The old token cannot decrypt the new current version (wrong key -> generic
	// decryption failure, indistinguishable from tampering).
	prOld := adminPrincipal()
	prOld.SecretToken = t1
	if _, err := s.GetSecret(ctx, prOld, "/cb", 0, ""); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("read current with rotated-away token err = %v, want ErrDecryptFailed", err)
	}
	// But the old token still reads the version it originally encrypted (v1):
	// rotation must not orphan prior versions.
	if val, err := s.GetSecret(ctx, prOld, "/cb", 1, ""); err != nil || string(val.Value) != "v1" {
		t.Fatalf("read v1 with old token = %q err=%v, want v1", val.Value, err)
	}
}

func TestPutSecretRejectsOversizeValue(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)

	big := make([]byte, maxValueBytes+1)
	_, err := s.PutSecret(ctx, adminPrincipal(), PutSecretInput{
		Path: "/s", Value: big, ContentType: "application/octet-stream",
	})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("oversize err = %v, want ErrInvalidArgument", err)
	}
}

func TestPutParameterRejectsOversizeValue(t *testing.T) {
	ctx := context.Background()
	s := newTestService(newFakeStore())
	big := make([]byte, maxValueBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	_, _, err := s.PutParameter(ctx, adminPrincipal(), "/p", string(big), "string", "{}")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("oversize err = %v, want ErrInvalidArgument", err)
	}
}

func TestIdentityTokensAreDistinct(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	_, ta, err := s.CreateIdentity(ctx, adminPrincipal(), "svc-a", domain.IdentityKindClient)
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	_, tb, err := s.CreateIdentity(ctx, adminPrincipal(), "svc-b", domain.IdentityKindClient)
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if ta == tb {
		t.Fatal("two identities received the same token")
	}
	tc, err := s.RotateIdentityToken(ctx, adminPrincipal(), "svc-a")
	if err != nil {
		t.Fatalf("rotate a: %v", err)
	}
	if tc == ta {
		t.Fatal("rotation returned the same token")
	}
}

func TestReauthorizeWatchReflectsPolicyChange(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	store.addIdentity("app", domain.IdentityKindClient, "kms_tok")
	pr := clientPrincipalTok("app", "kms_tok")

	// With no policy, the fresh predicate denies.
	check1, err := s.ReauthorizeWatch(ctx, pr)
	if err != nil {
		t.Fatalf("ReauthorizeWatch: %v", err)
	}
	if check1(domain.ResourceSecret, "/prod/x") {
		t.Fatal("predicate granted access with no policy")
	}

	// After granting a policy, a newly obtained predicate reflects it.
	store.addPolicy(domain.Policy{Name: "r", Subject: "app", Allow: []domain.PolicyRule{
		{Operation: domain.OpSecretRead, Path: "/prod/*"},
	}})
	check2, err := s.ReauthorizeWatch(ctx, pr)
	if err != nil {
		t.Fatalf("ReauthorizeWatch after grant: %v", err)
	}
	if !check2(domain.ResourceSecret, "/prod/x") {
		t.Fatal("fresh predicate did not reflect the new policy")
	}
}
