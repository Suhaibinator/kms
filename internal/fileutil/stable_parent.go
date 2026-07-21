package fileutil

import (
	"fmt"
	"path/filepath"
)

// RequireStableParent rejects a destination whose directory entries can be
// renamed or replaced by another OS account. Sensitive pathname-based creators
// such as SQLite VACUUM INTO cannot be made safe by chmodding the child after
// creation: the parent entry must remain stable for the whole operation.
func RequireStableParent(path string) error {
	_, err := ResolveStablePath(path)
	return err
}

// ResolveStablePath validates path's parent and returns the same basename under
// the canonical validated parent. Callers must perform subsequent pathname
// operations on the returned path; using the pre-resolution spelling would
// reintroduce a symlink-swap race after validation.
func ResolveStablePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("destination path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve destination path: %w", err)
	}
	parent := filepath.Dir(abs)
	// Validate the caller's spelling before following symlinks. This rejects an
	// attacker-owned component beneath a sticky/shared ancestor; checking only
	// the resolved target could otherwise be raced by swapping that component
	// during EvalSymlinks.
	if err := requireStablePathSpelling(parent); err != nil {
		return "", fmt.Errorf("unsafe destination spelling for %s: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve destination parent %s: %w", parent, err)
	}
	if err := requireStableDirectoryChain(resolved); err != nil {
		return "", fmt.Errorf("unsafe destination parent for %s: %w; use a directory whose entries are writable only by the service account", path, err)
	}
	return filepath.Join(resolved, filepath.Base(abs)), nil
}

// stablePathChain returns path and all of its ancestors ordered root-first so
// callers establish the stability of a parent before trusting its child entry.
func stablePathChain(path string) []string {
	chain := make([]string, 0, 8)
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		chain = append(chain, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	for left, right := 0, len(chain)-1; left < right; left, right = left+1, right-1 {
		chain[left], chain[right] = chain[right], chain[left]
	}
	return chain
}
