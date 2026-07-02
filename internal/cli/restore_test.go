package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeSQLiteBytes returns bytes that begin with the SQLite magic header.
func fakeSQLiteBytes(tail string) []byte {
	return append([]byte(sqliteMagic), []byte(tail)...)
}

func TestValidateSQLiteFile(t *testing.T) {
	dir := t.TempDir()

	good := filepath.Join(dir, "good.db")
	writeFileBytes(t, good, fakeSQLiteBytes("payload"))
	if err := validateSQLiteFile(good); err != nil {
		t.Fatalf("valid sqlite rejected: %v", err)
	}

	bad := filepath.Join(dir, "bad.json")
	writeFileBytes(t, bad, []byte(`{"not":"sqlite"}`))
	if err := validateSQLiteFile(bad); err == nil {
		t.Fatalf("non-sqlite accepted")
	}

	if err := validateSQLiteFile(filepath.Join(dir, "missing")); err == nil {
		t.Fatalf("missing file accepted")
	}
}

func TestRestoreFile(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "backup.db")
	dst := filepath.Join(dir, "kms.db")
	content := fakeSQLiteBytes("the-data")
	writeFileBytes(t, in, content)

	// Fresh restore into a non-existent destination.
	if err := restoreFile(in, dst, false); err != nil {
		t.Fatalf("restoreFile: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != string(content) {
		t.Fatalf("restored content mismatch")
	}
}

func TestRestoreFileRemovesSidecars(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "backup.db")
	dst := filepath.Join(dir, "kms.db")
	writeFileBytes(t, in, fakeSQLiteBytes("data"))
	writeFileBytes(t, dst, fakeSQLiteBytes("old"))
	writeFileBytes(t, dst+"-wal", []byte("stale wal"))
	writeFileBytes(t, dst+"-shm", []byte("stale shm"))

	if err := restoreFile(in, dst, true); err != nil {
		t.Fatalf("restoreFile: %v", err)
	}
	if fileExists(dst + "-wal") {
		t.Fatalf("stale -wal should be removed")
	}
	if fileExists(dst + "-shm") {
		t.Fatalf("stale -shm should be removed")
	}
}

func TestRestoreFileRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "backup.db")
	dst := filepath.Join(dir, "kms.db")
	writeFileBytes(t, in, fakeSQLiteBytes("data"))
	writeFileBytes(t, dst, fakeSQLiteBytes("existing"))

	if err := restoreFile(in, dst, false); err == nil {
		t.Fatalf("expected refusal without --force")
	}
	// With force it overwrites.
	if err := restoreFile(in, dst, true); err != nil {
		t.Fatalf("force restore: %v", err)
	}
}

func TestRestoreFileSameFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "kms.db")
	writeFileBytes(t, f, fakeSQLiteBytes("data"))
	if err := restoreFile(f, f, true); err == nil {
		t.Fatalf("expected same-file guard")
	}
}

func TestRestoreFileRejectsNonSQLite(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "backup.txt")
	dst := filepath.Join(dir, "kms.db")
	writeFileBytes(t, in, []byte("not a database"))
	if err := restoreFile(in, dst, false); err == nil {
		t.Fatalf("expected rejection of non-sqlite input")
	}
	if fileExists(dst) {
		t.Fatalf("destination should not be written on invalid input")
	}
}

func writeFileBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
