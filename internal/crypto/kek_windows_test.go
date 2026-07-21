//go:build windows

package crypto

import (
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsFileAllAccess = windows.ACCESS_MASK(0x001f01ff)

func TestWriteKEKMaterialFileUsesCurrentUserOwnerAndProtectedDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	material, err := WriteKEKMaterialFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer Zero(material)

	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		name,
		windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Errorf("close KEK file handle: %v", err)
		}
	}()

	sd, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	if owner == nil || !owner.Equals(user.User.Sid) {
		ownerName := "<none>"
		if owner != nil {
			ownerName = owner.String()
		}
		t.Fatalf("KEK owner = %s, want current user %s", ownerName, user.User.Sid.String())
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("KEK DACL control = %#x, want SE_DACL_PROTECTED", control)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if acl == nil || acl.AceCount != 1 {
		t.Fatalf("KEK DACL = %v, want exactly one current-user ACE", acl)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
		t.Fatalf("KEK ACE type/flags = %d/%#x, want effective ACCESS_ALLOWED", ace.Header.AceType, ace.Header.AceFlags)
	}
	if ace.Mask != windowsFileAllAccess {
		t.Fatalf("KEK ACE mask = %#x, want FILE_ALL_ACCESS %#x", ace.Mask, windowsFileAllAccess)
	}
	gotSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !gotSID.Equals(user.User.Sid) {
		t.Fatalf("KEK DACL belongs to %s, want current user %s", gotSID.String(), user.User.Sid.String())
	}
}
