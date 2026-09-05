package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	kms "github.com/Suhaibinator/kms"
	"github.com/Suhaibinator/kms/internal/config"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/metrics"
	"github.com/Suhaibinator/kms/internal/server/httpserver"
	"github.com/Suhaibinator/kms/internal/server/listenertls"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/internal/watch"

	"net"
)

// isNonLoopbackBind reports whether addr (host:port) listens on an address
// reachable from off-host: an explicit non-loopback IP, or a wildcard/empty
// host. A loopback literal (127.0.0.1, ::1, localhost) returns false.
func isNonLoopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return true
	case "localhost":
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback()
	}
	// A hostname we can't classify — assume networked and warn.
	return true
}

// GRPCServer is the slice of the gRPC server that serve needs. The concrete
// implementation lives in internal/server/grpcserver.
type GRPCServer interface {
	Serve() error
	// GracefulStop drains in-flight RPCs. It can block indefinitely if
	// long-lived streams (watch subscribers) never end on their own, so serve
	// bounds it and falls back to Stop.
	GracefulStop()
	// Stop force-closes the server and all active streams immediately.
	Stop()
}

// GRPCConfig configures the injected gRPC server.
type GRPCConfig struct {
	Addr string
	TLS  *tls.Config
	// Metrics is the Prometheus exporter the gRPC interceptors report to, or
	// nil when metrics.enabled is off.
	Metrics *metrics.Metrics
}

// GRPCFactory builds the gRPC server. It is the SINGLE integration seam between
// this package and internal/server/grpcserver, kept nil here so this package
// compiles and its tests run before grpcserver lands. At integration the lead
// wires it in one line, e.g.:
//
//	cli.GRPCFactory = func(svc *core.Service, hub *watch.Hub, cfg cli.GRPCConfig) (cli.GRPCServer, error) {
//	    return grpcserver.New(svc, hub, grpcserver.Config{Addr: cfg.Addr, TLS: cfg.TLS})
//	}
//
// When nil, serve runs the HTTP server only and logs a warning.
var GRPCFactory func(svc *core.Service, hub *watch.Hub, cfg GRPCConfig) (GRPCServer, error)

