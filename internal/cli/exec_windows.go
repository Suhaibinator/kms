//go:build windows

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
)

// maxEnvEntryBytes caps one "NAME=value" entry. Windows limits an environment
// variable's value to 32 KiB; the CLI reports the offending variable by name
// instead of letting CreateProcess fail opaquely.
const maxEnvEntryBytes = 32 << 10

// launchProcess runs argv as a child with the standard streams inherited and
// returns its exit code. Windows has no exec(2); the parent stays alive as a
// thin wrapper, ignoring Ctrl-C so that the console event reaches the child
// alone and the wrapper reports the child's exit status rather than dying
// first.
func launchProcess(argv, env []string) (int, error) {
	path, code, err := resolveCommand(argv[0])
	if err != nil {
		return code, err
	}
	signal.Ignore(os.Interrupt)
	cmd := exec.Command(path, argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return exitExecNotExecutable, fmt.Errorf("cannot execute %s: %v", path, err)
	}
	return cmd.ProcessState.ExitCode(), nil
}
