package cli

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
)

// auditStub is an AdminService that records every ListAuditEvents request and
// answers the calls in order from a fixed script, so a test can pin both what
// the CLI sent and what it did with the reply. Once the script runs out the
// last page repeats, which is what a tail against a quiet server sees.
type auditStub struct {
	kmsv1.UnimplementedAdminServiceServer
	mu    sync.Mutex
	calls []*kmsv1.ListAuditEventsRequest
	pages []*kmsv1.ListAuditEventsResponse
	// stopAfter closes stop once that many calls have been served, which is how
	// a --follow test ends the tail deterministically instead of signalling the
	// test binary.
	stopAfter int
	stop      chan struct{}
	stopOnce  sync.Once
}

func newAuditStub(pages ...*kmsv1.ListAuditEventsResponse) *auditStub {
	return &auditStub{pages: pages, stop: make(chan struct{})}
}

func (s *auditStub) ListAuditEvents(_ context.Context, req *kmsv1.ListAuditEventsRequest) (*kmsv1.ListAuditEventsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, proto.Clone(req).(*kmsv1.ListAuditEventsRequest))
	if s.stopAfter > 0 && len(s.calls) >= s.stopAfter {
		s.stopOnce.Do(func() { close(s.stop) })
	}
	if len(s.pages) == 0 {
		return &kmsv1.ListAuditEventsResponse{}, nil
	}
	page := len(s.calls) - 1
	if page >= len(s.pages) {
		page = len(s.pages) - 1
	}
	return s.pages[page], nil
}

func (s *auditStub) request(t *testing.T, n int) *kmsv1.ListAuditEventsRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) <= n {
		t.Fatalf("call %d not made; %d calls recorded", n, len(s.calls))
	}
	return s.calls[n]
}

func (s *auditStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// auditCLI wires a test CLI to a stub AdminService.
func auditCLI(t *testing.T, stub *auditStub) *testCLI {
	t.Helper()
	c := newTestCLI()
	c.dialOverride = startStubGRPC(t, func(s *grpc.Server) { kmsv1.RegisterAdminServiceServer(s, stub) })
	return c
}

// sampleAuditEvent is one fully populated namespaced event, so a table row and
// a JSON record can both be pinned against known values.
func sampleAuditEvent(id int64, at time.Time) *kmsv1.AuditEvent {
	return &kmsv1.AuditEvent{
		Id:              id,
		EventType:       "secret.read",
		ActorIdentity:   "gradethis-be",
		ActorType:       "client",
		ResourceType:    "secret",
		ResourceEnv:     "prod",
		ResourceApp:     "gradethis",
		ResourceKey:     "db-password",
		ResourceVersion: 3,
		Decision:        "allow",
		SourceIp:        "10.0.0.4",
		UserAgent:       "kms-go/1.0",
		RequestId:       fmt.Sprintf("req-%d", id),
		CreatedAtUnixMs: at.UnixMilli(),
		MetadataJson:    `{"label":"current"}`,
	}
}

// TestAuditListForwardsEveryFilter: the filters are the whole command. A flag
// that silently failed to reach the server would turn a narrow question into a
// wide answer, which reads as a clean audit log.
func TestAuditListForwardsEveryFilter(t *testing.T) {
	stub := newAuditStub(&kmsv1.ListAuditEventsResponse{})
	c := auditCLI(t, stub)
	before := time.Now()
	code := c.Run([]string{"audit", "list", "--insecure", "--token", "admin-token",
		"--env", "prod", "--app", "gradethis", "--key-prefix", "db/",
		"--actor", "ops", "--event", "secret.read", "--decision", "deny",
		"--since", "24h", "--until", "1h", "--limit", "25"})
	after := time.Now()
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, c.stderr())
	}
	req := stub.request(t, 0)
	if req.GetEnv() != "prod" || req.GetApp() != "gradethis" || req.GetKeyPrefix() != "db/" {
		t.Fatalf("namespace filters = %+v", req)
	}
	if req.GetActorIdentity() != "ops" || req.GetEventType() != "secret.read" || req.GetDecision() != "deny" {
		t.Fatalf("actor/event/decision filters = %+v", req)
	}
	if req.GetPageSize() != 25 {
		t.Fatalf("page size = %d, want 25", req.GetPageSize())
	}
	// --since 24h is relative to the moment the command resolved it, which the
	// test brackets rather than pins.
	lo, hi := before.Add(-24*time.Hour).UnixMilli(), after.Add(-24*time.Hour).UnixMilli()
	if req.GetFromUnixMs() < lo || req.GetFromUnixMs() > hi {
		t.Fatalf("from_unix_ms = %d, want within [%d,%d] (now - 24h)", req.GetFromUnixMs(), lo, hi)
	}
	lo, hi = before.Add(-time.Hour).UnixMilli(), after.Add(-time.Hour).UnixMilli()
	if req.GetToUnixMs() < lo || req.GetToUnixMs() > hi {
		t.Fatalf("to_unix_ms = %d, want within [%d,%d] (now - 1h)", req.GetToUnixMs(), lo, hi)
	}
}

