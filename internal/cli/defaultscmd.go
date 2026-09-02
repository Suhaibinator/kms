package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

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

Connection flags (--endpoint, --token, --ca, --cert, --key, --insecure) are shared
by every command here and documented in "defaults apply -h"; each has a KMS_*
environment fallback.
`)
}

func (c *CLI) defaultsUsageError(format string, args ...any) int {
	_, _ = fmt.Fprintf(c.Stderr, "error: "+format+"\n\n", args...)
	c.defaultsUsage()
	return 2
}

func (c *CLI) cmdDefaultsApply(args []string) int {
	fs := c.newFlags("defaults apply")
	cf := addConnFlags(c, fs)
	from := fs.String("from", "", "defaults artifact `file`, or - for stdin")
	overwrite := fs.Bool("overwrite", false, "permit updates to differing existing parameters")
	updateDefinition := fs.Bool("update-definition", false, "permit application contract and schema pin updates")
	execute := fs.Bool("execute", false, "apply after a fresh preview")
	confirmProduction := fs.String("confirm-production", "", "production `environment` name, repeated to confirm --execute")
	c.setUsage(fs, "defaults apply ENV/APP --from FILE|- [flags]",
		"Preview the parameter defaults a generated artifact would write, or apply them with --execute.", false)
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
		return c.failErr("reading defaults artifact", err)
	}

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
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
		return c.failErr("previewing defaults", err)
	}
	if err := validateDefaultsResponse(preview, false); err != nil {
		return c.fail("invalid defaults preview response: %v", err)
	}
	if !*execute {
		return c.reportDefaultsResult("Preview", "writing defaults preview", preview)
	}
	// With --execute the run ends in an applied result, and JSON mode may put
	// only one document on stdout: the preview stays a human-only step there.
	if !c.jsonOutput() {
		if code := c.reportDefaultsResult("Preview", "writing defaults preview", preview); code != exitOK {
			return code
		}
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
		return c.failErr("applying defaults", err)
	}
	if err := validateDefaultsResponse(applied, true); err != nil {
		return c.fail("invalid defaults apply response: %v", err)
	}
	return c.reportDefaultsResult("Applied", "writing defaults result", applied)
}

// dialConn honours the test transport override, otherwise dials the
// endpoint named by the connection flags. Connection settings are finalized
// first either way so token-file handling is exercised under the override.
func (c *CLI) dialConn(cf *connFlags) (*grpc.ClientConn, error) {
	if err := cf.finalize(); err != nil {
		return nil, err
	}
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

// defaultsStatuses is the fixed status vocabulary, in the order the summary
// line and the JSON counts report it.
var defaultsStatuses = []string{"create", "unchanged", "update", "blocked"}

// sortedDefaultsEntries orders a response's entries by alias then key so both
// renderers present the same plan in the same order every run.
func sortedDefaultsEntries(resp *kmsv1.ApplyApplicationDefaultsResponse) []*kmsv1.DefaultsApplyEntry {
	entries := append([]*kmsv1.DefaultsApplyEntry(nil), resp.GetEntries()...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].GetAlias() == entries[j].GetAlias() {
			return entries[i].GetKey() < entries[j].GetKey()
		}
		return entries[i].GetAlias() < entries[j].GetAlias()
	})
	return entries
}

// defaultsEntryJSON is one planned parameter write. The artifact's values are
// never echoed: an entry names the parameter and what would happen to it.
type defaultsEntryJSON struct {
	Status         string `json:"status"`
	Alias          string `json:"alias"`
	Key            string `json:"key"`
	ContentType    string `json:"content_type"`
	CurrentVersion uint64 `json:"current_version"`
	AppliedVersion uint64 `json:"applied_version"`
	Revision       uint64 `json:"revision"`
}

// defaultsCountsJSON is the summary line as fields.
type defaultsCountsJSON struct {
	Create    int `json:"create"`
	Unchanged int `json:"unchanged"`
	Update    int `json:"update"`
	Blocked   int `json:"blocked"`
}

// defaultsApplyJSON is one `defaults apply` result. executed distinguishes a
// preview from the applied run, which is what the table says in its heading.
type defaultsApplyJSON struct {
	Profile           string              `json:"profile"`
	PlanDigest        string              `json:"plan_digest"`
	Executed          bool                `json:"executed"`
	DefinitionChanged bool                `json:"definition_changed"`
	DefinitionUpdated bool                `json:"definition_updated"`
	Entries           []defaultsEntryJSON `json:"entries"`
	MissingSecrets    []string            `json:"missing_secrets"`
	Counts            defaultsCountsJSON  `json:"counts"`
}

func defaultsApplyJSONOf(resp *kmsv1.ApplyApplicationDefaultsResponse) defaultsApplyJSON {
	entries := sortedDefaultsEntries(resp)
	missingSecrets := append([]string{}, resp.GetMissingSecrets()...)
	sort.Strings(missingSecrets)
	document := defaultsApplyJSON{
		Profile:           resp.GetProfile(),
		PlanDigest:        resp.GetPlanDigest(),
		Executed:          resp.GetExecuted(),
		DefinitionChanged: resp.GetDefinitionChanged(),
		DefinitionUpdated: resp.GetDefinitionUpdated(),
		Entries:           make([]defaultsEntryJSON, 0, len(entries)),
		MissingSecrets:    missingSecrets,
		Counts: defaultsCountsJSON{
			Create:    countDefaultsStatus(entries, "create"),
			Unchanged: countDefaultsStatus(entries, "unchanged"),
			Update:    countDefaultsStatus(entries, "update"),
			Blocked:   countDefaultsStatus(entries, "blocked"),
		},
	}
	for _, entry := range entries {
		document.Entries = append(document.Entries, defaultsEntryJSON{
			Status:         entry.GetStatus(),
			Alias:          entry.GetAlias(),
			Key:            entry.GetKey(),
			ContentType:    entry.GetContentType(),
			CurrentVersion: entry.GetCurrentVersion(),
			AppliedVersion: entry.GetAppliedVersion(),
			Revision:       entry.GetRevision(),
		})
	}
	return document
}

// reportDefaultsResult renders one result: the JSON document, or the human
// table. failPrefix names the step for the error a broken stdout produces.
func (c *CLI) reportDefaultsResult(heading, failPrefix string, resp *kmsv1.ApplyApplicationDefaultsResponse) int {
	if c.jsonOutput() {
		return c.printJSON(defaultsApplyJSONOf(resp))
	}
	if err := c.writeDefaultsResult(heading, resp); err != nil {
		return c.failErr(failPrefix, err)
	}
	return exitOK
}

func (c *CLI) writeDefaultsResult(heading string, resp *kmsv1.ApplyApplicationDefaultsResponse) error {
	entries := sortedDefaultsEntries(resp)
	missingSecrets := append([]string(nil), resp.GetMissingSecrets()...)
	sort.Strings(missingSecrets)

	if _, err := fmt.Fprintf(c.Stdout, "%s defaults\nProfile: %s\nPlan digest: %s\nDefinition changed: %t\nDefinition updated: %t\n", heading, resp.GetProfile(), resp.GetPlanDigest(), resp.GetDefinitionChanged(), resp.GetDefinitionUpdated()); err != nil {
		return err
	}
	rows := make([][]string, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, []string{
			entry.GetStatus(), entry.GetAlias(), entry.GetKey(), entry.GetContentType(),
			strconv.FormatUint(entry.GetCurrentVersion(), 10),
			strconv.FormatUint(entry.GetAppliedVersion(), 10),
			strconv.FormatUint(entry.GetRevision(), 10),
		})
	}
	c.printTable([]string{"STATUS", "ALIAS", "KEY", "CONTENT TYPE", "CURRENT", "APPLIED", "REVISION"}, rows)
	parts := make([]string, 0, len(defaultsStatuses))
	for _, status := range defaultsStatuses {
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
