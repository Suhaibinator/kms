package integration

import (
	"context"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

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
