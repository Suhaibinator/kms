package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The systemd unit exists twice: as the file operators install
// (deploy/systemd/parameter-store.service) and as the block they copy out of
// the runbook. Two copies drift, and the one that matters here is
// ExecReload — without it `systemctl reload parameter-store` fails and the
// hot-reload section of the runbook describes something the shipped unit
// cannot do. These tests keep the copies identical.

const (
	unitExecStart  = "ExecStart=/usr/local/bin/parameter-store serve --config /etc/parameter-store/config.yaml"
	unitExecReload = "ExecReload=/bin/kill -HUP $MAINPID"
)

// repoFile reads a file by its path relative to the repository root. Tests run
// with the package directory as the working directory.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// documentedUnit returns the ini block under "Running under systemd" in the
// operations runbook.
func documentedUnit(t *testing.T) string {
	t.Helper()
	doc := repoFile(t, "docs/operations.md")
	section := "## Running under systemd"
	idx := strings.Index(doc, section)
	if idx < 0 {
		t.Fatalf("docs/operations.md has no %q section", section)
	}
	_, rest, ok := strings.Cut(doc[idx:], "```ini\n")
	if !ok {
		t.Fatal("the \"Running under systemd\" section has no fenced ini block")
	}
	block, _, ok := strings.Cut(rest, "```")
	if !ok {
		t.Fatal("the systemd unit block in docs/operations.md is not closed")
	}
	return block
}

// TestShippedSystemdUnitMatchesTheRunbook: the file and the documented block
// must be the same text, so an operator who copies either one gets the same
// service.
func TestShippedSystemdUnitMatchesTheRunbook(t *testing.T) {
	shipped := repoFile(t, "deploy/systemd/parameter-store.service")
	documented := documentedUnit(t)
	if strings.TrimRight(shipped, "\n") != strings.TrimRight(documented, "\n") {
		t.Errorf("deploy/systemd/parameter-store.service and the unit in docs/operations.md have drifted.\n"+
			"shipped:\n%s\ndocumented:\n%s", shipped, documented)
	}
}

// TestShippedSystemdUnitSupportsReload: `systemctl reload` is the documented
// way to rotate a certificate, and it only works if the unit says how.
func TestShippedSystemdUnitSupportsReload(t *testing.T) {
	shipped := repoFile(t, "deploy/systemd/parameter-store.service")
	for _, line := range []string{unitExecStart, unitExecReload} {
		if !strings.Contains(shipped, line+"\n") {
			t.Errorf("deploy/systemd/parameter-store.service has no %q line:\n%s", line, shipped)
		}
	}
	// ExecReload has to name the process ExecStart started, so it must follow
	// it in the same [Service] section.
	if strings.Index(shipped, unitExecReload) < strings.Index(shipped, unitExecStart) {
		t.Error("ExecReload appears before ExecStart in the shipped unit")
	}
}
