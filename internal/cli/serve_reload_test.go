package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/Suhaibinator/kms/internal/config"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/server/listenertls"
)

// These tests drive reloadServe directly — no listeners, no signals — so each
// case pins one contract of a reload: which keys are applied, which are only
// reported, that a rejected file changes nothing at all, and the fields an
// operator reads out of the two log lines. The end-to-end path through a real
// socket lives in serve_wiring_test.go.

// recordingReporter captures the outcomes serve hands the metrics exporter.
type recordingReporter struct{ results []string }

func (r *recordingReporter) ReloadResult(result string) { r.results = append(r.results, result) }

// reloadEnv is the state a running serve holds between reloads: the resolver
// built from its flag set, the config file it re-reads, the configuration
// currently in effect, and the atomic level its logger enables from.
type reloadEnv struct {
	c       *testCLI
	r       *settingsResolver
	path    string
	running config.Config
	level   zap.AtomicLevel
}

// newReloadEnv wires the resolver exactly as cmdServe does — a "serve" flag
// set carrying every setting flag plus --config — parses args through the same
// parseFlags, and resolves once for the running configuration.
func newReloadEnv(t *testing.T, body string, args ...string) *reloadEnv {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeConfigFile(t, path, body)

	c := newTestCLI()
	flags := c.newFlags("serve")
	r := c.serverSettings(flags)
	c.ConfigPath = path
	if !c.parseFlags(flags, args) {
		t.Fatalf("parse serve flags %v: %s", args, c.stderr())
	}
	running, _, _, err := r.resolve()
	if err != nil {
		t.Fatalf("resolve the running configuration: %v", err)
	}
	if err := running.Validate(); err != nil {
		t.Fatalf("the running configuration is invalid: %v", err)
	}
	return &reloadEnv{c: c, r: r, path: path, running: running, level: zap.NewAtomicLevelAt(running.LogLevel())}
}

// rewrite replaces the config file, as an operator editing it would.
func (e *reloadEnv) rewrite(t *testing.T, body string) {
	t.Helper()
	writeConfigFile(t, e.path, body)
}

// reloadOutcome is everything one reload produced.
type reloadOutcome struct {
	err  error
	logs *observer.ObservedLogs
	rep  *recordingReporter
}

// reload runs one reload and adopts the configuration it returns, so a test can
// chain reloads the way the serve loop does.
func (e *reloadEnv) reload(t *testing.T, holder *listenertls.Reloadable, svc *core.Service) reloadOutcome {
	t.Helper()
	logger, logs := observedLogger()
	rep := &recordingReporter{}
	next, err := e.c.reloadServe(context.Background(), e.r, logger, e.level, holder, svc, e.running, rep)
	e.running = next
	return reloadOutcome{err: err, logs: logs, rep: rep}
}

func writeConfigFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config file %s: %v", path, err)
	}
}

// stringsField reads a zap.Strings field off a logged entry. The observer
// encodes an array field as []any, so the values are converted back.
func stringsField(t *testing.T, e observer.LoggedEntry, key string) []string {
	t.Helper()
	raw, ok := e.ContextMap()[key]
	if !ok {
		t.Fatalf("entry %q has no field %q; fields: %v", e.Message, key, e.ContextMap())
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("field %q = %T, want an array", key, raw)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("field %q holds a %T, want strings", key, item)
		}
		out = append(out, s)
	}
	return out
}

// assertKeys compares a changed/ignored list against the keys it must hold.
func assertKeys(t *testing.T, e observer.LoggedEntry, field string, want []string) {
	t.Helper()
	if got := stringsField(t, e, field); !slices.Equal(got, want) {
		t.Errorf("%s = %v, want %v", field, got, want)
	}
}

