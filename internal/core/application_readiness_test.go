package core

import (
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

var readinessNow = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

func readinessApp() domain.Application {
	return domain.Application{Name: "gradethis", ReleaseName: "runtime", Contract: []domain.ApplicationContractField{
		{Alias: "database", Kind: domain.ReleaseEntryParameter, ContentType: "json"},
		{Alias: "db_password", Kind: domain.ReleaseEntrySecret},
		{Alias: "rate_limits", Kind: domain.ReleaseEntryParameter, ContentType: "integer"},
	}}
}

func readinessRows(env string, dbVersion, rateVersion uint64) []domain.ApplicationConfigurationRow {
	return []domain.ApplicationConfigurationRow{
		{Key: "database", Kind: domain.ResourceParameter, Cells: map[string]domain.ApplicationConfigurationCell{env: {Environment: env, Present: true, Value: `{"host":"SECRET-HOST-VALUE"}`, ContentType: "json", Version: dbVersion}}},
		{Key: "db_password", Kind: domain.ResourceSecret, Cells: map[string]domain.ApplicationConfigurationCell{env: {Environment: env, Present: true, ContentType: "text/plain", Version: 1}}},
		{Key: "rate_limits", Kind: domain.ResourceParameter, Cells: map[string]domain.ApplicationConfigurationCell{env: {Environment: env, Present: true, Value: "5", ContentType: "integer", Version: rateVersion}}},
	}
}

func readinessRefs(env string) map[string]domain.Ref {
	ns := domain.NamespaceRef{Env: env, App: "gradethis"}
	return map[string]domain.Ref{"database": {NS: ns, Key: "database"}, "db_password": {NS: ns, Key: "db_password"}, "rate_limits": {NS: ns, Key: "rate_limits"}}
}

func readinessActive(env string, version, revision, previous uint64, dbPin, ratePin uint64) *domain.ActiveConfigurationRelease {
	ns := domain.NamespaceRef{Env: env, App: "gradethis"}
	return &domain.ActiveConfigurationRelease{ActivationRevision: revision, PreviousVersion: previous, Release: domain.ConfigurationRelease{
		Namespace: ns, Name: "runtime", Version: version, Entries: []domain.ConfigurationReleaseEntry{
			{Alias: "database", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: ns, Key: "database"}, Version: dbPin, ContentType: "json"},
			{Alias: "db_password", Kind: domain.ReleaseEntrySecret, Ref: domain.Ref{NS: ns, Key: "db_password"}, Version: 1, ContentType: "text/plain"},
			{Alias: "rate_limits", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: ns, Key: "rate_limits"}, Version: ratePin, ContentType: "integer"},
		}}}
}

func ack(instance, state string, version, revision uint64, connected bool, at time.Time) domain.ReleaseAcknowledgement {
	return domain.ReleaseAcknowledgement{Namespace: domain.NamespaceRef{Env: "prod", App: "gradethis"}, ReleaseName: "runtime", ReleaseVersion: version, ActivationRevision: revision, ClientName: "api", InstanceID: instance, Identity: "svc", State: state, Connected: connected, ServerTimestamp: at}
}

func findingCodes(findings []domain.Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Code)
	}
	return out
}

func hasFinding(findings []domain.Finding, code string) (domain.Finding, bool) {
	for _, f := range findings {
		if f.Code == code {
			return f, true
		}
	}
	return domain.Finding{}, false
}

