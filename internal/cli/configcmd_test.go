package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/config"
)

// The passphrase is the one piece of secret material a configuration run can
// see. The table reports it as set/unset; the JSON document must apply exactly
// the same redaction, or `config show -o json` becomes a way to exfiltrate it.
func TestConfigShowJSONRedactsThePassphrase(t *testing.T) {
	c := newTestCLI()
	c.lookupEnv = mapLookup(map[string]string{"KMS_MASTER_PASSPHRASE": "correct-horse-battery-staple"})
	if code := c.Run([]string{"-o", "json", "config", "show"}); code != 0 {
		t.Fatalf("config show exit = %d, stderr=%s", code, c.stderr())
	}
	if strings.Contains(c.stdout(), "correct-horse-battery-staple") || strings.Contains(c.stderr(), "correct-horse-battery-staple") {
		t.Fatalf("config show leaked the passphrase:\nstdout=%s\nstderr=%s", c.stdout(), c.stderr())
	}
	document := decodeJSONStdout(t, c)
	assertJSONFields(t, document, "config_path", "config_path_source", "passphrase", "settings")
	if document["passphrase"] != "set" {
		t.Fatalf("passphrase = %v, want \"set\"", document["passphrase"])
	}
	// No config file was read, so the path is null rather than "".
	if document["config_path"] != nil || document["config_path_source"] != nil {
		t.Fatalf("config_path = %#v, config_path_source = %#v; want null", document["config_path"], document["config_path_source"])
	}
	settings, ok := document["settings"].([]any)
	if !ok || len(settings) != len(config.Settings) {
		t.Fatalf("settings = %#v, want one row per registered setting (%d)", document["settings"], len(config.Settings))
	}
	first, ok := settings[0].(map[string]any)
	if !ok {
		t.Fatalf("settings[0] = %#v", settings[0])
	}
	assertJSONFields(t, first, "key", "value", "source")
}

// The JSON rows must report the same values and provenance the table does, so
// a script and an operator never disagree about which database will be opened.
func TestConfigShowJSONReportsValuesAndProvenance(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "kms.yaml")
	writeFile(t, configPath, "storage:\n  sqlite_path: "+filepath.Join(dir, "file.db")+"\n")
	flagDB := filepath.Join(dir, "flag.db")

	c := newTestCLI()
	if code := c.Run([]string{"-o", "json", "config", "show", "--config", configPath, "--sqlite-path", flagDB}); code != 0 {
		t.Fatalf("config show exit = %d, stderr=%s", code, c.stderr())
	}
	document := decodeJSONStdout(t, c)
	if document["config_path"] != configPath || document["config_path_source"] != "flag --config" {
		t.Fatalf("config_path = %v (%v)", document["config_path"], document["config_path_source"])
	}
	if document["passphrase"] != "unset" {
		t.Fatalf("passphrase = %v, want \"unset\"", document["passphrase"])
	}
	settings, _ := document["settings"].([]any)
	found := false
	for _, raw := range settings {
		row, _ := raw.(map[string]any)
		if row["key"] != "storage.sqlite_path" {
			continue
		}
		found = true
		if row["value"] != flagDB || row["source"] != "flag --sqlite-path" {
			t.Fatalf("storage.sqlite_path row = %v", row)
		}
	}
	if !found {
		t.Fatalf("settings do not include storage.sqlite_path: %v", settings)
	}
}

// The table is a published interface; adding the JSON branch must not disturb
// it (note its own tabwriter padding, which differs from the list commands').
func TestConfigShowTableIsUnchanged(t *testing.T) {
	c := newTestCLI()
	if code := c.Run([]string{"config", "show"}); code != 0 {
		t.Fatalf("config show exit = %d, stderr=%s", code, c.stderr())
	}
	lines := strings.Split(c.stdout(), "\n")
	if lines[0] != "config file: none" || lines[1] != "" {
		t.Fatalf("header = %q / %q", lines[0], lines[1])
	}
	if !strings.HasPrefix(lines[2], "KEY") || !strings.Contains(lines[2], "VALUE") || !strings.Contains(lines[2], "SOURCE") {
		t.Fatalf("column header = %q", lines[2])
	}
	if !strings.Contains(c.stdout(), "\nKMS_MASTER_PASSPHRASE: unset\n") {
		t.Fatalf("passphrase footer missing: %s", c.stdout())
	}
}

func TestConfigValidateJSON(t *testing.T) {
	c := newTestCLI()
	if code := c.Run([]string{"-o", "json", "config", "validate"}); code != 0 {
		t.Fatalf("config validate exit = %d, stderr=%s", code, c.stderr())
	}
	document := decodeJSONStdout(t, c)
	assertJSONFields(t, document, "valid", "config_path")
	if document["valid"] != true || document["config_path"] != nil {
		t.Fatalf("document = %v", document)
	}
}

// An invalid configuration exits non-zero with the reason on stderr; stdout
// stays empty rather than carrying a valid:false document a script could
// mistake for success.
func TestConfigValidateRejectsInvalidConfigurationWithoutADocument(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "kms.yaml")
	writeFile(t, configPath, "server:\n  grpc_addr: \"\"\n")

	c := newTestCLI()
	if code := c.Run([]string{"-o", "json", "config", "validate", "--config", configPath}); code == 0 {
		t.Fatalf("config validate accepted an empty grpc_addr; stdout=%s", c.stdout())
	}
	if c.stdout() != "" {
		t.Fatalf("failed validation wrote to stdout: %q", c.stdout())
	}
	if !strings.Contains(c.stderr(), "error: invalid configuration: ") {
		t.Fatalf("stderr = %q", c.stderr())
	}
}
