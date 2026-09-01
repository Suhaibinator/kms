package cli

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"
)

func (c *CLI) cmdRelease(args []string) int {
	if len(args) == 0 {
		c.releaseUsage()
		return 2
	}
	switch args[0] {
	case "create":
		return c.cmdReleaseCreate(args[1:])
	case "validate":
		return c.cmdReleaseValidate(args[1:])
	case "verify-defaults":
		return c.cmdReleaseVerifyDefaults(args[1:])
	case "show":
		return c.cmdReleaseShow(args[1:])
	case "list":
		return c.cmdReleaseList(args[1:])
	case "diff":
		return c.cmdReleaseDiff(args[1:])
	case "activate":
		return c.cmdReleaseActivate(args[1:])
	case "rollback":
		return c.cmdReleaseRollback(args[1:])
	case "subscribers":
		return c.cmdReleaseSubscribers(args[1:])
	case "schema":
		return c.cmdReleaseSchema(args[1:])
	case "help", "-h", "--help":
		c.releaseUsage()
		return 0
	default:
		return c.fail("unknown release command %q", args[0])
	}
}

func (c *CLI) releaseUsage() {
	_, _ = fmt.Fprint(c.Stderr, `Usage: parameter-store release <command> [flags]

Commands:
  create FILE                         Create an immutable release from strict JSON/YAML.
  validate ENV/APP NAME VERSION       Validate resource pins and optional schema.
  verify-defaults ENV/APP             Compare a generated defaults artifact (--artifact FILE|-)
                                      with the active release by hash only (--release NAME).
  show ENV/APP NAME VERSION           Show manifest metadata (never secret values).
  list ENV/APP [NAME]                 List immutable release versions.
  diff ENV/APP NAME FROM TO           Diff aliases, pins, and parameter digests.
  activate ENV/APP NAME VERSION       Atomically activate a version.
  rollback ENV/APP NAME [VERSION]     Reactivate previous or explicit version.
  subscribers ENV/APP NAME            Show per-instance lifecycle state and lag.
  schema create|show|list              Manage immutable JSON Schemas.
`)
}

// releaseNamespaceJSON is the {env, app} pair JSON output carries wherever the
// table prints "env/app".
type releaseNamespaceJSON struct {
	Env string `json:"env"`
	App string `json:"app"`
}

func releaseNamespaceOf(ns *kmsv1.NamespaceRef) releaseNamespaceJSON {
	return releaseNamespaceJSON{Env: ns.GetEnv(), App: ns.GetApp()}
}

// releaseEntryJSON is one manifest entry as JSON. A release pins a secret by
// reference — path and version — never by value, so the JSON carries exactly
// the non-secret columns the table shows. The parameter digest is suppressed
// for secrets the same way entryDigest suppresses it for the table.
type releaseEntryJSON struct {
	Alias           string `json:"alias"`
	Kind            string `json:"kind"`
	Path            string `json:"path"`
	Version         uint64 `json:"version"`
	ContentType     string `json:"content_type"`
	ParameterDigest string `json:"parameter_digest"`
}

func releaseEntryToJSON(entry *kmsv1.ConfigurationReleaseEntry) releaseEntryJSON {
	return releaseEntryJSON{
		Alias:           entry.GetAlias(),
		Kind:            entryKind(entry),
		Path:            entryPath(entry),
		Version:         entry.GetVersion(),
		ContentType:     entry.GetContentType(),
		ParameterDigest: entryDigest(entry),
	}
}

type releaseDefinition struct {
	Namespace     string                   `json:"namespace" yaml:"namespace"`
	Name          string                   `json:"name" yaml:"name"`
	SchemaID      string                   `json:"schema_id,omitempty" yaml:"schema_id,omitempty"`
	SchemaVersion uint64                   `json:"schema_version,omitempty" yaml:"schema_version,omitempty"`
	MetadataJSON  string                   `json:"metadata_json,omitempty" yaml:"metadata_json,omitempty"`
	Entries       []releaseEntryDefinition `json:"entries" yaml:"entries"`
}

type releaseEntryDefinition struct {
	Alias   string `json:"alias" yaml:"alias"`
	Kind    string `json:"kind" yaml:"kind"`
	Key     string `json:"key" yaml:"key"`
	Version uint64 `json:"version,omitempty" yaml:"version,omitempty"`
	Label   string `json:"label,omitempty" yaml:"label,omitempty"`
}

// releaseCreateJSON reports a created release: the identity the caller asked
// for plus the immutable version and digest the server assigned.
type releaseCreateJSON struct {
	Namespace releaseNamespaceJSON `json:"namespace"`
	Name      string               `json:"name"`
	Version   uint64               `json:"version"`
	Digest    string               `json:"digest"`
}

func (c *CLI) cmdReleaseCreate(args []string) int {
	fs := c.newFlags("release create")
	cf := addConnFlags(c, fs)
	file := fs.String("file", "", "release JSON/YAML `file` ('-' for stdin)")
	c.setUsage(fs, "release create FILE [flags]",
		"Create an immutable release from a strict JSON or YAML definition.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if *file == "" && len(pos) > 0 {
		*file = pos[0]
	}
	if *file == "" {
		return c.fail("release create requires FILE or --file")
	}
	definition, err := c.readReleaseDefinition(*file)
	if err != nil {
		return c.failErr("reading release definition", err)
	}
	req, err := releaseCreateRequest(definition)
	if err != nil {
		return c.fail("invalid release definition: %v", err)
	}

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	resp, err := kmsv1.NewConfigurationReleaseServiceClient(conn).CreateRelease(cf.authCtx(ctx), req)
	if err != nil {
		return c.failErr("release create", err)
	}
	if resp.GetRelease() == nil {
		return c.fail("release create: server returned an empty release")
	}
	line := fmt.Sprintf("Created %s/%s version %d (digest %s)",
		definition.Namespace, definition.Name, resp.GetRelease().GetVersion(), resp.GetRelease().GetDigest())
	if c.jsonOutput() {
		c.info("%s", line)
		return c.printJSON(releaseCreateJSON{
			Namespace: releaseNamespaceOf(req.GetNamespace()),
			Name:      definition.Name,
			Version:   resp.GetRelease().GetVersion(),
			Digest:    resp.GetRelease().GetDigest(),
		})
	}
	_, _ = fmt.Fprintln(c.Stdout, line)
	return 0
}

