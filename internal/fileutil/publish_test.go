package fileutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishNoReplace(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	dst := filepath.Join(dir, "published")
	if err := os.WriteFile(staging, []byte("complete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishNoReplace(staging, dst); err != nil {
		t.Fatalf("PublishNoReplace: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "complete" {
		t.Fatalf("published content = %q, %v", got, err)
	}
}

func TestPublishNoReplacePreservesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	dst := filepath.Join(dir, "published")
	if err := os.WriteFile(staging, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishNoReplace(staging, dst); !errors.Is(err, os.ErrExist) {
		t.Fatalf("PublishNoReplace collision = %v, want os.ErrExist", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "existing" {
		t.Fatalf("existing destination = %q, %v", got, err)
	}
}
