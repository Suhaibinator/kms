package cli

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/Suhaibinator/kms/internal/storage"
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
	createKMSDB(t, in)

	// Fresh restore into a non-existent destination.
	if err := restoreFile(in, dst, false); err != nil {
		t.Fatalf("restoreFile: %v", err)
	}
	if err := storage.ValidateKMSDatabase(dst); err != nil {
		t.Fatalf("restored database is invalid: %v", err)
	}
}

func TestRestoreFileRemovesSidecars(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "backup.db")
	dst := filepath.Join(dir, "kms.db")
	createKMSDB(t, in)
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
	createKMSDB(t, in)
	writeFileBytes(t, dst, fakeSQLiteBytes("existing"))

	if err := restoreFile(in, dst, false); err == nil {
		t.Fatalf("expected refusal without --force")
	}
	// With force it overwrites.
	if err := restoreFile(in, dst, true); err != nil {
		t.Fatalf("force restore: %v", err)
	}
}

func TestCopyFileAtomicRefusesLateDestinationWithoutForce(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	dst := filepath.Join(dir, "destination")
	writeFileBytes(t, src, []byte("restored data"))
	writeFileBytes(t, dst, []byte("concurrent data"))

	err := copyFileAtomic(src, dst, false)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("copyFileAtomic error = %v, want destination-exists error", err)
	}
	if got := readFileString(t, dst); got != "concurrent data" {
		t.Fatalf("non-forced publish replaced concurrent destination: %q", got)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".kms-restore-*")); err != nil || len(matches) != 0 {
		t.Fatalf("restore staging files remain after refusal: %v (glob err %v)", matches, err)
	}

	// --force remains the explicit compatibility path for replacement.
	if err := copyFileAtomic(src, dst, true); err != nil {
		t.Fatalf("forced copyFileAtomic: %v", err)
	}
	if got := readFileString(t, dst); got != "restored data" {
		t.Fatalf("forced publish did not replace destination: %q", got)
	}
}

func TestCopyFileAtomicRefusesDestinationSymlinkWithoutForce(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	dst := filepath.Join(dir, "destination")
	writeFileBytes(t, src, []byte("restored data"))
	writeFileBytes(t, target, []byte("target data"))
	if err := os.Symlink(target, dst); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := copyFileAtomic(src, dst, false)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("copyFileAtomic error = %v, want destination-exists error", err)
	}
	if got := readFileString(t, target); got != "target data" {
		t.Fatalf("non-forced publish changed symlink target: %q", got)
	}
	if info, err := os.Lstat(dst); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("destination symlink was replaced: info=%v err=%v", info, err)
	}
}

func TestCopyFileAtomicRejectsSharedMutableParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows DACL rejection is covered by fileutil platform tests")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "shared")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "source")
	writeFileBytes(t, src, []byte("restored data"))
	dst := filepath.Join(dir, "destination")
	if err := copyFileAtomic(src, dst, false); err == nil {
		t.Fatal("restore staging accepted a parent mutable by another account")
	}
	if fileExists(dst) {
		t.Fatal("unsafe destination parent was modified")
	}
}

func TestRestoreUsesCanonicalDestinationParent(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	aliasDir := filepath.Join(root, "alias")
	if err := os.Symlink(realDir, aliasDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	in := filepath.Join(root, "backup.db")
	createKMSDB(t, in)
	aliasDst := filepath.Join(aliasDir, "kms.db")
	if err := restoreFile(in, aliasDst, false); err != nil {
		t.Fatal(err)
	}
	if err := storage.ValidateKMSDatabase(filepath.Join(realDir, "kms.db")); err != nil {
		t.Fatalf("canonical destination is not a valid KMS database: %v", err)
	}
}

func TestRestoreFileSameFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "kms.db")
	createKMSDB(t, f)
	if err := restoreFile(f, f, true); err == nil {
		t.Fatalf("expected same-file guard")
	}
}

func TestRestoreFileRejectsUnrelatedSQLite(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "unrelated.db")
	dst := filepath.Join(dir, "kms.db")
	// A fully valid SQLite database with an unrelated schema must be rejected
	// before the destination is touched.
	db, err := gorm.Open(sqlite.Open(in), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE unrelated (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := restoreFile(in, dst, false); err == nil {
		t.Fatal("expected unrelated SQLite input to be rejected")
	}
	if fileExists(dst) {
		t.Fatal("destination should not be written for unrelated SQLite input")
	}
}

func createKMSDB(t *testing.T, path string) {
	t.Helper()
	st, err := storage.Open(path)
	if err != nil {
		t.Fatalf("create KMS database: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close KMS database: %v", err)
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
