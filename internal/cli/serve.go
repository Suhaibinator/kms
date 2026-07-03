package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	kms "github.com/Suhaibinator/kms"
	"github.com/Suhaibinator/kms/internal/config"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/server/httpserver"
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
	configPath := flags.String("config", c.ConfigPath, "config file path (env KMS_CONFIG)")
	if !c.parseFlags(flags, args) {
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return c.fail("loading config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		return c.fail("invalid config: %v", err)
	}

	logger := newLogger(c.Stderr, cfg.LogLevel())
	logger.Info("starting parameter-store", "version", Version, "config", cfg.Redacted())

	// Startup order per plan 23.1: open store (migrates), unseal, then listeners.
	store, err := storage.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return c.fail("opening database: %v", err)
	}
	defer func() { _ = store.Close() }()

	svc := core.New(store, logger, Version)

	// Unseal before starting listeners. In interactive mode this blocks on the
	// passphrase prompt; readiness gating keeps any early request answered 503.
	keyring, err := c.unseal(context.Background(), store, cfg.Encryption.KEKFile, false)
	if err != nil {
		return c.fail("acquiring master key: %v", err)
	}
	svc.SetKeyring(keyring)

	// Bootstrap the built-in CA (generate on first unseal, load thereafter). Its
	// certificate anchors client-certificate authentication on the gRPC listener.
	if err := svc.BootstrapCA(context.Background()); err != nil {
		return c.fail("initializing certificate authority: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hub := watch.NewHub(store, logger, watch.Options{
		HeartbeatInterval: time.Duration(cfg.Watch.HeartbeatInterval),
		RetainDuration:    time.Duration(cfg.Watch.RetainDuration),
		RetainRows:        cfg.Watch.RetainRows,
	})
	svc.SetHub(hub)
	go func() {
		if err := hub.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("watch hub stopped", "error", err)
		}
	}()

	tlsCfg, err := cfg.BuildServerTLS()
	if err != nil {
		return c.fail("building TLS config: %v", err)
	}
	if tlsCfg == nil {
		// Plaintext transport carries bearer tokens and secret values in the
		// clear. Warn loudly, and escalate when bound to a non-loopback address
		// where that traffic leaves the host.
		for _, addr := range []string{cfg.Server.GRPCAddr, cfg.Server.HTTPAddr} {
			if isNonLoopbackBind(addr) {
				logger.Warn("TLS is DISABLED on a non-loopback address; bearer tokens and secret values will travel in cleartext — enable security.tls_enabled or bind to loopback / terminate TLS at a trusted proxy",
					"addr", addr)
			}
		}
		if !isNonLoopbackBind(cfg.Server.GRPCAddr) && !isNonLoopbackBind(cfg.Server.HTTPAddr) {
			logger.Warn("TLS is disabled (loopback bind); enable security.tls_enabled for any networked deployment")
		}
	}

	var grpcSrv GRPCServer
	if GRPCFactory != nil {
		// The gRPC listener authenticates machine clients by mTLS: add the built-in
		// CA to its client-CA pool and verify a presented client certificate, but do
		// not require one (VerifyClientCertIfGiven) — token-only clients still
		// connect, and the per-namespace auth-method gate decides admittance.
		grpcTLS, terr := grpcServerTLS(tlsCfg, svc)
		if terr != nil {
			return c.fail("building gRPC TLS config: %v", terr)
		}
		grpcSrv, err = GRPCFactory(svc, hub, GRPCConfig{Addr: cfg.Server.GRPCAddr, TLS: grpcTLS})
		if err != nil {
			return c.fail("building gRPC server: %v", err)
		}
		go func() {
			if err := grpcSrv.Serve(); err != nil {
				logger.Error("gRPC server stopped", "error", err)
			}
		}()
		logger.Info("gRPC listening", "addr", cfg.Server.GRPCAddr, "tls", tlsCfg != nil)
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
		Addr:              cfg.Server.HTTPAddr,
		FrontendEnabled:   cfg.Frontend.Enabled,
		Frontend:          webRoot,
		Version:           Version,
		TrustProxyHeaders: cfg.Security.TrustProxyHeaders,
	})
	if err != nil {
		return c.fail("building HTTP server: %v", err)
	}

	httpErr := make(chan error, 1)
	go func() {
		var e error
		if tlsCfg != nil {
			// The browser-facing HTTP listener uses the server certificate but
			// must not demand client certificates even under mTLS (that gate is
			// for machine gRPC clients). Operators wanting mTLS on the UI put it
			// behind a terminating proxy.
			httpTLS := tlsCfg.Clone()
			httpTLS.ClientAuth = tls.NoClientCert
			httpSrv.TLSConfig = httpTLS
			e = httpSrv.ListenAndServeTLS("", "")
		} else {
			e = httpSrv.ListenAndServe()
		}
		if e != nil && !errors.Is(e, http.ErrServerClosed) {
			httpErr <- e
		}
	}()
	logger.Info("HTTP listening", "addr", cfg.Server.HTTPAddr, "tls", tlsCfg != nil, "frontend", cfg.Frontend.Enabled)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	exitCode := 0
	select {
	case sig := <-sigCh:
		logger.Info("shutdown signal received", "signal", sig.String())
	case e := <-httpErr:
		// A listener that fails to start (e.g. address already in use) is fatal;
		// exit non-zero so a process supervisor reports the failure.
		logger.Error("HTTP server failed", "error", e)
		exitCode = 1
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
		logger.Error("HTTP shutdown error", "error", err)
	}
	cancel() // stop the watch hub before the deferred store.Close
	logger.Info("shutdown complete")
	return exitCode
}

// grpcServerTLS derives the gRPC listener's TLS config from the base server
// config, adding the built-in CA to the client-CA pool and switching to
// VerifyClientCertIfGiven so a client may authenticate by certificate without
// every client being forced to present one. A nil base (TLS disabled) yields nil
// — plaintext development transport carries no client certificates.
func grpcServerTLS(base *tls.Config, svc *core.Service) (*tls.Config, error) {
	if base == nil {
		return nil, nil
	}
	caCert, err := svc.CACertificate()
	if err != nil {
		return nil, err
	}
	cfg := base.Clone()
	var pool *x509.CertPool
	if cfg.ClientCAs != nil {
		pool = cfg.ClientCAs.Clone()
	} else {
		pool = x509.NewCertPool()
	}
	pool.AddCert(caCert)
	cfg.ClientCAs = pool
	cfg.ClientAuth = tls.VerifyClientCertIfGiven
	return cfg, nil
}

// newLogger builds a structured JSON logger at the given level, writing to w.
func newLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}
