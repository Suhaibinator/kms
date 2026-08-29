package kmsverify

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient/kmsclienttest"
)

// testRoot stands in for a generated application configuration root.
type testRoot struct {
	Limit int
}

var testContract = []configstore.ContractEntry{
	{Alias: "limits", Kind: configstore.ContractKindParameter, ContentType: "json"},
	{Alias: "db_password", Kind: configstore.ContractKindSecret},
}

func testSpec() Spec[testRoot] {
	return Spec[testRoot]{
		Defaults: func(profile string) (*testRoot, error) {
			if profile == "broken" {
				return nil, errors.New("no such profile")
			}
			return &testRoot{Limit: 10}, nil
		},
		Verify: func(ctx context.Context, client *kmsclient.Client, root *testRoot, opts configstore.VerifyOptions) (configstore.VerifyResult, error) {
			return configstore.VerifyDefaults(ctx, client, configstore.VerifyInput{
				SchemaSHA256: strings.Repeat("a", 64),
				Contract:     testContract,
				Groups: map[string]json.RawMessage{
					"limits": json.RawMessage(fmt.Sprintf(`{"limit":%d}`, root.Limit)),
				},
			}, opts)
		},
		Namespace: func(profile string) (string, error) {
			if profile == "" {
				return "", errors.New("profile required to derive namespace")
			}
			return profile + "/app", nil
		},
	}
}

// fakeServer routes every Verify through an in-process kmsclienttest server
// and records the client configuration kmsverify built.
type fakeServer struct {
	server *kmsclienttest.Server
	mu     sync.Mutex
	config kmsclient.Config
}

func installFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	server, err := kmsclienttest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	fake := &fakeServer{server: server}
	previous := newClient
	newClient = func(config kmsclient.Config) (*kmsclient.Client, error) {
		fake.mu.Lock()
		fake.config = config
		fake.mu.Unlock()
		config.Endpoint = server.Target()
		config.DialOptions = server.DialOptions()
		return kmsclient.NewClient(config)
	}
	t.Cleanup(func() { newClient = previous })
	return fake
}

func (f *fakeServer) clientConfig() kmsclient.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.config
}

func (f *fakeServer) queue(schemaMatches bool, verdict string) {
	f.server.QueueVerifyReleaseDefaultsResponse(&kmsv1.VerifyReleaseDefaultsResponse{
		Name: "runtime", Version: 3, ActivationRevision: 8, SchemaMatches: schemaMatches,
		Entries: []*kmsv1.VerifyEntryVerdict{{Alias: "limits", Verdict: verdict}},
	}, nil)
}

// recorderTB is a testing.TB that records outcomes instead of ending the
// goroutine. Fatal and Skip stop the caller through a panic that the test
// recovers with runRecorded.
type recorderTB struct {
	testing.TB
	logs    []string
	fatal   string
	skipped string
}

type recorderStop struct{}

func (r *recorderTB) Helper() {}
func (r *recorderTB) Logf(format string, args ...any) {
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}
func (r *recorderTB) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	panic(recorderStop{})
}
func (r *recorderTB) Skipf(format string, args ...any) {
	r.skipped = fmt.Sprintf(format, args...)
	panic(recorderStop{})
}

func runRecorded(spec Spec[testRoot], env Env) *recorderTB {
	recorder := &recorderTB{}
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				if _, ok := recovered.(recorderStop); !ok {
					panic(recovered)
				}
			}
		}()
		run(recorder, spec, env)
	}()
	return recorder
}

