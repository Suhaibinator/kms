package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// aliasAssignment is one NAME=VALUE pair from a repeatable flag.
type aliasAssignment struct {
	name  string
	value string
}

// aliasAssignments is a repeatable flag whose values are NAME=VALUE pairs
// (--rename NEW=OLD, --pin ALIAS=VERSION).
type aliasAssignments []aliasAssignment

func (a *aliasAssignments) String() string {
	parts := make([]string, 0, len(*a))
	for _, item := range *a {
		parts = append(parts, item.name+"="+item.value)
	}
	return strings.Join(parts, ",")
}

func (a *aliasAssignments) Set(raw string) error {
	name, value, found := strings.Cut(raw, "=")
	if !found || name == "" || value == "" {
		return fmt.Errorf("expected NAME=VALUE, got %q", raw)
	}
	*a = append(*a, aliasAssignment{name: name, value: value})
	return nil
}

var _ flag.Value = (*aliasAssignments)(nil)

// migrationSources lists the plan entry sources in the order the summary
// prints them.
var migrationSources = []string{"preserved", "renamed", "edited", "pinned", "added", "missing", "removed"}

// cmdReleaseMigrate is the CLI counterpart of the console's schema-upgrade
// wizard: it rebuilds the source track's active release against a target
// schema, carrying every pin it can and overriding conflicts with the values
// in a defaults artifact ("our version"). A dry run is the default; --execute
// re-sends the previewed plan by digest so nothing but what was shown can be
// written.
func (c *CLI) cmdReleaseMigrate(args []string) int {
	fs := c.newFlags("release migrate")
	cf := addConnFlags(c, fs)
	var fromSchema, toSchema optionalUint64
	fs.Var(&fromSchema, "from-schema", "source release schema track (required; 0 selects the schema-free track)")
	fs.Var(&toSchema, "to-schema", "target registered schema `version` (default: the newest registered schema newer than --from-schema)")
	from := fs.String("from", "", "defaults artifact `file`, or - for stdin: its contract is the target contract and its parameters override carried values that differ")
	var renames, pins aliasAssignments
	fs.Var(&renames, "rename", "carry OLD's active pin under NEW, as `NEW=OLD` (repeatable)")
	fs.Var(&pins, "pin", "pin an alias to an exact existing version, as `ALIAS=VERSION` (repeatable)")
	metadata := fs.String("metadata-json", "", "release metadata `JSON` (default: the source release's)")
	execute := fs.Bool("execute", false, "apply after a fresh preview (a dry run is the default)")
	confirmProduction := fs.String("confirm-production", "", "production `environment` name, repeated to confirm --execute")
	c.setUsage(fs, "release migrate ENV/APP --from-schema N [--to-schema N] [--from FILE|-] [flags]",
		"Upgrade an environment's active release to a newer registered schema: carry every pin the target contract still names, "+
			"override differing values from a defaults artifact, and preview the plan (the default) or activate it with --execute. "+
			"--from-schema is required because a namespace may hold an active release on more than one schema track; "+
			"\"release list ENV/APP\" shows them.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	if !c.rejectExtraPositionals(1) {
		return 2
	}
	pos := c.args()
	if len(pos) != 1 || pos[0] == "" {
		return c.releaseUsageError("release migrate requires ENV/APP")
	}
	ns, err := parseNamespaceProto(pos[0])
	if err != nil {
		return c.releaseUsageError("invalid namespace %q: %v", pos[0], err)
	}
	if !fromSchema.set {
		return c.releaseUsageError("release migrate requires --from-schema (0 selects the schema-free track)")
	}
	if toSchema.set && toSchema.value == 0 {
		return c.releaseUsageError("--to-schema must name a registered schema version (a migration never targets the schema-free track)")
	}
	if toSchema.set && toSchema.value == fromSchema.value {
		return c.releaseUsageError("--to-schema must differ from --from-schema")
	}
	if code := c.validateProductionConfirmation(ns.GetEnv(), *execute, *confirmProduction); code != 0 {
		return code
	}
	changes, err := parseMigrationOverrides(renames, pins)
	if err != nil {
		return c.releaseUsageError("%v", err)
	}
	var artifact *configstore.DefaultsArtifact
	if *from != "" {
		raw, readErr := c.readDefaultsArtifact(*from)
		if readErr != nil {
			return c.failErr("reading defaults artifact", readErr)
		}
		parsed, parseErr := configstore.ParseDefaultsArtifact(raw)
		if parseErr != nil {
			return c.fail("invalid defaults artifact: %v", parseErr)
		}
		artifact = &parsed
	}

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	authCtx := cf.authCtx(ctx)

	target, err := c.resolveMigrationTarget(authCtx, kmsv1.NewConfigurationSchemaServiceClient(conn), ns.GetApp(), fromSchema.value, toSchema)
	if err != nil {
		return c.failErr("resolving target schema", err)
	}
	contract, err := migrationContract(artifact, target)
	if err != nil {
		return c.failUsage("%v", err)
	}
	if err := validateMigrationOverrides(changes, contract); err != nil {
		return c.releaseUsageError("%v", err)
	}
	var skipped []string
	if artifact != nil && len(artifact.Parameters) > 0 {
		skipped, err = c.applyArtifactOverrides(authCtx, kmsv1.NewConfigurationReleaseServiceClient(conn), ns, fromSchema.value, artifact, changes)
		if err != nil {
			return c.failErr("comparing artifact values with the source release", err)
		}
	}

	request := &kmsv1.MigrateApplicationReleaseRequest{
		Namespace:           ns,
		SourceSchemaVersion: fromSchema.value,
		SchemaVersion:       target.GetVersion(),
		Contract:            contract,
		Changes:             sortedMigrationChanges(changes),
		MetadataJson:        *metadata,
	}
	client := kmsv1.NewAdminServiceClient(conn)
	preview, err := client.MigrateApplicationRelease(authCtx, request)
	if err != nil {
		return c.failMigration("previewing migration", err)
	}
	if err := validateMigrationResponse(preview, false); err != nil {
		return c.fail("invalid migration preview response: %v", err)
	}
	report := migrationReport{ns: ns, sourceSchema: fromSchema.value, dryRun: !*execute, skipped: skipped, resp: preview}
	if !preview.GetValid() {
		if c.jsonOutput() {
			if code := c.printJSON(report.json()); code != exitOK {
				return code
			}
		} else {
			report.write(c.Stdout)
		}
		_, _ = fmt.Fprintf(c.Stderr, "error: the migration plan is invalid; fix the %d problem(s) above and preview again\n", len(preview.GetValidation()))
		return exitFailedPrecondition
	}
	if !*execute {
		if c.jsonOutput() {
			return c.printJSON(report.json())
		}
		report.write(c.Stdout)
		return exitOK
	}
	// With --execute the run ends in the activated result, and JSON mode may
	// put only one document on stdout: the preview becomes the confirmation
	// context on stderr there. It is a preview shown before a prompt, so it
	// is never silenced by --quiet.
	if c.jsonOutput() {
		report.write(c.Stderr)
	} else {
		report.write(c.Stdout)
	}
	action := fmt.Sprintf("upgrade %s %s from schema v%d (version %d) to schema v%d and activate the new release", namespaceDisplay(ns), preview.GetReleaseName(), fromSchema.value, preview.GetSourceVersion(), target.GetVersion())
	if ok, code := c.confirmYesNo(action); !ok {
		return code
	}
	executeRequest := &kmsv1.MigrateApplicationReleaseRequest{
		Namespace:                        ns,
		SourceSchemaVersion:              fromSchema.value,
		SchemaVersion:                    target.GetVersion(),
		Contract:                         contract,
		Changes:                          sortedMigrationChanges(changes),
		MetadataJson:                     *metadata,
		Execute:                          true,
		PlanDigest:                       preview.GetPlanDigest(),
		ExpectedSourceVersion:            uint64Ptr(preview.GetSourceVersion()),
		ExpectedSourceActivationRevision: uint64Ptr(preview.GetSourceActivationRevision()),
	}
	applied, err := client.MigrateApplicationRelease(authCtx, executeRequest)
	if err != nil {
		return c.failMigration("migrating release", err)
	}
	if err := validateMigrationResponse(applied, true); err != nil {
		return c.fail("invalid migration response: %v", err)
	}
	report.resp, report.dryRun = applied, false
	if c.jsonOutput() {
		return c.printJSON(report.json())
	}
	_, _ = fmt.Fprintf(c.Stdout, "Activated %s@%d on schema v%d (revision %d)\n",
		applied.GetReleaseName(), applied.GetRelease().GetVersion(), applied.GetSchemaVersion(), applied.GetActivation().GetActivationRevision())
	return exitOK
}