func (c *CLI) cmdServe(args []string) int {
	flags := c.newFlags("serve")
	r := c.serverSettings(flags)
	c.setUsage(flags, "serve [flags]", "Run the gRPC and HTTP servers.", true)
	if !c.parseFlags(flags, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}

	// A hangup is a reload request once the listeners are up; before that —
	// during the passphrase prompt, database baseline verification, or CA
	// bootstrap — it must not kill the process. Ctrl-C at the prompt still works
	// (SIGINT is untouched).
	signal.Ignore(syscall.SIGHUP)

	cfg, prov, configFile, err := r.resolve()
	if err != nil {
		return c.fail("loading config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		return c.fail("invalid config: %v", err)
	}

	logger, logLevel := newLogger(c.Stderr, cfg.LogLevel())
	if configFile == "" {
		configFile = "none"
	}
	logger.Info("starting parameter-store",
		zap.String("version", Version),
		zap.String("config_file", configFile),
		zap.String("config", cfg.Redacted()))
	logConfigSources(logger, prov)

	// Startup order per plan 23.1: verify the store baseline, unseal, then
	// listeners.
	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.fail("opening database: %v", err)
	}
	defer func() { _ = store.Close() }()

	svc := core.New(store, logger, Version)
	svc.SetAuditEnabled(cfg.Audit.Enabled)
	svc.SetVerifyDefaultsLimits(core.VerifyDefaultsLimits{
		RequestsPerHour:       cfg.Server.VerifyDefaults.RequestsPerHour,
		Burst:                 cfg.Server.VerifyDefaults.Burst,
		MismatchBudgetPerHour: cfg.Server.VerifyDefaults.MismatchBudgetPerHour,
	})

	// Unseal before starting listeners. In interactive mode this blocks on the
	// passphrase prompt; no network listener exists until unseal succeeds.
	keyring, err := c.unseal(context.Background(), store, cfg.Encryption.KEKFile, false)
	if err != nil {
		return c.fail("acquiring master key: %v", err)
	}
	svc.SetKeyring(keyring)

	// Bootstrap the built-in CA (generate on first serve, load thereafter). Its
	// certificate anchors client-certificate authentication on the gRPC listener.
	if err := svc.BootstrapCA(context.Background()); err != nil {
		return c.fail("initializing certificate authority: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hub := watch.NewHub(store, logger, watch.Options{
		HeartbeatInterval:               time.Duration(cfg.Watch.HeartbeatInterval),
		RetainDuration:                  time.Duration(cfg.Watch.RetainDuration),
		RetainRows:                      cfg.Watch.RetainRows,
		ReleaseRetainDuration:           time.Duration(cfg.Watch.ReleaseRetainDuration),
		ReleaseRetainVersions:           cfg.Watch.ReleaseRetainVersions,
		ReleaseSubscriberRetainDuration: time.Duration(cfg.Watch.ReleaseSubscriberRetainDuration),
	})
	svc.SetHub(hub)
	go func() {
		if err := hub.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("watch hub stopped", zap.Error(err))
		}
	}()
	// Do not admit subscribers until the hub has captured its initial cursor;
	// otherwise a write can land between listener startup and hub initialization
	// and be absent from both replay and live delivery.
	<-hub.Started()

	// Attach the exporter before anything can serve a request, so no
	// authentication, authorization, or audit event is missed. Its watch
	// gauges read the hub at scrape time, which is why it waits for the hub.
	var exporter *metrics.Metrics
	if cfg.Metrics.Enabled {
		exporter = metrics.New(metrics.Options{Version: Version})
		svc.SetMetrics(exporter)
		exporter.SetWatchSource(func() metrics.WatchStats { return watchStats(hub.Stats()) })
	}

	// Audit retention runs on its own loop rather than inside the hub. The two
	// prune different things and fail differently: an unwritable archive
	// directory must stall audit retirement only, never the change-log pruning
	// subscribers depend on. The loop shares the serve context, so it ends with
	// everything else.
	auditRetain := time.Duration(cfg.Audit.RetainDuration)
	// Logged on every start, including when retention is off: "is this server
	// discarding audit history?" must be answerable from the boot log alone.
	logger.Info(auditRetentionStartupMsg,
		zap.Bool("enabled", auditRetain > 0),
		zap.String("retain", auditRetainDescription(auditRetain)),
		zap.String("archive_dir", cfg.Audit.ArchiveDir))
	if auditRetain > 0 {
		retention := &core.AuditRetention{
			Store:      store,
			Retain:     auditRetain,
			ArchiveDir: cfg.Audit.ArchiveDir,
			Logger:     logger,
		}
		// A typed nil would pass the interface's nil check and then
		// dereference, so the exporter is handed over only when it exists.
		if exporter != nil {
			retention.Metrics = exporter
		}
		if err := retention.Validate(); err != nil {
			return c.fail("configuring audit retention: %v", err)
		}
		go retention.Run(ctx)
	}

	tlsCfg, err := cfg.BuildServerTLS()
	if err != nil {
		return c.fail("building TLS config: %v", err)
	}

	// The admin client-certificate requirement is only meaningful when the
	// listeners speak TLS: without a handshake no certificate ever reaches the
	// server, so enforcing it would lock every admin out of a dev run. Relax it
	// and say so loudly rather than refusing to start.
	adminCertRequired := cfg.Security.AdminRequireClientCert && tlsCfg != nil
	svc.SetAdminRequireClientCert(adminCertRequired)
	var (
		lackingCerts  []string
		expiringCerts []core.ExpiringAdminCert
		lackingErr    error
	)
	if adminCertRequired {
		lackingCerts, expiringCerts, lackingErr = svc.AdminCertReport(ctx, adminCertExpiryWarning)
	}
	logAdminCertPosture(logger, cfg.Security.AdminRequireClientCert, tlsCfg != nil,
		isNonLoopbackBind(cfg.Server.GRPCAddr) || isNonLoopbackBind(cfg.Server.HTTPAddr),
		lackingCerts, expiringCerts, lackingErr)

	if tlsCfg == nil {
		// Plaintext transport carries bearer tokens and secret values in the
		// clear. Warn loudly, and escalate when bound to a non-loopback address
		// where that traffic leaves the host.
		for _, addr := range []string{cfg.Server.GRPCAddr, cfg.Server.HTTPAddr} {
			if isNonLoopbackBind(addr) {
				logger.Warn("TLS is DISABLED on a non-loopback address; bearer tokens and secret values will travel in cleartext — enable security.tls_enabled or bind to loopback / terminate TLS at a trusted proxy",
					zap.String("addr", addr))
			}
		}
		if !isNonLoopbackBind(cfg.Server.GRPCAddr) && !isNonLoopbackBind(cfg.Server.HTTPAddr) {
			logger.Warn("TLS is disabled (loopback bind); enable security.tls_enabled for any networked deployment")
		}
	}

	// A typed nil must not reach the reloadReporter interface, so the reload
	// path only sees the exporter when metrics are actually on.
	var reloadRep reloadReporter
	if exporter != nil {
		reloadRep = exporter
	}
	if exporter != nil {
		exporter.SetPosture(tlsCfg != nil, adminCertRequired)
		sampler := serveSampler(svc, store, cfg.Storage.SQLitePath)
		exporter.SetStartTime(time.Now())
		// Sample synchronously first: the listeners open on the next few
		// lines, and a scrape that arrives immediately should carry real
		// numbers rather than the zero every gauge starts at.
		exporter.Sample(ctx, sampler)
		go exporter.RunSampler(ctx, sampler)
	}

	// Both listeners accept — but never demand — a certificate from the
	// built-in CA, so machine clients and admins can authenticate by
	// certificate while token-only callers still connect. Admission is core's
	// decision, not the handshake's. The derived config lives in a Reloadable
	// so SIGHUP can swap the keypair and client CA under both listeners
	// without a restart; each listener gets its own slot because ALPN is
	// negotiated from the per-handshake config (gRPC insists on h2).
	derivedTLS, err := listenertls.Build(tlsCfg, svc)
	if err != nil {
		return c.fail("building listener TLS config: %v", err)
	}
	tlsHolder := listenertls.NewReloadable(derivedTLS)

	var grpcSrv GRPCServer
	grpcAddr := ""
	if GRPCFactory != nil {
		grpcAddr = cfg.Server.GRPCAddr
		grpcSrv, err = GRPCFactory(svc, hub, GRPCConfig{Addr: cfg.Server.GRPCAddr, TLS: tlsHolder.Listener("h2"), Metrics: exporter})
		if err != nil {
			return c.fail("building gRPC server: %v", err)
		}
		go func() {
			if err := grpcSrv.Serve(); err != nil {
				logger.Error("gRPC server stopped", zap.Error(err))
			}
		}()
		logger.Info("gRPC listening", zap.String("addr", cfg.Server.GRPCAddr), zap.Bool("tls", tlsCfg != nil))
	} else {
		logger.Warn("gRPC server not wired (GRPCFactory is nil); serving HTTP only")
	}

	var webRoot fs.FS
	if cfg.Frontend.Enabled {
		sub, serr := fs.Sub(kms.FrontendFS, "frontend/out")
		if serr != nil {
			return c.fail("mounting frontend assets: %v", serr)
		}
		webRoot = sub
	}
	httpSrv, err := httpserver.New(svc, httpserver.Config{
		Addr:                    cfg.Server.HTTPAddr,
		FrontendEnabled:         cfg.Frontend.Enabled,
		Frontend:                webRoot,
		Version:                 Version,
		TrustProxyHeaders:       cfg.Security.TrustProxyHeaders,
		GRPCAddr:                grpcAddr,
		TLSEnabled:              tlsCfg != nil,
		MTLSEnabled:             cfg.Security.MTLSEnabled,
		AdminClientCertRequired: adminCertRequired,
		AuditEnabled:            cfg.Audit.Enabled,
		AuditRetainDuration:     auditRetain,
		AuditArchiveEnabled:     cfg.Audit.ArchiveDir != "",
		Metrics:                 exporter,
	})
	if err != nil {
		return c.fail("building HTTP server: %v", err)
	}
	// The browser-facing listener uses the same derived config as gRPC: a
	// browser is offered the built-in CA and may present an admin certificate,
	// but one is never demanded at the handshake — an unauthenticated caller
	// still has to reach the login page and /api/v1/health.
	httpSrv.TLSConfig = tlsHolder.Listener("h2", "http/1.1")

	httpErr := make(chan error, 1)
	go func() {
		var e error
		if tlsCfg != nil {
			e = httpSrv.ListenAndServeTLS("", "")
		} else {
			e = httpSrv.ListenAndServe()
		}
		if e != nil && !errors.Is(e, http.ErrServerClosed) {
			httpErr <- e
		}
	}()
	logger.Info("HTTP listening", zap.String("addr", cfg.Server.HTTPAddr), zap.Bool("tls", tlsCfg != nil), zap.Bool("frontend", cfg.Frontend.Enabled))

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	exitCode := 0
	running := cfg
