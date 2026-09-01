package cli

import (
	"strings"
	"testing"
)

// TestGlobalOutputFlag runs the real dispatcher so the flag, its "=" form, and
// the environment variable are all exercised through the path a user takes.
// version is the subject because it needs no database and no server.
func TestGlobalOutputFlag(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		want outputMode
	}{
		{name: "default is table", args: []string{"version"}, want: outputTable},
		{name: "short flag", args: []string{"-o", "json", "version"}, want: outputJSON},
		{name: "long flag", args: []string{"--output", "json", "version"}, want: outputJSON},
		{name: "long flag with =", args: []string{"--output=json", "version"}, want: outputJSON},
		{name: "single-dash long flag", args: []string{"-output=json", "version"}, want: outputJSON},
		{name: "case insensitive", args: []string{"-o", "JSON", "version"}, want: outputJSON},
		{name: "environment", env: map[string]string{"KMS_OUTPUT": "json"}, args: []string{"version"}, want: outputJSON},
		// The flag is the more specific instruction, so it wins over the
		// environment the shell happens to carry.
		{name: "flag beats environment", env: map[string]string{"KMS_OUTPUT": "json"}, args: []string{"-o", "table", "version"}, want: outputTable},
		{name: "environment beats nothing", env: map[string]string{"KMS_OUTPUT": "TABLE"}, args: []string{"version"}, want: outputTable},
		// An exported-but-empty variable means "unset", not "invalid".
		{name: "empty environment ignored", env: map[string]string{"KMS_OUTPUT": ""}, args: []string{"version"}, want: outputTable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestCLI()
			c.lookupEnv = mapLookup(tc.env)
			if code := c.Run(tc.args); code != 0 {
				t.Fatalf("Run(%v) = %d, want 0; stderr=%s", tc.args, code, c.stderr())
			}
			if c.output != tc.want {
				t.Fatalf("Run(%v) left output = %q, want %q", tc.args, c.output, tc.want)
			}
			if c.jsonOutput() != (tc.want == outputJSON) {
				t.Fatalf("jsonOutput() = %v for mode %q", c.jsonOutput(), c.output)
			}
		})
	}
}

// TestGlobalOutputFlagRejectsBadValue: a typo in --output or KMS_OUTPUT is a
// usage error (exit 2), never a silent fall back to the table, so a script
// that expects JSON never parses a table by accident.
func TestGlobalOutputFlagRejectsBadValue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		want string // substring required on stderr
	}{
		{name: "short flag", args: []string{"-o", "yaml", "version"}, want: `invalid output format "yaml" (want table or json)`},
		{name: "long flag", args: []string{"--output", "yaml", "version"}, want: "invalid output format"},
		{name: "long flag with =", args: []string{"--output=yaml", "version"}, want: "invalid output format"},
		{name: "environment", env: map[string]string{"KMS_OUTPUT": "bogus"}, args: []string{"version"}, want: "KMS_OUTPUT"},
		{name: "missing value", args: []string{"-o"}, want: "requires a value"},
		{name: "missing value long", args: []string{"--output"}, want: "requires a value"},
		{name: "missing config value", args: []string{"--config"}, want: "requires a value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestCLI()
			c.lookupEnv = mapLookup(tc.env)
			if code := c.Run(tc.args); code != 2 {
				t.Fatalf("Run(%v) = %d, want 2; stderr=%s", tc.args, code, c.stderr())
			}
			if !strings.Contains(c.stderr(), tc.want) {
				t.Fatalf("Run(%v) stderr = %q, want it to contain %q", tc.args, c.stderr(), tc.want)
			}
			if c.stdout() != "" {
				t.Fatalf("Run(%v) wrote to stdout: %q", tc.args, c.stdout())
			}
		})
	}
}

