package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditRetentionDefaults(t *testing.T) {
	cfg := Default()
	if time.Duration(cfg.Audit.RetainDuration) != 0 {
		t.Errorf("retain_duration default = %v, want 0 (keep forever)", time.Duration(cfg.Audit.RetainDuration))
	}
	if cfg.Audit.ArchiveDir != "" {
		t.Errorf("archive_dir default = %q, want empty", cfg.Audit.ArchiveDir)
	}
}

func TestAuditRetentionYAMLAndEnvOverlay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte("audit:\n  retain_duration: \"720h\"\n  archive_dir: \"/var/lib/kms/audit\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if time.Duration(cfg.Audit.RetainDuration) != 720*time.Hour {
		t.Errorf("retain_duration from yaml = %v, want 720h", time.Duration(cfg.Audit.RetainDuration))
	}
	if cfg.Audit.ArchiveDir != "/var/lib/kms/audit" {
		t.Errorf("archive_dir from yaml = %q", cfg.Audit.ArchiveDir)
	}

	t.Setenv("KMS_AUDIT_RETAIN_DURATION", "48h")
	t.Setenv("KMS_AUDIT_ARCHIVE_DIR", "/srv/archive")
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if time.Duration(cfg.Audit.RetainDuration) != 48*time.Hour {
		t.Errorf("retain_duration env override = %v, want 48h", time.Duration(cfg.Audit.RetainDuration))
	}
	if cfg.Audit.ArchiveDir != "/srv/archive" {
		t.Errorf("archive_dir env override = %q", cfg.Audit.ArchiveDir)
	}
}

func TestAuditRetentionEnvRejectsMalformedDuration(t *testing.T) {
	t.Setenv("KMS_AUDIT_RETAIN_DURATION", "a while")
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "KMS_AUDIT_RETAIN_DURATION") {
		t.Fatalf("err = %v, want malformed duration error", err)
	}
}

func TestAuditRetentionValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Config)
		want string // empty = must validate cleanly
	}{
		{"zero keeps forever", func(*Config) {}, ""},
		{
			"retention without an archive discards rows",
			func(c *Config) { c.Audit.RetainDuration = Duration(24 * time.Hour) },
			"",
		},
		{
			"retention with an archive",
			func(c *Config) {
				c.Audit.RetainDuration = Duration(24 * time.Hour)
				c.Audit.ArchiveDir = t.TempDir()
			},
			"",
		},
		{
			// Retention never creates the directory, so a typo must refuse
			// the start rather than fail every pass.
			"archive directory must exist",
			func(c *Config) {
				c.Audit.RetainDuration = Duration(24 * time.Hour)
				c.Audit.ArchiveDir = filepath.Join(t.TempDir(), "missing")
			},
			"audit.archive_dir",
		},
		{
			"archive directory must be a directory",
			func(c *Config) {
				c.Audit.RetainDuration = Duration(24 * time.Hour)
				file := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(file, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				c.Audit.ArchiveDir = file
			},
			"is not a directory",
		},
		{
			"negative retention",
			func(c *Config) { c.Audit.RetainDuration = Duration(-time.Second) },
			"audit.retain_duration must not be negative",
		},
		{
			// Without retention nothing is ever retired, so an archive
			// directory would silently stay empty.
			"archive without retention",
			func(c *Config) { c.Audit.ArchiveDir = "/var/lib/kms/audit" },
			"audit.archive_dir requires audit.retain_duration",
		},
		{
			"archive with zero retention",
			func(c *Config) {
				c.Audit.RetainDuration = 0
				c.Audit.ArchiveDir = "/var/lib/kms/audit"
			},
			"audit.archive_dir requires audit.retain_duration",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mut(&cfg)
			err := cfg.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("Validate: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestAuditRetentionInRedacted(t *testing.T) {
	cfg := Default()
	cfg.Audit.RetainDuration = Duration(72 * time.Hour)
	// The archive directory is a path, not a credential, so it is printed in
	// full: an operator debugging retention needs to see where rows land.
	cfg.Audit.ArchiveDir = "/var/lib/kms/audit"
	red := cfg.Redacted()
	for _, want := range []string{"audit_retain_duration=72h0m0s", "audit_archive_dir=/var/lib/kms/audit"} {
		if !strings.Contains(red, want) {
			t.Errorf("Redacted missing %q: %s", want, red)
		}
	}
}

func TestAuditRetentionFlags(t *testing.T) {
	fs := newFlagSet(t)
	bound := AddFlags(fs, "audit.retain_duration", "audit.archive_dir")
	if err := fs.Parse([]string{"--audit-retain-duration=36h", "--audit-archive-dir=/flag/archive"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cfg, prov := mustResolve(t, Options{LookupEnv: noEnv, Flags: bound})
	if time.Duration(cfg.Audit.RetainDuration) != 36*time.Hour {
		t.Errorf("retain_duration from flag = %v, want 36h", time.Duration(cfg.Audit.RetainDuration))
	}
	if cfg.Audit.ArchiveDir != "/flag/archive" {
		t.Errorf("archive_dir from flag = %q", cfg.Audit.ArchiveDir)
	}
	for key, want := range map[string]string{
		"audit.retain_duration": "flag --audit-retain-duration",
		"audit.archive_dir":     "flag --audit-archive-dir",
	} {
		if got := prov[key].String(); got != want {
			t.Errorf("provenance[%s] = %q, want %q", key, got, want)
		}
	}
}
