package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// §25.2.5 / §25.3.4 — authorization denials, deny precedence, and list
// filtering, driven through real policies and identities.
func TestAuthorizationDenials(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Admin seeds data across two namespaces.
	mustPutParam(t, h, "/prod/app/rate", "10")
	mustPutParam(t, h, "/prod/app/pool", "20")
	mustPutParam(t, h, "/staging/app/rate", "5")
	if _, err := h.svc.PutSecret(ctx, h.admin, h.stdSecret("/prod/app/token", "s3cr3t")); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	// A client with no policy is denied everything.
	nobody, _ := h.createClient("nobody")
	if _, err := h.svc.GetParameter(ctx, nobody, h.ref("/prod/app/rate"), 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("unpolicied param read err = %v, want ErrPermissionDenied", err)
	}
	if _, err := h.svc.GetSecret(ctx, nobody, h.ref("/prod/app/token"), 0, "", "", ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("unpolicied secret read err = %v, want ErrPermissionDenied", err)
	}

	// A scoped client may read within its namespace only.
	app, _ := h.createClient("app")
	h.grant("app-read", "app", []domain.PolicyRule{
		allowRule(domain.OpParameterRead, "prod", "app"),
		allowRule(domain.OpParameterList, "prod", "app"),
		allowRule(domain.OpSecretRead, "prod", "app"),
	}, nil)

	if got, err := h.svc.GetParameter(ctx, app, h.ref("/prod/app/rate"), 0, ""); err != nil || got.Value != "10" {
		t.Errorf("scoped read = %q err=%v, want 10", got.Value, err)
	}
	if _, err := h.svc.GetParameter(ctx, app, h.ref("/staging/app/rate"), 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("cross-namespace read err = %v, want ErrPermissionDenied", err)
	}
	if got, err := h.svc.GetSecret(ctx, app, h.ref("/prod/app/token"), 0, "", "", ""); err != nil || string(got.Value) != "s3cr3t" {
		t.Errorf("scoped secret read = %q err=%v, want s3cr3t", got.Value, err)
	}

	// §25.2.5 list filtering: only readable items come back.
	params, _, err := h.svc.ListParameters(ctx, app, nsRef("prod", "app"), "", storage.ListPage{})
	if err != nil {
		t.Fatalf("ListParameters: %v", err)
	}
	if len(params) != 2 {
		t.Errorf("list returned %d items, want 2 (the readable namespace)", len(params))
	}
	// Listing a namespace the client has no allow rule for is denied outright.
	if _, _, err := h.svc.ListParameters(ctx, app, nsRef("staging", "app"), "", storage.ListPage{}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("list of un-granted namespace err = %v, want ErrPermissionDenied", err)
	}
}

// §25.1.2 / §16.3 — deny rules take precedence over allow rules. Authorization
// is namespace-level: a broad allow across an env is carved back by a deny on a
// single namespace.
func TestDenyPrecedence(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	mustPutParam(t, h, "/prod/open/setting", "ok")
	mustPutParam(t, h, "/prod/locked/setting", "no")

	client, _ := h.createClient("mixed")
	h.grant("mixed-policy", "mixed",
		[]domain.PolicyRule{allowRule(domain.OpParameterRead, "prod", "*")},
		[]domain.PolicyRule{allowRule(domain.OpParameterRead, "prod", "locked")},
	)

	if got, err := h.svc.GetParameter(ctx, client, h.ref("/prod/open/setting"), 0, ""); err != nil || got.Value != "ok" {
		t.Errorf("allowed namespace = %q err=%v, want ok", got.Value, err)
	}
	if _, err := h.svc.GetParameter(ctx, client, h.ref("/prod/locked/setting"), 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("denied namespace err = %v, want ErrPermissionDenied (deny precedence)", err)
	}
}
