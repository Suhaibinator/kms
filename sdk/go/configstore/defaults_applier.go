package configstore

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

// DefaultsApplierConfig supplies the application-owned pieces of the managed
// defaults workflow. KMS owns argument parsing, transport, preview, optimistic
// concurrency, idempotent execution, and result rendering.
type DefaultsApplierConfig[P ~string, T any] struct {
	Provider  func(P) (*T, error)
	Encoder   func(string, *T) ([]byte, error)
	Namespace func(P) (string, error)
}

type defaultsApplyClient interface {
	ApplyApplicationDefaults(context.Context, kmsclient.ApplicationDefaultsApplyOptions) (kmsclient.ApplicationDefaultsApplyResult, error)
	Close() error
}

type defaultsApplyClientFactory func(kmsclient.Config) (defaultsApplyClient, error)

type defaultsApplierFlags struct {
	profile           string
	endpoint          string
	overwrite         bool
	execute           bool
	confirmProduction string
	insecure          bool
	ca                string
	cert              string
	key               string
}

// RunDefaultsApplier runs a complete source-defaults preview or apply command.
// The importing application's main normally passes os.Args[1:], standard I/O,
// its defaults provider, generated artifact encoder, and profile-to-namespace
// resolver. The KMS identity token is read from KMS_TOKEN.
func RunDefaultsApplier[P ~string, T any](
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	config DefaultsApplierConfig[P, T],
) int {
	return runDefaultsApplier(args, stdout, stderr, config, func(clientConfig kmsclient.Config) (defaultsApplyClient, error) {
		return kmsclient.NewClient(clientConfig)
	})
}

func runDefaultsApplier[P ~string, T any](
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	config DefaultsApplierConfig[P, T],
	newClient defaultsApplyClientFactory,
) int {
	if stderr == nil {
		return 1
	}
	flags, help, err := parseDefaultsApplierFlags(args, stdout, stderr)
	if help {
		return 0
	}
	if err != nil {
		writeDefaultsApplierError(stderr, "arguments", err)
		return 2
	}
	if stdout == nil || config.Provider == nil || config.Encoder == nil || config.Namespace == nil || newClient == nil {
		writeDefaultsApplierError(stderr, "configuration", errors.New("runner configuration is incomplete"))
		return 1
	}

	profile := P(flags.profile)
	root, err := config.Provider(profile)
	if err != nil || root == nil {
		writeDefaultsApplierError(stderr, "load defaults", errors.New("provider failed"))
		return 1
	}
	artifactData, err := config.Encoder(flags.profile, root)
	if err != nil {
		writeDefaultsApplierError(stderr, "encode artifact", errors.New("encoder failed"))
		return 1
	}
	artifact, err := ParseDefaultsArtifact(artifactData)
	if err != nil || artifact.Profile != flags.profile {
		writeDefaultsApplierError(stderr, "validate encoded artifact", errors.New("artifact is invalid"))
		return 1
	}
	namespace, err := config.Namespace(profile)
	if err != nil || namespace == "" {
		writeDefaultsApplierError(stderr, "resolve namespace", errors.New("resolver failed"))
		return 1
	}
	environment, _, ok := strings.Cut(namespace, "/")
	if !ok || environment == "" {
		writeDefaultsApplierError(stderr, "resolve namespace", errors.New("resolver returned an invalid namespace"))
		return 1
	}
	if err := validateDefaultsProductionConfirmation(environment, flags.execute, flags.confirmProduction); err != nil {
		writeDefaultsApplierError(stderr, "arguments", err)
		return 2
	}

	clientConfig, err := defaultsApplierClientConfig(flags, namespace)
	if err != nil {
		writeDefaultsApplierError(stderr, "transport", err)
		return 2
	}
	client, err := newClient(clientConfig)
	if err != nil {
		writeDefaultsApplierError(stderr, "connect", err)
		return 1
	}
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	preview, err := client.ApplyApplicationDefaults(ctx, kmsclient.ApplicationDefaultsApplyOptions{
		Namespace: namespace, Artifact: artifactData, Overwrite: flags.overwrite,
	})
	if err != nil {
		writeDefaultsApplierError(stderr, "preview defaults", err)
		return 1
	}
	if err := writeDefaultsApplyResult(stdout, "Preview", preview); err != nil {
		writeDefaultsApplierError(stderr, "write preview", err)
		return 1
	}
	if !flags.execute {
		return 0
	}
	if blocked := countDefaultsApplyStatus(preview.Entries, "blocked"); blocked > 0 {
		writeDefaultsApplierError(stderr, "apply defaults", fmt.Errorf("%d parameter default(s) are blocked; pass --overwrite and retry", blocked))
		return 1
	}

	applied, err := client.ApplyApplicationDefaults(ctx, kmsclient.ApplicationDefaultsApplyOptions{
		Namespace: namespace, Artifact: artifactData, Overwrite: flags.overwrite,
		Execute: true, PlanDigest: preview.PlanDigest,
	})
	if err != nil {
		writeDefaultsApplierError(stderr, "apply defaults", err)
		return 1
	}
	if err := writeDefaultsApplyResult(stdout, "Applied", applied); err != nil {
		writeDefaultsApplierError(stderr, "write result", err)
		return 1
	}
	return 0
}