loop:
	for {
		select {
		case sig := <-sigCh:
			if sig == syscall.SIGHUP {
				logger.Info("reload signal received", zap.String("signal", sig.String()))
				running, _ = c.reloadServe(ctx, r, logger, logLevel, tlsHolder, svc, running, reloadRep)
				continue
			}
			logger.Info("shutdown signal received", zap.String("signal", sig.String()))
			break loop
		case <-c.reloadSignal:
			running, _ = c.reloadServe(ctx, r, logger, logLevel, tlsHolder, svc, running, reloadRep)
		case <-c.stopServe:
			logger.Info("shutdown requested")
			break loop
		case e := <-httpErr:
			// A listener that fails to start (e.g. address already in use) is fatal;
			// exit non-zero so a process supervisor reports the failure.
			logger.Error("HTTP server failed", zap.Error(e))
			exitCode = 1
			break loop
		}
	}

	shutdownCtx, shCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shCancel()
	if grpcSrv != nil {
		// GracefulStop waits for in-flight RPCs, but long-lived watch streams
		// never end on their own, so bound the wait and force Stop() past the
		// deadline. Otherwise a single connected subscriber would hang shutdown.
		stopped := make(chan struct{})
		go func() {
			grpcSrv.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			logger.Warn("gRPC graceful stop timed out; forcing close (active streams)")
			grpcSrv.Stop()
			<-stopped
		}
	}
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP shutdown error", zap.Error(err))
	}
	cancel() // stop the watch hub before the deferred store.Close
	logger.Info("shutdown complete")
	return exitCode
}

