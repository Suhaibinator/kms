package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

// ttyStdin returns an *os.File holding input, positioned at the start, for a
// CLI whose isTTY override claims stdin is interactive. A real file is needed
// because CLI.Stdin is an *os.File (term.IsTerminal wants a descriptor).
func ttyStdin(t *testing.T, input string) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatalf("create temp stdin: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if _, err := f.WriteString(input); err != nil {
		t.Fatalf("write temp stdin: %v", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("rewind temp stdin: %v", err)
	}
	return f
}

// interactive turns c into a CLI whose stdin is an interactive terminal
// carrying input.
func interactive(t *testing.T, c *testCLI, input string) {
	t.Helper()
	c.Stdin = ttyStdin(t, input)
	c.isTTY = func() bool { return true }
}

// TestConfirmAssumeYesSkipsStdin: --yes is the only supported way to run a
// destructive command from a script, and it must not read stdin at all (the
// command may be reading its payload from there).
func TestConfirmAssumeYesSkipsStdin(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		confirm func(c *CLI) (bool, int)
	}{
		{"confirmDestructive", func(c *CLI) (bool, int) { return c.confirmDestructive("delete namespace", "prod/api") }},
		{"confirmYesNo", func(c *CLI) (bool, int) { return c.confirmYesNo("restore /var/lib/kms/kms.db from backup.db") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestCLI()
			c.assumeYes = true
			// Stdin is nil: touching it would panic or error, so a pass proves
			// the prompt was skipped entirely.
			ok, code := tc.confirm(&c.CLI)
			if !ok || code != exitOK {
				t.Fatalf("--yes = (%v, %d), want (true, %d); stderr=%s", ok, code, exitOK, c.stderr())
			}
			if c.stderr() != "" {
				t.Fatalf("--yes still prompted: %q", c.stderr())
			}
			if c.stdout() != "" {
				t.Fatalf("confirmation wrote to stdout: %q", c.stdout())
			}
		})
	}
}

// TestConfirmNonInteractiveRefuses: a script that forgot --yes must fail
// loudly with the usage code rather than hang on a pipe or proceed silently.
func TestConfirmNonInteractiveRefuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		confirm func(c *CLI) (bool, int)
		want    string
	}{
		{
			name:    "confirmDestructive",
			confirm: func(c *CLI) (bool, int) { return c.confirmDestructive("delete namespace", "prod/api") },
			want:    "error: refusing to delete namespace prod/api without --yes on a non-interactive stdin\n",
		},
		{
			name:    "confirmYesNo",
			confirm: func(c *CLI) (bool, int) { return c.confirmYesNo("activate release api v3 in prod/api") },
			want:    "error: refusing to activate release api v3 in prod/api without --yes on a non-interactive stdin\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// isTTY nil and Stdin nil: stdinIsTTY takes the "no terminal" path
			// without consulting term.IsTerminal.
			c := newTestCLI()
			ok, code := tc.confirm(&c.CLI)
			if ok || code != exitUsage {
				t.Fatalf("non-TTY = (%v, %d), want (false, %d)", ok, code, exitUsage)
			}
			if got := c.stderr(); got != tc.want {
				t.Fatalf("refusal = %q, want %q", got, tc.want)
			}
			if c.stdout() != "" {
				t.Fatalf("refusal wrote to stdout: %q", c.stdout())
			}
		})
	}
}

// TestConfirmDestructiveTypedResource covers the retype gate: only the exact
// target string proceeds, and everything else aborts with the usage code.
func TestConfirmDestructiveTypedResource(t *testing.T) {
	t.Parallel()
	const resource = "prod/api"
	for _, tc := range []struct {
		name     string
		typed    string
		wantOK   bool
		wantCode int
		wantErr  string // substring required on stderr
	}{
		{name: "exact match", typed: "prod/api\n", wantOK: true, wantCode: exitOK},
		{name: "crlf is trimmed", typed: "prod/api\r\n", wantOK: true, wantCode: exitOK},
		{name: "no trailing newline", typed: "prod/api", wantOK: true, wantCode: exitOK},
		{name: "wrong target", typed: "prod/other\n", wantCode: exitUsage, wantErr: "does not match"},
		{name: "case differs", typed: "PROD/API\n", wantCode: exitUsage, wantErr: "does not match"},
		{name: "empty line", typed: "\n", wantCode: exitUsage, wantErr: "does not match"},
		{name: "leading space", typed: " prod/api\n", wantCode: exitUsage, wantErr: "does not match"},
		{name: "y is not enough", typed: "y\n", wantCode: exitUsage, wantErr: "does not match"},
		{name: "eof", typed: "", wantCode: exitUsage, wantErr: "reading confirmation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestCLI()
			interactive(t, c, tc.typed)
			ok, code := c.confirmDestructive("delete namespace", resource)
			if ok != tc.wantOK || code != tc.wantCode {
				t.Fatalf("confirmDestructive(%q) = (%v, %d), want (%v, %d); stderr=%s",
					tc.typed, ok, code, tc.wantOK, tc.wantCode, c.stderr())
			}
			if tc.wantErr != "" && !strings.Contains(c.stderr(), tc.wantErr) {
				t.Fatalf("stderr = %q, want it to contain %q", c.stderr(), tc.wantErr)
			}
			// The prompt is a person-facing line: stderr only, so JSON mode's
			// single stdout document stays intact.
			if !strings.Contains(c.stderr(), "Type \"prod/api\" to confirm:") {
				t.Fatalf("prompt missing from stderr: %q", c.stderr())
			}
			if c.stdout() != "" {
				t.Fatalf("prompt or abort message reached stdout: %q", c.stdout())
			}
		})
	}
}

