package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"google.golang.org/grpc"
)

// dialFunc opens the gRPC connection a server-backed command uses.
type dialFunc func(*connFlags) (*grpc.ClientConn, error)

func (c *CLI) cmdDefaults(args []string) int {
	if len(args) == 0 {
		c.defaultsUsage()
		return 2
	}
	switch args[0] {
	case "apply":
		return c.cmdDefaultsApply(args[1:])
	case "help", "-h", "--help":
		c.defaultsUsage()
		return 0
	default:
		return c.defaultsUsageError("unknown defaults command %q", args[0])
	}
}

func (c *CLI) defaultsUsage() {
	_, _ = fmt.Fprint(c.Stderr, `Usage:
  parameter-store defaults apply ENV/APP --from FILE|- [flags]

Commands:
  apply    Preview generated parameter defaults, or apply them with --execute.

Apply flags:
  --from FILE|-              Defaults artifact file, or - for stdin (required).
  --overwrite                Permit differing existing parameters to be updated.
  --update-definition        Permit the application contract and schema pin to be updated.
  --execute                  Apply after a fresh preview (preview is the default).
  --confirm-production ENV   Required for --execute against a production ENV.

Connection flags:
  --endpoint HOST:PORT       Server gRPC endpoint (default localhost:8443).
  --token TOKEN              Admin bearer token (env KMS_TOKEN).
  --insecure                 Disable TLS (development only).
  --ca FILE                  CA bundle for server verification.
  --cert FILE --key FILE     Client certificate and private key for mTLS.
`)
}

func (c *CLI) defaultsUsageError(format string, args ...any) int {
	_, _ = fmt.Fprintf(c.Stderr, "error: "+format+"\n\n", args...)
	c.defaultsUsage()
	return 2
}

func (c *CLI) cmdDefaultsApply(args []string) int {
	fs := c.newFlags("defaults apply")
	cf := addConnFlags(fs)
	from := fs.String("from", "", "defaults artifact file, or - for stdin")
	overwrite := fs.Bool("overwrite", false, "permit updates to differing existing parameters")
	updateDefinition := fs.Bool("update-definition", false, "permit application contract and schema pin updates")
	execute := fs.Bool("execute", false, "apply after a fresh preview")
	confirmProduction := fs.String("confirm-production", "", "production environment name confirmation")
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) != 1 || pos[0] == "" {
		return c.defaultsUsageError("defaults apply requires exactly one ENV/APP argument")
	}
	if *from == "" {
		return c.defaultsUsageError("defaults apply requires --from FILE|-")
	}
	ns, err := keyutil.ParseNamespace(pos[0])
	if err != nil {
		return c.defaultsUsageError("invalid namespace %q: %v", pos[0], err)
	}
	if code := c.validateProductionConfirmation(ns.Env, *execute, *confirmProduction); code != 0 {
		return code
	}
	artifact, err := c.readDefaultsArtifact(*from)
	if err != nil {
		return c.fail("reading defaults artifact: %v", err)
	}

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	client := kmsv1.NewAdminServiceClient(conn)
	pns := &kmsv1.NamespaceRef{Env: ns.Env, App: ns.App}

	preview, err := client.ApplyApplicationDefaults(cf.authCtx(ctx), &kmsv1.ApplyApplicationDefaultsRequest{
		Namespace:        pns,
		Artifact:         artifact,
		Overwrite:        *overwrite,
		UpdateDefinition: *updateDefinition,
	})
	if err != nil {
		return c.fail("previewing defaults: %v", err)
	}
	if err := validateDefaultsResponse(preview, false); err != nil {
		return c.fail("invalid defaults preview response: %v", err)
	}
	if err := c.writeDefaultsResult("Preview", preview); err != nil {
		return c.fail("writing defaults preview: %v", err)
	}
	if !*execute {
		return 0
	}
	if blocked := countDefaultsStatus(preview.GetEntries(), "blocked"); blocked > 0 {
		return c.fail("%d parameter default(s) are blocked; pass --overwrite and preview again", blocked)
	}
	if preview.GetDefinitionChanged() && !*updateDefinition {
		return c.fail("application definition differs; pass --update-definition and preview again")
	}

	applied, err := client.ApplyApplicationDefaults(cf.authCtx(ctx), &kmsv1.ApplyApplicationDefaultsRequest{
		Namespace:        pns,
		Artifact:         artifact,
		Overwrite:        *overwrite,
		UpdateDefinition: *updateDefinition,
		Execute:          true,
		PlanDigest:       preview.GetPlanDigest(),
	})
	if err != nil {
		return c.fail("applying defaults: %v", err)
	}
	if err := validateDefaultsResponse(applied, true); err != nil {
		return c.fail("invalid defaults apply response: %v", err)
	}
	if err := c.writeDefaultsResult("Applied", applied); err != nil {
		return c.fail("writing defaults result: %v", err)
	}
	return 0
}