func uint64Ptr(v uint64) *uint64 { return &v }

// failMigration reports an RPC failure. A lost compare-and-swap (Aborted)
// means the source release or the plan changed since the preview, so the
// message tells the operator what to do rather than echoing the status.
func (c *CLI) failMigration(step string, err error) int {
	if st, ok := status.FromError(err); ok && st.Code() == codes.Aborted {
		_, _ = fmt.Fprintf(c.Stderr, "error: %s: the migration plan is stale; preview again (%s)\n", step, st.Message())
		return exitConflict
	}
	return c.failErr(step, err)
}

// resolveMigrationTarget lists the application's registered schemas and picks
// the target: the explicit --to-schema version, or the newest version above
// the source track. ListSchemas (not GetSchema) is used because it needs no
// release name, which the CLI does not know, and every row already carries
// the digest and the established contract.
func (c *CLI) resolveMigrationTarget(ctx context.Context, client kmsv1.ConfigurationSchemaServiceClient, application string, sourceSchema uint64, toSchema optionalUint64) (*kmsv1.ConfigurationSchema, error) {
	var newest *kmsv1.ConfigurationSchema
	known := []string{}
	pageToken := ""
	for {
		resp, err := client.ListSchemas(ctx, &kmsv1.ListSchemasRequest{Application: application, PageSize: 100, PageToken: pageToken})
		if err != nil {
			return nil, err
		}
		for _, schema := range resp.GetSchemas() {
			known = append(known, strconv.FormatUint(schema.GetVersion(), 10))
			if toSchema.set {
				if schema.GetVersion() == toSchema.value {
					return schema, nil
				}
				continue
			}
			if schema.GetVersion() > sourceSchema && (newest == nil || schema.GetVersion() > newest.GetVersion()) {
				newest = schema
			}
		}
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			break
		}
	}
	sort.Strings(known)
	if toSchema.set {
		return nil, domain.Errorf(domain.ErrNotFound, "application %q has no registered schema version %d (registered: %s)", application, toSchema.value, orNone(strings.Join(known, ", ")))
	}
	if newest == nil {
		return nil, domain.Errorf(domain.ErrFailedPrecondition, "application %q has no registered schema newer than v%d (registered: %s); register one with \"release schema create\" or pass --to-schema", application, sourceSchema, orNone(strings.Join(known, ", ")))
	}
	return newest, nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// migrationContract returns the complete target contract: the artifact's when