func (c *CLI) readReleaseDefinition(path string) (releaseDefinition, error) {
	var reader io.Reader
	if path == "-" {
		if c.Stdin == nil {
			return releaseDefinition{}, errors.New("stdin is unavailable")
		}
		reader = c.Stdin
	} else {
		file, err := os.Open(path)
		if err != nil {
			return releaseDefinition{}, err
		}
		defer func() { _ = file.Close() }()
		reader = file
	}
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)
	var definition releaseDefinition
	if err := decoder.Decode(&definition); err != nil {
		return releaseDefinition{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return releaseDefinition{}, errors.New("multiple YAML/JSON documents are not allowed")
		}
		return releaseDefinition{}, err
	}
	return definition, nil
}

func releaseCreateRequest(definition releaseDefinition) (*kmsv1.CreateReleaseRequest, error) {
	ns, err := keyutil.ParseNamespace(definition.Namespace)
	if err != nil {
		return nil, fmt.Errorf("namespace: %w", err)
	}
	if strings.TrimSpace(definition.Name) == "" {
		return nil, errors.New("name is required")
	}
	if (definition.SchemaID == "") != (definition.SchemaVersion == 0) {
		return nil, errors.New("schema_id and schema_version must be supplied together")
	}
	if len(definition.Entries) == 0 {
		return nil, errors.New("at least one entry is required")
	}
	pns := &kmsv1.NamespaceRef{Env: ns.Env, App: ns.App}
	selectors := make([]*kmsv1.ReleaseEntrySelector, 0, len(definition.Entries))
	aliases := make(map[string]struct{}, len(definition.Entries))
	for i, entry := range definition.Entries {
		entry.Alias = strings.TrimSpace(entry.Alias)
		if entry.Alias == "" {
			return nil, fmt.Errorf("entries[%d].alias is required", i)
		}
		if _, exists := aliases[entry.Alias]; exists {
			return nil, fmt.Errorf("duplicate alias %q", entry.Alias)
		}
		aliases[entry.Alias] = struct{}{}
		if entry.Kind != "parameter" && entry.Kind != "secret" {
			return nil, fmt.Errorf("entries[%d].kind must be parameter or secret", i)
		}
		if entry.Key == "" {
			return nil, fmt.Errorf("entries[%d].key is required", i)
		}
		if entry.Version != 0 && entry.Label != "" {
			return nil, fmt.Errorf("entries[%d] may set version or label, not both", i)
		}
		var ref *kmsv1.ResourceRef
		if strings.HasPrefix(entry.Key, "/") {
			absolute, splitErr := keyutil.SplitDisplayPath(entry.Key)
			if splitErr != nil {
				return nil, fmt.Errorf("entries[%d].key: %w", i, splitErr)
			}
			ref = protoRef(absolute)
		} else {
			ref = &kmsv1.ResourceRef{Namespace: pns, Key: entry.Key}
		}
		selectors = append(selectors, &kmsv1.ReleaseEntrySelector{
			Alias: entry.Alias, Kind: entry.Kind, Ref: ref, Version: entry.Version, Label: entry.Label,
		})
	}
	return &kmsv1.CreateReleaseRequest{
		Namespace: pns, Name: definition.Name,
		SchemaId: definition.SchemaID, SchemaVersion: definition.SchemaVersion,
		Entries: selectors, MetadataJson: definition.MetadataJSON,
	}, nil
}

// releaseValidateJSON is the verdict of `release validate`. errors is always a
// list (empty when the release is valid), never null.
type releaseValidateJSON struct {
	Valid  bool                         `json:"valid"`
	Errors []releaseValidationErrorJSON `json:"errors"`
}

// releaseValidationErrorJSON carries one validation failure. The server
// sanitizes message, which never contains a resource value.
type releaseValidationErrorJSON struct {
	Alias         string `json:"alias"`
	Code          string `json:"code"`
	SchemaPointer string `json:"schema_pointer"`
	Message       string `json:"message"`
}

func releaseValidationErrorsJSON(validationErrors []*kmsv1.ReleaseValidationError) []releaseValidationErrorJSON {
	out := make([]releaseValidationErrorJSON, 0, len(validationErrors))
	for _, validationErr := range validationErrors {
		out = append(out, releaseValidationErrorJSON{
			Alias:         validationErr.GetAlias(),
			Code:          validationErr.GetCode(),
			SchemaPointer: validationErr.GetSchemaPointer(),
			Message:       validationErr.GetMessage(),
		})
	}
	return out
}

