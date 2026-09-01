package core

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// readiness-cases.json pins the pure readiness functions: each
// case is a compact input the frontend can also reason about plus the states
// and finding codes the backend derives from it, and the schema-type ↔
// content-type table both sides must agree on. Regenerate with
//
//	go test ./internal/core ./internal/server/httpserver -run TestConsoleFixtures -update
var updateFixtures = flag.Bool("update", false, "rewrite frontend/tests/fixtures/backend/readiness-cases.json")

const readinessFixturePath = "../../frontend/tests/fixtures/backend/readiness-cases.json"

type readinessCaseValue struct {
	Alias          string `json:"alias"`
	Kind           string `json:"kind"`
	Present        bool   `json:"present"`
	ContentType    string `json:"content_type,omitempty"`
	CurrentVersion uint64 `json:"current_version,omitempty"`
}

type readinessCaseActive struct {
	Version            uint64            `json:"version"`
	ActivationRevision uint64            `json:"activation_revision"`
	PreviousVersion    uint64            `json:"previous_version"`
	Pins               map[string]uint64 `json:"pins"`
}

type readinessCaseInstance struct {
	Identity              string `json:"identity"`
	ClientName            string `json:"client_name"`
	InstanceID            string `json:"instance_id"`
	State                 string `json:"state"`
	ReleaseVersion        uint64 `json:"release_version"`
	ActivationRevision    uint64 `json:"activation_revision"`
	RejectionCategory     string `json:"rejection_category"`
	Diagnostic            string `json:"diagnostic"`
	Connected             bool   `json:"connected"`
	ServerTimestampUnixMS int64  `json:"server_timestamp_unix_ms"`
}

type readinessCaseInput struct {
	Contract      []domain.ApplicationContractField `json:"contract"`
	SchemaPinned  bool                              `json:"schema_pinned"`
	SchemaExists  bool                              `json:"schema_exists"`
	Values        []readinessCaseValue              `json:"values"`
	Active        *readinessCaseActive              `json:"active"`
	LatestVersion uint64                            `json:"latest_version"`
	Instances     []readinessCaseInstance           `json:"instances"`
}

type readinessCaseExpected struct {
	ValuesState  string   `json:"values_state"`
	ReleaseState string   `json:"release_state"`
	RolloutState string   `json:"rollout_state"`
	EnvStatus    string   `json:"env_status"`
	AppStatus    string   `json:"app_status"`
	FindingCodes []string `json:"finding_codes"`
}

type readinessCase struct {
	Name     string                `json:"name"`
	Input    readinessCaseInput    `json:"input"`
	Expected readinessCaseExpected `json:"expected"`
}

// contentTypeToSchemaProperty is the reverse of JSONTypeToContentType, used
// to build a schema aligned with a contract.
func contentTypeToSchemaProperty(contentType string) map[string]any {
	switch contentType {
	case "json":
		return map[string]any{}
	case "float":
		return map[string]any{"type": "number"}
	case "binary":
		return map[string]any{"type": "string", "format": "kms-base64"}
	default:
		return map[string]any{"type": contentType}
	}
}

func alignedSchemaJSON(contract []domain.ApplicationContractField) string {
	properties := map[string]any{}
	required := []string{}
	for _, field := range contract {
		if field.Kind != domain.ReleaseEntryParameter {
			continue
		}
		properties[field.Alias] = contentTypeToSchemaProperty(field.ContentType)
		required = append(required, field.Alias)
	}
	b, _ := json.Marshal(map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false})
	return string(b)
}

