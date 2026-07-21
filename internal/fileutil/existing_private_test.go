package fileutil

import (
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
	if err := os.WriteFile(path, []byte("private"), 0o600); err != nil {
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
