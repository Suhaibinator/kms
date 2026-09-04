package core

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// recordingMetrics counts every hook call by label so tests can assert that
// a security decision reached the seam exactly once with the closed-set value.
type recordingMetrics struct {
	mu     sync.Mutex
	counts map[string]int
}

func newRecordingMetrics() *recordingMetrics { return &recordingMetrics{counts: map[string]int{}} }

func (r *recordingMetrics) bump(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[key]++
}

func (r *recordingMetrics) get(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[key]
}

func (r *recordingMetrics) AuthFailure(reason string)       { r.bump("auth_failure:" + reason) }
func (r *recordingMetrics) AuthzDenied(operation string)    { r.bump("authz_denied:" + operation) }
func (r *recordingMetrics) AuthzMethodDenied(method string) { r.bump("authz_method_denied:" + method) }
func (r *recordingMetrics) RateLimited(limiter string)      { r.bump("rate_limited:" + limiter) }
func (r *recordingMetrics) AuditEvent(eventType, decision string) {
	r.bump("audit:" + eventType + ":" + decision)
}
func (r *recordingMetrics) AuditWriteFailed()             { r.bump("audit_write_failed") }
func (r *recordingMetrics) AuditPruned(n int)             { r.bump("audit_pruned") }
func (r *recordingMetrics) DecryptFailed()                { r.bump("decrypt_failed") }
func (r *recordingMetrics) ReleaseOutcome(outcome string) { r.bump("release:" + outcome) }

func expectCount(t *testing.T, m *recordingMetrics, key string, want int) {
	t.Helper()
	if got := m.get(key); got != want {
		t.Errorf("%s = %d, want %d (all: %v)", key, got, want, m.counts)
	}
}

func TestMetricsDefaultIsNoop(t *testing.T) {
	s := newTestService(newFakeStore())
	// Every hook must be callable before SetMetrics and after SetMetrics(nil).
	s.m().AuthFailure(AuthFailureMissing)
	s.SetMetrics(nil)
	s.m().AuthzDenied(domain.OpParameterRead)
	if _, ok := s.m().(noopMetrics); !ok {
		t.Fatalf("SetMetrics(nil) left %T attached, want noopMetrics", s.m())
	}
}

func TestMetricsAuthFailureReasons(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)
	m := newRecordingMetrics()
	s.SetMetrics(m)
	leaf := certOnlyClient(t, s, store, "svc", nil)
	_, otherToken := newClient(t, s, store, "other", nil, domain.AuthMethodToken)

	// Nothing presented: no audit row, but the probe is counted.
	if _, err := s.ResolvePrincipal(ctx, CredentialInput{}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("missing credentials err = %v", err)
	}
	expectCount(t, m, "auth_failure:"+AuthFailureMissing, 1)

	// Invalid token: counted under the presented method.
	if _, err := s.ResolvePrincipal(ctx, CredentialInput{Token: "kms_nope"}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("invalid token err = %v", err)
	}
	expectCount(t, m, "auth_failure:"+AuthFailureToken, 1)

	// Valid certificate and a valid token for a different identity.
	if _, err := s.ResolvePrincipal(ctx, CredentialInput{Token: otherToken, PeerCert: leaf}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("mismatch err = %v", err)
	}
	expectCount(t, m, "auth_failure:"+AuthFailureCredentialMismatch, 1)

	// Admin with a token only while the client-certificate requirement is on.
	_, adminToken := issueAdminCert(t, s, store, "root")
	s.SetAdminRequireClientCert(true)
	if _, err := s.ResolvePrincipal(ctx, CredentialInput{Token: adminToken}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("admin token-only err = %v", err)
	}
	expectCount(t, m, "auth_failure:"+AuthFailureAdminClientCertRequired, 1)
	// The audit row for every persisted failure reaches the seam too.
	if got := m.get("audit:auth.failure:deny"); got < 3 {
		t.Errorf("audit auth.failure/deny = %d, want at least 3", got)
	}
}

