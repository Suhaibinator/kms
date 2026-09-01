package cli

import (
	"context"
	"encoding/json/v2"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// releaseServiceStub answers the release RPCs the output and confirmation
// tests exercise. Every field is optional: a test sets only what its command
// touches, and an unset error means the canned response is returned.
type releaseServiceStub struct {
	kmsv1.UnimplementedConfigurationReleaseServiceServer

	mu sync.Mutex
	// releases is keyed by version so a diff can serve two manifests.
	releases      map[uint64]*kmsv1.ConfigurationRelease
	getErr        error
	list          []*kmsv1.ReleaseSummary
	listErr       error
	validate      *kmsv1.ValidateReleaseResponse
	validateErr   error
	active        *kmsv1.GetActiveReleaseResponse
	activeErr     error
	activate      *kmsv1.ActivateReleaseResponse
	activateErr   error
	activateCalls []*kmsv1.ActivateReleaseRequest
}

func (s *releaseServiceStub) GetRelease(_ context.Context, req *kmsv1.GetReleaseRequest) (*kmsv1.GetReleaseResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	release, ok := s.releases[req.GetVersion()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "release version %d not found", req.GetVersion())
	}
	return &kmsv1.GetReleaseResponse{Release: release}, nil
}

func (s *releaseServiceStub) ListReleases(context.Context, *kmsv1.ListReleasesRequest) (*kmsv1.ListReleasesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	return &kmsv1.ListReleasesResponse{Releases: s.list}, nil
}

func (s *releaseServiceStub) ValidateRelease(context.Context, *kmsv1.ValidateReleaseRequest) (*kmsv1.ValidateReleaseResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.validateErr != nil {
		return nil, s.validateErr
	}
	return s.validate, nil
}

func (s *releaseServiceStub) GetActiveRelease(context.Context, *kmsv1.GetActiveReleaseRequest) (*kmsv1.GetActiveReleaseResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeErr != nil {
		return nil, s.activeErr
	}
	return s.active, nil
}

func (s *releaseServiceStub) ActivateRelease(_ context.Context, req *kmsv1.ActivateReleaseRequest) (*kmsv1.ActivateReleaseResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activateCalls = append(s.activateCalls, proto.Clone(req).(*kmsv1.ActivateReleaseRequest))
	if s.activateErr != nil {
		return nil, s.activateErr
	}
	return s.activate, nil
}

func (s *releaseServiceStub) activations() []*kmsv1.ActivateReleaseRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*kmsv1.ActivateReleaseRequest(nil), s.activateCalls...)
}

// runRelease drives one release command against stub.
func runRelease(t *testing.T, stub *releaseServiceStub, args ...string) (int, *testCLI) {
	t.Helper()
	c := newTestCLI()
	c.dialOverride = startStubGRPC(t, func(s *grpc.Server) { kmsv1.RegisterConfigurationReleaseServiceServer(s, stub) })
	return c.Run(append([]string{"release"}, args...)), c
}

// releaseStdinFile hands a command a real *os.File as stdin, which is what the
// TTY path needs: confirm reads from c.Stdin, and only a file has a descriptor.
func releaseStdinFile(t *testing.T, content string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	return file
}

// requireOneJSONDocument fails unless stdout is exactly one JSON document and
// nothing else — the contract every command owes a script in --output json.
func requireOneJSONDocument(t *testing.T, c *testCLI) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(c.stdout()), &document); err != nil {
		t.Fatalf("stdout is not a single JSON document (%v):\n%s", err, c.stdout())
	}
	return document
}

func releaseFixture(version uint64, digest string) *kmsv1.ConfigurationRelease {
	return &kmsv1.ConfigurationRelease{
		Namespace: &kmsv1.NamespaceRef{Env: "prod", App: "app"},
		Name:      "runtime", Version: version,
		SchemaId: "app/runtime", SchemaVersion: 2,
		Digest: digest, CreatedAtUnixMs: 1700000000000,
		Entries: []*kmsv1.ConfigurationReleaseEntry{
			{
				Alias: "settings", Kind: "parameter",
				Ref:     &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: "prod", App: "app"}, Key: "config/settings"},
				Version: version + 1, ContentType: "json", ParameterDigest: "param-digest-" + digest,
			},
			{
				Alias: "password", Kind: "secret",
				Ref:          &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: "prod", App: "app"}, Key: "db-password"},
				Version:      7,
				MetadataJson: `{"do_not_print":"secret-plaintext"}`,
			},
		},
	}
}

