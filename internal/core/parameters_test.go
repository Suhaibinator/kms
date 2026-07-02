package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func TestPutParameterValidation(t *testing.T) {
	ctx := context.Background()
	s := newTestService(newFakeStore())

	cases := map[string]struct {
		path, value, ct string
	}{
		"bad path":            {"no-slash", "1", "integer"},
		"unknown content":     {"/p", "1", "xml"},
		"integer not parsing": {"/p", "abc", "integer"},
		"float not parsing":   {"/p", "abc", "float"},
		"bool not parsing":    {"/p", "maybe", "boolean"},
		"json not valid":      {"/p", "{bad", "json"},
		"binary not base64":   {"/p", "!!!", "binary"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := s.PutParameter(ctx, adminPrincipal(), c.path, c.value, c.ct, "{}")
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
	v1, _, err := s.PutParameter(ctx, adminPrincipal(), "/p", "hello", "", "{}")
	if err != nil {
		t.Fatalf("PutParameter: %v", err)
	}
	if v1 != 1 {
		t.Fatalf("version = %d, want 1", v1)
	}
	v2, _, err := s.PutParameter(ctx, adminPrincipal(), "/p", "world", "string", "{}")
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
	s := newTestService(store)

	// Client without policy denied.
	_, _, err := s.PutParameter(ctx, clientPrincipal("app"), "/prod/x", "1", "integer", "{}")
	if !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}

	store.addPolicy(domain.Policy{Name: "w", Subject: "app",
		Allow: []domain.PolicyRule{{Operation: domain.OpParameterWrite, Path: "/prod/*"}}})
	if _, _, err := s.PutParameter(ctx, clientPrincipal("app"), "/prod/x", "1", "integer", "{}"); err != nil {
		t.Fatalf("authorized PutParameter: %v", err)
	}
}

func TestListParametersFiltersByPolicy(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	for _, p := range []string{"/prod/a", "/prod/b", "/staging/c"} {
		if _, _, err := store.PutParameter(ctx, p, "1", "integer", "{}", "root"); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}

	t.Run("admin sees all", func(t *testing.T) {
		got, _, err := s.ListParameters(ctx, adminPrincipal(), "", storage.ListPage{})
		if err != nil {
			t.Fatalf("ListParameters: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("admin saw %d, want 3", len(got))
		}
	})

	t.Run("client sees only permitted subtree", func(t *testing.T) {
		store.policies = nil
		store.addPolicy(domain.Policy{Name: "r", Subject: "app", Allow: []domain.PolicyRule{
			{Operation: domain.OpParameterList, Path: "/prod/*"},
			{Operation: domain.OpParameterRead, Path: "/prod/*"},
		}})
		got, _, err := s.ListParameters(ctx, clientPrincipal("app"), "", storage.ListPage{})
		if err != nil {
			t.Fatalf("ListParameters: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("client saw %d items, want 2", len(got))
		}
		for _, p := range got {
			if !strings.HasPrefix(p.Path, "/prod/") {
				t.Fatalf("client saw unpermitted path %q", p.Path)
			}
		}
	})

	t.Run("no list permission denied", func(t *testing.T) {
		store.policies = nil
		// read but not list.
		store.addPolicy(domain.Policy{Name: "r", Subject: "app", Allow: []domain.PolicyRule{
			{Operation: domain.OpParameterRead, Path: "/prod/*"},
		}})
		_, _, err := s.ListParameters(ctx, clientPrincipal("app"), "/prod", storage.ListPage{})
		if !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("err = %v, want ErrPermissionDenied", err)
		}
	})
}

func TestDeleteParameter(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	if _, _, err := store.PutParameter(ctx, "/p", "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Client without policy denied.
	if _, err := s.DeleteParameter(ctx, clientPrincipal("app"), "/p"); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
	// Admin deletes.
	if _, err := s.DeleteParameter(ctx, adminPrincipal(), "/p"); err != nil {
		t.Fatalf("DeleteParameter: %v", err)
	}
	// Deleting again is a not-found.
	if _, err := s.DeleteParameter(ctx, adminPrincipal(), "/p"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGetParameterInvalidPath(t *testing.T) {
	ctx := context.Background()
	s := newTestService(newFakeStore())
	if _, err := s.GetParameter(ctx, adminPrincipal(), "bad path", 0, ""); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}
