package cli

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

// testNS is the destination namespace shared by the import entry tests.
var testNS = domain.NamespaceRef{Env: "prod", App: "gradethis"}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"gradethis_TWILIO_ACCOUNT_SID": "gradethis-twilio-account-sid",
		"STRIPE_API_KEY":               "stripe-api-key",
		"already-lower":                "already-lower",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildImportEntriesHappy(t *testing.T) {
	items := []kv{{Key: "FOO", Value: "1"}, {Key: "BAR_BAZ", Value: "2"}}
	entries, err := buildImportEntries(testNS, items)
	if err != nil {
		t.Fatalf("buildImportEntries: %v", err)
	}
	// Sorted by key: BAR_BAZ < FOO.
	if entries[0].Key != "BAR_BAZ" || entries[0].Ref.String() != "/prod/gradethis/bar-baz" {
		t.Fatalf("entry0 = %+v", entries[0])
	}
	if entries[1].Ref.String() != "/prod/gradethis/foo" {
		t.Fatalf("entry1 = %+v", entries[1])
	}
}

func TestBuildImportEntriesCollision(t *testing.T) {
	// "A_B" and "a-b" both slug to "a-b".
	items := []kv{{Key: "A_B", Value: "1"}, {Key: "a-b", Value: "2"}}
	_, err := buildImportEntries(testNS, items)
	if err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("expected collision error, got %v", err)
	}
	if !strings.Contains(err.Error(), "A_B") || !strings.Contains(err.Error(), "a-b") {
		t.Fatalf("collision error should list both keys: %v", err)
	}
}

func TestBuildImportEntriesInvalidKey(t *testing.T) {
	items := []kv{{Key: "has space", Value: "1"}}
	_, err := buildImportEntries(testNS, items)
	if err == nil || !strings.Contains(err.Error(), "valid keys") {
		t.Fatalf("expected invalid key error, got %v", err)
	}
}

func TestResolveImportNamespaceBad(t *testing.T) {
	_, err := resolveImportNamespace("no-slash", "", "")
	if err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("expected namespace error, got %v", err)
	}
}

func TestResolveImportNamespaceEnvApp(t *testing.T) {
	ns, err := resolveImportNamespace("", "prod", "gradethis")
	if err != nil {
		t.Fatalf("resolveImportNamespace: %v", err)
	}
	if ns != testNS {
		t.Fatalf("ns = %+v, want %+v", ns, testNS)
	}
}

func TestLoadFromJSONObject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.json")
	writeFile(t, path, `{"A": "1", "B": "two", "C": 3}`)

	items, err := loadImportSource(path)
	if err != nil {
		t.Fatalf("loadImportSource: %v", err)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.Key] = it.Value
	}
	if got["A"] != "1" || got["B"] != "two" || got["C"] != "3" {
		t.Fatalf("parsed = %v", got)
	}
}

func TestLoadFromJSONArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.json")
	writeFile(t, path, `[{"key":"A","value":"1"},{"key":"B","value":"2"}]`)

	items, err := loadImportSource(path)
	if err != nil {
		t.Fatalf("loadImportSource: %v", err)
	}
	if len(items) != 2 || items[0].Key != "A" || items[1].Value != "2" {
		t.Fatalf("parsed = %+v", items)
	}
}

func TestLoadFromSQLite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")
	createOldStore(t, path, map[string]string{"FOO": "1", "BAR": "2"})

	items, err := loadImportSource(path)
	if err != nil {
		t.Fatalf("loadImportSource: %v", err)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.Key] = it.Value
	}
	if got["FOO"] != "1" || got["BAR"] != "2" {
		t.Fatalf("parsed = %v", got)
	}
}

func TestImportDryRun(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "export.json")
	writeFile(t, src, `{"STRIPE_KEY":"sk_live_x","TWILIO_SID":"AC123"}`)
	report := filepath.Join(dir, "report.txt")
	dbPath := filepath.Join(dir, "kms.db")

	c := newTestCLI()
	code := c.cmdImport([]string{
		"--from", src, "--namespace", "prod/gradethis",
		"--dry-run", "--report", report, "--sqlite-path", dbPath,
	})
	if code != 0 {
		t.Fatalf("import dry-run exit = %d, stderr=%s", code, c.stderr())
	}

	got, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	body := string(got)
	if !strings.Contains(body, "STRIPE_KEY -> /prod/gradethis/stripe-key") {
		t.Fatalf("report missing mapping: %s", body)
	}
	if !strings.Contains(body, "TWILIO_SID -> /prod/gradethis/twilio-sid") {
		t.Fatalf("report missing mapping: %s", body)
	}
	// No tokens in a dry run.
	if strings.Contains(body, "access token") || strings.Contains(strings.ToLower(body), "kmss_") {
		t.Fatalf("dry run must not include tokens: %s", body)
	}
	// No database written.
	if fileExists(dbPath) {
		t.Fatalf("dry run must not create the database")
	}
}

func TestImportReportRefusesExistingFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "export.json")
	writeFile(t, src, `{"A":"1"}`)
	report := filepath.Join(dir, "report.txt")
	writeFile(t, report, "existing")

	c := newTestCLI()
	code := c.cmdImport([]string{"--from", src, "--namespace", "prod/x", "--dry-run", "--report", report})
	if code == 0 {
		t.Fatalf("expected failure when report file exists")
	}
}

func TestWriteImportReportPropagatesOutputFailure(t *testing.T) {
	err := writeImportReport(errorWriter{err: io.ErrClosedPipe}, []importResult{{
		Key: "OLD_KEY", Path: "/prod/app/old-key", Token: "kmss_secret",
	}}, true)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("writeImportReport error = %v, want closed pipe", err)
	}
}