// one was supplied (after checking its schema digest names the target
// schema), otherwise the target schema's established contract.
func migrationContract(artifact *configstore.DefaultsArtifact, target *kmsv1.ConfigurationSchema) ([]*kmsv1.ApplicationContractField, error) {
	if artifact == nil {
		if !target.GetContractEstablished() {
			return nil, fmt.Errorf("schema v%d has no registered contract; pass --from FILE|- with a defaults artifact that carries the target contract", target.GetVersion())
		}
		contract := make([]*kmsv1.ApplicationContractField, 0, len(target.GetContract()))
		for _, field := range target.GetContract() {
			contract = append(contract, &kmsv1.ApplicationContractField{Alias: field.GetAlias(), Kind: field.GetKind(), ContentType: field.GetContentType()})
		}
		return contract, nil
	}
	if artifact.SchemaSHA256 != "" && !strings.EqualFold(artifact.SchemaSHA256, target.GetDigest()) {
		return nil, fmt.Errorf("the defaults artifact was generated for schema digest %s but target schema v%d has digest %s; regenerate the artifact from the target schema or pass --to-schema", artifact.SchemaSHA256, target.GetVersion(), target.GetDigest())
	}
	contract := make([]*kmsv1.ApplicationContractField, 0, len(artifact.Contract))
	for _, field := range artifact.Contract {
		contract = append(contract, &kmsv1.ApplicationContractField{Alias: field.Alias, Kind: string(field.Kind), ContentType: field.ContentType})
	}
	return contract, nil
}

