// Package cli implements the parameter-store command-line interface: the
// serve daemon plus administrative and convenience subcommands. Commands use
// only the standard library flag package (no third-party command framework).
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Suhaibinator/kms/internal/config"
)

// Version is the build version, overridable at link time with
// -ldflags "-X github.com/Suhaibinator/kms/internal/cli.Version=...".
var Version = "dev"

// CLI carries the process I/O streams so commands are testable with buffers.
type CLI struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  *os.File
	// ConfigPath is the config file path from the global --config flag or the
	// KMS_CONFIG environment variable; ConfigPathSource records which. Every
	// config-aware command (serve, the offline database commands, config)
	// resolves its settings from this file.
	ConfigPath       string
	ConfigPathSource string
	// lookupEnv reads environment variables; nil means os.LookupEnv. Tests
	// inject a map so command behaviour never depends on the developer's shell.
	lookupEnv func(key string) (string, bool)
	// stopServe, when non-nil, ends a running `serve` as a shutdown signal
	// would. Tests use it to run the real server wiring in-process; production
	// leaves it nil (a nil channel never fires).
	stopServe <-chan struct{}
	// positionals holds the non-flag arguments collected by the most recent
	// parseFlags call (flags may be interspersed with positionals).
	positionals []string
	// helpRequested records flag.ErrHelp from a nested command. Command handlers
	// keep their existing parse-error return path; Run translates this one case
	// to a successful process exit after the flag package prints command help.
	helpRequested bool
	// dialOverride is replaced by command tests with an in-memory gRPC
	// transport. Production callers leave it nil and use connFlags.dial.
	dialOverride dialFunc
}

// New builds a CLI bound to the process standard streams.
func New() *CLI {
	return &CLI{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin}
}

// Run dispatches a subcommand and returns the process exit code.
func (c *CLI) Run(args []string) int {
	c.ConfigPath, c.ConfigPathSource = "", ""
	if v, ok := c.env("KMS_CONFIG"); ok && v != "" {
		c.ConfigPath, c.ConfigPathSource = v, "env KMS_CONFIG"
	}
	c.helpRequested = false
	rest := c.consumeGlobalFlags(args)
	if len(rest) == 0 {
		c.usage()
		return 2
	}
	cmd, cmdArgs := rest[0], rest[1:]

	var code int
	switch cmd {
	case "serve":
		code = c.cmdServe(cmdArgs)
	case "config":
		code = c.cmdConfig(cmdArgs)
	case "init":
		code = c.cmdInit(cmdArgs)
	case "migrate":
		code = c.cmdMigrate(cmdArgs)
	case "check":
		code = c.cmdCheck(cmdArgs)
	case "backup":
		code = c.cmdBackup(cmdArgs)
	case "restore":
		code = c.cmdRestore(cmdArgs)
	case "create-admin":
		code = c.cmdCreateAdmin(cmdArgs)
	case "rotate-admin":
		code = c.cmdRotateAdmin(cmdArgs)
	case "admin-cert":
		code = c.cmdAdminCert(cmdArgs)
	case "rotate-kek":
		code = c.cmdRotateKEK(cmdArgs)
	case "admin":
		code = c.cmdAdmin(cmdArgs)
	case "import":
		code = c.cmdImport(cmdArgs)
	case "put-secret":
		code = c.cmdPutSecret(cmdArgs)
	case "get-secret":
		code = c.cmdGetSecret(cmdArgs)
	case "put-parameter":
		code = c.cmdPutParameter(cmdArgs)
	case "list":
		code = c.cmdList(cmdArgs)
	case "release":
		code = c.cmdRelease(cmdArgs)
	case "defaults":
		code = c.cmdDefaults(cmdArgs)
	case "version", "--version", "-version":
		_, _ = fmt.Fprintln(c.Stdout, Version)
		code = 0
	case "help", "-h", "--help":
		c.usage()
		code = 0
	default:
		_, _ = fmt.Fprintf(c.Stderr, "unknown command %q\n\n", cmd)
		c.usage()
		code = 2
	}
	if c.helpRequested {
		return 0
	}
	return code
}