// writeClientCA writes a self-signed CA bundle standing in for the operator's
// security.client_ca_file, and returns its path.
func writeClientCA(t *testing.T, path, cn string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(9),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatalf("create client CA certificate: %v", err)
	}
	writeFileAtomically(t, path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// tlsHolderFor builds the Reloadable serve installs at startup from cfg's TLS
// material, so a reload has a live holder to swap under.
func tlsHolderFor(t *testing.T, cfg config.Config, svc *core.Service) *listenertls.Reloadable {
	t.Helper()
	base, err := cfg.BuildServerTLS()
	if err != nil {
		t.Fatalf("build server TLS: %v", err)
	}
	derived, err := listenertls.Build(base, svc)
	if err != nil {
		t.Fatalf("build listener TLS: %v", err)
	}
	return listenertls.NewReloadable(derived)
}

// reloadTestService is the core.Service a TLS reload needs: the built-in CA to
// derive the listener config, and the admin-certificate report the reload
// restates after a swap.
func reloadTestService(t *testing.T, c *testCLI) *core.Service {
	t.Helper()
	db, keyFile := initKMS(t, "ops")
	svc, _, destroy := localAdminService(t, c, db, keyFile)
	t.Cleanup(destroy)
	return svc
}

// TestReloadServeAppliesLogLevel is the smallest complete reload: one
// reloadable key edited in the file, applied to the live logger, and reported.
func TestReloadServeAppliesLogLevel(t *testing.T) {
	env := newReloadEnv(t, "log:\n  level: info\n")
	env.rewrite(t, "log:\n  level: debug\n")

	out := env.reload(t, nil, nil)
	if out.err != nil {
		t.Fatalf("reload: %v", out.err)
	}
	if env.running.Log.Level != "debug" {
		t.Errorf("running log.level = %q, want debug", env.running.Log.Level)
	}
	if got := env.level.Level(); got != zapcore.DebugLevel {
		t.Errorf("logger level = %v, want debug", got)
	}
	e := entryFor(t, out.logs, configReloadedMsg)
	if e.Level != zapcore.InfoLevel {
		t.Errorf("%q level = %v, want info", configReloadedMsg, e.Level)
	}
	assertKeys(t, e, "changed", []string{"log.level"})
	assertKeys(t, e, "ignored", nil)
	if got := e.ContextMap()["log_level"]; got != "debug" {
		t.Errorf("log_level field = %v, want debug", got)
	}
	if got := e.ContextMap()["tls"]; got != false {
		t.Errorf("tls field = %v, want false", got)
	}
	// With TLS off there is no certificate to describe, so those fields are
	// absent rather than empty.
	if _, ok := e.ContextMap()["server_certificate_serial"]; ok {
		t.Error("a TLS-off reload reported a server certificate")
	}
	if !slices.Equal(out.rep.results, []string{reloadApplied}) {
		t.Errorf("reported %v, want [%s]", out.rep.results, reloadApplied)
	}
}

// TestReloadServeIgnoresListenAddress: the listeners are bound for the process
// lifetime, so a changed address is reported and not acted on. Reporting it
// matters — the operator has to learn that the file and the process disagree.
func TestReloadServeIgnoresListenAddress(t *testing.T) {
	env := newReloadEnv(t, "server:\n  http_addr: \"127.0.0.1:9001\"\n")
	env.rewrite(t, "server:\n  http_addr: \"127.0.0.1:9002\"\n")

	out := env.reload(t, nil, nil)
	if out.err != nil {
		t.Fatalf("reload: %v", out.err)
	}
	e := entryFor(t, out.logs, configReloadedMsg)
	assertKeys(t, e, "ignored", []string{"server.http_addr"})
	assertKeys(t, e, "changed", nil)
	if env.running.Server.HTTPAddr != "127.0.0.1:9001" {
		t.Errorf("running http_addr = %q, want the address the listener is bound to", env.running.Server.HTTPAddr)
	}
	if !slices.Equal(out.rep.results, []string{reloadApplied}) {
		t.Errorf("reported %v, want [%s]", out.rep.results, reloadApplied)
	}
}

// TestReloadServeRejectsBadConfiguration: a file the server cannot use must
// leave everything exactly as it was — one error line, nothing swapped, nothing
// applied, and the error handed back to the caller.
func TestReloadServeRejectsBadConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"unknown key", "log:\n  level: debug\nstorage:\n  sqlite_pth: \"/tmp/x.db\"\n"},
		{"malformed yaml", "log:\n  level: debug\n  : [\n"},
		{"fails validation", "log:\n  level: debug\nwatch:\n  retain_rows: 0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newReloadEnv(t, "log:\n  level: info\n")
			before := env.running
			env.rewrite(t, tc.body)

			out := env.reload(t, nil, nil)
			if out.err == nil {
				t.Fatal("reload accepted a configuration it cannot use")
			}
			e := entryFor(t, out.logs, configReloadFailedMsg)
			if e.Level != zapcore.ErrorLevel {
				t.Errorf("%q level = %v, want error", configReloadFailedMsg, e.Level)
			}
			if n := out.logs.FilterMessage(configReloadedMsg).Len(); n != 0 {
				t.Errorf("a rejected reload also logged %q %d times", configReloadedMsg, n)
			}
			if env.running != before {
				t.Errorf("running configuration changed: %+v, want %+v", env.running, before)
			}
			if got := env.level.Level(); got != zapcore.InfoLevel {
				t.Errorf("logger level = %v, want the running info level", got)
			}
			if !slices.Equal(out.rep.results, []string{reloadRejected}) {
				t.Errorf("reported %v, want [%s]", out.rep.results, reloadRejected)
			}
		})
	}
}

