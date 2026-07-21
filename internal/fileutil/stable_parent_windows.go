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
	trustedInstallerSIDString          = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
)

func requireStableDirectoryChain(path string) error {
	return requireStableWindowsChain(path, false)
}

func requireStablePathSpelling(path string) error {
	// Inspect each caller-visible component through an exact reparse-point
	// handle. Named security queries may follow a junction/symlink and report
	// only its target, leaving the mutable entry itself unchecked.
	return requireStableWindowsChain(path, true)
}

func requireStableWindowsChain(path string, allowTrustedReparse bool) error {
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
	trustedInstaller, err := windows.StringToSid(trustedInstallerSIDString)
	if err != nil {
		return fmt.Errorf("parse TrustedInstaller SID: %w", err)
	}
	trusted := func(sid *windows.SID) bool {
		return isTrustedWindowsOwner(sid, user.User.Sid, system, admins, trustedInstaller)
	}

	for _, current := range stablePathChain(path) {
		name, err := windows.UTF16PtrFromString(extendedWindowsPath(current))
		if err != nil {
			return err
		}
		handle, err := windows.CreateFile(
			name,
			windows.READ_CONTROL,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
			0,
		)
		if err != nil {
			return fmt.Errorf("open exact path component %s: %w", current, err)
		}
		var fileInfo windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(handle, &fileInfo); err != nil {
			_ = windows.CloseHandle(handle)
			return fmt.Errorf("inspect exact path component %s: %w", current, err)
		}
		isReparse := fileInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
		if !allowTrustedReparse && isReparse {
			_ = windows.CloseHandle(handle)
			return fmt.Errorf("resolved path component %s became a reparse point", current)
		}
		sd, err := windows.GetSecurityInfo(
			handle,
			windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
		)
		if closeErr := windows.CloseHandle(handle); closeErr != nil {
			return fmt.Errorf("close exact path component %s: %w", current, closeErr)
		}
		if err != nil {
			return fmt.Errorf("inspect DACL on exact path component %s: %w", current, err)
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
			if isReparse {
				// A junction/symlink can be retargeted in place through
				// FSCTL_SET_REPARSE_POINT; deleting the directory entry is not
				// required. Reject every untrusted write-shaped grant on the exact
				// reparse entry, including an unmapped generic ACE.
				dangerous |= windows.ACCESS_MASK(windows.GENERIC_WRITE |
					windows.FILE_WRITE_DATA |
					windows.FILE_APPEND_DATA |
					windows.FILE_WRITE_EA |
					windows.FILE_WRITE_ATTRIBUTES)
			}
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
				return fmt.Errorf("%s grants path-mutation rights to untrusted SID %s", current, sid.String())
			}
		}
	}
	return nil
}

func isTrustedWindowsOwner(sid, user, system, admins, trustedInstaller *windows.SID) bool {
	if sid == nil {
		return false
	}
	for _, trusted := range []*windows.SID{user, system, admins, trustedInstaller} {
		if trusted != nil && sid.Equals(trusted) {
			return true
		}
	}
	return false
}
