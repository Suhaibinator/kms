package cli

import (
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	c := newTestCLI()
	if code := c.Run([]string{"version"}); code != 0 {
		t.Fatalf("version exit = %d", code)
	}
	if strings.TrimSpace(c.stdout()) != Version {
		t.Fatalf("version output = %q, want %q", c.stdout(), Version)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	c := newTestCLI()
	if code := c.Run([]string{"frobnicate"}); code != 2 {
		t.Fatalf("unknown exit = %d, want 2", code)
	}
	if !strings.Contains(c.stderr(), "unknown command") {
		t.Fatalf("stderr = %s", c.stderr())
	}
}

func TestRunNoArgs(t *testing.T) {
	c := newTestCLI()
	if code := c.Run(nil); code != 2 {
		t.Fatalf("no-args exit = %d, want 2", code)
	}
}

func TestRunHelp(t *testing.T) {
	c := newTestCLI()
	if code := c.Run([]string{"help"}); code != 0 {
		t.Fatalf("help exit = %d", code)
	}
	if !strings.Contains(c.stderr(), "Usage:") {
		t.Fatalf("help output missing usage: %s", c.stderr())
	}
	for _, want := range []string{
		"Application onboarding",
		"rotate-admin",
		"create ./certs and restrict directory access to its owner",
		"admin namespace create --env ENV --app APP --auth-methods mtls",
		"admin identity create NAME --namespace ENV/APP --auth mtls --out ./certs",
		"Create NAME.crt and NAME.key for the application",
	} {
		if !strings.Contains(c.stderr(), want) {
			t.Fatalf("help output missing %q: %s", want, c.stderr())
		}
	}
}

func TestCommandFlagHelpExitsSuccessfully(t *testing.T) {
	for _, args := range [][]string{
		{"rotate-admin", "--help"},
		{"admin", "identity", "create", "-h"},
		{"admin", "identity", "issue-cert", "--help"},
		{"admin", "ca", "show", "-h"},
		{"put-secret", "--help"},
	} {
		c := newTestCLI()
		if code := c.Run(args); code != 0 {
			t.Errorf("Run(%v) exit = %d, want 0; stderr: %s", args, code, c.stderr())
		}
	}
}

func TestRunResetsHelpRequestedBetweenInvocations(t *testing.T) {
	c := newTestCLI()
	if code := c.Run([]string{"admin", "ca", "show", "-h"}); code != 0 {
		t.Fatalf("help exit = %d", code)
	}
	if code := c.Run([]string{"frobnicate"}); code != 2 {
		t.Fatalf("later invalid command exit = %d, want 2", code)
	}
}

func TestMalformedCommandFlagStillExitsWithUsageError(t *testing.T) {
	c := newTestCLI()
	if code := c.Run([]string{"admin", "identity", "create", "--not-a-real-flag"}); code != 2 {
		t.Fatalf("malformed flag exit = %d, want 2; stderr: %s", code, c.stderr())
	}
}

func TestConsumeGlobalConfigFlag(t *testing.T) {
	c := newTestCLI()
	// `--config x version` should set ConfigPath and still run version.
	rest := c.consumeGlobalFlags([]string{"--config", "/etc/kms.yaml", "version"})
	if c.ConfigPath != "/etc/kms.yaml" {
		t.Fatalf("ConfigPath = %q", c.ConfigPath)
	}
	if len(rest) != 1 || rest[0] != "version" {
		t.Fatalf("rest = %v", rest)
	}

	// `--config=x` form.
	c2 := newTestCLI()
	rest = c2.consumeGlobalFlags([]string{"--config=/a.yaml", "serve"})
	if c2.ConfigPath != "/a.yaml" || rest[0] != "serve" {
		t.Fatalf("config= form failed: %q %v", c2.ConfigPath, rest)
	}

	// No global flag: everything is the command.
	c3 := newTestCLI()
	rest = c3.consumeGlobalFlags([]string{"serve", "--config", "x"})
	if rest[0] != "serve" {
		t.Fatalf("rest = %v", rest)
	}
}