// TestGlobalYesAndQuietFlags: both spellings of each boolean, before the
// subcommand.
func TestGlobalYesAndQuietFlags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		args      []string
		wantYes   bool
		wantQuiet bool
	}{
		{name: "none", args: []string{"version"}},
		{name: "-y", args: []string{"-y", "version"}, wantYes: true},
		{name: "--yes", args: []string{"--yes", "version"}, wantYes: true},
		{name: "-yes", args: []string{"-yes", "version"}, wantYes: true},
		{name: "-q", args: []string{"-q", "version"}, wantQuiet: true},
		{name: "--quiet", args: []string{"--quiet", "version"}, wantQuiet: true},
		{name: "-quiet", args: []string{"-quiet", "version"}, wantQuiet: true},
		{name: "both", args: []string{"-y", "-q", "version"}, wantYes: true, wantQuiet: true},
		{name: "mixed with output", args: []string{"--yes", "-o", "json", "-q", "version"}, wantYes: true, wantQuiet: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestCLI()
			if code := c.Run(tc.args); code != 0 {
				t.Fatalf("Run(%v) = %d, want 0; stderr=%s", tc.args, code, c.stderr())
			}
			if c.assumeYes != tc.wantYes {
				t.Errorf("Run(%v) left assumeYes = %v, want %v", tc.args, c.assumeYes, tc.wantYes)
			}
			if c.quiet != tc.wantQuiet {
				t.Errorf("Run(%v) left quiet = %v, want %v", tc.args, c.quiet, tc.wantQuiet)
			}
		})
	}
}

// TestGlobalFlagsResetBetweenRuns: Run is called once per process in
// production but repeatedly in tests, so state from a previous invocation must
// never leak into the next one.
func TestGlobalFlagsResetBetweenRuns(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	if code := c.Run([]string{"-o", "json", "-y", "-q", "--config", "/etc/kms.yaml", "version"}); code != 0 {
		t.Fatalf("first Run = %d; stderr=%s", code, c.stderr())
	}
	if code := c.Run([]string{"version"}); code != 0 {
		t.Fatalf("second Run = %d; stderr=%s", code, c.stderr())
	}
	if c.output != outputTable || c.assumeYes || c.quiet || c.ConfigPath != "" || c.ConfigPathSource != "" {
		t.Fatalf("second Run inherited state: output=%q yes=%v quiet=%v config=%q (%s)",
			c.output, c.assumeYes, c.quiet, c.ConfigPath, c.ConfigPathSource)
	}
}

// TestGlobalFlagsAfterSubcommand: every global flag, long or short, is
// registered on every command flag set, so `<command> -y` means the same as
// `-y <command>`. config validate is the subject because it neither dials nor
// opens a database and still parses flags.
func TestGlobalFlagsAfterSubcommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		args      []string
		wantCode  int
		wantYes   bool
		wantQuiet bool
		wantMode  outputMode
	}{
		{name: "--yes before", args: []string{"--yes", "config", "validate"}, wantYes: true, wantMode: outputTable},
		{name: "--yes after", args: []string{"config", "validate", "--yes"}, wantYes: true, wantMode: outputTable},
		{name: "--quiet after", args: []string{"config", "validate", "--quiet"}, wantQuiet: true, wantMode: outputTable},
		{name: "--output after", args: []string{"config", "validate", "--output", "json"}, wantMode: outputJSON},
		{name: "--output= after", args: []string{"config", "validate", "--output=json"}, wantMode: outputJSON},
		// A value after the subcommand overrides one given before it.
		{name: "after overrides before", args: []string{"-o", "json", "config", "validate", "--output=table"}, wantMode: outputTable},
		// Short forms after the subcommand are the same flags.
		{name: "-y after", args: []string{"config", "validate", "-y"}, wantYes: true, wantMode: outputTable},
		{name: "-q after", args: []string{"config", "validate", "-q"}, wantQuiet: true, wantMode: outputTable},
		{name: "-o after", args: []string{"config", "validate", "-o", "json"}, wantMode: outputJSON},
		{name: "-o= after", args: []string{"config", "validate", "-o=json"}, wantMode: outputJSON},
		// An unknown flag after the subcommand is still a usage error.
		{name: "unknown short flag", args: []string{"config", "validate", "-z"}, wantCode: 2, wantMode: outputTable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestCLI()
			if code := c.Run(tc.args); code != tc.wantCode {
				t.Fatalf("Run(%v) = %d, want %d; stderr=%s", tc.args, code, tc.wantCode, c.stderr())
			}
			if tc.wantCode == 2 && !strings.Contains(c.stderr(), "flag provided but not defined") {
				t.Fatalf("Run(%v) stderr = %q, want an unknown-flag message", tc.args, c.stderr())
			}
			if tc.wantCode != 0 {
				return
			}
			if c.assumeYes != tc.wantYes || c.quiet != tc.wantQuiet || c.output != tc.wantMode {
				t.Fatalf("Run(%v) left yes=%v quiet=%v output=%q, want %v/%v/%q",
					tc.args, c.assumeYes, c.quiet, c.output, tc.wantYes, tc.wantQuiet, tc.wantMode)
			}
		})
	}
}