func (c *CLI) cmdReleaseValidate(args []string) int {
	fs := c.newFlags("release validate")
	cf := addConnFlags(c, fs)
	c.setUsage(fs, "release validate ENV/APP NAME VERSION [flags]",
		"Check that a release's resource pins still resolve and that pinned values satisfy its schema.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	ns, name, version, ok := c.parseReleaseIdentity(c.args(), true)
	if !ok {
		return 1
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	resp, err := kmsv1.NewConfigurationReleaseServiceClient(conn).ValidateRelease(cf.authCtx(ctx), &kmsv1.ValidateReleaseRequest{
		Namespace: ns, Name: name, Version: version,
	})
	if err != nil {
		return c.failErr("release validate", err)
	}
	if c.jsonOutput() {
		if code := c.printJSON(releaseValidateJSON{
			Valid:  resp.GetValid(),
			Errors: releaseValidationErrorsJSON(resp.GetErrors()),
		}); code != exitOK {
			return code
		}
		if resp.GetValid() {
			return exitOK
		}
		return exitError
	}
	if resp.GetValid() {
		_, _ = fmt.Fprintf(c.Stdout, "Release %s/%s version %d is valid.\n", namespaceDisplay(ns), name, version)
		return 0
	}
	printReleaseValidationErrors(c.Stdout, resp.GetErrors())
	return 1
}

func printReleaseValidationErrors(w io.Writer, validationErrors []*kmsv1.ReleaseValidationError) {
	rows := make([][]string, 0, len(validationErrors))
	for _, validationErr := range validationErrors {
		rows = append(rows, []string{
			validationErr.GetAlias(), validationErr.GetCode(),
			validationErr.GetSchemaPointer(), validationErr.GetMessage(),
		})
	}
	writeAlignedTable(w, []string{"ALIAS", "CODE", "SCHEMA POINTER", "MESSAGE"}, rows)
}

func releaseValidationDetails(err error) *kmsv1.ValidateReleaseResponse {
	for _, detail := range status.Convert(err).Details() {
		if validation, ok := detail.(*kmsv1.ValidateReleaseResponse); ok {
			return validation
		}
	}
	return nil
}

// releaseShowJSON is one release manifest. It repeats the table's header lines
// as fields (namespace, schema pin, activation labels) so a script never has to
// parse prose.
type releaseShowJSON struct {
	Namespace     releaseNamespaceJSON `json:"namespace"`
	Name          string               `json:"name"`
	Version       uint64               `json:"version"`
	Revision      uint64               `json:"revision"`
	Current       bool                 `json:"current"`
	Previous      bool                 `json:"previous"`
	SchemaID      string               `json:"schema_id"`
	SchemaVersion uint64               `json:"schema_version"`
	Digest        string               `json:"digest"`
	CreatedAt     *string              `json:"created_at"`
	Entries       []releaseEntryJSON   `json:"entries"`
}

func (c *CLI) cmdReleaseShow(args []string) int {
	fs := c.newFlags("release show")
	cf := addConnFlags(c, fs)
	c.setUsage(fs, "release show ENV/APP NAME VERSION [flags]",
		"Print a release manifest's metadata and entries; secret values are never shown.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	ns, name, version, ok := c.parseReleaseIdentity(c.args(), true)
	if !ok {
		return 1
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	resp, err := kmsv1.NewConfigurationReleaseServiceClient(conn).GetRelease(cf.authCtx(ctx), &kmsv1.GetReleaseRequest{
		Namespace: ns, Name: name, Version: version,
	})
	if err != nil {
		return c.failErr("release show", err)
	}
	return c.printRelease(resp.GetRelease(), 0, false, false)
}

func (c *CLI) printRelease(release *kmsv1.ConfigurationRelease, revision uint64, current, previous bool) int {
	if release == nil {
		return c.fail("server returned an empty release")
	}
	entries := append([]*kmsv1.ConfigurationReleaseEntry(nil), release.GetEntries()...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].GetAlias() < entries[j].GetAlias() })
	if c.jsonOutput() {
		document := releaseShowJSON{
			Namespace:     releaseNamespaceOf(release.GetNamespace()),
			Name:          release.GetName(),
			Version:       release.GetVersion(),
			Revision:      revision,
			Current:       current,
			Previous:      previous,
			SchemaID:      release.GetSchemaId(),
			SchemaVersion: release.GetSchemaVersion(),
			Digest:        release.GetDigest(),
			CreatedAt:     jsonTime(release.GetCreatedAtUnixMs()),
			Entries:       make([]releaseEntryJSON, 0, len(entries)),
		}
		for _, entry := range entries {
			document.Entries = append(document.Entries, releaseEntryToJSON(entry))
		}
		return c.printJSON(document)
	}
	_, _ = fmt.Fprintf(c.Stdout, "%s/%s version %d\n", namespaceDisplay(release.GetNamespace()), release.GetName(), release.GetVersion())
	_, _ = fmt.Fprintf(c.Stdout, "Digest: %s\n", release.GetDigest())
	if release.GetSchemaId() != "" {
		_, _ = fmt.Fprintf(c.Stdout, "Schema: %s version %d\n", release.GetSchemaId(), release.GetSchemaVersion())
	}
	if revision != 0 {
		_, _ = fmt.Fprintf(c.Stdout, "Activation revision: %d\n", revision)
	}
	if current || previous {
		_, _ = fmt.Fprintf(c.Stdout, "Labels: current=%t previous=%t\n", current, previous)
	}
	rows := make([][]string, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, []string{
			entry.GetAlias(), entry.GetKind(), displayPath(entry.GetRef()),
			strconv.FormatUint(entry.GetVersion(), 10), entry.GetContentType(), entry.GetParameterDigest(),
		})
	}
	c.printTable([]string{"ALIAS", "KIND", "PATH", "VERSION", "CONTENT TYPE", "PARAMETER DIGEST"}, rows)
	return 0
}

// releaseListItemJSON is one row of `release list`: every table column plus the
// creation time, which the table has no room for.
type releaseListItemJSON struct {
	Name      string  `json:"name"`
	Version   uint64  `json:"version"`
	Current   bool    `json:"current"`
	Previous  bool    `json:"previous"`
	Revision  uint64  `json:"revision"`
	Digest    string  `json:"digest"`
	CreatedAt *string `json:"created_at"`
}

func (c *CLI) cmdReleaseList(args []string) int {
	fs := c.newFlags("release list")
	cf := addConnFlags(c, fs)
	pageSize := fs.Int("page-size", 100, "result `count` per RPC")
	c.setUsage(fs, "release list ENV/APP [NAME] [flags]",
		"List immutable release versions and which one is active.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 1 {
		return c.fail("release list requires ENV/APP")
	}
	ns, err := parseNamespaceProto(pos[0])
	if err != nil {
		return c.fail("invalid namespace: %v", err)
	}
	name := ""
	if len(pos) > 1 {
		name = pos[1]
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	client := kmsv1.NewConfigurationReleaseServiceClient(conn)
	rows := [][]string{}
	items := []releaseListItemJSON{}
	pageToken := ""
	for {
		resp, listErr := client.ListReleases(cf.authCtx(ctx), &kmsv1.ListReleasesRequest{
			Namespace: ns, Name: name, PageSize: int32(*pageSize), PageToken: pageToken,
		})
		if listErr != nil {
			return c.failErr("release list", listErr)
		}
		for _, summary := range resp.GetReleases() {
			release := summary.GetRelease()
			if c.jsonOutput() {
				items = append(items, releaseListItemJSON{
					Name:      release.GetName(),
					Version:   release.GetVersion(),
					Current:   summary.GetCurrent(),
					Previous:  summary.GetPrevious(),
					Revision:  summary.GetActivationRevision(),
					Digest:    release.GetDigest(),
					CreatedAt: jsonTime(release.GetCreatedAtUnixMs()),
				})
				continue
			}
			rows = append(rows, []string{
				release.GetName(), strconv.FormatUint(release.GetVersion(), 10),
				strconv.FormatBool(summary.GetCurrent()), strconv.FormatBool(summary.GetPrevious()),
				strconv.FormatUint(summary.GetActivationRevision(), 10), release.GetDigest(),
			})
		}
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			break
		}
	}
	if c.jsonOutput() {
		// The loop above followed every page, so the result set is complete and
		// there is no token to hand back.
		return c.printList(items, "")
	}
	c.printTable([]string{"NAME", "VERSION", "CURRENT", "PREVIOUS", "REVISION", "DIGEST"}, rows)
	return 0
}

// releaseVersionJSON identifies one side of a diff.
type releaseVersionJSON struct {
	Name    string `json:"name"`
	Version uint64 `json:"version"`
}

// releaseEntryChange is an alias present in both releases whose pin moved.
type releaseEntryChange struct {
	Alias string           `json:"alias"`
	From  releaseEntryJSON `json:"from"`
	To    releaseEntryJSON `json:"to"`
}

// releaseDiff is the alias-keyed difference between two release manifests. It
// is computed once and rendered three ways — the JSON document, the diff
// table, and the preview `release activate` prints before it asks for
// confirmation — so all three can never disagree.
type releaseDiff struct {
	From    releaseVersionJSON   `json:"from"`
	To      releaseVersionJSON   `json:"to"`
	Added   []releaseEntryJSON   `json:"added"`
	Removed []releaseEntryJSON   `json:"removed"`
	Changed []releaseEntryChange `json:"changed"`
}

func (c *CLI) cmdReleaseDiff(args []string) int {
	fs := c.newFlags("release diff")
	cf := addConnFlags(c, fs)
	c.setUsage(fs, "release diff ENV/APP NAME FROM_VERSION TO_VERSION [flags]",
		"Compare two versions of a release by alias, pin, and parameter digest.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) != 4 {
		return c.fail("release diff requires ENV/APP NAME FROM_VERSION TO_VERSION")
	}
	ns, err := parseNamespaceProto(pos[0])
	if err != nil {
		return c.fail("invalid namespace: %v", err)
	}
	fromVersion, err := parseVersion(pos[2])
	if err != nil {
		return c.fail("invalid FROM_VERSION: %v", err)
	}
	toVersion, err := parseVersion(pos[3])
	if err != nil {
		return c.fail("invalid TO_VERSION: %v", err)
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	client := kmsv1.NewConfigurationReleaseServiceClient(conn)
	fromResp, err := client.GetRelease(cf.authCtx(ctx), &kmsv1.GetReleaseRequest{Namespace: ns, Name: pos[1], Version: fromVersion})
	if err != nil {
		return c.failErr(fmt.Sprintf("release diff: reading version %d", fromVersion), err)
	}
	toResp, err := client.GetRelease(cf.authCtx(ctx), &kmsv1.GetReleaseRequest{Namespace: ns, Name: pos[1], Version: toVersion})
	if err != nil {
		return c.failErr(fmt.Sprintf("release diff: reading version %d", toVersion), err)
	}
	diff := computeReleaseDiff(fromResp.GetRelease(), toResp.GetRelease())
	if c.jsonOutput() {
		return c.printJSON(diff)
	}
	writeReleaseDiff(c.Stdout, diff)
	return 0
}

// computeReleaseDiff pairs the two manifests by alias. It is pure: nothing is
// rendered or fetched here, so the JSON document, the table, and the activation
// preview all describe the same comparison.
func computeReleaseDiff(from, to *kmsv1.ConfigurationRelease) releaseDiff {
	fromEntries := make(map[string]*kmsv1.ConfigurationReleaseEntry)
	toEntries := make(map[string]*kmsv1.ConfigurationReleaseEntry)
	aliases := make(map[string]struct{})
	if from != nil {
		for _, entry := range from.GetEntries() {
			fromEntries[entry.GetAlias()] = entry
			aliases[entry.GetAlias()] = struct{}{}
		}
	}
	if to != nil {
		for _, entry := range to.GetEntries() {
			toEntries[entry.GetAlias()] = entry
			aliases[entry.GetAlias()] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(aliases))
	for alias := range aliases {
		ordered = append(ordered, alias)
	}
	sort.Strings(ordered)
	diff := releaseDiff{
		From:    releaseVersionJSON{Name: from.GetName(), Version: from.GetVersion()},
		To:      releaseVersionJSON{Name: to.GetName(), Version: to.GetVersion()},
		Added:   []releaseEntryJSON{},
		Removed: []releaseEntryJSON{},
		Changed: []releaseEntryChange{},
	}
	for _, alias := range ordered {
		before, hadBefore := fromEntries[alias]
		after, hasAfter := toEntries[alias]
		switch {
		case !hadBefore:
			diff.Added = append(diff.Added, releaseEntryToJSON(after))
		case !hasAfter:
			diff.Removed = append(diff.Removed, releaseEntryToJSON(before))
		case !releaseEntriesEqual(before, after):
			diff.Changed = append(diff.Changed, releaseEntryChange{
				Alias: alias, From: releaseEntryToJSON(before), To: releaseEntryToJSON(after),
			})
		}
	}
	return diff
}

// writeReleaseDiff renders a computed diff as one alias-ordered table. Aliases
// are unique across the three categories, so sorting the merged rows restores
// the single ordering the diff has always printed.
func writeReleaseDiff(w io.Writer, diff releaseDiff) {
	rows := make([][]string, 0, len(diff.Added)+len(diff.Removed)+len(diff.Changed))
	var absent releaseEntryJSON
	for _, entry := range diff.Added {
		rows = append(rows, releaseDiffRow(entry.Alias, "added", absent, false, entry, true))
	}
	for _, entry := range diff.Removed {
		rows = append(rows, releaseDiffRow(entry.Alias, "removed", entry, true, absent, false))
	}
	for _, change := range diff.Changed {
		rows = append(rows, releaseDiffRow(change.Alias, "changed", change.From, true, change.To, true))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
	writeAlignedTable(w, []string{"ALIAS", "CHANGE", "KIND", "PATH", "VERSION", "PARAMETER DIGEST"}, rows)
}

// releaseDiffRow renders one row; each cell shows "before -> after" when the
// two sides differ. A side the alias is missing from renders as empty cells.
func releaseDiffRow(alias, change string, before releaseEntryJSON, hadBefore bool, after releaseEntryJSON, hasAfter bool) []string {
	return []string{
		alias, change,
		diffText(before.Kind, after.Kind),
		diffText(before.Path, after.Path),
		diffText(diffVersionText(before.Version, hadBefore), diffVersionText(after.Version, hasAfter)),
		diffText(before.ParameterDigest, after.ParameterDigest),
	}
}

// diffVersionText renders a pin version, leaving the cell blank (rather than
// "0") for the side an alias is absent from.
func diffVersionText(version uint64, present bool) string {
	if !present {
		return ""
	}
	return strconv.FormatUint(version, 10)
}

func releaseEntriesEqual(a, b *kmsv1.ConfigurationReleaseEntry) bool {
	return entryKind(a) == entryKind(b) && entryPath(a) == entryPath(b) &&
		entryVersion(a) == entryVersion(b) && entryDigest(a) == entryDigest(b)
}

func entryKind(entry *kmsv1.ConfigurationReleaseEntry) string {
	if entry == nil {
		return ""
	}
	return entry.GetKind()
}
func entryPath(entry *kmsv1.ConfigurationReleaseEntry) string {
	if entry == nil {
		return ""
	}
	return displayPath(entry.GetRef())
}
func entryVersion(entry *kmsv1.ConfigurationReleaseEntry) string {
	if entry == nil {
		return ""
	}
	return strconv.FormatUint(entry.GetVersion(), 10)
}
func entryDigest(entry *kmsv1.ConfigurationReleaseEntry) string {
	if entry == nil || entry.GetKind() != "parameter" {
		return ""
	}
	return entry.GetParameterDigest()
}
func diffText(before, after string) string {
	if before == after {
		return before
	}
	return before + " -> " + after
}

type optionalUint64 struct {
	value uint64
	set   bool
}

func (v *optionalUint64) String() string { return strconv.FormatUint(v.value, 10) }
func (v *optionalUint64) Set(raw string) error {
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return err
	}
	v.value, v.set = parsed, true
	return nil
}

// releaseActivationJSON is the outcome of an activation. activate and rollback
// share it because they are the same RPC seen from two directions.
type releaseActivationJSON struct {
	Namespace       releaseNamespaceJSON `json:"namespace"`
	Name            string               `json:"name"`
	Version         uint64               `json:"version"`
	PreviousVersion uint64               `json:"previous_version"`
	Revision        uint64               `json:"revision"`
	Changed         bool                 `json:"changed"`
}

func releaseActivationOf(ns *kmsv1.NamespaceRef, name string, resp *kmsv1.ActivateReleaseResponse) releaseActivationJSON {
	return releaseActivationJSON{
		Namespace:       releaseNamespaceOf(ns),
		Name:            name,
		Version:         resp.GetCurrentVersion(),
		PreviousVersion: resp.GetPreviousVersion(),
		Revision:        resp.GetActivationRevision(),
		Changed:         resp.GetChanged(),
	}
}

func (c *CLI) cmdReleaseActivate(args []string) int {
	fs := c.newFlags("release activate")
	cf := addConnFlags(c, fs)
	var expected optionalUint64
	fs.Var(&expected, "expected-current-version", "CAS guard: expected active `version` (0 means expect no active release)")
	c.setUsage(fs, "release activate ENV/APP NAME VERSION [flags]",
		"Atomically make a release version the active one.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	ns, name, version, ok := c.parseReleaseIdentity(c.args(), true)
	if !ok {
		return 1
	}
	req := &kmsv1.ActivateReleaseRequest{Namespace: ns, Name: name, Version: version}
	if expected.set {
		req.ExpectedCurrentVersion = &expected.value
	}

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	client := kmsv1.NewConfigurationReleaseServiceClient(conn)
	if code := c.previewReleaseActivation(ctx, cf, client, ns, name, version); code != exitOK {
		return code
	}
	if ok, code := c.confirmYesNo(fmt.Sprintf("activate release %s v%d in %s", name, version, namespaceDisplay(ns))); !ok {
		return code
	}
	resp, err := client.ActivateRelease(cf.authCtx(ctx), req)
	if err != nil {
		return c.failReleaseActivation("activate", err)
	}
	line := fmt.Sprintf("Active %s/%s version %d (previous %d, revision %d, changed=%t)",
		namespaceDisplay(ns), name, resp.GetCurrentVersion(), resp.GetPreviousVersion(), resp.GetActivationRevision(), resp.GetChanged())
	if c.jsonOutput() {
		c.info("%s", line)
		return c.printJSON(releaseActivationOf(ns, name, resp))
	}
	_, _ = fmt.Fprintln(c.Stdout, line)
	return 0
}

// previewReleaseActivation shows what the activation would change: the diff
// from the currently active release to the requested one, or a note that the
// namespace has none yet. This is the thing the operator confirms against, so
// it goes straight to stderr and --quiet never suppresses it. A namespace with
// no active release answers NotFound, which is not an error here.
func (c *CLI) previewReleaseActivation(ctx context.Context, cf *connFlags, client kmsv1.ConfigurationReleaseServiceClient, ns *kmsv1.NamespaceRef, name string, version uint64) int {
	active, err := client.GetActiveRelease(cf.authCtx(ctx), &kmsv1.GetActiveReleaseRequest{Namespace: ns, Name: name})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			c.printNoActiveRelease(ns, name, version)
			return exitOK
		}
		return c.failErr("release activate: reading the active release", err)
	}
	if active.GetRelease() == nil {
		c.printNoActiveRelease(ns, name, version)
		return exitOK
	}
	requested, err := client.GetRelease(cf.authCtx(ctx), &kmsv1.GetReleaseRequest{Namespace: ns, Name: name, Version: version})
	if err != nil {
		return c.failErr(fmt.Sprintf("release activate: reading version %d", version), err)
	}
	_, _ = fmt.Fprintf(c.Stderr, "Activating %s v%d in %s over the active v%d:\n",
		name, version, namespaceDisplay(ns), active.GetRelease().GetVersion())
	writeReleaseDiff(c.Stderr, computeReleaseDiff(active.GetRelease(), requested.GetRelease()))
	return exitOK
}

func (c *CLI) printNoActiveRelease(ns *kmsv1.NamespaceRef, name string, version uint64) {
	_, _ = fmt.Fprintf(c.Stderr, "No active release in %s; %s v%d will become the first.\n", namespaceDisplay(ns), name, version)
}

// failReleaseActivation reports a refused activation. A validation failure
// arrives as FailedPrecondition carrying a ValidateReleaseResponse detail: the
// individual errors are printed for the operator, and the exit code stays the
// one the status maps to (7) so a script can tell "invalid release" from
// "server unreachable".
func (c *CLI) failReleaseActivation(verb string, err error) int {
	if validation := releaseValidationDetails(err); validation != nil {
		_, _ = fmt.Fprintf(c.Stderr, "error: release %s: configuration release validation failed\n", verb)
		printReleaseValidationErrors(c.Stderr, validation.GetErrors())
		return exitCodeFor(err)
	}
	return c.failErr("release "+verb, err)
}

func (c *CLI) cmdReleaseRollback(args []string) int {
	fs := c.newFlags("release rollback")
	cf := addConnFlags(c, fs)
	c.setUsage(fs, "release rollback ENV/APP NAME [VERSION] [flags]",
		"Reactivate the previous release version, or an explicit one.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) < 2 || len(pos) > 3 {
		return c.fail("release rollback requires ENV/APP NAME [VERSION]")
	}
	ns, err := parseNamespaceProto(pos[0])
	if err != nil {
		return c.fail("invalid namespace: %v", err)
	}
	name := pos[1]

	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	client := kmsv1.NewConfigurationReleaseServiceClient(conn)
	active, err := client.GetActiveRelease(cf.authCtx(ctx), &kmsv1.GetActiveReleaseRequest{Namespace: ns, Name: name})
	if err != nil {
		return c.failErr("release rollback: reading active release", err)
	}
	target := active.GetPreviousVersion()
	if len(pos) == 3 {
		target, err = parseVersion(pos[2])
		if err != nil {
			return c.fail("invalid VERSION: %v", err)
		}
	}
	if target == 0 {
		return c.fail("release rollback: no previous release is available")
	}
	// Confirm last: everything above only reads, so the operator is asked only
	// once there is a target to roll back to.
	if ok, code := c.confirmDestructive("roll back the active release of", namespaceDisplay(ns)); !ok {
		return code
	}
	expected := active.GetRelease().GetVersion()
	resp, err := client.ActivateRelease(cf.authCtx(ctx), &kmsv1.ActivateReleaseRequest{
		Namespace: ns, Name: name, Version: target, ExpectedCurrentVersion: &expected,
	})
	if err != nil {
		return c.failReleaseActivation("rollback", err)
	}
	line := fmt.Sprintf("Rolled back %s/%s to version %d (revision %d)", namespaceDisplay(ns), name, resp.GetCurrentVersion(), resp.GetActivationRevision())
	if c.jsonOutput() {
		c.info("%s", line)
		return c.printJSON(releaseActivationOf(ns, name, resp))
	}
	_, _ = fmt.Fprintln(c.Stdout, line)
	return 0
}

func (c *CLI) cmdReleaseSubscribers(args []string) int {
	fs := c.newFlags("release subscribers")
	cf := addConnFlags(c, fs)
	pageSize := fs.Int("page-size", 100, "result `count` per RPC")
	c.setUsage(fs, "release subscribers ENV/APP NAME [flags]",
		"Show per-instance release lifecycle state and activation lag.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) != 2 {
		return c.fail("release subscribers requires ENV/APP NAME")
	}
	ns, err := parseNamespaceProto(pos[0])
	if err != nil {
		return c.fail("invalid namespace: %v", err)
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	client := kmsv1.NewAdminServiceClient(conn)
	instances := map[releaseSubscriberInstanceKey]*releaseSubscriberInstanceStatus{}
	currentRevision := uint64(0)
	pageToken := ""
	for {
		resp, listErr := client.ListReleaseSubscribers(cf.authCtx(ctx), &kmsv1.ListReleaseSubscribersRequest{
			Namespace: ns, ReleaseName: pos[1], PageSize: int32(*pageSize), PageToken: pageToken,
		})
		if listErr != nil {
			return c.failErr("release subscribers", listErr)
		}
		currentRevision = max(currentRevision, resp.GetCurrentRevision())
		mergeReleaseSubscriberStates(instances, resp.GetSubscribers())
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			break
		}
	}
	if c.jsonOutput() {
		// Every page has been followed, so there is no token to hand back.
		return c.printList(releaseSubscriberInstancesJSON(instances, currentRevision), "")
	}
	writeReleaseSubscriberInstances(c.Stdout, instances, currentRevision)
	return 0
}

type releaseSubscriberInstanceStatus struct {
	identity, client, instance string
	connected                  bool
	latestRevision             uint64
	states                     map[string]*kmsv1.ReleaseSubscriberState
}

type releaseSubscriberInstanceKey struct {
	identity string
	client   string
	instance string
}

func mergeReleaseSubscriberStates(instances map[releaseSubscriberInstanceKey]*releaseSubscriberInstanceStatus, subscribers []*kmsv1.ReleaseSubscriberState) {
	for _, subscriber := range subscribers {
		key := releaseSubscriberInstanceKey{
			identity: subscriber.GetIdentity(),
			client:   subscriber.GetClientName(),
			instance: subscriber.GetInstanceId(),
		}
		instance := instances[key]
		if instance == nil {
			instance = &releaseSubscriberInstanceStatus{
				identity: subscriber.GetIdentity(),
				client:   subscriber.GetClientName(),
				instance: subscriber.GetInstanceId(),
				states:   make(map[string]*kmsv1.ReleaseSubscriberState),
			}
			instances[key] = instance
		}
		instance.states[subscriber.GetState()] = subscriber
		instance.connected = instance.connected || subscriber.GetConnected()
		instance.latestRevision = max(instance.latestRevision, subscriber.GetActivationRevision())
	}
}

// sortedReleaseSubscriberKeys orders instances the way both renderers present
// them: by identity, then client, then instance id.
func sortedReleaseSubscriberKeys(instances map[releaseSubscriberInstanceKey]*releaseSubscriberInstanceStatus) []releaseSubscriberInstanceKey {
	keys := make([]releaseSubscriberInstanceKey, 0, len(instances))
	for key := range instances {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].identity != keys[j].identity {
			return keys[i].identity < keys[j].identity
		}
		if keys[i].client != keys[j].client {
			return keys[i].client < keys[j].client
		}
		return keys[i].instance < keys[j].instance
	})
	return keys
}