func TestParseEnvReadsPrefixedVariables(t *testing.T) {
	for _, name := range []string{EnvEndpoint, EnvToken, EnvCAFile, EnvCAPEM, EnvProfile, EnvNamespace, EnvRelease, EnvRequired, EnvInsecure} {
		t.Setenv(name, "")
		t.Setenv("APP_"+name, "")
	}
	if env := ParseEnv(""); env != (Env{}) {
		t.Fatalf("empty environment parsed as %+v", env)
	}
	t.Setenv("APP_"+EnvEndpoint, " kms.example.com:8443 ")
	t.Setenv("APP_"+EnvToken, "token-value")
	t.Setenv("APP_"+EnvCAFile, "/etc/kms/ca.pem")
	t.Setenv("APP_"+EnvCAPEM, "-----BEGIN CERTIFICATE-----\n")
	t.Setenv("APP_"+EnvProfile, "staging")
	t.Setenv("APP_"+EnvNamespace, "staging/app")
	t.Setenv("APP_"+EnvRelease, "canary")
	t.Setenv("APP_"+EnvRequired, "yes")
	t.Setenv("APP_"+EnvInsecure, "1")
	env := ParseEnv("APP_")
	want := Env{
		Endpoint: "kms.example.com:8443", Token: "token-value", CAFile: "/etc/kms/ca.pem",
		CAPEM: "-----BEGIN CERTIFICATE-----", Profile: "staging", Namespace: "staging/app",
		Release: "canary", Required: true, Insecure: true,
	}
	if env != want {
		t.Fatalf("ParseEnv(APP_) = %+v, want %+v", env, want)
	}
	if unprefixed := ParseEnv(""); unprefixed.Endpoint != "" || unprefixed.Required {
		t.Fatalf("unprefixed ParseEnv read prefixed variables: %+v", unprefixed)
	}
	for value, want := range map[string]bool{"true": true, "TRUE": true, "on": true, "y": true, "false": false, "off": false, "maybe": false, "": false} {
		if got := truthy(value); got != want {
			t.Fatalf("truthy(%q) = %v", value, got)
		}
	}
}

func TestRunSkipsWithoutEndpointUnlessRequired(t *testing.T) {
	recorder := runRecorded(testSpec(), Env{})
	if recorder.skipped == "" || !strings.Contains(recorder.skipped, EnvEndpoint) || recorder.fatal != "" {
		t.Fatalf("recorder = %+v, want skip naming %s", recorder, EnvEndpoint)
	}
	recorder = runRecorded(testSpec(), Env{Required: true})
	if recorder.fatal == "" || !strings.Contains(recorder.fatal, EnvRequired) || recorder.skipped != "" {
		t.Fatalf("recorder = %+v, want fatal naming %s", recorder, EnvRequired)
	}

	// Run itself reads the environment.
	t.Setenv(EnvEndpoint, "")
	t.Setenv(EnvRequired, "")
	recorder = &recorderTB{}
	func() {
		defer func() { _ = recover() }()
		Run(recorder, testSpec())
	}()
	if recorder.skipped == "" {
		t.Fatalf("Run did not skip: %+v", recorder)
	}
}

func TestRunPassesAndLogsReport(t *testing.T) {
	fake := installFakeServer(t)
	fake.queue(true, kmsclient.VerifyVerdictMatch)
	recorder := runRecorded(testSpec(), Env{Endpoint: "localhost:1", Insecure: true, Profile: "prod", Token: "verify-token"})
	if recorder.fatal != "" || recorder.skipped != "" {
		t.Fatalf("recorder = %+v", recorder)
	}
	if len(recorder.logs) != 1 || !strings.Contains(recorder.logs[0], "result: active release matches source defaults") ||
		!strings.Contains(recorder.logs[0], "prod/app runtime@3#8") {
		t.Fatalf("logs = %q", recorder.logs)
	}
	calls := fake.server.VerifyReleaseDefaultsCalls()
	if len(calls) != 1 || calls[0].GetNamespace().GetEnv() != "prod" || calls[0].GetNamespace().GetApp() != "app" ||
		calls[0].GetName() != DefaultRelease || calls[0].GetProfile() != "prod" || len(calls[0].GetEntries()) != 1 {
		t.Fatalf("calls = %v", calls)
	}
	wantHash, _ := configstore.ParameterHash("json", []byte(`{"limit":10}`))
	if calls[0].GetEntries()[0].GetAlias() != "limits" || calls[0].GetEntries()[0].GetSha256() != wantHash {
		t.Fatalf("entry = %v, want limits hash %s", calls[0].GetEntries()[0], wantHash)
	}
	if got := fake.server.LastMetadata("VerifyReleaseDefaults").Get("authorization"); len(got) != 1 || got[0] != "Bearer verify-token" {
		t.Fatalf("authorization metadata = %v", got)
	}
	config := fake.clientConfig()
	if config.ClientName != "kms-verify" || config.Timeout != 30*time.Second || !config.Insecure || config.TLS != nil || config.Endpoint != "localhost:1" {
		t.Fatalf("client config = %+v", config)
	}
}

