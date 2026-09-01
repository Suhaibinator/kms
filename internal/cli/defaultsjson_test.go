package cli

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

func TestDefaultsApplyPreviewJSONIsTheWholeOfStdout(t *testing.T) {
	stub := &defaultsAdminStub{responses: []*kmsv1.ApplyApplicationDefaultsResponse{defaultsPreviewResponse()}}
	c := newTestCLI()
	c.dialOverride = startDefaultsAdminServer(t, stub)
	code := c.Run([]string{"defaults", "apply", "dev/my-app", "--from", writeDefaultsInput(t, "{}"), "--insecure", "--output", "json"})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, c.stderr())
	}
	const want = `{
  "profile": "dev",
  "plan_digest": "plan-123",
  "executed": false,
  "definition_changed": false,
  "definition_updated": false,
  "entries": [
    {
      "status": "create",
      "alias": "alpha",
      "key": "config/alpha",
      "content_type": "text/plain",
      "current_version": 0,
      "applied_version": 0,
      "revision": 0
    },
    {
      "status": "unchanged",
      "alias": "zeta",
      "key": "config/zeta",
      "content_type": "application/json",
      "current_version": 3,
      "applied_version": 0,
      "revision": 0
    }
  ],
  "missing_secrets": [
    "a-secret",
    "z-secret"
  ],
  "counts": {
    "create": 1,
    "unchanged": 1,
    "update": 0,
    "blocked": 0
  }
}
`
	if c.stdout() != want {
		t.Fatalf("defaults apply json =\n%s\nwant\n%s", c.stdout(), want)
	}
}

// TestDefaultsApplyExecuteJSONPrintsOnlyTheAppliedDocument pins rule 1 for the
// one command that renders two results: with --execute the preview is a human
// step, so JSON mode reports only what was actually applied.
func TestDefaultsApplyExecuteJSONPrintsOnlyTheAppliedDocument(t *testing.T) {
	preview := defaultsPreviewResponse()
	applied := defaultsPreviewResponse()
	applied.Executed = true
	applied.Entries[0].AppliedVersion = 3
	applied.Entries[1].AppliedVersion = 1
	stub := &defaultsAdminStub{responses: []*kmsv1.ApplyApplicationDefaultsResponse{preview, applied}}
	c := newTestCLI()
	c.dialOverride = startDefaultsAdminServer(t, stub)
	code := c.Run([]string{"defaults", "apply", "dev/app", "--from", writeDefaultsInput(t, "{}"), "--execute", "--insecure", "--output", "json"})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, c.stderr())
	}
	var document defaultsApplyJSON
	if err := json.Unmarshal([]byte(c.stdout()), &document); err != nil {
		t.Fatalf("stdout is not a single JSON document (%v):\n%s", err, c.stdout())
	}
	if !document.Executed {
		t.Fatalf("document = %+v", document)
	}
	if strings.Contains(c.stdout(), "Preview") || strings.Contains(c.stdout(), "Summary:") {
		t.Fatalf("human output reached stdout:\n%s", c.stdout())
	}
	if len(document.Entries) != 2 || document.Entries[0].Alias != "alpha" || document.Entries[0].AppliedVersion != 1 {
		t.Fatalf("entries = %+v", document.Entries)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.calls) != 2 {
		t.Fatalf("calls = %d, want preview plus execute", len(stub.calls))
	}
}

// A missing local artifact is a plain error, not "not found": 5 is reserved
// for the server's verdict on a store resource.
func TestDefaultsApplyMissingArtifactExitsError(t *testing.T) {
	stub := &defaultsAdminStub{}
	c := newTestCLI()
	c.dialOverride = startDefaultsAdminServer(t, stub)
	missing := filepath.Join(t.TempDir(), "absent.json")
	code := c.Run([]string{"defaults", "apply", "dev/app", "--from", missing, "--insecure"})
	if code != exitError {
		t.Fatalf("exit=%d want=%d stderr=%s", code, exitError, c.stderr())
	}
	if !strings.Contains(c.stderr(), "error: reading defaults artifact:") {
		t.Fatalf("stderr = %s", c.stderr())
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.calls) != 0 {
		t.Fatalf("an unreadable artifact must not reach the server: %d calls", len(stub.calls))
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("the fixture path must not exist")
	}
}
