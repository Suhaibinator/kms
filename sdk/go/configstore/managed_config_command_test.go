package configstore

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

func managedCommandTestConfig() ManagedConfigCommandConfig[exporterTestProfile, exporterTestConfig] {
	return ManagedConfigCommandConfig[exporterTestProfile, exporterTestConfig]{
		Application: "gradethis",
		Schema:      func() []byte { return []byte(`{"type":"object"}`) },
		Defaults:    defaultsApplierTestConfig("dev/gradethis"),
	}
}

func TestRunManagedConfigCommandUploadsSchema(t *testing.T) {
	t.Setenv("KMS_TOKEN", "admin-token")
	client := &fakeDefaultsApplyClient{schemaResults: []kmsclient.ApplicationSchema{{
		Application: "gradethis", ReleaseName: "runtime", Version: 3,
		Digest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}}
	var captured kmsclient.Config
	var stdout, stderr bytes.Buffer
	exitCode := runManagedConfigCommand(
		[]string{"schema", "upload", "--endpoint", "kms.local:8443", "--insecure", "--metadata-json", `{"commit":"abc"}`},
		&stdout, &stderr, managedCommandTestConfig(),
		func(config kmsclient.Config) (managedConfigClient, error) {
			captured = config
			return client, nil
		},
	)
	if exitCode != 0 || stderr.Len() != 0 || !client.closed {
		t.Fatalf("exit=%d stderr=%q closed=%t", exitCode, stderr.String(), client.closed)
	}
	if captured.Endpoint != "kms.local:8443" || !captured.Insecure || captured.Token != "admin-token" || captured.Namespace != "" {
		t.Fatalf("client config = %#v", captured)
	}
	if len(client.schemaCalls) != 1 || client.schemaCalls[0].Application != "gradethis" || string(client.schemaCalls[0].Schema) != `{"type":"object"}` || client.schemaCalls[0].MetadataJSON != `{"commit":"abc"}` {
		t.Fatalf("schema calls = %#v", client.schemaCalls)
	}
	if !strings.Contains(stdout.String(), "gradethis/runtime@3") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunManagedConfigCommandDuplicateFails(t *testing.T) {
	client := &fakeDefaultsApplyClient{schemaErrors: []error{kmsclient.ErrAlreadyExists}}
	var stdout, stderr bytes.Buffer
	exitCode := runManagedConfigCommand(
		[]string{"schema", "upload", "--insecure"}, &stdout, &stderr, managedCommandTestConfig(),
		func(kmsclient.Config) (managedConfigClient, error) { return client, nil },
	)
	if exitCode != 1 || !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestRunManagedConfigCommandDispatchesDefaults(t *testing.T) {
	client := &fakeDefaultsApplyClient{results: []kmsclient.ApplicationDefaultsApplyResult{{
		Profile: "dev", PlanDigest: "plan", Entries: []kmsclient.ApplicationDefaultsApplyEntry{},
	}}}
	var stdout, stderr bytes.Buffer
	exitCode := runManagedConfigCommand(
		[]string{"defaults", "apply", "--profile", "dev", "--insecure"}, &stdout, &stderr, managedCommandTestConfig(),
		func(kmsclient.Config) (managedConfigClient, error) { return client, nil },
	)
	if exitCode != 0 || len(client.calls) != 1 || len(client.schemaCalls) != 0 {
		t.Fatalf("exit=%d defaults=%d schemas=%d stderr=%q", exitCode, len(client.calls), len(client.schemaCalls), stderr.String())
	}
}

func TestRunManagedConfigCommandPreviewsApplicationRelease(t *testing.T) {
	t.Setenv("KMS_TOKEN", "admin-token")
	planDigest := strings.Repeat("a", 64)
	client := &fakeDefaultsApplyClient{releaseResults: []kmsclient.CreateApplicationReleaseResult{{
		Profile: "dev", PlanDigest: planDigest, Valid: true, ReleaseName: "runtime",
		SchemaVersion: 3, BaseReleaseVersion: 8,
		Entries: []kmsclient.ApplicationReleasePlanEntry{
			{Alias: "runtime", Kind: "parameter", Path: "/dev/gradethis/runtime", FromVersion: 4, ToVersion: 5, Source: kmsclient.ApplicationReleaseSourceGeneratedDefault},
			{Alias: "database_password", Kind: "secret", Path: "/dev/gradethis/database-password", FromVersion: 2, ToVersion: 2, Source: kmsclient.ApplicationReleaseSourceCarriedActiveSecret},
		},
	}}}
	config := managedCommandTestConfig()
	config.Defaults.Provider = func(exporterTestProfile) (*exporterTestConfig, error) {
		return &exporterTestConfig{Value: "PARAMETER-VALUE-MUST-NOT-BE-PRINTED"}, nil
	}
	var captured kmsclient.Config
	var stdout, stderr bytes.Buffer
	exitCode := runManagedConfigCommand(
		[]string{"release", "create", "--profile", "dev", "--endpoint", "kms.local:8443", "--insecure", "--metadata-json", `{"commit":"abc"}`},
		&stdout, &stderr, config,
		func(clientConfig kmsclient.Config) (managedConfigClient, error) {
			captured = clientConfig
			return client, nil
		},
	)
	if exitCode != 0 || stderr.Len() != 0 || !client.closed {
		t.Fatalf("exit=%d stderr=%q closed=%t", exitCode, stderr.String(), client.closed)
	}
	if captured.Endpoint != "kms.local:8443" || captured.Namespace != "dev/gradethis" || captured.Token != "admin-token" || !captured.Insecure {
		t.Fatalf("client config = %#v", captured)
	}
	if len(client.releaseCalls) != 1 || client.releaseCalls[0].Execute || client.releaseCalls[0].PlanDigest != "" ||
		client.releaseCalls[0].MetadataJSON != `{"commit":"abc"}` {
		t.Fatalf("release calls = %#v", client.releaseCalls)
	}
	for _, want := range []string{"Preview release", "Profile: dev", "Release: runtime", "Schema version: 3", "Base release version: 8", "database_password", "/dev/gradethis/database-password", "parameter=1 secret=1"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout %q does not contain %q", stdout.String(), want)
		}
	}
	if strings.Contains(stdout.String(), "PARAMETER-VALUE-MUST-NOT-BE-PRINTED") {
		t.Fatalf("stdout leaked a defaults value: %q", stdout.String())
	}
}

func TestRunManagedConfigCommandExecutesFreshReleasePlanIdempotently(t *testing.T) {
	planDigest := strings.Repeat("b", 64)
	client := &fakeDefaultsApplyClient{releaseResults: []kmsclient.CreateApplicationReleaseResult{
		{Profile: "dev", PlanDigest: planDigest, Valid: true, ReleaseName: "runtime", SchemaVersion: 2},
		{Profile: "dev", PlanDigest: planDigest, Valid: true, Executed: true, Created: false, ReleaseName: "runtime", SchemaVersion: 2},
	}}
	var stdout, stderr bytes.Buffer
	exitCode := runManagedConfigCommand(
		[]string{"release", "create", "--profile", "dev", "--insecure", "--execute"},
		&stdout, &stderr, managedCommandTestConfig(),
		func(kmsclient.Config) (managedConfigClient, error) { return client, nil },
	)
	if exitCode != 0 || stderr.Len() != 0 || len(client.releaseCalls) != 2 {
		t.Fatalf("exit=%d calls=%#v stderr=%q", exitCode, client.releaseCalls, stderr.String())
	}
	if client.releaseCalls[0].Execute || !client.releaseCalls[1].Execute || client.releaseCalls[1].PlanDigest != planDigest {
		t.Fatalf("release calls = %#v", client.releaseCalls)
	}
	if !strings.Contains(stdout.String(), "Preview release") || !strings.Contains(stdout.String(), "Result release") || !strings.Contains(stdout.String(), "Created: false") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunManagedConfigCommandRejectsInvalidReleasePreviewWithoutLeakingValidationMessage(t *testing.T) {
	client := &fakeDefaultsApplyClient{releaseResults: []kmsclient.CreateApplicationReleaseResult{{
		Profile: "dev", PlanDigest: strings.Repeat("c", 64), ReleaseName: "runtime",
		MissingSecrets: []string{"database_password"},
		Validation:     []kmsclient.ApplicationReleaseValidationError{{Code: "schema_mismatch", Message: "SECRET-VALUE-MUST-NOT-BE-PRINTED"}},
	}}}
	var stdout, stderr bytes.Buffer
	exitCode := runManagedConfigCommand(
		[]string{"release", "create", "--profile", "dev", "--insecure", "--execute"},
		&stdout, &stderr, managedCommandTestConfig(),
		func(kmsclient.Config) (managedConfigClient, error) { return client, nil },
	)
	if exitCode != 1 || len(client.releaseCalls) != 1 || !strings.Contains(stderr.String(), "release preview is invalid") {
		t.Fatalf("exit=%d calls=%#v stderr=%q", exitCode, client.releaseCalls, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Missing secrets (1): database_password") || !strings.Contains(stdout.String(), "Validation: schema_mismatch=1") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "SECRET-VALUE-MUST-NOT-BE-PRINTED") || strings.Contains(stderr.String(), "SECRET-VALUE-MUST-NOT-BE-PRINTED") {
		t.Fatalf("validation message leaked: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunManagedConfigCommandUsageAndConfiguration(t *testing.T) {
	for _, args := range [][]string{nil, {"schema", "wrong"}, {"defaults", "wrong"}, {"release", "wrong"}, {"release", "create"}, {"release", "create", "--profile", " dev"}} {
		var stdout, stderr bytes.Buffer
		if exitCode := runManagedConfigCommand(args, &stdout, &stderr, managedCommandTestConfig(), func(kmsclient.Config) (managedConfigClient, error) {
			return nil, errors.New("must not connect")
		}); exitCode != 2 {
			t.Fatalf("args=%v exit=%d stderr=%q", args, exitCode, stderr.String())
		}
	}

	config := managedCommandTestConfig()
	config.Defaults.Namespace = func(exporterTestProfile) (string, error) { return "dev/another-app", nil }
	var stdout, stderr bytes.Buffer
	createdClient := false
	if exitCode := runManagedConfigCommand(
		[]string{"release", "create", "--profile", "dev", "--insecure"}, &stdout, &stderr, config,
		func(kmsclient.Config) (managedConfigClient, error) {
			createdClient = true
			return &fakeDefaultsApplyClient{}, nil
		},
	); exitCode != 1 || createdClient || !strings.Contains(stderr.String(), "another application") {
		t.Fatalf("exit=%d created=%t stderr=%q", exitCode, createdClient, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if exitCode := runManagedConfigCommand([]string{"--help"}, &stdout, &stderr, managedCommandTestConfig(), nil); exitCode != 0 || !strings.Contains(stdout.String(), "release create") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}
