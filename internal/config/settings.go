package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Setting describes one leaf of Config and the three spellings by which it can
// be set: the section-qualified YAML key, the KMS_* environment variable, and
// the command-line flag (derived from the environment variable). The registry
// in Settings is the single source of truth for which keys exist; Resolve,
// AddFlags, and the CLI's help and `config show` output are all driven by it.
type Setting struct {
	// Key is the dotted YAML path, e.g. "storage.sqlite_path".
	Key string
	// Env is the environment variable, e.g. "KMS_SQLITE_PATH". Existing names
	// are a published contract (the container image sets them) and must not be
	// renamed.
	Env string
	// Help is the flag usage text. A back-quoted word names the placeholder in
	// help output, following the flag package's UnquoteUsage convention.
	Help string
	// ptr returns a pointer to the field inside cfg: *string, *int, *bool, or
	// *Duration. The value kind is derived from the pointer type.
	ptr func(cfg *Config) any
}

// Flag returns the command-line flag name, derived mechanically from Env:
// strip the KMS_ prefix, lowercase, and replace underscores with hyphens
// (KMS_SQLITE_PATH -> sqlite-path).
func (s Setting) Flag() string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(s.Env, "KMS_"), "_", "-"))
}

// IsBool reports whether the setting is a boolean, which affects flag syntax
// (--flag or --flag=false, never --flag false).
func (s Setting) IsBool() bool {
	_, ok := s.ptr(&Config{}).(*bool)
	return ok
}

// Get formats the setting's current value in cfg as a string, in the same
// form Set accepts.
func (s Setting) Get(cfg *Config) string {
	switch p := s.ptr(cfg).(type) {
	case *string:
		return *p
	case *int:
		return strconv.Itoa(*p)
	case *bool:
		return strconv.FormatBool(*p)
	case *Duration:
		return time.Duration(*p).String()
	default:
		panic(fmt.Sprintf("config: setting %s has unsupported type %T", s.Key, p))
	}
}

// Set parses raw and stores it into cfg. Integers and booleans are trimmed of
// surrounding whitespace; strings are stored verbatim; durations accept either
// a Go duration string ("30s", "24h") or a bare number of seconds, matching the
// YAML form. The returned error is a short phrase ("not a valid integer") so
// callers can prefix it with the environment variable or flag name.
func (s Setting) Set(cfg *Config, raw string) error {
	switch p := s.ptr(cfg).(type) {
	case *string:
		*p = raw
	case *int:
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return errors.New("not a valid integer")
		}
		*p = n
	case *bool:
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return errors.New("not a valid boolean (use true/false/1/0)")
		}
		*p = b
	case *Duration:
		d, err := parseDuration(strings.TrimSpace(raw))
		if err != nil {
			return err
		}
		*p = d
	default:
		panic(fmt.Sprintf("config: setting %s has unsupported type %T", s.Key, p))
	}
	return nil
}

func parseDuration(raw string) (Duration, error) {
	if d, err := time.ParseDuration(raw); err == nil {
		return Duration(d), nil
	}
	if secs, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return Duration(time.Duration(secs) * time.Second), nil
	}
	return 0, errors.New(`not a valid duration (use a value like "30s" or "24h", or a number of seconds)`)
}

