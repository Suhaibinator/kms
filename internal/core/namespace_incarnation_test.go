package core

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	kmscrypto "github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// namespaceSwapStore reproduces a cross-process ABA deterministically: it
// returns namespace incarnation A to core authorization, but replaces the row
// with incarnation B before the authorized storage operation begins.
type namespaceSwapStore struct {
	storage.Store
	storage.ReleaseStore
	target             domain.NamespaceRef
	afterNamespaceRead func() error
	afterDelete        func() error
	once               sync.Once
	hookErr            error
}

func (s *namespaceSwapStore) GetNamespace(ctx context.Context, ref domain.NamespaceRef) (domain.Namespace, error) {
	ns, err := s.Store.GetNamespace(ctx, ref)
	if err == nil && ref == s.target && s.afterNamespaceRead != nil {
		s.once.Do(func() { s.hookErr = s.afterNamespaceRead() })
		if s.hookErr != nil {
			return domain.Namespace{}, s.hookErr
		}
	}
	return ns, err
}

func (s *namespaceSwapStore) DeleteNamespace(ctx context.Context, ref domain.NamespaceRef) error {
	if err := s.Store.DeleteNamespace(ctx, ref); err != nil {
		return err
	}
	if s.afterDelete != nil {
		return s.afterDelete()
	}
	return nil
}

