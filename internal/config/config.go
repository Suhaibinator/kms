// Package config loads the parameter-store server configuration from a YAML
// file, applies environment-variable overrides, fills defaults, validates the
// result, and builds the server TLS configuration (plan section 7).
//
// Precedence, lowest to highest: built-in defaults, YAML file, environment
// variables. Values that may contain sensitive material are never logged
// wholesale; use Redacted for startup logging.
package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the fully resolved server configuration.
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Storage    StorageConfig    `yaml:"storage"`
	Security   SecurityConfig   `yaml:"security"`
	Encryption EncryptionConfig `yaml:"encryption"`
	Frontend   FrontendConfig   `yaml:"frontend"`
	Audit      AuditConfig      `yaml:"audit"`
	Watch      WatchConfig      `yaml:"watch"`
	Log        LogConfig        `yaml:"log"`
}

// ServerConfig holds listener addresses.
type ServerConfig struct {
	GRPCAddr string `yaml:"grpc_addr"`
	HTTPAddr string `yaml:"http_addr"`
}

// StorageConfig holds the SQLite location.
type StorageConfig struct {
	SQLitePath string `yaml:"sqlite_path"`
}

// SecurityConfig holds transport-security settings.
type SecurityConfig struct {
	TLSEnabled     bool   `yaml:"tls_enabled"`
	MTLSEnabled    bool   `yaml:"mtls_enabled"`
	ServerCertFile string `yaml:"server_cert_file"`
	ServerKeyFile  string `yaml:"server_key_file"`
	ClientCAFile   string `yaml:"client_ca_file"`
	// TrustProxyHeaders enables honoring X-Forwarded-For for the HTTP client
	// IP (rate-limit key + audit source IP). Enable ONLY behind a trusted
	// reverse proxy; otherwise clients can spoof it. Default false.
	TrustProxyHeaders bool `yaml:"trust_proxy_headers"`
}

// EncryptionConfig points at the local master-key file (may be empty for
// passphrase mode).
type EncryptionConfig struct {
	KEKFile string `yaml:"kek_file"`
}

// FrontendConfig toggles the embedded frontend.
type FrontendConfig struct {
	Enabled bool `yaml:"enabled"`
}

// AuditConfig toggles audit logging.
type AuditConfig struct {
	Enabled bool `yaml:"enabled"`
}

// WatchConfig holds watch/hot-reload tuning.
type WatchConfig struct {
	HeartbeatInterval Duration `yaml:"heartbeat_interval"`
	RetainDuration    Duration `yaml:"retain_duration"`
	RetainRows        int      `yaml:"retain_rows"`
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level string `yaml:"level"`
}

// Default returns the built-in configuration with every default filled in.
func Default() Config {
	return Config{
		Server: ServerConfig{
			GRPCAddr: "0.0.0.0:8443",
			HTTPAddr: "0.0.0.0:8080",
		},
		Storage: StorageConfig{
			SQLitePath: "./kms.db",
		},
		Encryption: EncryptionConfig{},
		Frontend:   FrontendConfig{Enabled: true},
		Audit:      AuditConfig{Enabled: true},
		Watch: WatchConfig{
			HeartbeatInterval: Duration(30 * time.Second),
			RetainDuration:    Duration(24 * time.Hour),
			RetainRows:        10000,
		},
		Log: LogConfig{Level: "info"},
	}
}

