//go:build windows

package fileutil

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func publishNoReplace(staging, dst string) error {
	from, err := windows.UTF16PtrFromString(extendedWindowsPath(staging))
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(extendedWindowsPath(dst))
	if err != nil {
		return err
	}
	// MoveFile without MOVEFILE_REPLACE_EXISTING fails when dst exists.
	if err := windows.MoveFile(from, to); err != nil {
		return &os.LinkError{Op: "rename-noreplace", Old: staging, New: dst, Err: err}
	}
	return nil
}

// extendedWindowsPath mirrors the essential long-path normalization used by
// the os package before calling MoveFileW/CreateFileW directly.
func extendedWindowsPath(path string) string {
	if strings.HasPrefix(path, `\\?\`) {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	abs = filepath.Clean(abs)
	if strings.HasPrefix(abs, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(abs, `\\`)
	}
	return `\\?\` + abs
}
