package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

// destructiveCase is one removal exercised through DeleteWithAudit, paired with
// the probe that says whether the resource is still there.
type destructiveCase struct {
	name     string
	mutation DestructiveMutation
	event    string
	// revisioned marks the kinds that append a change-log entry and therefore
	// return a revision; the others always return 0.
	revisioned bool
	exists     func(t *testing.T, st *SQLStore) bool
}

func auditCount(t *testing.T, st *SQLStore) int {
	t.Helper()
	events, _, err := st.ListAudit(context.Background(), domain.AuditFilter{}, ListPage{Limit: 1000})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	return len(events)
}

func auditEventTypes(t *testing.T, st *SQLStore) map[string]int {
	t.Helper()
	events, _, err := st.ListAudit(context.Background(), domain.AuditFilter{}, ListPage{Limit: 1000})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	byType := map[string]int{}
	for _, ev := range events {
		byType[ev.EventType]++
	}
	return byType
}

func currentRevision(t *testing.T, st *SQLStore) uint64 {
	t.Helper()
	rev, err := st.CurrentRevision(context.Background())
	if err != nil {
		t.Fatalf("CurrentRevision: %v", err)
	}
	return rev
}

func destructiveAudit(eventType string) domain.AuditEvent {
	return domain.AuditEvent{
		EventType:     eventType,
		ActorIdentity: "admin",
		ActorType:     domain.IdentityKindAdmin,
		Decision:      "allow",
		Metadata:      "{}",
	}
}

