package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"text/tabwriter"
	"time"
)

// TestOutputModeSet pins the accepted spellings and the exact rejection
// message: scripts pass --output from a variable, so a stray "JSON " or
// "Table" must not be a hard error, while anything else must name the value it
// saw so the operator can spot the typo.
func TestOutputModeSet(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		input   string
		want    outputMode
		wantErr string
	}{
		{name: "table", input: "table", want: outputTable},
		{name: "json", input: "json", want: outputJSON},
		{name: "upper table", input: "TABLE", want: outputTable},
		{name: "mixed json", input: "Json", want: outputJSON},
		{name: "padded", input: "  json  ", want: outputJSON},
		{name: "yaml", input: "yaml", wantErr: `invalid output format "yaml" (want table or json)`},
		{name: "empty", input: "", wantErr: `invalid output format "" (want table or json)`},
		// The message quotes the value as the caller spelled it, untrimmed, so
		// a variable holding a stray space is recognisable in the error.
		{name: "reports untrimmed value", input: " x ", wantErr: `invalid output format " x " (want table or json)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var m outputMode
			err := m.Set(tc.input)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Set(%q) = nil error, want %q", tc.input, tc.wantErr)
				}
				if err.Error() != tc.wantErr {
					t.Fatalf("Set(%q) error = %q, want %q", tc.input, err.Error(), tc.wantErr)
				}
				if m != "" {
					t.Fatalf("rejected value still set the mode to %q", m)
				}
				return
			}
			if err != nil {
				t.Fatalf("Set(%q) = %v", tc.input, err)
			}
			if m != tc.want {
				t.Fatalf("Set(%q) = %q, want %q", tc.input, m, tc.want)
			}
		})
	}
}

// TestOutputModeStringZeroValue: the flag package prints String() as the
// default in help, and an unset mode means the table.
func TestOutputModeStringZeroValue(t *testing.T) {
	t.Parallel()
	var m outputMode
	if got := m.String(); got != "table" {
		t.Fatalf("zero outputMode String() = %q, want %q", got, "table")
	}
	m = outputJSON
	if got := m.String(); got != "json" {
		t.Fatalf("outputJSON String() = %q, want %q", got, "json")
	}
}

// TestPrintTableMatchesTabwriterIdiom compares printTable against a hand-built
// tabwriter with the historical parameters. Table output is a documented
// interface (scripts cut columns out of it), so the helper must stay byte for
// byte identical to the idiom it replaced, including on ragged widths.
func TestPrintTableMatchesTabwriterIdiom(t *testing.T) {
	t.Parallel()
	headers := []string{"KEY", "KIND", "VERSION"}
	rows := [][]string{
		{"/prod/api/database-url", "secret", "12"},
		{"/prod/api/x", "parameter", "1"},
		{"a", "b", "100000"},
	}

	var want bytes.Buffer
	tw := tabwriter.NewWriter(&want, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, strings.Join(headers, "\t")); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if _, err := fmt.Fprintln(tw, strings.Join(row, "\t")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Flush(); err != nil {
		t.Fatal(err)
	}

	c := newTestCLI()
	c.printTable(headers, rows)
	if got := c.stdout(); got != want.String() {
		t.Fatalf("printTable output:\n%q\nwant:\n%q", got, want.String())
	}
	if c.stderr() != "" {
		t.Fatalf("printTable wrote to stderr: %q", c.stderr())
	}
}

// TestPrintTableEmptyRows: a list command with no results still prints its
// header row, so the caller can tell "no rows" from "command did nothing".
func TestPrintTableEmptyRows(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	c.printTable([]string{"KEY", "KIND"}, nil)
	if got, want := c.stdout(), "KEY  KIND\n"; got != want {
		t.Fatalf("printTable with no rows = %q, want %q", got, want)
	}
}

// TestPrintJSONIndentAndDeterminism pins the wire shape scripts parse: two
// space indentation, sorted map keys (so a diff of two runs is empty), and a
// trailing newline so the document ends a line.
func TestPrintJSONIndentAndDeterminism(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	if code := c.printJSON(map[string]any{"zeta": 1, "alpha": "a", "middle": true}); code != exitOK {
		t.Fatalf("printJSON exit = %d, want %d", code, exitOK)
	}
	want := "{\n  \"alpha\": \"a\",\n  \"middle\": true,\n  \"zeta\": 1\n}\n"
	if got := c.stdout(); got != want {
		t.Fatalf("printJSON = %q, want %q", got, want)
	}
	if c.stderr() != "" {
		t.Fatalf("printJSON wrote to stderr: %q", c.stderr())
	}

	// Same input, second CLI: byte-identical output.
	c2 := newTestCLI()
	if code := c2.printJSON(map[string]any{"middle": true, "zeta": 1, "alpha": "a"}); code != exitOK {
		t.Fatalf("printJSON exit = %d", code)
	}
	if c2.stdout() != c.stdout() {
		t.Fatalf("printJSON is not deterministic:\n%q\n%q", c.stdout(), c2.stdout())
	}
}

// TestPrintJSONNested checks the indent applies at depth and that a nil
// pointer field renders as null, the shape absent objects promise.
func TestPrintJSONNested(t *testing.T) {
	t.Parallel()
	type ns struct {
		Env string `json:"env"`
		App string `json:"app"`
	}
	type whoami struct {
		Name      string  `json:"name"`
		Namespace *ns     `json:"namespace"`
		CreatedAt *string `json:"created_at"`
	}
	c := newTestCLI()
	if code := c.printJSON(whoami{Name: "svc", Namespace: &ns{Env: "prod", App: "api"}}); code != exitOK {
		t.Fatalf("printJSON exit = %d", code)
	}
	want := "{\n  \"name\": \"svc\",\n  \"namespace\": {\n    \"env\": \"prod\",\n    \"app\": \"api\"\n  },\n  \"created_at\": null\n}\n"
	if got := c.stdout(); got != want {
		t.Fatalf("printJSON = %q, want %q", got, want)
	}
}

// TestPrintList covers the shared list envelope: an empty (non-nil) slice is
// [] rather than null, and next_page_token appears only when the server
// returned one.
func TestPrintList(t *testing.T) {
	t.Parallel()
	type row struct {
		Key string `json:"key"`
	}
	for _, tc := range []struct {
		name  string
		items any
		token string
		want  string
	}{
		{
			name:  "empty slice renders as []",
			items: []row{},
			want:  "{\n  \"items\": []\n}\n",
		},
		{
			name:  "one item, no token",
			items: []row{{Key: "/prod/api/x"}},
			want:  "{\n  \"items\": [\n    {\n      \"key\": \"/prod/api/x\"\n    }\n  ]\n}\n",
		},
		{
			name:  "token included when set",
			items: []row{},
			token: "next",
			want:  "{\n  \"items\": [],\n  \"next_page_token\": \"next\"\n}\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestCLI()
			if code := c.printList(tc.items, tc.token); code != exitOK {
				t.Fatalf("printList exit = %d", code)
			}
			if got := c.stdout(); got != tc.want {
				t.Fatalf("printList = %q, want %q", got, tc.want)
			}
			if tc.token == "" && strings.Contains(c.stdout(), "next_page_token") {
				t.Fatalf("empty token was still emitted: %s", c.stdout())
			}
		})
	}
}

// TestJSONOutputMode: jsonOutput is the single predicate commands branch on.
func TestJSONOutputMode(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	if c.jsonOutput() {
		t.Fatal("zero-value CLI reports JSON output")
	}
	c.output = outputTable
	if c.jsonOutput() {
		t.Fatal("table mode reports JSON output")
	}
	c.output = outputJSON
	if !c.jsonOutput() {
		t.Fatal("json mode does not report JSON output")
	}
}

// TestJSONTime: the wire uses 0 for "never"/"not yet", which must become JSON
// null rather than 1970. Non-zero values are RFC 3339 in UTC.
func TestJSONTime(t *testing.T) {
	t.Parallel()
	if got := jsonTime(0); got != nil {
		t.Fatalf("jsonTime(0) = %q, want nil", *got)
	}
	got := jsonTime(1_700_000_000_123)
	if got == nil {
		t.Fatal("jsonTime(1700000000123) = nil")
	}
	if want := "2023-11-14T22:13:20.123Z"; *got != want {
		t.Fatalf("jsonTime = %q, want %q", *got, want)
	}
	// A whole second carries no fractional part (RFC3339Nano trims zeros).
	whole := jsonTime(1_700_000_000_000)
	if whole == nil || *whole != "2023-11-14T22:13:20Z" {
		t.Fatalf("jsonTime(1700000000000) = %v, want 2023-11-14T22:13:20Z", whole)
	}
}

// TestJSONTimeOf: the time.Time form agrees with the millisecond form and
// converts a non-UTC instant rather than printing its local offset.
func TestJSONTimeOf(t *testing.T) {
	t.Parallel()
	if got := jsonTimeOf(time.Time{}); got != nil {
		t.Fatalf("jsonTimeOf(zero) = %q, want nil", *got)
	}
	zone := time.FixedZone("UTC+5", 5*60*60)
	got := jsonTimeOf(time.Date(2023, 11, 14, 22, 13, 20, 123_000_000, time.UTC).In(zone))
	if got == nil {
		t.Fatal("jsonTimeOf = nil for a non-zero time")
	}
	if want := "2023-11-14T22:13:20.123Z"; *got != want {
		t.Fatalf("jsonTimeOf = %q, want %q", *got, want)
	}
	if ms := jsonTime(1_700_000_000_123); ms == nil || *ms != *got {
		t.Fatalf("jsonTimeOf and jsonTime disagree: %v vs %v", got, ms)
	}
}
