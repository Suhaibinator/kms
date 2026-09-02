package storage

import (
	"context"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// opsNow is the fixed sampling instant every case below is relative to.
var opsNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

const (
	opsCertWindow   = 14 * 24 * time.Hour
	opsSecretWindow = 7 * 24 * time.Hour
)

// seedIdentityCert inserts one certificate for a freshly created identity.
func seedIdentityCert(t *testing.T, st *SQLStore, name, serial string, notAfter time.Time, revoked bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateIdentity(ctx, CreateIdentityParams{Name: name, Kind: domain.IdentityKindClient}); err != nil {
		t.Fatalf("CreateIdentity(%s): %v", name, err)
	}
	cert := domain.IdentityCert{Serial: serial, Fingerprint: "fp-" + serial, NotAfter: notAfter, CreatedAt: opsNow}
	if err := st.InsertIdentityCert(ctx, name, cert); err != nil {
		t.Fatalf("InsertIdentityCert(%s): %v", serial, err)
	}
	if revoked {
		if err := st.RevokeIdentityCert(ctx, serial); err != nil {
			t.Fatalf("RevokeIdentityCert(%s): %v", serial, err)
		}
	}
}

// seedExpiringSecret writes one secret version with an expiry.
func seedExpiringSecret(t *testing.T, st *SQLStore, r domain.Ref, expiresAt time.Time) uint64 {
	t.Helper()
	v, _, err := st.CreateSecretVersion(context.Background(), CreateSecretParams{
		Ref:       r,
		CreatedBy: "tester",
		ExpiresAt: expiresAt,
		Encrypt:   encryptStub(nil),
	})
	if err != nil {
		t.Fatalf("CreateSecretVersion(%s): %v", r, err)
	}
	return v
}

func mustOperationalStats(t *testing.T, st *SQLStore) OperationalStats {
	t.Helper()
	stats, err := st.OperationalStats(context.Background(), opsNow, opsCertWindow, opsSecretWindow)
	if err != nil {
		t.Fatalf("OperationalStats: %v", err)
	}
	return stats
}

// TestOperationalStatsEmptyDatabase pins the fresh-install reading: every count
// is zero and nothing errors on tables that have never been written to.
func TestOperationalStatsEmptyDatabase(t *testing.T) {
	t.Parallel()

	st := newStore(t)
	if got, want := mustOperationalStats(t, st), (OperationalStats{}); got != want {
		t.Fatalf("OperationalStats on an empty database = %+v, want %+v", got, want)
	}
}

func TestOperationalStatsChangeLog(t *testing.T) {
	t.Parallel()

	st := newStore(t)
	seedNS(t, st, "prod", "payments")
	ctx := context.Background()
	for i := range 5 {
		if _, _, err := st.PutParameter(ctx, ref("prod", "payments", "k"+string(rune('a'+i))), "v", "string", "", "tester"); err != nil {
			t.Fatalf("PutParameter: %v", err)
		}
	}

	stats := mustOperationalStats(t, st)
	if stats.ChangeLogRows != 5 {
		t.Errorf("ChangeLogRows = %d, want 5", stats.ChangeLogRows)
	}
	if stats.ChangeLogLastRevision != 5 {
		t.Errorf("ChangeLogLastRevision = %d, want 5", stats.ChangeLogLastRevision)
	}
	if stats.ChangeLogOldestRevision != 1 {
		t.Errorf("ChangeLogOldestRevision = %d, want 1", stats.ChangeLogOldestRevision)
	}
}

// TestOperationalStatsChangeLogAfterPrune is why the last revision comes from
// the sequence and not from MAX(revision): a prune that empties the log must
// not make the write position look like it rolled back to zero.
func TestOperationalStatsChangeLogAfterPrune(t *testing.T) {
	t.Parallel()

	st := newStore(t)
	seedNS(t, st, "prod", "payments")
	ctx := context.Background()
	for i := range 3 {
		if _, _, err := st.PutParameter(ctx, ref("prod", "payments", "k"+string(rune('a'+i))), "v", "string", "", "tester"); err != nil {
			t.Fatalf("PutParameter: %v", err)
		}
	}

	// Keep only the newest entry.
	if _, err := st.PruneChangeLog(ctx, time.Hour, 1); err != nil {
		t.Fatalf("PruneChangeLog: %v", err)
	}
	stats := mustOperationalStats(t, st)
	if stats.ChangeLogRows != 1 || stats.ChangeLogOldestRevision != 3 || stats.ChangeLogLastRevision != 3 {
		t.Fatalf("after a partial prune = %+v", stats)
	}

	// Now remove everything.
	if _, err := st.PruneChangeLog(ctx, time.Hour, 0); err != nil {
		t.Fatalf("PruneChangeLog: %v", err)
	}
	stats = mustOperationalStats(t, st)
	if stats.ChangeLogRows != 0 {
		t.Errorf("ChangeLogRows = %d, want 0", stats.ChangeLogRows)
	}
	if stats.ChangeLogOldestRevision != 0 {
		t.Errorf("ChangeLogOldestRevision = %d, want 0 with an empty log", stats.ChangeLogOldestRevision)
	}
	if stats.ChangeLogLastRevision != 3 {
		t.Errorf("ChangeLogLastRevision = %d, want 3 (revisions are never reused)", stats.ChangeLogLastRevision)
	}
}

// TestOperationalStatsIdentityCerts covers the window edges and the two
// exclusions that keep the number actionable: revoked certificates and ones
// that have already expired.
func TestOperationalStatsIdentityCerts(t *testing.T) {
	t.Parallel()

	st := newStore(t)
	horizon := opsNow.Add(opsCertWindow)
	seedIdentityCert(t, st, "at-now", "01", opsNow, false)
	seedIdentityCert(t, st, "mid-window", "02", opsNow.Add(24*time.Hour), false)
	seedIdentityCert(t, st, "at-horizon", "03", horizon, false)
	seedIdentityCert(t, st, "past-horizon", "04", horizon.Add(time.Nanosecond), false)
	seedIdentityCert(t, st, "already-expired", "05", opsNow.Add(-time.Nanosecond), false)
	seedIdentityCert(t, st, "revoked-in-window", "06", opsNow.Add(24*time.Hour), true)

	if got := mustOperationalStats(t, st).IdentityCertsExpiringSoon; got != 3 {
		t.Fatalf("IdentityCertsExpiringSoon = %d, want 3 (at-now, mid-window, at-horizon)", got)
	}

	// A zero window still counts a certificate expiring at exactly now, and
	// nothing else.
	stats, err := st.OperationalStats(context.Background(), opsNow, 0, opsSecretWindow)
	if err != nil {
		t.Fatalf("OperationalStats: %v", err)
	}
	if stats.IdentityCertsExpiringSoon != 1 {
		t.Errorf("zero window = %d, want 1", stats.IdentityCertsExpiringSoon)
	}
}

// TestOperationalStatsSecretVersions covers the same edges plus the state
// filter: a disabled or destroyed version's expiry is nobody's problem.
func TestOperationalStatsSecretVersions(t *testing.T) {
	t.Parallel()

	st := newStore(t)
	seedNS(t, st, "prod", "payments")
	ctx := context.Background()
	horizon := opsNow.Add(opsSecretWindow)

	seedExpiringSecret(t, st, ref("prod", "payments", "at-now"), opsNow)
	seedExpiringSecret(t, st, ref("prod", "payments", "mid-window"), opsNow.Add(time.Hour))
	seedExpiringSecret(t, st, ref("prod", "payments", "at-horizon"), horizon)
	seedExpiringSecret(t, st, ref("prod", "payments", "past-horizon"), horizon.Add(time.Nanosecond))
	seedExpiringSecret(t, st, ref("prod", "payments", "already-expired"), opsNow.Add(-time.Nanosecond))
	// No expiry at all: never counted, however old.
	putSecret(t, st, ref("prod", "payments", "never"), false)

	disabled := ref("prod", "payments", "disabled")
	v := seedExpiringSecret(t, st, disabled, opsNow.Add(time.Hour))
	if _, err := st.SetSecretVersionState(ctx, disabled, v, domain.StateDisabled); err != nil {
		t.Fatalf("SetSecretVersionState: %v", err)
	}
	destroyed := ref("prod", "payments", "destroyed")
	v = seedExpiringSecret(t, st, destroyed, opsNow.Add(time.Hour))
	if _, err := st.DestroySecretVersion(ctx, destroyed, v); err != nil {
		t.Fatalf("DestroySecretVersion: %v", err)
	}

	if got := mustOperationalStats(t, st).SecretVersionsExpiringSoon; got != 3 {
		t.Fatalf("SecretVersionsExpiringSoon = %d, want 3 (at-now, mid-window, at-horizon)", got)
	}
}

// TestOperationalStatsWindowsAreIndependent guards against the two windows
// being crossed: a long certificate window must not widen the secret one.
func TestOperationalStatsWindowsAreIndependent(t *testing.T) {
	t.Parallel()

	st := newStore(t)
	seedNS(t, st, "prod", "payments")
	seedIdentityCert(t, st, "cert", "01", opsNow.Add(10*24*time.Hour), false)
	seedExpiringSecret(t, st, ref("prod", "payments", "secret"), opsNow.Add(10*24*time.Hour))

	stats, err := st.OperationalStats(context.Background(), opsNow, 30*24*time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("OperationalStats: %v", err)
	}
	if stats.IdentityCertsExpiringSoon != 1 {
		t.Errorf("IdentityCertsExpiringSoon = %d, want 1", stats.IdentityCertsExpiringSoon)
	}
	if stats.SecretVersionsExpiringSoon != 0 {
		t.Errorf("SecretVersionsExpiringSoon = %d, want 0", stats.SecretVersionsExpiringSoon)
	}
}

// TestOperationalStatsZeroNow keeps a caller that forgets the clock honest: a
// zero instant samples against the wall clock rather than silently comparing
// every timestamp against the empty string.
func TestOperationalStatsZeroNow(t *testing.T) {
	t.Parallel()

	st := newStore(t)
	seedIdentityCert(t, st, "soon", "01", time.Now().Add(time.Hour), false)

	stats, err := st.OperationalStats(context.Background(), time.Time{}, opsCertWindow, opsSecretWindow)
	if err != nil {
		t.Fatalf("OperationalStats: %v", err)
	}
	if stats.IdentityCertsExpiringSoon != 1 {
		t.Fatalf("IdentityCertsExpiringSoon = %d, want 1", stats.IdentityCertsExpiringSoon)
	}
}