// releaseSubscriberLag is how many activations an instance is behind the
// namespace's current revision.
func releaseSubscriberLag(instance *releaseSubscriberInstanceStatus, currentRevision uint64) uint64 {
	if currentRevision > instance.latestRevision {
		return currentRevision - instance.latestRevision
	}
	return 0
}

// releaseSubscriberStateJSON is one lifecycle state an instance reported. The
// table squeezes it into "v1/r2[:category]"; JSON keeps the parts separate.
type releaseSubscriberStateJSON struct {
	ReleaseVersion     uint64 `json:"release_version"`
	ActivationRevision uint64 `json:"activation_revision"`
	RejectionCategory  string `json:"rejection_category,omitempty"`
}

// releaseSubscriberJSON is one row of `release subscribers`. A state the
// instance never reported is null, the JSON form of the table's "-".
type releaseSubscriberJSON struct {
	Identity  string                      `json:"identity"`
	Client    string                      `json:"client"`
	Instance  string                      `json:"instance"`
	Received  *releaseSubscriberStateJSON `json:"received"`
	Prepared  *releaseSubscriberStateJSON `json:"prepared"`
	Applied   *releaseSubscriberStateJSON `json:"applied"`
	Rejected  *releaseSubscriberStateJSON `json:"rejected"`
	Lag       uint64                      `json:"lag"`
	Connected bool                        `json:"connected"`
}

