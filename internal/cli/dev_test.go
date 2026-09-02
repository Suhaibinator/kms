package cli

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/server/grpcserver"
	"github.com/Suhaibinator/kms/internal/watch"
)

// These tests run the real `dev` command in-process: it bootstraps a store the
// way init does, seeds it through the core APIs, and starts the real serve
// wiring on a loopback TLS listener. They then talk to that server exactly as
// the banner tells a reader to — the same subcommands, the same --endpoint,
// --ca, and --token — so what the banner promises is what is tested.
//
// Unlike the serve wiring tests, these need the gRPC listener: every example
// the banner prints is a gRPC call. GRPCFactory is the package-level seam
// cmd/parameter-store fills, so each test installs it and restores it
// afterwards. None of these tests call t.Parallel, and neither do the serve
// tests, so the assignment never races another test that reads it.

// testGRPCAdapter narrows *grpcserver.Server to GRPCServer, the way
// cmd/parameter-store's adapter does.
type testGRPCAdapter struct {
	srv *grpcserver.Server
	lis net.Listener
}

func (a *testGRPCAdapter) Serve() error  { return a.srv.Serve(a.lis) }
func (a *testGRPCAdapter) GracefulStop() { a.srv.GracefulStop() }
func (a *testGRPCAdapter) Stop()         { a.srv.Stop() }

// wireGRPC installs the production gRPC factory for the duration of one test,
// so `dev` comes up with both listeners rather than HTTP alone.
func wireGRPC(t *testing.T) {
	t.Helper()
	previous := GRPCFactory
	GRPCFactory = func(svc *core.Service, hub *watch.Hub, cfg GRPCConfig) (GRPCServer, error) {
		srv, err := grpcserver.New(svc, hub, grpcserver.Config{Addr: cfg.Addr, TLS: cfg.TLS, Metrics: cfg.Metrics})
		if err != nil {
			return nil, err
		}
		lis, err := srv.Listen()
		if err != nil {
			return nil, err
		}
		return &testGRPCAdapter{srv: srv, lis: lis}, nil
	}
	t.Cleanup(func() { GRPCFactory = previous })
}

// devRun is one in-process `dev` and everything a test needs to drive it.
type devRun struct {
	stdout   *safeBuffer
	stderr   *safeBuffer
	httpAddr string
	grpcAddr string
	dir      string

	stop     chan struct{}
	done     chan int
	stopOnce sync.Once
	waited   bool
	exitCode int
}

// startDev runs `dev` on reserved loopback ports with a persisted --dir under
// the test's temporary directory, and returns once the banner (or, in JSON
// mode, the result document) has been written — which `dev` only does after
// the server answers its own health probe.
func startDev(t *testing.T, globalArgs []string, devArgs ...string) *devRun {
	t.Helper()
	wireGRPC(t)
	run := &devRun{
		stdout:   &safeBuffer{},
		stderr:   &safeBuffer{},
		httpAddr: freeLoopbackAddr(t),
		grpcAddr: freeLoopbackAddr(t),
		stop:     make(chan struct{}),
		done:     make(chan int, 1),
	}
	args := append([]string{}, globalArgs...)
	args = append(args, "dev", "--http-addr", run.httpAddr, "--grpc-addr", run.grpcAddr)
	args = append(args, devArgs...)
	for i, a := range devArgs {
		if a == "--dir" && i+1 < len(devArgs) {
			run.dir = devArgs[i+1]
		}
	}

	c := newDevCLI(run.stdout, run.stderr)
	c.stopServe = run.stop
	go func() { run.done <- c.Run(args) }()
	t.Cleanup(func() { run.stopAndWait(t) })
	run.awaitReady(t)
	return run
}

// newDevCLI is newTestCLI with concurrency-safe streams: `dev` writes its
// banner from the goroutine running the server while the test reads it.
func newDevCLI(stdout, stderr *safeBuffer) *CLI {
	c := &CLI{Stdout: stdout, Stderr: stderr}
	c.lookupEnv = func(string) (string, bool) { return "", false }
	return c
}

// awaitReady blocks until dev has announced itself on one stream or the other.
func (r *devRun) awaitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case code := <-r.done:
			r.waited, r.exitCode = true, code
			t.Fatalf("dev exited during startup with code %d; stderr:\n%s", code, r.stderr.String())
		default:
		}
		if strings.Contains(r.stderr.String(), "Press Ctrl-C to stop") || r.stdout.String() != "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("dev never announced itself within 60s; stderr:\n%s", r.stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// stopAndWait ends the run as a SIGTERM would and returns dev's exit code.
