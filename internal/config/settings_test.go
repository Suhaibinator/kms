package config

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// noEnv is an empty environment: every lookup misses.
var noEnv = envMap(nil)

// envMap returns a LookupEnv backed by a map, so tests never depend on (or
// mutate) the real process environment and can run in parallel.
func envMap(vars map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := vars[key]
		return v, ok
	}
}

// writeConfigFile writes body to a fresh temp file and returns its path.
func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// newFlagSet returns a quiet FlagSet suitable for tests.
func newFlagSet(t *testing.T) *flag.FlagSet {
	t.Helper()
	fs := flag.NewFlagSet("kms", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// recovered runs f and reports the panic value, if any.
func recovered(f func()) (msg string, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			msg, panicked = fmt.Sprint(r), true
		}
	}()
	f()
	return "", false
}

// mustResolve resolves and fails the test on error.
func mustResolve(t *testing.T, opts Options) (Config, Provenance) {
	t.Helper()
	cfg, prov, err := Resolve(opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return cfg, prov
}

// mustLookup returns the registered setting for key.
func mustLookup(t *testing.T, key string) Setting {
	t.Helper()
	s, ok := Lookup(key)
	if !ok {
		t.Fatalf("Lookup(%q): not registered", key)
	}
	return s
}

// configLeafKeys walks typ via its yaml struct tags and records every non-struct
// field as a dotted key, mirroring how a YAML config file addresses it.
func configLeafKeys(t *testing.T, typ reflect.Type, prefix string, out map[string]bool) {
	t.Helper()
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			t.Fatalf("%s.%s has no yaml tag; it cannot be configured or documented", typ.Name(), f.Name)
		}
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}
		// Duration is a named int64, not a struct, so it is a leaf.
		if f.Type.Kind() == reflect.Struct {
			configLeafKeys(t, f.Type, key, out)
			continue
		}
		out[key] = true
	}
}

// TestSettingsRegistryCoversConfig is the guard that keeps the registry and the
// Config struct in lockstep: a new field without a Setting would be silently
// unconfigurable, and a stale Setting would advertise a key that no longer
// exists.
func TestSettingsRegistryCoversConfig(t *testing.T) {
	t.Parallel()

	want := map[string]bool{}
	configLeafKeys(t, reflect.TypeOf(Config{}), "", want)

	got := map[string]bool{}
	for _, s := range Settings {
		got[s.Key] = true
	}

	for key := range want {
		if !got[key] {
			t.Errorf("Config leaf %s has no entry in Settings", key)
		}
	}
	for key := range got {
		if !want[key] {
			t.Errorf("Settings entry %s does not match any Config leaf", key)
		}
	}
}

func TestSettingsRegistryWellFormed(t *testing.T) {
	t.Parallel()

	flagRE := regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	keys := map[string]bool{}
	envs := map[string]bool{}
	flags := map[string]bool{}
	def := Default()

	for _, s := range Settings {
		if keys[s.Key] {
			t.Errorf("duplicate Key %s", s.Key)
		}
		keys[s.Key] = true

		if envs[s.Env] {
			t.Errorf("duplicate Env %s", s.Env)
		}
		envs[s.Env] = true

		fl := s.Flag()
		if flags[fl] {
			t.Errorf("duplicate Flag --%s", fl)
		}
		flags[fl] = true

		if !strings.HasPrefix(s.Env, "KMS_") {
			t.Errorf("%s: Env %q must start with KMS_", s.Key, s.Env)
		}
		if !flagRE.MatchString(fl) {
			t.Errorf("%s: Flag %q must match %s", s.Key, fl, flagRE)
		}
		if s.Help == "" {
			t.Errorf("%s: Help must not be empty", s.Key)
		}
		if msg, panicked := recovered(func() { _ = s.Get(&def) }); panicked {
			t.Errorf("%s: Get panicked: %s", s.Key, msg)
		}
	}
}

