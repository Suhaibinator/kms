//go:build unix

package envinject

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shellValues are values a real shell must reproduce byte for byte. A NUL byte
// cannot survive argv, so it is left out; Resolve base64-encodes such values
// before they ever reach a shell.
func shellValues() []string {
	return []string{
		"",
		"simple",
		"with space",
		"single'quote",
		`double"quote`,
		`back\slash`,
		"$HOME",
		"${x}",
		"$(id)",
		"`id`",
		"a*b?c[d]",
		"line1\nline2\n",
		"tab\there",
		"héllo ✓ 🔐",
		"';rm -rf /;'",
		"--flag=value",
		"#comment",
		"~root",
	}
}

func lookShell(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available:", err)
	}
	return sh
}

func TestShellQuoteRoundTripsThroughSh(t *testing.T) {
	t.Parallel()
	sh := lookShell(t)
	for _, value := range shellValues() {
		script := "printf '%s' " + ShellQuote(value)
		out, err := exec.Command(sh, "-c", script).Output()
		if err != nil {
			t.Errorf("sh -c %q: %v", script, err)
			continue
		}
		if string(out) != value {
			t.Errorf("sh -c %q printed %q, want %q", script, out, value)
		}
	}
}

func TestWriteExportRoundTripsThroughSh(t *testing.T) {
	t.Parallel()
	sh := lookShell(t)
	values := shellValues()
	vars := make([]Var, 0, len(values))
	for i, value := range values {
		vars = append(vars, Var{Name: "V" + string(rune('A'+i)), Value: value})
	}
	path := filepath.Join(t.TempDir(), "export.sh")
	var buf strings.Builder
	if err := WriteExport(&buf, vars); err != nil {
		t.Fatalf("WriteExport: %v", err)
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	for _, v := range vars {
		// $1 is the file to source; the variable is named literally so the
		// shell, not Go, does the lookup.
		script := `. "$1"; printf '%s' "$` + v.Name + `"`
		out, err := exec.Command(sh, "-c", script, "sh", path).Output()
		if err != nil {
			t.Errorf("sh -c %q: %v", script, err)
			continue
		}
		if string(out) != v.Value {
			t.Errorf("%s = %q after sourcing, want %q", v.Name, out, v.Value)
		}
	}
}

func TestWriteDotenvBareValuesSourceCleanly(t *testing.T) {
	t.Parallel()
	sh := lookShell(t)
	// Values DotenvQuote leaves bare must survive "set -a; . file", which is
	// how a plain POSIX shell reads a dotenv file.
	vars := []Var{
		{Name: "VA", Value: "simple"},
		{Name: "VB", Value: "a/b:c@d%e+f-g_h.i=j"},
		{Name: "VC", Value: "0123456789"},
		{Name: "VD", Value: "--flag=value"},
	}
	for _, v := range vars {
		if got := DotenvQuote(v.Value); got != v.Value {
			t.Fatalf("DotenvQuote(%q) = %q, want it bare", v.Value, got)
		}
	}
	path := filepath.Join(t.TempDir(), ".env")
	var buf strings.Builder
	if err := WriteDotenv(&buf, vars); err != nil {
		t.Fatalf("WriteDotenv: %v", err)
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	for _, v := range vars {
		script := `set -a; . "$1"; printf '%s' "$` + v.Name + `"`
		out, err := exec.Command(sh, "-c", script, "sh", path).Output()
		if err != nil {
			t.Errorf("sh -c %q: %v", script, err)
			continue
		}
		if string(out) != v.Value {
			t.Errorf("%s = %q after sourcing, want %q", v.Name, out, v.Value)
		}
	}
}
