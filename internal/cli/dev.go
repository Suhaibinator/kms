package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Suhaibinator/kms/internal/config"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/fileutil"
)

// The layout of a dev store. Everything `dev` needs lives in one directory, so
// a demo is deleted by deleting a folder.
const (
	// devMarkerFile is written at creation and is the only thing that makes a
	// directory a dev store. dev refuses to open a directory that has content
	// but no marker, so it can never be aimed at a production store — not by a
	// typo, not by a stale KMS_SQLITE_PATH, and not by --reset.
	devMarkerFile     = ".parameter-store-dev"
	devDBFile         = "kms.db"
	devKEKFile        = "master.key"
	devCACertFile     = "ca.crt"
	devCAKeyFile      = "ca.key"
	devServerCertFile = "server.crt"
	devServerKeyFile  = "server.key"
)

// Dev's own listen defaults. They are deliberately not the product defaults
// (which bind every interface): a demo server holding a printed admin token
// must not be reachable from the network by accident.
const (
	devHTTPAddr = "127.0.0.1:8443"
	devGRPCAddr = "127.0.0.1:8444"
)

// devMarkerContents is written into the marker file. It is read by no code —
// the guard only tests for the file's presence — but somebody who finds the
// directory later deserves to be told what it is.
const devMarkerContents = `This directory is a parameter-store dev store, created by "parameter-store dev".
It holds a disposable database, master key, and TLS material for demos only.
"parameter-store dev --reset" erases it. Do not put real data here.
`

// devPath joins a dev store file onto its directory.
func devPath(dir, name string) string { return filepath.Join(dir, name) }

// cmdDev runs a complete, seeded, throwaway parameter-store: an initialized
// database, a master key, the built-in CA, TLS material, an admin identity,
// demo data, and the real serve wiring on top of it. It exists so the first
// contact with the product is one command rather than a page of setup.
//
// Nothing here is a second implementation of init or serve: the database,
// master key, and CA come from bootstrapStore (which init also uses), the
// demo data is written through the same core APIs the admin CLI drives, and
// the server is cmdServe with a flag set dev composes.
func (c *CLI) cmdDev(args []string) int {
	flags := c.newFlags("dev")
	r := c.serverSettings(flags, "server.http_addr", "server.grpc_addr", "log.level")
	dir := flags.String("dir", "", "keep the dev store in this `directory` instead of a temporary one removed on exit")
	noSeed := flags.Bool("no-seed", false, "start with an empty store instead of the demo namespaces, parameters, secrets, and release")
	reset := flags.Bool("reset", false, "erase the contents of --dir before starting (refused unless it is a dev store)")
	allowRemote := flags.Bool("allow-remote", false, "permit a non-loopback listen address; the printed dev tokens then travel off this host")
	c.setUsage(flags, "dev [flags]",
		"Run a disposable, seeded demo server: a throwaway database, master key, built-in CA, TLS material, "+
			"an admin identity, and demo data, with the console and both listeners on loopback. "+
			"The tokens it prints belong to that store alone. Never point it at real data.", true)
	if !c.parseFlags(flags, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}
	cfg, prov, configFile, err := r.resolve()
	if err != nil {
		return c.failErr("", err)
	}

	// The listen addresses still resolve flag > environment > config file, but
	// the last tier is dev's loopback default rather than the product's
	// bind-everything one.
	httpAddr := devDefaultAddr(cfg.Server.HTTPAddr, prov, "server.http_addr", devHTTPAddr)
	grpcAddr := devDefaultAddr(cfg.Server.GRPCAddr, prov, "server.grpc_addr", devGRPCAddr)
	if code, ok := c.checkDevBind(httpAddr, grpcAddr, *allowRemote); !ok {
		return code
	}

	store, ephemeral, code := c.prepareDevDir(*dir, *reset)
	if store == "" {
		return code
	}
	if ephemeral {
		// A temporary store exists only for this run. Remove it on every exit
		// path, including the signal that stops the server.
		defer func() {
			if err := removeDevStore(store); err != nil {
				c.info("could not remove the temporary dev store %s: %v", store, err)
			}
		}()
	}

	facts, code := c.bootstrapDev(store, httpAddr, grpcAddr, *noSeed)
	if facts == nil {
		return code
	}
	facts.StoreDir = store
	facts.Ephemeral = ephemeral

	// serve is started in the background so the banner can wait for a real
	// health response rather than announcing a server that is still opening
	// its listeners. Every setting dev owns is passed as a flag, which beats
	// the environment and the config file, so a stray KMS_* export cannot
	// redirect the demo at another database or drop its TLS.
	serveArgs := []string{
		"--sqlite-path", devPath(store, devDBFile),
		"--kek-file", devPath(store, devKEKFile),
		"--http-addr", httpAddr,
		"--grpc-addr", grpcAddr,
		"--log-level", cfg.Log.Level,
		"--tls-enabled",
		"--server-cert-file", facts.serverCertFile,
		"--server-key-file", facts.serverKeyFile,
		"--client-ca-file=",
		"--mtls-enabled=false",
		// The banner hands out a bearer token and tells the reader to use it.
		// Requiring an admin client certificate as well would make every
		// example in it fail; the store is disposable and loopback-bound.
		"--admin-require-client-cert=false",
		"--frontend-enabled",
	}
	if configFile != "" {
		serveArgs = append(serveArgs, "--config", configFile)
	}

	done := make(chan int, 1)
	go func() { done <- c.cmdServe(serveArgs) }()

	if err := c.awaitDevReady(facts, done); err != nil {
		// serve has either exited (its own error is already on stderr) or never
		// answered; either way there is nothing to announce. A server that is
		// somehow still starting dies with the process on return.
		select {
		case code := <-done:
			return code
		default:
		}
		return c.failErr("waiting for the dev server", err)
	}
	// Wait for shutdown even when the announcement failed: a broken stdout is
	// no reason to delete a running server's store out from under it. The
	// announcement's exit code still wins, because a demo whose credentials
	// were never printed did not succeed.
	announced := c.announceDev(facts)
	served := <-done
	if announced != exitOK {
		return announced
	}
	return served
}