func TestReleaseShowJSONIsTheWholeOfStdout(t *testing.T) {
	stub := &releaseServiceStub{releases: map[uint64]*kmsv1.ConfigurationRelease{3: releaseFixture(3, "d3")}}
	code, c := runRelease(t, stub, "show", "prod/app", "runtime", "3", "--insecure", "--output", "json")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, c.stderr())
	}
	const want = `{
  "namespace": {
    "env": "prod",
    "app": "app"
  },
  "name": "runtime",
  "version": 3,
  "schema": {
    "id": "app/runtime",
    "version": 2
  },
  "digest": "d3",
  "created_at": "2023-11-14T22:13:20Z",
  "entries": [
    {
      "alias": "password",
      "kind": "secret",
      "path": "/prod/app/db-password",
      "version": 7,
      "content_type": "",
      "parameter_digest": ""
    },
    {
      "alias": "settings",
      "kind": "parameter",
      "path": "/prod/app/config/settings",
      "version": 4,
      "content_type": "json",
      "parameter_digest": "param-digest-d3"
    }
  ]
}
`
	if c.stdout() != want {
		t.Fatalf("release show json =\n%s\nwant\n%s", c.stdout(), want)
	}
	if strings.Contains(c.stdout(), "secret-plaintext") {
		t.Fatalf("release show leaked secret metadata:\n%s", c.stdout())
	}
}

func TestReleaseShowNotFoundExitsFive(t *testing.T) {
	stub := &releaseServiceStub{getErr: status.Error(codes.NotFound, "release runtime version 9 not found")}
	code, c := runRelease(t, stub, "show", "prod/app", "runtime", "9", "--insecure")
	if code != exitNotFound {
		t.Fatalf("exit=%d want=%d stderr=%s", code, exitNotFound, c.stderr())
	}
	if !strings.Contains(c.stderr(), "error: release show:") {
		t.Fatalf("stderr = %s", c.stderr())
	}
	if c.stdout() != "" {
		t.Fatalf("a failure must not write to stdout: %q", c.stdout())
	}
}

func TestReleaseListJSONRendersAnEmptyListAsItems(t *testing.T) {
	code, c := runRelease(t, &releaseServiceStub{}, "list", "prod/app", "--insecure", "--output", "json")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, c.stderr())
	}
	if c.stdout() != "{\n  \"items\": []\n}\n" {
		t.Fatalf("empty list json = %q", c.stdout())
	}
}

func TestReleaseListJSONCarriesEveryTableColumn(t *testing.T) {
	stub := &releaseServiceStub{list: []*kmsv1.ReleaseSummary{
		{Release: releaseFixture(3, "d3"), Current: true, ActivationRevision: 42},
		{Release: releaseFixture(2, "d2"), Previous: true, ActivationRevision: 41},
	}}
	code, c := runRelease(t, stub, "list", "prod/app", "--insecure", "--output", "json")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, c.stderr())
	}
	var page struct {
		Items []releaseListItemJSON `json:"items"`
	}
	if err := json.Unmarshal([]byte(c.stdout()), &page); err != nil {
		t.Fatalf("%v:\n%s", err, c.stdout())
	}
	if len(page.Items) != 2 {
		t.Fatalf("items = %+v", page.Items)
	}
	first := page.Items[0]
	if first.Name != "runtime" || first.Version != 3 || !first.Current || first.Previous || first.Revision != 42 || first.Digest != "d3" {
		t.Fatalf("first item = %+v", first)
	}
	if first.CreatedAt == nil || *first.CreatedAt != "2023-11-14T22:13:20Z" {
		t.Fatalf("created_at = %v", first.CreatedAt)
	}
}

