package storage

import (
	"context"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// seedBoundIdentityCert is seedIdentityCert for an identity bound to a
// namespace, so the listing's env/app columns have something to report.
func seedBoundIdentityCert(t *testing.T, st *SQLStore, name string, ns domain.NamespaceRef, serial string, notAfter time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateIdentity(ctx, CreateIdentityParams{
		Name: name, Kind: domain.IdentityKindClient, Namespace: &ns,
	}); err != nil {
		t.Fatalf("CreateIdentity(%s): %v", name, err)
	}
	cert := domain.IdentityCert{Serial: serial, Fingerprint: "fp-" + serial, NotAfter: notAfter, CreatedAt: opsNow}
	if err := st.InsertIdentityCert(ctx, name, cert); err != nil {
		t.Fatalf("InsertIdentityCert(%s): %v", serial, err)
	}
}

func mustExpiringCerts(t *testing.T, st *SQLStore, within time.Duration, limit int) []ExpiringIdentityCert {
	t.Helper()
	out, err := st.ExpiringIdentityCerts(context.Background(), opsNow, within, limit)
	if err != nil {
		t.Fatalf("ExpiringIdentityCerts: %v", err)
	}
	return out
}

func mustExpiringVersions(t *testing.T, st *SQLStore, within time.Duration, limit int) []ExpiringSecretVersion {
	t.Helper()
	out, err := st.ExpiringSecretVersions(context.Background(), opsNow, within, limit)
	if err != nil {
		t.Fatalf("ExpiringSecretVersions: %v", err)
	}
	return out
}

// TestExpiringListsEmptyDatabase pins the fresh-install reading: empty lists,
// no error on tables that have never been written to.
func TestExpiringListsEmptyDatabase(t *testing.T) {
	t.Parallel()

	st := newStore(t)
	if got := mustExpiringCerts(t, st, opsCertWindow, 10); len(got) != 0 {
		t.Errorf("ExpiringIdentityCerts on an empty database = %+v, want none", got)
	}
	if got := mustExpiringVersions(t, st, opsSecretWindow, 10); len(got) != 0 {
		t.Errorf("ExpiringSecretVersions on an empty database = %+v, want none", got)
	}
}

// TestExpiringIdentityCertsWindow checks the same boundaries the matching count
// enforces: inside the window is listed, already-expired and beyond-the-window
// are not, and a revoked certificate never appears however soon it expires.
func TestExpiringIdentityCertsWindow(t *testing.T) {
	t.Parallel()

	st := newStore(t)
	ns := seedNS(t, st, "prod", "payments").NamespaceRef
	seedBoundIdentityCert(t, st, "soon", ns, "s-soon", opsNow.Add(2*24*time.Hour))
	seedBoundIdentityCert(t, st, "sooner", ns, "s-sooner", opsNow.Add(time.Hour))
	seedBoundIdentityCert(t, st, "edge", ns, "s-edge", opsNow.Add(opsCertWindow))
	seedBoundIdentityCert(t, st, "later", ns, "s-later", opsNow.Add(opsCertWindow+time.Hour))
	seedBoundIdentityCert(t, st, "expired", ns, "s-expired", opsNow.Add(-time.Hour))
	// Unbound identities hold certificates too; they list with empty env/app.
	seedIdentityCert(t, st, "unbound", "s-unbound", opsNow.Add(3*24*time.Hour), false)
	seedIdentityCert(t, st, "revoked", "s-revoked", opsNow.Add(time.Minute), true)

	got := mustExpiringCerts(t, st, opsCertWindow, 10)
	want := []ExpiringIdentityCert{
		{Identity: "sooner", Env: "prod", App: "payments", Serial: "s-sooner", NotAfter: opsNow.Add(time.Hour)},
		{Identity: "soon", Env: "prod", App: "payments", Serial: "s-soon", NotAfter: opsNow.Add(2 * 24 * time.Hour)},
		{Identity: "unbound", Serial: "s-unbound", NotAfter: opsNow.Add(3 * 24 * time.Hour)},
		{Identity: "edge", Env: "prod", App: "payments", Serial: "s-edge", NotAfter: opsNow.Add(opsCertWindow)},
	}
	if len(got) != len(want) {
		t.Fatalf("ExpiringIdentityCerts = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestExpiringIdentityCertsLimit checks the cap is applied in the query and
// keeps the soonest expiries, not an arbitrary page of them.
func TestExpiringIdentityCertsLimit(t *testing.T) {
	t.Parallel()

	st := newStore(t)
	ns := seedNS(t, st, "prod", "payments").NamespaceRef
	for i := range 5 {
		name := "id-" + string(rune('a'+i))
		seedBoundIdentityCert(t, st, name, ns, "serial-"+name, opsNow.Add(time.Duration(5-i)*time.Hour))
	}

	got := mustExpiringCerts(t, st, opsCertWindow, 2)
	if len(got) != 2 {
		t.Fatalf("limited listing has %d rows, want 2", len(got))
	}
	if got[0].Identity != "id-e" || got[1].Identity != "id-d" {
		t.Errorf("limited listing = %s, %s; want the two soonest (id-e, id-d)", got[0].Identity, got[1].Identity)
	}
}

// TestExpiringSecretVersionsWindow mirrors the certificate case: window bounds
// are inclusive, already-expired versions are excluded, and a version that is
// no longer enabled is nobody's problem.
func TestExpiringSecretVersionsWindow(t *testing.T) {
	t.Parallel()

	st := newStore(t)
	seedNS(t, st, "prod", "payments")
	ctx := context.Background()

	seedExpiringSecret(t, st, ref("prod", "payments", "later"), opsNow.Add(opsSecretWindow+time.Hour))
	seedExpiringSecret(t, st, ref("prod", "payments", "edge"), opsNow.Add(opsSecretWindow))
	seedExpiringSecret(t, st, ref("prod", "payments", "soon"), opsNow.Add(time.Hour))
	seedExpiringSecret(t, st, ref("prod", "payments", "expired"), opsNow.Add(-time.Hour))
	seedExpiringSecret(t, st, ref("prod", "payments", "no-expiry"), time.Time{})
	disabled := seedExpiringSecret(t, st, ref("prod", "payments", "disabled"), opsNow.Add(time.Minute))
	if _, err := st.SetSecretVersionState(ctx, ref("prod", "payments", "disabled"), disabled, domain.StateDisabled); err != nil {
		t.Fatalf("SetSecretVersionState: %v", err)
	}

	got := mustExpiringVersions(t, st, opsSecretWindow, 10)
	want := []ExpiringSecretVersion{
		{Env: "prod", App: "payments", Key: "soon", Version: 1, ExpiresAt: opsNow.Add(time.Hour)},
		{Env: "prod", App: "payments", Key: "edge", Version: 1, ExpiresAt: opsNow.Add(opsSecretWindow)},
	}
	if len(got) != len(want) {
		t.Fatalf("ExpiringSecretVersions = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestExpiringListsAgreeWithCounts is the invariant the posture page depends
// on: the list and the gauge are two readings of one query, so a total that
// disagreed with the rows would make "truncated" meaningless.
func TestExpiringListsAgreeWithCounts(t *testing.T) {
	t.Parallel()

	st := newStore(t)
	ns := seedNS(t, st, "prod", "payments").NamespaceRef
	for i := range 3 {
		name := "id-" + string(rune('a'+i))
		seedBoundIdentityCert(t, st, name, ns, "serial-"+name, opsNow.Add(time.Duration(i+1)*time.Hour))
	}
	seedExpiringSecret(t, st, ref("prod", "payments", "one"), opsNow.Add(time.Hour))
	seedExpiringSecret(t, st, ref("prod", "payments", "two"), opsNow.Add(2*time.Hour))

	stats := mustOperationalStats(t, st)
	if got := int64(len(mustExpiringCerts(t, st, opsCertWindow, 100))); got != stats.IdentityCertsExpiringSoon {
		t.Errorf("listed %d expiring certs, counted %d", got, stats.IdentityCertsExpiringSoon)
	}
	if got := int64(len(mustExpiringVersions(t, st, opsSecretWindow, 100))); got != stats.SecretVersionsExpiringSoon {
		t.Errorf("listed %d expiring versions, counted %d", got, stats.SecretVersionsExpiringSoon)
	}
}