// Settings is the registry of every configurable leaf in Config. Order here
// is the display order for help and `config show`.
var Settings = []Setting{
	{
		Key:  "server.grpc_addr",
		Env:  "KMS_GRPC_ADDR",
		Help: "gRPC listen `address` (host:port)",
		ptr:  func(c *Config) any { return &c.Server.GRPCAddr },
	},
	{
		Key:  "server.http_addr",
		Env:  "KMS_HTTP_ADDR",
		Help: "HTTP listen `address` (host:port)",
		ptr:  func(c *Config) any { return &c.Server.HTTPAddr },
	},
	{
		Key:  "server.verify_defaults.requests_per_hour",
		Env:  "KMS_VERIFY_DEFAULTS_REQUESTS_PER_HOUR",
		Help: "VerifyReleaseDefaults `requests` allowed per hour per identity",
		ptr:  func(c *Config) any { return &c.Server.VerifyDefaults.RequestsPerHour },
	},
	{
		Key:  "server.verify_defaults.burst",
		Env:  "KMS_VERIFY_DEFAULTS_BURST",
		Help: "VerifyReleaseDefaults burst `capacity` per identity",
		ptr:  func(c *Config) any { return &c.Server.VerifyDefaults.Burst },
	},
	{
		Key:  "server.verify_defaults.mismatch_budget_per_hour",
		Env:  "KMS_VERIFY_DEFAULTS_MISMATCH_BUDGET_PER_HOUR",
		Help: "VerifyReleaseDefaults mismatch `verdicts` allowed per hour per identity",
		ptr:  func(c *Config) any { return &c.Server.VerifyDefaults.MismatchBudgetPerHour },
	},
	{
		Key:  "storage.sqlite_path",
		Env:  "KMS_SQLITE_PATH",
		Help: "SQLite database file `path`",
		ptr:  func(c *Config) any { return &c.Storage.SQLitePath },
	},
	{
		Key:  "encryption.kek_file",
		Env:  "KMS_KEK_FILE",
		Help: "master key file `path`; empty selects passphrase mode",
		ptr:  func(c *Config) any { return &c.Encryption.KEKFile },
	},
	{
		Key:  "security.tls_enabled",
		Env:  "KMS_TLS_ENABLED",
		Help: "serve TLS on the gRPC and HTTP listeners",
		ptr:  func(c *Config) any { return &c.Security.TLSEnabled },
	},
	{
		Key:  "security.mtls_enabled",
		Env:  "KMS_MTLS_ENABLED",
		Help: "require and verify client certificates (implies tls_enabled)",
		ptr:  func(c *Config) any { return &c.Security.MTLSEnabled },
	},
	{
		Key:  "security.server_cert_file",
		Env:  "KMS_SERVER_CERT_FILE",
		Help: "server TLS certificate `path` (PEM)",
		ptr:  func(c *Config) any { return &c.Security.ServerCertFile },
	},
	{
		Key:  "security.server_key_file",
		Env:  "KMS_SERVER_KEY_FILE",
		Help: "server TLS private key `path` (PEM)",
		ptr:  func(c *Config) any { return &c.Security.ServerKeyFile },
	},
	{
		Key:  "security.client_ca_file",
		Env:  "KMS_CLIENT_CA_FILE",
		Help: "CA bundle `path` the server uses to verify client certificates (server-side; not the client's --ca)",
		ptr:  func(c *Config) any { return &c.Security.ClientCAFile },
	},
	{
		Key:  "security.trust_proxy_headers",
		Env:  "KMS_TRUST_PROXY_HEADERS",
		Help: "honor X-Forwarded-For for the HTTP client IP (only behind a trusted reverse proxy)",
		ptr:  func(c *Config) any { return &c.Security.TrustProxyHeaders },
	},
	{
		Key:  "security.admin_require_client_cert",
		Env:  "KMS_ADMIN_REQUIRE_CLIENT_CERT",
		Help: "require admin identities to present a built-in-CA client certificate in addition to a bearer token (relaxed with a warning while tls_enabled is false)",
		ptr:  func(c *Config) any { return &c.Security.AdminRequireClientCert },
	},
	{
		Key:  "frontend.enabled",
		Env:  "KMS_FRONTEND_ENABLED",
		Help: "serve the embedded web frontend",
		ptr:  func(c *Config) any { return &c.Frontend.Enabled },
	},
	{
		Key:  "audit.enabled",
		Env:  "KMS_AUDIT_ENABLED",
		Help: "record audit log entries",
		ptr:  func(c *Config) any { return &c.Audit.Enabled },
	},
	{
		Key:  "watch.heartbeat_interval",
		Env:  "KMS_WATCH_HEARTBEAT_INTERVAL",
		Help: "watch stream heartbeat `interval`",
		ptr:  func(c *Config) any { return &c.Watch.HeartbeatInterval },
	},
	{
		Key:  "watch.retain_duration",
		Env:  "KMS_WATCH_RETAIN_DURATION",
		Help: "`duration` to retain change-log rows",
		ptr:  func(c *Config) any { return &c.Watch.RetainDuration },
	},
	{
		Key:  "watch.retain_rows",
		Env:  "KMS_WATCH_RETAIN_ROWS",
		Help: "maximum `count` of change-log rows retained",
		ptr:  func(c *Config) any { return &c.Watch.RetainRows },
	},
	{
		Key:  "watch.release_retain_duration",
		Env:  "KMS_WATCH_RELEASE_RETAIN_DURATION",
		Help: "`duration` to retain superseded release versions",
		ptr:  func(c *Config) any { return &c.Watch.ReleaseRetainDuration },
	},
	{
		Key:  "watch.release_retain_versions",
		Env:  "KMS_WATCH_RELEASE_RETAIN_VERSIONS",
		Help: "maximum `count` of superseded release versions retained",
		ptr:  func(c *Config) any { return &c.Watch.ReleaseRetainVersions },
	},
	{
		Key:  "watch.release_subscriber_retain_duration",
		Env:  "KMS_WATCH_RELEASE_SUBSCRIBER_RETAIN_DURATION",
		Help: "`duration` to retain idle release subscriber records",
		ptr:  func(c *Config) any { return &c.Watch.ReleaseSubscriberRetainDuration },
	},
	{
		Key:  "log.level",
		Env:  "KMS_LOG_LEVEL",
		Help: "log `level`: debug, info, warn, or error",
		ptr:  func(c *Config) any { return &c.Log.Level },
	},
}

