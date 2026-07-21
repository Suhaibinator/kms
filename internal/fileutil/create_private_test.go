package fileutil

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenPrivateExclusiveIsOwnerOnlyAndRefusesCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	file, err := OpenPrivateExclusive(path)
	if err != nil {
		t.Fatalf("OpenPrivateExclusive: %v", err)
	}
	if _, err := file.WriteString("original"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private file mode = %v, %v", info, err)
		}
	}

	replacement, err := OpenPrivateExclusive(path)
	if replacement != nil {
		_ = replacement.Close()
	}
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("collision error = %v, want os.ErrExist", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "original" {
		t.Fatalf("existing private file = %q, %v", content, err)
	}
}

func TestCreatePrivateTempUsesPrefixAndOwnerOnlyMode(t *testing.T) {
	dir := t.TempDir()
	file, err := CreatePrivateTemp(dir, ".kms-private-")
	if err != nil {
		t.Fatalf("CreatePrivateTemp: %v", err)
	}
	path := file.Name()
	defer os.Remove(path)
	defer file.Close()
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != resolvedDir || !strings.HasPrefix(filepath.Base(path), ".kms-private-") {
		t.Fatalf("temporary path = %q", path)
	}
	if runtime.GOOS != "windows" {
		info, err := file.Stat()
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private temporary file mode = %v, %v", info, err)
		}
	}
}

func TestMkdirPrivateTempUsesPrefixAndOwnerOnlyMode(t *testing.T) {
	parent := t.TempDir()
	dir, err := MkdirPrivateTemp(parent, ".kms-private-")
	if err != nil {
		t.Fatalf("MkdirPrivateTemp: %v", err)
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != resolvedParent || !strings.HasPrefix(filepath.Base(dir), ".kms-private-") {
		t.Fatalf("temporary directory = %q", dir)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("private temporary directory mode = %v, %v", info, err)
		}
	}
}