// parseMigrationOverrides turns --rename and --pin into one change per target
// alias. A rename and a pin may address the same alias (carry OLD's key, pin
// its version); two of the same flag for one alias is a mistake.
func parseMigrationOverrides(renames, pins aliasAssignments) (map[string]*kmsv1.ApplicationMigrationChange, error) {
	changes := map[string]*kmsv1.ApplicationMigrationChange{}
	for _, rename := range renames {
		if existing := changes[rename.name]; existing != nil && existing.GetFromAlias() != "" {
			return nil, fmt.Errorf("--rename names %q twice", rename.name)
		}
		if rename.name == rename.value {
			return nil, fmt.Errorf("--rename %s=%s renames an alias to itself", rename.name, rename.value)
		}
		change := changes[rename.name]
		if change == nil {
			change = &kmsv1.ApplicationMigrationChange{Alias: rename.name}
			changes[rename.name] = change
		}
		change.FromAlias = rename.value
	}
	for _, pin := range pins {
		version, err := parseVersion(pin.value)
		if err != nil {
			return nil, fmt.Errorf("--pin %s=%s: version %v", pin.name, pin.value, err)
		}
		if existing := changes[pin.name]; existing != nil && existing.GetVersion() != 0 {
			return nil, fmt.Errorf("--pin names %q twice", pin.name)
		}
		change := changes[pin.name]
		if change == nil {
			change = &kmsv1.ApplicationMigrationChange{Alias: pin.name}
			changes[pin.name] = change
		}
		change.Version = version
	}
	return changes, nil
}

// validateMigrationOverrides rejects renames and pins that address aliases
// the target contract does not name, before any RPC is made.
func validateMigrationOverrides(changes map[string]*kmsv1.ApplicationMigrationChange, contract []*kmsv1.ApplicationContractField) error {
	fields := contractFields(contract)
	aliases := make([]string, 0, len(changes))
	for alias := range changes {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		if _, ok := fields[alias]; !ok {
			return fmt.Errorf("--rename/--pin alias %q is not in the target contract", alias)
		}
	}
	return nil
}

func contractFields(contract []*kmsv1.ApplicationContractField) map[string]*kmsv1.ApplicationContractField {
	fields := make(map[string]*kmsv1.ApplicationContractField, len(contract))
	for _, field := range contract {
		fields[field.GetAlias()] = field
	}
	return fields
}

