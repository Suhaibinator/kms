package configstore

import (
	"bytes"
	"testing"
)

func TestManagedCommandsUseConnectionEnvironmentFallbacks(t *testing.T) {
	t.Setenv("KMS_ENDPOINT", "kms.environment:9443")
	t.Setenv("KMS_CA_FILE", "/environment/ca.pem")
	t.Setenv("KMS_CLIENT_CERT_FILE", "/environment/client.crt")
	t.Setenv("KMS_CLIENT_KEY_FILE", "/environment/client.key")

	tests := []struct {
		name  string
		parse func(*bytes.Buffer, *bytes.Buffer) (managedConnectionFlags, bool, error)
	}{
		{
			name: "defaults apply",
			parse: func(stdout, stderr *bytes.Buffer) (managedConnectionFlags, bool, error) {
				flags, help, err := parseDefaultsApplierFlags([]string{"--profile", "dev"}, stdout, stderr)
				return flags.managedConnectionFlags, help, err
			},
		},
		{
			name: "release create",
			parse: func(stdout, stderr *bytes.Buffer) (managedConnectionFlags, bool, error) {
				flags, help, err := parseManagedReleaseFlags([]string{"--profile", "dev"}, stdout, stderr)
				return flags.managedConnectionFlags, help, err
			},
		},
		{
			name: "schema upload",
			parse: func(stdout, stderr *bytes.Buffer) (managedConnectionFlags, bool, error) {
				flags, help, err := parseManagedSchemaFlags(nil, stdout, stderr)
				return flags.managedConnectionFlags, help, err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			flags, help, err := test.parse(&stdout, &stderr)
			if err != nil || help {
				t.Fatalf("parse: help=%t err=%v stderr=%q", help, err, stderr.String())
			}
			if flags.endpoint != "kms.environment:9443" || flags.ca != "/environment/ca.pem" ||
				flags.cert != "/environment/client.crt" || flags.key != "/environment/client.key" {
				t.Fatalf("connection flags = %#v", flags)
			}
		})
	}
}

func TestManagedConnectionFlagsBeatEnvironment(t *testing.T) {
	t.Setenv("KMS_ENDPOINT", "kms.environment:9443")
	t.Setenv("KMS_CA_FILE", "/environment/ca.pem")
	t.Setenv("KMS_CLIENT_CERT_FILE", "/environment/client.crt")
	t.Setenv("KMS_CLIENT_KEY_FILE", "/environment/client.key")

	var stdout, stderr bytes.Buffer
	flags, help, err := parseManagedSchemaFlags([]string{
		"--endpoint", "kms.flag:8443",
		"--ca", "/flag/ca.pem",
		"--cert", "/flag/client.crt",
		"--key", "/flag/client.key",
	}, &stdout, &stderr)
	if err != nil || help {
		t.Fatalf("parse: help=%t err=%v stderr=%q", help, err, stderr.String())
	}
	if flags.endpoint != "kms.flag:8443" || flags.ca != "/flag/ca.pem" ||
		flags.cert != "/flag/client.crt" || flags.key != "/flag/client.key" {
		t.Fatalf("connection flags = %#v", flags.managedConnectionFlags)
	}
}

func TestManagedConnectionFlagsUseBuiltInDefaults(t *testing.T) {
	t.Setenv("KMS_ENDPOINT", "")
	t.Setenv("KMS_CA_FILE", "")
	t.Setenv("KMS_CLIENT_CERT_FILE", "")
	t.Setenv("KMS_CLIENT_KEY_FILE", "")

	var stdout, stderr bytes.Buffer
	flags, help, err := parseManagedSchemaFlags(nil, &stdout, &stderr)
	if err != nil || help {
		t.Fatalf("parse: help=%t err=%v stderr=%q", help, err, stderr.String())
	}
	if flags.endpoint != managedConfigDefaultEndpoint || flags.ca != "" || flags.cert != "" || flags.key != "" {
		t.Fatalf("connection flags = %#v", flags.managedConnectionFlags)
	}
}
