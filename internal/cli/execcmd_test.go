package cli

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

// --- helpers ----------------------------------------------------------------

// launchRecord is what the launcher was asked to run.
type launchRecord struct {
	called bool
	argv   []string
	env    []string
}

// execFixture is the env fixture plus a captured launcher, so a test asserts
// on the child's argv and environment without starting a process.
type execFixture struct {
	*envFixture
	launched launchRecord
}

// newExecFixture wires the standard namespace to a recording launcher and a
// fixed parent environment. The parent deliberately carries per-secret and
// binding credentials plus an identity token: only the identity token survives.
func newExecFixture(t *testing.T, code int, err error) *execFixture {
	t.Helper()
	f := &execFixture{envFixture: newEnvFixture(t)}
	f.environOverride = func() []string {
		return []string{
			"PATH=/usr/bin",
			"HOME=/home/app",
			"KMS_TOKEN=id-token",
			"KMS_SECRET_TOKEN_STRIPE_KEY=" + envTestStripeToken,
			"KMS_SECRET_TOKEN_FILE=/run/secrets/stripe",
			bindingKeyEnv + "=" + testOldBindingKey,
			newBindingKeyEnv + "=" + testNewBindingKey,
		}
	}
	f.launchOverride = func(argv, env []string) (int, error) {
		f.launched = launchRecord{called: true, argv: append([]string(nil), argv...), env: append([]string(nil), env...)}
		return code, err
	}
	return f
}

// runExec invokes `exec prod/app <args> -- <command>`.
func (f *envFixture) runExec(args []string, command ...string) int {
	full := append([]string{"exec", "prod/app", "--insecure", "--token", "id-token"}, args...)
	full = append(full, "--")
	return f.Run(append(full, command...))
}

// --- argument splitting -----------------------------------------------------

// TestSplitExecArgs: the command is cut off before the flag parser ever sees
// it, so its own flags (sh -c, env -i, a second --) survive verbatim.
func TestSplitExecArgs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		args        []string
		wantOwn     []string
		wantCommand []string
		wantOK      bool
	}{
		{name: "no arguments", args: nil, wantOwn: nil, wantCommand: nil, wantOK: false},
		{
			name:    "no separator",
			args:    []string{"prod/app", "--strict"},
			wantOwn: []string{"prod/app", "--strict"},
			wantOK:  false,
		},
		{
			name:        "separator with a command",
			args:        []string{"prod/app", "--strict", "--", "echo", "hi"},
			wantOwn:     []string{"prod/app", "--strict"},
			wantCommand: []string{"echo", "hi"},
			wantOK:      true,
		},
		{
			name:        "separator first",
			args:        []string{"--", "echo"},
			wantOwn:     []string{},
			wantCommand: []string{"echo"},
			wantOK:      true,
		},
		{
			name:        "trailing separator",
			args:        []string{"prod/app", "--"},
			wantOwn:     []string{"prod/app"},
			wantCommand: []string{},
			wantOK:      true,
		},
		{
			name:        "only the first separator splits",
			args:        []string{"prod/app", "--", "env", "--", "sh", "-c", "echo $DB_HOST"},
			wantOwn:     []string{"prod/app"},
			wantCommand: []string{"env", "--", "sh", "-c", "echo $DB_HOST"},
			wantOK:      true,
		},
		{
			name:        "command flags are not exec flags",
			args:        []string{"prod/app", "--", "sh", "-c", "id", "--strict", "--release", "x"},
			wantOwn:     []string{"prod/app"},
			wantCommand: []string{"sh", "-c", "id", "--strict", "--release", "x"},
			wantOK:      true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			own, command, ok := splitExecArgs(tc.args)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if strings.Join(own, "\x00") != strings.Join(tc.wantOwn, "\x00") {
				t.Fatalf("own = %q, want %q", own, tc.wantOwn)
			}
			if strings.Join(command, "\x00") != strings.Join(tc.wantCommand, "\x00") {
				t.Fatalf("command = %q, want %q", command, tc.wantCommand)
			}
		})
	}
}

