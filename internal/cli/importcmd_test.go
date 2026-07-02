package cli

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

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
	entries, err := buildImportEntries("/prod/gradethis", items)
	if err != nil {
		t.Fatalf("buildImportEntries: %v", err)
	}
	// Sorted by key: BAR_BAZ < FOO.
	if entries[0].Key != "BAR_BAZ" || entries[0].Path != "/prod/gradethis/bar-baz" {
		t.Fatalf("entry0 = %+v", entries[0])
	}
	if entries[1].Path != "/prod/gradethis/foo" {
		t.Fatalf("entry1 = %+v", entries[1])
	}
}

func TestBuildImportEntriesCollision(t *testing.T) {
	// "A_B" and "a-b" both slug to "a-b".
	items := []kv{{Key: "A_B", Value: "1"}, {Key: "a-b", Value: "2"}}
	_, err := buildImportEntries("/prod/gradethis", items)
	if err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("expected collision error, got %v", err)
	}
	if !strings.Contains(err.Error(), "A_B") || !strings.Contains(err.Error(), "a-b") {
		t.Fatalf("collision error should list both keys: %v", err)
	}
}

func TestBuildImportEntriesInvalidPath(t *testing.T) {
	items := []kv{{Key: "has space", Value: "1"}}
	_, err := buildImportEntries("/prod/gradethis", items)
	if err == nil || !strings.Contains(err.Error(), "valid paths") {
		t.Fatalf("expected invalid path error, got %v", err)
	}
}

func TestBuildImportEntriesBadNamespace(t *testing.T) {
	_, err := buildImportEntries("no-slash", []kv{{Key: "A", Value: "1"}})
	if err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("expected namespace error, got %v", err)
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
		"--from", src, "--namespace", "/prod/gradethis",
		"--dry-run", "--report", report, "--db", dbPath,
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
	code := c.cmdImport([]string{"--from", src, "--namespace", "/prod/x", "--dry-run", "--report", report})
	if code == 0 {
		t.Fatalf("expected failure when report file exists")
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

// newTestCLI builds a CLI with buffered output for assertions.
func newTestCLI() *testCLI {
	c := &testCLI{out: &bytes.Buffer{}, err: &bytes.Buffer{}}
	c.CLI = CLI{Stdout: c.out, Stderr: c.err, Stdin: nil}
	return c
}

type testCLI struct {
	CLI
	out *bytes.Buffer
	err *bytes.Buffer
}

func (c *testCLI) stderr() string { return c.err.String() }
func (c *testCLI) stdout() string { return c.out.String() }