// Lookup returns the setting registered under the dotted YAML key.
func Lookup(key string) (Setting, bool) {
	for _, s := range Settings {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// SourceKind classifies where a resolved value came from.
type SourceKind int

const (
	// SourceDefault is the built-in default from Default().
	SourceDefault SourceKind = iota
	// SourceFile is the YAML configuration file.
	SourceFile
	// SourceEnv is a KMS_* environment variable.
	SourceEnv
	// SourceFlag is a command-line flag.
	SourceFlag
)

// Source records the origin of one resolved setting.
type Source struct {
	Kind SourceKind
	// Name is the YAML key, environment variable, or flag name (without
	// leading dashes) depending on Kind; empty for SourceDefault.
	Name string
}

// String renders the source for humans: "default", "file storage.sqlite_path",
// "env KMS_SQLITE_PATH", or "flag --sqlite-path".
func (s Source) String() string {
	switch s.Kind {
	case SourceFile:
		return "file " + s.Name
	case SourceEnv:
		return "env " + s.Name
	case SourceFlag:
		return "flag --" + s.Name
	default:
		return "default"
	}
}

// Provenance maps each Setting.Key to the source its resolved value came from.
type Provenance map[string]Source

// Bound is the set of settings flags registered on a FlagSet by AddFlags. Pass
// it to Resolve so explicitly set flags take precedence.
type Bound struct {
	fs     *flag.FlagSet
	byFlag map[string]Setting
}

// AddFlags registers a flag for each named setting (every setting when keys is
// empty) on fs. Flag names come from Setting.Flag; usage text is the setting's
// Help followed by its environment variable and YAML key; the displayed
// default is the built-in default from Default(), regardless of environment
// or config file, so help output is stable. Unknown keys are a programming
// error and panic.
func AddFlags(fs *flag.FlagSet, keys ...string) *Bound {
	var selected []Setting
	if len(keys) == 0 {
		selected = Settings
	} else {
		for _, k := range keys {
			s, ok := Lookup(k)
			if !ok {
				panic("config: AddFlags: unknown setting " + k)
			}
			selected = append(selected, s)
		}
	}
	b := &Bound{fs: fs, byFlag: make(map[string]Setting, len(selected))}
	for _, s := range selected {
		scratch := Default()
		v := &settingValue{setting: s, cfg: &scratch}
		usage := fmt.Sprintf("%s (env %s, config %s)", s.Help, s.Env, s.Key)
		fs.Var(v, s.Flag(), usage)
		b.byFlag[s.Flag()] = s
	}
	return b
}

// Setting returns the setting bound to the named flag, if any.
func (b *Bound) Setting(flagName string) (Setting, bool) {
	if b == nil {
		return Setting{}, false
	}
	s, ok := b.byFlag[flagName]
	return s, ok
}

// settingValue adapts a Setting to flag.Value. Parsed values land in a scratch
// Config; Resolve copies explicitly set flags onto the real Config afterwards
// so flag precedence is applied after file and environment.
type settingValue struct {
	setting Setting
	cfg     *Config
}

func (v *settingValue) String() string {
	// The flag package constructs a zero settingValue via reflection to learn
	// the zero-value rendering; it must not dereference nil.
	if v == nil || v.cfg == nil || v.setting.ptr == nil {
		return ""
	}
	return v.setting.Get(v.cfg)
}

func (v *settingValue) Set(raw string) error { return v.setting.Set(v.cfg, raw) }

// IsBoolFlag lets boolean settings be written as --flag (true) or --flag=false.
func (v *settingValue) IsBoolFlag() bool {
	return v != nil && v.setting.ptr != nil && v.setting.IsBool()
}

// Options controls Resolve.
type Options struct {
	// Path is the YAML config file; empty skips the file. Resolve never
	// consults KMS_CONFIG itself — the CLI decides the path.
	Path string
	// Flags, when non-nil, supplies explicitly set command-line flags (the
	// highest-precedence layer). The FlagSet must already be parsed.
	Flags *Bound
	// LookupEnv reads environment variables; nil means os.LookupEnv. Tests
	// inject a map-backed lookup so results do not depend on the shell.
	LookupEnv func(key string) (string, bool)
}

// Resolve builds the effective configuration by layering, lowest to highest
// precedence: built-in defaults, the YAML file, KMS_* environment variables,
// and explicitly set command-line flags. The returned Provenance records the
// winning source for every setting. Unknown YAML keys are an error (with the
// offending line) so typos cannot silently fall back to defaults. Resolve does
// not run semantic validation; call Config.Validate separately.
func Resolve(opts Options) (Config, Provenance, error) {
	cfg := Default()
	prov := make(Provenance, len(Settings))
	for _, s := range Settings {
		prov[s.Key] = Source{Kind: SourceDefault}
	}

	if opts.Path != "" {
		if err := applyFile(&cfg, opts.Path, prov); err != nil {
			return Config{}, nil, err
		}
	}

	lookup := opts.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if err := applyEnv(&cfg, lookup, prov); err != nil {
		return Config{}, nil, err
	}

	if opts.Flags != nil {
		if err := applyFlags(&cfg, opts.Flags, prov); err != nil {
			return Config{}, nil, err
		}
	}
	return cfg, prov, nil
}

// applyFile decodes the YAML file at path onto cfg, recording each present
// leaf in prov and rejecting keys that are not in the registry.
func applyFile(cfg *Config, path string, prov Provenance) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading config file %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parsing config file %s: %w", path, err)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return nil // empty file: nothing to apply
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("parsing config file %s: top level must be a mapping", path)
	}
	if err := walkYAML(root, "", path, prov); err != nil {
		return err
	}
	// Decode onto the defaults so unset fields keep their defaults.
	if err := doc.Decode(cfg); err != nil {
		return fmt.Errorf("parsing config file %s: %w", path, err)
	}
	return nil
}