func TestMetricsAuthzDenials(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	seedTokenNS(store)
	if _, _, err := store.PutParameter(ctx, tref("x"), "1", "integer", "{}", "root"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := newTestService(store)
	m := newRecordingMetrics()
	s.SetMetrics(m)

	if _, err := s.GetParameter(ctx, clientPrincipal("app"), tref("x"), 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
	expectCount(t, m, "authz_denied:"+domain.OpParameterRead, 1)
	expectCount(t, m, "audit:authz.denial:deny", 1)

	if _, _, err := s.ListParameters(ctx, clientPrincipal("app"), tns, "", storage.ListPage{Limit: 10}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("list err = %v, want ErrPermissionDenied", err)
	}
	expectCount(t, m, "authz_denied:"+domain.OpParameterList, 1)

	// Purely administrative operation refused for a client.
	if _, err := s.ListKeyMetadata(ctx, clientPrincipal("app")); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("admin op err = %v, want ErrPermissionDenied", err)
	}
	expectCount(t, m, "authz_denied:key.read", 1) // admin ops report their event type

	// Namespace auth-method gate: an mTLS-only namespace refuses a token caller.
	mtlsOnly := mkns("prod", "locked")
	store.addNamespace(mtlsOnly, domain.AuthMethodMTLS)
	if _, err := s.GetParameter(ctx, clientPrincipal("app"), domain.Ref{NS: mtlsOnly, Key: "x"}, 0, ""); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("method gate err = %v, want ErrPermissionDenied", err)
	}
	expectCount(t, m, "authz_method_denied:"+AuthFailureToken, 1)
	expectCount(t, m, "authz_denied:"+domain.OpParameterRead, 1) // the gate is not a policy denial
}

func TestMetricsDecryptFailed(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	m := newRecordingMetrics()
	s.SetMetrics(m)
	putSecret(t, s, PutSecretInput{Ref: tref("s"), Value: []byte("v"), ContentType: "text/plain"})
	expectCount(t, m, "decrypt_failed", 0)

	store.tamperCiphertext(tref("s"), 1)
	if _, err := s.GetSecret(ctx, adminPrincipal(), tref("s"), 0, "", "", ""); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Fatalf("err = %v, want ErrDecryptFailed", err)
	}
	expectCount(t, m, "decrypt_failed", 1)
}

func TestMetricsBindingLifecyclePersistedAudits(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	m := newRecordingMetrics()
	s.SetMetrics(m)
	ref := tref("binding-audit-metrics")
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("v")})

	if _, err := s.BindSecret(ctx, adminPrincipal(), ref, 1, testBindingKeyA); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, err := s.PreviewSecretBindingCohort(ctx, adminPrincipal(), ref, 2, testBindingKeyA); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if _, err := s.RotateSecretBindingKey(ctx, adminPrincipal(), ref, 2, testBindingKeyA, testBindingKeyB); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := s.UnbindSecret(ctx, adminPrincipal(), ref, 3, testBindingKeyB); err != nil {
		t.Fatalf("unbind: %v", err)
	}
	purgeRef := tref("binding-purge-audit-metrics")
	putSecret(t, s, PutSecretInput{Ref: purgeRef, Value: []byte("v"), BindingKey: testBindingKeyA})
	preview, err := s.PreviewSecretBindingCohort(ctx, adminPrincipal(), purgeRef, 1, testBindingKeyA)
	if err != nil {
		t.Fatalf("purge preview: %v", err)
	}
	if _, err := s.PurgeSecretBindingCohort(ctx, adminPrincipal(), purgeRef, 1, testBindingKeyA, preview.Revision, preview.AffectedVersions); err != nil {
		t.Fatalf("purge: %v", err)
	}

	for _, eventType := range []string{"secret.bind", "secret.binding_cohort.preview", "secret.binding_key.rotate", "secret.unbind", "secret.binding_cohort.purge"} {
		want := 1
		if eventType == "secret.binding_cohort.preview" {
			want = 2
		}
		expectCount(t, m, "audit:"+eventType+":allow", want)
	}
	expectCount(t, m, "audit_write_failed", 0)
}

func TestMetricsBindingPurgeCleanupPendingCountsCommittedAudit(t *testing.T) {
	store := newFakeStore()
	s := newTestService(store)
	withKeyring(t, s)
	m := newRecordingMetrics()
	s.SetMetrics(m)
	ref := tref("purge-cleanup-pending-metrics")
	putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("v"), BindingKey: testBindingKeyA})
	preview, err := s.PreviewSecretBindingCohort(context.Background(), adminPrincipal(), ref, 1, testBindingKeyA)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	store.purgeResultErr = storage.ErrPurgeCleanupPending

	if _, err := s.PurgeSecretBindingCohort(context.Background(), adminPrincipal(), ref, 1, testBindingKeyA, preview.Revision, preview.AffectedVersions); !errors.Is(err, storage.ErrPurgeCleanupPending) {
		t.Fatalf("purge cleanup pending = %v", err)
	}
	expectCount(t, m, "audit:secret.binding_cohort.purge:allow", 1)
	expectCount(t, m, "audit:secret.binding_cohort.purge:error", 0)
	expectCount(t, m, "audit_write_failed", 0)
}