// Load resolves configuration from defaults, an optional YAML file, and
// environment overrides. An empty path skips the file. It does not run
// semantic validation; call Validate separately (the CLI does so for serve).
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("reading config file %s: %w", path, err)
		}
		// Decode onto the defaults so unset fields keep their defaults.
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("parsing config file %s: %w", path, err)
		}
	}
	if err := cfg.ApplyEnv(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ApplyEnv overlays KMS_* environment variables onto the config. Only
// variables that are set take effect. A malformed boolean value is a hard
// error rather than being silently ignored, so an operator who writes
// KMS_TLS_ENABLED=yes gets told instead of unknowingly running without TLS.
func (c *Config) ApplyEnv() error {
	setStr(&c.Server.GRPCAddr, "KMS_GRPC_ADDR")
	setStr(&c.Server.HTTPAddr, "KMS_HTTP_ADDR")
	setStr(&c.Storage.SQLitePath, "KMS_SQLITE_PATH")
	setStr(&c.Encryption.KEKFile, "KMS_KEK_FILE")
	setStr(&c.Security.ServerCertFile, "KMS_SERVER_CERT_FILE")
	setStr(&c.Security.ServerKeyFile, "KMS_SERVER_KEY_FILE")
	setStr(&c.Security.ClientCAFile, "KMS_CLIENT_CA_FILE")
	setStr(&c.Log.Level, "KMS_LOG_LEVEL")

	for _, b := range []struct {
		dst *bool
		key string
	}{
		{&c.Security.TLSEnabled, "KMS_TLS_ENABLED"},
		{&c.Security.MTLSEnabled, "KMS_MTLS_ENABLED"},
		{&c.Security.TrustProxyHeaders, "KMS_TRUST_PROXY_HEADERS"},
		{&c.Frontend.Enabled, "KMS_FRONTEND_ENABLED"},
		{&c.Audit.Enabled, "KMS_AUDIT_ENABLED"},
	} {
		if err := setBool(b.dst, b.key); err != nil {
			return err
		}
	}
	return nil
}

func setStr(dst *string, key string) {
	if v, ok := os.LookupEnv(key); ok {
		*dst = v
	}
}

func setBool(dst *bool, key string) error {
	v, ok := os.LookupEnv(key)
	if !ok {
		return nil
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("%s=%q is not a valid boolean (use true/false/1/0)", key, v)
	}
	*dst = b
	return nil
}

// Validate checks cross-field constraints and the existence of any referenced
// TLS material. It returns the first problem found.
func (c Config) Validate() error {
	if c.Server.GRPCAddr == "" {
		return fmt.Errorf("server.grpc_addr must not be empty")
	}
	if c.Server.HTTPAddr == "" {
		return fmt.Errorf("server.http_addr must not be empty")
	}
	if c.Storage.SQLitePath == "" {
		return fmt.Errorf("storage.sqlite_path must not be empty")
	}
	if c.Security.MTLSEnabled && !c.Security.TLSEnabled {
		return fmt.Errorf("security.mtls_enabled requires security.tls_enabled")
	}
	if c.Security.TLSEnabled {
		if c.Security.ServerCertFile == "" || c.Security.ServerKeyFile == "" {
			return fmt.Errorf("security.tls_enabled requires server_cert_file and server_key_file")
		}
		if err := fileMustExist(c.Security.ServerCertFile, "security.server_cert_file"); err != nil {
			return err
		}
		if err := fileMustExist(c.Security.ServerKeyFile, "security.server_key_file"); err != nil {
			return err
		}
	}
	if c.Security.MTLSEnabled {
		if c.Security.ClientCAFile == "" {
			return fmt.Errorf("security.mtls_enabled requires client_ca_file")
		}
		if err := fileMustExist(c.Security.ClientCAFile, "security.client_ca_file"); err != nil {
			return err
		}
	}
	return nil
}

func fileMustExist(path, label string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s: %s is a directory, expected a file", label, path)
	}
	return nil
}

// BuildServerTLS builds the server *tls.Config from the security settings, or
// returns (nil, nil) when TLS is disabled. When mtls_enabled is set it loads
// the client CA bundle and requires+verifies client certificates.
func (c Config) BuildServerTLS() (*tls.Config, error) {
	if !c.Security.TLSEnabled {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(c.Security.ServerCertFile, c.Security.ServerKeyFile)
	if err != nil {
		return nil, fmt.Errorf("loading server key pair: %w", err)
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}
	if c.Security.MTLSEnabled {
		pem, err := os.ReadFile(c.Security.ClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("reading client CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in client CA file %s", c.Security.ClientCAFile)
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

// Redacted returns a single-line, log-safe summary of the configuration. It
// includes addresses, paths, and feature flags but is deliberately conservative
// so no accidentally-sensitive value is ever dumped wholesale.
func (c Config) Redacted() string {
	kek := "unset"
	if c.Encryption.KEKFile != "" {
		kek = "set"
	}
	return fmt.Sprintf(
		"grpc_addr=%s http_addr=%s sqlite_path=%s tls=%t mtls=%t kek_file=%s frontend=%t audit=%t "+
			"heartbeat=%s retain_duration=%s retain_rows=%d log_level=%s",
		c.Server.GRPCAddr, c.Server.HTTPAddr, c.Storage.SQLitePath,
		c.Security.TLSEnabled, c.Security.MTLSEnabled, kek,
		c.Frontend.Enabled, c.Audit.Enabled,
		time.Duration(c.Watch.HeartbeatInterval), time.Duration(c.Watch.RetainDuration),
		c.Watch.RetainRows, c.Log.Level)
}

// LogLevel maps the configured level string to a slog.Level, defaulting to
// info for empty or unrecognized values.
func (c Config) LogLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(c.Log.Level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
