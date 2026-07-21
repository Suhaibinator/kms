package fileutil

import "os"

// RestrictOwnerOnly removes inherited broad access from an already-open file
// or directory. On POSIX this applies 0600/0700; on Windows it installs a
// protected DACL granting full access only to the process user. Call it before
// writing sensitive content so inherited parent permissions never create an
// exposure window.
func RestrictOwnerOnly(file *os.File, directory bool) error {
	return restrictOwnerOnly(file, directory)
}

// OpenForOwnerRestriction opens an existing file with the platform-specific
// handle rights needed by RestrictOwnerOnly. Keeping the same open handle for
// the ACL change prevents a pathname swap between open and restriction.
func OpenForOwnerRestriction(path string) (*os.File, error) {
	stablePath, err := ResolveStablePath(path)
	if err != nil {
		return nil, err
	}
	return openForOwnerRestriction(stablePath)
}
