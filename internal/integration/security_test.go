package integration

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
)

// §25.3.1 / §25.3.2 / §25.3.11 — secrets and tokens are never stored in
// plaintext on disk and never appear in logs; only hashes are persisted.
func TestNoPlaintextOrTokensAtRest(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const path = "/prod/app/at-rest"
	const plaintext = "AT-REST-PLAINTEXT-CANARY-4d9e21"

	// A per-secret access token is minted; an identity token is minted too.
	res, err := h.svc.PutSecret(ctx, h.admin, core.PutSecretInput{
		Path: path, Value: []byte(plaintext), GenerateToken: true,
	})
	if err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	accessToken := res.AccessToken
	if accessToken == "" {
		t.Fatal("expected an access token")
	}
	_, idToken, err := h.svc.CreateIdentity(ctx, h.admin, "diskscan", domain.IdentityKindClient)
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}

	// Read the secret so a decryption path also runs before we inspect logs.
	tokenPr := h.admin
	tokenPr.SecretToken = accessToken
	if _, err := h.svc.GetSecret(ctx, tokenPr, path, 0, ""); err != nil {
		t.Fatalf("GetSecret: %v", err)
	}

	_, ver, err := h.store.GetSecretVersion(ctx, path, 0, "")
	if err != nil {
		t.Fatalf("GetSecretVersion: %v", err)
	}

	disk := h.scanBytes()

	// Sanity: the scan actually sees stored data (the ciphertext) and the token
	// hashes — otherwise "absent" would be meaningless.
	if !bytes.Contains(disk, ver.Ciphertext) {
		t.Fatal("scan did not find the stored ciphertext; scan is not covering the data")
	}
	if !bytes.Contains(disk, crypto.TokenHash(idToken)) {
		t.Error("identity token hash not found on disk (expected it to be stored)")
	}
	if !bytes.Contains(disk, crypto.TokenHash(accessToken)) {
		t.Error("secret access-token hash not found on disk (expected it to be stored)")
	}

	// The invariants: no plaintext, no raw tokens on disk.
	if bytes.Contains(disk, []byte(plaintext)) {
		t.Error("secret plaintext found on disk")
	}
	if bytes.Contains(disk, []byte(idToken)) {
		t.Error("raw identity token found on disk (must store only the hash)")
	}
	if bytes.Contains(disk, []byte(accessToken)) {
		t.Error("raw secret access token found on disk (must store only the hash)")
	}

	// §25.3.2 — nothing sensitive reached the logs.
	logs := h.logBuf.String()
	for _, secret := range []string{plaintext, idToken, accessToken} {
		if strings.Contains(logs, secret) {
			t.Errorf("logs leaked sensitive value")
		}
	}
}

// §25.3.5 — disabled secret versions cannot be read.
func TestDisabledSecretUnreadable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const path = "/prod/app/disable-me"

	if _, err := h.svc.PutSecret(ctx, h.admin, putSecret(path, "disabled-value")); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	if _, err := h.svc.DisableSecret(ctx, h.admin, path, 1, false); err != nil {
		t.Fatalf("DisableSecret: %v", err)
	}
	if _, err := h.svc.GetSecret(ctx, h.admin, path, 1, ""); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Errorf("read disabled version err = %v, want ErrFailedPrecondition", err)
	}
	if _, err := h.svc.GetSecret(ctx, h.admin, path, 0, ""); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Errorf("read disabled current err = %v, want ErrFailedPrecondition", err)
	}

	// Re-enabling restores readability.
	if _, err := h.svc.DisableSecret(ctx, h.admin, path, 1, true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if got, err := h.svc.GetSecret(ctx, h.admin, path, 1, ""); err != nil || string(got.Value) != "disabled-value" {
		t.Errorf("re-enabled read = %q err=%v, want disabled-value", got.Value, err)
	}
}