func TestReleaseDiffJSONSplitsAddedRemovedAndChanged(t *testing.T) {
	from := releaseFixture(1, "d1")
	from.Entries = append(from.Entries, &kmsv1.ConfigurationReleaseEntry{
		Alias: "gone", Kind: "parameter",
		Ref:     &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: "prod", App: "app"}, Key: "config/gone"},
		Version: 1, ParameterDigest: "gone-digest",
	})
	to := releaseFixture(2, "d2")
	to.Entries = append(to.Entries, &kmsv1.ConfigurationReleaseEntry{
		Alias: "fresh", Kind: "parameter",
		Ref:     &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: "prod", App: "app"}, Key: "config/fresh"},
		Version: 9, ParameterDigest: "fresh-digest",
	})
	stub := &releaseServiceStub{releases: map[uint64]*kmsv1.ConfigurationRelease{1: from, 2: to}}
	code, c := runRelease(t, stub, "diff", "prod/app", "runtime", "1", "2", "--insecure", "--output", "json")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, c.stderr())
	}
	var diff releaseDiff
	if err := json.Unmarshal([]byte(c.stdout()), &diff); err != nil {
		t.Fatalf("%v:\n%s", err, c.stdout())
	}
	if diff.From.Name != "runtime" || diff.From.Version != 1 || diff.To.Version != 2 {
		t.Fatalf("diff identity = %+v -> %+v", diff.From, diff.To)
	}
	if len(diff.Added) != 1 || diff.Added[0].Alias != "fresh" || diff.Added[0].Version != 9 {
		t.Fatalf("added = %+v", diff.Added)
	}
	if len(diff.Removed) != 1 || diff.Removed[0].Alias != "gone" {
		t.Fatalf("removed = %+v", diff.Removed)
	}
	// password is pinned to version 7 in both releases and must not show up as
	// a change; only the parameter whose digest moved does.
	if len(diff.Changed) != 1 || diff.Changed[0].Alias != "settings" {
		t.Fatalf("changed = %+v", diff.Changed)
	}
	change := diff.Changed[0]
	if change.From.Version != 2 || change.To.Version != 3 || change.From.ParameterDigest != "param-digest-d1" || change.To.ParameterDigest != "param-digest-d2" {
		t.Fatalf("changed entry = %+v", change)
	}
	if strings.Contains(c.stdout(), "secret-plaintext") {
		t.Fatalf("diff leaked secret metadata:\n%s", c.stdout())
	}
}

// TestReleaseDiffTableMatchesTheComputedDiff pins the human rendering: the
// table is built from the same computation the JSON document reports.
func TestReleaseDiffTableMatchesTheComputedDiff(t *testing.T) {
	from := releaseFixture(1, "d1")
	to := releaseFixture(2, "d2")
	to.Entries = append(to.Entries, &kmsv1.ConfigurationReleaseEntry{
		Alias: "fresh", Kind: "parameter",
		Ref:     &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: "prod", App: "app"}, Key: "config/fresh"},
		Version: 9, ParameterDigest: "fresh-digest",
	})
	stub := &releaseServiceStub{releases: map[uint64]*kmsv1.ConfigurationRelease{1: from, 2: to}}
	code, c := runRelease(t, stub, "diff", "prod/app", "runtime", "1", "2", "--insecure")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, c.stderr())
	}
	lines := strings.Split(strings.TrimSpace(c.stdout()), "\n")
	if len(lines) != 3 {
		t.Fatalf("rows = %d, want header plus fresh and settings:\n%s", len(lines), c.stdout())
	}
	if got := strings.Join(strings.Fields(lines[0]), "|"); got != "ALIAS|CHANGE|KIND|PATH|VERSION|PARAMETER|DIGEST" {
		t.Fatalf("header = %q", got)
	}
	if got := strings.Join(strings.Fields(lines[1]), "|"); got != "fresh|added|->|parameter|->|/prod/app/config/fresh|->|9|->|fresh-digest" {
		t.Fatalf("added row = %q", got)
	}
	if !strings.Contains(lines[2], "settings") || !strings.Contains(lines[2], "2 -> 3") {
		t.Fatalf("changed row = %q", lines[2])
	}
}