func releaseSubscriberStateToJSON(state *kmsv1.ReleaseSubscriberState) *releaseSubscriberStateJSON {
	if state == nil {
		return nil
	}
	return &releaseSubscriberStateJSON{
		ReleaseVersion:     state.GetReleaseVersion(),
		ActivationRevision: state.GetActivationRevision(),
		RejectionCategory:  state.GetRejectionCategory(),
	}
}

func releaseSubscriberInstancesJSON(instances map[releaseSubscriberInstanceKey]*releaseSubscriberInstanceStatus, currentRevision uint64) []releaseSubscriberJSON {
	items := make([]releaseSubscriberJSON, 0, len(instances))
	for _, key := range sortedReleaseSubscriberKeys(instances) {
		instance := instances[key]
		items = append(items, releaseSubscriberJSON{
			Identity:  instance.identity,
			Client:    instance.client,
			Instance:  instance.instance,
			Received:  releaseSubscriberStateToJSON(instance.states[domain.ReleaseStateReceived]),
			Prepared:  releaseSubscriberStateToJSON(instance.states[domain.ReleaseStatePrepared]),
			Applied:   releaseSubscriberStateToJSON(instance.states[domain.ReleaseStateApplied]),
			Rejected:  releaseSubscriberStateToJSON(instance.states[domain.ReleaseStateRejected]),
			Lag:       releaseSubscriberLag(instance, currentRevision),
			Connected: instance.connected,
		})
	}
	return items
}

