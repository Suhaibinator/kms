// Package kmsverify runs a value-free comparison of an application's
// source-owned configuration defaults against the active KMS release, for use
// from a Go test or a CI script.
//
// A generated binding supplies the two halves of a Spec: how to build the
// defaults for a profile and how to verify them (its generated
// VerifyReleaseDefaults). Connection details come from the environment so the
// same test binary can run locally (skipped), in CI against a staging server,
// or as a release gate:
//
//	func TestDefaultsMatchKMS(t *testing.T) {
//		kmsverify.Run(t, kmsverify.Spec[rootconfig.Config]{
//			Defaults:  rootconfig.Defaults,             // func(profile string) (*rootconfig.Config, error)
//			Verify:    configkms.VerifyReleaseDefaults, // generated binding
//			Namespace: func(profile string) (string, error) { return profile + "/app", nil },
//		})
//	}
//
// Only canonical hashes travel to the server and only bounded verdicts come
// back; the report printed on failure names aliases, never values.
package kmsverify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

// Spec describes how to build and verify one application's defaults. The
// generated binding provides Defaults and Verify; Namespace is optional and
// derives the "env/app" namespace from the profile when the environment does
// not name one.
type Spec[T any] struct {
	// Defaults builds the source-owned configuration root for profile.
	Defaults func(profile string) (*T, error)
	// Verify hashes root and asks the server for verdicts. Generated bindings
	// expose this as VerifyReleaseDefaults.
	Verify func(ctx context.Context, client *kmsclient.Client, root *T, opts configstore.VerifyOptions) (configstore.VerifyResult, error)
	// Namespace optionally maps a profile to the namespace whose active
	// release is compared. It is consulted only when Env.Namespace is empty.
	Namespace func(profile string) (string, error)
}

// Env is the connection and selection input read from the environment by
// ParseEnv. Every field may also be set directly by scripts.
type Env struct {
	// Endpoint is the KMS gRPC host:port. Empty means "not configured".
	Endpoint string
	// Token is the identity token for the verification identity.
	Token string
	// CAFile is a path to the PEM bundle that verifies the server.
	CAFile string
	// CAPEM is the PEM bundle itself, for environments that inject secrets
	// as values rather than files. CAFile and CAPEM are mutually exclusive.
	CAPEM string
	// Profile selects the defaults profile; empty is the binding's default.
	Profile string
	// Namespace overrides Spec.Namespace.
	Namespace string
	// Release is the release name; empty selects "runtime".
	Release string
	// Required makes a missing Endpoint a failure instead of a skip.
	Required bool
	// Insecure permits a cleartext connection; only loopback endpoints are
	// accepted.
	Insecure bool
}

// Environment variable suffixes read by ParseEnv.
const (
	EnvEndpoint  = "KMS_VERIFY_ENDPOINT"
	EnvToken     = "KMS_VERIFY_TOKEN"
	EnvCAFile    = "KMS_VERIFY_CA_FILE"
	EnvCAPEM     = "KMS_VERIFY_CA_PEM"
	EnvProfile   = "KMS_VERIFY_PROFILE"
	EnvNamespace = "KMS_VERIFY_NAMESPACE"
	EnvRelease   = "KMS_VERIFY_RELEASE"
	EnvRequired  = "KMS_VERIFY_REQUIRED"
	EnvInsecure  = "KMS_VERIFY_INSECURE"
)

// DefaultRelease is the release name used when Env.Release is empty.
const DefaultRelease = "runtime"

// ParseEnv reads Env from <prefix>KMS_VERIFY_* variables. The prefix is
// normally empty; it lets one process verify several applications. Boolean
// variables accept the strconv.ParseBool forms plus yes/no and on/off.
func ParseEnv(prefix string) Env {
	get := func(name string) string { return strings.TrimSpace(os.Getenv(prefix + name)) }
	return Env{
		Endpoint:  get(EnvEndpoint),
		Token:     get(EnvToken),
		CAFile:    get(EnvCAFile),
		CAPEM:     strings.TrimSpace(os.Getenv(prefix + EnvCAPEM)),
		Profile:   get(EnvProfile),
		Namespace: get(EnvNamespace),
		Release:   get(EnvRelease),
		Required:  truthy(get(EnvRequired)),
		Insecure:  truthy(get(EnvInsecure)),
	}
}

func truthy(value string) bool {
	switch strings.ToLower(value) {
	case "yes", "y", "on":
		return true
	case "no", "n", "off", "":
		return false
	}
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}

// runTimeout bounds one Run invocation end to end.
const runTimeout = 2 * time.Minute

// Run verifies spec from a test. When KMS_VERIFY_ENDPOINT is unset the test
// is skipped, unless KMS_VERIFY_REQUIRED is truthy, in which case it fails.
// A verification that does not pass fails the test with the value-free
// report; a passing one logs it.
func Run[T any](t testing.TB, spec Spec[T]) {
	t.Helper()
	run(t, spec, ParseEnv(""))
}