func (r *devRun) stopAndWait(t *testing.T) int {
	t.Helper()
	r.stopOnce.Do(func() { close(r.stop) })
	if !r.waited {
		select {
		case code := <-r.done:
			r.waited, r.exitCode = true, code
		case <-time.After(30 * time.Second):
			r.waited = true
			t.Errorf("dev did not shut down within 30s; stderr:\n%s", r.stderr.String())
		}
	}
	return r.exitCode
}

// caFile is the CA certificate the banner names, and the one every client
// command below verifies the server with.
func (r *devRun) caFile(t *testing.T) string {
	t.Helper()
	if r.dir == "" {
		t.Fatal("this run has no persisted --dir")
	}
	return filepath.Join(r.dir, devCACertFile)
}

// bannerToken reads one credential out of the printed banner: the label line,
// then the indented token on the line below it.
func (r *devRun) bannerToken(t *testing.T, label string) string {
	t.Helper()
	lines := strings.Split(r.stderr.String(), "\n")
	for i, line := range lines {
		if strings.Contains(line, label) && i+1 < len(lines) {
			token := strings.TrimSpace(lines[i+1])
			if token == "" {
				t.Fatalf("banner label %q is followed by a blank line", label)
			}
			return token
		}
	}
	t.Fatalf("banner has no %q line; stderr:\n%s", label, r.stderr.String())
	return ""
}

// client runs a client subcommand against the running dev server with the
// credentials and CA the banner handed out, and returns the exit code, stdout,
// and stderr.
func (r *devRun) client(t *testing.T, token string, args ...string) (int, string, string) {
	t.Helper()
	c := newTestCLI()
	full := append([]string{}, args...)
	full = append(full, "--endpoint", r.grpcAddr, "--ca", r.caFile(t), "--token", token)
	code := c.Run(full)
	return code, c.stdout(), c.stderr()
}

