package cli

import (
	"bytes"
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
	"text/tabwriter"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
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

func (c *CLI) cmdReleaseCreate(args []string) int {
	fs := c.newFlags("release create")
	cf := addConnFlags(c, fs)
	file := fs.String("file", "", "release JSON/YAML file ('-' for stdin)")
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
		return c.fail("reading release definition: %v", err)
	}
	req, err := releaseCreateRequest(definition)
	if err != nil {
		return c.fail("invalid release definition: %v", err)
	}

	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	resp, err := kmsv1.NewConfigurationReleaseServiceClient(conn).CreateRelease(cf.authCtx(ctx), req)
	if err != nil {
		return c.fail("release create: %v", err)
	}
	if resp.GetRelease() == nil {
		return c.fail("release create: server returned an empty release")
	}
	_, _ = fmt.Fprintf(c.Stdout, "Created %s/%s version %d (digest %s)\n",
		definition.Namespace, definition.Name, resp.GetRelease().GetVersion(), resp.GetRelease().GetDigest())
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

func (c *CLI) cmdReleaseValidate(args []string) int {
	fs := c.newFlags("release validate")
	cf := addConnFlags(c, fs)
	if !c.parseFlags(fs, args) {
		return 2
	}
	ns, name, version, ok := c.parseReleaseIdentity(c.args(), true)
	if !ok {
		return 1
	}
	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	resp, err := kmsv1.NewConfigurationReleaseServiceClient(conn).ValidateRelease(cf.authCtx(ctx), &kmsv1.ValidateReleaseRequest{
		Namespace: ns, Name: name, Version: version,
	})
	if err != nil {
		return c.fail("release validate: %v", err)
	}
	if resp.GetValid() {
		_, _ = fmt.Fprintf(c.Stdout, "Release %s/%s version %d is valid.\n", namespaceDisplay(ns), name, version)
		return 0
	}
	printReleaseValidationErrors(c.Stdout, resp.GetErrors())
	return 1
}

func printReleaseValidationErrors(w io.Writer, validationErrors []*kmsv1.ReleaseValidationError) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ALIAS\tCODE\tSCHEMA POINTER\tMESSAGE")
	for _, validationErr := range validationErrors {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", validationErr.GetAlias(), validationErr.GetCode(), validationErr.GetSchemaPointer(), validationErr.GetMessage())
	}
	_ = tw.Flush()
}

func releaseValidationDetails(err error) *kmsv1.ValidateReleaseResponse {
	for _, detail := range status.Convert(err).Details() {
		if validation, ok := detail.(*kmsv1.ValidateReleaseResponse); ok {
			return validation
		}
	}
	return nil
}

func (c *CLI) cmdReleaseShow(args []string) int {
	fs := c.newFlags("release show")
	cf := addConnFlags(c, fs)
	if !c.parseFlags(fs, args) {
		return 2
	}
	ns, name, version, ok := c.parseReleaseIdentity(c.args(), true)
	if !ok {
		return 1
	}
	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	resp, err := kmsv1.NewConfigurationReleaseServiceClient(conn).GetRelease(cf.authCtx(ctx), &kmsv1.GetReleaseRequest{
		Namespace: ns, Name: name, Version: version,
	})
	if err != nil {
		return c.fail("release show: %v", err)
	}
	return c.printRelease(resp.GetRelease(), 0, false, false)
}

func (c *CLI) printRelease(release *kmsv1.ConfigurationRelease, revision uint64, current, previous bool) int {
	if release == nil {
		return c.fail("server returned an empty release")
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
	tw := tabwriter.NewWriter(c.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ALIAS\tKIND\tPATH\tVERSION\tCONTENT TYPE\tPARAMETER DIGEST")
	entries := append([]*kmsv1.ConfigurationReleaseEntry(nil), release.GetEntries()...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].GetAlias() < entries[j].GetAlias() })
	for _, entry := range entries {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n",
			entry.GetAlias(), entry.GetKind(), displayPath(entry.GetRef()), entry.GetVersion(), entry.GetContentType(), entry.GetParameterDigest())
	}
	_ = tw.Flush()
	return 0
}

