package configstore

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"
)

const (
	managedConfigDefaultEndpoint = "localhost:8443"
	// managedConfigDefaultTimeout bounds each administrative RPC. It is longer
	// than the kmsclient data-plane default because schema upload, defaults
	// apply, and release creation do server-side validation over the whole
	// application contract.
	managedConfigDefaultTimeout = 60 * time.Second
)

// managedConnectionFlags is the connection surface shared by every managed
// configuration command. String settings resolve in the same order as the KMS
// CLI: flag, KMS_* environment variable, then built-in default.
//
// --timeout resolves the same way from KMS_RPC_TIMEOUT, then the built-in
// default.
//
// --insecure intentionally remains flag-only. An ambient environment variable
// must not silently downgrade an administrative command from TLS.
type managedConnectionFlags struct {
	endpoint string
	insecure bool
	ca       string
	cert     string
	key      string
	timeout  time.Duration
}

func addManagedConnectionFlags(set *flag.FlagSet, result *managedConnectionFlags) {
	set.StringVar(&result.endpoint, "endpoint", "", "KMS gRPC endpoint")
	set.BoolVar(&result.insecure, "insecure", false, "disable TLS for local development")
	set.StringVar(&result.ca, "ca", "", "CA bundle for server verification")
	set.StringVar(&result.cert, "cert", "", "client certificate for mTLS")
	set.StringVar(&result.key, "key", "", "client private key for mTLS")
	set.DurationVar(&result.timeout, "timeout", 0, "per-RPC timeout")
}

func resolveManagedConnectionFlags(set *flag.FlagSet, result *managedConnectionFlags) error {
	explicit := make(map[string]bool)
	set.Visit(func(value *flag.Flag) {
		explicit[value.Name] = true
	})
	for _, fallback := range []struct {
		flagName string
		envName  string
		target   *string
	}{
		{"endpoint", "KMS_ENDPOINT", &result.endpoint},
		{"ca", "KMS_CA_FILE", &result.ca},
		{"cert", "KMS_CLIENT_CERT_FILE", &result.cert},
		{"key", "KMS_CLIENT_KEY_FILE", &result.key},
	} {
		if explicit[fallback.flagName] {
			continue
		}
		if value, ok := os.LookupEnv(fallback.envName); ok {
			*fallback.target = value
		}
	}
	if result.endpoint == "" {
		result.endpoint = managedConfigDefaultEndpoint
	}
	if explicit["timeout"] {
		if result.timeout <= 0 {
			return errors.New("--timeout must be a positive duration")
		}
		return nil
	}
	if value, ok := os.LookupEnv("KMS_RPC_TIMEOUT"); ok && value != "" {
		timeout, err := time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return fmt.Errorf("KMS_RPC_TIMEOUT must be a positive duration, got %q", value)
		}
		result.timeout = timeout
		return nil
	}
	result.timeout = managedConfigDefaultTimeout
	return nil
}