func TestReleaseValidateJSONReportsErrorsAndExitsOne(t *testing.T) {
	stub := &releaseServiceStub{validate: &kmsv1.ValidateReleaseResponse{Errors: []*kmsv1.ReleaseValidationError{
		{Alias: "settings", Code: "schema_violation", SchemaPointer: "/properties/retries", Message: "must be an integer"},
		{Code: "pin_unresolved", Message: "parameter version 4 no longer exists"},
	}}}
	code, c := runRelease(t, stub, "validate", "prod/app", "runtime", "3", "--insecure", "--output", "json")
	if code != 1 {
		t.Fatalf("exit=%d want 1, stderr=%s", code, c.stderr())
	}
	var report releaseValidateJSON
	if err := json.Unmarshal([]byte(c.stdout()), &report); err != nil {
		t.Fatalf("%v:\n%s", err, c.stdout())
	}
	if report.Valid || len(report.Errors) != 2 {
		t.Fatalf("report = %+v", report)
	}
	if report.Errors[0] != (releaseValidationErrorJSON{Alias: "settings", Code: "schema_violation", SchemaPointer: "/properties/retries", Message: "must be an integer"}) {
		t.Fatalf("first error = %+v", report.Errors[0])
	}
	if report.Errors[1].Alias != "" || report.Errors[1].Code != "pin_unresolved" {
		t.Fatalf("release-level error = %+v", report.Errors[1])
	}

	valid := &releaseServiceStub{validate: &kmsv1.ValidateReleaseResponse{Valid: true}}
	code, c = runRelease(t, valid, "validate", "prod/app", "runtime", "3", "--insecure", "--output", "json")
	if code != 0 {
		t.Fatalf("valid exit=%d stderr=%s", code, c.stderr())
	}
	document := requireOneJSONDocument(t, c)
	if document["valid"] != true {
		t.Fatalf("valid document = %v", document)
	}
	if items, ok := document["errors"].([]any); !ok || len(items) != 0 {
		t.Fatalf("errors must be an empty list, got %v", document["errors"])
	}
}

// failedValidationStatus is the error the server returns when an activation is
// refused because the release no longer validates: FailedPrecondition carrying
// the individual errors as a detail.
func failedValidationStatus(t *testing.T) error {
	t.Helper()
	st, err := status.New(codes.FailedPrecondition, "configuration release validation failed").
		WithDetails(&kmsv1.ValidateReleaseResponse{Errors: []*kmsv1.ReleaseValidationError{
			{Alias: "settings", Code: "schema_violation", Message: "must be an integer"},
		}})
	if err != nil {
		t.Fatal(err)
	}
	return st.Err()
}

func TestReleaseActivateValidationFailureExitsSeven(t *testing.T) {
	stub := &releaseServiceStub{
		activeErr:   status.Error(codes.NotFound, "no active release"),
		activateErr: failedValidationStatus(t),
	}
	code, c := runRelease(t, stub, "activate", "prod/app", "runtime", "3", "--insecure", "--yes")
	if code != exitFailedPrecondition {
		t.Fatalf("exit=%d want=%d stderr=%s", code, exitFailedPrecondition, c.stderr())
	}
	for _, want := range []string{"error: release activate: configuration release validation failed", "SCHEMA POINTER", "schema_violation", "must be an integer"} {
		if !strings.Contains(c.stderr(), want) {
			t.Fatalf("stderr missing %q:\n%s", want, c.stderr())
		}
	}
	if c.stdout() != "" {
		t.Fatalf("a refused activation must not write to stdout: %q", c.stdout())
	}
}

