//go:build windows

package fileutil

import (
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

// appendAccess is the least authority an append writer needs: write at the end
// of the file, read back the attributes os.File.Stat reports, and read the DACL
// the privacy checks inspect. SYNCHRONIZE makes the handle usable for the
// synchronous I/O os.File performs.
const appendAccess = windows.FILE_APPEND_DATA | windows.FILE_READ_ATTRIBUTES |
	windows.READ_CONTROL | windows.SYNCHRONIZE

func openPrivateAppendNew(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(extendedWindowsPath(path))
	if err != nil {
		return nil, err
	}
	sa, sd, err := privateSecurityAttributes(false)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		appendAccess,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		sa,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	runtime.KeepAlive(sd)
	if err != nil {
		return nil, &os.PathError{Op: "open-private-append", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

// openPrivateAppendExisting opens the named entry itself rather than a reparse
// point's target, which is the Windows spelling of O_NOFOLLOW: a symlink then
// fails the regular-file check instead of redirecting the append.
func openPrivateAppendExisting(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(extendedWindowsPath(path))
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		appendAccess,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open-private-append", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}
