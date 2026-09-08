//go:build linux

package envinject

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run explicitly on a host with a working user systemd manager:
// KMS_TEST_SYSTEMD=1 go test ./internal/envinject -run TestWriteDotenvSystemd -count=1 -v
// Once opted in, missing tools or an inaccessible manager are failures, not skips.
func TestWriteDotenvSystemd(t *testing.T) {
	if os.Getenv("KMS_TEST_SYSTEMD") != "1" {
		t.Skip("set KMS_TEST_SYSTEMD=1 on a systemd-enabled host")
	}
	vars := dotenvRoundTripVars()
	var buf strings.Builder
	if err := WriteDotenv(&buf, vars); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(path, []byte(buf.String()), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	envPath, err := exec.LookPath("env")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "systemd-run", "--user", "--quiet", "--wait", "--pipe", "--collect", "--property=EnvironmentFile="+path, "--", envPath, "-0")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("systemd-run: %v: %s", err, stderr.String())
	}
	got := make(map[string]string)
	for _, entry := range strings.Split(string(out), "\x00") {
		if name, value, ok := strings.Cut(entry, "="); ok {
			got[name] = value
		}
	}
	for _, v := range vars {
		if value, ok := got[v.Name]; !ok || value != v.Value {
			t.Errorf("%s: got %q, want %q", v.Name, value, v.Value)
		}
	}
}