// consumeGlobalFlags extracts a leading --config before the subcommand so both
// `parameter-store --config x serve` and `parameter-store serve --config x`
// work. Parsing stops at the first token that is not a recognized global flag.
func (c *CLI) consumeGlobalFlags(args []string) []string {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--config" || a == "-config":
			if i+1 >= len(args) {
				_, _ = fmt.Fprintln(c.Stderr, "--config requires a value")
				return nil
			}
			c.ConfigPath, c.ConfigPathSource = args[i+1], "flag --config"
			i += 2
		case strings.HasPrefix(a, "--config="):
			c.ConfigPath, c.ConfigPathSource = strings.TrimPrefix(a, "--config="), "flag --config"
			i++
		case strings.HasPrefix(a, "-config="):
			c.ConfigPath, c.ConfigPathSource = strings.TrimPrefix(a, "-config="), "flag --config"
			i++
		default:
			return args[i:]
		}
	}
	return args[i:]
}

func (c *CLI) usage() {
	_, _ = fmt.Fprint(c.Stderr, `parameter-store — parameter and secret management service

Usage:
  parameter-store [--config FILE] <command> [flags]

Server:
  serve            Run the gRPC + HTTP server.
  config show      Print the effective configuration and where each value came from.
  config validate  Check the configuration file, environment, and flags.

Administration:
  init             Create/migrate a database and master key.
  migrate          Apply pending database migrations.
  check            Verify a database and (optionally) the master key.
  backup           Write a consistent online database backup.
  restore          Restore a database file (server must be stopped).
  create-admin     Create an admin identity and print its token once (--cert-dir also
                   issues its client certificate).
  rotate-admin     Recover an existing admin by rotating its token directly.
  admin-cert       Issue, list, or revoke admin client certificates offline (no server needed).
  rotate-kek       Rotate the master key, rewrapping all secrets.
  import           Import data from SuhaibParameterStore.

Application onboarding (talk to a running server over gRPC):
  Prerequisite: create ./certs and restrict directory access to its owner.
                   This directory receives one-time application credentials.
  admin namespace create --env ENV --app APP --auth-methods mtls
                   Create the application's namespace and allow mTLS.
  admin identity create NAME --namespace ENV/APP --auth mtls --out ./certs
                   Create NAME.crt and NAME.key for the application.

Management (talk to a running server over gRPC):
  admin            Manage namespaces, application identities, policies, and client certificates.

Convenience (talk to a running server over gRPC):
  put-secret /env/app/key       Store a secret (value from --value-file or stdin).
  get-secret /env/app/key       Fetch a secret (requires --show, --out, or a pipe).
  put-parameter /env/app/key V  Store a parameter value.
  list env/app                  List parameters and secrets in a namespace (--prefix).
  release                       Manage configuration releases and schemas.
  defaults                      Preview or apply generated application defaults.

Other:
  version          Print the build version.
  help             Show this help.

Global flags:
  --config FILE    Config file path (env KMS_CONFIG). Read by serve, config,
                   and every command that opens the database directly.

Settings resolve in this order: flag, then KMS_* environment variable, then the
config file, then the built-in default. Commands that talk to a running server
read KMS_ENDPOINT, KMS_TOKEN, KMS_CA_FILE, KMS_CLIENT_CERT_FILE, and
KMS_CLIENT_KEY_FILE as flag defaults.

Run "parameter-store <command> -h" for command-specific flags.
`)
}

// fail prints an error to stderr and returns exit code 1.
func (c *CLI) fail(format string, args ...any) int {
	_, _ = fmt.Fprintf(c.Stderr, "error: "+format+"\n", args...)
	return 1
}