// TestExecRequiresACommand: without a command there is nothing to run, and a
// silently successful no-op would look like the workload started.
func TestExecRequiresACommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "no separator",
			args: []string{"exec", "prod/app", "--insecure"},
			want: "exec requires a command after \"--\"",
		},
		{
			name: "separator with nothing after it",
			args: []string{"exec", "prod/app", "--insecure", "--"},
			want: "exec requires a command after \"--\"",
		},
		{
			name: "empty command",
			args: []string{"exec", "prod/app", "--insecure", "--", ""},
			want: "exec requires a command after \"--\"",
		},
		{
			name: "no namespace",
			args: []string{"exec", "--insecure", "--", "echo"},
			want: "exec requires an env/app namespace argument",
		},
		{
			name: "invalid namespace",
			args: []string{"exec", "prod", "--insecure", "--", "echo"},
			want: "invalid namespace",
		},
		{
			name: "extra positional",
			args: []string{"exec", "prod/app", "extra", "--insecure", "--", "echo"},
			want: "unexpected argument",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newExecFixture(t, 0, nil)
			if code := f.Run(tc.args); code != exitUsage {
				t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitUsage, f.stderr())
			}
			if !strings.Contains(f.stderr(), tc.want) {
				t.Fatalf("stderr = %s, want %q", f.stderr(), tc.want)
			}
			if f.launched.called {
				t.Fatal("a usage error still launched a process")
			}
			if len(f.rec.snapshot()) != 0 {
				t.Fatalf("a usage error still made RPCs: %+v", f.rec.snapshot())
			}
		})
	}
}

// --- the child environment --------------------------------------------------

// TestExecInjectsTheMergedEnvironment: the child sees its parent's environment
// plus the store's values, with the injected names last and sorted, and with
// every per-secret or binding credential removed.
func TestExecInjectsTheMergedEnvironment(t *testing.T) {
	t.Parallel()
	f := newExecFixture(t, 0, nil)
	code := f.runExec(
		[]string{"--secret-token", "stripe-key=" + envTestStripeToken},
		"/usr/bin/app", "--serve", "--port=8080",
	)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
	}
	if !f.launched.called {
		t.Fatal("the launcher was never called")
	}
	wantArgv := []string{"/usr/bin/app", "--serve", "--port=8080"}
	if strings.Join(f.launched.argv, "\x00") != strings.Join(wantArgv, "\x00") {
		t.Fatalf("argv = %q, want %q", f.launched.argv, wantArgv)
	}
	wantEnv := []string{
		// Surviving parent entries, in their original order...
		"PATH=/usr/bin",
		"HOME=/home/app",
		"KMS_TOKEN=id-token",
		// ...then the injected variables, sorted by name.
		"DB_HOST=" + envTestHostValue,
		"GREETING=" + envTestGreetValue,
		"SESSION_SECRET=" + envTestSessionValue,
		"STRIPE_KEY=" + envTestStripeValue,
	}
	if strings.Join(f.launched.env, "\x00") != strings.Join(wantEnv, "\x00") {
		t.Fatalf("env = %q, want %q", f.launched.env, wantEnv)
	}
}

// TestExecStripsSecretTokensFromTheChild states the rule on its own: the
// identity token is inherited (the child may want to call the store itself),
// the per-secret tokens are not.
func TestExecStripsSecretTokensFromTheChild(t *testing.T) {
	t.Parallel()
	f := newExecFixture(t, 0, nil)
	if code := f.runExec([]string{"--no-secrets"}, "/usr/bin/app"); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
	}
	for _, entry := range f.launched.env {
		if strings.HasPrefix(entry, secretTokenEnvPrefix) {
			t.Fatalf("child environment carries %q", entry)
		}
		name, _, _ := strings.Cut(entry, "=")
		if name == bindingKeyEnv || name == newBindingKeyEnv {
			t.Fatalf("child environment carries binding credential %q", name)
		}
	}
	var sawIdentity bool
	for _, entry := range f.launched.env {
		if entry == "KMS_TOKEN=id-token" {
			sawIdentity = true
		}
	}
	if !sawIdentity {
		t.Fatalf("child environment lost KMS_TOKEN: %q", f.launched.env)
	}
	// The stripped names never reach stderr either, since they carry tokens.
	if strings.Contains(f.stderr(), envTestStripeToken) {
		t.Fatalf("stderr = %s", f.stderr())
	}
}