// logConfigSources records, at debug level, where every setting that is not on
// its built-in default came from — one field per setting, keyed by the YAML
// key. It answers "why is it using that value?" from the startup log alone,
// without printing the values themselves (Redacted already covers those).
func logConfigSources(logger *zap.Logger, prov config.Provenance) {
	if !logger.Core().Enabled(zapcore.DebugLevel) {
		return
	}
	fields := make([]zap.Field, 0, len(prov))
	for _, key := range config.SortedKeys() {
		if src := prov[key]; src.Kind != config.SourceDefault {
			fields = append(fields, zap.String(key, src.String()))
		}
	}
	logger.Debug("configuration sources", fields...)
}

// Startup messages describing what the admin client-certificate requirement
// actually does in this deployment. They are constants so the posture test
// pins the operator-facing wording, including the offline command an operator
// has to run.
const (
	adminCertUnenforceableMsg = "security.admin_require_client_cert cannot be enforced without TLS; admin token-only login is active"
	adminCertExposedMsg       = "security.admin_require_client_cert cannot be enforced without TLS; admin token-only login is active, so a bearer token alone grants full administrative access from the network — enable security.tls_enabled"
	adminCertDisabledMsg      = "security.admin_require_client_cert is disabled; a stolen admin token alone grants full administrative access"
	adminCertEnforcedMsg      = `admin identities must present a client certificate in addition to a bearer token; issue one offline with "parameter-store admin-cert issue NAME --out DIR"`
	adminCertMissingMsg       = "admin identity has no valid client certificate and cannot authenticate while admin_require_client_cert is enforced; run: parameter-store admin-cert issue <name> --out <dir>"
	adminCertExpiringMsg      = "admin identity's newest valid client certificate expires soon; an expired certificate is rejected by the TLS handshake itself, so re-issue before then with: parameter-store admin-cert issue <name> --out <dir>"
	adminCertScanFailedMsg    = "could not check which admin identities have a valid client certificate; admins without one will be refused"
)