func run[T any](t testing.TB, spec Spec[T], env Env) {
	t.Helper()
	if env.Endpoint == "" {
		if env.Required {
			t.Fatalf("kmsverify: %s is unset but %s requires verification", EnvEndpoint, EnvRequired)
			return
		}
		t.Skipf("kmsverify: %s is unset; set it to verify defaults against KMS", EnvEndpoint)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	result, err := Verify(ctx, spec, env)
	if err != nil {
		t.Fatalf("kmsverify: %v", err)
		return
	}
	if !result.Passed() {
		t.Fatalf("%s", result.Report())
		return
	}
	t.Logf("%s", result.Report())
}

// newClient is replaced by tests to reach an in-process fake server.
var newClient = kmsclient.NewClient

// Verify connects to env.Endpoint and verifies spec's defaults for
// env.Profile. It is the script-friendly form of Run: the caller decides how
// to present the result. The returned error explains configuration mistakes
// (missing endpoint or namespace, insecure non-loopback endpoint, unreadable
// CA) and transport or server failures; a completed comparison that does not
// match is reported through VerifyResult.Passed, not as an error.
func Verify[T any](ctx context.Context, spec Spec[T], env Env) (configstore.VerifyResult, error) {
	if spec.Defaults == nil || spec.Verify == nil {
		return configstore.VerifyResult{}, errors.New("kmsverify: Spec.Defaults and Spec.Verify are required")
	}
	env.Endpoint = strings.TrimSpace(env.Endpoint)
	if env.Endpoint == "" {
		return configstore.VerifyResult{}, fmt.Errorf("kmsverify: endpoint is required (set %s)", EnvEndpoint)
	}
	namespace := strings.TrimSpace(env.Namespace)
	if namespace == "" && spec.Namespace != nil {
		derived, err := spec.Namespace(env.Profile)
		if err != nil {
			return configstore.VerifyResult{}, fmt.Errorf("kmsverify: derive namespace for profile %q: %w", env.Profile, err)
		}
		namespace = strings.TrimSpace(derived)
	}
	if namespace == "" {
		return configstore.VerifyResult{}, fmt.Errorf("kmsverify: namespace is required (set %s or Spec.Namespace)", EnvNamespace)
	}
	release := strings.TrimSpace(env.Release)
	if release == "" {
		release = DefaultRelease
	}

	config := kmsclient.Config{
		Endpoint:   env.Endpoint,
		Token:      env.Token,
		ClientName: "kms-verify",
		Timeout:    30 * time.Second,
	}
	if env.Insecure {
		if env.CAFile != "" || env.CAPEM != "" {
			return configstore.VerifyResult{}, errors.New("kmsverify: insecure and a CA bundle are mutually exclusive")
		}
		if !loopbackEndpoint(env.Endpoint) {
			return configstore.VerifyResult{}, fmt.Errorf("kmsverify: %s is only permitted for loopback endpoints, not %q", EnvInsecure, env.Endpoint)
		}
		config.Insecure = true
	} else {
		tlsConfig, err := buildTLS(env)
		if err != nil {
			return configstore.VerifyResult{}, err
		}
		config.TLS = tlsConfig
	}

	root, err := spec.Defaults(env.Profile)
	if err != nil {
		return configstore.VerifyResult{}, fmt.Errorf("kmsverify: build defaults for profile %q: %w", env.Profile, err)
	}
	if root == nil {
		return configstore.VerifyResult{}, fmt.Errorf("kmsverify: Spec.Defaults returned nil for profile %q", env.Profile)
	}

	client, err := newClient(config)
	if err != nil {
		return configstore.VerifyResult{}, fmt.Errorf("kmsverify: connect to %s: %w", env.Endpoint, err)
	}
	defer func() { _ = client.Close() }()

	result, err := spec.Verify(ctx, client, root, configstore.VerifyOptions{
		Namespace: namespace,
		Release:   release,
		Profile:   env.Profile,
	})
	if err != nil {
		return configstore.VerifyResult{}, fmt.Errorf("kmsverify: verify %s %s: %w", namespace, release, err)
	}
	return result, nil
}

// buildTLS returns the TLS configuration for a secure connection: system
// roots by default, or the CA bundle from CAFile or CAPEM. CAPEM is written
// to a private temporary file for the duration of the call because
// kmsclient.TLSConfig accepts a path.
func buildTLS(env Env) (tlsConfig *tls.Config, err error) {
	if env.CAFile != "" && env.CAPEM != "" {
		return nil, fmt.Errorf("kmsverify: %s and %s are mutually exclusive", EnvCAFile, EnvCAPEM)
	}
	caFile := env.CAFile
	if env.CAPEM != "" {
		file, createErr := os.CreateTemp("", "kms-verify-ca-*.pem")
		if createErr != nil {
			return nil, fmt.Errorf("kmsverify: stage CA bundle: %w", createErr)
		}
		defer func() { _ = os.Remove(file.Name()) }()
		if _, writeErr := file.WriteString(env.CAPEM); writeErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("kmsverify: stage CA bundle: %w", writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return nil, fmt.Errorf("kmsverify: stage CA bundle: %w", closeErr)
		}
		caFile = file.Name()
	}
	tlsConfig, err = kmsclient.TLSConfig(caFile)
	if err != nil {
		return nil, fmt.Errorf("kmsverify: %w", err)
	}
	return tlsConfig, nil
}

// loopbackEndpoint reports whether endpoint's host is localhost or a loopback
// IP address. Anything unparsable is not loopback.
func loopbackEndpoint(endpoint string) bool {
	host := endpoint
	if h, _, err := net.SplitHostPort(endpoint); err == nil {
		host = h
	} else {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