func writeReleaseSubscriberInstances(w io.Writer, instances map[releaseSubscriberInstanceKey]*releaseSubscriberInstanceStatus, currentRevision uint64) {
	keys := sortedReleaseSubscriberKeys(instances)
	rows := make([][]string, 0, len(keys))
	for _, key := range keys {
		instance := instances[key]
		rows = append(rows, []string{
			instance.identity, instance.client, instance.instance,
			releaseSubscriberStateText(instance.states[domain.ReleaseStateReceived]),
			releaseSubscriberStateText(instance.states[domain.ReleaseStatePrepared]),
			releaseSubscriberStateText(instance.states[domain.ReleaseStateApplied]),
			releaseSubscriberStateText(instance.states[domain.ReleaseStateRejected]),
			strconv.FormatUint(releaseSubscriberLag(instance, currentRevision), 10),
			strconv.FormatBool(instance.connected),
		})
	}
	writeAlignedTable(w, []string{"IDENTITY", "CLIENT", "INSTANCE", "RECEIVED", "PREPARED", "APPLIED", "REJECTED", "LAG", "CONNECTED"}, rows)
}

func releaseSubscriberStateText(state *kmsv1.ReleaseSubscriberState) string {
	if state == nil {
		return "-"
	}
	value := fmt.Sprintf("v%d/r%d", state.GetReleaseVersion(), state.GetActivationRevision())
	if state.GetState() == domain.ReleaseStateRejected && state.GetRejectionCategory() != "" {
		value += ":" + state.GetRejectionCategory()
	}
	return value
}