func TestExecKeepsBoundSecretAsAnEmptyChildVariableWithoutFetching(t *testing.T) {
	f := newExecFixture(t, 0, nil)
	f.secrets.list[0].Bound = true
	f.secrets.list[0].Versions[0].Bound = true
	baseEnvironment := f.environOverride
	f.environOverride = func() []string {
		return append(baseEnvironment(), "SESSION_SECRET=stale-parent-default")
	}
	if code := f.runExec([]string{"--secret-token", "stripe-key=" + envTestStripeToken, "--preserve-env", "--quiet"}, "/usr/bin/app"); code != exitOK {
		t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
	}
	if !slices.Contains(f.launched.env, "SESSION_SECRET=") {
		t.Fatalf("child environment = %q, want bound secret present and empty", f.launched.env)
	}
	if slices.Contains(f.launched.env, "SESSION_SECRET=stale-parent-default") {
		t.Fatalf("--preserve-env replaced a required empty bound output: %q", f.launched.env)
	}
	for _, call := range f.rec.snapshot() {
		if call.method == "GetSecret" && call.path == "/prod/app/session-secret" {
			t.Fatal("GetSecret was called for a bound exec value")
		}
	}
}

func TestScrubBindingKeyEnvironmentRemovesOnlyExactCredentialNames(t *testing.T) {
	parent := []string{
		bindingKeyEnv + "=old",
		newBindingKeyEnv + "=new",
		"KMS_BINDING_KEY_SUFFIX=keep",
		"kms_binding_key=case",
		"PATH=/bin",
	}
	wantSensitive := []string{"KMS_BINDING_KEY_SUFFIX=keep", "kms_binding_key=case", "PATH=/bin"}
	if got := scrubBindingKeyEnvironment(parent, false); !slices.Equal(got, wantSensitive) {
		t.Fatalf("case-sensitive scrub = %q, want %q", got, wantSensitive)
	}
	wantInsensitive := []string{"KMS_BINDING_KEY_SUFFIX=keep", "PATH=/bin"}
	if got := scrubBindingKeyEnvironment(parent, true); !slices.Equal(got, wantInsensitive) {
		t.Fatalf("case-insensitive scrub = %q, want %q", got, wantInsensitive)
	}
}

func TestScrubChildCredentialEnvironmentAlsoRemovesInjectedTokenNames(t *testing.T) {
	entries := []string{
		"KMS_SECRET_TOKEN_API=token",
		"KMS_SECRET_TOKEN_=token",
		bindingKeyEnv + "=binding",
		"KMS_SECRET_TOKENX=keep",
		"APP=value",
	}
	want := []string{"KMS_SECRET_TOKENX=keep", "APP=value"}
	if got := scrubChildCredentialEnvironment(entries, false); !slices.Equal(got, want) {
		t.Fatalf("credential scrub = %q, want %q", got, want)
	}
}

// TestExecInjectedWinsUnlessPreserveEnv: the default is that the store decides,
// because a stale variable left in a service manager's environment is exactly
// the drift exec exists to remove. --preserve-env inverts that and reports
// every name it kept, so the operator can see what was not injected.
func TestExecInjectedWinsUnlessPreserveEnv(t *testing.T) {
	t.Parallel()
	parent := func() []string {
		return []string{"PATH=/usr/bin", "DB_HOST=stale.internal", "GREETING=stale"}
	}

	t.Run("injected wins", func(t *testing.T) {
		t.Parallel()
		f := newExecFixture(t, 0, nil)
		f.environOverride = parent
		if code := f.runExec([]string{"--no-secrets"}, "/usr/bin/app"); code != 0 {
			t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
		}
		want := []string{"PATH=/usr/bin", "DB_HOST=" + envTestHostValue, "GREETING=" + envTestGreetValue}
		if strings.Join(f.launched.env, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("env = %q, want %q", f.launched.env, want)
		}
		if strings.Contains(f.stderr(), "--preserve-env") {
			t.Fatalf("stderr = %s, want no shadowing note", f.stderr())
		}
	})

	t.Run("--preserve-env keeps the parent and says so", func(t *testing.T) {
		t.Parallel()
		f := newExecFixture(t, 0, nil)
		f.environOverride = parent
		if code := f.runExec([]string{"--no-secrets", "--preserve-env"}, "/usr/bin/app"); code != 0 {
			t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
		}
		want := []string{"PATH=/usr/bin", "DB_HOST=stale.internal", "GREETING=stale"}
		if strings.Join(f.launched.env, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("env = %q, want %q", f.launched.env, want)
		}
		for _, name := range []string{"DB_HOST", "GREETING"} {
			note := "note: " + name + " is already set and kept (--preserve-env); the store's value was not injected"
			if !strings.Contains(f.stderr(), note) {
				t.Fatalf("stderr = %s, want %q", f.stderr(), note)
			}
		}
		// The note names variables, never values.
		if strings.Contains(f.stderr(), envTestHostValue) {
			t.Fatalf("stderr leaked the store's value: %s", f.stderr())
		}
	})

	t.Run("--quiet drops the shadowing notes", func(t *testing.T) {
		t.Parallel()
		f := newExecFixture(t, 0, nil)
		f.environOverride = parent
		if code := f.runExec([]string{"--no-secrets", "--preserve-env", "--quiet"}, "/usr/bin/app"); code != 0 {
			t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
		}
		if strings.Contains(f.stderr(), "--preserve-env") {
			t.Fatalf("stderr = %s, want the advisory note suppressed", f.stderr())
		}
	})
}