// auditRetentionStartupMsg is the single line every start emits about audit
// retention. It is a constant because the wiring test pins it: the posture it
// reports — keeping history forever, or retiring it on a schedule — is one an
// operator has to be able to read back out of the boot log.
const auditRetentionStartupMsg = "audit retention"

// auditRetainDescription spells the retention window for that line. "forever"
// is the default posture and deserves the word; a bare "0s" would read like a
// misconfiguration.
func auditRetainDescription(retain time.Duration) string {
	if retain <= 0 {
		return "forever"
	}
	return retain.String()
}

// adminCertExpiryWarning is how far ahead serve warns about an admin
// certificate's expiry. Two weeks leaves room for a re-issue that needs host
// access and a browser import.
const adminCertExpiryWarning = 14 * 24 * time.Hour

// logAdminCertPosture states, at startup, whether an admin needs a client
// certificate on this server and what the consequence is either way. The three
// cases are distinct security postures, so each gets its own message:
// configured-but-unenforceable (TLS off, escalated when the server is reachable
// from the network), deliberately disabled, and enforced. Under enforcement it
// also names every admin identity that has no usable certificate — on an
// upgrade those admins are locked out until one is issued, and the operator
// needs to hear that at boot rather than at their next login. lacking and
// scanErr come from core.AdminsWithoutValidCert and are only consulted when the
// requirement is in force; a failed scan is reported, never fatal.
func logAdminCertPosture(logger *zap.Logger, configured, tlsEnabled, nonLoopback bool, lacking []string, expiring []core.ExpiringAdminCert, scanErr error) {
	switch {
	case !configured:
		logger.Warn(adminCertDisabledMsg)
	case !tlsEnabled:
		if nonLoopback {
			logger.Warn(adminCertExposedMsg)
		} else {
			logger.Warn(adminCertUnenforceableMsg)
		}
	default:
		logger.Info(adminCertEnforcedMsg)
		if scanErr != nil {
			logger.Warn(adminCertScanFailedMsg, zap.Error(scanErr))
		}
		for _, name := range lacking {
			logger.Warn(adminCertMissingMsg, zap.String("identity", name))
		}
		for _, e := range expiring {
			logger.Warn(adminCertExpiringMsg, zap.String("identity", e.Name), zap.String("serial", e.Serial),
				zap.Time("not_after", e.NotAfter), zap.Duration("expires_in", time.Until(e.NotAfter).Round(time.Hour)))
		}
	}
}

// newLogger builds a structured JSON logger at the given level, writing to w.
// The returned AtomicLevel is the core's enabler, so SetLevel takes effect for
// every consumer of the logger at once (serve changes it on SIGHUP).
func newLogger(w io.Writer, level zapcore.Level) (*zap.Logger, zap.AtomicLevel) {
	encCfg := zap.NewProductionEncoderConfig()
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	encCfg.EncodeLevel = zapcore.LowercaseLevelEncoder
	atomicLevel := zap.NewAtomicLevelAt(level)
	core := zapcore.NewCore(zapcore.NewJSONEncoder(encCfg), zapcore.AddSync(w), atomicLevel)
	return zap.New(core), atomicLevel
}