// TestAuditListAcceptsAnAbsoluteWindow: an operator investigating an incident
// names instants, not durations, so the RFC 3339 spelling has to reach the wire
// unchanged.
func TestAuditListAcceptsAnAbsoluteWindow(t *testing.T) {
	stub := newAuditStub(&kmsv1.ListAuditEventsResponse{})
	c := auditCLI(t, stub)
	from := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	to := from.Add(90 * time.Minute)
	code := c.Run([]string{"audit", "list", "--insecure", "--token", "t",
		"--since", from.Format(time.RFC3339), "--until", to.Format(time.RFC3339)})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, c.stderr())
	}
	req := stub.request(t, 0)
	if req.GetFromUnixMs() != from.UnixMilli() || req.GetToUnixMs() != to.UnixMilli() {
		t.Fatalf("window = [%d,%d], want [%d,%d]", req.GetFromUnixMs(), req.GetToUnixMs(), from.UnixMilli(), to.UnixMilli())
	}
	if req.GetPageSize() != auditListDefaultLimit {
		t.Fatalf("default page size = %d, want %d", req.GetPageSize(), auditListDefaultLimit)
	}
}

func TestAuditListRejectsInvalidInvocations(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"decision", []string{"--decision", "bogus"}, "invalid --decision"},
		{"follow with page token", []string{"--follow", "--page-token", "x"}, "cannot resume a --page-token"},
		{"limit above the cap", []string{"--limit", "1001"}, "--limit must be between"},
		{"interval below the floor", []string{"--follow", "--interval", "10ms"}, "--interval must be at least"},
		{"window inverted", []string{"--since", "1h", "--until", "2h"}, "is before --since"},
		{"since unparseable", []string{"--since", "yesterday"}, "invalid --since"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newAuditStub()
			c := auditCLI(t, stub)
			args := append([]string{"audit", "list", "--insecure", "--token", "t"}, tc.args...)
			if code := c.Run(args); code != 2 {
				t.Fatalf("exit = %d, want 2; stderr = %s", code, c.stderr())
			}
			if !strings.Contains(c.stderr(), tc.want) {
				t.Fatalf("stderr = %q, want it to mention %q", c.stderr(), tc.want)
			}
			if stub.callCount() != 0 {
				t.Fatalf("a rejected invocation still contacted the server (%d calls)", stub.callCount())
			}
		})
	}
}