func newNamespaceIncarnationStore(t *testing.T) (*storage.SQLStore, domain.NamespaceRef, domain.Namespace) {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	nsRef := domain.NamespaceRef{Env: "prod", App: "app"}
	ns, err := st.CreateNamespace(context.Background(), domain.Namespace{
		NamespaceRef:       nsRef,
		AllowedAuthMethods: []domain.AuthMethod{domain.AuthMethodToken},
		CreatedBy:          "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, nsRef, ns
}

func replaceWithMTLSNamespace(st *storage.SQLStore, ns domain.NamespaceRef) error {
	ctx := context.Background()
	if err := st.DeleteNamespace(ctx, ns); err != nil {
		return err
	}
	_, err := st.CreateNamespace(ctx, domain.Namespace{
		NamespaceRef:       ns,
		AllowedAuthMethods: []domain.AuthMethod{domain.AuthMethodMTLS},
		CreatedBy:          "racing-admin",
	})
	return err
}

func testSQLKeyring(t *testing.T, st *storage.SQLStore) *kmscrypto.Keyring {
	t.Helper()
	kek, err := kmscrypto.NewKEKFromMaterial("kek-namespace-aba", bytes.Repeat([]byte{0x51}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertKeyMetadata(context.Background(), domain.KeyMetadata{
		ID: "kek-namespace-aba", Source: domain.KeySourceFile, State: domain.KeyStateActive, KeyCheck: []byte("test"),
	}); err != nil {
		t.Fatal(err)
	}
	return kmscrypto.NewKeyring(kek)
}

func TestNamespaceIncarnationBindingRejectsAuthorizationABA(t *testing.T) {
	t.Run("parameter read cannot cross into recreated namespace", func(t *testing.T) {
		st, ns, _ := newNamespaceIncarnationStore(t)
		ref := domain.Ref{NS: ns, Key: "private"}
		wrapped := &namespaceSwapStore{Store: st, ReleaseStore: st, target: ns}
		wrapped.afterNamespaceRead = func() error {
			if err := replaceWithMTLSNamespace(st, ns); err != nil {
				return err
			}
			_, _, err := st.PutParameter(context.Background(), ref, "incarnation-b", "string", "{}", "racing-admin")
			return err
		}

		svc := New(wrapped, nil, "test")
		_, err := svc.GetParameter(context.Background(), boundClientPrincipal("client", ns), ref, 0, "")
		if !errors.Is(err, domain.ErrAborted) {
			t.Fatalf("GetParameter after namespace ABA err = %v, want ErrAborted", err)
		}
		got, err := st.GetParameter(context.Background(), ref, 0, "")
		if err != nil || got.Value != "incarnation-b" {
			t.Fatalf("unbound legitimate read = %+v, err %v", got, err)
		}
	})

	t.Run("parameter write cannot cross into recreated namespace", func(t *testing.T) {
		st, ns, _ := newNamespaceIncarnationStore(t)
		ref := domain.Ref{NS: ns, Key: "new"}
		if _, err := st.CreatePolicy(context.Background(), domain.Policy{Name: "parameter-writer", Subject: "client", Allow: []domain.PolicyRule{{Operation: domain.OpParameterWrite, Env: ns.Env, App: ns.App}}}); err != nil {
			t.Fatal(err)
		}
		wrapped := &namespaceSwapStore{Store: st, ReleaseStore: st, target: ns}
		wrapped.afterNamespaceRead = func() error { return replaceWithMTLSNamespace(st, ns) }

		svc := New(wrapped, nil, "test")
		_, _, err := svc.PutParameter(context.Background(), boundClientPrincipal("client", ns), ref, "crossed", "string", "{}")
		if !errors.Is(err, domain.ErrAborted) {
			t.Fatalf("PutParameter after namespace ABA err = %v, want ErrAborted", err)
		}
		if _, err := st.GetParameter(context.Background(), ref, 0, ""); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("recreated namespace parameter err = %v, want ErrNotFound", err)
		}
	})

	t.Run("secret read cannot cross into recreated namespace", func(t *testing.T) {
		st, ns, _ := newNamespaceIncarnationStore(t)
		ref := domain.Ref{NS: ns, Key: "private"}
		ring := testSQLKeyring(t, st)
		seed := New(st, nil, "test")
		seed.SetKeyring(ring)
		wrapped := &namespaceSwapStore{Store: st, ReleaseStore: st, target: ns}
		wrapped.afterNamespaceRead = func() error {
			if err := replaceWithMTLSNamespace(st, ns); err != nil {
				return err
			}
			_, err := seed.PutSecret(context.Background(), adminPrincipal(), PutSecretInput{Ref: ref, Value: []byte("incarnation-b"), ContentType: "text/plain"})
			return err
		}

		svc := New(wrapped, nil, "test")
		svc.SetKeyring(ring)
		_, err := svc.GetSecret(context.Background(), boundClientPrincipal("client", ns), ref, 0, "", "", "")
		if !errors.Is(err, domain.ErrAborted) {
			t.Fatalf("GetSecret after namespace ABA err = %v, want ErrAborted", err)
		}
		got, err := seed.RevealSecret(context.Background(), adminPrincipal(), ref, 0, "", "")
		if err != nil || string(got.Value) != "incarnation-b" {
			t.Fatalf("unbound legitimate reveal = %q, err %v", got.Value, err)
		}
	})

	t.Run("secret write cannot cross into recreated namespace", func(t *testing.T) {
		st, ns, _ := newNamespaceIncarnationStore(t)
		ref := domain.Ref{NS: ns, Key: "new"}
		if _, err := st.CreatePolicy(context.Background(), domain.Policy{Name: "secret-writer", Subject: "client", Allow: []domain.PolicyRule{{Operation: domain.OpSecretWrite, Env: ns.Env, App: ns.App}}}); err != nil {
			t.Fatal(err)
		}
		ring := testSQLKeyring(t, st)
		wrapped := &namespaceSwapStore{Store: st, ReleaseStore: st, target: ns}
		wrapped.afterNamespaceRead = func() error { return replaceWithMTLSNamespace(st, ns) }

		svc := New(wrapped, nil, "test")
		svc.SetKeyring(ring)
		_, err := svc.PutSecret(context.Background(), boundClientPrincipal("client", ns), PutSecretInput{Ref: ref, Value: []byte("crossed"), ContentType: "text/plain"})
		if !errors.Is(err, domain.ErrAborted) {
			t.Fatalf("PutSecret after namespace ABA err = %v, want ErrAborted", err)
		}
		if _, err := st.GetSecretRecord(context.Background(), ref); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("recreated namespace secret err = %v, want ErrNotFound", err)
		}
	})

	t.Run("configuration release write cannot cross into recreated namespace", func(t *testing.T) {
		st, ns, _ := newNamespaceIncarnationStore(t)
		ref := domain.Ref{NS: ns, Key: "settings"}
		wrapped := &namespaceSwapStore{Store: st, ReleaseStore: st, target: ns}
		wrapped.afterNamespaceRead = func() error {
			if err := replaceWithMTLSNamespace(st, ns); err != nil {
				return err
			}
			_, _, err := st.PutParameter(context.Background(), ref, "1", "integer", "{}", "racing-admin")
			return err
		}

		svc := New(wrapped, nil, "test")
		_, err := svc.CreateConfigurationRelease(context.Background(), adminPrincipal(), domain.CreateConfigurationReleaseInput{
			Namespace: ns,
			Name:      "runtime",
			Entries: []domain.ReleaseEntrySelector{{
				Alias: "settings", Kind: domain.ReleaseEntryParameter, Ref: ref, Label: domain.LabelCurrent,
			}},
		})
		if !errors.Is(err, domain.ErrAborted) {
			t.Fatalf("CreateConfigurationRelease after namespace ABA err = %v, want ErrAborted", err)
		}
		rows, _, err := st.ListConfigurationReleases(context.Background(), ns, "runtime", storage.ListPage{})
		if err != nil || len(rows) != 0 {
			t.Fatalf("recreated namespace releases = %+v, err %v", rows, err)
		}
	})
}

func TestNamespaceDeleteAuditKeepsAuthorizedIncarnation(t *testing.T) {
	st, ns, original := newNamespaceIncarnationStore(t)
	wrapped := &namespaceSwapStore{Store: st, ReleaseStore: st, target: ns}
	wrapped.afterDelete = func() error {
		_, err := st.CreateNamespace(context.Background(), domain.Namespace{
			NamespaceRef: ns, AllowedAuthMethods: []domain.AuthMethod{domain.AuthMethodMTLS}, CreatedBy: "racing-admin",
		})
		return err
	}

	if err := New(wrapped, nil, "test").DeleteNamespace(context.Background(), adminPrincipal(), ns); err != nil {
		t.Fatal(err)
	}
	recreated, err := st.GetNamespace(context.Background(), ns)
	if err != nil {
		t.Fatal(err)
	}
	if recreated.ID == original.ID {
		t.Fatalf("namespace row ID was reused: %d", recreated.ID)
	}
	events, _, err := st.ListAudit(context.Background(), domain.AuditFilter{EventType: "namespace.delete"}, storage.ListPage{})
	if err != nil || len(events) != 1 {
		t.Fatalf("delete audit events = %+v, err %v", events, err)
	}
	if events[0].ResourceNamespaceID != original.ID {
		t.Fatalf("delete audit namespace ID = %d, want authorized incarnation %d (recreated %d)", events[0].ResourceNamespaceID, original.ID, recreated.ID)
	}
}