func TestFlagNameDerivation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		env  string
		want string
	}{
		{"KMS_SQLITE_PATH", "sqlite-path"},
		{"KMS_VERIFY_DEFAULTS_MISMATCH_BUDGET_PER_HOUR", "verify-defaults-mismatch-budget-per-hour"},
		{"KMS_GRPC_ADDR", "grpc-addr"},
		{"KMS_KEK_FILE", "kek-file"},
		{"KMS_TLS_ENABLED", "tls-enabled"},
		{"KMS_METRICS_ENABLED", "metrics-enabled"},
		{"KMS_WATCH_RELEASE_SUBSCRIBER_RETAIN_DURATION", "watch-release-subscriber-retain-duration"},
		{"KMS_LOG_LEVEL", "log-level"},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Parallel()
			if got := (Setting{Env: tc.env}).Flag(); got != tc.want {
				t.Fatalf("Flag() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolvePrecedence pins the layering rule -- flag > env > file > default --
// and the provenance reported for each layer, once per value kind.
func TestResolvePrecedence(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		key         string
		file        string
		wantDefault string
		wantFile    string
		env         string
		wantEnv     string
		flagArgs    []string
		wantFlag    string
	}{
		{
			name:        "string",
			key:         "storage.sqlite_path",
			file:        "storage:\n  sqlite_path: \"/file.db\"\n",
			wantDefault: "./kms.db",
			wantFile:    "/file.db",
			env:         "/env.db",
			wantEnv:     "/env.db",
			flagArgs:    []string{"--sqlite-path=/flag.db"},
			wantFlag:    "/flag.db",
		},
		{
			name:        "int",
			key:         "server.verify_defaults.burst",
			file:        "server:\n  verify_defaults:\n    burst: 11\n",
			wantDefault: "10",
			wantFile:    "11",
			env:         "12",
			wantEnv:     "12",
			flagArgs:    []string{"--verify-defaults-burst=13"},
			wantFlag:    "13",
		},
		{
			name:        "bool",
			key:         "frontend.enabled",
			file:        "frontend:\n  enabled: false\n",
			wantDefault: "true",
			wantFile:    "false",
			env:         "true",
			wantEnv:     "true",
			flagArgs:    []string{"--frontend-enabled=false"},
			wantFlag:    "false",
		},
		{
			name:        "duration",
			key:         "watch.heartbeat_interval",
			file:        "watch:\n  heartbeat_interval: \"45s\"\n",
			wantDefault: "30s",
			wantFile:    "45s",
			env:         "50s",
			wantEnv:     "50s",
			flagArgs:    []string{"--watch-heartbeat-interval=55s"},
			wantFlag:    "55s",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := mustLookup(t, tc.key)
			path := writeConfigFile(t, tc.file)
			env := envMap(map[string]string{s.Env: tc.env})

			check := func(t *testing.T, cfg Config, prov Provenance, wantValue, wantSource string) {
				t.Helper()
				if got := s.Get(&cfg); got != wantValue {
					t.Errorf("%s = %q, want %q", tc.key, got, wantValue)
				}
				if got := prov[tc.key].String(); got != wantSource {
					t.Errorf("provenance = %q, want %q", got, wantSource)
				}
			}

			t.Run("default", func(t *testing.T) {
				cfg, prov := mustResolve(t, Options{LookupEnv: noEnv})
				check(t, cfg, prov, tc.wantDefault, "default")
			})

			t.Run("file", func(t *testing.T) {
				cfg, prov := mustResolve(t, Options{Path: path, LookupEnv: noEnv})
				check(t, cfg, prov, tc.wantFile, "file "+tc.key)
			})

			t.Run("env beats file", func(t *testing.T) {
				cfg, prov := mustResolve(t, Options{Path: path, LookupEnv: env})
				check(t, cfg, prov, tc.wantEnv, "env "+s.Env)
			})

			t.Run("flag beats env", func(t *testing.T) {
				fs := newFlagSet(t)
				b := AddFlags(fs, tc.key)
				if err := fs.Parse(tc.flagArgs); err != nil {
					t.Fatalf("Parse(%q): %v", tc.flagArgs, err)
				}
				cfg, prov := mustResolve(t, Options{Path: path, LookupEnv: env, Flags: b})
				check(t, cfg, prov, tc.wantFlag, "flag --"+s.Flag())
			})
		})
	}
}

// TestResolveExplicitEmptyFlagBeatsEnv covers the case a naive "non-zero wins"
// merge gets wrong: --kek-file= is an explicit request for passphrase mode and
// must override an inherited KMS_KEK_FILE.
func TestResolveExplicitEmptyFlagBeatsEnv(t *testing.T) {
	t.Parallel()

	fs := newFlagSet(t)
	b := AddFlags(fs, "encryption.kek_file")
	if err := fs.Parse([]string{"--kek-file="}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cfg, prov := mustResolve(t, Options{
		Flags:     b,
		LookupEnv: envMap(map[string]string{"KMS_KEK_FILE": "/k"}),
	})
	if cfg.Encryption.KEKFile != "" {
		t.Errorf("kek_file = %q, want empty", cfg.Encryption.KEKFile)
	}
	if got := prov["encryption.kek_file"].String(); got != "flag --kek-file" {
		t.Errorf("provenance = %q, want %q", got, "flag --kek-file")
	}
}

// TestResolveFlagVisitAcrossMultipleParses guards the assumption in applyFlags
// that FlagSet.Visit reports the union of every Parse call, not just the last.
func TestResolveFlagVisitAcrossMultipleParses(t *testing.T) {
	t.Parallel()

	fs := newFlagSet(t)
	b := AddFlags(fs)
	if err := fs.Parse([]string{"--sqlite-path", "a"}); err != nil {
		t.Fatalf("first Parse: %v", err)
	}
	if err := fs.Parse([]string{"--frontend-enabled=false"}); err != nil {
		t.Fatalf("second Parse: %v", err)
	}

	cfg, prov := mustResolve(t, Options{Flags: b, LookupEnv: noEnv})
	if cfg.Storage.SQLitePath != "a" {
		t.Errorf("sqlite_path = %q, want %q", cfg.Storage.SQLitePath, "a")
	}
	if cfg.Frontend.Enabled {
		t.Errorf("frontend.enabled = true, want false")
	}
	if got := prov["storage.sqlite_path"].String(); got != "flag --sqlite-path" {
		t.Errorf("sqlite_path provenance = %q", got)
	}
	if got := prov["frontend.enabled"].String(); got != "flag --frontend-enabled" {
		t.Errorf("frontend.enabled provenance = %q", got)
	}
}

// TestResolveEmptySetEnvApplies: a set-but-empty variable is a real value, not
// an absent one, so "KMS_LOG_LEVEL=" clears the level rather than being ignored.
func TestResolveEmptySetEnvApplies(t *testing.T) {
	t.Parallel()

	cfg, prov := mustResolve(t, Options{LookupEnv: envMap(map[string]string{"KMS_LOG_LEVEL": ""})})
	if cfg.Log.Level != "" {
		t.Errorf("log.level = %q, want empty", cfg.Log.Level)
	}
	if got := prov["log.level"].String(); got != "env KMS_LOG_LEVEL" {
		t.Errorf("provenance = %q, want %q", got, "env KMS_LOG_LEVEL")
	}
}

// TestResolveUnknownKey: a typo must be reported with its line and a suggestion
// rather than silently leaving the default in place.
func TestResolveUnknownKey(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		yaml     string
		contains []string
		absent   []string
	}{
		{
			name:     "nested typo",
			yaml:     "storage:\n  sqlite_pth: x\n",
			contains: []string{":2: unknown key storage.sqlite_pth", "did you mean storage.sqlite_path"},
		},
		{
			name:     "deeply nested typo",
			yaml:     "server:\n  verify_defaults:\n    burts: 1\n",
			contains: []string{":3: unknown key server.verify_defaults.burts", "did you mean server.verify_defaults.burst"},
		},
		{
			name:     "unknown top-level section",
			yaml:     "foo: 1\n",
			contains: []string{":1: unknown key foo"},
			absent:   []string{"did you mean"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := writeConfigFile(t, tc.yaml)
			_, _, err := Resolve(Options{Path: path, LookupEnv: noEnv})
			if err == nil {
				t.Fatalf("Resolve: want error for %q", tc.yaml)
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, path+":") {
				t.Errorf("error should start with the file path and line: %q", msg)
			}
			for _, want := range tc.contains {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q does not contain %q", msg, want)
				}
			}
			for _, bad := range tc.absent {
				if strings.Contains(msg, bad) {
					t.Errorf("error %q should not contain %q", msg, bad)
				}
			}
		})
	}
}

