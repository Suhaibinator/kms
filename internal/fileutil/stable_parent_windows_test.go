//go:build windows

package fileutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestIsTrustedWindowsOwnerAcceptsExactTrustedInstallerSID(t *testing.T) {
	owner, err := windows.StringToSid(trustedInstallerSIDString)
	if err != nil {
		t.Fatal(err)
	}
	trustedInstaller, err := windows.StringToSid(trustedInstallerSIDString)
	if err != nil {
		t.Fatal(err)
	}
	if !isTrustedWindowsOwner(owner, nil, nil, nil, trustedInstaller) {
		t.Fatal("exact TrustedInstaller service SID was rejected")
	}
}

func TestIsTrustedWindowsOwnerRejectsDifferentServiceSID(t *testing.T) {
	trustedInstaller, err := windows.StringToSid(trustedInstallerSIDString)
	if err != nil {
		t.Fatal(err)
	}
	// This remains a syntactically valid NT SERVICE SID and differs only in the
	// final sub-authority. Service-SID namespace membership alone is not trust.
	otherService, err := windows.StringToSid("S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478465")
	if err != nil {
		t.Fatal(err)
	}
	if isTrustedWindowsOwner(otherService, nil, nil, nil, trustedInstaller) {
		t.Fatal("non-TrustedInstaller service SID was accepted")
	}
}

func TestResolveStablePathRejectsDeleteChildGrant(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	if err := windows.CreateDirectory(windows.StringToUTF16Ptr(dir), nil); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.ACCESS_MASK(windows.GENERIC_ALL),
			AccessMode:        windows.SET_ACCESS,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid)},
		},
		{
			AccessPermissions: fileDeleteChild,
			AccessMode:        windows.SET_ACCESS,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(everyone)},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveStablePath(filepath.Join(dir, "output.db")); err == nil {
		t.Fatal("parent granting Everyone FILE_DELETE_CHILD was accepted")
	}
}

func TestRequireStableParentInspectsReparseEntryDACL(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	createTestDirectoryJunction(t, target, link)
	name, err := windows.UTF16PtrFromString(extendedWindowsPath(link))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		name,
		windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Errorf("close reparse-point handle: %v", err)
		}
	}()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.ACCESS_MASK(windows.GENERIC_ALL),
			AccessMode:        windows.SET_ACCESS,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid)},
		},
		{
			AccessPermissions: windows.ACCESS_MASK(windows.GENERIC_WRITE),
			AccessMode:        windows.SET_ACCESS,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(everyone)},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveStablePath(filepath.Join(link, "output.db")); err == nil {
		t.Fatal("reparse entry granting Everyone GENERIC_WRITE was accepted")
	}
}

// createTestDirectoryJunction deterministically exercises the unprivileged
// reparse-point setup used by CI. Directory symlinks may require Developer Mode
// or SeCreateSymbolicLinkPrivilege; junctions test the same exact-entry DACL
// invariant without making privilege availability a reason to skip.
func createTestDirectoryJunction(t *testing.T, target, link string) {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create test directory junction: %v: %s", err, out)
	}

	name, err := windows.UTF16PtrFromString(extendedWindowsPath(link))
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := windows.GetFileAttributes(name)
	if err != nil {
		t.Fatalf("inspect test reparse point: %v", err)
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		t.Fatalf("test path %s is not a reparse point (attributes %#x)", link, attrs)
	}
	info, err := os.Stat(link)
	if err != nil || !info.IsDir() {
		t.Fatalf("test reparse point does not resolve to its directory target: info=%v err=%v", info, err)
	}
}