// TestReloadServeUnknownLogLevelIsRejected: a level the logger does not know
// is a typo, and Config.Validate refuses it — so the reload is rejected as a
// whole rather than quietly turning a debug session into an info one.
func TestReloadServeUnknownLogLevelIsRejected(t *testing.T) {
	env := newReloadEnv(t, "log:\n  level: debug\n")
	env.rewrite(t, "log:\n  level: bogus\n")

	out := env.reload(t, nil, nil)
	if out.err == nil || !strings.Contains(out.err.Error(), "log.level") {
		t.Fatalf("reload err = %v, want a log.level validation error", out.err)
	}
	if got := env.level.Level(); got != zapcore.DebugLevel {
		t.Errorf("logger level = %v, want the running debug level", got)
	}
	entryFor(t, out.logs, configReloadFailedMsg)
}

// TestReloadServeRotatesServerCertificate is the cert-rotation path: the paths
// did not change, only the bytes behind them, and the reload picks that up,
// swaps the holder, and names the new serial. A second reload with nothing
// touched reports no change, so an operator can tell a rotation from a no-op.
func TestReloadServeRotatesServerCertificate(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, _, _ := writeServerCert(t, dir, 101)
	env := newReloadEnv(t, tlsConfigBody(certFile, keyFile))
	svc := reloadTestService(t, env.c)
	holder := tlsHolderFor(t, env.running, svc)

	_, _, _, leafB := writeServerCert(t, dir, 202)
	out := env.reload(t, holder, svc)
	if out.err != nil {
		t.Fatalf("reload: %v", out.err)
	}
	e := entryFor(t, out.logs, configReloadedMsg)
	if got := e.ContextMap()["server_certificate_changed"]; got != true {
		t.Errorf("server_certificate_changed = %v, want true", got)
	}
	if got, want := e.ContextMap()["server_certificate_serial"], leafB.SerialNumber.Text(16); got != want {
		t.Errorf("server_certificate_serial = %v, want %q", got, want)
	}
	if got, ok := e.ContextMap()["server_certificate_not_after"].(time.Time); !ok || !got.Equal(leafB.NotAfter) {
		t.Errorf("server_certificate_not_after = %v, want %v", e.ContextMap()["server_certificate_not_after"], leafB.NotAfter)
	}
	if got := e.ContextMap()["client_ca_changed"]; got != false {
		t.Errorf("client_ca_changed = %v, want false", got)
	}
	if got := e.ContextMap()["tls"]; got != true {
		t.Errorf("tls field = %v, want true", got)
	}
	// The holder now serves the new keypair to every new handshake.
	if leaf := leafCertificate(holder.Current()); leaf == nil || leaf.SerialNumber.Cmp(leafB.SerialNumber) != 0 {
		t.Fatalf("holder serves %v, want serial %v", leaf, leafB.SerialNumber)
	}

	// Nothing touched: the same reload signal must not claim a rotation.
	again := env.reload(t, holder, svc)
	if again.err != nil {
		t.Fatalf("second reload: %v", again.err)
	}
	if got := entryFor(t, again.logs, configReloadedMsg).ContextMap()["server_certificate_changed"]; got != false {
		t.Errorf("server_certificate_changed on an unchanged reload = %v, want false", got)
	}
}

