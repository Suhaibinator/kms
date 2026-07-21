//go:build darwin

package fileutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOpenPrivateExclusiveRejectsInheritableDarwinACL(t *testing.T) {
	parent := t.TempDir()
	addDarwinTestACL(t, parent, "everyone allow read,file_inherit,directory_inherit")
	path := filepath.Join(parent, "secret")
	if file, err := OpenPrivateExclusive(path); err == nil {
		_ = file.Close()
		t.Fatal("private creation accepted an ACL-inheriting parent")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("rejected private path was created: %v", err)
	}
	if _, err := MkdirPrivateTemp(parent, ".private-"); err == nil {
		t.Fatal("private directory creation accepted an ACL-inheriting parent")
	}
}

func TestRestrictOwnerOnlyRemovesDarwinACLByDescriptor(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "probe")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Errorf("close ACL test file: %v", err)
		}
	})
	addDarwinTestACL(t, path, "everyone allow read")
	before, err := darwinACLLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 {
		t.Fatal("test setup did not produce an inherited ACL")
	}
	if err := RestrictOwnerOnly(file, false); err != nil {
		t.Fatal(err)
	}
	after, err := darwinACLLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("ACL remains after restriction: %v", after)
	}
}

func TestSecureExistingPrivateFileRejectsDarwinAllowACLWithoutChangingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	want := []byte("existing database")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	addDarwinTestACL(t, path, "everyone allow read")
	before, err := darwinACLLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SecureExistingPrivateFile(path); err == nil {
		t.Fatal("existing file with broad allow ACL was accepted")
	}
	after, err := darwinACLLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("rejected ACL was changed: before=%v after=%v", before, after)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("rejected file data changed: %q", got)
	}
}

func addDarwinTestACL(t *testing.T, path, entry string) {
	t.Helper()
	cmd := exec.Command("/bin/chmod", "+a", entry, path)
	cmd.Env = darwinCommandEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add test ACL: %v: %s", err, out)
	}
	t.Cleanup(func() {
		cmd := exec.Command("/bin/chmod", "-N", path)
		cmd.Env = darwinCommandEnv()
		_ = cmd.Run()
	})
}