// TestResolveEmptySectionTolerated: a section header with nothing under it is a
// common way to sketch a config; it must leave the section's defaults intact.
func TestResolveEmptySectionTolerated(t *testing.T) {
	t.Parallel()

	path := writeConfigFile(t, "watch:\nserver:\n  grpc_addr: x\n")
	cfg, prov := mustResolve(t, Options{Path: path, LookupEnv: noEnv})

	if cfg.Server.GRPCAddr != "x" {
		t.Errorf("grpc_addr = %q, want %q", cfg.Server.GRPCAddr, "x")
	}
	if got := prov["server.grpc_addr"].String(); got != "file server.grpc_addr" {
		t.Errorf("grpc_addr provenance = %q", got)
	}
	if got := time.Duration(cfg.Watch.HeartbeatInterval); got != 30*time.Second {
		t.Errorf("heartbeat_interval = %v, want the 30s default", got)
	}
	for _, key := range SortedKeys() {
		if !strings.HasPrefix(key, "watch.") {
			continue
		}
		if got := prov[key].String(); got != "default" {
			t.Errorf("%s provenance = %q, want %q", key, got, "default")
		}
	}
}

func TestResolveSectionMustBeMapping(t *testing.T) {
	t.Parallel()

	path := writeConfigFile(t, "watch: 5\n")
	_, _, err := Resolve(Options{Path: path, LookupEnv: noEnv})
	if err == nil || !strings.Contains(err.Error(), "watch must be a mapping") {
		t.Fatalf("err = %v, want %q", err, "watch must be a mapping")
	}
}

