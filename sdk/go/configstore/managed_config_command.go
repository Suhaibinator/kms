package configstore

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

// ManagedConfigCommandConfig combines the source-owned application schema and
// profile-specific defaults workflows behind one small command runner.
type ManagedConfigCommandConfig[P ~string, T any] struct {
	Application string
	Schema      func() []byte
	Defaults    DefaultsApplierConfig[P, T]
}

type managedConfigClient interface {
	defaultsApplyClient
	CreateApplicationSchema(context.Context, kmsclient.CreateApplicationSchemaOptions) (kmsclient.ApplicationSchema, error)
}

type managedConfigClientFactory func(kmsclient.Config) (managedConfigClient, error)

// RunManagedConfigCommand dispatches either "schema upload" or "defaults
// apply". The importing application's main normally passes os.Args[1:] and
// the generated GeneratedSchema and EncodeDefaultsArtifact functions.
func RunManagedConfigCommand[P ~string, T any](
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	config ManagedConfigCommandConfig[P, T],
) int {
	return runManagedConfigCommand(args, stdout, stderr, config, func(clientConfig kmsclient.Config) (managedConfigClient, error) {
		return kmsclient.NewClient(clientConfig)
	})
}

func runManagedConfigCommand[P ~string, T any](
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	config ManagedConfigCommandConfig[P, T],
	newClient managedConfigClientFactory,
) int {
	if stderr == nil {
		return 1
	}
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		if stdout != nil {
			_, _ = fmt.Fprint(stdout, managedConfigUsage())
		}
		return 0
	}
	if len(args) < 2 {
		writeManagedConfigError(stderr, errors.New("expected schema upload or defaults apply"))
		return 2
	}
	switch args[0] + " " + args[1] {
	case "defaults apply":
		if len(args) == 3 && (args[2] == "-h" || args[2] == "--help") {
			if stdout != nil {
				_, _ = fmt.Fprint(stdout, strings.Replace(defaultsApplierUsage(), "defaults-applier", "managed-config defaults apply", 1))
			}
			return 0
		}
		return runDefaultsApplier(args[2:], stdout, stderr, config.Defaults, func(clientConfig kmsclient.Config) (defaultsApplyClient, error) {
			return newClient(clientConfig)
		})
	case "schema upload":
		return runManagedSchemaUpload(args[2:], stdout, stderr, config.Application, config.Schema, newClient)
	default:
		writeManagedConfigError(stderr, fmt.Errorf("unknown command %q", strings.Join(args[:2], " ")))
		return 2
	}
}

type managedSchemaFlags struct {
	endpoint string
	metadata string
	insecure bool
	ca       string
	cert     string
	key      string
}

func runManagedSchemaUpload(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	application string,
	schemaProvider func() []byte,
	newClient managedConfigClientFactory,
) int {
	if stdout == nil || schemaProvider == nil || newClient == nil || !validManagedApplication(application) {
		writeManagedConfigError(stderr, errors.New("runner configuration is incomplete"))
		return 1
	}
	flags, help, err := parseManagedSchemaFlags(args, stdout, stderr)
	if help {
		return 0
	}
	if err != nil {
		writeManagedConfigError(stderr, err)
		return 2
	}
	schema := schemaProvider()
	if len(schema) == 0 {
		writeManagedConfigError(stderr, errors.New("generated schema is empty"))
		return 1
	}
	clientConfig, err := defaultsApplierClientConfig(defaultsApplierFlags{
		endpoint: flags.endpoint, insecure: flags.insecure, ca: flags.ca, cert: flags.cert, key: flags.key,
	}, "")
	if err != nil {
		writeManagedConfigError(stderr, err)
		return 2
	}
	client, err := newClient(clientConfig)
	if err != nil {
		writeManagedConfigError(stderr, fmt.Errorf("connect: %w", err))
		return 1
	}
	defer func() { _ = client.Close() }()
	created, err := client.CreateApplicationSchema(context.Background(), kmsclient.CreateApplicationSchemaOptions{
		Application: application, Schema: schema, MetadataJSON: flags.metadata,
	})
	if err != nil {
		writeManagedConfigError(stderr, fmt.Errorf("upload schema: %w", err))
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "Uploaded schema %s/%s@%d\nDigest: %s\n", created.Application, created.ReleaseName, created.Version, created.Digest); err != nil {
		writeManagedConfigError(stderr, fmt.Errorf("write result: %w", err))
		return 1
	}
	return 0
}

func parseManagedSchemaFlags(args []string, stdout, stderr io.Writer) (managedSchemaFlags, bool, error) {
	var result managedSchemaFlags
	set := flag.NewFlagSet("managed-config schema upload", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.StringVar(&result.endpoint, "endpoint", "localhost:8443", "KMS gRPC endpoint")
	set.StringVar(&result.metadata, "metadata-json", "", "non-sensitive metadata JSON")
	set.BoolVar(&result.insecure, "insecure", false, "disable TLS for local development")
	set.StringVar(&result.ca, "ca", "", "CA bundle for server verification")
	set.StringVar(&result.cert, "cert", "", "client certificate for mTLS")
	set.StringVar(&result.key, "key", "", "client private key for mTLS")
	set.Usage = func() {
		if stdout != nil {
			_, _ = fmt.Fprint(stdout, managedSchemaUsage())
		}
	}
	if err := set.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return managedSchemaFlags{}, true, nil
		}
		return managedSchemaFlags{}, false, err
	}
	if set.NArg() != 0 {
		return managedSchemaFlags{}, false, errors.New("positional arguments are not supported")
	}
	return result, false, nil
}

func validManagedApplication(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

func managedConfigUsage() string {
	return "Usage: managed-config <command> [flags]\n\nCommands:\n" +
		"  schema upload    Upload the generated application schema\n" +
		"  defaults apply   Preview or apply defaults for a profile\n"
}

func managedSchemaUsage() string {
	return "Usage: managed-config schema upload [flags]\n\n" +
		"Flags:\n" +
		"  --endpoint <host:port>  KMS gRPC endpoint (default localhost:8443)\n" +
		"  --metadata-json JSON    Non-sensitive schema metadata\n" +
		"  --insecure              Disable TLS for local development\n" +
		"  --ca FILE               CA bundle for server verification\n" +
		"  --cert FILE --key FILE  Client certificate and private key for mTLS\n" +
		"  --help                  Show this help\n\n" +
		"The identity bearer token is read from KMS_TOKEN.\n"
}

func writeManagedConfigError(stderr io.Writer, err error) {
	message := fmt.Sprintf("managed-config: %v", err)
	if len(message) > maxDefaultsExporterErrorBytes {
		message = message[:maxDefaultsExporterErrorBytes-3] + "..."
	}
	_, _ = fmt.Fprintln(stderr, message)
}