// TestExecReportsSkippedSecretsBeforeLaunching: the warning must be on stderr
// before the process image is replaced, or on Unix it would never be printed
// at all.
func TestExecReportsSkippedSecretsBeforeLaunching(t *testing.T) {
	t.Parallel()
	f := newExecFixture(t, 0, nil)
	if code := f.runExec(nil, "/usr/bin/app"); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, f.stderr())
	}
	if !strings.Contains(f.stderr(), "warning: skipped secret /prod/app/stripe-key") {
		t.Fatalf("stderr = %s", f.stderr())
	}
	if !f.launched.called {
		t.Fatal("a skipped secret must not stop the launch without --strict")
	}
	for _, entry := range f.launched.env {
		if strings.HasPrefix(entry, "STRIPE_KEY=") {
			t.Fatalf("a skipped secret reached the child: %q", entry)
		}
	}
}

// --- exit status ------------------------------------------------------------

// TestExecReturnsTheCommandsExitCode: exec is transparent, so the wrapper's
// status is the command's, including the codes a shell reserves.
func TestExecReturnsTheCommandsExitCode(t *testing.T) {
	t.Parallel()
	for _, code := range []int{0, 1, 7, 126, 127, 255} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			t.Parallel()
			f := newExecFixture(t, code, nil)
			if got := f.runExec([]string{"--no-secrets"}, "/usr/bin/app"); got != code {
				t.Fatalf("exit = %d, want the command's %d (stderr=%s)", got, code, f.stderr())
			}
		})
	}
}

// TestExecReportsALaunchFailure: when the command could not be started the
// reason goes to stderr and the shell's own code is returned, so a supervisor
// reads 127 the way it would from sh.
func TestExecReportsALaunchFailure(t *testing.T) {
	t.Parallel()
	f := newExecFixture(t, exitExecNotFound, errors.New("command not found"))
	code := f.runExec([]string{"--no-secrets"}, "no-such-binary", "--flag")
	if code != exitExecNotFound {
		t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitExecNotFound, f.stderr())
	}
	want := "error: exec no-such-binary: command not found"
	if !strings.Contains(f.stderr(), want) {
		t.Fatalf("stderr = %s, want %q", f.stderr(), want)
	}
}

// TestExecNeverLaunchesOnAResolutionError: a process must never start with a
// silently partial environment, so every failure mode short-circuits the
// launch.
func TestExecNeverLaunchesOnAResolutionError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		set  func(*envFixture)
		args []string
		want int
	}{
		{
			name: "--strict with a secret that has no token",
			args: []string{"--strict"},
			set:  func(*envFixture) {},
			want: exitError,
		},
		{
			name: "a stray secret token",
			args: []string{"--secret-token", "stipe-key=typo"},
			set:  func(*envFixture) {},
			want: exitError,
		},
		{
			name: "the server refuses the listing",
			set: func(f *envFixture) {
				f.params.listErr = status.Error(codes.PermissionDenied, "denied")
			},
			want: exitPermissionDenied,
		},
		{
			name: "a release digest does not verify",
			args: []string{"--release", "runtime", "--secret-token", "billing-key=" + envTestStripeToken},
			set: func(f *envFixture) {
				f.installRelease()
				f.params.get["/prod/app/db/host"].Value = "db.tampered"
			},
			want: exitError,
		},
		{
			name: "a value has no legal variable name",
			set: func(f *envFixture) {
				f.params.list = append(f.params.list, &kmsv1.Parameter{
					Ref: envTestRef("prod", "app", "2fa-issuer"), Value: "acme", ContentType: "string",
				})
			},
			args: []string{"--no-secrets"},
			want: exitError,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newExecFixture(t, 0, nil)
			tc.set(f.envFixture)
			if code := f.runExec(tc.args, "/usr/bin/app"); code != tc.want {
				t.Fatalf("exit = %d, want %d (stderr=%s)", code, tc.want, f.stderr())
			}
			if f.launched.called {
				t.Fatalf("the launcher ran despite a resolution error: %s", f.stderr())
			}
		})
	}
}