func TestReleaseActivatePreviewsTheDiffAndRequiresConfirmation(t *testing.T) {
	active := releaseFixture(2, "d2")
	requested := releaseFixture(3, "d3")
	newStub := func() *releaseServiceStub {
		return &releaseServiceStub{
			releases: map[uint64]*kmsv1.ConfigurationRelease{2: active, 3: requested},
			active:   &kmsv1.GetActiveReleaseResponse{Release: active, ActivationRevision: 41, PreviousVersion: 1},
			activate: &kmsv1.ActivateReleaseResponse{CurrentVersion: 3, PreviousVersion: 2, ActivationRevision: 42, Changed: true},
		}
	}

	t.Run("non-interactive without --yes is refused", func(t *testing.T) {
		stub := newStub()
		code, c := runRelease(t, stub, "activate", "prod/app", "runtime", "3", "--insecure")
		if code != exitUsage {
			t.Fatalf("exit=%d want=%d stderr=%s", code, exitUsage, c.stderr())
		}
		if len(stub.activations()) != 0 {
			t.Fatalf("a refused activation must not reach the server: %+v", stub.activations())
		}
		for _, want := range []string{
			"Activating runtime v3 in prod/app over the active v2:",
			"ALIAS", "settings", "3 -> 4",
			"refusing to activate release runtime v3 in prod/app without --yes",
		} {
			if !strings.Contains(c.stderr(), want) {
				t.Fatalf("stderr missing %q:\n%s", want, c.stderr())
			}
		}
		if c.stdout() != "" {
			t.Fatalf("the preview belongs on stderr, stdout = %q", c.stdout())
		}
	})

	t.Run("--yes activates", func(t *testing.T) {
		stub := newStub()
		code, c := runRelease(t, stub, "activate", "prod/app", "runtime", "3", "--insecure", "--yes")
		if code != 0 {
			t.Fatalf("exit=%d stderr=%s", code, c.stderr())
		}
		calls := stub.activations()
		if len(calls) != 1 || calls[0].GetVersion() != 3 || calls[0].GetName() != "runtime" {
			t.Fatalf("activations = %+v", calls)
		}
		if !strings.Contains(c.stdout(), "Active prod/app/runtime version 3 (previous 2, revision 42, changed=true)") {
			t.Fatalf("stdout = %q", c.stdout())
		}
		if !strings.Contains(c.stderr(), "Activating runtime v3 in prod/app over the active v2:") {
			t.Fatalf("the preview must be printed even with --yes:\n%s", c.stderr())
		}
	})

	t.Run("--quiet never suppresses the preview", func(t *testing.T) {
		stub := newStub()
		code, c := runRelease(t, stub, "activate", "prod/app", "runtime", "3", "--insecure", "--yes", "--quiet")
		if code != 0 {
			t.Fatalf("exit=%d stderr=%s", code, c.stderr())
		}
		if !strings.Contains(c.stderr(), "Activating runtime v3 in prod/app over the active v2:") {
			t.Fatalf("stderr = %q", c.stderr())
		}
	})

	t.Run("json mode keeps the preview off stdout", func(t *testing.T) {
		stub := newStub()
		code, c := runRelease(t, stub, "activate", "prod/app", "runtime", "3", "--insecure", "--yes", "--output", "json")
		if code != 0 {
			t.Fatalf("exit=%d stderr=%s", code, c.stderr())
		}
		document := requireOneJSONDocument(t, c)
		if document["version"] != 3.0 || document["previous_version"] != 2.0 || document["revision"] != 42.0 || document["changed"] != true {
			t.Fatalf("activation document = %v", document)
		}
		if !strings.Contains(c.stderr(), "ALIAS") {
			t.Fatalf("the preview belongs on stderr:\n%s", c.stderr())
		}
		// The result line still reaches the operator, just not on stdout.
		if !strings.Contains(c.stderr(), "Active prod/app/runtime version 3 (previous 2, revision 42, changed=true)") {
			t.Fatalf("stderr missing the result line:\n%s", c.stderr())
		}
	})

	t.Run("--quiet silences the json result line but not the preview", func(t *testing.T) {
		stub := newStub()
		code, c := runRelease(t, stub, "activate", "prod/app", "runtime", "3", "--insecure", "--yes", "--quiet", "--output", "json")
		if code != 0 {
			t.Fatalf("exit=%d stderr=%s", code, c.stderr())
		}
		requireOneJSONDocument(t, c)
		if strings.Contains(c.stderr(), "Active prod/app/runtime version 3") {
			t.Fatalf("--quiet must silence the informational result line:\n%s", c.stderr())
		}
		if !strings.Contains(c.stderr(), "Activating runtime v3 in prod/app over the active v2:") {
			t.Fatalf("the preview is never suppressed:\n%s", c.stderr())
		}
	})

	t.Run("no active release says so instead of diffing", func(t *testing.T) {
		stub := newStub()
		stub.active = nil
		stub.activeErr = status.Error(codes.NotFound, "no active release")
		code, c := runRelease(t, stub, "activate", "prod/app", "runtime", "3", "--insecure", "--yes")
		if code != 0 {
			t.Fatalf("exit=%d stderr=%s", code, c.stderr())
		}
		if !strings.Contains(c.stderr(), "No active release in prod/app; runtime v3 will become the first.") {
			t.Fatalf("stderr = %q", c.stderr())
		}
		if strings.Contains(c.stderr(), "ALIAS") {
			t.Fatalf("there is nothing to diff against:\n%s", c.stderr())
		}
	})

	t.Run("a broken active lookup is an error", func(t *testing.T) {
		stub := newStub()
		stub.active = nil
		stub.activeErr = status.Error(codes.Unavailable, "server is starting")
		code, c := runRelease(t, stub, "activate", "prod/app", "runtime", "3", "--insecure", "--yes")
		if code != exitUnavailable {
			t.Fatalf("exit=%d want=%d stderr=%s", code, exitUnavailable, c.stderr())
		}
		if len(stub.activations()) != 0 {
			t.Fatalf("a failed preview must not activate: %+v", stub.activations())
		}
	})
}

