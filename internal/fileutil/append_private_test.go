package fileutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// appendString opens path for private append, writes s, and closes the file.
func appendString(t *testing.T, path, s string) {
	t.Helper()
	file, err := OpenPrivateAppend(path)
	if err != nil {
		t.Fatalf("OpenPrivateAppend(%s): %v", path, err)
	}
	if _, err := file.WriteString(s); err != nil {
		_ = file.Close()
		t.Fatalf("write: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestOpenPrivateAppendCreatesOwnerOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	appendString(t, path, "first\n")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("mode = %v, want a regular file", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestOpenPrivateAppendPreservesPriorContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	appendString(t, path, "first\n")
	appendString(t, path, "second\n")

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first\nsecond\n" {
		t.Fatalf("content = %q, want the two appends in order", got)
	}
}

func TestOpenPrivateAppendAcceptsExistingPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	created, err := OpenPrivateExclusive(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := created.WriteString("seed\n"); err != nil {
		_ = created.Close()
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}

	appendString(t, path, "more\n")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "seed\nmore\n" {
		t.Fatalf("content = %q, want the append after the seeded content", got)
	}
}

func TestOpenPrivateAppendRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	file, err := OpenPrivateAppend(link)
	if err == nil {
		_ = file.Close()
		t.Fatal("existing symlink was accepted as a private regular file")
	}
	// The target must not have been extended through the link.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "private" {
		t.Fatalf("symlink target = %q, want it untouched", got)
	}
}

func TestOpenPrivateAppendRejectsBroadMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows security is asserted through DACL-specific tests")
	}
	path := filepath.Join(t.TempDir(), "existing")
	if err := os.WriteFile(path, []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := OpenPrivateAppend(path)
	if err == nil {
		_ = file.Close()
		t.Fatal("existing world-readable file was accepted")
	}
}

func TestOpenPrivateAppendRejectsGroupWritableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows security is asserted through DACL-specific tests")
	}
	path := filepath.Join(t.TempDir(), "existing")
	if err := os.WriteFile(path, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o620); err != nil {
		t.Fatal(err)
	}
	file, err := OpenPrivateAppend(path)
	if err == nil {
		_ = file.Close()
		t.Fatal("existing group-writable file was accepted")
	}
}

func TestOpenPrivateAppendRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := OpenPrivateAppend(path)
	if err == nil {
		_ = file.Close()
		t.Fatal("a directory was accepted as an append target")
	}
}

func TestOpenPrivateAppendRejectsEmptyPath(t *testing.T) {
	file, err := OpenPrivateAppend("")
	if err == nil {
		_ = file.Close()
		t.Fatal("empty path was accepted")
	}
}