func TestResolveEmptyFile(t *testing.T) {
	t.Parallel()

	t.Run("zero bytes yields defaults", func(t *testing.T) {
		t.Parallel()
		path := writeConfigFile(t, "")
		cfg, prov := mustResolve(t, Options{Path: path, LookupEnv: noEnv})
		if cfg != Default() {
			t.Errorf("cfg = %+v, want defaults", cfg)
		}
		for _, key := range SortedKeys() {
			if got := prov[key].String(); got != "default" {
				t.Errorf("%s provenance = %q, want %q", key, got, "default")
			}
		}
	})

	t.Run("missing file is an error", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "absent.yaml")
		_, _, err := Resolve(Options{Path: path, LookupEnv: noEnv})
		if err == nil || !strings.Contains(err.Error(), "reading config file") {
			t.Fatalf("err = %v, want a %q error", err, "reading config file")
		}
	})
}

// TestDurationParityFlagEnvYAML: a duration must mean the same thing whichever
// layer supplies it, in both the "90m" and bare-seconds spellings.
func TestDurationParityFlagEnvYAML(t *testing.T) {
	t.Parallel()

	const key = "watch.retain_duration"
	want := 90 * time.Minute

	for _, spelling := range []struct {
		name string
		raw  string
	}{
		{"duration string", "90m"},
		{"bare seconds", "5400"},
	} {
		t.Run(spelling.name, func(t *testing.T) {
			t.Parallel()

			t.Run("yaml", func(t *testing.T) {
				t.Parallel()
				path := writeConfigFile(t, "watch:\n  retain_duration: "+spelling.raw+"\n")
				cfg, prov := mustResolve(t, Options{Path: path, LookupEnv: noEnv})
				if got := time.Duration(cfg.Watch.RetainDuration); got != want {
					t.Errorf("retain_duration = %v, want %v", got, want)
				}
				if got := prov[key].String(); got != "file "+key {
					t.Errorf("provenance = %q", got)
				}
			})

			t.Run("env", func(t *testing.T) {
				t.Parallel()
				cfg, prov := mustResolve(t, Options{
					LookupEnv: envMap(map[string]string{"KMS_WATCH_RETAIN_DURATION": spelling.raw}),
				})
				if got := time.Duration(cfg.Watch.RetainDuration); got != want {
					t.Errorf("retain_duration = %v, want %v", got, want)
				}
				if got := prov[key].String(); got != "env KMS_WATCH_RETAIN_DURATION" {
					t.Errorf("provenance = %q", got)
				}
			})

			t.Run("flag", func(t *testing.T) {
				t.Parallel()
				fs := newFlagSet(t)
				b := AddFlags(fs, key)
				if err := fs.Parse([]string{"--watch-retain-duration=" + spelling.raw}); err != nil {
					t.Fatalf("Parse: %v", err)
				}
				cfg, prov := mustResolve(t, Options{Flags: b, LookupEnv: noEnv})
				if got := time.Duration(cfg.Watch.RetainDuration); got != want {
					t.Errorf("retain_duration = %v, want %v", got, want)
				}
				if got := prov[key].String(); got != "flag --watch-retain-duration" {
					t.Errorf("provenance = %q", got)
				}
			})
		})
	}

	t.Run("garbage is rejected", func(t *testing.T) {
		t.Parallel()
		_, _, err := Resolve(Options{
			LookupEnv: envMap(map[string]string{"KMS_WATCH_RETAIN_DURATION": "soon"}),
		})
		const want = `KMS_WATCH_RETAIN_DURATION="soon" is not a valid duration`
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to contain %q", err, want)
		}
	})
}

