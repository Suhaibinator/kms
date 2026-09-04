package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// §25.2.4 — a token-protected but unbound secret: GetSecret requires
// the token even for an admin, while the audited RevealSecret break-glass path
// bypasses it (because the server holds the key material for standard wrapping).
func TestTokenProtectedSecret(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const path = "/prod/app/token-protected"
	const plaintext = "token-protected-value"
	ref := h.ensureNS(path)

	res, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{
		Ref: ref, Value: []byte(plaintext), GenerateToken: true,
	})
	if err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	if res.AccessToken == "" {
		t.Fatal("expected a minted access token")
	}

	// Admin GetSecret without the token is denied.
	if _, err := h.svc.GetSecret(ctx, h.admin, ref, 0, "", "", ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("admin GetSecret without token err = %v, want ErrPermissionDenied", err)
	}
	// A wrong token is denied with the same generic error.
	if _, err := h.svc.GetSecret(ctx, h.admin, ref, 0, "", "kmss_wrongwrongwrongwrongwrongwrong", ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Errorf("GetSecret wrong token err = %v, want ErrPermissionDenied", err)
	}
	// The correct token succeeds.
	got, err := h.svc.GetSecret(ctx, h.admin, ref, 0, "", res.AccessToken, "")
	if err != nil || string(got.Value) != plaintext {
		t.Fatalf("GetSecret with token = %q err=%v, want %q", got.Value, err, plaintext)
	}

	// RevealSecret (admin break-glass) bypasses the token gate for a standard
	// secret and is audited.
	revealed, err := h.svc.RevealSecret(ctx, h.admin, ref, 0, "", "", "")
	if err != nil {
		t.Fatalf("RevealSecret: %v", err)
	}
	if string(revealed.Value) != plaintext {
		t.Errorf("RevealSecret value = %q, want %q", revealed.Value, plaintext)
	}
	if !hasAuditEvent(t, h, "secret.reveal", "allow") {
		t.Error("expected an audited secret.reveal event")
	}
}

// hasAuditEvent reports whether an audit event of the given type and decision
// exists.
func hasAuditEvent(t *testing.T, h *harness, eventType, decision string) bool {
	t.Helper()
	events, _, err := h.svc.ListAuditEvents(context.Background(), h.admin, domain.AuditFilter{}, storage.ListPage{Limit: 1000})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	for _, ev := range events {
		if ev.EventType == eventType && ev.Decision == decision {
			return true
		}
	}
	return false
}