// TestReloadServeKeepsCertificateWhenTheNewOneIsUnreadable: the TLS material is
// loaded into a local config before anything running is touched, so a
// half-written or corrupt certificate leaves the listeners serving the old one.
func TestReloadServeRejectsUnreadableCertificate(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, _, leafA := writeServerCert(t, dir, 101)
	env := newReloadEnv(t, tlsConfigBody(certFile, keyFile))
	svc := reloadTestService(t, env.c)
	holder := tlsHolderFor(t, env.running, svc)
	before := holder.Current()

	if err := os.WriteFile(certFile, []byte("-----BEGIN CERTIFICATE-----\nnot a certificate\n"), 0o600); err != nil {
		t.Fatalf("corrupt the certificate file: %v", err)
	}
	out := env.reload(t, holder, svc)
	if out.err == nil {
		t.Fatal("reload accepted an unreadable certificate")
	}
	entryFor(t, out.logs, configReloadFailedMsg)
	if n := out.logs.FilterMessage(configReloadedMsg).Len(); n != 0 {
		t.Errorf("a rejected reload also logged %q", configReloadedMsg)
	}
	if holder.Current() != before {
		t.Error("the listener TLS config was swapped despite the failure")
	}
	if leaf := leafCertificate(holder.Current()); leaf == nil || leaf.SerialNumber.Cmp(leafA.SerialNumber) != 0 {
		t.Errorf("holder serves %v, want the original serial %v", leaf, leafA.SerialNumber)
	}
	if got := env.level.Level(); got != zapcore.InfoLevel {
		t.Errorf("logger level = %v, want unchanged info", got)
	}
}

// TestReloadServeSwapsClientCA: rotating security.client_ca_file changes which
// client certificates the listeners will verify, and the reload says so —
// separately from the server keypair, which did not move.
func TestReloadServeSwapsClientCA(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, _, _ := writeServerCert(t, dir, 101)
	caOne := filepath.Join(dir, "clients-1.pem")
	caTwo := filepath.Join(dir, "clients-2.pem")
	writeClientCA(t, caOne, "operator-ca-1")
	writeClientCA(t, caTwo, "operator-ca-2")

	env := newReloadEnv(t, tlsConfigBody(certFile, keyFile)+"  mtls_enabled: true\n  client_ca_file: \""+caOne+"\"\n")
	svc := reloadTestService(t, env.c)
	holder := tlsHolderFor(t, env.running, svc)

	env.rewrite(t, tlsConfigBody(certFile, keyFile)+"  mtls_enabled: true\n  client_ca_file: \""+caTwo+"\"\n")
	out := env.reload(t, holder, svc)
	if out.err != nil {
		t.Fatalf("reload: %v", out.err)
	}
	e := entryFor(t, out.logs, configReloadedMsg)
	if got := e.ContextMap()["client_ca_changed"]; got != true {
		t.Errorf("client_ca_changed = %v, want true", got)
	}
	if got := e.ContextMap()["server_certificate_changed"]; got != false {
		t.Errorf("server_certificate_changed = %v, want false", got)
	}
	assertKeys(t, e, "changed", []string{"security.client_ca_file"})
	if env.running.Security.ClientCAFile != caTwo {
		t.Errorf("running client_ca_file = %q, want %q", env.running.Security.ClientCAFile, caTwo)
	}
}

// TestReloadServeIgnoresCertPathsWithoutTLS: with TLS off the listeners hold no
// certificate at all, so the three security.*_file settings are as unreloadable
// as the listen addresses — reported, never applied.
func TestReloadServeIgnoresCertPathsWithoutTLS(t *testing.T) {
	body := func(suffix string) string {
		return fmt.Sprintf("security:\n  server_cert_file: \"/tls/server%s.crt\"\n"+
			"  server_key_file: \"/tls/server%s.key\"\n  client_ca_file: \"/tls/clients%s.pem\"\n", suffix, suffix, suffix)
	}
	env := newReloadEnv(t, body("-a"))
	env.rewrite(t, body("-b"))

	out := env.reload(t, nil, nil)
	if out.err != nil {
		t.Fatalf("reload: %v", out.err)
	}
	e := entryFor(t, out.logs, configReloadedMsg)
	assertKeys(t, e, "ignored", []string{"security.server_cert_file", "security.server_key_file", "security.client_ca_file"})
	assertKeys(t, e, "changed", nil)
	if env.running.Security.ServerCertFile != "/tls/server-a.crt" {
		t.Errorf("running server_cert_file = %q, want the value the process started with", env.running.Security.ServerCertFile)
	}
}

