package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// stdinIsTTY reports whether stdin is an interactive terminal. A nil or
// non-terminal stdin (a pipe, a file, a test buffer) is non-interactive.
func (c *CLI) stdinIsTTY() bool {
	if c.isTTY != nil {
		return c.isTTY()
	}
	return c.Stdin != nil && term.IsTerminal(int(c.Stdin.Fd()))
}

// confirmDestructive gates an irreversible command on the operator retyping
// the resource it targets, which forces a second look at the target rather
// than a reflexive "y". --yes skips the prompt. On a non-interactive stdin
// without --yes the command is refused with the usage exit code, so a script
// that forgot --yes fails loudly instead of hanging or proceeding. action is
// a verb phrase ("delete namespace"); resource is the exact string the
// operator must type.
func (c *CLI) confirmDestructive(action, resource string) (ok bool, code int) {
	if c.assumeYes {
		return true, exitOK
	}
	if !c.stdinIsTTY() {
		_, _ = fmt.Fprintf(c.Stderr, "error: refusing to %s %s without --yes on a non-interactive stdin\n", action, resource)
		return false, exitUsage
	}
	_, _ = fmt.Fprintf(c.Stderr, "This will %s %s. This cannot be undone.\nType %q to confirm: ", action, resource, resource)
	typed, err := c.readLine()
	if err != nil {
		_, _ = fmt.Fprintf(c.Stderr, "\nerror: reading confirmation: %v\n", err)
		return false, exitUsage
	}
	if typed != resource {
		_, _ = fmt.Fprintf(c.Stderr, "error: confirmation %q does not match %q; aborted\n", typed, resource)
		return false, exitUsage
	}
	return true, exitOK
}

// confirmYesNo asks a yes/no question for a command whose effect the caller
// has just previewed on stderr. The default is no. --yes answers yes; a
// non-interactive stdin without --yes is refused with the usage exit code.
func (c *CLI) confirmYesNo(action string) (ok bool, code int) {
	if c.assumeYes {
		return true, exitOK
	}
	if !c.stdinIsTTY() {
		_, _ = fmt.Fprintf(c.Stderr, "error: refusing to %s without --yes on a non-interactive stdin\n", action)
		return false, exitUsage
	}
	_, _ = fmt.Fprintf(c.Stderr, "%s? [y/N]: ", strings.ToUpper(action[:1])+action[1:])
	typed, err := c.readLine()
	if err != nil {
		_, _ = fmt.Fprintf(c.Stderr, "\nerror: reading confirmation: %v\n", err)
		return false, exitUsage
	}
	switch strings.ToLower(typed) {
	case "y", "yes":
		return true, exitOK
	default:
		_, _ = fmt.Fprintln(c.Stderr, "aborted")
		return false, exitUsage
	}
}

// readLine reads one line from stdin without the trailing newline.
func (c *CLI) readLine() (string, error) {
	if c.Stdin == nil {
		return "", os.ErrInvalid
	}
	line, err := bufio.NewReader(c.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
