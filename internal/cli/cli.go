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
)

// Version is the build version, overridable at link time with
// -ldflags "-X github.com/Suhaibinator/kms/internal/cli.Version=...".
var Version = "dev"

// CLI carries the process I/O streams so commands are testable with buffers.
type CLI struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  *os.File
	// ConfigPath is the global --config value (or KMS_CONFIG); used by serve.
	ConfigPath string
	// positionals holds the non-flag arguments collected by the most recent
	// parseFlags call (flags may be interspersed with positionals).
	positionals []string
}

// New builds a CLI bound to the process standard streams.
func New() *CLI {
	return &CLI{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin}
}

// Run dispatches a subcommand and returns the process exit code.
func (c *CLI) Run(args []string) int {
	c.ConfigPath = os.Getenv("KMS_CONFIG")
	rest := c.consumeGlobalFlags(args)
	if len(rest) == 0 {
		c.usage()
		return 2
	}
	cmd, cmdArgs := rest[0], rest[1:]

	switch cmd {
	case "serve":
		return c.cmdServe(cmdArgs)
	case "init":
		return c.cmdInit(cmdArgs)
	case "migrate":
		return c.cmdMigrate(cmdArgs)
	case "check":
		return c.cmdCheck(cmdArgs)
	case "backup":
		return c.cmdBackup(cmdArgs)
	case "restore":
		return c.cmdRestore(cmdArgs)
	case "create-admin":
		return c.cmdCreateAdmin(cmdArgs)
	case "rotate-kek":
		return c.cmdRotateKEK(cmdArgs)
	case "admin":
		return c.cmdAdmin(cmdArgs)
	case "import":
		return c.cmdImport(cmdArgs)
	case "put-secret":
		return c.cmdPutSecret(cmdArgs)
	case "get-secret":
		return c.cmdGetSecret(cmdArgs)
	case "put-parameter":
		return c.cmdPutParameter(cmdArgs)
	case "list":
		return c.cmdList(cmdArgs)
	case "version", "--version", "-version":
		_, _ = fmt.Fprintln(c.Stdout, Version)
		return 0
	case "help", "-h", "--help":
		c.usage()
		return 0
	default:
		_, _ = fmt.Fprintf(c.Stderr, "unknown command %q\n\n", cmd)
		c.usage()
		return 2
	}
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
			c.ConfigPath = args[i+1]
			i += 2
		case strings.HasPrefix(a, "--config="):
			c.ConfigPath = strings.TrimPrefix(a, "--config=")
			i++
		case strings.HasPrefix(a, "-config="):
			c.ConfigPath = strings.TrimPrefix(a, "-config=")
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

Administration:
  init             Create/migrate a database and master key.
  migrate          Apply pending database migrations.
  check            Verify a database and (optionally) the master key.
  backup           Write a consistent online database backup.
  restore          Restore a database file (server must be stopped).
  create-admin     Create an admin identity and print its token once.
  rotate-kek       Rotate the master key, rewrapping all secrets.
  import           Import data from SuhaibParameterStore.

Management (talk to a running server over gRPC):
  admin            Manage namespaces, identities, and the built-in CA.

Convenience (talk to a running server over gRPC):
  put-secret /env/app/key       Store a secret (value from --value-file or stdin).
  get-secret /env/app/key       Fetch a secret (requires --show, --out, or a pipe).
  put-parameter /env/app/key V  Store a parameter value.
  list env/app                  List parameters and secrets in a namespace (--prefix).

Other:
  version          Print the build version.
  help             Show this help.

Global flags:
  --config FILE    Config file path (env KMS_CONFIG). Used by serve.

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

// parseFlags parses args, returning false when the caller should exit with
// code 2 (help requested or bad flags).
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