// devDefaultAddr applies dev's loopback default only where the setting is
// still on its built-in one, so a flag, a KMS_* variable, or a config file
// entry keeps precedence exactly as it does for serve.
func devDefaultAddr(resolved string, prov config.Provenance, key, devDefault string) string {
	if prov[key].Kind == config.SourceDefault {
		return devDefault
	}
	return resolved
}

// checkDevBind refuses a listen address reachable from off-host unless the
// operator asked for it. dev prints credentials to the terminal; on a
// non-loopback bind those credentials become network-reachable, so the default
// is a usage error rather than a warning nobody reads.
func (c *CLI) checkDevBind(httpAddr, grpcAddr string, allowRemote bool) (int, bool) {
	exposed := make([]string, 0, 2)
	for _, bind := range []struct{ flag, addr string }{{"--http-addr", httpAddr}, {"--grpc-addr", grpcAddr}} {
		if !isNonLoopbackBind(bind.addr) {
			continue
		}
		if !allowRemote {
			return c.failUsage("%s %s is not a loopback address; dev serves a disposable store and prints its tokens, so pass --allow-remote to expose it deliberately",
				bind.flag, bind.addr), false
		}
		exposed = append(exposed, bind.flag+" "+bind.addr)
	}
	if len(exposed) > 0 {
		// Never routed through info: --quiet must not hide the fact that a
		// demo server's printed tokens just became reachable from the network.
		_, _ = fmt.Fprintf(c.Stderr, "WARNING: dev is listening off-loopback (%s). The admin token below grants full administrative access to anyone who can reach it.\n",
			strings.Join(exposed, ", "))
	}
	return exitOK, true
}