func parseDefaultsApplierFlags(args []string, stdout, stderr io.Writer) (defaultsApplierFlags, bool, error) {
	var result defaultsApplierFlags
	set := flag.NewFlagSet("defaults-applier", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.StringVar(&result.profile, "profile", "", "application defaults profile")
	set.StringVar(&result.endpoint, "endpoint", "localhost:8443", "KMS gRPC endpoint")
	set.BoolVar(&result.overwrite, "overwrite", false, "permit differing parameter values to be updated")
	set.BoolVar(&result.execute, "execute", false, "apply after a fresh preview")
	set.StringVar(&result.confirmProduction, "confirm-production", "", "production environment name confirmation")
	set.BoolVar(&result.insecure, "insecure", false, "disable TLS for local development")
	set.StringVar(&result.ca, "ca", "", "CA bundle for server verification")
	set.StringVar(&result.cert, "cert", "", "client certificate for mTLS")
	set.StringVar(&result.key, "key", "", "client private key for mTLS")
	set.Usage = func() {
		if stdout == nil {
			return
		}
		_, _ = fmt.Fprint(stdout, defaultsApplierUsage())
	}
	if err := set.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return defaultsApplierFlags{}, true, nil
		}
		return defaultsApplierFlags{}, false, err
	}
	if set.NArg() != 0 {
		return defaultsApplierFlags{}, false, errors.New("positional arguments are not supported")
	}
	if !canonicalDefaultsText(result.profile, false) {
		return defaultsApplierFlags{}, false, errors.New("--profile must be nonempty and canonical")
	}
	return result, false, nil
}

func defaultsApplierUsage() string {
	return "Usage: defaults-applier --profile <profile> [flags]\n\n" +
		"Flags:\n" +
		"  --profile <profile>       Application defaults profile (required)\n" +
		"  --endpoint <host:port>    KMS gRPC endpoint (default localhost:8443)\n" +
		"  --overwrite               Permit differing parameter values to be updated\n" +
		"  --execute                 Apply after a fresh preview\n" +
		"  --confirm-production ENV  Required with --execute for production environments\n" +
		"  --insecure                Disable TLS for local development\n" +
		"  --ca FILE                 CA bundle for server verification\n" +
		"  --cert FILE --key FILE    Client certificate and private key for mTLS\n" +
		"  --help                    Show this help\n\n" +
		"The identity bearer token is read from KMS_TOKEN.\n"
}

func defaultsApplierClientConfig(flags defaultsApplierFlags, namespace string) (kmsclient.Config, error) {
	if flags.insecure && (flags.ca != "" || flags.cert != "" || flags.key != "") {
		return kmsclient.Config{}, errors.New("--insecure cannot be combined with --ca, --cert, or --key")
	}
	if (flags.cert == "") != (flags.key == "") {
		return kmsclient.Config{}, errors.New("--cert and --key must be supplied together")
	}
	config := kmsclient.Config{
		Endpoint: flags.endpoint, Namespace: namespace, Token: os.Getenv("KMS_TOKEN"), Insecure: flags.insecure,
	}
	if flags.insecure {
		return config, nil
	}
	var err error
	if flags.cert != "" {
		config.TLS, err = kmsclient.MTLSConfig(flags.cert, flags.key, flags.ca)
	} else {
		config.TLS, err = kmsclient.TLSConfig(flags.ca)
	}
	if err != nil {
		return kmsclient.Config{}, err
	}
	return config, nil
}

func validateDefaultsProductionConfirmation(environment string, execute bool, confirmation string) error {
	production := environment == "prod" || strings.HasPrefix(environment, "prod-") || environment == "production"
	if !production && confirmation != "" {
		return errors.New("--confirm-production is only valid for production environments")
	}
	if confirmation != "" && confirmation != environment {
		return fmt.Errorf("--confirm-production must exactly match environment %q", environment)
	}
	if execute && production && confirmation == "" {
		return fmt.Errorf("--execute for production environment %q requires --confirm-production %s", environment, environment)
	}
	return nil
}

func writeDefaultsApplyResult(writer io.Writer, heading string, result kmsclient.ApplicationDefaultsApplyResult) error {
	entries := append([]kmsclient.ApplicationDefaultsApplyEntry(nil), result.Entries...)
	sort.Slice(entries, func(left, right int) bool {
		if entries[left].Alias == entries[right].Alias {
			return entries[left].Key < entries[right].Key
		}
		return entries[left].Alias < entries[right].Alias
	})
	missingSecrets := append([]string(nil), result.MissingSecrets...)
	sort.Strings(missingSecrets)
	if _, err := fmt.Fprintf(writer, "%s defaults\nProfile: %s\nPlan digest: %s\n", heading, result.Profile, result.PlanDigest); err != nil {
		return err
	}
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "STATUS\tALIAS\tKEY\tCONTENT TYPE\tCURRENT\tAPPLIED\tREVISION"); err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%d\t%d\t%d\n",
			entry.Status, entry.Alias, entry.Key, entry.ContentType,
			entry.CurrentVersion, entry.AppliedVersion, entry.Revision); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	statuses := []string{"create", "unchanged", "update", "blocked"}
	summary := make([]string, 0, len(statuses))
	for _, status := range statuses {
		summary = append(summary, fmt.Sprintf("%s=%d", status, countDefaultsApplyStatus(entries, status)))
	}
	if _, err := fmt.Fprintf(writer, "Summary: %s\n", strings.Join(summary, " ")); err != nil {
		return err
	}
	if len(missingSecrets) > 0 {
		if _, err := fmt.Fprintf(writer, "Missing secrets: %s\n", strings.Join(missingSecrets, ", ")); err != nil {
			return err
		}
	}
	return nil
}

func countDefaultsApplyStatus(entries []kmsclient.ApplicationDefaultsApplyEntry, status string) int {
	count := 0
	for _, entry := range entries {
		if entry.Status == status {
			count++
		}
	}
	return count
}

func writeDefaultsApplierError(stderr io.Writer, operation string, err error) {
	message := fmt.Sprintf("defaults-applier: %s: %v", operation, err)
	if len(message) > maxDefaultsExporterErrorBytes {
		message = message[:maxDefaultsExporterErrorBytes-3] + "..."
	}
	_, _ = fmt.Fprintln(stderr, message)
}