func TestReleaseRollbackRequiresTypedConfirmation(t *testing.T) {
	newStub := func() *releaseServiceStub {
		return &releaseServiceStub{
			active:   &kmsv1.GetActiveReleaseResponse{Release: releaseFixture(3, "d3"), ActivationRevision: 42, PreviousVersion: 2},
			activate: &kmsv1.ActivateReleaseResponse{CurrentVersion: 2, PreviousVersion: 3, ActivationRevision: 43, Changed: true},
		}
	}

	t.Run("non-interactive without --yes is refused", func(t *testing.T) {
		stub := newStub()
		code, c := runRelease(t, stub, "rollback", "prod/app", "runtime", "--insecure")
		if code != exitUsage {
			t.Fatalf("exit=%d want=%d stderr=%s", code, exitUsage, c.stderr())
		}
		if len(stub.activations()) != 0 {
			t.Fatalf("a refused rollback must not reach the server: %+v", stub.activations())
		}
		if !strings.Contains(c.stderr(), "refusing to roll back the active release of prod/app without --yes") {
			t.Fatalf("stderr = %s", c.stderr())
		}
	})

	t.Run("--yes rolls back to the previous version", func(t *testing.T) {
		stub := newStub()
		code, c := runRelease(t, stub, "rollback", "prod/app", "runtime", "--insecure", "--yes")
		if code != 0 {
			t.Fatalf("exit=%d stderr=%s", code, c.stderr())
		}
		calls := stub.activations()
		if len(calls) != 1 || calls[0].GetVersion() != 2 || calls[0].GetExpectedCurrentVersion() != 3 {
			t.Fatalf("activations = %+v", calls)
		}
		if !strings.Contains(c.stdout(), "Rolled back prod/app/runtime to version 2 (revision 43)") {
			t.Fatalf("stdout = %q", c.stdout())
		}
	})

	t.Run("typing the namespace at a terminal confirms", func(t *testing.T) {
		stub := newStub()
		c := newTestCLI()
		c.dialOverride = startStubGRPC(t, func(s *grpc.Server) { kmsv1.RegisterConfigurationReleaseServiceServer(s, stub) })
		c.isTTY = func() bool { return true }
		c.Stdin = releaseStdinFile(t, "prod/app\n")
		if code := c.Run([]string{"release", "rollback", "prod/app", "runtime", "--insecure"}); code != 0 {
			t.Fatalf("exit=%d stderr=%s", code, c.stderr())
		}
		if len(stub.activations()) != 1 {
			t.Fatalf("activations = %+v", stub.activations())
		}
		if !strings.Contains(c.stderr(), `Type "prod/app" to confirm:`) {
			t.Fatalf("prompt = %q", c.stderr())
		}
	})

	t.Run("a mismatched answer aborts", func(t *testing.T) {
		stub := newStub()
		c := newTestCLI()
		c.dialOverride = startStubGRPC(t, func(s *grpc.Server) { kmsv1.RegisterConfigurationReleaseServiceServer(s, stub) })
		c.isTTY = func() bool { return true }
		c.Stdin = releaseStdinFile(t, "prod/other\n")
		if code := c.Run([]string{"release", "rollback", "prod/app", "runtime", "--insecure"}); code != exitUsage {
			t.Fatalf("exit=%d want=%d stderr=%s", code, exitUsage, c.stderr())
		}
		if len(stub.activations()) != 0 {
			t.Fatalf("a mismatched confirmation must not roll back: %+v", stub.activations())
		}
		if !strings.Contains(c.stderr(), `confirmation "prod/other" does not match "prod/app"`) {
			t.Fatalf("stderr = %q", c.stderr())
		}
	})

	t.Run("nothing to roll back to is refused before the prompt", func(t *testing.T) {
		stub := newStub()
		stub.active = &kmsv1.GetActiveReleaseResponse{Release: releaseFixture(1, "d1")}
		code, c := runRelease(t, stub, "rollback", "prod/app", "runtime", "--insecure")
		if code != 1 || !strings.Contains(c.stderr(), "no previous release is available") {
			t.Fatalf("exit=%d stderr=%s", code, c.stderr())
		}
	})

	t.Run("a failed active lookup carries the server's exit code", func(t *testing.T) {
		stub := newStub()
		stub.active = nil
		stub.activeErr = status.Error(codes.PermissionDenied, "identity may not read releases")
		code, c := runRelease(t, stub, "rollback", "prod/app", "runtime", "--insecure", "--yes")
		if code != exitPermissionDenied {
			t.Fatalf("exit=%d want=%d stderr=%s", code, exitPermissionDenied, c.stderr())
		}
	})
}

