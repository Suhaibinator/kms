//go:build darwin

package fileutil

import (
	"fmt"
	"os"
	"strings"
)

func requireAlreadyPrivate(file *os.File, stablePath string) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("mode %04o grants group or other access", info.Mode().Perm())
	}
	acl, err := darwinACLLines(stablePath)
	if err != nil {
		return err
	}
	for _, entry := range acl {
		if strings.Contains(entry, " allow ") {
			return fmt.Errorf("an allow ACL grants additional access")
		}
	}
	return nil
}
