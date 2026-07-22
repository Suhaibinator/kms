package core

import (
	"context"
	"encoding/json"
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

func TestPutParameterJSONRetainsDuplicatePropertyCompatibility(t *testing.T) {
	ctx := context.Background()
	s := newTestService(newFakeStore())

	for name, value := range map[string]string{
		"root":   `{"enabled":true,"enabled":false}`,
		"nested": `{"outer":{"limit":1,"limit":2}}`,
	} {
		t.Run(name, func(t *testing.T) {
			version, _, err := s.PutParameter(ctx, adminPrincipal(), tref("duplicate-"+name), value, "json", "{}")
			if err != nil {
				t.Fatalf("PutParameter rejected legacy-compatible duplicate JSON properties: %v", err)
			}
			if version != 1 {
				t.Fatalf("version = %d, want 1", version)
			}
		})
	}
}

func TestParseJSONParameterRetainsLegacySyntaxAcceptance(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "object", raw: `{"enabled":true}`},
		{name: "duplicate root", raw: `{"enabled":true,"enabled":false}`},
		{name: "duplicate nested", raw: `{"outer":{"limit":1,"limit":2}}`},
		{name: "large exact integer", raw: `9007199254740993`},
		{name: "null", raw: `null`},
		{name: "boolean", raw: `true`},
		{name: "string", raw: `"value"`},
		{name: "surrounding whitespace", raw: " \n\t[1,2,3]\r "},
		{name: "empty", raw: ``},
		{name: "multiple values", raw: `{} {}`},
		{name: "trailing junk", raw: `{}x`},
		{name: "incomplete", raw: `{"enabled":`},
		{name: "positive float overflow", raw: `1e400`},
		{name: "negative float overflow", raw: `-1e400`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var legacy any
			legacyErr := json.Unmarshal([]byte(tc.raw), &legacy)
			_, currentErr := parseParameterValue(tc.raw, "json")
			if (currentErr == nil) != (legacyErr == nil) {
				t.Fatalf("acceptance differs from legacy json.Unmarshal: current error = %v, legacy error = %v", currentErr, legacyErr)
			}
		})
	}
}

func TestManagedParameterSchemaValueRejectsDuplicateJSONProperties(t *testing.T) {
	for name, value := range map[string]string{
		"root":                `{"enabled":true,"enabled":false}`,
		"nested":              `{"outer":{"limit":1,"limit":2}}`,
		"object inside array": `[{"name":"first","name":"second"}]`,
		"escaped equivalent":  `{"name":1,"\u006eame":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parameterSchemaValue(value, "json"); err == nil {
				t.Fatal("managed parameter schema value accepted duplicate JSON properties")
			}
		})
	}

	if _, err := parameterSchemaValue(`{"left":{"name":"first"},"right":{"name":"second"}}`, "json"); err != nil {
		t.Fatalf("distinct objects reusing a property name were rejected: %v", err)
	}
}

func TestManagedParameterSchemaValuePreservesExactNumbers(t *testing.T) {
	parsed, err := parameterSchemaValue(`{"large":9007199254740993}`, "json")
	if err != nil {
		t.Fatal(err)
	}
	value, ok := parsed.(map[string]any)["large"].(json.Number)
	if !ok || value.String() != "9007199254740993" {
		t.Fatalf("large number = %#v, want exact json.Number", parsed)
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

	t.Run("explicit deny overrides home list grant", func(t *testing.T) {
		store.policies = nil
		store.addPolicy(domain.Policy{Name: "deny-list", Subject: "app", Deny: []domain.PolicyRule{
			{Operation: domain.OpParameterList, Env: "prod", App: "app"},
		}})
		_, _, err := s.ListParameters(ctx, boundClientPrincipal("app", tns), tns, "", storage.ListPage{})
		if !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("explicit list deny err = %v, want ErrPermissionDenied", err)
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
