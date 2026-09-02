package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/Suhaibinator/kms/internal/envinject"
)

// Exit codes exec uses when the command itself could not be started; they
// match the shell's conventions so wrappers and supervisors read them the
// same way they read sh's.
const (
	exitExecNotExecutable = 126
	exitExecNotFound      = 127
)

// launchFunc replaces the CLI with the command (or, where the platform
// cannot, runs it to completion and returns its exit code). It only returns
// when the command could not be started or has exited.
type launchFunc func(argv, env []string) (int, error)

// splitExecArgs separates exec's own arguments from the command after the
// first "--". The command is never handed to the flag parser, so its flags
// (sh -c, env -i) are passed through verbatim.
func splitExecArgs(args []string) (own, command []string, ok bool) {
	if i := slices.Index(args, "--"); i >= 0 {
		return args[:i], args[i+1:], true
	}
	return args, nil, false
}

func (c *CLI) cmdExec(args []string) int {
	own, command, hasSep := splitExecArgs(args)
	fs := c.newFlags("exec")
	cf := addConnFlags(c, fs)
	var sel envSelection
	addEnvSelectionFlags(fs, &sel)
	preserveEnv := fs.Bool("preserve-env", false, "let an existing environment variable win over an injected one of the same name (the shadowed names are reported)")
	c.setUsage(fs, "exec ENV/APP [flags] -- COMMAND [ARGS...]",
		"Run COMMAND with the namespace's parameters and secrets injected as environment variables. The CLI resolves every value first, then replaces itself with COMMAND (on Unix), so signals and the exit status pass straight through. Injected variables win over the parent environment unless --preserve-env is given; KMS_SECRET_TOKEN_* variables never reach the command. Prefer --release NAME in production: the values are then the exact, digest-verified versions the active release pins.", false)
	if !c.parseFlags(fs, own) {
		return 2
	}
	pos := c.args()
	if len(pos) < 1 || pos[0] == "" {
		return c.failUsage("exec requires an env/app namespace argument")
	}
	if !c.rejectExtraPositionals(1) {
		return 2
	}
	if !hasSep || len(command) == 0 || command[0] == "" {
		return c.failUsage("exec requires a command after \"--\": exec %s [flags] -- COMMAND [ARGS...]", pos[0])
	}
	ns, err := parseNamespaceProto(pos[0])
	if err != nil {
		return c.failUsage("invalid namespace: %v", err)
	}
	if err := sel.validate(); err != nil {
		return c.failUsage("%v", err)
	}

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	ctx, cancel := callContext()
	res, err := c.resolveEnvironment(ctx, conn, cf, ns, &sel)
	cancel()
	// The connection is closed before the launch, not deferred: on Unix the
	// process image is replaced and deferred calls never run.
	_ = conn.Close()
	if err != nil {
		return c.failErr("exec", err)
	}
	c.printResolutionNotes(res)

	env, shadowed := envinject.Merge(c.environ(), res.vars, *preserveEnv, caseInsensitiveEnv())
	for _, name := range shadowed {
		c.info("note: %s is already set and kept (--preserve-env); the store's value was not injected", name)
	}
	code, err := c.launcher()(command, env)
	if err != nil {
		_, _ = fmt.Fprintf(c.Stderr, "error: exec %s: %v\n", command[0], err)
	}
	return code
}

// environ returns the parent environment for the command, from the test
// override when set.
func (c *CLI) environ() []string {
	if c.environOverride != nil {
		return c.environOverride()
	}
	return os.Environ()
}

// launcher returns the process launcher, from the test override when set.
func (c *CLI) launcher() launchFunc {
	if c.launchOverride != nil {
		return c.launchOverride
	}
	return launchProcess
}

// resolveCommand locates the executable for argv[0] the way a shell would,
// except that a bare name is never resolved from the current directory (Go's
// exec.ErrDot), since exec typically runs in a checkout or a service's
// working directory that other users may write to.
func resolveCommand(name string) (string, int, error) {
	path, err := exec.LookPath(name)
	switch {
	case err == nil:
		return path, 0, nil
	case errors.Is(err, exec.ErrDot):
		return "", exitExecNotFound, fmt.Errorf("%q resolves to the current directory only; run it as ./%s or give its path", name, name)
	case errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist):
		return "", exitExecNotFound, fmt.Errorf("command not found")
	case errors.Is(err, os.ErrPermission):
		return "", exitExecNotExecutable, fmt.Errorf("permission denied")
	}
	// A path that exists but is not executable (or is a directory) is the
	// classic 126 case; anything else is reported as-is.
	if strings.Contains(err.Error(), "permission denied") {
		return "", exitExecNotExecutable, err
	}
	return "", exitExecNotFound, err
}