// applyArtifactOverrides decides which artifact parameters must be written
// on the target track. Every artifact value is hashed locally and compared,
// value-free, with the pin the source release carries for that alias (the
// renamed-from alias when --rename applies): a match keeps the carried pin,
// anything else sends the artifact value so the migration edits or adds the
// parameter. An explicit --pin wins over the artifact. The returned list
// names the aliases whose artifact value already matched.
func (c *CLI) applyArtifactOverrides(ctx context.Context, client kmsv1.ConfigurationReleaseServiceClient, ns *kmsv1.NamespaceRef, sourceSchema uint64, artifact *configstore.DefaultsArtifact, changes map[string]*kmsv1.ApplicationMigrationChange) ([]string, error) {
	type candidate struct {
		parameter   configstore.DefaultsParameter
		sourceAlias string
	}
	candidates := map[string]candidate{}
	entries := make([]*kmsv1.VerifyEntry, 0, len(artifact.Parameters))
	for _, parameter := range artifact.Parameters {
		// ParseDefaultsArtifact already guarantees every parameter names a
		// parameter field of the artifact's own contract with the same
		// content type, and that contract is the one being sent.
		if change := changes[parameter.Alias]; change != nil && change.GetVersion() != 0 {
			c.info("%s: --pin overrides the artifact value", parameter.Alias)
			continue
		}
		sourceAlias := parameter.Alias
		if change := changes[parameter.Alias]; change != nil && change.GetFromAlias() != "" {
			sourceAlias = change.GetFromAlias()
		}
		if _, dup := candidates[sourceAlias]; dup {
			return nil, fmt.Errorf("artifact parameters %q and %q both carry from source alias %q", candidates[sourceAlias].parameter.Alias, parameter.Alias, sourceAlias)
		}
		sum, err := configstore.ParameterHash(parameter.ContentType, []byte(parameter.Value))
		if err != nil {
			return nil, fmt.Errorf("hashing parameter %q: %w", parameter.Alias, err)
		}
		candidates[sourceAlias] = candidate{parameter: parameter, sourceAlias: sourceAlias}
		entries = append(entries, &kmsv1.VerifyEntry{Alias: sourceAlias, ContentType: parameter.ContentType, Sha256: sum})
	}
	if len(entries) == 0 {
		return []string{}, nil
	}
	resp, err := client.VerifyReleaseDefaults(ctx, &kmsv1.VerifyReleaseDefaultsRequest{
		Namespace: ns, Profile: artifact.Profile, Entries: entries, SchemaVersion: uint64Ptr(sourceSchema),
	})
	if err != nil {
		return nil, err
	}
	verdicts := map[string]string{}
	for _, verdict := range resp.GetEntries() {
		verdicts[verdict.GetAlias()] = verdict.GetVerdict()
	}
	skipped := []string{}
	sourceAliases := make([]string, 0, len(candidates))
	for alias := range candidates {
		sourceAliases = append(sourceAliases, alias)
	}
	sort.Strings(sourceAliases)
	for _, sourceAlias := range sourceAliases {
		parameter := candidates[sourceAlias].parameter
		verdict, ok := verdicts[sourceAlias]
		if !ok {
			return nil, fmt.Errorf("the server returned no verdict for alias %q", sourceAlias)
		}
		switch verdict {
		case domain.VerifyVerdictMatch:
			skipped = append(skipped, parameter.Alias)
			continue
		case domain.VerifyVerdictSecretAlias:
			return nil, fmt.Errorf("alias %q is a secret in the source release; a defaults artifact never carries secrets", sourceAlias)
		case domain.VerifyVerdictDiffers, domain.VerifyVerdictMissingInRelease, domain.VerifyVerdictUnknownAlias, domain.VerifyVerdictUnsupportedContentType:
		default:
			return nil, fmt.Errorf("unexpected verdict %q for alias %q", verdict, sourceAlias)
		}
		change := changes[parameter.Alias]
		if change == nil {
			change = &kmsv1.ApplicationMigrationChange{Alias: parameter.Alias}
			changes[parameter.Alias] = change
		}
		value := parameter.Value
		change.Value, change.ContentType = &value, parameter.ContentType
	}
	return skipped, nil
}

// sortedMigrationChanges orders the changes by alias so the request, and
// therefore the plan digest, is the same on every run.
func sortedMigrationChanges(changes map[string]*kmsv1.ApplicationMigrationChange) []*kmsv1.ApplicationMigrationChange {
	out := make([]*kmsv1.ApplicationMigrationChange, 0, len(changes))
	for _, change := range changes {
		out = append(out, change)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetAlias() < out[j].GetAlias() })
	return out
}

func validateMigrationResponse(resp *kmsv1.MigrateApplicationReleaseResponse, execute bool) error {
	if resp == nil {
		return errors.New("empty response")
	}
	if resp.GetPlanDigest() == "" {
		return errors.New("missing plan digest")
	}
	if resp.GetExecuted() != execute {
		return errors.New("executed state does not match request")
	}
	if execute && (resp.GetRelease() == nil || resp.GetActivation() == nil) {
		return errors.New("missing release or activation after execute")
	}
	for index, entry := range resp.GetEntries() {
		if entry == nil {
			return fmt.Errorf("entry %d is empty", index)
		}
	}
	return nil
}

// migrationReport renders one migration response, as the human plan or as
// the JSON document.
type migrationReport struct {
	ns           *kmsv1.NamespaceRef
	sourceSchema uint64
	dryRun       bool
	skipped      []string
	resp         *kmsv1.MigrateApplicationReleaseResponse
}

func (r migrationReport) sortedEntries() []*kmsv1.ApplicationReleasePlanEntry {
	entries := append([]*kmsv1.ApplicationReleasePlanEntry(nil), r.resp.GetEntries()...)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].GetAlias() < entries[j].GetAlias() })
	return entries
}

func (r migrationReport) countSource(source string) int {
	count := 0
	for _, entry := range r.resp.GetEntries() {
		if entry.GetSource() == source {
			count++
		}
	}
	return count
}

