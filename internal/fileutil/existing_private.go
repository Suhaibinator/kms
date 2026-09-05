package fileutil

import (
	"fmt"
	"os"
)

// ValidateExistingPrivateFile validates that path names an already-private,
// current-user-owned regular file beneath an entry-stable parent and returns
// its canonical path without changing the file. The descriptor is compared
// with the directory entry before and after the ownership/access checks so a
// final-component swap cannot make validation apply to a different inode.
func ValidateExistingPrivateFile(path string) (validatedPath string, retErr error) {
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
	file, err := os.Open(stablePath)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := file.Close(); err != nil && retErr == nil {
			validatedPath = ""
			retErr = fmt.Errorf("close validated private file %s: %w", path, err)
		}
	}()

	opened, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return "", fmt.Errorf("%s changed while it was being validated", path)
	}
	if err := requireCurrentUserOwner(file); err != nil {
		return "", fmt.Errorf("validate owner of %s: %w", path, err)
	}
	if err := requireAlreadyPrivate(file, stablePath); err != nil {
		return "", fmt.Errorf("validate existing access to %s: %w", path, err)
	}
	after, err := os.Lstat(stablePath)
	if err != nil {
		return "", err
	}
	if !after.Mode().IsRegular() || !os.SameFile(opened, after) {
		return "", fmt.Errorf("%s changed while it was being validated", path)
	}
	return stablePath, nil
}

// SecureExistingPrivateFile validates that path names an already-private,
// current-user-owned regular file beneath an entry-stable parent, normalizes
// its owner permissions, and returns the canonical path. Broad access is
// rejected rather than repaired: changing an ACL cannot revoke a handle that
// another account may already have opened. The exact opened inode is verified
// against the inspected directory entry before and after normalization.
func SecureExistingPrivateFile(path string) (securedPath string, retErr error) {
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
	defer func() {
		if err := file.Close(); err != nil && retErr == nil {
			securedPath = ""
			retErr = fmt.Errorf("close secured private file %s: %w", path, err)
		}
	}()

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