func TestComputeEnvironmentReadinessStates(t *testing.T) {
	app := readinessApp()
	base := func(env string) environmentReadinessInput {
		return environmentReadinessInput{App: app, Namespace: domain.Namespace{NamespaceRef: domain.NamespaceRef{Env: env, App: "gradethis"}}, Rows: readinessRows(env, 1, 1), Refs: readinessRefs(env), Secrets: map[string]secretCurrentState{"db_password": {State: domain.StateEnabled}}, Now: readinessNow}
	}

	t.Run("ready", func(t *testing.T) {
		in := base("dev")
		in.Active = readinessActive("dev", 3, 40, 2, 1, 1)
		in.Acks = []domain.ReleaseAcknowledgement{ack("i1", domain.ReleaseStateApplied, 3, 40, true, readinessNow), ack("i1", domain.ReleaseStateReceived, 3, 40, true, readinessNow)}
		out := computeEnvironmentReadiness(in)
		if out.Status != domain.EnvStatusReady || out.ValuesState != domain.ValuesStateComplete || out.ReleaseState != domain.ReleaseStateActive || out.RolloutState != domain.RolloutStateApplied {
			t.Fatalf("ready env = %s/%s/%s/%s findings=%v", out.Status, out.ValuesState, out.ReleaseState, out.RolloutState, findingCodes(out.Findings))
		}
		if out.Rollout.Total != 1 || out.Rollout.AppliedCurrent != 1 || out.Rollout.Connected != 1 {
			t.Fatalf("rollout = %+v", out.Rollout)
		}
		if out.Production || out.Active == nil || out.Active.IsRolledBack || len(out.Values) != 3 || out.Values[2].PinnedVersion != 1 || out.Values[2].Key != "rate_limits" {
			t.Fatalf("overview = %+v", out)
		}
		if _, ok := hasFinding(out.Findings, domain.FindingProduction); ok {
			t.Fatalf("dev must not carry the production finding: %v", findingCodes(out.Findings))
		}
	})

	t.Run("drift and rolled back", func(t *testing.T) {
		in := base("prod")
		in.Rows = readinessRows("prod", 1, 3)
		in.Active = readinessActive("prod", 2, 41, 3, 1, 2)
		in.Acks = []domain.ReleaseAcknowledgement{ack("i1", domain.ReleaseStateApplied, 2, 41, true, readinessNow)}
		out := computeEnvironmentReadiness(in)
		if out.Status != domain.EnvStatusDrift || out.ReleaseState != domain.ReleaseStateDrift || !out.Production || !out.Active.IsRolledBack {
			t.Fatalf("drift env = %+v findings=%v", out, findingCodes(out.Findings))
		}
		f, ok := hasFinding(out.Findings, domain.FindingUnreleasedChanges)
		if !ok || f.Scope.Alias != "rate_limits" || f.Params["current"] != uint64(3) || f.Params["pinned"] != uint64(2) {
			t.Fatalf("unreleased_changes = %+v", f)
		}
		if _, ok := hasFinding(out.Findings, domain.FindingRolledBack); !ok {
			t.Fatalf("missing rolled_back: %v", findingCodes(out.Findings))
		}
		if _, ok := hasFinding(out.Findings, domain.FindingProduction); !ok {
			t.Fatalf("missing production: %v", findingCodes(out.Findings))
		}
	})

	t.Run("applied but divergent from source defaults", func(t *testing.T) {
		in := base("prod")
		in.Active = readinessActive("prod", 3, 42, 2, 1, 1)
		in.Acks = []domain.ReleaseAcknowledgement{
			ack("drifted", domain.ReleaseStateApplied, 3, 42, true, readinessNow),
			ack("clean", domain.ReleaseStateApplied, 3, 42, true, readinessNow),
		}
		in.Acks[0].AppliedDivergent = true
		in.Acks[0].DivergentFieldCount = 4
		out := computeEnvironmentReadiness(in)
		r := out.Rollout
		if r.Total != 2 || r.AppliedCurrent != 2 || r.AppliedDivergent != 1 || r.Rejected != 0 {
			t.Fatalf("rollout = %+v", r)
		}
		if out.RolloutState != domain.RolloutStateApplied {
			t.Fatalf("divergence must not degrade the rollout: %s", out.RolloutState)
		}
		f, ok := hasFinding(out.Findings, domain.FindingInstanceDivergent)
		if !ok || f.Scope.Instance != "drifted" || f.Severity != domain.FindingWarning || f.Params["divergent_fields"] != 4 {
			t.Fatalf("instance_divergent = %+v", f)
		}
		if _, ok := hasFinding(out.Findings, domain.FindingInstanceRejected); ok {
			t.Fatalf("divergent instance must not be reported as rejected: %v", findingCodes(out.Findings))
		}
		instances := groupSubscriberInstances(in.Acks)
		if len(instances) != 2 || !instances[1].AppliedDivergent || instances[1].DivergentFieldCount != 4 || instances[0].AppliedDivergent {
			t.Fatalf("grouped instances = %+v", instances)
		}
	})

	t.Run("degraded, rolling and stale", func(t *testing.T) {
		in := base("prod")
		in.Active = readinessActive("prod", 3, 42, 2, 1, 1)
		in.Acks = []domain.ReleaseAcknowledgement{
			ack("rejecting", domain.ReleaseStateRejected, 3, 42, true, readinessNow),
			ack("slow", domain.ReleaseStateApplied, 2, 41, true, readinessNow),
			ack("gone", domain.ReleaseStateApplied, 2, 41, false, readinessNow.Add(-5*time.Minute)),
			ack("fresh", domain.ReleaseStateApplied, 3, 42, true, readinessNow),
		}
		in.Acks[0].RejectionCategory = domain.ReleaseRejectConfigValidationFailed
		in.Acks = append(in.Acks, domain.ReleaseAcknowledgement{Namespace: in.Namespace.NamespaceRef, ReleaseName: "batch", ClientName: "worker", InstanceID: "w1", Identity: "svc", State: domain.ReleaseStateApplied, ActivationRevision: 42, Connected: true, ServerTimestamp: readinessNow})
		out := computeEnvironmentReadiness(in)
		if out.Status != domain.EnvStatusDegraded || out.RolloutState != domain.RolloutStateDegraded {
			t.Fatalf("degraded env = %s/%s", out.Status, out.RolloutState)
		}
		r := out.Rollout
		if r.Total != 4 || r.Connected != 3 || r.AppliedCurrent != 1 || r.Rejected != 1 || r.Pending != 1 || r.Stale != 1 || len(r.RejectedInstances) != 1 || r.RejectedInstances[0].InstanceID != "rejecting" {
			t.Fatalf("rollout = %+v", r)
		}
		if len(r.OtherReleaseNames) != 1 || r.OtherReleaseNames[0] != "batch" {
			t.Fatalf("other release names = %v", r.OtherReleaseNames)
		}
		f, ok := hasFinding(out.Findings, domain.FindingInstanceRejected)
		if !ok || f.Scope.Instance != "rejecting" || f.Params["category"] != domain.ReleaseRejectConfigValidationFailed {
			t.Fatalf("instance_rejected = %+v", f)
		}
		for _, code := range []string{domain.FindingInstancePending, domain.FindingInstanceStale, domain.FindingSubscriberOtherRelease} {
			if _, ok := hasFinding(out.Findings, code); !ok {
				t.Fatalf("missing %s: %v", code, findingCodes(out.Findings))
			}
		}
		in.Acks = in.Acks[1:4]
		out = computeEnvironmentReadiness(in)
		if out.Status != domain.EnvStatusRolling || out.RolloutState != domain.RolloutStateRolling {
			t.Fatalf("rolling env = %s/%s", out.Status, out.RolloutState)
		}
		in.Acks = []domain.ReleaseAcknowledgement{ack("gone", domain.ReleaseStateApplied, 2, 41, false, readinessNow.Add(-5*time.Minute))}
		out = computeEnvironmentReadiness(in)
		if out.Status != domain.EnvStatusReady || out.RolloutState != domain.RolloutStateStale {
			t.Fatalf("stale env = %s/%s", out.Status, out.RolloutState)
		}
	})

	t.Run("setup progression", func(t *testing.T) {
		in := base("dev")
		in.Rows, in.Refs, in.Secrets = nil, nil, nil
		out := computeEnvironmentReadiness(in)
		if out.Status != domain.EnvStatusEmpty || out.ValuesState != domain.ValuesStateEmpty || out.ReleaseState != domain.ReleaseStateNone || out.RolloutState != domain.RolloutStateNoSubscribers {
			t.Fatalf("empty env = %+v", out)
		}
		if _, ok := hasFinding(out.Findings, domain.FindingNoActiveRelease); !ok {
			t.Fatalf("missing no_active_release: %v", findingCodes(out.Findings))
		}
		in.Rows = readinessRows("dev", 1, 1)[:1]
		in.Refs = map[string]domain.Ref{"database": readinessRefs("dev")["database"]}
		out = computeEnvironmentReadiness(in)
		if out.Status != domain.EnvStatusIncomplete || out.ValuesState != domain.ValuesStateIncomplete {
			t.Fatalf("incomplete env = %s/%s", out.Status, out.ValuesState)
		}
		missing := 0
		for _, f := range out.Findings {
			if f.Code == domain.FindingResourceMissing {
				missing++
				if f.Params["kind"] == "" || f.Scope.Alias == "" {
					t.Fatalf("resource_missing params = %+v", f)
				}
			}
		}
		if missing != 2 {
			t.Fatalf("resource_missing count = %d", missing)
		}
		in = base("dev")
		out = computeEnvironmentReadiness(in)
		if out.Status != domain.EnvStatusUnreleased {
			t.Fatalf("unreleased env = %s", out.Status)
		}
	})

	t.Run("blocked by contract mismatch and stale pin", func(t *testing.T) {
		in := base("dev")
		in.Active = readinessActive("dev", 1, 10, 0, 1, 1)
		in.Active.Release.Entries = in.Active.Release.Entries[:2]
		out := computeEnvironmentReadiness(in)
		if out.Status != domain.EnvStatusBlocked || out.ReleaseState != domain.ReleaseStateBlocked {
			t.Fatalf("mismatch env = %s/%s", out.Status, out.ReleaseState)
		}
		if _, ok := hasFinding(out.Findings, domain.FindingContractReleaseMismatch); !ok {
			t.Fatalf("missing contract_release_mismatch: %v", findingCodes(out.Findings))
		}
		if _, ok := hasFinding(out.Findings, domain.FindingPreviousUnavailable); !ok {
			t.Fatalf("missing previous_unavailable: %v", findingCodes(out.Findings))
		}
		in = base("dev")
		in.Active = readinessActive("dev", 1, 10, 0, 1, 1)
		in.ActiveValidation = []domain.ReleaseValidationError{{Alias: "database", Code: domain.ReleaseValidationNotFound}}
		out = computeEnvironmentReadiness(in)
		f, ok := hasFinding(out.Findings, domain.FindingReleasePinStale)
		if out.Status != domain.EnvStatusBlocked || !ok || f.Scope.Alias != "database" {
			t.Fatalf("stale pin env = %s findings=%v", out.Status, findingCodes(out.Findings))
		}
		in = base("dev")
		in.SchemaMissing = true
		if out := computeEnvironmentReadiness(in); out.Status != domain.EnvStatusBlocked {
			t.Fatalf("schema missing env = %s", out.Status)
		}
	})

	t.Run("resource findings", func(t *testing.T) {
		in := base("dev")
		in.Secrets = map[string]secretCurrentState{"db_password": {State: domain.StateDisabled}}
		in.Rows[1].Cells["dev"] = domain.ApplicationConfigurationCell{Present: true, ContentType: "text/plain", Version: 1, HasAccessToken: true}
		in.Rows[2].Cells["dev"] = domain.ApplicationConfigurationCell{Present: true, ContentType: "string", Version: 1}
		out := computeEnvironmentReadiness(in)
		if out.Status != domain.EnvStatusIncomplete {
			t.Fatalf("status = %s findings=%v", out.Status, findingCodes(out.Findings))
		}
		f, ok := hasFinding(out.Findings, domain.FindingSecretUnreadable)
		if !ok || f.Params["state"] != domain.StateDisabled {
			t.Fatalf("secret_unreadable = %+v", f)
		}
		if _, ok := hasFinding(out.Findings, domain.FindingSecretTokenRequired); !ok {
			t.Fatalf("missing secret_token_required: %v", findingCodes(out.Findings))
		}
		f, ok = hasFinding(out.Findings, domain.FindingContentTypeMismatch)
		if !ok || f.Params["found"] != "string" || f.Params["content_type"] != "integer" {
			t.Fatalf("content_type_mismatch = %+v", f)
		}
		in = base("dev")
		in.Secrets = map[string]secretCurrentState{"db_password": {State: domain.StateEnabled, Expired: true}}
		out = computeEnvironmentReadiness(in)
		if f, ok := hasFinding(out.Findings, domain.FindingSecretUnreadable); !ok || f.Params["state"] != "expired" {
			t.Fatalf("expired secret = %+v", f)
		}
		in = base("dev")
		in.Rows[1] = domain.ApplicationConfigurationRow{Key: "db_password", Kind: domain.ResourceParameter, Cells: map[string]domain.ApplicationConfigurationCell{"dev": {Present: true, ContentType: "string", Version: 1}}}
		out = computeEnvironmentReadiness(in)
		if f, ok := hasFinding(out.Findings, domain.FindingKindMismatch); !ok || f.Params["found"] != domain.ResourceParameter {
			t.Fatalf("kind_mismatch = %+v findings=%v", f, findingCodes(out.Findings))
		}
	})

	t.Run("findings never carry values", func(t *testing.T) {
		in := base("prod")
		in.Rows = readinessRows("prod", 2, 3)
		in.Active = readinessActive("prod", 1, 10, 0, 1, 1)
		in.Acks = []domain.ReleaseAcknowledgement{ack("i1", domain.ReleaseStateRejected, 1, 10, true, readinessNow)}
		in.Acks[0].Diagnostic = "[redacted]"
		out := computeEnvironmentReadiness(in)
		b, err := json.Marshal(out.Findings)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "SECRET-HOST-VALUE") {
			t.Fatalf("findings leak a parameter value: %s", b)
		}
	})
}

