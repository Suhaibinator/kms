//go:build windows

package fileutil

import (
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func privateSecurityAttributes(directory bool) (*windows.SecurityAttributes, *windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, nil, err
	}
	inherit := ""
	if directory {
		inherit = "OICI"
	}
	userSID := user.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString("O:" + userSID + "D:P(A;" + inherit + ";GA;;;" + userSID + ")")
	if err != nil {
		return nil, nil, err
	}
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	return sa, sd, nil
}

func openPrivateExclusive(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(extendedWindowsPath(path))
	if err != nil {
		return nil, err
	}
	sa, sd, err := privateSecurityAttributes(false)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.WRITE_DAC|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		sa,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	runtime.KeepAlive(sd)
	if err != nil {
		return nil, &os.PathError{Op: "open-private", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

func mkdirPrivateExclusive(path string) error {
	name, err := windows.UTF16PtrFromString(extendedWindowsPath(path))
	if err != nil {
		return err
	}
	sa, sd, err := privateSecurityAttributes(true)
	if err != nil {
		return err
	}
	err = windows.CreateDirectory(name, sa)
	runtime.KeepAlive(sd)
	if err != nil {
		return &os.PathError{Op: "mkdir-private", Path: path, Err: err}
	}
	return nil
}
