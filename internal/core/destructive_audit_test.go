package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// auditUnavailableMessage is the fixed response a destructive mutation gets
// when its mandatory audit row cannot be written. The underlying database
// error is never disclosed.
const auditUnavailableMessage = "audit unavailable: failed precondition"

// TestDestructiveMutationsFailClosedWithoutAudit pins the fail-closed contract
// at the service layer: with audit storage refusing writes, no destructive
// mutation commits, nothing is revised, and the watch hub is never woken; once
// audit storage recovers the same call succeeds and leaves its audit row.
func TestDestructiveMutationsFailClosedWithoutAudit(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	s, hub := newTestServiceWithHub(t, store)
	admin := adminPrincipal()

	emptyNS := mkns("dev", "app")
	store.addNamespace(emptyNS, domain.AuthMethodToken)
	store.addPolicy(domain.Policy{Name: "pol", Subject: "app"})
	if _, _, err := store.PutParameter(ctx, tref("p"), "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed parameter: %v", err)
	}
	putSecret(t, s, PutSecretInput{Ref: tref("s-del"), Value: []byte("v"), ContentType: "text/plain"})
	putSecret(t, s, PutSecretInput{Ref: tref("s-destroy"), Value: []byte("v"), ContentType: "text/plain"})

	cases := []struct {
		name string
		run  func() error
		// present reports whether the resource is still there.
		present func() bool
		event   string
		// metadata, when set, must appear in the committed audit row.
		metadata string
	}{
		{
			name:    "parameter",
			run:     func() error { _, err := s.DeleteParameter(ctx, admin, tref("p")); return err },
			present: func() bool { return store.params[tref("p").String()] != nil },
			event:   "parameter.delete",
		},
		{
			name:    "secret",
			run:     func() error { _, err := s.DeleteSecret(ctx, admin, tref("s-del")); return err },
			present: func() bool { return store.secrets[tref("s-del").String()] != nil },
			event:   "secret.delete",
		},
		{
			name: "secret version",
			run:  func() error { _, err := s.DestroySecretVersion(ctx, admin, tref("s-destroy"), 1); return err },
			present: func() bool {
				sec := store.secrets[tref("s-destroy").String()]
				return sec != nil && sec.versions[1].State != domain.StateDestroyed
			},
			event: "secret.destroy",
		},
		{
			name: "namespace",
			run:  func() error { return s.DeleteNamespace(ctx, admin, emptyNS) },
			present: func() bool {
				_, ok := store.namespaces[emptyNS.String()]
				return ok
			},
			event: "namespace.delete",
		},
		{
			name: "policy",
			run:  func() error { return s.DeletePolicy(ctx, admin, "pol") },
			present: func() bool {
				for _, p := range store.policies {
					if p.Name == "pol" {
						return true
					}
				}
				return false
			},
			event:    "policy.write",
			metadata: `"action":"delete"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			audits, revision, wakes := len(store.audits), store.revision, hub.wakes

			store.auditErr = errors.New("audit unavailable")
			err := tc.run()
			if !errors.Is(err, domain.ErrFailedPrecondition) {
				t.Fatalf("err = %v, want ErrFailedPrecondition", err)
			}
			if err.Error() != auditUnavailableMessage {
				t.Errorf("err = %q, want %q", err.Error(), auditUnavailableMessage)
			}
			if !tc.present() {
				t.Error("resource was removed with no durable audit row")
			}
			if store.revision != revision {
				t.Errorf("revision = %d, want %d (unchanged)", store.revision, revision)
			}
			if len(store.audits) != audits {
				t.Errorf("audit rows = %d, want %d (unchanged)", len(store.audits), audits)
			}
			if hub.wakes != wakes {
				t.Errorf("wakes = %d, want %d: a rolled-back mutation must not wake watchers", hub.wakes, wakes)
			}

			store.auditErr = nil
			if err := tc.run(); err != nil {
				t.Fatalf("after audit storage recovered: %v", err)
			}
			if tc.present() {
				t.Error("resource survived a committed removal")
			}
			if !hasAudit(store, tc.event, tc.metadata) {
				t.Errorf("no %s audit row with metadata %q", tc.event, tc.metadata)
			}
		})
	}
}

func hasAudit(store *fakeStore, eventType, metadata string) bool {
	for _, ev := range store.audits {
		if ev.EventType == eventType && strings.Contains(ev.Metadata, metadata) {
			return true
		}
	}
	return false
}

// auditlessApplicationStore refuses the transactional audit insert for every
// destructive mutation while leaving the rest of the real store intact.
type auditlessApplicationStore struct {
	storage.Store
	storage.ApplicationStore
}

func (auditlessApplicationStore) DeleteWithAudit(context.Context, storage.DestructiveMutation, domain.AuditEvent) (uint64, error) {
	return 0, storage.ErrRequiredAuditUnavailable
}

// TestDeleteApplicationFailsClosedWithoutAudit covers the application kind,
// which the core fake store does not implement, against the real SQL store.
func TestDeleteApplicationFailsClosedWithoutAudit(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.CreateApplication(ctx, domain.Application{Name: "lonely", ReleaseName: "runtime", CreatedBy: "admin"}); err != nil {
		t.Fatalf("CreateApplication: %v", err)
	}
	admin := adminPrincipal()

	blocked := New(auditlessApplicationStore{Store: st, ApplicationStore: st}, nil, "test")
	err = blocked.DeleteApplication(ctx, admin, "lonely")
	if !errors.Is(err, domain.ErrFailedPrecondition) || err.Error() != auditUnavailableMessage {
		t.Fatalf("err = %v, want %q", err, auditUnavailableMessage)
	}
	if _, err := st.GetApplication(ctx, "lonely"); err != nil {
		t.Fatalf("application was removed with no durable audit row: %v", err)
	}

	svc := New(st, nil, "test")
	if err := svc.DeleteApplication(ctx, admin, "lonely"); err != nil {
		t.Fatalf("DeleteApplication: %v", err)
	}
	if _, err := st.GetApplication(ctx, "lonely"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("application survived a committed removal: %v", err)
	}
	events, _, err := st.ListAudit(ctx, domain.AuditFilter{}, storage.ListPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	deletes := 0
	for _, ev := range events {
		if ev.EventType == "application.delete" {
			deletes++
		}
	}
	if deletes != 1 {
		t.Fatalf("application.delete audit rows = %d, want 1", deletes)
	}
}
