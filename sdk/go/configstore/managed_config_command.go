package configstore

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

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
	CreateApplicationRelease(context.Context, kmsclient.CreateApplicationReleaseOptions) (kmsclient.CreateApplicationReleaseResult, error)
}

type managedConfigClientFactory func(kmsclient.Config) (managedConfigClient, error)

// RunManagedConfigCommand dispatches "schema upload", "defaults apply", or
// "release create". The importing application's main normally passes
// os.Args[1:] and the generated GeneratedSchema and EncodeDefaultsArtifact
// functions.
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
		writeManagedConfigError(stderr, errors.New("expected schema upload, defaults apply, or release create"))
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
	case "release create":
		return runManagedReleaseCreate(args[2:], stdout, stderr, config, newClient)
	default:
		writeManagedConfigError(stderr, fmt.Errorf("unknown command %q", strings.Join(args[:2], " ")))
		return 2
	}
}

type managedReleaseFlags struct {
	profile  string
	endpoint string
	metadata string
	execute  bool
	insecure bool
	ca       string
	cert     string
	key      string
}

func runManagedReleaseCreate[P ~string, T any](
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	config ManagedConfigCommandConfig[P, T],
	newClient managedConfigClientFactory,
) int {
	flags, help, err := parseManagedReleaseFlags(args, stdout, stderr)
	if help {
		return 0
	}
	if err != nil {
		writeManagedConfigError(stderr, err)
		return 2
	}
	if stdout == nil || !validManagedApplication(config.Application) || config.Defaults.Provider == nil ||
		config.Defaults.Encoder == nil || config.Defaults.Namespace == nil || newClient == nil {
		writeManagedConfigError(stderr, errors.New("runner configuration is incomplete"))
		return 1
	}

	profile := P(flags.profile)
	root, err := config.Defaults.Provider(profile)
	if err != nil || root == nil {
		writeManagedConfigError(stderr, errors.New("load defaults: provider failed"))
		return 1
	}
	artifactData, err := config.Defaults.Encoder(flags.profile, root)
	if err != nil {
		writeManagedConfigError(stderr, errors.New("encode artifact: encoder failed"))
		return 1
	}
	artifact, err := ParseDefaultsArtifact(artifactData)
	if err != nil || artifact.Profile != flags.profile {
		writeManagedConfigError(stderr, errors.New("validate encoded artifact: artifact is invalid"))
		return 1
	}
	namespace, err := config.Defaults.Namespace(profile)
	if err != nil || namespace == "" {
		writeManagedConfigError(stderr, errors.New("resolve namespace: resolver failed"))
		return 1
	}
	_, application, ok := strings.Cut(namespace, "/")
	if !ok || application != config.Application || strings.Contains(application, "/") {
		writeManagedConfigError(stderr, errors.New("resolve namespace: resolver returned a namespace for another application"))
		return 1
	}

	clientConfig, err := defaultsApplierClientConfig(defaultsApplierFlags{
		endpoint: flags.endpoint, insecure: flags.insecure, ca: flags.ca, cert: flags.cert, key: flags.key,
	}, namespace)
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

	ctx := context.Background()
	preview, err := client.CreateApplicationRelease(ctx, kmsclient.CreateApplicationReleaseOptions{
		Namespace: namespace, Artifact: artifactData, MetadataJSON: flags.metadata,
	})
	if err != nil {
		writeManagedConfigError(stderr, fmt.Errorf("preview release: %w", err))
		return 1
	}
	if err := validateManagedReleaseResult(preview, flags.profile, false, ""); err != nil {
		writeManagedConfigError(stderr, err)
		return 1
	}
	if err := writeManagedReleaseResult(stdout, "Preview", preview); err != nil {
		writeManagedConfigError(stderr, fmt.Errorf("write preview: %w", err))
		return 1
	}
	if !preview.Valid {
		writeManagedConfigError(stderr, errors.New("release preview is invalid"))
		return 1
	}
	if !flags.execute {
		return 0
	}

	created, err := client.CreateApplicationRelease(ctx, kmsclient.CreateApplicationReleaseOptions{
		Namespace: namespace, Artifact: artifactData, MetadataJSON: flags.metadata,
		Execute: true, PlanDigest: preview.PlanDigest,
	})
	if err != nil {
		writeManagedConfigError(stderr, fmt.Errorf("create release: %w", err))
		return 1
	}
	if err := validateManagedReleaseResult(created, flags.profile, true, preview.PlanDigest); err != nil {
		writeManagedConfigError(stderr, err)
		return 1
	}
	if err := writeManagedReleaseResult(stdout, "Result", created); err != nil {
		writeManagedConfigError(stderr, fmt.Errorf("write result: %w", err))
		return 1
	}
	return 0
}

