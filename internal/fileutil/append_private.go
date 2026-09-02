package fileutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// OpenPrivateAppend opens path for appending beneath an entry-stable parent,
// creating it owner-only when it is absent. An existing file must already be a
// regular file owned by the current user with no group or other access; broad
// access is rejected rather than repaired, because changing an ACL cannot
// revoke a handle another account may already have opened. Symlinks are never
// followed, and the exact opened inode is verified against the inspected
// directory entry before and after validation, so a swap between the two
// cannot widen access.
//
// Use it for an append-only artifact that must stay private for its whole life
// — an audit archive, say — where PublishNoReplace's write-then-rename is not
// available because the file is reopened and extended.
func OpenPrivateAppend(path string) (*os.File, error) {
	stablePath, err := ResolveStablePath(path)
	if err != nil {
		return nil, err
	}
	_, err = os.Lstat(stablePath)
	switch {
	case err == nil:
		// Existing entry: validated below.
	case errors.Is(err, fs.ErrNotExist):
		file, createErr := openPrivateAppendNew(stablePath)
		if createErr == nil {
			return file, nil
		}
		if !errors.Is(createErr, fs.ErrExist) {
			return nil, createErr
		}
		// Another writer won the create race; validate what it left behind.
	default:
		return nil, err
	}
	return openExistingPrivateAppend(path, stablePath)
}

// openExistingPrivateAppend opens an entry that already exists and proves it is
// the private, current-user-owned regular file the caller asked for.
func openExistingPrivateAppend(path, stablePath string) (file *os.File, retErr error) {
	before, err := os.Lstat(stablePath)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	opened, err := openPrivateAppendExisting(stablePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if retErr != nil {
			_ = opened.Close()
		}
	}()

	info, err := opened.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return nil, fmt.Errorf("%s changed while it was being opened", path)
	}
	if err := requireCurrentUserOwner(opened); err != nil {
		return nil, fmt.Errorf("validate owner of %s: %w", path, err)
	}
	if err := requireAlreadyPrivate(opened, stablePath); err != nil {
		return nil, fmt.Errorf("validate existing access to %s: %w", path, err)
	}
	after, err := os.Lstat(stablePath)
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(info, after) {
		return nil, fmt.Errorf("%s changed while it was being opened", path)
	}
	return opened, nil
}