// §25.3.6 — destroyed secret versions cannot be decrypted, and their ciphertext
// is physically gone.
func TestDestroyedVersionUndecryptable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const path = "/prod/app/destroy-me"

	if _, err := h.svc.PutSecret(ctx, h.admin, putSecret(path, "v1-destroyme")); err != nil {
		t.Fatalf("PutSecret v1: %v", err)
	}
	if _, err := h.svc.PutSecret(ctx, h.admin, putSecret(path, "v2-live")); err != nil {
		t.Fatalf("PutSecret v2: %v", err)
	}
	if _, err := h.svc.DestroySecretVersion(ctx, h.admin, path, 1); err != nil {
		t.Fatalf("DestroySecretVersion: %v", err)
	}

	if _, err := h.svc.GetSecret(ctx, h.admin, path, 1, ""); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Errorf("read destroyed version err = %v, want ErrFailedPrecondition", err)
	}

	// The ciphertext bytes are nulled at rest.
	_, ver, err := h.store.GetSecretVersion(ctx, path, 1, "")
	if err != nil {
		t.Fatalf("GetSecretVersion: %v", err)
	}
	if ver.State != domain.StateDestroyed {
		t.Errorf("state = %q, want destroyed", ver.State)
	}
	if ver.Ciphertext != nil || ver.EncryptedDEK != nil || ver.Nonce != nil {
		t.Errorf("destroyed version retains key material: ct=%v dek=%v nonce=%v",
			ver.Ciphertext != nil, ver.EncryptedDEK != nil, ver.Nonce != nil)
	}

	// The still-live version is unaffected.
	if got, err := h.svc.GetSecret(ctx, h.admin, path, 2, ""); err != nil || string(got.Value) != "v2-live" {
		t.Errorf("live version = %q err=%v, want v2-live", got.Value, err)
	}
}

// §25.3.7 — tampering with stored ciphertext causes a decryption failure.
func TestCiphertextTamperingFails(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const path = "/prod/app/tamper-ct"

	if _, err := h.svc.PutSecret(ctx, h.admin, putSecret(path, "tamper-target")); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	h.reopen(func(db *sql.DB) {
		flipColumnByte(t, db, path, 1, "ciphertext")
	})
	if _, err := h.svc.GetSecret(ctx, h.admin, path, 1, ""); !errors.Is(err, domain.ErrDecryptFailed) {
		t.Errorf("read tampered ciphertext err = %v, want ErrDecryptFailed", err)
	}
}

// §25.3.8 — an associated-data mismatch causes a decryption failure. Corrupting
// the stored AAD (which binds resource/path/version) breaks authentication.
// The AAD used for decryption is derived from the row's authoritative identity
// (resource type, path, version), not read from the stored aad column. So the
// stored column is advisory: corrupting it alone must NOT affect a legitimate
// read (the value still decrypts), because the column is never a tamperable
// decryption input. The real binding — that a version's ciphertext cannot be
// decrypted under a different secret's identity — is exercised by
// TestAADCrossWireFails.
func TestStoredAADColumnIsAdvisory(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const path = "/prod/app/tamper-aad"

	if _, err := h.svc.PutSecret(ctx, h.admin, putSecret(path, "aad-target")); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	h.reopen(func(db *sql.DB) {
		if _, err := db.Exec(
			`UPDATE secret_versions SET aad = aad || 'x'
			 WHERE version_number = 1 AND secret_id = (SELECT id FROM secrets WHERE path = ?)`, path); err != nil {
			t.Fatalf("corrupt aad: %v", err)
		}
	})
	got, err := h.svc.GetSecret(ctx, h.admin, path, 1, "")
	if err != nil {
		t.Fatalf("read after corrupting advisory aad column: %v (AAD must derive from identity, not the column)", err)
	}
	if string(got.Value) != "aad-target" {
		t.Errorf("value = %q, want aad-target", got.Value)
	}
}

// flipColumnByte inverts the first byte of a blob column on a secret version.
func flipColumnByte(t *testing.T, db *sql.DB, path string, version int, column string) {
	t.Helper()
	var id int64
	var blob []byte
	row := db.QueryRow(
		`SELECT sv.id, sv.`+column+` FROM secret_versions sv
		 JOIN secrets s ON s.id = sv.secret_id
		 WHERE s.path = ? AND sv.version_number = ?`, path, version)
	if err := row.Scan(&id, &blob); err != nil {
		t.Fatalf("read %s: %v", column, err)
	}
	if len(blob) == 0 {
		t.Fatalf("%s is empty; nothing to tamper", column)
	}
	blob[0] ^= 0xff
	if _, err := db.Exec(`UPDATE secret_versions SET `+column+` = ? WHERE id = ?`, blob, id); err != nil {
		t.Fatalf("write tampered %s: %v", column, err)
	}
}
