package integration

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// §25.2.6 / §25.3.3 — audit events are generated for meaningful operations and
// never contain secret plaintext.
func TestAuditGeneration(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const secretPlaintext = "audit-canary-plaintext-8f3a"

	// Generate a spread of audited activity.
	if _, err := h.svc.PutSecret(ctx, h.admin, putSecret("/prod/app/secret", secretPlaintext)); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	if _, err := h.svc.GetSecret(ctx, h.admin, "/prod/app/secret", 0, ""); err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	mustPutParam(t, h, "/prod/app/param", "v")

	// A denied client read (authz denial) and a failed auth both audit.
	nobody, _ := h.createClient("auditnobody")
	_, _ = h.svc.GetParameter(ctx, nobody, "/prod/app/param", 0, "")
	_, _ = h.svc.Authenticate(ctx, "kms_bogusbogusbogusbogusbogus", "9.9.9.9", "bad")

	events, _, err := h.svc.ListAuditEvents(ctx, h.admin, domain.AuditFilter{}, storage.ListPage{Limit: 1000})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}

	wantTypes := map[string]bool{
		"secret.write":    false,
		"secret.read":     false,
		"parameter.write": false,
		"authz.denial":    false,
		"auth.failure":    false,
	}
	for _, ev := range events {
		if _, ok := wantTypes[ev.EventType]; ok {
			wantTypes[ev.EventType] = true
		}
		// §25.3.3 — no audit field may carry the secret plaintext.
		blob := ev.EventType + "\x00" + ev.ResourcePath + "\x00" + ev.Metadata + "\x00" +
			ev.ActorIdentity + "\x00" + ev.Decision
		if strings.Contains(blob, secretPlaintext) {
			t.Errorf("audit event %q leaked secret plaintext: %+v", ev.EventType, ev)
		}
	}
	for typ, seen := range wantTypes {
		if !seen {
			t.Errorf("expected an audit event of type %q, none found", typ)
		}
	}
}

// §25.2.6 — each secret and parameter mutation emits its specific audit event
// type with an allow decision.
func TestAuditEventCoverage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const path = "/prod/app/covered"

	for i := 0; i < 3; i++ {
		if _, err := h.svc.PutSecret(ctx, h.admin, putSecret(path, "v")); err != nil {
			t.Fatalf("PutSecret: %v", err)
		}
	}
	if _, err := h.svc.RevealSecret(ctx, h.admin, path, 1, ""); err != nil {
		t.Fatalf("RevealSecret: %v", err)
	}
	if _, _, _, err := h.svc.PromoteSecretVersion(ctx, h.admin, path, 1); err != nil {
		t.Fatalf("PromoteSecretVersion: %v", err)
	}
	if _, err := h.svc.DisableSecret(ctx, h.admin, path, 2, false); err != nil {
		t.Fatalf("DisableSecret: %v", err)
	}
	if _, err := h.svc.DisableSecret(ctx, h.admin, path, 2, true); err != nil {
		t.Fatalf("EnableSecret: %v", err)
	}
	if _, err := h.svc.DestroySecretVersion(ctx, h.admin, path, 3); err != nil {
		t.Fatalf("DestroySecretVersion: %v", err)
	}

	const pparam = "/prod/app/covered-param"
	mustPutParam(t, h, pparam, "1")
	if _, err := h.svc.DeleteParameter(ctx, h.admin, pparam); err != nil {
		t.Fatalf("DeleteParameter: %v", err)
	}

	for _, et := range []string{
		"secret.write", "secret.reveal", "secret.promote",
		"secret.disable", "secret.enable", "secret.destroy",
		"parameter.write", "parameter.delete",
	} {
		if !hasAuditEvent(t, h, et, "allow") {
			t.Errorf("missing audited %q (allow) event", et)
		}
	}
}

// §25.3.3 (defense in depth) — scan every audit metadata blob on disk for the
// plaintext, independent of the API projection.
func TestAuditRowsHaveNoPlaintext(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const plaintext = "on-disk-audit-scan-canary-b7e1"

	if _, err := h.svc.PutSecret(ctx, h.admin, putSecret("/prod/x/secret", plaintext)); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	if _, err := h.svc.GetSecret(ctx, h.admin, "/prod/x/secret", 0, ""); err != nil {
		t.Fatalf("GetSecret: %v", err)
	}

	h.closeStore()
	h.withRawDB(func(db *sql.DB) {
		rows, err := db.QueryContext(ctx, "SELECT event_type, metadata_json, resource_path FROM audit_events")
		if err != nil {
			t.Fatalf("query audit rows: %v", err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var et, md, rp string
			if err := rows.Scan(&et, &md, &rp); err != nil {
				t.Fatalf("scan: %v", err)
			}
			n++
			if strings.Contains(et+md+rp, plaintext) {
				t.Errorf("audit row %q contains plaintext", et)
			}
		}
		if n == 0 {
			t.Fatal("expected audit rows on disk")
		}
	})
}
