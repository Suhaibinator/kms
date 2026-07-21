//go:build darwin

package fileutil

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

func requireStableDirectoryChain(path string) error {
	euid := uint32(os.Geteuid())
	for _, current := range stablePathChain(path) {
		info, err := os.Stat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", current)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot inspect owner of %s", current)
		}
		if stat.Uid != 0 && stat.Uid != euid {
			return fmt.Errorf("%s is owned by uid %d", current, stat.Uid)
		}
		if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("%s is group- or other-writable without the sticky bit", current)
		}
		acl, aclErr := darwinACLLines(current)
		if aclErr != nil {
			return fmt.Errorf("inspect ACL on %s: %w", current, aclErr)
		}
		for _, entry := range acl {
			if strings.Contains(entry, " allow ") {
				// Deny-only ACLs (including the default macOS home-directory
				// delete guard) add no authority. Any allow ACE anywhere in the
				// canonical chain is rejected: it may grant entry mutation or be
				// inherited by a nominally 0600/0700 child despite its mode bits.
				return fmt.Errorf("%s has an allow ACL", current)
			}
		}
	}
	return nil
}

func requireStablePathSpelling(path string) error {
	euid := uint32(os.Geteuid())
	for _, current := range stablePathChain(path) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot inspect owner of %s", current)
		}
		if stat.Uid != 0 && stat.Uid != euid {
			return fmt.Errorf("%s is owned by uid %d", current, stat.Uid)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory or trusted symlink", current)
		}
		if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("%s is group- or other-writable without the sticky bit", current)
		}
		acl, err := darwinACLLines(current)
		if err != nil {
			return fmt.Errorf("inspect ACL on %s: %w", current, err)
		}
		for _, entry := range acl {
			if strings.Contains(entry, " allow ") {
				return fmt.Errorf("%s has an allow ACL", current)
			}
		}
	}
	return nil
}
