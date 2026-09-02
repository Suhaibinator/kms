package cli

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"go.uber.org/zap"

	"github.com/Suhaibinator/kms/internal/config"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/server/listenertls"
)

// Reload messages. Constants so the tests pin the operator-facing wording.
const (
	configReloadedMsg     = "configuration reloaded"
	configReloadFailedMsg = "configuration reload failed; running configuration unchanged"
)

// Reload results, as reported to the metrics exporter.
const (
	reloadApplied  = "applied"
	reloadRejected = "rejected"
)

// reloadableKeys are the settings SIGHUP applies without a restart. Everything
// else is reported under "ignored" and keeps its running value: listen
// addresses and the database path are bound for the process lifetime, the KEK
// changes only through rotate-kek, TLS/mTLS on-off picks ListenAndServe vs
// ListenAndServeTLS at start, and the admin client-certificate requirement
// and audit switch are privilege-boundary changes that must be a deliberate
// restart emitting the startup posture log.
//
// The certificate, key and client-CA *contents* are always re-read when TLS
// is on — operators rotate the files far more often than they change the
// paths — so a SIGHUP with no config change is the cert-rotation signal. With
// TLS off the three path settings are ignored like everything else.
var reloadableKeys = []string{
	"log.level",
	"security.server_cert_file",
	"security.server_key_file",
	"security.client_ca_file",
}

// reloadReporter is the metrics hook for reload outcomes; a nil reporter is
// fine (the Prometheus exporter is optional).
type reloadReporter interface{ ReloadResult(result string) }

// reloadServe re-resolves the configuration with the startup precedence
// (flag > env > file > default; a flag-set value therefore never changes) and
// applies the reloadable subset all-or-nothing: the new file is parsed and
// validated and the new TLS material is loaded into a local config before
// anything running is touched. On any error it logs once at error level and
// returns running unchanged.
//
// It returns the configuration now in effect — running with the reloadable
// keys taken from the new resolution — for the next reload to diff against.
func (c *CLI) reloadServe(ctx context.Context, r *settingsResolver, logger *zap.Logger, level zap.AtomicLevel,
	holder *listenertls.Reloadable, svc *core.Service, running config.Config, rep reloadReporter) (config.Config, error) {
	next, err := c.prepareReload(ctx, r, logger, level, holder, svc, running)
	if err != nil {
		logger.Error(configReloadFailedMsg, zap.Error(err))
		if rep != nil {
			rep.ReloadResult(reloadRejected)
		}
		return running, err
	}
	if rep != nil {
		rep.ReloadResult(reloadApplied)
	}
	return next, nil
}

func (c *CLI) prepareReload(ctx context.Context, r *settingsResolver, logger *zap.Logger, level zap.AtomicLevel,
	holder *listenertls.Reloadable, svc *core.Service, running config.Config) (config.Config, error) {
	cfg, prov, _, err := r.resolve()
	if err != nil {
		return running, fmt.Errorf("loading config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return running, fmt.Errorf("invalid config: %w", err)
	}

	// TLS material: built into a local config first so a bad file cannot
	// take down the listeners. The running on/off switches are used with the
	// new paths, since tls_enabled/mtls_enabled themselves are not reloadable.
	var (
		derived           *tls.Config
		certChanged       bool
		clientCAChanged   bool
		leafSerial        string
		leafNotAfter      time.Time
		tlsOn             = holder != nil
		previousCertPrint string
	)
	if tlsOn {
		tlsCfg := cfg
		tlsCfg.Security.TLSEnabled = running.Security.TLSEnabled
		tlsCfg.Security.MTLSEnabled = running.Security.MTLSEnabled
		base, err := tlsCfg.BuildServerTLS()
		if err != nil {
			return running, fmt.Errorf("building TLS config: %w", err)
		}
		derived, err = listenertls.Build(base, svc)
		if err != nil {
			return running, fmt.Errorf("building listener TLS config: %w", err)
		}
		current := holder.Current()
		previousCertPrint = leafFingerprint(current)
		certChanged = previousCertPrint != leafFingerprint(derived)
		clientCAChanged = !current.ClientCAs.Equal(derived.ClientCAs)
		if leaf := leafCertificate(derived); leaf != nil {
			leafSerial = leaf.SerialNumber.Text(16)
			leafNotAfter = leaf.NotAfter
		}
	}

	// Everything validated: apply.
	if tlsOn {
		holder.Swap(derived)
	}
	level.SetLevel(cfg.LogLevel())

	next := running
	next.Log.Level = cfg.Log.Level
	applied := reloadableKeys
	if tlsOn {
		next.Security.ServerCertFile = cfg.Security.ServerCertFile
		next.Security.ServerKeyFile = cfg.Security.ServerKeyFile
		next.Security.ClientCAFile = cfg.Security.ClientCAFile
	} else {
		applied = reloadableKeys[:1]
	}

	// The certificate posture is worth restating after a swap: a rotated
	// client CA or server cert changes which admin certificates still work.
	adminCertRequired := running.Security.AdminRequireClientCert && tlsOn
	var (
		lackingCerts  []string
		expiringCerts []core.ExpiringAdminCert
		lackingErr    error
	)
	if adminCertRequired {
		lackingCerts, expiringCerts, lackingErr = svc.AdminCertReport(ctx, adminCertExpiryWarning)
	}
	logAdminCertPosture(logger, running.Security.AdminRequireClientCert, tlsOn,
		isNonLoopbackBind(running.Server.GRPCAddr) || isNonLoopbackBind(running.Server.HTTPAddr),
		lackingCerts, expiringCerts, lackingErr)
	logConfigSources(logger, prov)

	changed, ignored := diffSettings(&running, &cfg, applied)
	fields := []zap.Field{
		zap.Strings("changed", changed),
		zap.Strings("ignored", ignored),
		zap.String("log_level", cfg.LogLevel().String()),
		zap.Bool("tls", tlsOn),
	}
	if tlsOn {
		fields = append(fields,
			zap.Bool("server_certificate_changed", certChanged),
			zap.String("server_certificate_serial", leafSerial),
			zap.Time("server_certificate_not_after", leafNotAfter),
			zap.Bool("client_ca_changed", clientCAChanged))
	}
	logger.Info(configReloadedMsg, fields...)
	return next, nil
}

// diffSettings partitions the settings whose value differs between the running
// and newly resolved configuration into those the reload applied and those it
// ignored. Both lists follow the registry order of config.Settings.
func diffSettings(running, next *config.Config, applied []string) (changed, ignored []string) {
	for _, key := range config.SortedKeys() {
		s, ok := config.Lookup(key)
		if !ok || s.Get(running) == s.Get(next) {
			continue
		}
		if slices.Contains(applied, key) {
			changed = append(changed, key)
		} else {
			ignored = append(ignored, key)
		}
	}
	return changed, ignored
}

// leafCertificate parses the first certificate of the config's chain.
func leafCertificate(cfg *tls.Config) *x509.Certificate {
	if cfg == nil || len(cfg.Certificates) == 0 {
		return nil
	}
	c := cfg.Certificates[0]
	if c.Leaf != nil {
		return c.Leaf
	}
	if len(c.Certificate) == 0 {
		return nil
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return nil
	}
	return leaf
}

// leafFingerprint is the SHA-256 of the leaf's DER bytes, "" without one.
func leafFingerprint(cfg *tls.Config) string {
	if cfg == nil || len(cfg.Certificates) == 0 || len(cfg.Certificates[0].Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(cfg.Certificates[0].Certificate[0])
	return hex.EncodeToString(sum[:])
}