// walkYAML records present leaves and rejects unknown keys. A known section
// with a null value (e.g. "watch:" with nothing beneath it) is tolerated.
func walkYAML(n *yaml.Node, prefix, path string, prov Provenance) error {
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Tag == "!!merge" {
			continue
		}
		key := k.Value
		if prefix != "" {
			key = prefix + "." + key
		}
		if v.Kind == yaml.AliasNode {
			v = v.Alias
		}
		switch {
		case v.Kind == yaml.MappingNode && isSection(key):
			if err := walkYAML(v, key, path, prov); err != nil {
				return err
			}
		case isSection(key):
			if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
				continue
			}
			return fmt.Errorf("%s:%d: %s must be a mapping", path, k.Line, key)
		default:
			if _, ok := Lookup(key); !ok {
				return fmt.Errorf("%s:%d: unknown key %s%s", path, k.Line, key, suggestKey(key))
			}
			prov[key] = Source{Kind: SourceFile, Name: key}
		}
	}
	return nil
}

// isSection reports whether key is a proper prefix of some registered key.
func isSection(key string) bool {
	p := key + "."
	for _, s := range Settings {
		if strings.HasPrefix(s.Key, p) {
			return true
		}
	}
	return false
}

// suggestKey offers a " (did you mean X?)" hint for near-miss keys.
func suggestKey(key string) string {
	best, bestDist := "", 3
	for _, s := range Settings {
		if d := editDistance(key, s.Key); d < bestDist {
			best, bestDist = s.Key, d
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf(" (did you mean %s?)", best)
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// applyEnv overlays environment variables onto cfg. Only variables that are
// set take effect (an empty-but-set variable applies as empty). A malformed
// value is a hard error rather than being silently ignored, so an operator
// who writes KMS_TLS_ENABLED=yes gets told instead of unknowingly running
// without TLS.
func applyEnv(cfg *Config, lookup func(string) (string, bool), prov Provenance) error {
	for _, s := range Settings {
		v, ok := lookup(s.Env)
		if !ok {
			continue
		}
		if err := s.Set(cfg, v); err != nil {
			return fmt.Errorf("%s=%q is %v", s.Env, v, err)
		}
		if prov != nil {
			prov[s.Key] = Source{Kind: SourceEnv, Name: s.Env}
		}
	}
	return nil
}

// applyFlags copies every explicitly set settings flag onto cfg. fs.Visit
// reports the union of flags set across all Parse calls on the FlagSet,
// including a flag explicitly set to its default value.
func applyFlags(cfg *Config, b *Bound, prov Provenance) error {
	var firstErr error
	b.fs.Visit(func(f *flag.Flag) {
		s, ok := b.byFlag[f.Name]
		if !ok || firstErr != nil {
			return
		}
		if err := s.Set(cfg, f.Value.String()); err != nil {
			firstErr = fmt.Errorf("--%s=%q is %v", f.Name, f.Value.String(), err)
			return
		}
		prov[s.Key] = Source{Kind: SourceFlag, Name: f.Name}
	})
	return firstErr
}

// SortedKeys returns the registered setting keys in registry order. It exists
// so callers rendering Provenance produce deterministic output without
// depending on map iteration.
func SortedKeys() []string {
	keys := make([]string, 0, len(Settings))
	for _, s := range Settings {
		keys = append(keys, s.Key)
	}
	return keys
}

// EnvNames returns every registered environment variable name, sorted.
func EnvNames() []string {
	names := make([]string, 0, len(Settings))
	for _, s := range Settings {
		names = append(names, s.Env)
	}
	sort.Strings(names)
	return names
}
