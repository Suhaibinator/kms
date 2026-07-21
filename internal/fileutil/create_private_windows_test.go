//go:build windows

package fileutil

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsFileAllAccess = windows.ACCESS_MASK(0x001f01ff)

func TestOpenPrivateExclusiveUsesProtectedCurrentUserDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	file, err := OpenPrivateExclusive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close private file: %v", err)
		}
	}()
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
	defer func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Errorf("close private directory handle: %v", err)
		}
	}()
	assertProtectedCurrentUserDACL(t, handle, true)
}

func assertProtectedCurrentUserDACL(t *testing.T, handle windows.Handle, expectInheritance bool) {
	t.Helper()
	sd, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
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
	wantUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	if !isCurrentWindowsUserOwner(owner, wantUser.User.Sid) {
		ownerName := "<none>"
		if owner != nil {
			ownerName = owner.String()
		}
		t.Fatalf("owner = %s, want current user %s", ownerName, wantUser.User.Sid.String())
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
	if ace.Mask != windowsFileAllAccess {
		t.Fatalf("ACE mask = %#x, want FILE_ALL_ACCESS %#x", ace.Mask, windowsFileAllAccess)
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
	gotSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !gotSID.Equals(wantUser.User.Sid) {
		t.Fatalf("only DACL ACE belongs to %s, want current user %s", gotSID.String(), wantUser.User.Sid.String())
	}
}

func TestIsCurrentWindowsUserOwnerRequiresExactSID(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	if !isCurrentWindowsUserOwner(user.User.Sid, user.User.Sid) {
		t.Fatal("current-user owner was rejected")
	}
	if isCurrentWindowsUserOwner(admins, user.User.Sid) {
		t.Fatal("Administrators owner was accepted as the current user")
	}
	if isCurrentWindowsUserOwner(nil, user.User.Sid) {
		t.Fatal("nil owner was accepted as the current user")
	}
}

func TestRestrictOwnerOnlySetsCurrentUserOwnerAndExactDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	if err := os.WriteFile(path, []byte("private"), 0o666); err != nil {
		t.Fatal(err)
	}
	file, err := OpenForOwnerRestriction(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close restricted file: %v", err)
		}
	}()
	if err := RestrictOwnerOnly(file, false); err != nil {
		t.Fatal(err)
	}
	assertProtectedCurrentUserDACL(t, windows.Handle(file.Fd()), false)
}

func TestSecureExistingPrivateFileRejectsAdditionalWindowsACE(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	file, err := OpenPrivateExclusive(path)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windowsFileAllAccess,
			AccessMode:        windows.SET_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
			},
		},
		{
			AccessPermissions: windows.ACCESS_MASK(windows.FILE_READ_DATA),
			AccessMode:        windows.SET_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(everyone),
			},
		},
	}, nil)
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := windows.SetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := SecureExistingPrivateFile(path); err == nil {
		t.Fatal("file granting Everyone read access was accepted as private")
	}
}
