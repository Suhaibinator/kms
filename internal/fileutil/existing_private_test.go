package fileutil

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSecureExistingPrivateFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := SecureExistingPrivateFile(link); err == nil {
		t.Fatal("existing symlink was accepted as a private regular file")
	}
}

func TestSecureExistingPrivateFileRejectsBroadMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows security is asserted through DACL-specific tests")
	}
	path := filepath.Join(t.TempDir(), "existing")
	if err := os.WriteFile(path, []byte("private"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := SecureExistingPrivateFile(path); err == nil {
		t.Fatal("existing broadly accessible file was accepted")
	}
}

func TestSecureExistingPrivateFileAcceptsOwnerOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	file, err := OpenPrivateExclusive(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("private"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	stable, err := SecureExistingPrivateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if stable == "" {
		t.Fatal("secured existing path is empty")
	}
}

func TestValidateExistingPrivateFileDoesNotNormalizeOwnerMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows access is expressed through DACLs rather than POSIX mode bits")
	}
	path := filepath.Join(t.TempDir(), "existing.db")
	want := []byte("operator database")
	if err := os.WriteFile(path, want, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stable, err := ValidateExistingPrivateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if stable == "" {
		t.Fatal("validated path is empty")
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("validation changed file metadata: before=%v/%v after=%v/%v", before.Mode(), before.ModTime(), after.Mode(), after.ModTime())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("validation changed content: %q", got)
	}
}