// evaluateReadinessCase runs the readiness functions over the compact input.
func evaluateReadinessCase(in readinessCaseInput) readinessCaseExpected {
	ns := domain.NamespaceRef{Env: "dev", App: "gradethis"}
	app := domain.Application{Name: ns.App, ReleaseName: "runtime", Contract: in.Contract}
	if in.SchemaPinned {
		app.SchemaID, app.SchemaVersion = "runtime", 1
	}
	kinds := map[string]string{}
	contentTypes := map[string]string{}
	for _, field := range in.Contract {
		kinds[field.Alias] = field.Kind
		contentTypes[field.Alias] = field.ContentType
		if field.Kind == domain.ReleaseEntrySecret {
			contentTypes[field.Alias] = "text/plain"
		}
	}
	rows := []domain.ApplicationConfigurationRow{}
	refs := map[string]domain.Ref{}
	secrets := map[string]secretCurrentState{}
	for _, value := range in.Values {
		if !value.Present {
			continue
		}
		contentType := value.ContentType
		if contentType == "" {
			contentType = contentTypes[value.Alias]
		}
		rows = append(rows, domain.ApplicationConfigurationRow{Key: value.Alias, Kind: value.Kind, Cells: map[string]domain.ApplicationConfigurationCell{ns.Env: {Environment: ns.Env, Present: true, ContentType: contentType, Version: value.CurrentVersion}}})
		refs[value.Alias] = domain.Ref{NS: ns, Key: value.Alias}
		if value.Kind == domain.ReleaseEntrySecret {
			secrets[value.Alias] = secretCurrentState{State: domain.StateEnabled}
		}
	}
	var active *domain.ActiveConfigurationRelease
	if in.Active != nil {
		release := domain.ConfigurationRelease{Namespace: ns, Name: app.ReleaseName, Version: in.Active.Version, SchemaID: app.SchemaID, SchemaVersion: app.SchemaVersion}
		for _, field := range in.Contract {
			if version, ok := in.Active.Pins[field.Alias]; ok {
				release.Entries = append(release.Entries, domain.ConfigurationReleaseEntry{Alias: field.Alias, Kind: field.Kind, Ref: domain.Ref{NS: ns, Key: field.Alias}, Version: version, ContentType: contentTypes[field.Alias]})
			}
		}
		active = &domain.ActiveConfigurationRelease{Release: release, ActivationRevision: in.Active.ActivationRevision, PreviousVersion: in.Active.PreviousVersion}
	}
	acks := []domain.ReleaseAcknowledgement{}
	for _, inst := range in.Instances {
		acks = append(acks, domain.ReleaseAcknowledgement{
			Namespace: ns, ReleaseName: app.ReleaseName, ReleaseVersion: inst.ReleaseVersion, ActivationRevision: inst.ActivationRevision,
			ClientName: inst.ClientName, InstanceID: inst.InstanceID, Identity: inst.Identity, State: inst.State,
			RejectionCategory: inst.RejectionCategory, Diagnostic: inst.Diagnostic, Connected: inst.Connected,
			ServerTimestamp: time.UnixMilli(inst.ServerTimestampUnixMS).UTC(),
		})
	}
	var schema *domain.ConfigurationSchema
	schemaMissing := in.SchemaPinned && !in.SchemaExists
	if in.SchemaPinned && in.SchemaExists {
		schema = &domain.ConfigurationSchema{ID: "runtime", Version: 1, Schema: alignedSchemaJSON(in.Contract)}
	}
	env := computeEnvironmentReadiness(environmentReadinessInput{
		App: app, Namespace: domain.Namespace{NamespaceRef: ns, AllowedAuthMethods: []domain.AuthMethod{domain.AuthMethodToken}},
		Rows: rows, Refs: refs, Secrets: secrets, Active: active, LatestVersion: in.LatestVersion, ReleaseCount: in.LatestVersion,
		Acks: acks, SchemaMissing: schemaMissing, Now: readinessNow,
	})
	status, appFindings := computeApplicationFindings(applicationReadinessInput{App: app, Environments: []domain.EnvironmentOverview{env}, Schema: schema, SchemaMissing: schemaMissing})
	codes := append(findingCodes(appFindings), findingCodes(env.Findings)...)
	return readinessCaseExpected{ValuesState: env.ValuesState, ReleaseState: env.ReleaseState, RolloutState: env.RolloutState, EnvStatus: env.Status, AppStatus: status, FindingCodes: codes}
}

