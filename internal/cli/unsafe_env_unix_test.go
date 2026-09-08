//go:build unix

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the real dev server and CLI resolution, launching a child subprocess
// instead of replacing the test runner with exec(2).
func TestDevUnsafeEnvironmentSmoke(t *testing.T) {
	run := startDev(t, nil, "--dir", filepath.Join(t.TempDir(), "store"))
	token := run.bannerToken(t, "dev-only admin token")
	if code, _, stderr := run.client(t, token, "put-parameter", "/dev/demo/node_options", "literal-runtime-option"); code != 0 {
		t.Fatal(stderr)
	}
	for _, mode := range []string{"deny", "prefix", "override"} {
		c := newTestCLI()
		launched := false
		var output []byte
		c.launchOverride = func(argv, env []string) (int, error) {
			launched = true
			cmd := exec.Command(argv[0], argv[1:]...)
			cmd.Env = env
			var err error
			output, err = cmd.Output()
			if err != nil {
				return 1, err
			}
			return 0, nil
		}
		args := []string{"exec", "dev/demo", "--endpoint", run.grpcAddr, "--ca", run.caFile(t), "--token", token, "--no-secrets", "--prefix", "node_options"}
		name := "NODE_OPTIONS"
		if mode == "prefix" {
			args = append(args, "--env-prefix", "MYAPP_")
			name = "MYAPP_NODE_OPTIONS"
		}
		if mode == "override" {
			args = append(args, "--allow-unsafe-env-names")
		}
		args = append(args, "--", "printenv", name)
		code := c.Run(args)
		if mode == "deny" {
			if code == 0 || launched || !strings.Contains(c.stderr(), name) {
				t.Fatalf("unsafe launch: %s", c.stderr())
			}
		} else if code != 0 || !launched || string(output) != "literal-runtime-option\n" {
			t.Fatalf("%s failed: %q %s", mode, output, c.stderr())
		}
	}
	marker := filepath.Join(t.TempDir(), "executed")
	value := "$(touch " + marker + ")"
	if code, _, stderr := run.client(t, token, "put-parameter", "/dev/demo/hostile", value); code != 0 {
		t.Fatal(stderr)
	}
	path := filepath.Join(t.TempDir(), "env")
	if code, _, stderr := run.client(t, token, "env", "dev/demo", "--no-secrets", "--prefix", "hostile", "--out", path); code != 0 {
		t.Fatal(stderr)
	}
	out, err := exec.Command("sh", "-c", `set -a; . "$1"; printf '%s' "$HOSTILE"`, "sh", path).Output()
	if err != nil || string(out) != value {
		t.Fatalf("sourced value: %q, %v", out, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("substitution executed: %v", err)
	}
}
