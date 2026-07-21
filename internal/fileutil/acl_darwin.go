//go:build darwin

package fileutil

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var darwinACLEntryRE = regexp.MustCompile(`^[[:space:]]*[0-9]+:`)

// darwinACLLines reads an extended ACL from a canonical, entry-stable path.
// macOS hides ACLs from ordinary xattr APIs. /bin/ls is invoked by absolute
// path under the C locale and any output shape other than one stat line plus
// recognized numbered ACL lines fails closed.
func darwinACLLines(path string) ([]string, error) {
	cmd := exec.Command("/bin/ls", "-lde", "--", path)
	cmd.Env = darwinCommandEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("inspect ACL: %w: %s", err, strings.TrimSpace(string(out)))
	}
	lines := bytes.Split(bytes.TrimSpace(out), []byte{'\n'})
	if len(lines) == 0 || len(bytes.TrimSpace(lines[0])) == 0 {
		return nil, fmt.Errorf("inspect ACL: empty ls output")
	}
	acl := make([]string, 0)
	for _, line := range lines[1:] {
		text := string(line)
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if !darwinACLEntryRE.MatchString(text) {
			return nil, fmt.Errorf("inspect ACL: unrecognized ls output %q", text)
		}
		acl = append(acl, strings.TrimSpace(text))
	}
	return acl, nil
}

func clearDarwinACL(file *os.File) error {
	cmd := exec.Command("/bin/chmod", "-N", "/dev/fd/3")
	cmd.ExtraFiles = []*os.File{file}
	cmd.Env = darwinCommandEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("remove ACL: %w: %s", err, strings.TrimSpace(string(out)))
	}
	stablePath, err := ResolveStablePath(file.Name())
	if err != nil {
		return err
	}
	handleInfo, err := file.Stat()
	if err != nil {
		return err
	}
	pathInfo, err := os.Stat(stablePath)
	if err != nil {
		return err
	}
	if !os.SameFile(handleInfo, pathInfo) {
		return fmt.Errorf("private path changed during ACL removal")
	}
	acl, err := darwinACLLines(stablePath)
	if err != nil {
		return err
	}
	if len(acl) != 0 {
		return fmt.Errorf("extended ACL remains after removal")
	}
	return nil
}

func darwinCommandEnv() []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "LC_ALL=") || strings.HasPrefix(entry, "LANG=") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "LC_ALL=C", "LANG=C")
}
