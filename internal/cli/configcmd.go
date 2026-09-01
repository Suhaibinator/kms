package cli

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/Suhaibinator/kms/internal/config"
)

// cmdConfig dispatches the `config` group: inspect and validate the server
// configuration as the other commands will resolve it.
func (c *CLI) cmdConfig(args []string) int {
	if len(args) == 0 {
		c.configUsage()
		return 2
	}
	switch args[0] {
	case "show":
		return c.cmdConfigShow(args[1:])
	case "validate":
		return c.cmdConfigValidate(args[1:])
	case "help", "-h", "--help":
		c.configUsage()
		return 0
	default:
		_, _ = fmt.Fprintf(c.Stderr, "unknown config command %q\n\n", args[0])
		c.configUsage()
		return 2
	}
}

func (c *CLI) configUsage() {
	_, _ = fmt.Fprint(c.Stderr, `Usage: parameter-store config <command> [flags]

Commands:
  show       Print the effective configuration and the source of every value.
  validate   Resolve the configuration and run the same checks serve performs.

Both commands accept --config and every server setting flag, so they show
exactly what serve (or init, backup, restore, ...) would use with the same
arguments and environment.
`)
}

// cmdConfigShow prints every setting with its effective value and where the
// value came from. Paths are printed in full: this is the tool for answering
// "which database file is it actually going to open?". Secret material never
// lives in Config; the passphrase variable is reported only as set or unset.
func (c *CLI) cmdConfigShow(args []string) int {
	fs := c.newFlags("config show")
	r := c.serverSettings(fs)
	c.setUsage(fs, "config show [flags]",
		"Print the effective configuration with the source of each value.", true)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}
	cfg, prov, path, err := r.resolve()
	if err != nil {
		return c.failErr("", err)
	}
	if c.jsonOutput() {
		return c.printJSON(configShowJSON{
			ConfigPath:       optionalString(path),
			ConfigPathSource: optionalString(r.configFileSource()),
			Passphrase:       c.passphraseState(),
			Settings:         configSettingsJSON(&cfg, prov),
		})
	}
	printConfigTable(c.Stdout, &cfg, prov, path, r.configFileSource(), c.passphraseState())
	return 0
}

// configShowJSON is the JSON form of config show. config_path is null when no
// file was read, and passphrase reports only presence ("set"/"unset") — the
// same redaction the table applies, since KMS_MASTER_PASSPHRASE never belongs
// in machine-readable output.
type configShowJSON struct {
	ConfigPath       *string             `json:"config_path"`
	ConfigPathSource *string             `json:"config_path_source"`
	Passphrase       string              `json:"passphrase"` // set | unset
	Settings         []configSettingJSON `json:"settings"`
}

// configSettingJSON is one resolved setting with the layer it came from.
type configSettingJSON struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

// configSettingsJSON renders every registered setting in the registry's order,
// so the JSON rows line up one-for-one with the table's.
func configSettingsJSON(cfg *config.Config, prov config.Provenance) []configSettingJSON {
	settings := make([]configSettingJSON, 0, len(config.Settings))
	for _, s := range config.Settings {
		// Unlike the table, an unset value stays the empty string here: JSON
		// has a representation for "empty" and does not need the `""` marker
		// the aligned columns use to keep the row readable.
		settings = append(settings, configSettingJSON{Key: s.Key, Value: s.Get(cfg), Source: prov[s.Key].String()})
	}
	return settings
}

// optionalString maps the empty string to JSON null, for fields whose absence
// is meaningful ("no config file" rather than "a file named nothing").
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// cmdConfigValidate resolves the configuration and runs Config.Validate, which
// also checks that referenced TLS files exist.
func (c *CLI) cmdConfigValidate(args []string) int {
	fs := c.newFlags("config validate")
	r := c.serverSettings(fs)
	c.setUsage(fs, "config validate [flags]",
		"Resolve the configuration and check it the way serve does, including that referenced TLS files exist.", true)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectPositionals() {
		return 2
	}
	cfg, _, path, err := r.resolve()
	if err != nil {
		return c.failErr("", err)
	}
	if err := cfg.Validate(); err != nil {
		return c.failErr("invalid configuration", err)
	}
	if c.jsonOutput() {
		// Only the valid case reaches this point; an invalid configuration
		// exits non-zero with the reason on stderr rather than reporting
		// valid:false, so a script never has to parse the document to learn
		// that the command failed.
		return c.printJSON(configValidateJSON{Valid: true, ConfigPath: optionalString(path)})
	}
	if path != "" {
		_, _ = fmt.Fprintf(c.Stdout, "configuration OK (%s)\n", path)
	} else {
		_, _ = fmt.Fprintln(c.Stdout, "configuration OK (no config file; defaults, environment, and flags only)")
	}
	return 0
}

// configValidateJSON is the JSON form of config validate.
type configValidateJSON struct {
	Valid      bool    `json:"valid"`
	ConfigPath *string `json:"config_path"`
}

func (c *CLI) passphraseState() string {
	if v, ok := c.env("KMS_MASTER_PASSPHRASE"); ok && v != "" {
		return "set"
	}
	return "unset"
}

// printConfigTable renders the header, the KEY/VALUE/SOURCE table, and the
// passphrase footer.
func printConfigTable(w io.Writer, cfg *config.Config, prov config.Provenance, path, pathSource, passphrase string) {
	switch {
	case path == "":
		_, _ = fmt.Fprintln(w, "config file: none")
	case pathSource != "":
		_, _ = fmt.Fprintf(w, "config file: %s (%s)\n", path, pathSource)
	default:
		_, _ = fmt.Fprintf(w, "config file: %s\n", path)
	}
	_, _ = fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "KEY\tVALUE\tSOURCE")
	for _, s := range config.Settings {
		val := s.Get(cfg)
		if val == "" {
			val = `""`
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Key, val, prov[s.Key])
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintf(w, "\nKMS_MASTER_PASSPHRASE: %s\n", passphrase)
}
