package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// §25.2.1 — full secret put/get flow, including versioning and promotion.
func TestSecretPutGetFlow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const path = "/prod/payments/db-password"

	r1, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{Path: path, Value: []byte("v1-secret")})
	if err != nil {
		t.Fatalf("PutSecret v1: %v", err)
	}
	if r1.Version != 1 {
		t.Fatalf("first version = %d, want 1", r1.Version)
	}

	got, err := h.svc.GetSecret(ctx, h.admin, path, 0, "")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if string(got.Value) != "v1-secret" {
		t.Errorf("value = %q, want v1-secret", got.Value)
	}
	if got.Version != 1 {
		t.Errorf("version = %d, want 1", got.Version)
	}

	// Second version becomes current; previous label points at v1.
	r2, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{Path: path, Value: []byte("v2-secret")})
	if err != nil {
		t.Fatalf("PutSecret v2: %v", err)
	}
	if r2.Version != 2 {
		t.Fatalf("second version = %d, want 2", r2.Version)
	}

	cur, err := h.svc.GetSecret(ctx, h.admin, path, 0, domain.LabelCurrent)
	if err != nil || string(cur.Value) != "v2-secret" {
		t.Fatalf("current = %q err=%v, want v2-secret", cur.Value, err)
	}
	prev, err := h.svc.GetSecret(ctx, h.admin, path, 0, domain.LabelPrevious)
	if err != nil || string(prev.Value) != "v1-secret" {
		t.Fatalf("previous = %q err=%v, want v1-secret", prev.Value, err)
	}
	byVer, err := h.svc.GetSecret(ctx, h.admin, path, 1, "")
	if err != nil || string(byVer.Value) != "v1-secret" {
		t.Fatalf("v1 by number = %q err=%v", byVer.Value, err)
	}

	// Promote v1 back to current (rollback).
	cur2, prevVer, _, err := h.svc.PromoteSecretVersion(ctx, h.admin, path, 1)
	if err != nil {
		t.Fatalf("PromoteSecretVersion: %v", err)
	}
	if cur2 != 1 || prevVer != 2 {
		t.Errorf("promote result current=%d previous=%d, want 1/2", cur2, prevVer)
	}
	afterPromote, err := h.svc.GetSecret(ctx, h.admin, path, 0, "")
	if err != nil || string(afterPromote.Value) != "v1-secret" {
		t.Fatalf("after promote current = %q err=%v, want v1-secret", afterPromote.Value, err)
	}

	// Metadata/history view exposes two versions and never a value.
	info, err := h.svc.GetSecretInfo(ctx, h.admin, path)
	if err != nil {
		t.Fatalf("GetSecretInfo: %v", err)
	}
	if len(info.Versions) != 2 {
		t.Errorf("version count = %d, want 2", len(info.Versions))
	}

	secrets, _, err := h.svc.ListSecrets(ctx, h.admin, "/prod", storage.ListPage{})
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if len(secrets) != 1 || secrets[0].Path != path {
		t.Errorf("ListSecrets = %+v, want [%s]", secrets, path)
	}
}

// §25.2.2 — full parameter put/get flow, including labels and delete.
func TestParameterPutGetFlow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const path = "/prod/payments/rate-limit"

	v1, _, err := h.svc.PutParameter(ctx, h.admin, path, "100", "integer", "")
	if err != nil {
		t.Fatalf("PutParameter v1: %v", err)
	}
	if v1 != 1 {
		t.Fatalf("version = %d, want 1", v1)
	}

	got, err := h.svc.GetParameter(ctx, h.admin, path, 0, "")
	if err != nil || got.Value != "100" {
		t.Fatalf("GetParameter = %q err=%v, want 100", got.Value, err)
	}

	if _, _, err := h.svc.PutParameter(ctx, h.admin, path, "200", "integer", ""); err != nil {
		t.Fatalf("PutParameter v2: %v", err)
	}
	cur, err := h.svc.GetParameter(ctx, h.admin, path, 0, domain.LabelCurrent)
	if err != nil || cur.Value != "200" {
		t.Fatalf("current = %q err=%v, want 200", cur.Value, err)
	}
	prev, err := h.svc.GetParameter(ctx, h.admin, path, 0, domain.LabelPrevious)
	if err != nil || prev.Value != "100" {
		t.Fatalf("previous = %q err=%v, want 100", prev.Value, err)
	}

	// Type validation: a non-integer value for an integer parameter is rejected.
	if _, _, err := h.svc.PutParameter(ctx, h.admin, path, "not-a-number", "integer", ""); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("bad integer value err = %v, want ErrInvalidArgument", err)
	}

	params, _, err := h.svc.ListParameters(ctx, h.admin, "/prod", storage.ListPage{})
	if err != nil || len(params) != 1 {
		t.Fatalf("ListParameters = %+v err=%v, want 1", params, err)
	}

	if _, err := h.svc.DeleteParameter(ctx, h.admin, path); err != nil {
		t.Fatalf("DeleteParameter: %v", err)
	}
	if _, err := h.svc.GetParameter(ctx, h.admin, path, 0, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("after delete err = %v, want ErrNotFound", err)
	}
}

// §25.2.2 — parameter metadata/version history is exposed via GetParameterInfo.
func TestParameterVersionHistory(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const path = "/prod/app/history"

	for _, v := range []string{"1", "2", "3"} {
		if _, _, err := h.svc.PutParameter(ctx, h.admin, path, v, "integer", ""); err != nil {
			t.Fatalf("PutParameter %s: %v", v, err)
		}
	}
	info, err := h.svc.GetParameterInfo(ctx, h.admin, path)
	if err != nil {
		t.Fatalf("GetParameterInfo: %v", err)
	}
	if len(info.Versions) != 3 {
		t.Errorf("version count = %d, want 3", len(info.Versions))
	}
	if info.Labels[domain.LabelCurrent] != 3 || info.Labels[domain.LabelPrevious] != 2 {
		t.Errorf("labels = %v, want current=3 previous=2", info.Labels)
	}
	if info.ContentType != "integer" {
		t.Errorf("content type = %q, want integer", info.ContentType)
	}
}

// §25.2.3 — token authentication: correct token resolves; wrong/empty/disabled
// tokens fail with a generic unauthenticated error.
func TestTokenAuthentication(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	id, token, err := h.svc.CreateIdentity(ctx, h.admin, "billing", domain.IdentityKindClient)
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}

	authed, err := h.svc.Authenticate(ctx, token, "10.0.0.1", "ua")
	if err != nil {
		t.Fatalf("Authenticate(correct): %v", err)
	}
	if authed.Name != "billing" || authed.Kind != domain.IdentityKindClient {
		t.Errorf("authenticated identity = %+v", authed)
	}
	_ = id

	for _, tc := range []struct{ name, token string }{
		{"wrong", "kms_wrongtokenwrongtokenwrongtoken"},
		{"empty", ""},
		{"garbage", "not-a-real-token"},
	} {
		if _, err := h.svc.Authenticate(ctx, tc.token, "10.0.0.1", "ua"); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Errorf("Authenticate(%s) err = %v, want ErrUnauthenticated", tc.name, err)
		}
	}

	// A revoked identity can no longer authenticate with its (valid) token.
	if err := h.svc.RevokeIdentity(ctx, h.admin, "billing"); err != nil {
		t.Fatalf("RevokeIdentity: %v", err)
	}
	if _, err := h.svc.Authenticate(ctx, token, "10.0.0.1", "ua"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("Authenticate(revoked) err = %v, want ErrUnauthenticated", err)
	}
}