// TestAddGlobalFlagsAliasesShareOneValue: -o/-y/-q after the subcommand are
// the same flags as their long forms (Go's flag package never abbreviates, so
// -o cannot collide with a command's --out), and both spellings must write to
// the same field so neither can drift.
func TestAddGlobalFlagsAliasesShareOneValue(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	fs := c.newFlags("test")
	for long, short := range shortFlags {
		l, s := fs.Lookup(long), fs.Lookup(short)
		if l == nil || s == nil {
			t.Fatalf("command flag set is missing --%s or -%s", long, short)
		}
		if err := s.Value.Set(l.DefValue); err != nil {
			t.Fatalf("-%s.Set(%q): %v", short, l.DefValue, err)
		}
		if got := l.Value.String(); got != s.Value.String() {
			t.Errorf("--%s reads %q after -%s was set, want the same value", long, got, short)
		}
	}
}

// TestConsumeGlobalFlagsStopsAtTheSubcommand: parsing stops at the first token
// that is not a global flag, so a command's own flags are never eaten.
func TestConsumeGlobalFlagsStopsAtTheSubcommand(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	rest, ok := c.consumeGlobalFlags([]string{"-o", "json", "-y", "list", "prod/api", "--output", "table"})
	if !ok {
		t.Fatalf("consumeGlobalFlags failed: %s", c.stderr())
	}
	if want := []string{"list", "prod/api", "--output", "table"}; strings.Join(rest, " ") != strings.Join(want, " ") {
		t.Fatalf("rest = %v, want %v", rest, want)
	}
	if c.output != outputJSON || !c.assumeYes {
		t.Fatalf("output = %q, assumeYes = %v", c.output, c.assumeYes)
	}
}

// TestGlobalConfigFlagThroughRun: --config and KMS_CONFIG both set ConfigPath,
// and each records where the value came from so `config show` can report it.
func TestGlobalConfigFlagThroughRun(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		env        map[string]string
		args       []string
		wantPath   string
		wantSource string
	}{
		{name: "flag", args: []string{"--config", "/etc/kms.yaml", "version"}, wantPath: "/etc/kms.yaml", wantSource: "flag --config"},
		{name: "flag with =", args: []string{"--config=/etc/kms.yaml", "version"}, wantPath: "/etc/kms.yaml", wantSource: "flag --config"},
		{name: "single dash", args: []string{"-config", "/etc/kms.yaml", "version"}, wantPath: "/etc/kms.yaml", wantSource: "flag --config"},
		{name: "environment", env: map[string]string{"KMS_CONFIG": "/env/kms.yaml"}, args: []string{"version"}, wantPath: "/env/kms.yaml", wantSource: "env KMS_CONFIG"},
		{
			name:       "flag beats environment",
			env:        map[string]string{"KMS_CONFIG": "/env/kms.yaml"},
			args:       []string{"--config", "/etc/kms.yaml", "version"},
			wantPath:   "/etc/kms.yaml",
			wantSource: "flag --config",
		},
		{name: "none", args: []string{"version"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestCLI()
			c.lookupEnv = mapLookup(tc.env)
			if code := c.Run(tc.args); code != 0 {
				t.Fatalf("Run(%v) = %d; stderr=%s", tc.args, code, c.stderr())
			}
			if c.ConfigPath != tc.wantPath || c.ConfigPathSource != tc.wantSource {
				t.Fatalf("Run(%v) left ConfigPath = %q (%q), want %q (%q)",
					tc.args, c.ConfigPath, c.ConfigPathSource, tc.wantPath, tc.wantSource)
			}
		})
	}
}