func TestComputeApplicationFindingsAndStatus(t *testing.T) {
	app := readinessApp()
	env := func(status string, findings ...domain.Finding) domain.EnvironmentOverview {
		return domain.EnvironmentOverview{Namespace: domain.Namespace{NamespaceRef: domain.NamespaceRef{Env: "dev", App: app.Name}}, Status: status, Findings: findings}
	}
	schema := &domain.ConfigurationSchema{Application: app.Name, ReleaseName: app.ReleaseName, Version: 1, Schema: `{"type":"object","properties":{"database":{"type":"object"},"rate_limits":{"type":"integer"}},"required":["database","rate_limits"],"additionalProperties":false}`}
	app.SchemaVersion = 1

	status, findings := computeApplicationFindings(applicationReadinessInput{App: app, Environments: nil, Schema: schema})
	if status != domain.AppStatusSetup {
		t.Fatalf("no envs status = %s findings=%v", status, findingCodes(findings))
	}
	if _, ok := hasFinding(findings, domain.FindingNoEnvironments); !ok {
		t.Fatalf("missing no_environments: %v", findingCodes(findings))
	}
	if len(findings) != 1 {
		t.Fatalf("aligned schema must add no findings: %v", findingCodes(findings))
	}

	status, _ = computeApplicationFindings(applicationReadinessInput{App: app, Schema: schema, Environments: []domain.EnvironmentOverview{env(domain.EnvStatusReady), env(domain.EnvStatusUnreleased)}})
	if status != domain.AppStatusAttention {
		t.Fatalf("mixed status = %s", status)
	}
	status, _ = computeApplicationFindings(applicationReadinessInput{App: app, Schema: schema, Environments: []domain.EnvironmentOverview{env(domain.EnvStatusEmpty), env(domain.EnvStatusIncomplete)}})
	if status != domain.AppStatusSetup {
		t.Fatalf("all-setup status = %s", status)
	}
	status, _ = computeApplicationFindings(applicationReadinessInput{App: app, Schema: schema, Environments: []domain.EnvironmentOverview{env(domain.EnvStatusReady), env(domain.EnvStatusBlocked)}})
	if status != domain.AppStatusBlocked {
		t.Fatalf("blocked env status = %s", status)
	}
	status, _ = computeApplicationFindings(applicationReadinessInput{App: app, Schema: schema, Environments: []domain.EnvironmentOverview{env(domain.EnvStatusReady, finding(domain.FindingSubscriberOtherRelease, domain.FindingWarning, domain.FindingScope{Env: "dev"}, nil))}})
	if status != domain.AppStatusAttention {
		t.Fatalf("env warning status = %s", status)
	}
	status, findings = computeApplicationFindings(applicationReadinessInput{App: app, Schema: schema, Environments: []domain.EnvironmentOverview{env(domain.EnvStatusReady)}, InsecureListener: true})
	if status != domain.AppStatusReady {
		t.Fatalf("insecure listener must not change status: %s", status)
	}
	if _, ok := hasFinding(findings, domain.FindingInsecureListener); !ok {
		t.Fatalf("missing insecure_listener: %v", findingCodes(findings))
	}
	status, findings = computeApplicationFindings(applicationReadinessInput{App: app, SchemaMissing: true, Environments: []domain.EnvironmentOverview{env(domain.EnvStatusReady)}})
	if f, ok := hasFinding(findings, domain.FindingSchemaMissing); status != domain.AppStatusBlocked || !ok || f.Params["application"] != app.Name || f.Params["release_name"] != app.ReleaseName {
		t.Fatalf("schema missing = %s %+v", status, findings)
	}
	unpinned := app
	unpinned.SchemaVersion = 0
	if _, findings = computeApplicationFindings(applicationReadinessInput{App: unpinned, Environments: []domain.EnvironmentOverview{env(domain.EnvStatusReady)}}); len(findings) != 1 || findings[0].Code != domain.FindingSchemaUnpinned {
		t.Fatalf("unpinned findings = %v", findingCodes(findings))
	}
	empty := app
	empty.Contract = nil
	_, findings = computeApplicationFindings(applicationReadinessInput{App: empty, Schema: schema, Environments: []domain.EnvironmentOverview{env(domain.EnvStatusReady)}})
	if _, ok := hasFinding(findings, domain.FindingContractEmpty); !ok || findings[0].Severity != domain.FindingBlocking {
		t.Fatalf("empty contract findings = %v (blocking findings must sort first)", findingCodes(findings))
	}
}