func (c *CLI) cmdReleaseList(args []string) int {
	fs := c.newFlags("release list")
	cf := addConnFlags(c, fs)
	pageSize := fs.Int("page-size", 100, "results per RPC")
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
	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	client := kmsv1.NewConfigurationReleaseServiceClient(conn)
	tw := tabwriter.NewWriter(c.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tVERSION\tCURRENT\tPREVIOUS\tREVISION\tDIGEST")
	pageToken := ""
	for {
		resp, listErr := client.ListReleases(cf.authCtx(ctx), &kmsv1.ListReleasesRequest{
			Namespace: ns, Name: name, PageSize: int32(*pageSize), PageToken: pageToken,
		})
		if listErr != nil {
			return c.fail("release list: %v", listErr)
		}
		for _, summary := range resp.GetReleases() {
			release := summary.GetRelease()
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%t\t%t\t%d\t%s\n", release.GetName(), release.GetVersion(), summary.GetCurrent(), summary.GetPrevious(), summary.GetActivationRevision(), release.GetDigest())
		}
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			break
		}
	}
	_ = tw.Flush()
	return 0
}

func (c *CLI) cmdReleaseDiff(args []string) int {
	fs := c.newFlags("release diff")
	cf := addConnFlags(c, fs)
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
	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	client := kmsv1.NewConfigurationReleaseServiceClient(conn)
	fromResp, err := client.GetRelease(cf.authCtx(ctx), &kmsv1.GetReleaseRequest{Namespace: ns, Name: pos[1], Version: fromVersion})
	if err != nil {
		return c.fail("release diff: reading version %d: %v", fromVersion, err)
	}
	toResp, err := client.GetRelease(cf.authCtx(ctx), &kmsv1.GetReleaseRequest{Namespace: ns, Name: pos[1], Version: toVersion})
	if err != nil {
		return c.fail("release diff: reading version %d: %v", toVersion, err)
	}
	printReleaseDiff(c.Stdout, fromResp.GetRelease(), toResp.GetRelease())
	return 0
}