func parseManagedReleaseFlags(args []string, stdout, stderr io.Writer) (managedReleaseFlags, bool, error) {
	var result managedReleaseFlags
	set := flag.NewFlagSet("managed-config release create", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.StringVar(&result.profile, "profile", "", "application defaults profile")
	set.StringVar(&result.endpoint, "endpoint", "localhost:8443", "KMS gRPC endpoint")
	set.StringVar(&result.metadata, "metadata-json", "", "non-sensitive release metadata JSON")
	set.BoolVar(&result.execute, "execute", false, "create after a fresh preview")
	set.BoolVar(&result.insecure, "insecure", false, "disable TLS for local development")
	set.StringVar(&result.ca, "ca", "", "CA bundle for server verification")
	set.StringVar(&result.cert, "cert", "", "client certificate for mTLS")
	set.StringVar(&result.key, "key", "", "client private key for mTLS")
	set.Usage = func() {
		if stdout != nil {
			_, _ = fmt.Fprint(stdout, managedReleaseUsage())
		}
	}
	if err := set.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return managedReleaseFlags{}, true, nil
		}
		return managedReleaseFlags{}, false, err
	}
	if set.NArg() != 0 {
		return managedReleaseFlags{}, false, errors.New("positional arguments are not supported")
	}
	if !canonicalDefaultsText(result.profile, false) {
		return managedReleaseFlags{}, false, errors.New("--profile must be nonempty and canonical")
	}
	return result, false, nil
}

func validateManagedReleaseResult(result kmsclient.CreateApplicationReleaseResult, profile string, execute bool, planDigest string) error {
	if result.Profile != profile || result.PlanDigest == "" || result.ReleaseName == "" {
		return errors.New("invalid application release response")
	}
	if result.Executed != execute || result.Created && !execute {
		return errors.New("application release response execution state mismatch")
	}
	if execute && (result.PlanDigest != planDigest || !result.Valid) {
		return errors.New("application release response does not match the preview")
	}
	return nil
}

func writeManagedReleaseResult(writer io.Writer, heading string, result kmsclient.CreateApplicationReleaseResult) error {
	entries := append([]kmsclient.ApplicationReleasePlanEntry(nil), result.Entries...)
	sort.Slice(entries, func(left, right int) bool { return entries[left].Alias < entries[right].Alias })
	missingSecrets := append([]string(nil), result.MissingSecrets...)
	sort.Strings(missingSecrets)
	if _, err := fmt.Fprintf(writer,
		"%s release\nProfile: %s\nRelease: %s\nSchema version: %d\nBase release version: %d\nPlan digest: %s\nValid: %t\nExecuted: %t\nCreated: %t\n",
		heading, result.Profile, result.ReleaseName, result.SchemaVersion, result.BaseReleaseVersion,
		result.PlanDigest, result.Valid, result.Executed, result.Created); err != nil {
		return err
	}
	if result.Release != nil {
		if _, err := fmt.Fprintf(writer, "Release version: %d\nRelease digest: %s\nActivation: inactive\n",
			result.Release.Version(), result.Release.Digest()); err != nil {
			return err
		}
	}
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "ALIAS\tKIND\tPATH\tFROM\tTO\tSOURCE"); err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%d\t%s\n",
			entry.Alias, entry.Kind, entry.Path, entry.FromVersion, entry.ToVersion, entry.Source); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	kinds := map[string]int{"parameter": 0, "secret": 0}
	for _, entry := range entries {
		kinds[entry.Kind]++
	}
	if _, err := fmt.Fprintf(writer, "Summary: parameter=%d secret=%d\n", kinds["parameter"], kinds["secret"]); err != nil {
		return err
	}
	if len(missingSecrets) == 0 {
		if _, err := fmt.Fprintln(writer, "Missing secrets: none"); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(writer, "Missing secrets (%d): %s\n", len(missingSecrets), strings.Join(missingSecrets, ", ")); err != nil {
		return err
	}
	validationCounts := make(map[string]int)
	for _, validation := range result.Validation {
		validationCounts[validation.Code]++
	}
	if len(validationCounts) == 0 {
		_, err := fmt.Fprintln(writer, "Validation: none")
		return err
	}
	codes := make([]string, 0, len(validationCounts))
	for code := range validationCounts {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	summary := make([]string, 0, len(codes))
	for _, code := range codes {
		summary = append(summary, fmt.Sprintf("%s=%d", code, validationCounts[code]))
	}
	_, err := fmt.Fprintf(writer, "Validation: %s\n", strings.Join(summary, " "))
	return err
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
		"  defaults apply   Preview or apply defaults for a profile\n" +
		"  release create   Preview or create an immutable release for a profile\n"
}

func managedReleaseUsage() string {
	return "Usage: managed-config release create --profile <profile> [flags]\n\n" +
		"Flags:\n" +
		"  --profile <profile>     Application defaults profile (required)\n" +
		"  --endpoint <host:port>  KMS gRPC endpoint (default localhost:8443)\n" +
		"  --metadata-json JSON    Non-sensitive release metadata\n" +
		"  --execute               Create after a fresh preview; does not activate\n" +
		"  --insecure              Disable TLS for local development\n" +
		"  --ca FILE               CA bundle for server verification\n" +
		"  --cert FILE --key FILE  Client certificate and private key for mTLS\n" +
		"  --help                  Show this help\n\n" +
		"The identity bearer token is read from KMS_TOKEN.\n"
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