// In JSON mode the mapping report becomes the result document on stdout. A
// dry run mints nothing, so no entry carries a token.
func TestImportDryRunJSON(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "export.json")
	writeFile(t, src, `{"STRIPE_KEY":"sk_live_x","TWILIO_SID":"AC123"}`)

	c := newTestCLI()
	code := c.Run([]string{"-o", "json", "import", "--from", src, "--namespace", "prod/gradethis", "--dry-run"})
	if code != 0 {
		t.Fatalf("import exit = %d, stderr=%s", code, c.stderr())
	}
	document := decodeJSONStdout(t, c)
	assertJSONFields(t, document, "namespace", "dry_run", "imported", "entries")
	if document["dry_run"] != true {
		t.Fatalf("dry_run = %v", document["dry_run"])
	}
	if imported, _ := document["imported"].(float64); imported != 0 {
		t.Fatalf("imported = %v, want 0 for a dry run", document["imported"])
	}
	namespace, _ := document["namespace"].(map[string]any)
	if namespace["env"] != "prod" || namespace["app"] != "gradethis" {
		t.Fatalf("namespace = %v", namespace)
	}
	entries, ok := document["entries"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("entries = %#v, want 2", document["entries"])
	}
	// Entries are sorted by source key, so the document is reproducible.
	first, _ := entries[0].(map[string]any)
	assertJSONFields(t, first, "key", "path")
	if first["key"] != "STRIPE_KEY" || first["path"] != "/prod/gradethis/stripe-key" {
		t.Fatalf("entries[0] = %v", first)
	}
	if strings.Contains(c.stdout(), "token") {
		t.Fatalf("a dry run disclosed a token field: %s", c.stdout())
	}
}

// A real import mints one access token per secret. Each appears exactly once
// in the document, with the one-time warning on stderr.
func TestImportJSONCarriesEachAccessTokenOnce(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "kms.db")
	keyFile := filepath.Join(dir, "master.key")
	src := filepath.Join(dir, "export.json")
	writeFile(t, src, `{"STRIPE_KEY":"sk_live_x"}`)

	init := newTestCLI()
	if code := init.cmdInit([]string{"--sqlite-path", db, "--kek-file", keyFile}); code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, init.stderr())
	}

	c := newTestCLI()
	code := c.Run([]string{"-o", "json", "import", "--from", src, "--namespace", "prod/gradethis",
		"--sqlite-path", db, "--kek-file", keyFile})
	if code != 0 {
		t.Fatalf("import exit = %d, stderr=%s", code, c.stderr())
	}
	document := decodeJSONStdout(t, c)
	if document["dry_run"] != false {
		t.Fatalf("dry_run = %v", document["dry_run"])
	}
	if imported, _ := document["imported"].(float64); imported != 1 {
		t.Fatalf("imported = %v, want 1", document["imported"])
	}
	entries, _ := document["entries"].([]any)
	entry, _ := entries[0].(map[string]any)
	assertJSONFields(t, entry, "key", "path", "token")
	token, _ := entry["token"].(string)
	if !strings.HasPrefix(token, "kmss_") {
		t.Fatalf("token = %q", token)
	}
	if strings.Count(c.stdout(), token) != 1 {
		t.Fatalf("the one-time token appears more than once: %s", c.stdout())
	}
	if !strings.Contains(c.stderr(), "WARNING: the access tokens are shown once") {
		t.Fatalf("stderr = %q", c.stderr())
	}
}

// --report keeps its meaning in JSON mode: the file still receives the text
// mapping, and stdout still carries exactly one document.
func TestImportJSONWithReportFileWritesBoth(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "export.json")
	writeFile(t, src, `{"A":"1"}`)
	report := filepath.Join(dir, "report.txt")

	c := newTestCLI()
	code := c.Run([]string{"-o", "json", "import", "--from", src, "--namespace", "prod/gradethis",
		"--dry-run", "--report", report})
	if code != 0 {
		t.Fatalf("import exit = %d, stderr=%s", code, c.stderr())
	}
	if body := readFileString(t, report); !strings.Contains(body, "A -> /prod/gradethis/a") {
		t.Fatalf("report = %q", body)
	}
	if entries, _ := decodeJSONStdout(t, c)["entries"].([]any); len(entries) != 1 {
		t.Fatalf("stdout document = %q", c.stdout())
	}
}

// --- helpers ---------------------------------------------------------------

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// createOldStore builds a minimal SuhaibParameterStore-style SQLite database.
func createOldStore(t *testing.T, path string, rows map[string]string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("CREATE TABLE parameters (key TEXT, value TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, err := db.Exec("INSERT INTO parameters (key, value) VALUES (?, ?)", k, rows[k]); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
}

// newTestCLI builds a CLI with buffered output for assertions and an empty
// environment, so no command under test ever observes the developer's shell.
// A test that needs a variable installs its own lookup with mapLookup.
func newTestCLI() *testCLI {
	c := &testCLI{out: &bytes.Buffer{}, err: &bytes.Buffer{}}
	c.CLI = CLI{Stdout: c.out, Stderr: c.err, Stdin: nil}
	c.lookupEnv = func(string) (string, bool) { return "", false }
	return c
}

type testCLI struct {
	CLI
	out *bytes.Buffer
	err *bytes.Buffer
}

func (c *testCLI) stderr() string { return c.err.String() }
func (c *testCLI) stdout() string { return c.out.String() }
