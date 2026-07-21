//go:build windows

package storage

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsSQLiteFileURIAndOpenUseExactLocalDrivePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kms db#100%.sqlite")
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	slashPath := filepath.ToSlash(abs)
	uri := sqliteFileURI(slashPath)
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse generated URI: %v", err)
	}
	if parsed.Host != "" {
		t.Fatalf("Windows drive became URI host %q in %q", parsed.Host, uri)
	}
	if parsed.Path != "/"+slashPath {
		t.Fatalf("generated URI path = %q, want %q", parsed.Path, "/"+slashPath)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	if err := ValidateKMSDatabase(path); err != nil {
		t.Fatalf("ValidateKMSDatabase(%q): %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("exact database path was not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("exact database path remained the empty reservation file")
	}
}
