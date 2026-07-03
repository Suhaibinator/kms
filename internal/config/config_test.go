package config

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"
)

func TestDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Server.GRPCAddr != "0.0.0.0:8443" || cfg.Server.HTTPAddr != "0.0.0.0:8080" {
		t.Fatalf("default addrs = %+v", cfg.Server)
	}
	if cfg.Storage.SQLitePath != "./kms.db" {
		t.Fatalf("default sqlite = %q", cfg.Storage.SQLitePath)
	}
	if !cfg.Frontend.Enabled || !cfg.Audit.Enabled {
		t.Fatalf("frontend/audit should default enabled")
	}
	if time.Duration(cfg.Watch.HeartbeatInterval) != 30*time.Second {
		t.Fatalf("heartbeat default = %v", time.Duration(cfg.Watch.HeartbeatInterval))
	}
}

func TestLoadYAMLOverlay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
server:
  grpc_addr: "127.0.0.1:9443"
storage:
  sqlite_path: "/data/kms.db"
frontend:
  enabled: false
watch:
  heartbeat_interval: "45s"
  retain_rows: 500
log:
  level: debug
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.GRPCAddr != "127.0.0.1:9443" {
		t.Errorf("grpc_addr = %q", cfg.Server.GRPCAddr)
	}
	// Unset fields keep defaults.
	if cfg.Server.HTTPAddr != "0.0.0.0:8080" {
		t.Errorf("http_addr = %q (should keep default)", cfg.Server.HTTPAddr)
	}
	if cfg.Frontend.Enabled {
		t.Errorf("frontend should be disabled by file")
	}
	if time.Duration(cfg.Watch.HeartbeatInterval) != 45*time.Second {
		t.Errorf("heartbeat = %v", time.Duration(cfg.Watch.HeartbeatInterval))
	}
	if cfg.Watch.RetainRows != 500 {
		t.Errorf("retain_rows = %d", cfg.Watch.RetainRows)
	}
	if cfg.LogLevel() != zapcore.DebugLevel {
		t.Errorf("log level = %v", cfg.LogLevel())
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("KMS_GRPC_ADDR", "10.0.0.1:1")
	t.Setenv("KMS_HTTP_ADDR", "10.0.0.1:2")
	t.Setenv("KMS_SQLITE_PATH", "/env/kms.db")
	t.Setenv("KMS_KEK_FILE", "/env/master.key")
	t.Setenv("KMS_TLS_ENABLED", "true")
	t.Setenv("KMS_FRONTEND_ENABLED", "false")
	t.Setenv("KMS_LOG_LEVEL", "warn")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.GRPCAddr != "10.0.0.1:1" || cfg.Server.HTTPAddr != "10.0.0.1:2" {
		t.Errorf("env addrs = %+v", cfg.Server)
	}
	if cfg.Storage.SQLitePath != "/env/kms.db" || cfg.Encryption.KEKFile != "/env/master.key" {
		t.Errorf("env storage/kek = %+v %+v", cfg.Storage, cfg.Encryption)
	}
	if !cfg.Security.TLSEnabled {
		t.Errorf("tls should be enabled via env")
	}
	if cfg.Frontend.Enabled {
		t.Errorf("frontend should be disabled via env")
	}
	if cfg.LogLevel() != zapcore.WarnLevel {
		t.Errorf("log level = %v", cfg.LogLevel())
	}
}

func TestEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("server:\n  grpc_addr: \"1.1.1.1:1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMS_GRPC_ADDR", "2.2.2.2:2")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.GRPCAddr != "2.2.2.2:2" {
		t.Fatalf("env should override file: %q", cfg.Server.GRPCAddr)
	}
}

func TestValidate(t *testing.T) {
	t.Run("mtls requires tls", func(t *testing.T) {
		cfg := Default()
		cfg.Security.MTLSEnabled = true
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "tls_enabled") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("tls requires cert and key", func(t *testing.T) {
		cfg := Default()
		cfg.Security.TLSEnabled = true
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "server_cert_file") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("tls cert must exist", func(t *testing.T) {
		cfg := Default()
		cfg.Security.TLSEnabled = true
		cfg.Security.ServerCertFile = "/nope/server.crt"
		cfg.Security.ServerKeyFile = "/nope/server.key"
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected error for missing cert files")
		}
	})
	t.Run("valid tls with real files", func(t *testing.T) {
		dir := t.TempDir()
		cert, key := writeTestCert(t, dir)
		cfg := Default()
		cfg.Security.TLSEnabled = true
		cfg.Security.ServerCertFile = cert
		cfg.Security.ServerKeyFile = key
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})
	t.Run("defaults are valid", func(t *testing.T) {
		if err := Default().Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})
}

func TestBuildServerTLS(t *testing.T) {
	t.Run("disabled returns nil", func(t *testing.T) {
		cfg := Default()
		tc, err := cfg.BuildServerTLS()
		if err != nil || tc != nil {
			t.Fatalf("expected (nil,nil), got (%v,%v)", tc, err)
		}
	})
	t.Run("tls loads cert", func(t *testing.T) {
		dir := t.TempDir()
		cert, key := writeTestCert(t, dir)
		cfg := Default()
		cfg.Security.TLSEnabled = true
		cfg.Security.ServerCertFile = cert
		cfg.Security.ServerKeyFile = key
		tc, err := cfg.BuildServerTLS()
		if err != nil {
			t.Fatalf("BuildServerTLS: %v", err)
		}
		if len(tc.Certificates) != 1 || tc.ClientAuth != tls.NoClientCert {
			t.Fatalf("tls config = %+v", tc)
		}
	})
	t.Run("mtls requires client cert", func(t *testing.T) {
		dir := t.TempDir()
		cert, key := writeTestCert(t, dir)
		cfg := Default()
		cfg.Security.TLSEnabled = true
		cfg.Security.MTLSEnabled = true
		cfg.Security.ServerCertFile = cert
		cfg.Security.ServerKeyFile = key
		cfg.Security.ClientCAFile = cert // reuse the self-signed cert as a CA bundle
		tc, err := cfg.BuildServerTLS()
		if err != nil {
			t.Fatalf("BuildServerTLS: %v", err)
		}
		if tc.ClientAuth != tls.RequireAndVerifyClientCert || tc.ClientCAs == nil {
			t.Fatalf("mtls not configured: %+v", tc)
		}
	})
}

func TestRedactedNoSecretsDumped(t *testing.T) {
	cfg := Default()
	cfg.Encryption.KEKFile = "/etc/secret/master.key"
	red := cfg.Redacted()
	if strings.Contains(red, "/etc/secret/master.key") {
		t.Fatalf("Redacted leaked the kek file path: %q", red)
	}
	if !strings.Contains(red, "kek_file=set") {
		t.Fatalf("Redacted should indicate kek presence: %q", red)
	}
	if !strings.Contains(red, "grpc_addr=") {
		t.Fatalf("Redacted missing addresses: %q", red)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("/no/such/config.yaml"); err == nil {
		t.Fatalf("expected error for missing config file")
	}
}