func TestMetricsBindingTransactionalAuditFailureIsDistinct(t *testing.T) {
	t.Run("required allow audit failure", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withKeyring(t, s)
		m := newRecordingMetrics()
		s.SetMetrics(m)
		ref := tref("binding-audit-metric-failure")
		putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("v")})
		store.bindingAuditErr = errors.New("transactional audit sink unavailable")

		if _, err := s.BindSecret(context.Background(), adminPrincipal(), ref, 1, testBindingKeyA); !errors.Is(err, domain.ErrFailedPrecondition) || err.Error() != "audit unavailable: failed precondition" {
			t.Fatalf("bind audit failure = %v, want fixed failed precondition", err)
		}
		expectCount(t, m, "audit_write_failed", 1)
		expectCount(t, m, "audit:secret.bind:allow", 0)
		expectCount(t, m, "audit:secret.bind:error", 1)
	})

	t.Run("ordinary binding failure", func(t *testing.T) {
		store := newFakeStore()
		s := newTestService(store)
		withKeyring(t, s)
		m := newRecordingMetrics()
		s.SetMetrics(m)
		ref := tref("binding-error-metric")
		putSecret(t, s, PutSecretInput{Ref: ref, Value: []byte("v"), BindingKey: testBindingKeyA})

		if _, err := s.UnbindSecret(context.Background(), adminPrincipal(), ref, 1, testBindingKeyB); !errors.Is(err, domain.ErrDecryptFailed) {
			t.Fatalf("wrong-key unbind = %v", err)
		}
		expectCount(t, m, "audit_write_failed", 0)
		expectCount(t, m, "audit:secret.unbind:error", 1)
	})
}

func TestMetricsReleaseOutcomes(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns, CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	ref := domain.Ref{NS: ns, Key: "config"}
	svc := New(st, nil, "test")
	m := newRecordingMetrics()
	svc.SetMetrics(m)
	pr := adminPrincipal()
	create := func(value string) domain.ConfigurationRelease {
		if _, _, err := st.PutParameter(ctx, ref, value, "integer", "{}", "admin"); err != nil {
			t.Fatal(err)
		}
		r, err := svc.CreateConfigurationRelease(ctx, pr, domain.CreateConfigurationReleaseInput{Namespace: ns, Name: "runtime", Entries: []domain.ReleaseEntrySelector{{Alias: "config", Kind: domain.ReleaseEntryParameter, Ref: ref}}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r1, r2 := create("1"), create("2")
	if _, _, err := svc.ActivateConfigurationRelease(ctx, pr, ns, "runtime", r1.Version, nil); err != nil {
		t.Fatal(err)
	}
	expectCount(t, m, "release:"+ReleaseOutcomeActivated, 1)

	// A stale expectation on activation is a CAS conflict.
	stale := uint64(99)
	if _, _, err := svc.ActivateConfigurationRelease(ctx, pr, ns, "runtime", r2.Version, &stale); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("activate CAS err = %v, want ErrAborted", err)
	}
	expectCount(t, m, "release:"+ReleaseOutcomeCASConflict, 1)

	if _, _, err := svc.ActivateConfigurationRelease(ctx, pr, ns, "runtime", r2.Version, nil); err != nil {
		t.Fatal(err)
	}
	expectCount(t, m, "release:"+ReleaseOutcomeActivated, 2)

	// Rollback's own expectation check is a CAS conflict too, then a
	// successful rollback is classified as rolled_back, not activated.
	wrong := uint64(1)
	if _, err := svc.RollbackConfigurationRelease(ctx, pr, ns, "runtime", &wrong); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("rollback CAS err = %v, want ErrAborted", err)
	}
	expectCount(t, m, "release:"+ReleaseOutcomeCASConflict, 2)
	if _, err := svc.RollbackConfigurationRelease(ctx, pr, ns, "runtime", nil); err != nil {
		t.Fatal(err)
	}
	expectCount(t, m, "release:"+ReleaseOutcomeRolledBack, 1)
	expectCount(t, m, "release:"+ReleaseOutcomeActivated, 2)
}

func TestOperationalReport(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	s := newTestService(store)
	withCA(t, s)

	rep, err := s.OperationalReport(ctx, reportWindow)
	if err != nil {
		t.Fatalf("OperationalReport: %v", err)
	}
	if rep.AdminCertsLacking != 0 || rep.AdminCertsExpiringSoon != 0 {
		t.Fatalf("empty store report = %+v", rep)
	}

	// An admin without a certificate is counted, never named.
	store.addIdentity("root", domain.IdentityKindAdmin, "kms_root")
	rep, err = s.OperationalReport(ctx, reportWindow)
	if err != nil {
		t.Fatalf("OperationalReport: %v", err)
	}
	if rep.AdminCertsLacking != 1 {
		t.Fatalf("lacking = %d, want 1", rep.AdminCertsLacking)
	}
	if rep.KEKGenerations != len(store.keys) {
		t.Fatalf("kek generations = %d, want %d", rep.KEKGenerations, len(store.keys))
	}
}