func TestContractSchemaAlignment(t *testing.T) {
	contract := readinessApp().Contract
	cases := []struct {
		name   string
		schema string
		want   []string
	}{
		{"aligned", `{"type":"object","properties":{"database":{"type":"object"},"rate_limits":{"type":"integer"}},"required":["database"]}`, nil},
		{"type mismatch", `{"type":"object","properties":{"database":{"type":"object"},"rate_limits":{"type":"string"}}}`, []string{domain.FindingContractTypeMismatch}},
		{"closed schema drops alias", `{"type":"object","properties":{"database":{"type":"object"}},"additionalProperties":false}`, []string{domain.FindingAliasNotInSchema}},
		{"open schema ignores alias", `{"type":"object","properties":{"database":{"type":"object"}}}`, []string{domain.FindingSchemaPropertyMissingAlias}},
		{"required secret is blocking", `{"type":"object","properties":{"database":{"type":"object"},"rate_limits":{"type":"integer"}},"required":["db_password"]}`, []string{domain.FindingSchemaRequiredMissingAlias}},
		{"invalid schema json", `{`, nil},
	}
	for _, c := range cases {
		got := findingCodes(contractSchemaAlignment(contract, c.schema))
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: findings = %v, want %v", c.name, got, c.want)
		}
	}
	if f := contractSchemaAlignment(contract, cases[4].schema)[0]; f.Severity != domain.FindingBlocking || f.Scope.Alias != "db_password" {
		t.Fatalf("required finding = %+v", f)
	}
}