// newFlags builds a flag set that writes usage to the CLI's stderr and returns
// an error rather than exiting the process on a parse problem.
func (c *CLI) newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(c.Stderr)
	return fs
}

// parseFlags parses args, returning false when the caller should exit through
// the command's normal usage path. Run maps flag.ErrHelp to exit code 0 while
// malformed flags retain the command's non-zero usage exit.
//
// It supports flags interspersed with positional arguments (the standard flag
// package stops at the first positional, which would silently drop a trailing
// "--endpoint x" in "put-secret /path --endpoint x" — dangerous in a secrets
// CLI). A "--" terminator ends flag parsing: everything after it is a
// positional verbatim, even if it begins with "-" (so a value like "-5" or a
// PEM block can be passed).
func (c *CLI) parseFlags(fs *flag.FlagSet, args []string) bool {
	// Peel off an explicit "--" terminator first, before any flag parsing, so
	// its trailing arguments are never re-interpreted as flags.
	var trailing []string
	for i, a := range args {
		if a == "--" {
			args, trailing = args[:i], args[i+1:]
			break
		}
	}

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			c.helpRequested = true
		}
		return false
	}
	positionals := []string{}
	for {
		rest := fs.Args()
		i := 0
		// Collect leading positionals. A lone "-" (the conventional stdin/stdout
		// token) is a positional, not a flag: treat it as one so the loop makes
		// progress — flag.Parse leaves "-" unconsumed, which would otherwise spin
		// here forever.
		for i < len(rest) && (!strings.HasPrefix(rest[i], "-") || rest[i] == "-") {
			positionals = append(positionals, rest[i])
			i++
		}
		if i == len(rest) {
			break
		}
		before := len(rest) - i
		if err := fs.Parse(rest[i:]); err != nil {
			if err == flag.ErrHelp {
				c.helpRequested = true
			}
			return false
		}
		// Defensive: if a re-parse consumed nothing, we'd loop forever. Bail
		// rather than hang. (Reachable only if flag.Parse leaves an unconsumed
		// leading token that isn't a positional we handle above.)
		if len(fs.Args()) >= before {
			return false
		}
	}
	c.positionals = append(positionals, trailing...)
	return true
}

// args returns the positional arguments collected by parseFlags (flags may be
// interspersed before or after them).
func (c *CLI) args() []string { return c.positionals }

// env reads an environment variable through the CLI's injectable lookup.
func (c *CLI) env(key string) (string, bool) {
	if c.lookupEnv != nil {
		return c.lookupEnv(key)
	}
	return os.LookupEnv(key)
}

// rejectPositionals fails a command that takes no positional arguments when
// parseFlags collected any. Its main job is catching "--flag false" for a
// boolean flag: the flag package sets the flag to true and leaves "false" as a
// positional, which would otherwise be ignored silently.
func (c *CLI) rejectPositionals() bool {
	if len(c.positionals) == 0 {
		return true
	}
	_, _ = fmt.Fprintf(c.Stderr, "error: unexpected argument %q (boolean flags take the form --flag=false)\n", c.positionals[0])
	return false
}

// settingsResolver ties a command's FlagSet to the config registry. Build it
// with serverSettings before parseFlags and call resolve afterwards.
type settingsResolver struct {
	c          *CLI
	bound      *config.Bound
	configPath *string
}

// serverSettings registers --config plus a flag for each named config setting
// (all of them when keys is empty) on fs, so the command participates in the
// standard resolution order: flag > environment > config file > default.
func (c *CLI) serverSettings(fs *flag.FlagSet, keys ...string) *settingsResolver {
	r := &settingsResolver{c: c}
	r.configPath = fs.String("config", "", "config `file` path (env KMS_CONFIG)")
	r.bound = config.AddFlags(fs, keys...)
	return r
}