// TestResolveMalformedEnvErrors: a bad value is a hard error naming the
// variable, so an operator who writes KMS_TLS_ENABLED=yes is told rather than
// unknowingly running without TLS.
func TestResolveMalformedEnvErrors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		env  string
		val  string
		want string
	}{
		{
			name: "int",
			env:  "KMS_VERIFY_DEFAULTS_BURST",
			val:  "lots",
			want: `KMS_VERIFY_DEFAULTS_BURST="lots" is not a valid integer`,
		},
		{
			name: "bool",
			env:  "KMS_TLS_ENABLED",
			val:  "yes",
			want: `KMS_TLS_ENABLED="yes" is not a valid boolean (use true/false/1/0)`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := Resolve(Options{LookupEnv: envMap(map[string]string{tc.env: tc.val})})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestAddFlagsHelpShowsBuiltinDefaults: help output documents the built-in
// default, not whatever the current environment happens to say, so `--help` is
// stable and reproducible across machines.
func TestAddFlagsHelpShowsBuiltinDefaults(t *testing.T) {
	t.Setenv("KMS_SQLITE_PATH", "/elsewhere")

	fs := newFlagSet(t)
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	AddFlags(fs)
	fs.PrintDefaults()
	out := buf.String()

	if !strings.Contains(out, "./kms.db") {
		t.Errorf("help should show the built-in default ./kms.db:\n%s", out)
	}
	if strings.Contains(out, "/elsewhere") {
		t.Errorf("help must not leak the environment value:\n%s", out)
	}
	// The flag package prints this when String() panics on a zero Value.
	if strings.Contains(out, "panic") {
		t.Errorf("help output reports a panic:\n%s", out)
	}
	if !strings.Contains(out, "(env KMS_SQLITE_PATH, config storage.sqlite_path)") {
		t.Errorf("help should name the env var and YAML key:\n%s", out)
	}
	// Booleans take no operand; other kinds name their placeholder.
	if !strings.Contains(out, "  -frontend-enabled\n") {
		t.Errorf("bool flag should have no placeholder:\n%s", out)
	}
	if !strings.Contains(out, "  -sqlite-path path\n") {
		t.Errorf("string flag should show its back-quoted placeholder:\n%s", out)
	}
}

func TestAddFlagsUnknownKeyPanics(t *testing.T) {
	t.Parallel()

	msg, panicked := recovered(func() { AddFlags(newFlagSet(t), "storage.sqlite_pth") })
	if !panicked {
		t.Fatalf("AddFlags with an unknown key should panic")
	}
	if !strings.Contains(msg, "unknown setting storage.sqlite_pth") {
		t.Errorf("panic = %q, want it to name the unknown setting", msg)
	}
}

func TestBoundSettingLookup(t *testing.T) {
	t.Parallel()

	b := AddFlags(newFlagSet(t), "storage.sqlite_path")
	s, ok := b.Setting("sqlite-path")
	if !ok {
		t.Fatalf("Setting(%q): not found", "sqlite-path")
	}
	if s.Key != "storage.sqlite_path" {
		t.Errorf("Setting(%q).Key = %q", "sqlite-path", s.Key)
	}
	if _, ok := b.Setting("nope"); ok {
		t.Errorf("Setting(%q): want not found", "nope")
	}

	var nilBound *Bound
	if _, ok := nilBound.Setting("sqlite-path"); ok {
		t.Errorf("nil *Bound should report not found")
	}
}

func TestLoadAndApplyEnvStillWork(t *testing.T) {
	t.Run("Load with no path yields defaults", func(t *testing.T) {
		for _, name := range EnvNames() {
			if _, ok := os.LookupEnv(name); ok {
				t.Skipf("%s is set in the environment; this case needs a clean one", name)
			}
		}
		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg != Default() {
			t.Fatalf("Load(\"\") = %+v, want %+v", cfg, Default())
		}
	})

	t.Run("ApplyEnv reads the process environment", func(t *testing.T) {
		t.Setenv("KMS_HTTP_ADDR", "1.2.3.4:1")
		cfg := Default()
		if err := cfg.ApplyEnv(); err != nil {
			t.Fatalf("ApplyEnv: %v", err)
		}
		if cfg.Server.HTTPAddr != "1.2.3.4:1" {
			t.Errorf("http_addr = %q, want %q", cfg.Server.HTTPAddr, "1.2.3.4:1")
		}
	})
}

func TestSortedKeysAndEnvNames(t *testing.T) {
	t.Parallel()

	keys := SortedKeys()
	if len(keys) != len(Settings) {
		t.Fatalf("SortedKeys() has %d entries, want %d", len(keys), len(Settings))
	}
	for i, s := range Settings {
		if keys[i] != s.Key {
			t.Errorf("SortedKeys()[%d] = %q, want registry order %q", i, keys[i], s.Key)
		}
	}

	names := EnvNames()
	if len(names) != len(Settings) {
		t.Fatalf("EnvNames() has %d entries, want %d", len(names), len(Settings))
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("EnvNames() is not sorted: %q", names)
	}
	want := make([]string, 0, len(Settings))
	for _, s := range Settings {
		want = append(want, s.Env)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(names, want) {
		t.Errorf("EnvNames() = %q, want %q", names, want)
	}
}

// TestAdminRequireClientCertDefaultsOn pins the security-relevant default: the
// admin client-certificate requirement is on unless an operator turns it off,
// and it can be turned off through either the environment or the flag. A
// regression here (a Default() that forgets the Security block, say) would
// silently downgrade every deployment to token-only admin auth.
func TestAdminRequireClientCertDefaultsOn(t *testing.T) {
	t.Parallel()

	const key = "security.admin_require_client_cert"
	s := mustLookup(t, key)
	if s.Env != "KMS_ADMIN_REQUIRE_CLIENT_CERT" {
		t.Errorf("Env = %q, want KMS_ADMIN_REQUIRE_CLIENT_CERT", s.Env)
	}
	if s.Flag() != "admin-require-client-cert" {
		t.Errorf("Flag() = %q, want admin-require-client-cert", s.Flag())
	}

	t.Run("default", func(t *testing.T) {
		t.Parallel()
		cfg, prov := mustResolve(t, Options{LookupEnv: noEnv})
		if !cfg.Security.AdminRequireClientCert {
			t.Errorf("AdminRequireClientCert = false, want true by default")
		}
		if got := prov[key].String(); got != "default" {
			t.Errorf("provenance = %q, want default", got)
		}
	})

	t.Run("env disables", func(t *testing.T) {
		t.Parallel()
		env := envMap(map[string]string{s.Env: "false"})
		cfg, prov := mustResolve(t, Options{LookupEnv: env})
		if cfg.Security.AdminRequireClientCert {
			t.Errorf("AdminRequireClientCert = true, want false from %s", s.Env)
		}
		if got, want := prov[key].String(), "env "+s.Env; got != want {
			t.Errorf("provenance = %q, want %q", got, want)
		}
	})

	t.Run("file disables", func(t *testing.T) {
		t.Parallel()
		path := writeConfigFile(t, "security:\n  admin_require_client_cert: false\n")
		cfg, prov := mustResolve(t, Options{Path: path, LookupEnv: noEnv})
		if cfg.Security.AdminRequireClientCert {
			t.Errorf("AdminRequireClientCert = true, want false from the config file")
		}
		if got, want := prov[key].String(), "file "+key; got != want {
			t.Errorf("provenance = %q, want %q", got, want)
		}
	})

	t.Run("flag beats env", func(t *testing.T) {
		t.Parallel()
		// Env says on, the flag says off: the operator typing --...=false on the
		// command line must win over an inherited environment variable.
		env := envMap(map[string]string{s.Env: "true"})
		fs := newFlagSet(t)
		b := AddFlags(fs, key)
		if err := fs.Parse([]string{"--admin-require-client-cert=false"}); err != nil {
			t.Fatalf("Parse: %v", err)
		}
		cfg, prov := mustResolve(t, Options{LookupEnv: env, Flags: b})
		if cfg.Security.AdminRequireClientCert {
			t.Errorf("AdminRequireClientCert = true, want false from the flag")
		}
		if got, want := prov[key].String(), "flag --"+s.Flag(); got != want {
			t.Errorf("provenance = %q, want %q", got, want)
		}
	})
}