// TestReloadServeWithoutReporter: the Prometheus exporter is optional, and the
// serve loop passes nil for it today. Neither outcome may panic.
func TestReloadServeWithoutReporter(t *testing.T) {
	env := newReloadEnv(t, "log:\n  level: info\n")
	logger, _ := observedLogger()

	env.rewrite(t, "log:\n  level: debug\n")
	if _, err := env.c.reloadServe(context.Background(), env.r, logger, env.level, nil, nil, env.running, nil); err != nil {
		t.Fatalf("reload with a nil reporter: %v", err)
	}
	env.rewrite(t, "log:\n  level: debug\nstorage:\n  sqlite_pth: \"/tmp/x.db\"\n")
	if _, err := env.c.reloadServe(context.Background(), env.r, logger, env.level, nil, nil, env.running, nil); err == nil {
		t.Fatal("a rejected reload with a nil reporter returned no error")
	}
}

// TestReloadServeFlagBeatsTheFile: a reload re-resolves with the startup
// precedence, so a value pinned on the command line cannot be changed by
// editing the file. The resolved value never differs from the running one, so
// the key is not even reported as ignored.
func TestReloadServeFlagBeatsTheFile(t *testing.T) {
	env := newReloadEnv(t, "log:\n  level: info\n", "--log-level", "info")
	env.rewrite(t, "log:\n  level: debug\n")

	out := env.reload(t, nil, nil)
	if out.err != nil {
		t.Fatalf("reload: %v", out.err)
	}
	if got := env.level.Level(); got != zapcore.InfoLevel {
		t.Errorf("logger level = %v, want the flag's info", got)
	}
	if env.running.Log.Level != "info" {
		t.Errorf("running log.level = %q, want info", env.running.Log.Level)
	}
	e := entryFor(t, out.logs, configReloadedMsg)
	assertKeys(t, e, "changed", nil)
	assertKeys(t, e, "ignored", nil)
}

// tlsConfigBody is a config file with TLS on and the given keypair, the shape
// a reload of TLS material starts from.
func tlsConfigBody(certFile, keyFile string) string {
	return fmt.Sprintf("security:\n  tls_enabled: true\n  server_cert_file: %q\n  server_key_file: %q\n", certFile, keyFile)
}

// TestDiffSettings pins the partition the reload log is built from: only
// settings whose value actually differs appear, each on exactly one side, in
// the registry's own order so two reloads reporting the same change read alike.
func TestDiffSettings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*config.Config)
		applied []string
		changed []string
		ignored []string
	}{
		{
			name:    "no differences",
			mutate:  func(*config.Config) {},
			applied: reloadableKeys,
		},
		{
			name:    "an applied key",
			mutate:  func(c *config.Config) { c.Log.Level = "debug" },
			applied: reloadableKeys,
			changed: []string{"log.level"},
		},
		{
			name:    "a key outside the applied set",
			mutate:  func(c *config.Config) { c.Server.HTTPAddr = "127.0.0.1:1" },
			applied: reloadableKeys,
			ignored: []string{"server.http_addr"},
		},
		{
			name:    "an applied key that TLS-off does not apply",
			mutate:  func(c *config.Config) { c.Security.ServerCertFile = "/tls/new.crt" },
			applied: reloadableKeys[:1],
			ignored: []string{"security.server_cert_file"},
		},
		{
			name: "both sides, in registry order",
			mutate: func(c *config.Config) {
				c.Log.Level = "warn"
				c.Server.HTTPAddr = "127.0.0.1:1"
				c.Security.ServerCertFile = "/tls/new.crt"
				c.Audit.Enabled = false
			},
			applied: reloadableKeys,
			changed: []string{"security.server_cert_file", "log.level"},
			ignored: []string{"server.http_addr", "audit.enabled"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			running := config.Default()
			next := config.Default()
			tc.mutate(&next)

			changed, ignored := diffSettings(&running, &next, tc.applied)
			if !slices.Equal(changed, tc.changed) {
				t.Errorf("changed = %v, want %v", changed, tc.changed)
			}
			if !slices.Equal(ignored, tc.ignored) {
				t.Errorf("ignored = %v, want %v", ignored, tc.ignored)
			}
		})
	}
}
