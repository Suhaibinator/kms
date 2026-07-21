//go:build windows

package fileutil

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func requireAlreadyPrivate(file *os.File, _ string) error {
	sd, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("DACL inherits access from its parent")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return fmt.Errorf("file has no restrictive DACL")
	}
	if acl.AceCount != 1 {
		return fmt.Errorf("DACL has %d entries, want exactly one current-user entry", acl.AceCount)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		return err
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
		return fmt.Errorf("DACL does not contain one effective current-user allow entry")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !sid.Equals(user.User.Sid) {
		return fmt.Errorf("DACL grants access to %s instead of the current user", sid.String())
	}
	return nil
}
