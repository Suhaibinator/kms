package fileutil

import (
	"fmt"
	"os"
)

// SecureExistingPrivateFile validates that path names an already-private,
// current-user-owned regular file beneath an entry-stable parent, normalizes
// its owner permissions, and returns the canonical path. Broad access is
// rejected rather than repaired: changing an ACL cannot revoke a handle that
// another account may already have opened. The exact opened inode is verified
// against the inspected directory entry before and after normalization.
func SecureExistingPrivateFile(path string) (string, error) {
	stablePath, err := ResolveStablePath(path)
	if err != nil {
		return "", err
	}
	before, err := os.Lstat(stablePath)
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	file, err := openForOwnerRestriction(stablePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	opened, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return "", fmt.Errorf("%s changed while it was being secured", path)
	}
	if err := requireCurrentUserOwner(file); err != nil {
		return "", fmt.Errorf("validate owner of %s: %w", path, err)
	}
	if err := requireAlreadyPrivate(file, stablePath); err != nil {
		return "", fmt.Errorf("validate existing access to %s: %w", path, err)
	}
	if err := restrictOwnerOnly(file, false); err != nil {
		return "", fmt.Errorf("restrict access to %s: %w", path, err)
	}
	after, err := os.Lstat(stablePath)
	if err != nil {
		return "", err
	}
	if !after.Mode().IsRegular() || !os.SameFile(opened, after) {
		return "", fmt.Errorf("%s changed while it was being secured", path)
	}
	return stablePath, nil
}