// resolve returns the effective configuration, the source of every setting,
// and the config file path that was read ("" when none). Malformed environment
// variables and unknown config keys are errors. It does not run
// Config.Validate; serve and config validate do that themselves because it
// stats TLS material that offline commands have no business requiring.
func (r *settingsResolver) resolve() (config.Config, config.Provenance, string, error) {
	path := *r.configPath
	if path == "" {
		path = r.c.ConfigPath
	}
	cfg, prov, err := config.Resolve(config.Options{Path: path, Flags: r.bound, LookupEnv: r.c.env})
	return cfg, prov, path, err
}

// configFileSource describes where the resolved config path came from, for
// output such as "config file: /etc/x.yaml (flag --config)".
func (r *settingsResolver) configFileSource() string {
	if *r.configPath != "" {
		return "flag --config"
	}
	return r.c.ConfigPathSource
}

// setUsage installs a structured help renderer on fs: a usage line, a short
// description, the flag table, and (for commands that read server settings)
// a footer explaining the resolution order. It replaces the flag package's
// bare default listing.
func (c *CLI) setUsage(fs *flag.FlagSet, synopsis, description string, settingsFooter bool) {
	fs.Usage = func() {
		w := c.Stderr
		_, _ = fmt.Fprintf(w, "Usage: parameter-store %s\n", synopsis)
		if description != "" {
			_, _ = fmt.Fprintf(w, "\n%s\n", wrapText(description, 78, ""))
		}
		_, _ = fmt.Fprintln(w, "\nFlags:")
		printFlagTable(w, fs)
		if settingsFooter {
			_, _ = fmt.Fprint(w, settingsFooterText)
		}
	}
}

const settingsFooterText = `
Settings resolve in this order: flag, then environment variable, then the
config file (--config or KMS_CONFIG), then the built-in default. A malformed
KMS_* value or an unknown key in the config file is an error. Run
"parameter-store config show" to print the effective configuration and where
each value came from.
`

// printFlagTable renders every flag on fs as "--name PLACEHOLDER" followed by
// its usage text, default value, and (for boolean flags) the =false form.
func printFlagTable(w io.Writer, fs *flag.FlagSet) {
	type row struct{ head, body string }
	var rows []row
	width := 0
	fs.VisitAll(func(f *flag.Flag) {
		name, usage := flag.UnquoteUsage(f)
		head := "--" + f.Name
		if isBoolFlag(f) {
			head += "[=true|false]"
		} else if name != "" {
			head += " " + strings.ToUpper(name)
		}
		if f.DefValue != "" && f.DefValue != "false" && !isBoolFlag(f) {
			usage += fmt.Sprintf(" (default %q)", f.DefValue)
		} else if isBoolFlag(f) && f.DefValue == "true" {
			usage += " (default true)"
		}
		rows = append(rows, row{head, usage})
		if len(head) > width {
			width = len(head)
		}
	})
	if width > 34 {
		width = 34
	}
	for _, r := range rows {
		if len(r.head) > width {
			_, _ = fmt.Fprintf(w, "  %s\n  %s%s\n", r.head, strings.Repeat(" ", width+2), wrapText(r.body, 78-width-4, strings.Repeat(" ", width+4)))
			continue
		}
		_, _ = fmt.Fprintf(w, "  %-*s  %s\n", width, r.head, wrapText(r.body, 78-width-4, strings.Repeat(" ", width+4)))
	}
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// wrapText word-wraps s to width columns; continuation lines are prefixed
// with indent. The first line carries no prefix so callers can place it.
func wrapText(s string, width int, indent string) string {
	if width < 20 {
		width = 20
	}
	words := strings.Fields(s)
	var b strings.Builder
	line := 0
	for i, wd := range words {
		if i > 0 {
			if line+1+len(wd) > width {
				b.WriteString("\n" + indent)
				line = 0
			} else {
				b.WriteByte(' ')
				line++
			}
		}
		b.WriteString(wd)
		line += len(wd)
	}
	return b.String()
}
