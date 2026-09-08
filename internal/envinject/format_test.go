package envinject

import (
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// formatVars is the shared sample: awkward values in an order that is not
// sorted, so every writer can be checked for preserving the order it is given.
func formatVars() []Var {
	return []Var{
		{Name: "PLAIN", Value: "simple"},
		{Name: "EMPTY", Value: ""},
		{Name: "SPACES", Value: "with space"},
		{Name: "DQUOTE", Value: `quote"double`},
		{Name: "SQUOTE", Value: "single'quote"},
		{Name: "BACKSLASH", Value: `back\slash`},
		{Name: "NEWLINE", Value: "line1\nline2"},
		{Name: "TAB", Value: "a\tb"},
		{Name: "CR", Value: "a\rb"},
		{Name: "CTRL", Value: "a\x01b\x7f"},
		{Name: "SEPARATORS", Value: "a\u0085b\u2028c\u009fd"},
		{Name: "UNICODE", Value: "héllo ✓ 🔐"},
		{Name: "SHELL", Value: "$HOME `id` ${x} $(y) *"},
		{Name: "BARE", Value: "a/b:c@d%e+f-g_h.i=j"},
	}
}

func wantMap(vars []Var) map[string]string {
	m := make(map[string]string, len(vars))
	for _, v := range vars {
		m[v.Name] = v.Value
	}
	return m
}

func TestShellQuote(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"", "''"},
		{"abc", "'abc'"},
		{"a b", "'a b'"},
		{"$HOME", "'$HOME'"},
		{"a'b", `'a'\''b'`},
		{"'", `''\'''`},
		{"a\nb", "'a\nb'"},
	}
	for _, tc := range cases {
		if got := ShellQuote(tc.in); got != tc.want {
			t.Errorf("ShellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDotenvQuote(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"simple", "simple"},
		{"a/b:c@d%e+f-g_h.i=j", "a/b:c@d%e+f-g_h.i=j"},
		{"", `""`},
		{"with space", `"with space"`},
		{`quote"double`, `"quote\"double"`},
		{`back\slash`, `"back\\slash"`},
		{"line1\nline2", "\"line1\nline2\""},
		{"a\tb", "\"a\tb\""},
		{"a\rb", "\"a\rb\""},
		{"a\x01b", "\"a\x01b\""},
		{"a\x7fb", "\"a\x7fb\""},
		{"a\u0085b\u2028c", "\"a\u0085b\u2028c\""},
		{"héllo", `"héllo"`},
		{"single'quote", `"single'quote"`},
		{"$HOME", `"\$HOME"`},
	}
	for _, tc := range cases {
		if got := DotenvQuote(tc.in); got != tc.want {
			t.Errorf("DotenvQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWriteDotenvPreservesAssignmentOrder(t *testing.T) {
	t.Parallel()
	vars := formatVars()
	var buf strings.Builder
	if err := WriteDotenv(&buf, vars); err != nil {
		t.Fatalf("WriteDotenv: %v", err)
	}
	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("output does not end with a newline: %q", out)
	}
	// Assignment ordering is preserved even when quoted values span lines.
	previous := -1
	for _, v := range vars {
		at := strings.Index(out, v.Name+"=")
		if at <= previous {
			t.Fatalf("assignment order lost for %s", v.Name)
		}
		previous = at
	}
}

func TestWriteDotenvEmpty(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	if err := WriteDotenv(&buf, nil); err != nil {
		t.Fatalf("WriteDotenv: %v", err)
	}
	if buf.String() != "" {
		t.Errorf("output = %q, want empty", buf.String())
	}
}

func TestWriteExport(t *testing.T) {
	t.Parallel()
	vars := []Var{{Name: "A", Value: "1"}, {Name: "B", Value: "a'b"}, {Name: "C", Value: ""}}
	var buf strings.Builder
	if err := WriteExport(&buf, vars); err != nil {
		t.Fatalf("WriteExport: %v", err)
	}
	want := "export A='1'\n" + `export B='a'\''b'` + "\nexport C=''\n"
	if buf.String() != want {
		t.Errorf("output = %q, want %q", buf.String(), want)
	}
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()
	vars := formatVars()
	var buf strings.Builder
	if err := WriteJSON(&buf, vars); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	out := buf.String()
	if !strings.HasSuffix(out, "}\n") {
		t.Errorf("output does not end with a newline-terminated object: %q", out)
	}
	if !strings.HasPrefix(out, "{\n  \"PLAIN\": ") {
		t.Errorf("output is not indented two spaces: %q", out)
	}
	// Names appear in the order given, not sorted.
	prev := -1
	for _, v := range vars {
		at := strings.Index(out, "\n  \""+v.Name+"\": ")
		if at < 0 {
			t.Fatalf("%s missing from output %q", v.Name, out)
		}
		if at <= prev {
			t.Errorf("%s is out of order in %q", v.Name, out)
		}
		prev = at
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := wantMap(vars)
	if len(got) != len(want) {
		t.Fatalf("decoded %d variables, want %d", len(got), len(want))
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("%s = %q, want %q", name, got[name], value)
		}
	}
}

func TestWriteJSONEmpty(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	if err := WriteJSON(&buf, nil); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if buf.String() != "{}\n" {
		t.Errorf("output = %q, want %q", buf.String(), "{}\n")
	}
}

func TestWriteYAML(t *testing.T) {
	t.Parallel()
	vars := formatVars()
	var buf strings.Builder
	if err := WriteYAML(&buf, vars); err != nil {
		t.Fatalf("WriteYAML: %v", err)
	}
	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("output does not end with a newline: %q", out)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != len(vars) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(vars), out)
	}
	// Every line is "NAME: " followed by a JSON string, in the order given.
	for i, line := range lines {
		prefix := vars[i].Name + ": "
		if !strings.HasPrefix(line, prefix) {
			t.Fatalf("line %d = %q, want it to start with %q", i, line, prefix)
		}
		quoted := strings.TrimPrefix(line, prefix)
		if !strings.HasPrefix(quoted, `"`) || !strings.HasSuffix(quoted, `"`) {
			t.Errorf("line %d value %q is not a quoted string", i, quoted)
		}
		var value string
		if err := json.Unmarshal([]byte(quoted), &value); err != nil {
			t.Fatalf("line %d value %q is not a JSON string: %v", i, quoted, err)
		}
		if value != vars[i].Value {
			t.Errorf("line %d decoded %q, want %q", i, value, vars[i].Value)
		}
	}
	// A JSON string is also a YAML double-quoted scalar, so a YAML parser
	// reads the same values back.
	var got map[string]string
	if err := yaml.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	want := wantMap(vars)
	if len(got) != len(want) {
		t.Fatalf("decoded %d variables, want %d", len(got), len(want))
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("%s = %q, want %q", name, got[name], value)
		}
	}
}