// httpsGet fetches an endpoint on the dev HTTP listener, verifying the server
// against the generated CA — the same trust anchor the banner tells a reader
// to pass as --ca.
func (r *devRun) httpsGet(t *testing.T, path string) (int, string) {
	t.Helper()
	pem, err := os.ReadFile(r.caFile(t))
	if err != nil {
		t.Fatalf("read the dev CA certificate: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("the dev CA file holds no certificate")
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get("https://" + r.httpAddr + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := make([]byte, 256)
	n, _ := resp.Body.Read(body)
	return resp.StatusCode, string(body[:n])
}

// TestDevServesTheSeededDemo is the end-to-end statement of the feature: one
// command produces a TLS server whose certificate the generated CA verifies,
// an admin token that administers it, and an unprivileged token that can read
// the development namespace and is refused in production.
func TestDevServesTheSeededDemo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	run := startDev(t, nil, "--dir", dir)

	if code, body := run.httpsGet(t, "/healthz"); code != http.StatusOK {
		t.Fatalf("GET /healthz over the generated CA = %d %q, want 200", code, body)
	}

	// Everything the store needs is in one directory, so a demo is deleted by
	// deleting a folder.
	for _, name := range []string{devMarkerFile, devDBFile, devKEKFile, devCACertFile, devServerCertFile, devServerKeyFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("dev store is missing %s: %v", name, err)
		}
	}

	adminToken := run.bannerToken(t, "dev-only admin token")
	code, out, errOut := run.client(t, adminToken, "admin", "namespace", "list")
	if code != exitOK {
		t.Fatalf("admin namespace list with the banner's token = %d; stderr=%s", code, errOut)
	}
	for _, ns := range []string{"dev/demo", "prod/demo"} {
		if !strings.Contains(out, ns) {
			t.Errorf("namespace list does not show %s:\n%s", ns, out)
		}
	}

	// The unprivileged identity: allowed where its policy says so, refused
	// everywhere else. This is the first thing anyone evaluating the
	// authorization model tries, so the seed makes it a one-liner.
	appToken := run.bannerToken(t, "dev-only application token")
	code, out, errOut = run.client(t, appToken, "list", "dev/demo")
	if code != exitOK {
		t.Fatalf("demo-app list dev/demo = %d; stderr=%s", code, errOut)
	}
	for _, want := range []string{"/dev/demo/app/greeting", "/dev/demo/db/password"} {
		if !strings.Contains(out, want) {
			t.Errorf("demo-app cannot see %s:\n%s", want, out)
		}
	}
	if code, _, errOut = run.client(t, appToken, "list", "prod/demo"); code != exitPermissionDenied {
		t.Fatalf("demo-app list prod/demo = %d, want %d (permission denied); stderr=%s", code, exitPermissionDenied, errOut)
	}

	// The banner is stderr-only: stdout stays free for a pipe.
	if got := run.stdout.String(); got != "" {
		t.Errorf("dev wrote %q to stdout; the banner belongs on stderr", got)
	}
	banner := run.stderr.String()
	for _, want := range []string{
		"disposable demo server",
		"parameter-store admin namespace list --endpoint ",
		"parameter-store exec dev/demo --endpoint ",
		"-- env",
	} {
		if !strings.Contains(banner, want) {
			t.Errorf("banner is missing %q:\n%s", want, banner)
		}
	}
	if exit := run.stopAndWait(t); exit != 0 {
		t.Fatalf("dev exit = %d, want 0; stderr:\n%s", exit, run.stderr.String())
	}
}

// TestDevNoSeedStartsEmpty: --no-seed still gives a working server and an
// admin, but nothing has been created in it.
func TestDevNoSeedStartsEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	run := startDev(t, nil, "--dir", dir, "--no-seed")

	adminToken := run.bannerToken(t, "dev-only admin token")
	code, out, errOut := run.client(t, adminToken, "-o", "json", "admin", "namespace", "list")
	if code != exitOK {
		t.Fatalf("admin namespace list = %d; stderr=%s", code, errOut)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatalf("decode namespace list %q: %v", out, err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("--no-seed left %d namespaces behind: %v", len(page.Items), page.Items)
	}
	if strings.Contains(run.stderr.String(), "dev-only application token") {
		t.Error("--no-seed still printed an application token")
	}
}

// TestDevRefusesADirectoryThatIsNotADevStore is the guard that makes `dev`
// safe to point anywhere: a directory with contents and no marker is refused
// before a single byte is written, so a mistyped --dir costs an error message
// and nothing else.
func TestDevRefusesADirectoryThatIsNotADevStore(t *testing.T) {
	dir := t.TempDir()
	realDB := filepath.Join(dir, devDBFile)
	if err := os.WriteFile(realDB, []byte("pretend this is production"), 0o600); err != nil {
		t.Fatalf("write the decoy database: %v", err)
	}
	before := dirDigest(t, dir)

	c := newTestCLI()
	if code := c.Run([]string{"dev", "--dir", dir}); code != exitUsage {
		t.Fatalf("dev on a non-dev directory = %d, want %d; stderr=%s", code, exitUsage, c.stderr())
	}
	if !strings.Contains(c.stderr(), "not a dev store") {
		t.Errorf("refusal does not say why: %s", c.stderr())
	}
	if after := dirDigest(t, dir); after != before {
		t.Fatalf("dev modified the directory it refused: %s -> %s", before, after)
	}

	// --reset is not a way around the guard: the marker is what authorizes
	// erasing a directory, and this one has none.
	reset := newTestCLI()
	if code := reset.Run([]string{"dev", "--dir", dir, "--reset"}); code != exitUsage {
		t.Fatalf("dev --reset on a non-dev directory = %d, want %d; stderr=%s", code, exitUsage, reset.stderr())
	}
	if after := dirDigest(t, dir); after != before {
		t.Fatalf("dev --reset modified the directory it refused: %s -> %s", before, after)
	}
}

// dirDigest hashes every entry name and its contents, so a test can assert
// that a refused command left a directory untouched.
func dirDigest(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	sum := sha256.New()
	for _, name := range names {
		sum.Write([]byte(name))
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		sum.Write(data)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// TestDevResetReseeds: --reset erases a marked store and builds it again, so a
// demo that has been poked at is one command away from being pristine.
func TestDevResetReseeds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	first := startDev(t, nil, "--dir", dir)
	adminToken := first.bannerToken(t, "dev-only admin token")
	if code, _, errOut := first.client(t, adminToken, "put-parameter", "/dev/demo/app/scribble", "left behind"); code != exitOK {
		t.Fatalf("put-parameter = %d; stderr=%s", code, errOut)
	}
	if code, out, _ := first.client(t, adminToken, "list", "dev/demo"); !strings.Contains(out, "scribble") {
		t.Fatalf("the parameter under test was not written (exit %d):\n%s", code, out)
	}
	if exit := first.stopAndWait(t); exit != 0 {
		t.Fatalf("first dev exit = %d, want 0; stderr:\n%s", exit, first.stderr.String())
	}

	second := startDev(t, nil, "--dir", dir, "--reset")
	adminToken = second.bannerToken(t, "dev-only admin token")
	code, out, errOut := second.client(t, adminToken, "list", "dev/demo")
	if code != exitOK {
		t.Fatalf("list after --reset = %d; stderr=%s", code, errOut)
	}
	if strings.Contains(out, "scribble") {
		t.Errorf("--reset kept a parameter from the previous store:\n%s", out)
	}
	if !strings.Contains(out, "/dev/demo/app/greeting") {
		t.Errorf("--reset did not re-seed the store:\n%s", out)
	}
}

// TestDevRefusesANonLoopbackBind: dev prints credentials, so making them
// reachable from the network has to be asked for. checkDevBind is exercised
// directly as well as through the command, because the warning path must not
// require actually opening a public listener to test.
func TestDevRefusesANonLoopbackBind(t *testing.T) {
	c := newTestCLI()
	if code := c.Run([]string{"dev", "--http-addr", "0.0.0.0:8443"}); code != exitUsage {
		t.Fatalf("dev on a wildcard bind = %d, want %d; stderr=%s", code, exitUsage, c.stderr())
	}
	if !strings.Contains(c.stderr(), "--allow-remote") {
		t.Errorf("refusal does not name the flag that permits it: %s", c.stderr())
	}

	refused := newTestCLI()
	if code, ok := refused.checkDevBind(devHTTPAddr, "203.0.113.5:8444", false); ok || code != exitUsage {
		t.Fatalf("checkDevBind on a routable gRPC address = (%d, %t), want (%d, false)", code, ok, exitUsage)
	}

	allowed := newTestCLI()
	code, ok := allowed.checkDevBind("0.0.0.0:8443", devGRPCAddr, true)
	if !ok || code != exitOK {
		t.Fatalf("checkDevBind with --allow-remote = (%d, %t), want (%d, true)", code, ok, exitOK)
	}
	if !strings.Contains(allowed.stderr(), "WARNING: dev is listening off-loopback") {
		t.Errorf("--allow-remote did not warn: %s", allowed.stderr())
	}
	// Loopback stays silent; a warning on every ordinary run teaches people to
	// ignore warnings.
	quiet := newTestCLI()
	if code, ok := quiet.checkDevBind(devHTTPAddr, devGRPCAddr, true); !ok || code != exitOK || quiet.stderr() != "" {
		t.Fatalf("checkDevBind on loopback = (%d, %t) stderr=%q, want (%d, true) and silence", code, ok, quiet.stderr(), exitOK)
	}
}

// TestDevJSONOutputCarriesTheSameFacts: --output json replaces the banner with
// one document on stdout, carrying exactly the contract's fields and nothing
// else — no banner text leaks onto either stream.
func TestDevJSONOutputCarriesTheSameFacts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	run := startDev(t, []string{"--output", "json"}, "--dir", dir)

	var document map[string]any
	if err := json.Unmarshal([]byte(run.stdout.String()), &document); err != nil {
		t.Fatalf("decode the dev document %q: %v", run.stdout.String(), err)
	}
	got := make([]string, 0, len(document))
	for key := range document {
		got = append(got, key)
	}
	sort.Strings(got)
	want := []string{"admin", "ca_file", "console_url", "demo_app", "ephemeral", "examples",
		"grpc_addr", "http_addr", "namespaces", "seeded", "store_dir"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("dev document fields = %v, want %v", got, want)
	}
	if document["seeded"] != true || document["ephemeral"] != false {
		t.Errorf("seeded/ephemeral = %v/%v, want true/false", document["seeded"], document["ephemeral"])
	}
	if document["ca_file"] != run.caFile(t) {
		t.Errorf("ca_file = %v, want %s", document["ca_file"], run.caFile(t))
	}
	if strings.Contains(run.stderr.String(), "disposable demo server") {
		t.Error("JSON mode still printed the banner")
	}

	// The token in the document is the real one, not a placeholder.
	admin, ok := document["admin"].(map[string]any)
	if !ok {
		t.Fatalf("admin = %v, want an object", document["admin"])
	}
	token, _ := admin["token"].(string)
	if code, _, errOut := run.client(t, token, "admin", "namespace", "list"); code != exitOK {
		t.Fatalf("the document's admin token does not work: exit %d; stderr=%s", code, errOut)
	}
}

// TestDevRemovesItsTemporaryStore: with no --dir the store is temporary, and
// "temporary" has to mean the directory is gone afterwards — otherwise a few
// demos leave a trail of databases and master keys in the temp directory.
func TestDevRemovesItsTemporaryStore(t *testing.T) {
	run := startDev(t, []string{"--output", "json"})
	var document struct {
		StoreDir  string `json:"store_dir"`
		Ephemeral bool   `json:"ephemeral"`
	}
	if err := json.Unmarshal([]byte(run.stdout.String()), &document); err != nil {
		t.Fatalf("decode the dev document %q: %v", run.stdout.String(), err)
	}
	if !document.Ephemeral {
		t.Fatal("a run without --dir reported a persistent store")
	}
	if _, err := os.Stat(filepath.Join(document.StoreDir, devDBFile)); err != nil {
		t.Fatalf("the temporary store has no database: %v", err)
	}
	if exit := run.stopAndWait(t); exit != 0 {
		t.Fatalf("dev exit = %d, want 0; stderr:\n%s", exit, run.stderr.String())
	}
	if _, err := os.Stat(document.StoreDir); !os.IsNotExist(err) {
		t.Fatalf("the temporary store %s survived shutdown (stat err = %v)", document.StoreDir, err)
	}
}
