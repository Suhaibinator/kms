//go:build windows

package fileutil

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func requireCurrentUserOwner(file *os.File) error {
	sd, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(user.User.Sid) {
		ownerName := "<none>"
		if owner != nil {
			ownerName = owner.String()
		}
		return fmt.Errorf("file is owned by %s, current user is %s", ownerName, user.User.Sid.String())
	}
	return nil
}
