package fileutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveStablePathAcceptsPrivateDirectory(t *testing.T) {
	dir := t.TempDir()
	if _, err := ResolveStablePath(filepath.Join(dir, "output.db")); err != nil {
		t.Fatalf("ResolveStablePath: %v", err)
	}
}

func TestResolveStablePathRejectsSharedMutableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows DACL rejection is covered by its platform test")
	}
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveStablePath(filepath.Join(dir, "output.db")); err == nil {
		t.Fatal("group/other-writable non-sticky parent was accepted")
	}
}

func TestResolveStablePathAcceptsOwnedStickyDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sticky directories do not apply on Windows")
	}
	dir := filepath.Join(t.TempDir(), "sticky")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveStablePath(filepath.Join(dir, "output.db")); err != nil {
		t.Fatalf("owned sticky parent: %v", err)
	}
}
