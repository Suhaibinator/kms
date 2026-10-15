package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

// requireEmptyValue fails unless value is a non-nil, zero-length slice: an
// empty secret must read back identically on every path, never as nil.
func requireEmptyValue(t *testing.T, what string, value []byte) {
	t.Helper()
	if value == nil || len(value) != 0 {
		t.Fatalf("%s value = %#v, want non-nil []byte{}", what, value)
	}
}

func TestPutSecretEmptyValueRoundTrips(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	ref := tref("empty")

	res := putSecret(t, s, PutSecretInput{Ref: ref, Value: nil})
	if res.Version != 1 {
		t.Fatalf("version = %d, want 1", res.Version)
	}
	got, err := s.GetSecret(ctx, adminPrincipal(), ref, 0, "", "")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	requireEmptyValue(t, "GetSecret", got.Value)
	if got.ContentType != "application/octet-stream" {
		t.Fatalf("content type = %q, want the application/octet-stream default", got.ContentType)
	}
	revealed, err := s.RevealSecret(ctx, adminPrincipal(), ref, 0, "", "")
	if err != nil {
		t.Fatalf("RevealSecret: %v", err)
	}
	requireEmptyValue(t, "RevealSecret", revealed.Value)

	// A non-JSON text type stores an empty value too.
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte{}, ContentType: "text/plain; charset=utf-8"})
	got, err = s.GetSecret(ctx, adminPrincipal(), ref, 2, "", "")
	if err != nil {
		t.Fatalf("GetSecret v2: %v", err)
	}
	requireEmptyValue(t, "GetSecret v2", got.Value)
}

func TestPutSecretRejectsEmptyJSONValue(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)

	for _, contentType := range []string{
		"application/json",
		"Application/JSON",
		"application/json; charset=utf-8",
		"application/vnd.foo+json",
		"application/problem+json; charset=utf-8",
		"application/json; charset",
	} {
		t.Run(contentType, func(t *testing.T) {
			_, err := s.PutSecret(ctx, adminPrincipal(), PutSecretInput{Ref: tref("json"), Value: nil, ContentType: contentType})
			if !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
			if !strings.Contains(err.Error(), "an empty value is not valid JSON") {
				t.Fatalf("err = %q, want the empty-JSON explanation", err)
			}
		})
	}
	if _, ok := store.secrets[tref("json").String()]; ok {
		t.Fatal("a rejected empty JSON value was stored")
	}

	// A non-empty JSON value and an empty value under a JSON-adjacent type
	// are unaffected.
	putSecret(t, s, PutSecretInput{Ref: tref("json"), Value: []byte(`{}`), ContentType: "application/json"})
	putSecret(t, s, PutSecretInput{Ref: tref("jsonish"), Value: nil, ContentType: "application/jsonl"})
}

// TestEmptySecretBindingLifecycleOverSQL runs an empty secret through bound
// writes and every protection transition against the real store, whose
// transition path rejects incomplete cryptographic material. AES-GCM still
// emits a 16-byte tag for an empty plaintext, so every version has ciphertext.
func TestEmptySecretBindingLifecycleOverSQL(t *testing.T) {
	ctx := context.Background()
	svc, _ := newConsoleTestService(t)
	pr := adminPrincipal()
	if _, err := svc.CreateNamespace(ctx, pr, tns, "empty secrets", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatal(err)
	}

	read := func(t *testing.T, ref domain.Ref, version uint64, bindingKey string) {
		t.Helper()
		got, err := svc.GetSecret(ctx, pr, ref, version, "", bindingKey)
		if err != nil {
			t.Fatalf("GetSecret v%d: %v", version, err)
		}
		requireEmptyValue(t, "GetSecret", got.Value)
		revealed, err := svc.RevealSecret(ctx, pr, ref, version, "", bindingKey)
		if err != nil {
			t.Fatalf("RevealSecret v%d: %v", version, err)
		}
		requireEmptyValue(t, "RevealSecret", revealed.Value)
	}

	t.Run("bound put", func(t *testing.T) {
		ref := tref("empty-bound")
		if _, err := svc.PutSecret(ctx, pr, PutSecretInput{Ref: ref, Value: nil, ContentType: "text/plain", BindingKey: testBindingKeyA}); err != nil {
			t.Fatalf("PutSecret bound: %v", err)
		}
		read(t, ref, 1, testBindingKeyA)
		if _, err := svc.RevealSecret(ctx, pr, ref, 1, "", testBindingKeyB); !errors.Is(err, domain.ErrDecryptFailed) {
			t.Fatalf("wrong binding key err = %v, want ErrDecryptFailed", err)
		}
	})

	t.Run("bind rotate unbind", func(t *testing.T) {
		ref := tref("empty-transitions")
		if _, err := svc.PutSecret(ctx, pr, PutSecretInput{Ref: ref, Value: []byte{}}); err != nil {
			t.Fatalf("PutSecret: %v", err)
		}
		read(t, ref, 1, "")

		bound, err := svc.BindSecret(ctx, pr, ref, 1, testBindingKeyA)
		if err != nil {
			t.Fatalf("BindSecret: %v", err)
		}
		read(t, ref, bound.CurrentVersion, testBindingKeyA)

		rotated, err := svc.RotateSecretBindingKey(ctx, pr, ref, bound.CurrentVersion, testBindingKeyA, testBindingKeyB)
		if err != nil {
			t.Fatalf("RotateSecretBindingKey: %v", err)
		}
		read(t, ref, rotated.CurrentVersion, testBindingKeyB)

		unbound, err := svc.UnbindSecret(ctx, pr, ref, rotated.CurrentVersion, testBindingKeyB)
		if err != nil {
			t.Fatalf("UnbindSecret: %v", err)
		}
		if unbound.CurrentVersion != 4 {
			t.Fatalf("unbound version = %d, want 4", unbound.CurrentVersion)
		}
		read(t, ref, unbound.CurrentVersion, "")
	})
}
