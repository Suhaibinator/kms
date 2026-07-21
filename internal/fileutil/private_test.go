//go:build !windows

package fileutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestrictOwnerOnlyPOSIXModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := RestrictOwnerOnly(dirFile, true); err != nil {
		t.Fatal(err)
	}
	_ = dirFile.Close()
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %v, %v", info, err)
	}

	path := filepath.Join(dir, "secret")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	if err := RestrictOwnerOnly(file, false); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, %v", info, err)
	}
}
