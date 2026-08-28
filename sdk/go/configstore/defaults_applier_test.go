package configstore

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

type fakeDefaultsApplyClient struct {
	calls   []kmsclient.ApplicationDefaultsApplyOptions
	results []kmsclient.ApplicationDefaultsApplyResult
	errors  []error
	closed  bool
}

func (client *fakeDefaultsApplyClient) ApplyApplicationDefaults(_ context.Context, options kmsclient.ApplicationDefaultsApplyOptions) (kmsclient.ApplicationDefaultsApplyResult, error) {
	client.calls = append(client.calls, options)
	index := len(client.calls) - 1
	if index < len(client.errors) && client.errors[index] != nil {
		return kmsclient.ApplicationDefaultsApplyResult{}, client.errors[index]
	}
	return client.results[index], nil
}

func (client *fakeDefaultsApplyClient) Close() error {
	client.closed = true
	return nil
}

func defaultsApplierTestConfig(namespace string) DefaultsApplierConfig[exporterTestProfile, exporterTestConfig] {
	return DefaultsApplierConfig[exporterTestProfile, exporterTestConfig]{
		Provider: func(profile exporterTestProfile) (*exporterTestConfig, error) {
			return &exporterTestConfig{Value: string(profile)}, nil
		},
		Encoder: exporterTestArtifact,
		Namespace: func(exporterTestProfile) (string, error) {
			return namespace, nil
		},
	}
}

func TestRunDefaultsApplierPreviewsWithoutWriting(t *testing.T) {
	t.Setenv("KMS_TOKEN", "admin-token")
	client := &fakeDefaultsApplyClient{results: []kmsclient.ApplicationDefaultsApplyResult{{
		Profile: "dev", PlanDigest: "preview-plan",
		Entries: []kmsclient.ApplicationDefaultsApplyEntry{{Alias: "runtime", Key: "runtime", Status: "unchanged"}},
	}}}
	var captured kmsclient.Config
	var stdout, stderr bytes.Buffer
	exitCode := runDefaultsApplier(
		[]string{"--profile", "dev", "--endpoint", "kms.local:8443", "--insecure"},
		&stdout, &stderr, defaultsApplierTestConfig("dev/app"),
		func(config kmsclient.Config) (defaultsApplyClient, error) {
			captured = config
			return client, nil
		},
	)
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	if len(client.calls) != 1 || client.calls[0].Execute || client.calls[0].Namespace != "dev/app" {
		t.Fatalf("calls = %#v", client.calls)
	}
	if captured.Endpoint != "kms.local:8443" || captured.Namespace != "dev/app" || captured.Token != "admin-token" || !captured.Insecure {
		t.Fatalf("client config = %#v", captured)
	}
	if !strings.Contains(stdout.String(), "unchanged=1") || !client.closed {
		t.Fatalf("stdout=%q closed=%t", stdout.String(), client.closed)
	}
}

func TestRunDefaultsApplierExecutesFreshPreviewPlan(t *testing.T) {
	client := &fakeDefaultsApplyClient{results: []kmsclient.ApplicationDefaultsApplyResult{
		{Profile: "dev", PlanDigest: "fresh-plan", Entries: []kmsclient.ApplicationDefaultsApplyEntry{{Alias: "runtime", Key: "runtime", Status: "update"}}},
		{Profile: "dev", PlanDigest: "fresh-plan", Executed: true, Entries: []kmsclient.ApplicationDefaultsApplyEntry{{Alias: "runtime", Key: "runtime", Status: "update", AppliedVersion: 4}}},
	}}
	var stdout, stderr bytes.Buffer
	exitCode := runDefaultsApplier(
		[]string{"--profile", "dev", "--insecure", "--overwrite", "--execute"},
		&stdout, &stderr, defaultsApplierTestConfig("dev/app"),
		func(kmsclient.Config) (defaultsApplyClient, error) { return client, nil },
	)
	if exitCode != 0 || stderr.Len() != 0 || len(client.calls) != 2 {
		t.Fatalf("exit=%d calls=%#v stderr=%q", exitCode, client.calls, stderr.String())
	}
	if !client.calls[0].Overwrite || client.calls[0].Execute || !client.calls[1].Overwrite ||
		!client.calls[1].Execute || client.calls[1].PlanDigest != "fresh-plan" {
		t.Fatalf("calls = %#v", client.calls)
	}
	if !strings.Contains(stdout.String(), "Preview defaults") || !strings.Contains(stdout.String(), "Applied defaults") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunDefaultsApplierBlocksDriftWithoutOverwrite(t *testing.T) {
	client := &fakeDefaultsApplyClient{results: []kmsclient.ApplicationDefaultsApplyResult{{
		Profile: "dev", PlanDigest: "plan",
		Entries: []kmsclient.ApplicationDefaultsApplyEntry{{Alias: "runtime", Key: "runtime", Status: "blocked"}},
	}}}
	var stdout, stderr bytes.Buffer
	exitCode := runDefaultsApplier(
		[]string{"--profile", "dev", "--insecure", "--execute"},
		&stdout, &stderr, defaultsApplierTestConfig("dev/app"),
		func(kmsclient.Config) (defaultsApplyClient, error) { return client, nil },
	)
	if exitCode != 1 || len(client.calls) != 1 || !strings.Contains(stderr.String(), "pass --overwrite") {
		t.Fatalf("exit=%d calls=%#v stderr=%q", exitCode, client.calls, stderr.String())
	}
}

func TestRunDefaultsApplierRequiresProductionConfirmation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	created := false
	exitCode := runDefaultsApplier(
		[]string{"--profile", "docker", "--execute"},
		&stdout, &stderr, defaultsApplierTestConfig("prod/app"),
		func(kmsclient.Config) (defaultsApplyClient, error) {
			created = true
			return nil, errors.New("unexpected")
		},
	)
	if exitCode != 2 || created || !strings.Contains(stderr.String(), "--confirm-production prod") {
		t.Fatalf("exit=%d created=%t stderr=%q", exitCode, created, stderr.String())
	}
}

func TestRunDefaultsApplierArgumentAndConfigurationFailures(t *testing.T) {
	for name, args := range map[string][]string{
		"missing profile": nil,
		"positional":      {"--profile", "dev", "extra"},
		"partial mtls":    {"--profile", "dev", "--cert", "client.pem"},
		"mixed transport": {"--profile", "dev", "--insecure", "--ca", "ca.pem"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exitCode := runDefaultsApplier(
				args, &stdout, &stderr, defaultsApplierTestConfig("dev/app"),
				func(kmsclient.Config) (defaultsApplyClient, error) { return &fakeDefaultsApplyClient{}, nil },
			)
			if exitCode != 2 {
				t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
			}
		})
	}
}

func TestRunDefaultsApplierSanitizesApplicationCallbacks(t *testing.T) {
	config := defaultsApplierTestConfig("dev/app")
	config.Provider = func(exporterTestProfile) (*exporterTestConfig, error) {
		return nil, errors.New("sensitive provider details")
	}
	var stdout, stderr bytes.Buffer
	exitCode := runDefaultsApplier(
		[]string{"--profile", "dev", "--insecure"}, &stdout, &stderr, config,
		func(kmsclient.Config) (defaultsApplyClient, error) { return &fakeDefaultsApplyClient{}, nil },
	)
	if exitCode != 1 || strings.Contains(stderr.String(), "sensitive provider details") {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
}