func TestReleaseVerifyDefaultsJSONKeepsItsOwnExitCodes(t *testing.T) {
	stub := &verifyReleaseStub{response: &kmsv1.VerifyReleaseDefaultsResponse{
		Name: "runtime", Version: 3, ActivationRevision: 42, SchemaMatches: false,
		Entries:      []*kmsv1.VerifyEntryVerdict{{Alias: "greeting", Verdict: "differs"}, {Alias: "settings", Verdict: "match"}},
		MatchCount:   1,
		DiffersCount: 1,
	}}
	c := newTestCLI()
	c.dialOverride = startStubGRPC(t, func(s *grpc.Server) { kmsv1.RegisterConfigurationReleaseServiceServer(s, stub) })
	code := c.Run([]string{"release", "verify-defaults", "prod/gradethis", "--artifact", writeVerifyArtifact(t), "--insecure", "--output", "json"})
	if code != 1 {
		t.Fatalf("exit=%d want 1, stderr=%s", code, c.stderr())
	}
	var report releaseVerifyDefaultsJSON
	if err := json.Unmarshal([]byte(c.stdout()), &report); err != nil {
		t.Fatalf("%v:\n%s", err, c.stdout())
	}
	if report.Name != "runtime" || report.Version != 3 || report.ActivationRevision != 42 {
		t.Fatalf("report identity = %+v", report)
	}
	if report.Schema != "mismatch" || report.Clean {
		t.Fatalf("schema=%q clean=%t", report.Schema, report.Clean)
	}
	if report.Counts.Match != 1 || report.Counts.Differs != 1 {
		t.Fatalf("counts = %+v", report.Counts)
	}
	if len(report.Entries) != 2 || report.Entries[0].Verdict != "differs" {
		t.Fatalf("entries = %+v", report.Entries)
	}
}
