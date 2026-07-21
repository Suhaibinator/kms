//go:build windows

package fileutil

import (
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestOpenPrivateExclusiveUsesProtectedCurrentUserDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	file, err := OpenPrivateExclusive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	assertProtectedCurrentUserDACL(t, windows.Handle(file.Fd()), false)
}

func TestMkdirPrivateTempUsesProtectedCurrentUserDACL(t *testing.T) {
	dir, err := MkdirPrivateTemp(t.TempDir(), ".kms-private-")
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(extendedWindowsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		name,
		windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	assertProtectedCurrentUserDACL(t, handle, true)
}

func assertProtectedCurrentUserDACL(t *testing.T, handle windows.Handle, expectInheritance bool) {
	t.Helper()
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("DACL control = %#x, want SE_DACL_PROTECTED", control)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if acl == nil || acl.AceCount != 1 {
		t.Fatalf("DACL ACE count = %v, want exactly one current-user ACE", acl)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		t.Fatalf("ACE type = %d, want ACCESS_ALLOWED", ace.Header.AceType)
	}
	if ace.Mask&windows.GENERIC_ALL != windows.GENERIC_ALL {
		t.Fatalf("ACE mask = %#x, want GENERIC_ALL", ace.Mask)
	}
	inheritance := uint8(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	inheritanceControl := inheritance | windows.INHERIT_ONLY_ACE | windows.NO_PROPAGATE_INHERIT_ACE
	wantFlags := uint8(0)
	if expectInheritance {
		wantFlags = inheritance
	}
	if got := ace.Header.AceFlags & inheritanceControl; got != wantFlags {
		t.Fatalf("ACE inheritance flags = %#x, want %#x", got, wantFlags)
	}
	wantUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	gotSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !gotSID.Equals(wantUser.User.Sid) {
		t.Fatalf("only DACL ACE belongs to %s, want current user %s", gotSID.String(), wantUser.User.Sid.String())
	}
}