// prepareDevDir resolves the dev store directory and guarantees it is one:
// creating and marking it when it is new or empty, wiping it under --reset,
// and refusing it when it holds anything without the marker. It returns the
// directory, whether it is temporary (and so removed on exit), and an exit
// code; on refusal the directory is empty and that code is the command's.
func (c *CLI) prepareDevDir(dir string, reset bool) (string, bool, int) {
	if dir == "" {
		if reset {
			return "", false, c.failUsage("--reset needs --dir; a temporary dev store is new on every run")
		}
		path, err := fileutil.MkdirPrivateTemp(os.TempDir(), "parameter-store-dev-")
		if err != nil {
			return "", false, c.failErr("creating a temporary dev store", err)
		}
		if err := c.markDevDir(path); err != nil {
			_ = os.RemoveAll(path)
			return "", false, c.failErr("", err)
		}
		return path, true, exitOK
	}

	path := absPath(dir)
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// MkdirAll creates every missing component 0700; an existing parent
		// keeps whatever mode it already has.
		if err := os.MkdirAll(path, 0o700); err != nil {
			return "", false, c.failErr("creating the dev store directory", err)
		}
	case err != nil:
		return "", false, c.failErr("opening the dev store directory", err)
	case !info.IsDir():
		return "", false, c.failUsage("--dir %s is not a directory", path)
	}

	marked, err := devDirIsMarked(path)
	if err != nil {
		return "", false, c.failErr("inspecting the dev store directory", err)
	}
	if !marked {
		empty, err := dirIsEmpty(path)
		if err != nil {
			return "", false, c.failErr("inspecting the dev store directory", err)
		}
		if !empty {
			// Nothing has been written yet, and nothing will be: the refusal
			// happens before the first file is created, so a directory named
			// by mistake is left byte for byte as it was.
			return "", false, c.failUsage("%s is not a dev store — refusing to touch it (it has contents but no %s marker); use an empty or new directory",
				path, devMarkerFile)
		}
	} else if reset {
		if err := wipeDirContents(path); err != nil {
			return "", false, c.failErr("resetting the dev store", err)
		}
		marked = false
	}
	if !marked {
		if err := c.markDevDir(path); err != nil {
			return "", false, c.failErr("", err)
		}
	}
	return path, false, exitOK
}

// markDevDir writes the marker that makes a directory a dev store.
func (c *CLI) markDevDir(dir string) error {
	path := devPath(dir, devMarkerFile)
	if err := writePrivateFile(path, false, func(w io.Writer) error {
		_, err := w.Write([]byte(devMarkerContents))
		return err
	}); err != nil {
		return fmt.Errorf("writing the dev store marker %s: %w", path, err)
	}
	return nil
}