func TestConsoleFixturesReadiness(t *testing.T) {
	contract := []domain.ApplicationContractField{
		{Alias: "database", Kind: domain.ReleaseEntryParameter, ContentType: "json"},
		{Alias: "rate_limits", Kind: domain.ReleaseEntryParameter, ContentType: "integer"},
		{Alias: "db_password", Kind: domain.ReleaseEntrySecret},
	}
	const serverTime = int64(1755800000000)
	complete := []readinessCaseValue{
		{Alias: "database", Kind: domain.ReleaseEntryParameter, Present: true, ContentType: "json", CurrentVersion: 8},
		{Alias: "rate_limits", Kind: domain.ReleaseEntryParameter, Present: true, ContentType: "integer", CurrentVersion: 9},
		{Alias: "db_password", Kind: domain.ReleaseEntrySecret, Present: true, CurrentVersion: 2},
	}
	withValue := func(values []readinessCaseValue, alias string, mutate func(*readinessCaseValue)) []readinessCaseValue {
		out := append([]readinessCaseValue(nil), values...)
		for i := range out {
			if out[i].Alias == alias {
				mutate(&out[i])
			}
		}
		return out
	}
	activeRelease := func() *readinessCaseActive {
		return &readinessCaseActive{Version: 12, ActivationRevision: 41, PreviousVersion: 11, Pins: map[string]uint64{"database": 8, "rate_limits": 9, "db_password": 2}}
	}
	instance := func(id, state, category string, revision uint64, connected bool) readinessCaseInstance {
		version := uint64(12)
		if revision < 41 {
			version = 11
		}
		return readinessCaseInstance{Identity: "gradethis-dev", ClientName: "grader-api", InstanceID: id, State: state, ReleaseVersion: version, ActivationRevision: revision, RejectionCategory: category, Connected: connected, ServerTimestampUnixMS: serverTime}
	}
	base := func() readinessCaseInput {
		return readinessCaseInput{Contract: contract, SchemaPinned: true, SchemaExists: true, Values: complete, Instances: []readinessCaseInstance{}}
	}
	cases := []readinessCase{}
	add := func(name string, mutate func(*readinessCaseInput)) {
		in := base()
		mutate(&in)
		cases = append(cases, readinessCase{Name: name, Input: in, Expected: evaluateReadinessCase(in)})
	}
	add("no rows at all", func(in *readinessCaseInput) {
		in.Values = []readinessCaseValue{{Alias: "database", Kind: domain.ReleaseEntryParameter}, {Alias: "rate_limits", Kind: domain.ReleaseEntryParameter}, {Alias: "db_password", Kind: domain.ReleaseEntrySecret}}
	})
	add("one alias missing", func(in *readinessCaseInput) {
		in.Values = withValue(complete, "rate_limits", func(v *readinessCaseValue) { *v = readinessCaseValue{Alias: v.Alias, Kind: v.Kind} })
	})
	add("content type differs from contract", func(in *readinessCaseInput) {
		in.Values = withValue(complete, "rate_limits", func(v *readinessCaseValue) { v.ContentType = "string" })
	})
	add("complete, nothing active", func(in *readinessCaseInput) {})
	add("schema not pinned", func(in *readinessCaseInput) { in.SchemaPinned, in.SchemaExists = false, false })
	add("active, no subscribers", func(in *readinessCaseInput) { in.Active, in.LatestVersion = activeRelease(), 12 })
	add("active and applied", func(in *readinessCaseInput) {
		in.Active, in.LatestVersion = activeRelease(), 12
		in.Instances = []readinessCaseInstance{instance("grader-api-1", domain.ReleaseStateApplied, "", 41, true)}
	})
	add("newer parameter version than the active pin", func(in *readinessCaseInput) {
		in.Active, in.LatestVersion = activeRelease(), 12
		in.Values = withValue(complete, "rate_limits", func(v *readinessCaseValue) { v.CurrentVersion = 10 })
		in.Instances = []readinessCaseInstance{instance("grader-api-1", domain.ReleaseStateApplied, "", 41, true)}
	})
	add("active release missing a contract alias", func(in *readinessCaseInput) {
		in.Active, in.LatestVersion = activeRelease(), 12
		delete(in.Active.Pins, "rate_limits")
	})
	add("instance rejected at the current revision", func(in *readinessCaseInput) {
		in.Active, in.LatestVersion = activeRelease(), 12
		in.Instances = []readinessCaseInstance{
			instance("grader-api-1", domain.ReleaseStateApplied, "", 41, true),
			instance("grader-api-2", domain.ReleaseStateRejected, domain.ReleaseRejectConfigValidationFailed, 41, true),
		}
	})
	add("instance still preparing", func(in *readinessCaseInput) {
		in.Active, in.LatestVersion = activeRelease(), 12
		in.Instances = []readinessCaseInstance{instance("grader-api-1", domain.ReleaseStatePrepared, "", 41, true)}
	})
	add("instance disconnected before applying", func(in *readinessCaseInput) {
		in.Active, in.LatestVersion = activeRelease(), 12
		in.Instances = []readinessCaseInstance{instance("grader-api-1", domain.ReleaseStateApplied, "", 40, false)}
	})
	add("rolled back to the previous version", func(in *readinessCaseInput) {
		in.Active, in.LatestVersion = activeRelease(), 12
		in.Active.Version, in.Active.PreviousVersion = 11, 12
		in.Instances = []readinessCaseInstance{instance("grader-api-1", domain.ReleaseStateApplied, "", 41, true)}
	})
	add("pinned schema missing from the registry", func(in *readinessCaseInput) {
		in.Active, in.LatestVersion = activeRelease(), 12
		in.SchemaExists = false
	})

	typeMapping := map[string]string{}
	for name, property := range map[string]map[string]any{
		"object": {"type": "object"}, "array": {"type": "array"}, "string": {"type": "string"},
		"string+kms-base64": {"type": "string", "format": "kms-base64"}, "integer": {"type": "integer"},
		"number": {"type": "number"}, "boolean": {"type": "boolean"}, "union": {"type": []any{"string", "null"}}, "absent": {},
	} {
		typeMapping[name] = JSONTypeToContentType(property)
	}
	out, err := json.Marshal(
		map[string]any{"type_mapping": typeMapping, "cases": cases},
		json.Deterministic(true),
		jsontext.WithIndent("  "),
	)
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, '\n')

	// Guard the pinned expectations that matter most before touching the file.
	byName := map[string]readinessCaseExpected{}
	for _, c := range cases {
		byName[c.Name] = c.Expected
	}
	if e := byName["active and applied"]; e.EnvStatus != domain.EnvStatusReady || e.AppStatus != domain.AppStatusReady {
		t.Fatalf("active and applied = %+v", e)
	}
	if e := byName["instance rejected at the current revision"]; e.EnvStatus != domain.EnvStatusDegraded || e.AppStatus != domain.AppStatusAttention {
		t.Fatalf("rejected = %+v", e)
	}
	if e := byName["pinned schema missing from the registry"]; e.EnvStatus != domain.EnvStatusBlocked || e.AppStatus != domain.AppStatusBlocked {
		t.Fatalf("schema missing = %+v", e)
	}
	if e := byName["no rows at all"]; e.EnvStatus != domain.EnvStatusEmpty || e.AppStatus != domain.AppStatusSetup {
		t.Fatalf("no rows = %+v", e)
	}

	path := filepath.Clean(readinessFixturePath)
	if *updateFixtures {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture is missing; run with -update: %v", err)
	}
	if !bytes.Equal(existing, out) {
		t.Fatalf("readiness-cases.json is stale; run `go test ./internal/core -run TestConsoleFixturesReadiness -update`")
	}
}