// TestNoCommandPrintsUsage: an empty invocation is a usage error, and the help
// goes to stderr so a piped stdout stays clean.
func TestNoCommandPrintsUsage(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	if code := c.Run(nil); code != 2 {
		t.Fatalf("Run(nil) = %d, want 2", code)
	}
	if !strings.Contains(c.stderr(), "Usage:") {
		t.Fatalf("stderr = %q, want the usage text", c.stderr())
	}
	if c.stdout() != "" {
		t.Fatalf("usage reached stdout: %q", c.stdout())
	}
}

// TestGlobalFlagsWithoutACommandAreAUsageError: `parameter-store -o json` on
// its own has nothing to run.
func TestGlobalFlagsWithoutACommandAreAUsageError(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	if code := c.Run([]string{"-o", "json", "-y"}); code != 2 {
		t.Fatalf("Run = %d, want 2; stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stderr(), "Usage:") {
		t.Fatalf("stderr = %q, want the usage text", c.stderr())
	}
}

// TestUnknownCommandIsAUsageError.
func TestUnknownCommandIsAUsageError(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	if code := c.Run([]string{"-o", "json", "frobnicate"}); code != 2 {
		t.Fatalf("Run = %d, want 2", code)
	}
	if !strings.Contains(c.stderr(), `unknown command "frobnicate"`) {
		t.Fatalf("stderr = %q", c.stderr())
	}
}

// TestInfoRespectsQuiet: info carries progress and advice, which --quiet
// silences; it never carries results (stdout) or errors.
func TestInfoRespectsQuiet(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	c.info("wrote %d bytes to %s", 12, "/tmp/x")
	if got, want := c.stderr(), "wrote 12 bytes to /tmp/x\n"; got != want {
		t.Fatalf("info stderr = %q, want %q", got, want)
	}
	if c.stdout() != "" {
		t.Fatalf("info wrote to stdout: %q", c.stdout())
	}

	q := newTestCLI()
	q.quiet = true
	q.info("wrote %d bytes to %s", 12, "/tmp/x")
	if q.stderr() != "" {
		t.Fatalf("--quiet did not silence info: %q", q.stderr())
	}
}

// TestInfoQuietSetThroughRun ties the flag to the behaviour: -q flips the same
// switch info reads.
func TestInfoQuietSetThroughRun(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	if code := c.Run([]string{"-q", "version"}); code != 0 {
		t.Fatalf("Run = %d; stderr=%s", code, c.stderr())
	}
	c.err.Reset()
	c.info("progress")
	if c.stderr() != "" {
		t.Fatalf("-q did not silence info: %q", c.stderr())
	}
}

// TestVersionRejectsUnknownFlags: version runs through a flag set like every
// other command, so the long global flags work after it and an unknown flag
// is the usual usage error rather than being silently discarded.
func TestVersionRejectsUnknownFlags(t *testing.T) {
	c := newTestCLI()
	if code := c.Run([]string{"version", "--not-a-flag"}); code != 2 {
		t.Errorf("Run(version --not-a-flag) = %d, want 2", code)
	}
	c2 := newTestCLI()
	if code := c2.Run([]string{"version", "--output=json"}); code != 0 {
		t.Fatalf("Run(version --output=json) = %d, want 0; stderr=%s", code, c2.stderr())
	}
	if c2.output != outputJSON {
		t.Errorf("Run(version --output=json) left output = %q, want json", c2.output)
	}
	if got, want := c2.stdout(), "{\n  \"version\": \""+Version+"\"\n}\n"; got != want {
		t.Errorf("Run(version --output=json) stdout = %q, want %q", got, want)
	}
	c3 := newTestCLI()
	if code := c3.Run([]string{"help", "frobnicate"}); code != 2 {
		t.Errorf("Run(help frobnicate) = %d, want 2", code)
	}
}