func (c *CLI) cmdReleaseSchema(args []string) int {
	if len(args) == 0 {
		return c.fail("release schema requires create, show, or list")
	}
	switch args[0] {
	case "create":
		return c.cmdReleaseSchemaCreate(args[1:])
	case "show":
		return c.cmdReleaseSchemaShow(args[1:])
	case "list":
		return c.cmdReleaseSchemaList(args[1:])
	default:
		return c.fail("unknown release schema command %q", args[0])
	}
}

// releaseSchemaJSON identifies an immutable schema version.
type releaseSchemaJSON struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
	Digest  string `json:"digest"`
}

func (c *CLI) cmdReleaseSchemaCreate(args []string) int {
	fs := c.newFlags("release schema create")
	cf := addConnFlags(c, fs)
	metadata := fs.String("metadata-json", "", "non-sensitive metadata `json`")
	c.setUsage(fs, "release schema create ID FILE [flags]",
		"Create an immutable JSON Schema version from a JSON or YAML file.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) != 2 {
		return c.fail("release schema create requires ID FILE")
	}
	schemaJSON, err := readSchemaJSON(pos[1])
	if err != nil {
		return c.failErr("reading schema", err)
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	resp, err := kmsv1.NewConfigurationSchemaServiceClient(conn).CreateSchema(cf.authCtx(ctx), &kmsv1.CreateSchemaRequest{
		Id: pos[0], SchemaJson: schemaJSON, MetadataJson: *metadata,
	})
	if err != nil {
		return c.failErr("release schema create", err)
	}
	line := fmt.Sprintf("Created schema %s version %d (digest %s)", resp.GetSchema().GetId(), resp.GetSchema().GetVersion(), resp.GetSchema().GetDigest())
	if c.jsonOutput() {
		c.info("%s", line)
		return c.printJSON(releaseSchemaJSON{
			ID: resp.GetSchema().GetId(), Version: resp.GetSchema().GetVersion(), Digest: resp.GetSchema().GetDigest(),
		})
	}
	_, _ = fmt.Fprintln(c.Stdout, line)
	return 0
}

func readSchemaJSON(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := jsontext.Value(data).Clone()
	if err := value.Compact(); err == nil {
		return string(value), nil
	}
	var document any
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return "", errors.New("schema must be valid JSON or YAML")
	}
	encoded, err := json.Marshal(document, json.Deterministic(true))
	if err != nil {
		return "", fmt.Errorf("converting YAML schema to JSON: %w", err)
	}
	return string(encoded), nil
}

