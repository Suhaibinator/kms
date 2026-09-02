package fileutil

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeReadOnlyPrivate(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	file, err := OpenPrivateExclusive(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestReadPrivateFileAcceptsReadOnlyOwnerFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are not the Windows access model")
	}
	path := filepath.Join(t.TempDir(), "token")
	writeReadOnlyPrivate(t, path, []byte("secret\n"), 0o400)
	data, err := ReadPrivateFile(path)
	if err != nil {
		t.Fatalf("0400 owner-only file rejected: %v", err)
	}
	if !bytes.Equal(data, []byte("secret\n")) {
		t.Fatalf("content = %q", data)
	}
	// A read must not repair or otherwise touch the mode.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("mode after read = %04o, want 0400", info.Mode().Perm())
	}
}

func TestReadPrivateFileRejectsBroadMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows security is asserted through DACL-specific tests")
	}
	path := filepath.Join(t.TempDir(), "token")
	writeReadOnlyPrivate(t, path, []byte("secret"), 0o640)
	if _, err := ReadPrivateFile(path); err == nil {
		t.Fatal("group-readable file was accepted")
	}
}

func TestReadPrivateFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	writeReadOnlyPrivate(t, target, []byte("secret"), 0o600)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ReadPrivateFile(link); err == nil {
		t.Fatal("symlink was accepted as a private regular file")
	}
}

func TestReadPrivateFileRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big")
	writeReadOnlyPrivate(t, path, bytes.Repeat([]byte("x"), MaxPrivateFileBytes+1), 0o600)
	if _, err := ReadPrivateFile(path); err == nil {
		t.Fatal("oversized file was accepted")
	}
}

func TestReadPrivateFileMissing(t *testing.T) {
	if _, err := ReadPrivateFile(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing file was accepted")
	}
}