func (r migrationReport) missingAliases() []string {
	missing := []string{}
	for _, entry := range r.sortedEntries() {
		if entry.GetSource() == "missing" {
			missing = append(missing, entry.GetAlias())
		}
	}
	return missing
}

func shortDigest(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

func planVersionText(version uint64) string {
	if version == 0 {
		return "-"
	}
	return strconv.FormatUint(version, 10)
}

func (r migrationReport) write(w io.Writer) {
	resp := r.resp
	_, _ = fmt.Fprintf(w, "Source: %s %s@%d (schema v%d, activation %d)\nTarget: schema v%d\nPlan: %s\n",
		namespaceDisplay(r.ns), resp.GetReleaseName(), resp.GetSourceVersion(), r.sourceSchema, resp.GetSourceActivationRevision(),
		resp.GetSchemaVersion(), shortDigest(resp.GetPlanDigest()))
	entries := r.sortedEntries()
	rows := make([][]string, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, []string{
			entry.GetAlias(), entry.GetKind(), entry.GetRef().GetKey(),
			planVersionText(entry.GetFromVersion()), planVersionText(entry.GetToVersion()), entry.GetSource(),
		})
	}
	writeAlignedTable(w, []string{"ALIAS", "KIND", "KEY", "FROM", "TO", "SOURCE"}, rows)
	if missing := r.missingAliases(); len(missing) > 0 {
		_, _ = fmt.Fprintf(w, "Missing: %s (no carried pin; supply a value with --from or an existing version with --pin)\n", strings.Join(missing, ", "))
	}
	if len(resp.GetValidation()) > 0 {
		_, _ = fmt.Fprintln(w, "Validation problems:")
		validationRows := make([][]string, 0, len(resp.GetValidation()))
		for _, problem := range resp.GetValidation() {
			validationRows = append(validationRows, []string{problem.GetAlias(), problem.GetCode(), problem.GetInstancePointer(), problem.GetMessage()})
		}
		writeAlignedTable(w, []string{"ALIAS", "CODE", "INSTANCE POINTER", "MESSAGE"}, validationRows)
	}
	if len(r.skipped) > 0 {
		_, _ = fmt.Fprintf(w, "Unchanged (artifact value already carried): %s\n", strings.Join(r.skipped, ", "))
	}
	parts := make([]string, 0, len(migrationSources))
	for _, source := range migrationSources {
		parts = append(parts, fmt.Sprintf("%s=%d", source, r.countSource(source)))
	}
	_, _ = fmt.Fprintf(w, "Summary: %s; valid=%t\n", strings.Join(parts, " "), resp.GetValid())
	if affected := resp.GetAffectedEnvironments(); len(affected) > 0 {
		descriptions := make([]string, 0, len(affected))
		for _, env := range affected {
			if env.GetActiveVersion() == 0 {
				descriptions = append(descriptions, env.GetEnvironment()+" (no active release)")
				continue
			}
			descriptions = append(descriptions, fmt.Sprintf("%s (active %s@%d)", env.GetEnvironment(), resp.GetReleaseName(), env.GetActiveVersion()))
		}
		_, _ = fmt.Fprintf(w, "Other environments on schema v%d: %s\n", resp.GetSchemaVersion(), strings.Join(descriptions, ", "))
	}
	if r.dryRun {
		_, _ = fmt.Fprintln(w, "Dry run: nothing was written. Re-run with --execute to apply.")
	}
}

// releaseMigrateEntryJSON is one value-free plan row.
type releaseMigrateEntryJSON struct {
	Alias       string `json:"alias"`
	Kind        string `json:"kind"`
	Key         string `json:"key"`
	FromVersion uint64 `json:"from_version"`
	ToVersion   uint64 `json:"to_version"`
	Source      string `json:"source"`
}

type releaseMigrateEnvironmentJSON struct {
	Environment   string `json:"environment"`
	ActiveVersion uint64 `json:"active_version"`
	SchemaVersion uint64 `json:"schema_version"`
}

type releaseMigrateActivationJSON struct {
	ActivationRevision uint64 `json:"activation_revision"`
	PreviousVersion    uint64 `json:"previous_version"`
	Changed            bool   `json:"changed"`
}