func devDirIsMarked(dir string) (bool, error) {
	_, err := os.Lstat(devPath(dir, devMarkerFile))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

func dirIsEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

// removeDevStore deletes a temporary store. A demo that leaves its master key
// behind in the temp directory is not temporary in the way the banner
// promised, so the delete is worth retrying.
func removeDevStore(dir string) error { return removeWithRetry(dir) }

// wipeDirContents empties a marked dev store without removing the directory
// itself, which the operator may have created (and may have opened a shell in).
func wipeDirContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeWithRetry(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// removeWithRetry deletes a path, retrying briefly: on Windows a database file
// can stay locked for a moment after the last handle is closed, and both
// callers run immediately after a server released one.
func removeWithRetry(path string) error {
	var err error
	for attempt := range 5 {
		if err = os.RemoveAll(path); err == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
	}
	return err
}

// devFacts is everything the banner and the JSON document report. The two
// unexported fields are the TLS material paths serve needs, which are an
// implementation detail of how dev stands the server up rather than a fact
// about the running demo.
type devFacts struct {
	ConsoleURL string       `json:"console_url"`
	HTTPAddr   string       `json:"http_addr"`
	GRPCAddr   string       `json:"grpc_addr"`
	StoreDir   string       `json:"store_dir"`
	Ephemeral  bool         `json:"ephemeral"`
	CAFile     string       `json:"ca_file"`
	Admin      devIdentity  `json:"admin"`
	DemoApp    *devIdentity `json:"demo_app"`
	Seeded     bool         `json:"seeded"`
	Namespaces []string     `json:"namespaces"`
	Examples   []string     `json:"examples"`

	serverCertFile string
	serverKeyFile  string
	probeURL       string
	caPool         *x509.CertPool
}

// devIdentity is one credential the banner hands out. Both tokens belong to
// the disposable store and to nothing else.
type devIdentity struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}

// Names the seeded store uses. They appear in the banner's copy-paste
// examples, so they are constants rather than literals scattered about.
const (
	devAdminName   = "dev-admin"
	devAppName     = "demo-app"
	devDemoApp     = "demo"
	devDemoEnv     = "dev"
	devDemoProdEnv = "prod"
)

// bootstrapDev creates the store's contents: database, master key, built-in
// CA, TLS material, the admin identity, and (unless --no-seed) the demo data.
// It runs entirely before serve opens the database, so the demo is complete
// the moment the console answers.
func (c *CLI) bootstrapDev(dir, httpAddr, grpcAddr string, noSeed bool) (*devFacts, int) {
	ctx := context.Background()
	facts := &devFacts{HTTPAddr: httpAddr, GRPCAddr: grpcAddr}

	tlsMaterial, err := c.devTLS(dir, httpAddr, grpcAddr)
	if err != nil {
		return nil, c.failErr("preparing dev TLS material", err)
	}
	facts.CAFile = tlsMaterial.CACertPath
	facts.serverCertFile = tlsMaterial.ServerCertPath
	facts.serverKeyFile = tlsMaterial.ServerKeyPath
	pool, err := devCAPool(tlsMaterial.CACertPath)
	if err != nil {
		return nil, c.failErr("reading the dev CA certificate", err)
	}
	facts.caPool = pool

	bootstrapped, err := c.bootstrapStore(ctx, devPath(dir, devDBFile), devPath(dir, devKEKFile))
	if err != nil {
		return nil, c.failErr("", err)
	}
	defer bootstrapped.close()
	svc := bootstrapped.svc

	// The same core API "admin identity create" calls, so a dev admin is an
	// ordinary admin identity with nothing special about it but its lifespan.
	adminToken, err := devMintToken(ctx, svc, core.CreateIdentityInput{
		Name:        devAdminName,
		Kind:        domain.IdentityKindAdmin,
		AuthMethods: []domain.AuthMethod{domain.AuthMethodToken},
	})
	if err != nil {
		return nil, c.failErr("creating the dev admin identity", err)
	}
	facts.Admin = devIdentity{Name: devAdminName, Token: adminToken}

	if !noSeed {
		seeded, err := c.seedDevStore(ctx, svc)
		if err != nil {
			return nil, c.failErr("seeding the dev store", err)
		}
		facts.Seeded = true
		facts.Namespaces = seeded.namespaces
		facts.DemoApp = &devIdentity{Name: devAppName, Token: seeded.appToken}
	}

	facts.ConsoleURL = devConsoleURL(httpAddr)
	facts.probeURL = devProbeURL(httpAddr)
	facts.Examples = devExamples(facts)
	return facts, exitOK
}

// devTLS reuses the store's existing keypair when one is already there (a
// persisted --dir restarted), and generates a fresh one otherwise. Reusing it
// matters: a CA file the operator has already trusted in a browser should not
// change under them on every restart.
func (c *CLI) devTLS(dir, httpAddr, grpcAddr string) (devTLSMaterial, error) {
	existing := devTLSMaterial{
		CACertPath:     devPath(dir, devCACertFile),
		ServerCertPath: devPath(dir, devServerCertFile),
		ServerKeyPath:  devPath(dir, devServerKeyFile),
	}
	if fileExists(existing.CACertPath) && fileExists(existing.ServerCertPath) && fileExists(existing.ServerKeyPath) {
		return existing, nil
	}
	return generateDevTLS(dir, []string{hostOf(httpAddr), hostOf(grpcAddr)})
}

// devMintToken creates an identity, or — when the store already has one from a
// previous run against the same --dir — rotates its token. Either way the
// caller leaves with a usable credential, because a token that was printed to
// a terminal an hour ago is not one dev can print again.
func devMintToken(ctx context.Context, svc *core.Service, in core.CreateIdentityInput) (string, error) {
	res, err := svc.CreateIdentity(ctx, localAdminPrincipal(), in)
	switch {
	case err == nil:
		return res.Token, nil
	case errors.Is(err, domain.ErrAlreadyExists):
		return svc.RotateIdentityToken(ctx, localAdminPrincipal(), in.Name)
	default:
		return "", err
	}
}

// devCAPool builds the trust store dev's own readiness probe uses. It is the
// same file the banner tells the reader to pass as --ca, so a probe that
// verifies proves the instruction works.
func devCAPool(caFile string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caFile) //nolint:gosec // a path this command just wrote
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s holds no certificate", caFile)
	}
	return pool, nil
}

// hostOf returns the host part of a listen address, or the address itself when
// it carries no port.
func hostOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// devReachableHost is the host a person on this machine types to reach the
// server. A wildcard bind names no address, so the loopback one is the honest
// answer for a banner printed on the host doing the binding.
func devReachableHost(addr string) string {
	host := hostOf(addr)
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	default:
		return host
	}
}

func devPort(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}