// dialConn honours the test transport override, otherwise dials the
// endpoint named by the connection flags.
func (c *CLI) dialConn(cf *connFlags) (*grpc.ClientConn, error) {
	if c.dialOverride != nil {
		return c.dialOverride(cf)
	}
	return cf.dial()
}

func (c *CLI) validateProductionConfirmation(env string, execute bool, confirmation string) int {
	isProduction := domain.IsProductionEnvironment(env)
	if !isProduction && confirmation != "" {
		return c.defaultsUsageError("--confirm-production is only valid for production environments")
	}
	if confirmation != "" && confirmation != env {
		return c.defaultsUsageError("--confirm-production must exactly match environment %q", env)
	}
	if execute && isProduction && confirmation == "" {
		return c.defaultsUsageError("--execute for production environment %q requires --confirm-production %s", env, env)
	}
	return 0
}

func (c *CLI) readDefaultsArtifact(from string) ([]byte, error) {
	var (
		reader io.Reader
		file   *os.File
	)
	if from == "-" {
		if c.Stdin == nil {
			return nil, fmt.Errorf("stdin is unavailable")
		}
		reader = c.Stdin
	} else {
		var err error
		file, err = os.Open(from)
		if err != nil {
			return nil, err
		}
		defer func() { _ = file.Close() }()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, configstore.MaxDefaultsArtifactSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > configstore.MaxDefaultsArtifactSize {
		return nil, fmt.Errorf("artifact exceeds 4 MiB")
	}
	return data, nil
}

func validateDefaultsResponse(resp *kmsv1.ApplyApplicationDefaultsResponse, execute bool) error {
	if resp == nil {
		return fmt.Errorf("empty response")
	}
	if resp.GetPlanDigest() == "" {
		return fmt.Errorf("missing plan digest")
	}
	if resp.GetExecuted() != execute {
		return fmt.Errorf("executed state does not match request")
	}
	if resp.GetDefinitionUpdated() != (execute && resp.GetDefinitionChanged()) {
		return fmt.Errorf("definition state does not match request")
	}
	for index, entry := range resp.GetEntries() {
		if entry == nil {
			return fmt.Errorf("entry %d is empty", index)
		}
		switch entry.GetStatus() {
		case "create", "unchanged", "update", "blocked":
		default:
			return fmt.Errorf("entry %d has unknown status %q", index, entry.GetStatus())
		}
		if entry.GetAlias() == "" || entry.GetKey() == "" {
			return fmt.Errorf("entry %d is missing its alias or key", index)
		}
	}
	return nil
}

func countDefaultsStatus(entries []*kmsv1.DefaultsApplyEntry, status string) int {
	count := 0
	for _, entry := range entries {
		if entry != nil && entry.GetStatus() == status {
			count++
		}
	}
	return count
}

func (c *CLI) writeDefaultsResult(heading string, resp *kmsv1.ApplyApplicationDefaultsResponse) error {
	entries := append([]*kmsv1.DefaultsApplyEntry(nil), resp.GetEntries()...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].GetAlias() == entries[j].GetAlias() {
			return entries[i].GetKey() < entries[j].GetKey()
		}
		return entries[i].GetAlias() < entries[j].GetAlias()
	})
	missingSecrets := append([]string(nil), resp.GetMissingSecrets()...)
	sort.Strings(missingSecrets)

	if _, err := fmt.Fprintf(c.Stdout, "%s defaults\nProfile: %s\nPlan digest: %s\nDefinition changed: %t\nDefinition updated: %t\n", heading, resp.GetProfile(), resp.GetPlanDigest(), resp.GetDefinitionChanged(), resp.GetDefinitionUpdated()); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(c.Stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "STATUS\tALIAS\tKEY\tCONTENT TYPE\tCURRENT\tAPPLIED\tREVISION"); err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\n",
			entry.GetStatus(), entry.GetAlias(), entry.GetKey(), entry.GetContentType(),
			entry.GetCurrentVersion(), entry.GetAppliedVersion(), entry.GetRevision()); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	statuses := []string{"create", "unchanged", "update", "blocked"}
	parts := make([]string, 0, len(statuses))
	for _, status := range statuses {
		parts = append(parts, fmt.Sprintf("%s=%d", status, countDefaultsStatus(entries, status)))
	}
	if _, err := fmt.Fprintf(c.Stdout, "Summary: %s\n", strings.Join(parts, " ")); err != nil {
		return err
	}
	if len(missingSecrets) > 0 {
		if _, err := fmt.Fprintf(c.Stdout, "Missing secrets: %s\n", strings.Join(missingSecrets, ", ")); err != nil {
			return err
		}
	}
	return nil
}
