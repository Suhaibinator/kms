package configstore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxDefaultsExporterErrorBytes = 512

// RunDefaultsExporter runs the small command used by an importing application
// to export source-owned parameter defaults. It returns a process exit code:
// zero on success, two for invalid arguments, and one for all other failures.
//
// A typical main passes os.Args[1:], os.Stdout, os.Stderr, its profile defaults
// provider, and the generated binding's EncodeDefaultsArtifact function. When
// --output is omitted, the artifact is written to stdout.
func RunDefaultsExporter[P ~string, T any](
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	provider func(P) (*T, error),
	encoder func(string, *T) ([]byte, error),
) int {
	if stderr == nil {
		return 1
	}
	flags, err := parseDefaultsExporterFlags(args)
	if err != nil {
		writeDefaultsExporterError(stderr, "arguments", err)
		return 2
	}
	if flags.help {
		if stdout == nil {
			return 1
		}
		if _, err := io.WriteString(stdout, defaultsExporterUsage()); err != nil {
			return 1
		}
		return 0
	}
	if provider == nil {
		writeDefaultsExporterError(stderr, "configuration", errors.New("defaults provider is required"))
		return 1
	}
	if encoder == nil {
		writeDefaultsExporterError(stderr, "configuration", errors.New("artifact encoder is required"))
		return 1
	}

	defaults, err := provider(P(flags.profile))
	if err != nil {
		writeDefaultsExporterError(stderr, "load defaults", errors.New("provider failed"))
		return 1
	}
	if defaults == nil {
		writeDefaultsExporterError(stderr, "load defaults", errors.New("provider returned nil"))
		return 1
	}
	artifactData, err := encoder(flags.profile, defaults)
	if err != nil {
		writeDefaultsExporterError(stderr, "encode artifact", errors.New("encoder failed"))
		return 1
	}
	artifact, err := ParseDefaultsArtifact(artifactData)
	if err != nil {
		writeDefaultsExporterError(stderr, "validate encoded artifact", err)
		return 1
	}
	if artifact.Profile != flags.profile {
		writeDefaultsExporterError(stderr, "validate encoded artifact", errors.New("profile does not match --profile"))
		return 1
	}

	if flags.output == "-" {
		if stdout == nil {
			writeDefaultsExporterError(stderr, "write artifact", errors.New("stdout is required when --output is -"))
			return 1
		}
		if _, err := stdout.Write(artifactData); err != nil {
			writeDefaultsExporterError(stderr, "write artifact", err)
			return 1
		}
		return 0
	}
	if err := writeDefaultsArtifactFile(flags.output, artifactData); err != nil {
		writeDefaultsExporterError(stderr, "write artifact", errors.New("output failed"))
		return 1
	}
	return 0
}

type defaultsExporterFlags struct {
	profile string
	output  string
	help    bool
}

func parseDefaultsExporterFlags(args []string) (defaultsExporterFlags, error) {
	values := make(map[string]string, 2)
	help := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "" {
			continue
		}
		if argument == "--help" || argument == "-h" {
			help = true
			continue
		}
		if !strings.HasPrefix(argument, "--") {
			return defaultsExporterFlags{}, errors.New("positional arguments are not supported")
		}
		name, inline, hasInline := strings.Cut(argument, "=")
		if name != "--profile" && name != "--output" {
			return defaultsExporterFlags{}, fmt.Errorf("unknown option %q", boundedDefaultsOption(name))
		}
		if _, duplicate := values[name]; duplicate {
			return defaultsExporterFlags{}, fmt.Errorf("duplicate option %s", name)
		}
		value := inline
		if !hasInline {
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "--") {
				return defaultsExporterFlags{}, fmt.Errorf("%s requires a value", name)
			}
			value = args[index]
		}
		if value == "" {
			return defaultsExporterFlags{}, fmt.Errorf("%s requires a value", name)
		}
		values[name] = value
	}
	if help {
		return defaultsExporterFlags{help: true}, nil
	}
	profile := values["--profile"]
	if !canonicalDefaultsText(profile, false) {
		return defaultsExporterFlags{}, errors.New("--profile must be nonempty and canonical")
	}
	output := values["--output"]
	if output == "" {
		output = "-"
	}
	return defaultsExporterFlags{profile: profile, output: output}, nil
}

func boundedDefaultsOption(option string) string {
	if len(option) <= 80 {
		return option
	}
	return option[:77] + "..."
}

func defaultsExporterUsage() string {
	return "Usage: defaults-exporter --profile <profile> [--output <file|->]\n\n" +
		"Options:\n" +
		"  --profile <profile>  Application defaults profile\n" +
		"  --output <file|->    Artifact destination (default: - for stdout)\n" +
		"  --help               Show this help\n"
}

func writeDefaultsExporterError(stderr io.Writer, operation string, err error) {
	message := fmt.Sprintf("defaults-exporter: %s: %v", operation, err)
	if len(message) > maxDefaultsExporterErrorBytes {
		message = message[:maxDefaultsExporterErrorBytes-3] + "..."
	}
	_, _ = fmt.Fprintln(stderr, message)
}

func writeDefaultsArtifactFile(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".kms-defaults-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}
