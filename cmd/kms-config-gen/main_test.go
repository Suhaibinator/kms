package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunCheckModeDoesNotRewriteStaleArtifacts(t *testing.T) {
	dir := t.TempDir()
	binding := filepath.Join(dir, "config.gen.go")
	schema := filepath.Join(dir, "runtime.schema.json")
	contract := filepath.Join(dir, "runtime.contract.json")
	base := []string{
		"-package", "../../internal/configgen/testdata/valid",
		"-type", "Config",
		"-binding-package", "generated",
		"-binding-output", binding,
		"-schema-output", schema,
		"-contract-output", contract,
	}
	if code := run(base); code != 0 {
		t.Fatalf("generation exit code = %d", code)
	}
	if code := run(append(append([]string(nil), base...), "-verify")); code != 0 {
		t.Fatalf("fresh verification exit code = %d", code)
	}
	if err := os.WriteFile(schema, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run(append(append([]string(nil), base...), "-check")); code != 1 {
		t.Fatalf("stale verification exit code = %d, want 1", code)
	}
	contents, err := os.ReadFile(schema)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "stale\n" {
		t.Fatalf("check mode rewrote schema: %q", contents)
	}
}