func TestRunFailsWithValueFreeReport(t *testing.T) {
	fake := installFakeServer(t)
	fake.queue(true, kmsclient.VerifyVerdictDiffers)
	recorder := runRecorded(testSpec(), Env{Endpoint: "127.0.0.1:1", Insecure: true, Namespace: "prod/app", Release: "canary"})
	if recorder.fatal == "" || recorder.skipped != "" || len(recorder.logs) != 0 {
		t.Fatalf("recorder = %+v", recorder)
	}
	if !strings.Contains(recorder.fatal, "differs  limits  json") || !strings.Contains(recorder.fatal, "result: active release differs from source defaults") {
		t.Fatalf("fatal report = %q", recorder.fatal)
	}
	if strings.Contains(recorder.fatal, "10") && strings.Contains(recorder.fatal, "limit\":") {
		t.Fatalf("report contained a value: %q", recorder.fatal)
	}
	calls := fake.server.VerifyReleaseDefaultsCalls()
	if len(calls) != 1 || calls[0].GetName() != "canary" || calls[0].GetNamespace().GetEnv() != "prod" {
		t.Fatalf("calls = %v", calls)
	}

	// Transport and server errors surface as fatal too, with context.
	fake.server.QueueVerifyReleaseDefaultsResponse(nil, errors.New("verify budget exhausted"))
	recorder = runRecorded(testSpec(), Env{Endpoint: "127.0.0.1:1", Insecure: true, Namespace: "prod/app"})
	if recorder.fatal == "" || !strings.Contains(recorder.fatal, "kmsverify: verify prod/app runtime") {
		t.Fatalf("recorder = %+v", recorder)
	}
}

