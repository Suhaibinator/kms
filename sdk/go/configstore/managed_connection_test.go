package configstore

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestManagedCommandsUseConnectionEnvironmentFallbacks(t *testing.T) {
	t.Setenv("KMS_ENDPOINT", "kms.environment:9443")
	t.Setenv("KMS_CA_FILE", "/environment/ca.pem")
	t.Setenv("KMS_CLIENT_CERT_FILE", "/environment/client.crt")
	t.Setenv("KMS_CLIENT_KEY_FILE", "/environment/client.key")
	t.Setenv("KMS_RPC_TIMEOUT", "90s")

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
				flags.cert != "/environment/client.crt" || flags.key != "/environment/client.key" ||
				flags.timeout != 90*time.Second {
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
	t.Setenv("KMS_RPC_TIMEOUT", "90s")

	var stdout, stderr bytes.Buffer
	flags, help, err := parseManagedSchemaFlags([]string{
		"--endpoint", "kms.flag:8443",
		"--timeout", "2m",
		"--ca", "/flag/ca.pem",
		"--cert", "/flag/client.crt",
		"--key", "/flag/client.key",
	}, &stdout, &stderr)
	if err != nil || help {
		t.Fatalf("parse: help=%t err=%v stderr=%q", help, err, stderr.String())
	}
	if flags.endpoint != "kms.flag:8443" || flags.ca != "/flag/ca.pem" ||
		flags.cert != "/flag/client.crt" || flags.key != "/flag/client.key" || flags.timeout != 2*time.Minute {
		t.Fatalf("connection flags = %#v", flags.managedConnectionFlags)
	}
}

func TestManagedConnectionFlagsUseBuiltInDefaults(t *testing.T) {
	t.Setenv("KMS_ENDPOINT", "")
	t.Setenv("KMS_CA_FILE", "")
	t.Setenv("KMS_CLIENT_CERT_FILE", "")
	t.Setenv("KMS_CLIENT_KEY_FILE", "")
	t.Setenv("KMS_RPC_TIMEOUT", "")

	var stdout, stderr bytes.Buffer
	flags, help, err := parseManagedSchemaFlags(nil, &stdout, &stderr)
	if err != nil || help {
		t.Fatalf("parse: help=%t err=%v stderr=%q", help, err, stderr.String())
	}
	if flags.endpoint != managedConfigDefaultEndpoint || flags.ca != "" || flags.cert != "" || flags.key != "" ||
		flags.timeout != managedConfigDefaultTimeout {
		t.Fatalf("connection flags = %#v", flags.managedConnectionFlags)
	}
}

func TestManagedConnectionRejectsNonPositiveTimeouts(t *testing.T) {
	tests := []struct {
		name string
		env  string
		args []string
		want string
	}{
		{name: "zero flag", args: []string{"--timeout", "0s"}, want: "--timeout"},
		{name: "negative flag", args: []string{"--timeout", "-5s"}, want: "--timeout"},
		{name: "unparseable environment", env: "soon", want: "KMS_RPC_TIMEOUT"},
		{name: "zero environment", env: "0s", want: "KMS_RPC_TIMEOUT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("KMS_RPC_TIMEOUT", test.env)
			var stdout, stderr bytes.Buffer
			_, help, err := parseManagedSchemaFlags(test.args, &stdout, &stderr)
			if help || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse: help=%t err=%v, want error mentioning %q", help, err, test.want)
			}
		})
	}
}

func TestManagedConnectionTimeoutReachesClientConfig(t *testing.T) {
	config, err := defaultsApplierClientConfig(defaultsApplierFlags{managedConnectionFlags: managedConnectionFlags{
		endpoint: "kms.flag:8443", insecure: true, timeout: 3 * time.Minute,
	}}, "dev/app")
	if err != nil {
		t.Fatalf("client config: %v", err)
	}
	if config.Timeout != 3*time.Minute {
		t.Fatalf("client timeout = %s, want 3m", config.Timeout)
	}
}