func printReleaseDiff(w io.Writer, from, to *kmsv1.ConfigurationRelease) {
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
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ALIAS\tCHANGE\tKIND\tPATH\tVERSION\tPARAMETER DIGEST")
	for _, alias := range ordered {
		before, hadBefore := fromEntries[alias]
		after, hasAfter := toEntries[alias]
		change := "changed"
		switch {
		case !hadBefore:
			change = "added"
		case !hasAfter:
			change = "removed"
		case releaseEntriesEqual(before, after):
			continue
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", alias, change,
			diffText(entryKind(before), entryKind(after)),
			diffText(entryPath(before), entryPath(after)),
			diffText(entryVersion(before), entryVersion(after)),
			diffText(entryDigest(before), entryDigest(after)))
	}
	_ = tw.Flush()
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

func (c *CLI) cmdReleaseActivate(args []string) int {
	fs := c.newFlags("release activate")
	cf := addConnFlags(c, fs)
	var expected optionalUint64
	fs.Var(&expected, "expected-current-version", "CAS guard (0 means expect no active release)")
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
	return c.activateRelease(cf, req, "activate")
}

func (c *CLI) activateRelease(cf *connFlags, req *kmsv1.ActivateReleaseRequest, verb string) int {
	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	resp, err := kmsv1.NewConfigurationReleaseServiceClient(conn).ActivateRelease(cf.authCtx(ctx), req)
	if err != nil {
		return c.failReleaseActivation(verb, err)
	}
	_, _ = fmt.Fprintf(c.Stdout, "Active %s/%s version %d (previous %d, revision %d, changed=%t)\n",
		namespaceDisplay(req.GetNamespace()), req.GetName(), resp.GetCurrentVersion(), resp.GetPreviousVersion(), resp.GetActivationRevision(), resp.GetChanged())
	return 0
}

func (c *CLI) failReleaseActivation(verb string, err error) int {
	if validation := releaseValidationDetails(err); validation != nil {
		_, _ = fmt.Fprintf(c.Stderr, "error: release %s: configuration release validation failed\n", verb)
		printReleaseValidationErrors(c.Stderr, validation.GetErrors())
		return 1
	}
	return c.fail("release %s: %v", verb, err)
}

func (c *CLI) cmdReleaseRollback(args []string) int {
	fs := c.newFlags("release rollback")
	cf := addConnFlags(c, fs)
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

	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	client := kmsv1.NewConfigurationReleaseServiceClient(conn)
	active, err := client.GetActiveRelease(cf.authCtx(ctx), &kmsv1.GetActiveReleaseRequest{Namespace: ns, Name: name})
	if err != nil {
		return c.fail("release rollback: reading active release: %v", err)
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
	expected := active.GetRelease().GetVersion()
	resp, err := client.ActivateRelease(cf.authCtx(ctx), &kmsv1.ActivateReleaseRequest{
		Namespace: ns, Name: name, Version: target, ExpectedCurrentVersion: &expected,
	})
	if err != nil {
		return c.failReleaseActivation("rollback", err)
	}
	_, _ = fmt.Fprintf(c.Stdout, "Rolled back %s/%s to version %d (revision %d)\n", namespaceDisplay(ns), name, resp.GetCurrentVersion(), resp.GetActivationRevision())
	return 0
}

func (c *CLI) cmdReleaseSubscribers(args []string) int {
	fs := c.newFlags("release subscribers")
	cf := addConnFlags(c, fs)
	pageSize := fs.Int("page-size", 100, "results per RPC")
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
	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
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
			return c.fail("release subscribers: %v", listErr)
		}
		currentRevision = max(currentRevision, resp.GetCurrentRevision())
		mergeReleaseSubscriberStates(instances, resp.GetSubscribers())
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			break
		}
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

func writeReleaseSubscriberInstances(w io.Writer, instances map[releaseSubscriberInstanceKey]*releaseSubscriberInstanceStatus, currentRevision uint64) {
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
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "IDENTITY\tCLIENT\tINSTANCE\tRECEIVED\tPREPARED\tAPPLIED\tREJECTED\tLAG\tCONNECTED")
	for _, key := range keys {
		instance := instances[key]
		lag := uint64(0)
		if currentRevision > instance.latestRevision {
			lag = currentRevision - instance.latestRevision
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%t\n",
			instance.identity, instance.client, instance.instance,
			releaseSubscriberStateText(instance.states[domain.ReleaseStateReceived]),
			releaseSubscriberStateText(instance.states[domain.ReleaseStatePrepared]),
			releaseSubscriberStateText(instance.states[domain.ReleaseStateApplied]),
			releaseSubscriberStateText(instance.states[domain.ReleaseStateRejected]),
			lag, instance.connected)
	}
	_ = tw.Flush()
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

func (c *CLI) cmdReleaseSchemaCreate(args []string) int {
	fs := c.newFlags("release schema create")
	cf := addConnFlags(c, fs)
	metadata := fs.String("metadata-json", "", "non-sensitive metadata JSON")
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	if len(pos) != 2 {
		return c.fail("release schema create requires ID FILE")
	}
	schemaJSON, err := readSchemaJSON(pos[1])
	if err != nil {
		return c.fail("reading schema: %v", err)
	}
	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	resp, err := kmsv1.NewConfigurationSchemaServiceClient(conn).CreateSchema(cf.authCtx(ctx), &kmsv1.CreateSchemaRequest{
		Id: pos[0], SchemaJson: schemaJSON, MetadataJson: *metadata,
	})
	if err != nil {
		return c.fail("release schema create: %v", err)
	}
	_, _ = fmt.Fprintf(c.Stdout, "Created schema %s version %d (digest %s)\n", resp.GetSchema().GetId(), resp.GetSchema().GetVersion(), resp.GetSchema().GetDigest())
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

func (c *CLI) cmdReleaseSchemaShow(args []string) int {
	fs := c.newFlags("release schema show")
	cf := addConnFlags(c, fs)
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
	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	resp, err := kmsv1.NewConfigurationSchemaServiceClient(conn).GetSchema(cf.authCtx(ctx), &kmsv1.GetSchemaRequest{Id: pos[0], Version: version})
	if err != nil {
		return c.fail("release schema show: %v", err)
	}
	schema := resp.GetSchema()
	_, _ = fmt.Fprintf(c.Stdout, "Schema %s version %d\nDigest: %s\n%s\n", schema.GetId(), schema.GetVersion(), schema.GetDigest(), schema.GetSchemaJson())
	return 0
}

func (c *CLI) cmdReleaseSchemaList(args []string) int {
	fs := c.newFlags("release schema list")
	cf := addConnFlags(c, fs)
	pageSize := fs.Int("page-size", 100, "results per RPC")
	if !c.parseFlags(fs, args) {
		return 2
	}
	pos := c.args()
	id := ""
	if len(pos) > 0 {
		id = pos[0]
	}
	conn, err := cf.dial()
	if err != nil {
		return c.fail("%v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := callContext()
	defer cancel()
	client := kmsv1.NewConfigurationSchemaServiceClient(conn)
	tw := tabwriter.NewWriter(c.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tVERSION\tDIGEST\tCREATED")
	pageToken := ""
	for {
		resp, listErr := client.ListSchemas(cf.authCtx(ctx), &kmsv1.ListSchemasRequest{Id: id, PageSize: int32(*pageSize), PageToken: pageToken})
		if listErr != nil {
			return c.fail("release schema list: %v", listErr)
		}
		for _, schema := range resp.GetSchemas() {
			created := time.UnixMilli(schema.GetCreatedAtUnixMs()).UTC().Format(time.RFC3339)
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", schema.GetId(), schema.GetVersion(), schema.GetDigest(), created)
		}
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			break
		}
	}
	_ = tw.Flush()
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
