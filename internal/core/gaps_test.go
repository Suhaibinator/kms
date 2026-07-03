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

	res := putSecret(t, s, PutSecretInput{
		Ref: tref("cb"), Value: []byte("v1"), ContentType: "text/plain",
		ClientBound: true, GenerateToken: true,
	})
	t1 := res.AccessToken

	// Rotate: new version, supply the current token, request a fresh one.
	upd := adminPrincipal()
	upd.SecretToken = t1
	res2, err := s.PutSecret(ctx, upd, PutSecretInput{
		Ref: tref("cb"), Value: []byte("v2"), ContentType: "text/plain",
		ClientBound: true, GenerateToken: true,
	})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	t2 := res2.AccessToken
	if t2 == "" || t2 == t1 {
		t.Fatalf("rotation did not mint a distinct token (t1=%q t2=%q)", t1, t2)
	}

	prNew := adminPrincipal()
	prNew.SecretToken = t2
	val, err := s.GetSecret(ctx, prNew, tref("cb"), 0, "")
	if err != nil {
		t.Fatalf("read with new token: %v", err)
	}
	if string(val.Value) != "v2" {
		t.Fatalf("value = %q, want v2", val.Value)
	}

	// The old token cannot decrypt the new current version.
	prOld := adminPrincipal()
	prOld.SecretToken = t1
	if _, err := s.GetSecret(ctx, prOld, tref("cb"), 0, ""); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("read current with rotated-away token err = %v, want ErrDecryptFailed", err)
	}
	// But the old token still reads the version it originally encrypted (v1).
	if val, err := s.GetSecret(ctx, prOld, tref("cb"), 1, ""); err != nil || string(val.Value) != "v1" {
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
		Ref: tref("s"), Value: big, ContentType: "application/octet-stream",
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
	_, _, err := s.PutParameter(ctx, adminPrincipal(), tref("p"), string(big), "string", "{}")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("oversize err = %v, want ErrInvalidArgument", err)
	}
}

func TestIdentityTokensAreDistinct(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)

	a, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{
		Name: "svc-a", Kind: domain.IdentityKindClient, AuthMethods: []domain.AuthMethod{domain.AuthMethodToken},
	})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := s.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{
		Name: "svc-b", Kind: domain.IdentityKindClient, AuthMethods: []domain.AuthMethod{domain.AuthMethodToken},
	})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if a.Token == b.Token {
		t.Fatal("two identities received the same token")
	}
	tc, err := s.RotateIdentityToken(ctx, adminPrincipal(), "svc-a")
	if err != nil {
		t.Fatalf("rotate a: %v", err)
	}
	if tc == a.Token {
		t.Fatal("rotation returned the same token")
	}
}