// TestConfirmYesNoAnswers: the default is no, and only an explicit yes (in any
// case) proceeds.
func TestConfirmYesNoAnswers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		typed    string
		wantOK   bool
		wantCode int
		wantErr  string
	}{
		{name: "y", typed: "y\n", wantOK: true, wantCode: exitOK},
		{name: "yes", typed: "yes\n", wantOK: true, wantCode: exitOK},
		{name: "YES", typed: "YES\n", wantOK: true, wantCode: exitOK},
		{name: "Y", typed: "Y\n", wantOK: true, wantCode: exitOK},
		{name: "crlf", typed: "yes\r\n", wantOK: true, wantCode: exitOK},
		{name: "no trailing newline", typed: "y", wantOK: true, wantCode: exitOK},
		{name: "empty defaults to no", typed: "\n", wantCode: exitUsage, wantErr: "aborted"},
		{name: "n", typed: "n\n", wantCode: exitUsage, wantErr: "aborted"},
		{name: "no", typed: "no\n", wantCode: exitUsage, wantErr: "aborted"},
		{name: "yolo", typed: "yolo\n", wantCode: exitUsage, wantErr: "aborted"},
		{name: "eof", typed: "", wantCode: exitUsage, wantErr: "reading confirmation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestCLI()
			interactive(t, c, tc.typed)
			ok, code := c.confirmYesNo("restore /var/lib/kms/kms.db from backup.db")
			if ok != tc.wantOK || code != tc.wantCode {
				t.Fatalf("confirmYesNo(%q) = (%v, %d), want (%v, %d); stderr=%s",
					tc.typed, ok, code, tc.wantOK, tc.wantCode, c.stderr())
			}
			if tc.wantErr != "" && !strings.Contains(c.stderr(), tc.wantErr) {
				t.Fatalf("stderr = %q, want it to contain %q", c.stderr(), tc.wantErr)
			}
			if c.stdout() != "" {
				t.Fatalf("prompt reached stdout: %q", c.stdout())
			}
		})
	}
}

// TestConfirmYesNoPrompt pins the prompt text: the action is capitalised into
// a question and the default is shown as [y/N] so the operator sees that
// pressing Enter declines.
func TestConfirmYesNoPrompt(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	interactive(t, c, "y\n")
	if ok, code := c.confirmYesNo("restore /var/lib/kms/kms.db from backup.db"); !ok || code != exitOK {
		t.Fatalf("confirmYesNo = (%v, %d)", ok, code)
	}
	want := "Restore /var/lib/kms/kms.db from backup.db? [y/N]: "
	if got := c.stderr(); got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

// TestConfirmDestructivePrompt pins the destructive prompt: it states the
// effect, says it cannot be undone, and quotes the string to retype.
func TestConfirmDestructivePrompt(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	interactive(t, c, "prod/api\n")
	if ok, code := c.confirmDestructive("delete namespace", "prod/api"); !ok || code != exitOK {
		t.Fatalf("confirmDestructive = (%v, %d)", ok, code)
	}
	want := "This will delete namespace prod/api. This cannot be undone.\nType \"prod/api\" to confirm: "
	if got := c.stderr(); got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

// TestConfirmPromptsSurviveQuiet: --quiet silences progress, never the prompt
// that guards an irreversible command.
func TestConfirmPromptsSurviveQuiet(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	c.quiet = true
	interactive(t, c, "prod/api\n")
	if ok, _ := c.confirmDestructive("delete namespace", "prod/api"); !ok {
		t.Fatalf("confirmDestructive refused: %s", c.stderr())
	}
	if !strings.Contains(c.stderr(), "to confirm:") {
		t.Fatalf("--quiet suppressed the confirmation prompt: %q", c.stderr())
	}
}

// TestStdinIsTTY covers the override and the nil-stdin default; the real
// term.IsTerminal path is exercised by a non-terminal file, which must read as
// non-interactive.
func TestStdinIsTTY(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	if c.stdinIsTTY() {
		t.Fatal("nil stdin reported as a terminal")
	}
	c.Stdin = ttyStdin(t, "")
	if c.stdinIsTTY() {
		t.Fatal("a regular file reported as a terminal")
	}
	c.isTTY = func() bool { return true }
	if !c.stdinIsTTY() {
		t.Fatal("isTTY override ignored")
	}
}

// TestReadLineNilStdin: a caller that claims a terminal but has no stdin gets
// an error rather than a panic.
func TestReadLineNilStdin(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	c.isTTY = func() bool { return true }
	ok, code := c.confirmYesNo("do the thing")
	if ok || code != exitUsage {
		t.Fatalf("nil stdin with a TTY claim = (%v, %d), want (false, %d)", ok, code, exitUsage)
	}
	if !strings.Contains(c.stderr(), "reading confirmation") {
		t.Fatalf("stderr = %q, want it to name the read failure", c.stderr())
	}
}