func devConsoleURL(httpAddr string) string {
	return "https://" + net.JoinHostPort(devReachableHost(httpAddr), devPort(httpAddr)) + "/"
}

// devProbeURL is where dev waits for the server to come up. It always dials
// loopback — the probe runs on the host that is binding — while still
// verifying the certificate, which the dev leaf's 127.0.0.1 SAN allows.
func devProbeURL(httpAddr string) string {
	return "https://" + net.JoinHostPort("127.0.0.1", devPort(httpAddr)) + "/healthz"
}

// devEndpoint is the gRPC address the examples pass to --endpoint.
func devEndpoint(grpcAddr string) string {
	return net.JoinHostPort(devReachableHost(grpcAddr), devPort(grpcAddr))
}

// devExamples are the two commands the banner invites the reader to paste:
// one administrative call as the dev admin, and one application call as the
// unprivileged demo identity.
func devExamples(facts *devFacts) []string {
	endpoint := devEndpoint(facts.GRPCAddr)
	examples := []string{
		fmt.Sprintf("parameter-store admin namespace list --endpoint %s --ca %s --token %s",
			endpoint, facts.CAFile, facts.Admin.Token),
	}
	if facts.DemoApp != nil {
		examples = append(examples, fmt.Sprintf("parameter-store exec %s/%s --endpoint %s --ca %s --token %s -- env",
			devDemoEnv, devDemoApp, endpoint, facts.CAFile, facts.DemoApp.Token))
	}
	return examples
}

// devReadyTimeout bounds the wait for the listener. It is generous because the
// first start of a dev store also materializes the baseline and generates a
// master key.
const devReadyTimeout = 30 * time.Second

// awaitDevReady polls the health endpoint over TLS, verifying against the
// generated CA, until the server answers. Announcing before that would print a
// console URL that is not yet listening.
func (c *CLI) awaitDevReady(facts *devFacts, done <-chan int) error {
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: facts.caPool, MinVersion: tls.VersionTLS12},
		},
	}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(devReadyTimeout)
	var lastErr error
	for {
		select {
		case <-done:
			return errors.New("the server exited during startup")
		default:
		}
		resp, err := client.Get(facts.probeURL)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("%s: HTTP %d", facts.probeURL, resp.StatusCode)
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not answer within %s: %w", facts.probeURL, devReadyTimeout, lastErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// announceDev prints what the reader needs to use the server. Table mode puts
// a delimited banner on stderr so stdout stays free for a pipe; JSON mode puts
// the same facts on stdout as the single document every command promises
// there, and prints no banner.
func (c *CLI) announceDev(facts *devFacts) int {
	if c.jsonOutput() {
		return c.printJSON(facts)
	}
	const rule = "────────────────────────────────────────────────────────────────────────"
	w := c.Stderr
	store := facts.StoreDir
	if facts.Ephemeral {
		store += " (temporary; removed on exit)"
	}
	_, _ = fmt.Fprintf(w, "\n%s\nparameter-store dev — disposable demo server. Never use it for real data.\n%s\n", rule, rule)
	_, _ = fmt.Fprintf(w, "  console         %s\n", facts.ConsoleURL)
	_, _ = fmt.Fprintf(w, "  gRPC            %s\n", devEndpoint(facts.GRPCAddr))
	_, _ = fmt.Fprintf(w, "  CA certificate  %s\n", facts.CAFile)
	_, _ = fmt.Fprintf(w, "  store           %s\n", store)
	_, _ = fmt.Fprintf(w, "\n  dev-only admin token (%s)\n    %s\n", facts.Admin.Name, facts.Admin.Token)
	if facts.DemoApp != nil {
		_, _ = fmt.Fprintf(w, "  dev-only application token (%s)\n    %s\n", facts.DemoApp.Name, facts.DemoApp.Token)
	}
	if len(facts.Namespaces) > 0 {
		_, _ = fmt.Fprintf(w, "  seeded namespaces: %s\n", strings.Join(facts.Namespaces, ", "))
	}
	_, _ = fmt.Fprintln(w, "\n  Try it:")
	for _, example := range facts.Examples {
		_, _ = fmt.Fprintf(w, "    %s\n", example)
	}
	_, _ = fmt.Fprintf(w, "\n  Both tokens exist only in this store. Press Ctrl-C to stop.\n%s\n\n", rule)
	return exitOK
}