func TestVerifyRejectsMisconfiguration(t *testing.T) {
	fake := installFakeServer(t)
	spec := testSpec()
	tests := []struct {
		name string
		spec Spec[testRoot]
		env  Env
		want string
	}{
		{name: "missing endpoint", spec: spec, env: Env{Insecure: true}, want: "endpoint is required"},
		{name: "missing spec", spec: Spec[testRoot]{}, env: Env{Endpoint: "localhost:1"}, want: "Spec.Defaults and Spec.Verify"},
		{name: "insecure public endpoint", spec: spec, env: Env{Endpoint: "kms.example.com:8443", Insecure: true, Namespace: "prod/app"}, want: "only permitted for loopback"},
		{name: "insecure private endpoint", spec: spec, env: Env{Endpoint: "10.0.0.5:8443", Insecure: true, Namespace: "prod/app"}, want: "only permitted for loopback"},
		{name: "insecure with ca", spec: spec, env: Env{Endpoint: "localhost:1", Insecure: true, CAFile: "ca.pem", Namespace: "prod/app"}, want: "mutually exclusive"},
		{name: "namespace underivable", spec: spec, env: Env{Endpoint: "localhost:1", Insecure: true}, want: "derive namespace"},
		{name: "namespace missing", spec: Spec[testRoot]{Defaults: spec.Defaults, Verify: spec.Verify}, env: Env{Endpoint: "localhost:1", Insecure: true}, want: "namespace is required"},
		{name: "defaults fail", spec: spec, env: Env{Endpoint: "localhost:1", Insecure: true, Profile: "broken"}, want: "build defaults for profile \"broken\""},
		{name: "ca file and pem", spec: spec, env: Env{Endpoint: "localhost:1", CAFile: "a", CAPEM: "b", Namespace: "prod/app"}, want: "mutually exclusive"},
		{name: "ca file unreadable", spec: spec, env: Env{Endpoint: "localhost:1", CAFile: filepath.Join(t.TempDir(), "missing.pem"), Namespace: "prod/app"}, want: "read CA file"},
		{name: "ca pem invalid", spec: spec, env: Env{Endpoint: "localhost:1", CAPEM: "not a certificate", Namespace: "prod/app"}, want: "no certificates found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Verify(context.Background(), tt.spec, tt.env)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
	if calls := fake.server.VerifyReleaseDefaultsCalls(); len(calls) != 0 {
		t.Fatalf("misconfigured runs reached the server: %d", len(calls))
	}
	nilRoot := spec
	nilRoot.Defaults = func(string) (*testRoot, error) { return nil, nil }
	if _, err := Verify(context.Background(), nilRoot, Env{Endpoint: "localhost:1", Insecure: true, Namespace: "prod/app"}); err == nil || !strings.Contains(err.Error(), "returned nil") {
		t.Fatalf("nil root error = %v", err)
	}
}

func TestLoopbackEndpoint(t *testing.T) {
	for endpoint, want := range map[string]bool{
		"localhost:8443": true, "LOCALHOST:1": true, "127.0.0.1:8443": true, "127.0.0.1": true, "[::1]:8443": true, "::1": true,
		"127.1.2.3:1": true, "kms.example.com:8443": false, "10.0.0.1:8443": false, "": false, "[::]:1": false, "localhost.example:1": false,
	} {
		if got := loopbackEndpoint(endpoint); got != want {
			t.Fatalf("loopbackEndpoint(%q) = %v, want %v", endpoint, got, want)
		}
	}
}

func selfSignedCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kmsverify test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestVerifyBuildsTLSFromCAFileOrPEM(t *testing.T) {
	fake := installFakeServer(t)
	caPEM := selfSignedCAPEM(t)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, []byte(caPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	stagedBefore := stagedCAFiles(t)
	for _, tt := range []struct {
		name string
		env  Env
	}{
		{name: "file", env: Env{Endpoint: "kms.example.com:8443", CAFile: caFile, Namespace: "prod/app"}},
		{name: "pem", env: Env{Endpoint: "kms.example.com:8443", CAPEM: caPEM, Namespace: "prod/app"}},
		{name: "system roots", env: Env{Endpoint: "kms.example.com:8443", Namespace: "prod/app"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake.queue(true, kmsclient.VerifyVerdictMatch)
			result, err := Verify(context.Background(), testSpec(), tt.env)
			if err != nil || !result.Passed() {
				t.Fatalf("Verify() = (%+v, %v)", result, err)
			}
			config := fake.clientConfig()
			if config.Insecure || config.TLS == nil || config.TLS.MinVersion < 0x0303 {
				t.Fatalf("client config = %+v", config)
			}
			if tt.name == "system roots" {
				if config.TLS.RootCAs != nil {
					t.Fatal("system roots run pinned a CA pool")
				}
				return
			}
			if config.TLS.RootCAs == nil || config.TLS.RootCAs.Equal(x509.NewCertPool()) {
				t.Fatal("CA bundle was not loaded into RootCAs")
			}
		})
	}
	if after := stagedCAFiles(t); len(after) != len(stagedBefore) {
		t.Fatalf("staged CA files leaked: before=%v after=%v", stagedBefore, after)
	}
}

func stagedCAFiles(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "kms-verify-ca-*.pem"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}