// --- the real launcher ------------------------------------------------------

// TestResolveCommandUsesTheShellsCodes: 127 for "not found", 126 for "found
// but not runnable"; a wrapper or supervisor reads those exactly as it reads
// sh's.
func TestResolveCommandUsesTheShellsCodes(t *testing.T) {
	t.Parallel()

	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		path, code, err := resolveCommand("parameter-store-no-such-command")
		if err == nil {
			t.Fatalf("resolveCommand found %q", path)
		}
		if code != exitExecNotFound {
			t.Fatalf("code = %d, want %d", code, exitExecNotFound)
		}
		if !strings.Contains(err.Error(), "command not found") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("present but not executable", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" {
			t.Skip("Windows decides executability by extension, not by a mode bit")
		}
		path := filepath.Join(t.TempDir(), "script")
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		resolved, code, err := resolveCommand(path)
		if err == nil {
			t.Fatalf("resolveCommand accepted a non-executable file as %q", resolved)
		}
		if code != exitExecNotExecutable {
			t.Fatalf("code = %d, want %d (err %v)", code, exitExecNotExecutable, err)
		}
		if !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("executable", func(t *testing.T) {
		t.Parallel()
		resolved, code, err := resolveCommand(os.Args[0])
		if err != nil {
			t.Fatalf("resolveCommand(%q) = %v", os.Args[0], err)
		}
		if code != 0 || resolved == "" {
			t.Fatalf("resolveCommand = (%q, %d)", resolved, code)
		}
	})
}

// TestResolveCommandRefusesTheWorkingDirectory: exec typically runs in a
// checkout or a service's working directory, so a bare name that resolves only
// through "." is refused with the path to type instead.
func TestResolveCommandRefusesTheWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the executable bit is what makes this reachable")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "localtool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Chdir(dir)
	t.Setenv("PATH", ".")
	resolved, code, err := resolveCommand("localtool")
	if err == nil {
		t.Fatalf("resolveCommand accepted %q from the working directory", resolved)
	}
	if code != exitExecNotFound {
		t.Fatalf("code = %d, want %d (err %v)", code, exitExecNotFound, err)
	}
	if !strings.Contains(err.Error(), "./localtool") {
		t.Fatalf("err = %v, want it to suggest ./localtool", err)
	}
}

// TestLauncherDefaultsToTheProcessLauncher: the override exists for tests
// only; production must reach launchProcess.
func TestLauncherDefaultsToTheProcessLauncher(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	if c.launcher() == nil {
		t.Fatal("launcher() = nil")
	}
	called := false
	c.launchOverride = func([]string, []string) (int, error) { called = true; return 3, nil }
	code, err := c.launcher()([]string{"x"}, nil)
	if err != nil || code != 3 || !called {
		t.Fatalf("launcher() = (%d, %v), called=%v", code, err, called)
	}
}

// TestEnvironDefaultsToTheProcessEnvironment is the companion for the parent
// environment source.
func TestEnvironDefaultsToTheProcessEnvironment(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	if len(c.environ()) != len(os.Environ()) {
		t.Fatalf("environ() = %d entries, want the process environment's %d", len(c.environ()), len(os.Environ()))
	}
	c.environOverride = func() []string { return []string{"A=1"} }
	if got := c.environ(); len(got) != 1 || got[0] != "A=1" {
		t.Fatalf("environ() = %q", got)
	}
}
