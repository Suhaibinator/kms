package integration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// §25.2.8 — an online backup restores to a fully readable database: secrets and
// parameters survive, and the same master key decrypts them.
func TestBackupAndRestore(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.PutSecret(ctx, h.admin, h.stdSecret("/prod/app/db", "restore-me-secret")); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	mustPutParam(t, h, "/prod/app/rate", "42")
	wantRev, err := h.svc.CurrentRevision(ctx)
	if err != nil {
		t.Fatalf("CurrentRevision: %v", err)
	}

	// Take the online backup.
	backupPath := filepath.Join(t.TempDir(), "backup.db")
	if err := h.store.Backup(ctx, backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// Backup refuses to overwrite an existing destination.
	if err := h.store.Backup(ctx, backupPath); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Errorf("Backup over existing file err = %v, want ErrAlreadyExists", err)
	}

	// Restore: open the backup as an independent store and unseal with the same
	// master key file.
	restored, err := storage.Open(backupPath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer func() { _ = restored.Close() }()

	svc := core.New(restored, newTestLogger(h.logBuf), "test")
	keyring, err := crypto.Unseal(ctx, restored, crypto.UnsealOptions{KeyFilePath: h.keyPath})
	if err != nil {
		t.Fatalf("unseal backup: %v", err)
	}
	svc.SetKeyring(keyring)

	gotRev, err := svc.CurrentRevision(ctx)
	if err != nil {
		t.Fatalf("restored CurrentRevision: %v", err)
	}
	if gotRev != wantRev {
		t.Errorf("restored revision = %d, want %d", gotRev, wantRev)
	}

	sec, err := svc.GetSecret(ctx, h.admin, h.ref("/prod/app/db"), 0, "", "", "")
	if err != nil {
		t.Fatalf("restored GetSecret: %v", err)
	}
	if string(sec.Value) != "restore-me-secret" {
		t.Errorf("restored secret = %q, want restore-me-secret", sec.Value)
	}
	param, err := svc.GetParameter(ctx, h.admin, h.ref("/prod/app/rate"), 0, "")
	if err != nil || param.Value != "42" {
		t.Errorf("restored parameter = %q err=%v, want 42", param.Value, err)
	}
}

// §25.2.8 / §25.3 — restoring a backup with the WRONG master key fails fast at
// unseal (key-check verification), before any secret is touched.
func TestRestoreWithWrongKeyFails(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.PutSecret(ctx, h.admin, h.stdSecret("/prod/app/db", "value")); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	backupPath := filepath.Join(t.TempDir(), "backup.db")
	if err := h.store.Backup(ctx, backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// A different key file cannot unseal the backup.
	wrongKeyPath := filepath.Join(t.TempDir(), "wrong.key")
	if _, err := crypto.WriteKEKMaterialFile(wrongKeyPath); err != nil {
		t.Fatalf("write wrong key: %v", err)
	}
	restored, err := storage.Open(backupPath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer func() { _ = restored.Close() }()
	if _, err := crypto.Unseal(ctx, restored, crypto.UnsealOptions{KeyFilePath: wrongKeyPath}); err == nil {
		t.Error("unseal with wrong key succeeded; expected key-check failure")
	}
}
