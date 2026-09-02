package fileutil

import (
	"fmt"
	"io"
	"os"
)

// MaxPrivateFileBytes bounds what ReadPrivateFile will load. Credential files
// are a few hundred bytes; the cap only stops a misdirected path (a log, a
// database) from being slurped into memory.
const MaxPrivateFileBytes = 64 << 10

// ReadPrivateFile reads a file that must already be private to the current
// user: a regular, current-user-owned file with no group or other access,
// beneath an entry-stable parent. Unlike SecureExistingPrivateFile it opens
// the file read-only and never changes its mode, so a 0400 credential, or one
// on a read-only mount, is accepted and a read leaves the file untouched. The
// opened inode is verified against the inspected directory entry so a swap
// between the checks and the read is detected rather than followed.
func ReadPrivateFile(path string) (data []byte, retErr error) {
	stablePath, err := ResolveStablePath(path)
	if err != nil {
		return nil, err
	}
	before, err := os.Lstat(stablePath)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	file, err := os.Open(stablePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); err != nil && retErr == nil {
			data = nil
			retErr = fmt.Errorf("close private file %s: %w", path, err)
		}
	}()

	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("%s changed while it was being read", path)
	}
	if err := requireCurrentUserOwner(file); err != nil {
		return nil, fmt.Errorf("validate owner of %s: %w", path, err)
	}
	if err := requireAlreadyPrivate(file, stablePath); err != nil {
		return nil, fmt.Errorf("validate existing access to %s: %w", path, err)
	}
	if opened.Size() > MaxPrivateFileBytes {
		return nil, fmt.Errorf("%s is %d bytes, larger than the %d-byte limit", path, opened.Size(), MaxPrivateFileBytes)
	}
	data, err = io.ReadAll(io.LimitReader(file, MaxPrivateFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > MaxPrivateFileBytes {
		return nil, fmt.Errorf("%s grew past the %d-byte limit while being read", path, MaxPrivateFileBytes)
	}
	return data, nil
}