// TestAuditListTableColumns pins the human output: the column order, the UTC
// RFC 3339 timestamp, the collapsed namespace, and the "-" a global event
// leaves in every column it cannot fill.
func TestAuditListTableColumns(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	global := &kmsv1.AuditEvent{
		Id: 8, EventType: "auth.failure", Decision: "deny", ActorType: "unknown",
		CreatedAtUnixMs: at.Add(time.Minute).UnixMilli(), MetadataJson: `{"reason":"credential_mismatch"}`,
	}
	stub := newAuditStub(&kmsv1.ListAuditEventsResponse{
		Events:        []*kmsv1.AuditEvent{global, sampleAuditEvent(7, at)},
		NextPageToken: "next-page",
	})
	c := auditCLI(t, stub)
	if code := c.Run([]string{"audit", "list", "--insecure", "--token", "admin-token"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, c.stderr())
	}
	lines := strings.Split(strings.TrimRight(c.stdout(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("table = %q, want a header and two rows", c.stdout())
	}
	want := []string{
		"TIME|EVENT|DECISION|ACTOR|NAMESPACE|KEY|REQUEST_ID",
		"2026-03-04T05:07:07Z|auth.failure|deny|-|-|-|-",
		"2026-03-04T05:06:07Z|secret.read|allow|gradethis-be|prod/gradethis|db-password|req-7",
	}
	for i, w := range want {
		if got := strings.Join(strings.Fields(lines[i]), "|"); got != w {
			t.Fatalf("line %d = %q, want %q", i, got, w)
		}
	}
	// The continuation token is advice, not a result: it belongs on stderr so a
	// piped table stays parseable.
	if !strings.Contains(c.stderr(), "--page-token next-page") {
		t.Fatalf("stderr does not offer the continuation token: %q", c.stderr())
	}
}

// TestAuditListJSONIsTheCanonicalRecord: `audit list -o json`, `audit export`,
// and the server archive must be one format, so the JSON has to decode back
// into the same core.AuditRecord the other two write — metadata included.
func TestAuditListJSONIsTheCanonicalRecord(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 123000000, time.UTC)
	stub := newAuditStub(&kmsv1.ListAuditEventsResponse{
		Events:        []*kmsv1.AuditEvent{sampleAuditEvent(7, at)},
		NextPageToken: "next-page",
	})
	c := auditCLI(t, stub)
	if code := c.Run([]string{"-o", "json", "audit", "list", "--insecure", "--token", "admin-token"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, c.stderr())
	}
	var doc struct {
		Items         []core.AuditRecord `json:"items"`
		NextPageToken string             `json:"next_page_token"`
	}
	if err := json.Unmarshal([]byte(c.stdout()), &doc); err != nil {
		t.Fatalf("decode %q: %v", c.stdout(), err)
	}
	if doc.NextPageToken != "next-page" || len(doc.Items) != 1 {
		t.Fatalf("document = %+v", doc)
	}
	got := doc.Items[0]
	if got.ID != 7 || got.Event != "secret.read" || got.Decision != "allow" {
		t.Fatalf("record identity = %+v", got)
	}
	if !got.CreatedAt.Equal(at) {
		t.Fatalf("created_at = %s, want %s", got.CreatedAt, at)
	}
	if got.Actor != (core.AuditActor{Identity: "gradethis-be", Type: "client"}) {
		t.Fatalf("actor = %+v", got.Actor)
	}
	wantResource := core.AuditResource{Type: "secret", Env: "prod", App: "gradethis", Key: "db-password", Version: 3}
	if got.Resource != wantResource {
		t.Fatalf("resource = %+v, want %+v", got.Resource, wantResource)
	}
	if got.SourceIP != "10.0.0.4" || got.UserAgent != "kms-go/1.0" || got.RequestID != "req-7" {
		t.Fatalf("provenance fields = %+v", got)
	}
	var metadata map[string]string
	if err := json.Unmarshal(got.Metadata, &metadata); err != nil {
		t.Fatalf("metadata %q is not the JSON object the row held: %v", got.Metadata, err)
	}
	if metadata["label"] != "current" {
		t.Fatalf("metadata = %v, want the row's label", metadata)
	}
}

// TestAuditListFollowPrintsOldestFirstAndNeverRepeats is the tail's contract:
// the first page is reversed into reading order, the next poll asks from the
// newest event it has seen, and rows it already printed are dropped by id.
func TestAuditListFollowPrintsOldestFirstAndNeverRepeats(t *testing.T) {
	base := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	first := &kmsv1.ListAuditEventsResponse{Events: []*kmsv1.AuditEvent{
		sampleAuditEvent(2, base.Add(time.Second)),
		sampleAuditEvent(1, base),
	}}
	// The second poll re-fetches the boundary event (the millisecond bound on
	// the wire is inclusive and coarse) plus one genuinely new row.
	second := &kmsv1.ListAuditEventsResponse{Events: []*kmsv1.AuditEvent{
		sampleAuditEvent(3, base.Add(2*time.Second)),
		sampleAuditEvent(2, base.Add(time.Second)),
	}}
	stub := newAuditStub(first, second)
	stub.stopAfter = 2
	c := auditCLI(t, stub)
	c.followStop = stub.stop

	done := make(chan int, 1)
	go func() {
		done <- c.Run([]string{"audit", "list", "--follow", "--interval", "1s", "--insecure", "--token", "admin-token"})
	}()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit = %d, stderr = %s", code, c.stderr())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("follow did not stop; stdout = %q", c.stdout())
	}

	lines := strings.Split(strings.TrimRight(c.stdout(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("tail = %q, want a header and three rows", c.stdout())
	}
	for i, want := range []string{"req-1", "req-2", "req-3"} {
		if !strings.Contains(lines[i+1], want) {
			t.Fatalf("row %d = %q, want %s (oldest first, no repeats)", i, lines[i+1], want)
		}
	}
	if stub.callCount() != 2 {
		t.Fatalf("polls = %d, want 2", stub.callCount())
	}
	if from := stub.request(t, 1).GetFromUnixMs(); from != base.Add(time.Second).UnixMilli() {
		t.Fatalf("second poll from_unix_ms = %d, want the newest seen event %d", from, base.Add(time.Second).UnixMilli())
	}
}

// TestAuditListFollowJSONEmitsJSONLines: a stream has no closing bracket, so a
// tail writes one record per line rather than the single document every other
// JSON result is.
func TestAuditListFollowJSONEmitsJSONLines(t *testing.T) {
	base := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	stub := newAuditStub(&kmsv1.ListAuditEventsResponse{Events: []*kmsv1.AuditEvent{
		sampleAuditEvent(2, base.Add(time.Second)),
		sampleAuditEvent(1, base),
	}})
	stub.stopAfter = 1
	c := auditCLI(t, stub)
	c.followStop = stub.stop
	if code := c.Run([]string{"-o", "json", "audit", "list", "--follow", "--insecure", "--token", "admin-token"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, c.stderr())
	}
	lines := strings.Split(strings.TrimRight(c.stdout(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("tail = %q, want one JSON object per line", c.stdout())
	}
	for i, line := range lines {
		var record core.AuditRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line %d %q is not a record: %v", i, line, err)
		}
		if record.ID != int64(i+1) {
			t.Fatalf("line %d id = %d, want %d (oldest first)", i, record.ID, i+1)
		}
	}
}

// --- export ----------------------------------------------------------------

func TestAuditExportStreamsEveryPageToAPrivateFile(t *testing.T) {
	base := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	stub := newAuditStub(
		&kmsv1.ListAuditEventsResponse{
			Events:        []*kmsv1.AuditEvent{sampleAuditEvent(3, base.Add(2*time.Second)), sampleAuditEvent(2, base.Add(time.Second))},
			NextPageToken: "page-2",
		},
		&kmsv1.ListAuditEventsResponse{Events: []*kmsv1.AuditEvent{sampleAuditEvent(1, base)}},
	)
	c := auditCLI(t, stub)
	out := filepath.Join(t.TempDir(), "audit.jsonl")
	if code := c.Run([]string{"audit", "export", "--out", out, "--insecure", "--token", "admin-token"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, c.stderr())
	}
	if !strings.Contains(c.stdout(), "Exported 3 audit events to "+out) {
		t.Fatalf("result line = %q", c.stdout())
	}
	if stub.request(t, 0).GetPageSize() != auditExportPageSize {
		t.Fatalf("page size = %d, want the server maximum", stub.request(t, 0).GetPageSize())
	}
	if token := stub.request(t, 1).GetPageToken(); token != "page-2" {
		t.Fatalf("second page token = %q", token)
	}

	ids := auditExportIDs(t, out)
	if len(ids) != 3 {
		t.Fatalf("export holds %d records, want 3", len(ids))
	}
	// Pages arrive newest first and are written verbatim, so the file mirrors
	// the listing order rather than re-sorting it.
	for i, want := range []int64{3, 2, 1} {
		if ids[i] != want {
			t.Fatalf("record %d id = %d, want %d", i, ids[i], want)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(out)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("export mode = %#o, want 0600", mode)
		}
	}
	if entries := auditStagingLeftovers(t, filepath.Dir(out)); len(entries) != 0 {
		t.Fatalf("staging files left behind: %v", entries)
	}
}

// TestAuditExportRefusesToOverwrite: an export is evidence. A second run over
// the same path must fail as a conflict and leave the first one byte for byte.
func TestAuditExportRefusesToOverwrite(t *testing.T) {
	base := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	page := &kmsv1.ListAuditEventsResponse{Events: []*kmsv1.AuditEvent{sampleAuditEvent(1, base)}}
	out := filepath.Join(t.TempDir(), "audit.jsonl")

	first := auditCLI(t, newAuditStub(page))
	if code := first.Run([]string{"audit", "export", "--out", out, "--insecure", "--token", "t"}); code != 0 {
		t.Fatalf("first export exit = %d, stderr = %s", code, first.stderr())
	}
	original, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}

	stub := newAuditStub(&kmsv1.ListAuditEventsResponse{Events: []*kmsv1.AuditEvent{sampleAuditEvent(2, base)}})
	second := auditCLI(t, stub)
	code := second.Run([]string{"audit", "export", "--out", out, "--insecure", "--token", "t"})
	if code != exitConflict {
		t.Fatalf("second export exit = %d, want %d (conflict); stderr = %s", code, exitConflict, second.stderr())
	}
	if !strings.Contains(second.stderr(), out) {
		t.Fatalf("refusal does not name the path: %q", second.stderr())
	}
	if stub.callCount() != 0 {
		t.Fatalf("a refused export still queried the server (%d calls)", stub.callCount())
	}
	again, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(original) {
		t.Fatalf("the refused export modified the existing file")
	}
}

func TestAuditExportRequiresOut(t *testing.T) {
	stub := newAuditStub()
	c := auditCLI(t, stub)
	if code := c.Run([]string{"audit", "export", "--insecure", "--token", "t"}); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %s", code, c.stderr())
	}
	if !strings.Contains(c.stderr(), "--out is required") {
		t.Fatalf("stderr = %q", c.stderr())
	}
}

func TestAuditExportJSONNamesTheFile(t *testing.T) {
	base := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	stub := newAuditStub(&kmsv1.ListAuditEventsResponse{Events: []*kmsv1.AuditEvent{sampleAuditEvent(1, base)}})
	c := auditCLI(t, stub)
	out := filepath.Join(t.TempDir(), "audit.jsonl")
	if code := c.Run([]string{"-o", "json", "audit", "export", "--out", out, "--insecure", "--token", "t"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, c.stderr())
	}
	var document auditExportJSON
	if err := json.Unmarshal([]byte(c.stdout()), &document); err != nil {
		t.Fatalf("decode %q: %v", c.stdout(), err)
	}
	if document.Path != out || document.Count != 1 {
		t.Fatalf("document = %+v", document)
	}
}

// auditExportIDs reads back the record ids an export file holds, which also
// proves every line is one parseable canonical record.
func auditExportIDs(t *testing.T, path string) []int64 {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open export: %v", err)
	}
	defer func() { _ = f.Close() }()
	var ids []int64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var record core.AuditRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("line %q is not an audit record: %v", scanner.Text(), err)
		}
		ids = append(ids, record.ID)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read export: %v", err)
	}
	return ids
}

