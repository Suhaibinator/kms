//go:build windows

package fileutil

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	accessAllowedObjectACEType         = 5
	accessAllowedCallbackACEType       = 9
	accessAllowedCallbackObjectACEType = 11
	fileDeleteChild                    = windows.ACCESS_MASK(0x00000040)
)

func requireStableDirectoryChain(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	trusted := func(sid *windows.SID) bool {
		return sid != nil && (sid.Equals(user.User.Sid) || sid.Equals(system) || sid.Equals(admins))
	}

	for _, current := range stablePathChain(path) {
		sd, err := windows.GetNamedSecurityInfo(
			extendedWindowsPath(current),
			windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
		)
		if err != nil {
			return fmt.Errorf("inspect DACL on %s: %w", current, err)
		}
		owner, _, err := sd.Owner()
		if err != nil {
			return fmt.Errorf("inspect owner of %s: %w", current, err)
		}
		if !trusted(owner) {
			ownerName := "<none>"
			if owner != nil {
				ownerName = owner.String()
			}
			return fmt.Errorf("%s is owned by an untrusted SID %s", current, ownerName)
		}
		acl, _, err := sd.DACL()
		if err != nil || acl == nil {
			return fmt.Errorf("%s has no restrictive DACL", current)
		}
		for i := uint16(0); i < acl.AceCount; i++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(acl, uint32(i), &ace); err != nil {
				return fmt.Errorf("inspect DACL entry on %s: %w", current, err)
			}
			if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
				continue
			}
			dangerous := windows.ACCESS_MASK(windows.DELETE|windows.WRITE_DAC|windows.WRITE_OWNER|windows.GENERIC_ALL) | fileDeleteChild
			if ace.Mask&dangerous == 0 {
				continue
			}
			switch ace.Header.AceType {
			case windows.ACCESS_ALLOWED_ACE_TYPE:
				// Continue below.
			case accessAllowedObjectACEType, accessAllowedCallbackACEType, accessAllowedCallbackObjectACEType:
				return fmt.Errorf("%s has an unsupported allow ACE type %d", current, ace.Header.AceType)
			default:
				continue // deny/audit ACEs do not grant entry mutation
			}
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if !trusted(sid) {
				return fmt.Errorf("%s grants delete or ACL-control rights to untrusted SID %s", current, sid.String())
			}
		}
	}
	return nil
}

func requireStablePathSpelling(path string) error {
	// Inspect the caller-visible chain before resolving reparse points, then the
	// canonical target chain is inspected separately by ResolveStablePath.
	return requireStableDirectoryChain(path)
}
