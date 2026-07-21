//go:build darwin || linux

package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestPrivateDatabaseArtifactsIgnoreUmask022 reproduces the umask from the
// original database and VACUUM INTO findings. Both final artifacts must remain
// owner-only even when ordinary file creation would produce mode 0644.
func TestPrivateDatabaseArtifactsIgnoreUmask022(t *testing.T) {
	previous := unix.Umask(0o022)
	t.Cleanup(func() { unix.Umask(previous) })

	dir := t.TempDir()
	databasePath := filepath.Join(dir, "kms.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatalf("open database under umask 022: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	assertPrivateArtifactMode(t, databasePath)

	backupPath := filepath.Join(dir, "backup.db")
	if err := store.Backup(context.Background(), backupPath); err != nil {
		t.Fatalf("backup database under umask 022: %v", err)
	}
	assertPrivateArtifactMode(t, backupPath)
}

func assertPrivateArtifactMode(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("%s mode under umask 022 = %04o, want 0600", path, got)
	}
}
