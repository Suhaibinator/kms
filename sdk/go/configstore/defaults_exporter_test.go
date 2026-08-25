package configstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type exporterTestProfile string

type exporterTestConfig struct{ Value string }

func exporterTestArtifact(profile string, config *exporterTestConfig) ([]byte, error) {
	return EncodeDefaultsArtifact(DefaultsArtifact{
		Format:       DefaultsArtifactFormat,
		Profile:      profile,
		SchemaSHA256: testSchemaSHA256,
		Contract: []ContractEntry{{
			Alias: "runtime", Kind: ContractKindParameter, ContentType: "text/plain",
		}},
		Parameters: []DefaultsParameter{{
			Alias: "runtime", ContentType: "text/plain", Value: config.Value,
		}},
	})
}

func TestRunDefaultsExporterWritesStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var gotProfile exporterTestProfile
	exitCode := RunDefaultsExporter(
		[]string{"--profile", "dev"},
		&stdout,
		&stderr,
		func(profile exporterTestProfile) (*exporterTestConfig, error) {
			gotProfile = profile
			return &exporterTestConfig{Value: "exact\nvalue"}, nil
		},
		exporterTestArtifact,
	)
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	if gotProfile != "dev" {
		t.Fatalf("provider profile = %q", gotProfile)
	}
	artifact, err := ParseDefaultsArtifact(stdout.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Profile != "dev" || artifact.Parameters[0].Value != "exact\nvalue" {
		t.Fatalf("artifact = %#v", artifact)
	}
}

func TestRunDefaultsExporterWritesFileAtomically(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "nested", "defaults.json")
	var stdout, stderr bytes.Buffer
	exitCode := RunDefaultsExporter(
		[]string{"--profile=ci", "--output", output},
		&stdout,
		&stderr,
		func(profile exporterTestProfile) (*exporterTestConfig, error) {
			return &exporterTestConfig{Value: string(profile)}, nil
		},
		exporterTestArtifact,
	)
	if exitCode != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := ParseDefaultsArtifact(data)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Parameters[0].Value != "ci" {
		t.Fatalf("file value = %q", artifact.Parameters[0].Value)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("new output mode = %o, want 600", got)
	}
	temporary, err := filepath.Glob(filepath.Join(filepath.Dir(output), ".kms-defaults-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary files remain: %v", temporary)
	}
}

func TestRunDefaultsExporterArgumentAndProviderFailures(t *testing.T) {
	provider := func(exporterTestProfile) (*exporterTestConfig, error) {
		return &exporterTestConfig{}, nil
	}
	for name, args := range map[string][]string{
		"missing profile": nil,
		"positional":      {"--profile", "dev", "--output", "-", "extra"},
		"unknown flag":    {"--profile", "dev", "--output", "-", "--unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			if got := RunDefaultsExporter(args, &bytes.Buffer{}, &stderr, provider, exporterTestArtifact); got != 2 {
				t.Fatalf("exit code = %d, want 2", got)
			}
			if !strings.HasPrefix(stderr.String(), "defaults-exporter: arguments:") {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}

	var stderr bytes.Buffer
	exitCode := RunDefaultsExporter(
		[]string{"--profile", "dev", "--output", "-"},
		&bytes.Buffer{},
		&stderr,
		func(exporterTestProfile) (*exporterTestConfig, error) {
			return nil, errors.New(strings.Repeat("sensitive-detail", 200))
		},
		exporterTestArtifact,
	)
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if strings.Contains(stderr.String(), "sensitive-detail") {
		t.Fatalf("provider error leaked: %q", stderr.String())
	}
	if stderr.String() != "defaults-exporter: load defaults: provider failed\n" {
		t.Fatalf("provider stderr = %q", stderr.String())
	}
}

func TestRunDefaultsExporterValidatesEncoderOutput(t *testing.T) {
	provider := func(exporterTestProfile) (*exporterTestConfig, error) {
		return &exporterTestConfig{}, nil
	}
	tests := map[string]func(string, *exporterTestConfig) ([]byte, error){
		"malformed": func(string, *exporterTestConfig) ([]byte, error) { return []byte("{}\n"), nil },
		"wrong profile": func(_ string, config *exporterTestConfig) ([]byte, error) {
			return exporterTestArtifact("prod", config)
		},
	}
	for name, encoder := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := RunDefaultsExporter([]string{"--profile", "dev", "--output", "-"}, &stdout, &stderr, provider, encoder); got != 1 {
				t.Fatalf("exit code = %d, want 1", got)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "validate encoded artifact") {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

type failingDefaultsWriter struct{}

func (failingDefaultsWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestRunDefaultsExporterReportsStdoutFailure(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := RunDefaultsExporter(
		[]string{"--profile", "dev", "--output", "-"},
		failingDefaultsWriter{},
		&stderr,
		func(exporterTestProfile) (*exporterTestConfig, error) { return &exporterTestConfig{}, nil },
		exporterTestArtifact,
	)
	if exitCode != 1 || !strings.Contains(stderr.String(), "write artifact") {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestRunDefaultsExporterHelpAndBoundedArgumentErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	provider := func(exporterTestProfile) (*exporterTestConfig, error) { return &exporterTestConfig{}, nil }
	if got := RunDefaultsExporter([]string{"--help"}, &stdout, &stderr, provider, exporterTestArtifact); got != 0 {
		t.Fatalf("help exit = %d", got)
	}
	if !strings.Contains(stdout.String(), "Usage: defaults-exporter") || stderr.Len() != 0 {
		t.Fatalf("help stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	if got := RunDefaultsExporter([]string{"--" + strings.Repeat("x", 2000)}, &stdout, &stderr, provider, exporterTestArtifact); got != 2 {
		t.Fatalf("invalid argument exit = %d", got)
	}
	if stderr.Len() > maxDefaultsExporterErrorBytes+1 {
		t.Fatalf("argument error was not bounded: %d bytes", stderr.Len())
	}
}
