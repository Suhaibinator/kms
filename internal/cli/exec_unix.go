//go:build unix

package cli

import (
	"errors"
	"fmt"
	"syscall"
)

// maxEnvEntryBytes caps one "NAME=value" entry. Linux rejects a single
// argument or environment string over MAX_ARG_STRLEN (128 KiB) with E2BIG;
// the CLI reports the offending variable by name instead.
const maxEnvEntryBytes = 128 << 10

// launchProcess replaces the current process with argv. On success it never
// returns: the command inherits the process ID, the standard streams and the
// signal disposition, so a supervisor sees the command itself, and its exit
// status is the exec's. It returns only when the command could not be
// started.
func launchProcess(argv, env []string) (int, error) {
	path, code, err := resolveCommand(argv[0])
	if err != nil {
		return code, err
	}
	err = syscall.Exec(path, argv, env)
	// Exec only returns on failure.
	switch {
	case errors.Is(err, syscall.ENOENT):
		return exitExecNotFound, fmt.Errorf("command not found")
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.ENOEXEC), errors.Is(err, syscall.EISDIR):
		return exitExecNotExecutable, fmt.Errorf("cannot execute %s: %v", path, err)
	case errors.Is(err, syscall.E2BIG):
		return exitExecNotExecutable, fmt.Errorf("argument list or environment too large for the kernel: %v", err)
	default:
		return exitExecNotExecutable, fmt.Errorf("cannot execute %s: %v", path, err)
	}
}