func TestWriteYAMLEmpty(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	if err := WriteYAML(&buf, nil); err != nil {
		t.Fatalf("WriteYAML: %v", err)
	}
	if buf.String() != "" {
		t.Errorf("output = %q, want empty", buf.String())
	}
}

func TestInvalidUTF8BecomesReplacementCharacter(t *testing.T) {
	t.Parallel()
	// Resolve base64-encodes such a value instead, but the writers must stay
	// well defined if one is handed to them directly.
	var buf strings.Builder
	if err := WriteYAML(&buf, []Var{{Name: "A", Value: "a\xffb"}}); err != nil {
		t.Fatalf("WriteYAML: %v", err)
	}
	if got, want := buf.String(), "A: \"a�b\"\n"; got != want {
		t.Errorf("WriteYAML = %q, want %q", got, want)
	}
	buf.Reset()
	if err := WriteJSON(&buf, []Var{{Name: "A", Value: "a\xffb"}}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if got, want := buf.String(), "{\n  \"A\": \"a�b\"\n}\n"; got != want {
		t.Errorf("WriteJSON = %q, want %q", got, want)
	}
}

func TestWritersReportWriteErrors(t *testing.T) {
	t.Parallel()
	vars := []Var{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}
	writers := map[string]func(w *failingWriter) error{
		"dotenv": func(w *failingWriter) error { return WriteDotenv(w, vars) },
		"export": func(w *failingWriter) error { return WriteExport(w, vars) },
		"json":   func(w *failingWriter) error { return WriteJSON(w, vars) },
		"yaml":   func(w *failingWriter) error { return WriteYAML(w, vars) },
	}
	for name, write := range writers {
		if err := write(&failingWriter{}); !errors.Is(err, errWrite) {
			t.Errorf("%s: err = %v, want %v", name, err, errWrite)
		}
	}
	// WriteJSON streams, so it must also report a failure part way through.
	big := []Var{
		{Name: "A", Value: strings.Repeat("a", 1<<16)},
		{Name: "B", Value: strings.Repeat("b", 1<<16)},
	}
	if err := WriteJSON(&failingWriter{ok: 1}, big); !errors.Is(err, errWrite) {
		t.Errorf("json after one write: err = %v, want %v", err, errWrite)
	}
}

var errWrite = errors.New("write failed")

// failingWriter accepts ok writes and then fails, so the format writers can be
// checked for propagating the error instead of swallowing it.
type failingWriter struct{ ok int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.ok > 0 {
		w.ok--
		return len(p), nil
	}
	return 0, errWrite
}

func TestWriteDotenvRejectsUnsupportedCharactersBeforeOutput(t *testing.T) {
	for _, value := range []string{"secret\x00", "secret\xff", "secret\uFEFF", "secret\uFDD0", "secret\uFDEF", "secret\uFFFF", "secret\U0001FFFE", "secret\U0010FFFF"} {
		var out strings.Builder
		err := WriteDotenv(&out, []Var{{Name: "FIRST", Value: "ok"}, {Name: "BAD", Value: value}})
		if err == nil || !strings.Contains(err.Error(), "BAD") || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe error: %v", err)
		}
		if out.Len() != 0 {
			t.Fatal("partial output")
		}
	}
}