// TestDeleteWithAuditRollsBackOnAuditFailure pins the fail-closed contract for
// every destructive kind: with the audit table refusing inserts nothing is
// removed, and once it accepts them the removal and its audit row commit
// together.
func TestDeleteWithAuditRollsBackOnAuditFailure(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	seedNS(t, st, "dev", "app")

	paramRef := ref("prod", "app", "p1")
	if _, _, err := st.PutParameter(ctx, paramRef, "1", "integer", "{}", "admin"); err != nil {
		t.Fatalf("PutParameter: %v", err)
	}
	deletedSecret := ref("prod", "app", "s1")
	destroyedSecret := ref("prod", "app", "s2")
	for _, r := range []domain.Ref{deletedSecret, destroyedSecret} {
		if _, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
			Ref: r, ContentType: "text/plain", CreatedBy: "admin", Encrypt: encryptStub(nil),
		}); err != nil {
			t.Fatalf("CreateSecretVersion(%s): %v", r, err)
		}
	}
	if _, err := st.CreatePolicy(ctx, domain.Policy{Name: "pol", Subject: "app"}); err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	if _, err := st.CreateApplication(ctx, domain.Application{Name: "lonely", ReleaseName: "runtime", CreatedBy: "admin"}); err != nil {
		t.Fatalf("CreateApplication: %v", err)
	}

	cases := []destructiveCase{
		{
			name:       "parameter",
			mutation:   DestructiveMutation{Kind: DestructiveParameter, Ref: paramRef},
			event:      "parameter.delete",
			revisioned: true,
			exists: func(t *testing.T, st *SQLStore) bool {
				_, err := st.GetParameterInfo(ctx, paramRef)
				return existsFromErr(t, err)
			},
		},
		{
			name:       "secret",
			mutation:   DestructiveMutation{Kind: DestructiveSecret, Ref: deletedSecret},
			event:      "secret.delete",
			revisioned: true,
			exists: func(t *testing.T, st *SQLStore) bool {
				_, err := st.GetSecretRecord(ctx, deletedSecret)
				return existsFromErr(t, err)
			},
		},
		{
			name:       "secret version",
			mutation:   DestructiveMutation{Kind: DestructiveSecretVersion, Ref: destroyedSecret, Version: 1},
			event:      "secret.destroy",
			revisioned: true,
			exists: func(t *testing.T, st *SQLStore) bool {
				_, ver, err := st.GetSecretVersion(ctx, destroyedSecret, 1, "")
				if err != nil {
					t.Fatalf("GetSecretVersion: %v", err)
				}
				// "Still there" for a version means not yet destroyed.
				return ver.State != domain.StateDestroyed
			},
		},
		{
			name:     "namespace",
			mutation: DestructiveMutation{Kind: DestructiveNamespace, Ref: domain.Ref{NS: nsRef("dev", "app")}},
			event:    "namespace.delete",
			exists: func(t *testing.T, st *SQLStore) bool {
				_, err := st.GetNamespace(ctx, nsRef("dev", "app"))
				return existsFromErr(t, err)
			},
		},
		{
			name:     "policy",
			mutation: DestructiveMutation{Kind: DestructivePolicy, Name: "pol"},
			event:    "policy.write",
			exists: func(t *testing.T, st *SQLStore) bool {
				policies, _, err := st.ListPolicies(ctx, ListPage{Limit: 100})
				if err != nil {
					t.Fatalf("ListPolicies: %v", err)
				}
				for _, p := range policies {
					if p.Name == "pol" {
						return true
					}
				}
				return false
			},
		},
		{
			name:     "application",
			mutation: DestructiveMutation{Kind: DestructiveApplication, Name: "lonely"},
			event:    "application.delete",
			exists: func(t *testing.T, st *SQLStore) bool {
				_, err := st.GetApplication(ctx, "lonely")
				return existsFromErr(t, err)
			},
		},
	}

	if got := auditCount(t, st); got != 0 {
		t.Fatalf("seeded audit rows = %d, want 0", got)
	}
	revisionBefore := currentRevision(t, st)

	if err := st.db.Exec(`CREATE TRIGGER reject_destructive_audit BEFORE INSERT ON audit_events
		BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`).Error; err != nil {
		t.Fatalf("create audit trigger: %v", err)
	}
	for _, tc := range cases {
		t.Run(tc.name+" rolls back", func(t *testing.T) {
			rev, err := st.DeleteWithAudit(ctx, tc.mutation, destructiveAudit(tc.event))
			if !errors.Is(err, ErrRequiredAuditUnavailable) {
				t.Fatalf("err = %v, want ErrRequiredAuditUnavailable", err)
			}
			if rev != 0 {
				t.Errorf("revision = %d on a rolled-back mutation, want 0", rev)
			}
			if !tc.exists(t, st) {
				t.Error("resource was removed even though its audit row could not be written")
			}
			if got := currentRevision(t, st); got != revisionBefore {
				t.Errorf("revision = %d, want %d (unchanged)", got, revisionBefore)
			}
			if got := auditCount(t, st); got != 0 {
				t.Errorf("audit rows = %d, want 0", got)
			}
		})
	}

	if err := st.db.Exec(`DROP TRIGGER reject_destructive_audit`).Error; err != nil {
		t.Fatalf("drop audit trigger: %v", err)
	}
	for i, tc := range cases {
		t.Run(tc.name+" commits", func(t *testing.T) {
			rev, err := st.DeleteWithAudit(ctx, tc.mutation, destructiveAudit(tc.event))
			if err != nil {
				t.Fatalf("DeleteWithAudit: %v", err)
			}
			if tc.revisioned {
				if got := currentRevision(t, st); rev != got {
					t.Errorf("revision = %d, want the current revision %d", rev, got)
				}
			} else if rev != 0 {
				t.Errorf("revision = %d for a kind with no change-log entry, want 0", rev)
			}
			if tc.exists(t, st) {
				t.Error("resource survived a committed removal")
			}
			if got := auditEventTypes(t, st)[tc.event]; got != 1 {
				t.Errorf("%s audit rows = %d, want 1", tc.event, got)
			}
			if got := auditCount(t, st); got != i+1 {
				t.Errorf("total audit rows = %d, want %d", got, i+1)
			}
		})
	}

	// A mutation that refuses on its own terms writes no audit row: only the
	// removals that actually happened are recorded.
	before := auditCount(t, st)
	if _, err := st.DeleteWithAudit(ctx, DestructiveMutation{Kind: DestructiveParameter, Ref: paramRef},
		destructiveAudit("parameter.delete")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if got := auditCount(t, st); got != before {
		t.Errorf("audit rows = %d after a not-found mutation, want %d", got, before)
	}
}

// existsFromErr reads a getter's error as presence/absence, failing the test on
// anything that is not a clean hit or a clean not-found.
func existsFromErr(t *testing.T, err error) bool {
	t.Helper()
	switch {
	case err == nil:
		return true
	case errors.Is(err, domain.ErrNotFound):
		return false
	default:
		t.Fatalf("unexpected lookup error: %v", err)
		return false
	}
}