// auditStagingLeftovers names the dot-prefixed staging files an interrupted or
// completed export must not leave in the destination directory.
func auditStagingLeftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var leftovers []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".kms-audit-export-") {
			leftovers = append(leftovers, entry.Name())
		}
	}
	return leftovers
}

// TestAuditUsageAndUnknownSubcommand keeps the dispatch honest: no arguments
// and a typo are usage errors, and `help` is not.
func TestAuditUsageAndUnknownSubcommand(t *testing.T) {
	c := newTestCLI()
	if code := c.Run([]string{"audit"}); code != 2 {
		t.Fatalf("bare `audit` exit = %d, want 2", code)
	}
	c = newTestCLI()
	if code := c.Run([]string{"audit", "frobnicate"}); code != 2 {
		t.Fatalf("unknown subcommand exit = %d, want 2", code)
	}
	if !strings.Contains(c.stderr(), `unknown audit subcommand "frobnicate"`) {
		t.Fatalf("stderr = %q", c.stderr())
	}
	c = newTestCLI()
	if code := c.Run([]string{"audit", "help"}); code != 0 {
		t.Fatalf("`audit help` exit = %d, want 0", code)
	}
}

// TestParseSinceUntilGrammar pins the three accepted spellings and that the
// relative ones read as "that long ago".
func TestParseSinceUntilGrammar(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Time
	}{
		{"", time.Time{}},
		{"24h", now.Add(-24 * time.Hour)},
		{"7d", now.AddDate(0, 0, -7)},
		{"2026-01-02T03:04:05Z", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
	}
	for _, tc := range cases {
		got, err := parseSinceUntil("--since", tc.in, now)
		if err != nil {
			t.Fatalf("parseSinceUntil(%q) error = %v", tc.in, err)
		}
		if !got.Equal(tc.want) {
			t.Fatalf("parseSinceUntil(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"yesterday", "-1h", "2026-01-02", "3x"} {
		if _, err := parseSinceUntil("--since", bad, now); err == nil {
			t.Fatalf("parseSinceUntil(%q) accepted an unparseable bound", bad)
		}
	}
}