func TestJSONTypeToContentType(t *testing.T) {
	cases := map[string]string{
		`{"type":"object"}`:                       "json",
		`{"type":"array"}`:                        "json",
		`{"type":"string"}`:                       "string",
		`{"type":"string","format":"kms-base64"}`: "binary",
		`{"type":"integer"}`:                      "integer",
		`{"type":"number"}`:                       "float",
		`{"type":"boolean"}`:                      "boolean",
		`{"type":["string","null"]}`:              "json",
		`{"description":"anything"}`:              "json",
	}
	for raw, want := range cases {
		var prop map[string]any
		if err := json.Unmarshal([]byte(raw), &prop); err != nil {
			t.Fatal(err)
		}
		if got := JSONTypeToContentType(prop); got != want {
			t.Errorf("%s → %s, want %s", raw, got, want)
		}
	}
}

func TestGroupSubscriberInstances(t *testing.T) {
	now := readinessNow
	acks := []domain.ReleaseAcknowledgement{
		ack("b", domain.ReleaseStateReceived, 3, 40, true, now.Add(-2*time.Second)),
		ack("b", domain.ReleaseStatePrepared, 3, 40, true, now.Add(-time.Second)),
		ack("b", domain.ReleaseStateApplied, 3, 40, false, now),
		ack("a", domain.ReleaseStateApplied, 2, 39, true, now.Add(-time.Minute)),
		ack("a", domain.ReleaseStateRejected, 3, 40, true, now),
		{Namespace: domain.NamespaceRef{Env: "prod", App: "gradethis"}, ReleaseName: "runtime", ClientName: "api", InstanceID: "c", Identity: "svc", Connected: true, ServerTimestamp: now},
	}
	got := groupSubscriberInstances(acks)
	if len(got) != 3 || got[0].InstanceID != "a" || got[1].InstanceID != "b" || got[2].InstanceID != "c" {
		t.Fatalf("grouped = %+v", got)
	}
	if got[0].State != domain.ReleaseStateRejected || got[0].ActivationRevision != 40 || got[0].ReleaseVersion != 3 {
		t.Fatalf("a = %+v", got[0])
	}
	if got[1].State != domain.ReleaseStateApplied || !got[1].Connected || !got[1].ServerTimestamp.Equal(now) {
		t.Fatalf("b = %+v", got[1])
	}
	if got[2].State != "" || got[2].ActivationRevision != 0 || !got[2].Connected {
		t.Fatalf("c = %+v", got[2])
	}
}

func TestIsProductionEnvironment(t *testing.T) {
	cases := map[string]bool{"prod": true, "prod-eu": true, "production": true, "prod_eu": false, "reproduction": false, "non-prod": false, "dev": false, "preprod": false}
	for env, want := range cases {
		if got := domain.IsProductionEnvironment(env); got != want {
			t.Errorf("IsProductionEnvironment(%q) = %v, want %v", env, got, want)
		}
	}
}
