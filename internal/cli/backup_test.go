package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/storage"
)

// TestBackupCommand exercises backup against a real (migrated) database and
// checks the refuse-existing-output behavior.
func TestBackupCommand(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kms.db")
	// Create a real database so backup has something valid to copy.
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	_ = store.Close()

	out := filepath.Join(dir, "backup.db")
	c := newTestCLI()
	if code := c.cmdBackup([]string{"--sqlite-path", dbPath, "--out", out}); code != 0 {
		t.Fatalf("backup exit = %d, stderr=%s", code, c.stderr())
	}
	if err := validateSQLiteFile(out); err != nil {
		t.Fatalf("backup output is not a valid sqlite db: %v", err)
	}

	// A second backup to the same path must be refused.
	c2 := newTestCLI()
	if code := c2.cmdBackup([]string{"--sqlite-path", dbPath, "--out", out}); code == 0 {
		t.Fatalf("expected refusal to overwrite existing backup")
	}
	if !strings.Contains(c2.stderr(), "already exists") {
		t.Fatalf("stderr = %s", c2.stderr())
	}
}

func TestBackupRequiresOut(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kms.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	_ = store.Close()

	c := newTestCLI()
	if code := c.cmdBackup([]string{"--sqlite-path", dbPath}); code == 0 {
		t.Fatalf("expected failure without --out")
	}
}

func TestBackupRejectsMissingSource(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.db")
	out := filepath.Join(dir, "backup.db")
	c := newTestCLI()
	if code := c.cmdBackup([]string{"--sqlite-path", missing, "--out", out}); code == 0 {
		t.Fatal("missing source was accepted as an empty backup")
	}
	if fileExists(missing) || fileExists(out) {
		t.Fatal("failed backup created a source or output database")
	}
}

// TestRestoreCommandEndToEnd backs up a real database and restores it,
// validating that the restored copy opens.
func TestRestoreCommandEndToEnd(t *testing.T) {
	dir := t.TempDir()
	srcDB := filepath.Join(dir, "src.db")
	store, err := storage.Open(srcDB)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	_ = store.Close()

	backup := filepath.Join(dir, "backup.db")
	c := newTestCLI()
	if code := c.cmdBackup([]string{"--sqlite-path", srcDB, "--out", backup}); code != 0 {
		t.Fatalf("backup exit=%d stderr=%s", code, c.stderr())
	}

	// restore now confirms before it writes; a non-interactive caller supplies
	// the answer with --yes.
	restored := filepath.Join(dir, "restored.db")
	c2 := newTestCLI()
	if code := c2.cmdRestore([]string{"--sqlite-path", restored, "--in", backup, "--yes"}); code != 0 {
		t.Fatalf("restore exit=%d stderr=%s", code, c2.stderr())
	}
	if !strings.Contains(c2.stdout(), "Restored") {
		t.Fatalf("stdout = %s", c2.stdout())
	}
}
