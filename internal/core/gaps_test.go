package core

import (
	"context"
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

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