// releaseSchemaShowJSON embeds the schema document itself as JSON rather than
// as a string, so a caller can pipe it straight into a validator.
type releaseSchemaShowJSON struct {
	ID      string         `json:"id"`
	Version uint64         `json:"version"`
	Digest  string         `json:"digest"`
	Schema  jsontext.Value `json:"schema"`
}

func (c *CLI) cmdReleaseSchemaShow(args []string) int {
	fs := c.newFlags("release schema show")
	cf := addConnFlags(c, fs)
	c.setUsage(fs, "release schema show ID VERSION [flags]",
		"Print a schema version and its digest.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) != 2 {
		return c.fail("release schema show requires ID VERSION")
	}
	version, err := parseVersion(pos[1])
	if err != nil {
		return c.fail("invalid VERSION: %v", err)
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	resp, err := kmsv1.NewConfigurationSchemaServiceClient(conn).GetSchema(cf.authCtx(ctx), &kmsv1.GetSchemaRequest{Id: pos[0], Version: version})
	if err != nil {
		return c.failErr("release schema show", err)
	}
	schema := resp.GetSchema()
	if c.jsonOutput() {
		document := jsontext.Value(schema.GetSchemaJson())
		if len(bytes.TrimSpace(document)) == 0 {
			document = jsontext.Value("null")
		}
		return c.printJSON(releaseSchemaShowJSON{
			ID: schema.GetId(), Version: schema.GetVersion(), Digest: schema.GetDigest(), Schema: document,
		})
	}
	_, _ = fmt.Fprintf(c.Stdout, "Schema %s version %d\nDigest: %s\n%s\n", schema.GetId(), schema.GetVersion(), schema.GetDigest(), schema.GetSchemaJson())
	return 0
}

// releaseSchemaListItemJSON is one row of `release schema list`.
type releaseSchemaListItemJSON struct {
	ID        string  `json:"id"`
	Version   uint64  `json:"version"`
	Digest    string  `json:"digest"`
	CreatedAt *string `json:"created_at"`
}

func (c *CLI) cmdReleaseSchemaList(args []string) int {
	fs := c.newFlags("release schema list")
	cf := addConnFlags(c, fs)
	pageSize := fs.Int("page-size", 100, "result `count` per RPC")
	c.setUsage(fs, "release schema list [ID] [flags]",
		"List schema versions with their digests and creation times.", false)
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	id := ""
	if len(pos) > 0 {
		id = pos[0]
	}
	conn, err := c.dialConn(cf)
	if err != nil {
		return c.failErr("", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	client := kmsv1.NewConfigurationSchemaServiceClient(conn)
	rows := [][]string{}
	items := []releaseSchemaListItemJSON{}
	pageToken := ""
	for {
		resp, listErr := client.ListSchemas(cf.authCtx(ctx), &kmsv1.ListSchemasRequest{Id: id, PageSize: int32(*pageSize), PageToken: pageToken})
		if listErr != nil {
			return c.failErr("release schema list", listErr)
		}
		for _, schema := range resp.GetSchemas() {
			if c.jsonOutput() {
				items = append(items, releaseSchemaListItemJSON{
					ID: schema.GetId(), Version: schema.GetVersion(), Digest: schema.GetDigest(),
					CreatedAt: jsonTime(schema.GetCreatedAtUnixMs()),
				})
				continue
			}
			created := time.UnixMilli(schema.GetCreatedAtUnixMs()).UTC().Format(time.RFC3339)
			rows = append(rows, []string{
				schema.GetId(), strconv.FormatUint(schema.GetVersion(), 10), schema.GetDigest(), created,
			})
		}
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			break
		}
	}
	if c.jsonOutput() {
		// Every page has been followed, so there is no token to hand back.
		return c.printList(items, "")
	}
	c.printTable([]string{"ID", "VERSION", "DIGEST", "CREATED"}, rows)
	return 0
}

func (c *CLI) parseReleaseIdentity(args []string, requireVersion bool) (*kmsv1.NamespaceRef, string, uint64, bool) {
	want := 2
	if requireVersion {
		want = 3
	}
	if len(args) != want {
		_ = c.fail("release command requires ENV/APP NAME%s", map[bool]string{true: " VERSION"}[requireVersion])
		return nil, "", 0, false
	}
	ns, err := parseNamespaceProto(args[0])
	if err != nil {
		_ = c.fail("invalid namespace: %v", err)
		return nil, "", 0, false
	}
	version := uint64(0)
	if requireVersion {
		version, err = parseVersion(args[2])
		if err != nil {
			_ = c.fail("invalid VERSION: %v", err)
			return nil, "", 0, false
		}
	}
	return ns, args[1], version, true
}

func parseNamespaceProto(raw string) (*kmsv1.NamespaceRef, error) {
	ns, err := keyutil.ParseNamespace(raw)
	if err != nil {
		return nil, err
	}
	return &kmsv1.NamespaceRef{Env: ns.Env, App: ns.App}, nil
}

func namespaceDisplay(ns *kmsv1.NamespaceRef) string {
	if ns == nil {
		return ""
	}
	return ns.GetEnv() + "/" + ns.GetApp()
}

func parseVersion(raw string) (uint64, error) {
	version, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || version == 0 {
		return 0, errors.New("must be a positive integer")
	}
	return version, nil
}

var _ flag.Value = (*optionalUint64)(nil)