// releaseMigrateJSON is the one document `release migrate` prints. release
// and activation are null until the plan has been executed.
type releaseMigrateJSON struct {
	DryRun                   bool                            `json:"dry_run"`
	PlanDigest               string                          `json:"plan_digest"`
	Valid                    bool                            `json:"valid"`
	Executed                 bool                            `json:"executed"`
	DefinitionChanged        bool                            `json:"definition_changed"`
	ReleaseName              string                          `json:"release_name"`
	SourceVersion            uint64                          `json:"source_version"`
	SourceActivationRevision uint64                          `json:"source_activation_revision"`
	SchemaVersion            uint64                          `json:"schema_version"`
	SourceSchemaVersion      uint64                          `json:"source_schema_version"`
	Entries                  []releaseMigrateEntryJSON       `json:"entries"`
	Validation               []releaseValidationErrorJSON    `json:"validation"`
	AffectedEnvironments     []releaseMigrateEnvironmentJSON `json:"affected_environments"`
	Release                  *releaseShowJSON                `json:"release"`
	Activation               *releaseMigrateActivationJSON   `json:"activation"`
	SkippedOverrides         []string                        `json:"skipped_overrides"`
}

func (r migrationReport) json() releaseMigrateJSON {
	resp := r.resp
	document := releaseMigrateJSON{
		DryRun:                   r.dryRun,
		PlanDigest:               resp.GetPlanDigest(),
		Valid:                    resp.GetValid(),
		Executed:                 resp.GetExecuted(),
		DefinitionChanged:        resp.GetDefinitionChanged(),
		ReleaseName:              resp.GetReleaseName(),
		SourceVersion:            resp.GetSourceVersion(),
		SourceActivationRevision: resp.GetSourceActivationRevision(),
		SchemaVersion:            resp.GetSchemaVersion(),
		SourceSchemaVersion:      r.sourceSchema,
		Entries:                  []releaseMigrateEntryJSON{},
		Validation:               releaseValidationErrorsJSON(resp.GetValidation()),
		AffectedEnvironments:     []releaseMigrateEnvironmentJSON{},
		SkippedOverrides:         append([]string{}, r.skipped...),
	}
	for _, entry := range r.sortedEntries() {
		document.Entries = append(document.Entries, releaseMigrateEntryJSON{
			Alias: entry.GetAlias(), Kind: entry.GetKind(), Key: entry.GetRef().GetKey(),
			FromVersion: entry.GetFromVersion(), ToVersion: entry.GetToVersion(), Source: entry.GetSource(),
		})
	}
	for _, env := range resp.GetAffectedEnvironments() {
		document.AffectedEnvironments = append(document.AffectedEnvironments, releaseMigrateEnvironmentJSON{
			Environment: env.GetEnvironment(), ActiveVersion: env.GetActiveVersion(), SchemaVersion: env.GetSchemaVersion(),
		})
	}
	if release := resp.GetRelease(); release != nil {
		entries := append([]*kmsv1.ConfigurationReleaseEntry(nil), release.GetEntries()...)
		sort.Slice(entries, func(i, j int) bool { return entries[i].GetAlias() < entries[j].GetAlias() })
		shown := &releaseShowJSON{
			Namespace: namespaceRefValue(release.GetNamespace()), Name: release.GetName(),
			SchemaVersion: release.GetSchemaVersion(), Version: release.GetVersion(), Digest: release.GetDigest(),
			CreatedAt: jsonTime(release.GetCreatedAtUnixMs()), Entries: make([]releaseEntryJSON, 0, len(entries)),
		}
		if release.GetSchemaVersion() != 0 {
			shown.Schema = &releaseSchemaRef{Version: release.GetSchemaVersion()}
		}
		for _, entry := range entries {
			shown.Entries = append(shown.Entries, releaseEntryToJSON(entry))
		}
		document.Release = shown
	}
	if activation := resp.GetActivation(); activation != nil {
		document.Activation = &releaseMigrateActivationJSON{
			ActivationRevision: activation.GetActivationRevision(), PreviousVersion: activation.GetPreviousVersion(), Changed: activation.GetChanged(),
		}
	}
	return document
}
