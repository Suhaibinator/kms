package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyDefaultsDefaults(t *testing.T) {
	cfg := Default()
	vd := cfg.Server.VerifyDefaults
	if vd.RequestsPerHour != 60 || vd.Burst != 10 || vd.MismatchBudgetPerHour != 500 {
		t.Fatalf("verify_defaults defaults = %+v", vd)
	}
}

func TestVerifyDefaultsYAMLAndEnvOverlay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("server:\n  verify_defaults:\n    requests_per_hour: 5\n    burst: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMS_VERIFY_DEFAULTS_BURST", "7")
	t.Setenv("KMS_VERIFY_DEFAULTS_MISMATCH_BUDGET_PER_HOUR", " 42 ")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	vd := cfg.Server.VerifyDefaults
	if vd.RequestsPerHour != 5 {
		t.Errorf("requests_per_hour from yaml = %d, want 5", vd.RequestsPerHour)
	}
	if vd.Burst != 7 {
		t.Errorf("burst env override = %d, want 7", vd.Burst)
	}
	if vd.MismatchBudgetPerHour != 42 {
		t.Errorf("mismatch budget env override = %d, want 42", vd.MismatchBudgetPerHour)
	}
}

func TestVerifyDefaultsEnvRejectsMalformedInteger(t *testing.T) {
	t.Setenv("KMS_VERIFY_DEFAULTS_REQUESTS_PER_HOUR", "lots")
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "KMS_VERIFY_DEFAULTS_REQUESTS_PER_HOUR") {
		t.Fatalf("err = %v, want malformed integer error", err)
	}
}

func TestVerifyDefaultsValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"requests", func(c *Config) { c.Server.VerifyDefaults.RequestsPerHour = 0 }, "requests_per_hour"},
		{"burst", func(c *Config) { c.Server.VerifyDefaults.Burst = -1 }, "burst"},
		{"mismatch", func(c *Config) { c.Server.VerifyDefaults.MismatchBudgetPerHour = 0 }, "mismatch_budget_per_hour"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mut(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %s", err, tc.want)
			}
		})
	}
}

func TestVerifyDefaultsInRedacted(t *testing.T) {
	cfg := Default()
	cfg.Server.VerifyDefaults.MismatchBudgetPerHour = 123
	red := cfg.Redacted()
	for _, want := range []string{"verify_defaults_requests_per_hour=60", "verify_defaults_burst=10", "verify_defaults_mismatch_budget_per_hour=123"} {
		if !strings.Contains(red, want) {
			t.Errorf("Redacted missing %q: %s", want, red)
		}
	}
}
