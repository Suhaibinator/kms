package integration

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
)

// §25.3.4 — a version that has expired is unreadable. It is created with a
// future expiry (writing an already-past expiry is rejected) and then aged in
// the database to simulate expiry during its lifetime, exercising the read-side
// check in checkVersionReadable.
func TestExpiredVersionUnreadable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const path = "/prod/app/expired"

	future := time.Now().Add(time.Hour).UnixMilli()
	if _, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{
		Path: path, Value: []byte("expired-value"), ExpiresAt: future,
	}); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	// Readable while unexpired.
	if _, err := h.svc.GetSecret(ctx, h.admin, path, 0, ""); err != nil {
		t.Fatalf("read before expiry: %v", err)
	}
	// Age the row so its expiry is in the past. Use the store's fixed-width
	// RFC3339 layout so the value round-trips through parseTime.
	past := time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	h.reopen(func(db *sql.DB) {
		if _, err := db.Exec(
			`UPDATE secret_versions SET expires_at = ?
			 WHERE version_number = 1 AND secret_id = (SELECT id FROM secrets WHERE path = ?)`, past, path); err != nil {
			t.Fatalf("age expiry: %v", err)
		}
	})
	if _, err := h.svc.GetSecret(ctx, h.admin, path, 0, ""); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Errorf("read expired version err = %v, want ErrFailedPrecondition", err)
	}
}

// Writing a secret whose expiry is already in the past is rejected outright.
func TestPutSecretRejectsPastExpiry(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute).UnixMilli()
	_, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{
		Path: "/prod/app/already-expired", Value: []byte("v"), ExpiresAt: past,
	})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("PutSecret past expiry err = %v, want ErrInvalidArgument", err)
	}
}

// §25.3.2 — tampering the wrapped DEK or the nonce yields the same generic
// decryption failure as ciphertext tampering, and never panics.
func TestDEKAndNonceTamperingFail(t *testing.T) {
	for _, column := range []string{"encrypted_dek", "nonce"} {
		t.Run(column, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			const path = "/prod/app/tamper"
			if _, err := h.svc.PutSecret(ctx, h.admin, putSecret(path, "tamper-me")); err != nil {
				t.Fatalf("PutSecret: %v", err)
			}
			h.reopen(func(db *sql.DB) { flipColumnByte(t, db, path, 1, column) })
			_, err := h.svc.GetSecret(ctx, h.admin, path, 0, "")
			if !errors.Is(err, domain.ErrDecryptFailed) {
				t.Errorf("tampered %s err = %v, want ErrDecryptFailed", column, err)
			}
		})
	}
}

// §25.3.3 — cross-wiring one secret's ciphertext/DEK/nonce onto another secret's
// row (which keeps its own, now-mismatched AAD) fails to decrypt: the AAD binds
// ciphertext to its record identity.
func TestAADCrossWireFails(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const dst = "/prod/app/dst"
	const src = "/prod/app/src"

	if _, err := h.svc.PutSecret(ctx, h.admin, putSecret(dst, "destination-value")); err != nil {
		t.Fatalf("PutSecret dst: %v", err)
	}
	if _, err := h.svc.PutSecret(ctx, h.admin, putSecret(src, "source-value")); err != nil {
		t.Fatalf("PutSecret src: %v", err)
	}

	h.reopen(func(db *sql.DB) {
		var ct, dek, nonce []byte
		row := db.QueryRow(
			`SELECT sv.ciphertext, sv.encrypted_dek, sv.nonce FROM secret_versions sv
			 JOIN secrets s ON s.id = sv.secret_id WHERE s.path = ? AND sv.version_number = 1`, src)
		if err := row.Scan(&ct, &dek, &nonce); err != nil {
			t.Fatalf("read src crypto: %v", err)
		}
		// Copy src's crypto material onto dst's row, leaving dst's AAD intact.
		if _, err := db.Exec(
			`UPDATE secret_versions SET ciphertext = ?, encrypted_dek = ?, nonce = ?
			 WHERE version_number = 1 AND secret_id = (SELECT id FROM secrets WHERE path = ?)`,
			ct, dek, nonce, dst); err != nil {
			t.Fatalf("cross-wire update: %v", err)
		}
	})

	if _, err := h.svc.GetSecret(ctx, h.admin, dst, 0, ""); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Errorf("cross-wired read err = %v, want ErrDecryptFailed", err)
	}
}

// §25.3.6 — an unauthorized caller gets the same generic permission error
// whether the resource exists or not, so it cannot probe for existence. Only an
// authorized caller ever observes not-found.
func TestUnauthorizedDoesNotRevealExistence(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const existing = "/prod/app/real"
	const missing = "/prod/app/ghost"

	if _, err := h.svc.PutSecret(ctx, h.admin, putSecret(existing, "real-value")); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}

	client, _ := h.createClient("prober")
	_, errExisting := h.svc.GetSecret(ctx, client, existing, 0, "")
	_, errMissing := h.svc.GetSecret(ctx, client, missing, 0, "")

	if !errors.Is(errExisting, domain.ErrPermissionDenied) || !errors.Is(errMissing, domain.ErrPermissionDenied) {
		t.Fatalf("existing=%v missing=%v, both want ErrPermissionDenied", errExisting, errMissing)
	}
	if errExisting.Error() != errMissing.Error() {
		t.Errorf("existence leaked: existing=%q missing=%q must be identical", errExisting, errMissing)
	}

	// An authorized caller (admin) does see the distinction.
	if _, err := h.svc.GetSecret(ctx, h.admin, missing, 0, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("admin missing read err = %v, want ErrNotFound", err)
	}
}
