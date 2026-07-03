package core

import (
	"context"
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func TestPutParameterValidation(t *testing.T) {
	ctx := context.Background()
	s := newTestService(newFakeStore())

	cases := map[string]struct {
		ref       domain.Ref
		value, ct string
	}{
		"bad env":             {domain.Ref{NS: mkns("PROD", "app"), Key: "p"}, "1", "integer"},
		"bad key":             {domain.Ref{NS: tns, Key: "/leading"}, "1", "integer"},
		"unknown content":     {tref("p"), "1", "xml"},
		"integer not parsing": {tref("p"), "abc", "integer"},
		"float not parsing":   {tref("p"), "abc", "float"},
		"bool not parsing":    {tref("p"), "maybe", "boolean"},
		"json not valid":      {tref("p"), "{bad", "json"},
		"binary not base64":   {tref("p"), "!!!", "binary"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := s.PutParameter(ctx, adminPrincipal(), c.ref, c.value, c.ct, "{}")
			if !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestPutParameterDefaultsAndVersions(t *testing.T) {
	ctx := context.Background()
	s := newTestService(newFakeStore())

	// Empty content type defaults to "string" and any value is accepted.
	v1, _, err := s.PutParameter(ctx, adminPrincipal(), tref("p"), "hello", "", "{}")
	if err != nil {
		t.Fatalf("PutParameter: %v", err)
	}
	if v1 != 1 {
		t.Fatalf("version = %d, want 1", v1)
	}
	v2, _, err := s.PutParameter(ctx, adminPrincipal(), tref("p"), "world", "string", "{}")
	if err != nil {
		t.Fatalf("PutParameter v2: %v", err)
	}
	if v2 != 2 {
		t.Fatalf("version = %d, want 2", v2)
	}
}

func TestPutParameterAuthorization(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	s := newTestService(store)

	// Client without policy denied.
	_, _, err := s.PutParameter(ctx, clientPrincipal("app"), tref("x"), "1", "integer", "{}")
	if !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}

	store.addPolicy(domain.Policy{Name: "w", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: domain.OpParameterWrite, Env: "prod", App: "app"}}})
	if _, _, err := s.PutParameter(ctx, clientPrincipal("app"), tref("x"), "1", "integer", "{}"); err != nil {
		t.Fatalf("authorized PutParameter: %v", err)
	}
}

func TestListParametersFiltersByPolicy(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	s := newTestService(store)
	for _, k := range []string{"a", "b", "billing/c"} {
		if _, _, err := store.PutParameter(ctx, tref(k), "1", "integer", "{}", "root"); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}

	t.Run("admin sees all", func(t *testing.T) {
		got, _, err := s.ListParameters(ctx, adminPrincipal(), tns, "", storage.ListPage{})
		if err != nil {
			t.Fatalf("ListParameters: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("admin saw %d, want 3", len(got))
		}
	})

	t.Run("client granted the namespace sees every key", func(t *testing.T) {
		// Authorization is namespace-level: a client granted list+read on the
		// namespace sees all of its keys, not a key subtree.
		store.policies = nil
		store.addPolicy(domain.Policy{Name: "r", Subject: "app", Allow: []domain.PolicyRule{
			{Operation: domain.OpParameterList, Env: "prod", App: "app"},
			{Operation: domain.OpParameterRead, Env: "prod", App: "app"},
		}})
		got, _, err := s.ListParameters(ctx, clientPrincipal("app"), tns, "", storage.ListPage{})
		if err != nil {
			t.Fatalf("ListParameters: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("client saw %d keys, want 3 (whole namespace)", len(got))
		}
	})

	t.Run("no list permission denied", func(t *testing.T) {
		store.policies = nil
		// read but not list.
		store.addPolicy(domain.Policy{Name: "r", Subject: "app", Allow: []domain.PolicyRule{
			{Operation: domain.OpParameterRead, Env: "prod", App: "app"},
		}})
		_, _, err := s.ListParameters(ctx, clientPrincipal("app"), tns, "", storage.ListPage{})
		if !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("err = %v, want ErrPermissionDenied", err)
		}
	})

	t.Run("home namespace list via implicit grant", func(t *testing.T) {
		store.policies = nil
		home := boundClientPrincipal("app", tns)
		got, _, err := s.ListParameters(ctx, home, tns, "", storage.ListPage{})
		if err != nil {
			t.Fatalf("home ListParameters: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("home client saw %d, want 3", len(got))
		}
	})
}

func TestDeleteParameter(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	s := newTestService(store)
	if _, _, err := store.PutParameter(ctx, tref("p"), "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Client without policy denied.
	if _, err := s.DeleteParameter(ctx, clientPrincipal("app"), tref("p")); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
	// Admin deletes.
	if _, err := s.DeleteParameter(ctx, adminPrincipal(), tref("p")); err != nil {
		t.Fatalf("DeleteParameter: %v", err)
	}
	// Deleting again is a not-found.
	if _, err := s.DeleteParameter(ctx, adminPrincipal(), tref("p")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGetParameterInvalidRef(t *testing.T) {
	ctx := context.Background()
	s := newTestService(newFakeStore())
	if _, err := s.GetParameter(ctx, adminPrincipal(), domain.Ref{NS: mkns("prod", "app"), Key: "bad key"}, 0, ""); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}
