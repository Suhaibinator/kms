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

func TestRunManagedConfigCommandUsageAndConfiguration(t *testing.T) {
	for _, args := range [][]string{nil, {"schema", "wrong"}, {"defaults", "wrong"}} {
		var stdout, stderr bytes.Buffer
		if exitCode := runManagedConfigCommand(args, &stdout, &stderr, managedCommandTestConfig(), func(kmsclient.Config) (managedConfigClient, error) {
			return nil, errors.New("must not connect")
		}); exitCode != 2 {
			t.Fatalf("args=%v exit=%d stderr=%q", args, exitCode, stderr.String())
		}
	}
}
