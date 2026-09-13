package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// migrateAdminStub records every MigrateApplicationRelease request and
// answers them in order from responses/errs.
type migrateAdminStub struct {
	kmsv1.UnimplementedAdminServiceServer
	mu        sync.Mutex
	calls     []*kmsv1.MigrateApplicationReleaseRequest
	responses []*kmsv1.MigrateApplicationReleaseResponse
	errs      []error
}

func (s *migrateAdminStub) MigrateApplicationRelease(_ context.Context, req *kmsv1.MigrateApplicationReleaseRequest) (*kmsv1.MigrateApplicationReleaseResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, proto.Clone(req).(*kmsv1.MigrateApplicationReleaseRequest))
	index := len(s.calls) - 1
	if index < len(s.errs) && s.errs[index] != nil {
		return nil, s.errs[index]
	}
	if index >= len(s.responses) {
		return nil, status.Error(codes.Internal, "unexpected migrate call")
	}
	return s.responses[index], nil
}

func (s *migrateAdminStub) requests() []*kmsv1.MigrateApplicationReleaseRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*kmsv1.MigrateApplicationReleaseRequest(nil), s.calls...)
}

// migrateSchemaStub serves the application's registered schemas from a fixed
// list, one page.
type migrateSchemaStub struct {
	kmsv1.UnimplementedConfigurationSchemaServiceServer
	mu      sync.Mutex
	calls   []*kmsv1.ListSchemasRequest
	schemas []*kmsv1.ConfigurationSchema
	err     error
}

func (s *migrateSchemaStub) ListSchemas(_ context.Context, req *kmsv1.ListSchemasRequest) (*kmsv1.ListSchemasResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, proto.Clone(req).(*kmsv1.ListSchemasRequest))
	if s.err != nil {
		return nil, s.err
	}
	return &kmsv1.ListSchemasResponse{Schemas: s.schemas}, nil
}

// migrateTestContract mirrors the contract writeVerifyArtifact encodes.
func migrateTestContract() []*kmsv1.ApplicationContractField {
	return []*kmsv1.ApplicationContractField{
		{Alias: "db_password", Kind: "secret"},
		{Alias: "greeting", Kind: "parameter", ContentType: "string"},
		{Alias: "settings", Kind: "parameter", ContentType: "json"},
	}
}

func migrateTestSchemas() []*kmsv1.ConfigurationSchema {
	return []*kmsv1.ConfigurationSchema{
		{Version: 1, Application: "gradethis", ReleaseName: "runtime", Digest: "1111111111111111111111111111111111111111111111111111111111111111", ContractEstablished: true, Contract: []*kmsv1.ApplicationContractField{{Alias: "db_password", Kind: "secret"}, {Alias: "greeting", Kind: "parameter", ContentType: "string"}}},
		{Version: 2, Application: "gradethis", ReleaseName: "runtime", Digest: verifyTestSchemaSHA, ContractEstablished: true, Contract: migrateTestContract()},
	}
}

func migratePreviewResponse() *kmsv1.MigrateApplicationReleaseResponse {
	ns := &kmsv1.NamespaceRef{Env: "dev", App: "gradethis"}
	return &kmsv1.MigrateApplicationReleaseResponse{
		PlanDigest: "abcdef0123456789abcdef0123456789", Valid: true, ReleaseName: "runtime",
		SourceVersion: 12, SourceActivationRevision: 7, SchemaVersion: 2,
		Entries: []*kmsv1.ApplicationReleasePlanEntry{
			{Alias: "settings", Kind: "parameter", Ref: &kmsv1.ResourceRef{Namespace: ns, Key: "config/settings"}, FromVersion: 3, ToVersion: 4, Source: "edited"},
			{Alias: "db_password", Kind: "secret", Ref: &kmsv1.ResourceRef{Namespace: ns, Key: "db-password"}, FromVersion: 2, ToVersion: 2, Source: "preserved"},
			{Alias: "greeting", Kind: "parameter", Ref: &kmsv1.ResourceRef{Namespace: ns, Key: "config/greeting"}, FromVersion: 1, ToVersion: 1, Source: "preserved"},
		},
		AffectedEnvironments: []*kmsv1.ApplicationMigrationEnvironment{{Environment: "prod", ActiveVersion: 5, SchemaVersion: 2}, {Environment: "staging"}},
	}
}

func migrateExecutedResponse() *kmsv1.MigrateApplicationReleaseResponse {
	resp := migratePreviewResponse()
	resp.Executed = true
	resp.Release = &kmsv1.ConfigurationRelease{
		Namespace: &kmsv1.NamespaceRef{Env: "dev", App: "gradethis"}, Name: "runtime", Version: 13, SchemaVersion: 2, Digest: "release-digest", CreatedAtUnixMs: 1700000000000,
		Entries: []*kmsv1.ConfigurationReleaseEntry{{Alias: "greeting", Kind: "parameter", Ref: &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: "dev", App: "gradethis"}, Key: "config/greeting"}, Version: 1, ContentType: "string", ParameterDigest: "d"}},
	}
	resp.Activation = &kmsv1.ApplicationMigrationActivation{ActivationRevision: 8, Changed: true}
	return resp
}

type migrateStubs struct {
	admin  *migrateAdminStub
	schema *migrateSchemaStub
	verify *verifyReleaseStub
}

// newMigrateCLI wires the three stub services the command talks to on one
// in-memory transport. By default every artifact parameter differs from the
// source release, so each one becomes an edit.
func newMigrateCLI(t *testing.T, stubs migrateStubs) *testCLI {
	t.Helper()
	if stubs.admin == nil {
		stubs.admin = &migrateAdminStub{responses: []*kmsv1.MigrateApplicationReleaseResponse{migratePreviewResponse()}}
	}
	if stubs.schema == nil {
		stubs.schema = &migrateSchemaStub{schemas: migrateTestSchemas()}
	}
	if stubs.verify == nil {
		stubs.verify = &verifyReleaseStub{response: &kmsv1.VerifyReleaseDefaultsResponse{
			Name: "runtime", Version: 12, ActivationRevision: 7, SchemaVersion: 1,
			Entries: []*kmsv1.VerifyEntryVerdict{{Alias: "greeting", Verdict: "differs"}, {Alias: "settings", Verdict: "differs"}},
		}}
	}
	c := newTestCLI()
	c.dialOverride = startStubGRPC(t, func(server *grpc.Server) {
		kmsv1.RegisterAdminServiceServer(server, stubs.admin)
		kmsv1.RegisterConfigurationSchemaServiceServer(server, stubs.schema)
		kmsv1.RegisterConfigurationReleaseServiceServer(server, stubs.verify)
	})
	return c
}

func migrateArgs(t *testing.T, extra ...string) []string {
	t.Helper()
	return append([]string{"release", "migrate", "dev/gradethis", "--from-schema", "1", "--from", writeVerifyArtifact(t), "--insecure", "--token", "admin-token"}, extra...)
}

func changeByAlias(t *testing.T, req *kmsv1.MigrateApplicationReleaseRequest, alias string) *kmsv1.ApplicationMigrationChange {
	t.Helper()
	for _, change := range req.GetChanges() {
		if change.GetAlias() == alias {
			return change
		}
	}
	return nil
}

func TestReleaseMigrateDryRunByDefault(t *testing.T) {
	admin := &migrateAdminStub{responses: []*kmsv1.MigrateApplicationReleaseResponse{migratePreviewResponse()}}
	schema := &migrateSchemaStub{schemas: migrateTestSchemas()}
	c := newMigrateCLI(t, migrateStubs{admin: admin, schema: schema})
	c.isTTY = func() bool { t.Fatal("a dry run must not consult the terminal"); return false }
	code := c.Run(migrateArgs(t))
	if code != exitOK {
		t.Fatalf("exit = %d, stderr=%s", code, c.stderr())
	}
	calls := admin.requests()
	if len(calls) != 1 {
		t.Fatalf("migrate calls = %d, want exactly one preview", len(calls))
	}
	req := calls[0]
	if req.GetExecute() || req.GetPlanDigest() != "" || req.ExpectedSourceVersion != nil || req.ExpectedSourceActivationRevision != nil {
		t.Fatalf("preview request must not carry execute state: %v", req)
	}
	if req.GetNamespace().GetEnv() != "dev" || req.GetNamespace().GetApp() != "gradethis" || req.GetSourceSchemaVersion() != 1 || req.GetSchemaVersion() != 2 {
		t.Fatalf("request tracks = %v", req)
	}
	if len(req.GetContract()) != 3 || req.GetContract()[0].GetAlias() != "db_password" || req.GetContract()[2].GetContentType() != "json" {
		t.Fatalf("contract = %v, want the artifact's", req.GetContract())
	}
	if len(schema.calls) != 1 || schema.calls[0].GetApplication() != "gradethis" {
		t.Fatalf("schema listing calls = %v", schema.calls)
	}
	out := c.stdout()
	for _, want := range []string{
		"Source: dev/gradethis runtime@12 (schema v1, activation 7)", "Target: schema v2", "Plan: abcdef012345",
		"ALIAS", "SOURCE", "db_password  secret     db-password      2     2   preserved", "settings     parameter  config/settings  3     4   edited",
		"Summary: preserved=2 renamed=0 edited=1 pinned=0 added=0 missing=0 removed=0; valid=true",
		"Other environments on schema v2: prod (active runtime@5), staging (no active release)",
		"Dry run: nothing was written. Re-run with --execute to apply.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q:\n%s", want, out)
		}
	}
	if idx := strings.Index(out, "db_password"); idx > strings.Index(out, "greeting") || strings.Index(out, "greeting") > strings.Index(out, "settings") {
		t.Fatalf("rows are not sorted by alias:\n%s", out)
	}
	if strings.Contains(out, "hello") || strings.Contains(c.stderr(), "hello") {
		t.Fatalf("a parameter value leaked into the output:\n%s\n%s", out, c.stderr())
	}
}

func TestReleaseMigrateDefaultsToNewestSchemaAboveSource(t *testing.T) {
	admin := &migrateAdminStub{responses: []*kmsv1.MigrateApplicationReleaseResponse{migratePreviewResponse()}}
	schemas := append(migrateTestSchemas(), &kmsv1.ConfigurationSchema{Version: 3, Application: "gradethis", Digest: "3333", ContractEstablished: true, Contract: migrateTestContract()})
	c := newMigrateCLI(t, migrateStubs{admin: admin, schema: &migrateSchemaStub{schemas: schemas}})
	// The artifact carries schema v2's digest, so the newest (v3) is a
	// digest mismatch: the operator must regenerate or pass --to-schema.
	if code := c.Run(migrateArgs(t)); code != exitUsage {
		t.Fatalf("exit = %d, want %d (stderr=%s)", code, exitUsage, c.stderr())
	}
	if !strings.Contains(c.stderr(), verifyTestSchemaSHA) || !strings.Contains(c.stderr(), "v3 has digest 3333") {
		t.Fatalf("digest mismatch must name both digests: %s", c.stderr())
	}
	if len(admin.requests()) != 0 {
		t.Fatal("a digest mismatch must fail before any AdminService call")
	}
	c = newMigrateCLI(t, migrateStubs{admin: admin, schema: &migrateSchemaStub{schemas: schemas}})
	if code := c.Run(migrateArgs(t, "--to-schema", "2")); code != exitOK {
		t.Fatalf("explicit --to-schema exit = %d: %s", code, c.stderr())
	}
	if calls := admin.requests(); len(calls) != 1 || calls[0].GetSchemaVersion() != 2 {
		t.Fatalf("explicit target not honoured: %v", calls)
	}
}

func TestReleaseMigrateFailsWhenNoNewerSchema(t *testing.T) {
	admin := &migrateAdminStub{}
	c := newMigrateCLI(t, migrateStubs{admin: admin, schema: &migrateSchemaStub{schemas: migrateTestSchemas()}})
	code := c.Run([]string{"release", "migrate", "dev/gradethis", "--from-schema", "2", "--insecure", "--token", "t"})
	if code != exitFailedPrecondition {
		t.Fatalf("exit = %d, want %d: %s", code, exitFailedPrecondition, c.stderr())
	}
	if !strings.Contains(c.stderr(), "no registered schema newer than v2") || !strings.Contains(c.stderr(), "registered: 1, 2") {
		t.Fatalf("stderr = %s", c.stderr())
	}
	if len(admin.requests()) != 0 {
		t.Fatal("no AdminService call expected")
	}
}

func TestReleaseMigrateUsesRegisteredContractWithoutArtifact(t *testing.T) {
	admin := &migrateAdminStub{responses: []*kmsv1.MigrateApplicationReleaseResponse{migratePreviewResponse()}}
	verify := &verifyReleaseStub{}
	c := newMigrateCLI(t, migrateStubs{admin: admin, verify: verify})
	code := c.Run([]string{"release", "migrate", "dev/gradethis", "--from-schema", "1", "--insecure", "--token", "t"})
	if code != exitOK {
		t.Fatalf("exit = %d: %s", code, c.stderr())
	}
	calls := admin.requests()
	if len(calls) != 1 || len(calls[0].GetContract()) != 3 || len(calls[0].GetChanges()) != 0 {
		t.Fatalf("request = %v, want the registered contract and no changes", calls)
	}
	if len(verify.calls) != 0 {
		t.Fatal("nothing to verify without an artifact")
	}
	schemas := migrateTestSchemas()
	schemas[1].ContractEstablished, schemas[1].Contract = false, nil
	c = newMigrateCLI(t, migrateStubs{schema: &migrateSchemaStub{schemas: schemas}})
	code = c.Run([]string{"release", "migrate", "dev/gradethis", "--from-schema", "1", "--insecure", "--token", "t"})
	if code != exitUsage || !strings.Contains(c.stderr(), "no registered contract") {
		t.Fatalf("exit = %d, stderr = %s", code, c.stderr())
	}
}

func TestReleaseMigrateArtifactOverridesFollowVerdicts(t *testing.T) {
	admin := &migrateAdminStub{responses: []*kmsv1.MigrateApplicationReleaseResponse{migratePreviewResponse()}}
	verify := &verifyReleaseStub{response: &kmsv1.VerifyReleaseDefaultsResponse{
		Name: "runtime", Version: 12, ActivationRevision: 7, SchemaVersion: 1,
		Entries: []*kmsv1.VerifyEntryVerdict{{Alias: "greeting", Verdict: "match"}, {Alias: "settings", Verdict: "unknown_alias"}},
	}}
	c := newMigrateCLI(t, migrateStubs{admin: admin, verify: verify})
	if code := c.Run(migrateArgs(t, "--output", "json")); code != exitOK {
		t.Fatalf("exit = %d: %s", code, c.stderr())
	}
	if len(verify.calls) != 1 {
		t.Fatalf("verify calls = %d", len(verify.calls))
	}
	verifyReq := verify.calls[0]
	if verifyReq.GetName() != "" || verifyReq.GetSchemaSha256() != "" || verifyReq.SchemaVersion == nil || *verifyReq.SchemaVersion != 1 {
		t.Fatalf("verify must select the source track by version with the application's release name: %v", verifyReq)
	}
	if len(verifyReq.GetEntries()) != 2 || verifyReq.GetEntries()[0].GetAlias() != "greeting" || verifyReq.GetEntries()[0].GetSha256() == "" {
		t.Fatalf("verify entries = %v", verifyReq.GetEntries())
	}
	for _, entry := range verifyReq.GetEntries() {
		if strings.Contains(entry.GetSha256(), "hello") {
			t.Fatal("verify entry carries a value")
		}
	}
	req := admin.requests()[0]
	if changeByAlias(t, req, "greeting") != nil {
		t.Fatalf("a matching artifact value must not be sent: %v", req.GetChanges())
	}
	settings := changeByAlias(t, req, "settings")
	if settings == nil || settings.Value == nil || settings.GetContentType() != "json" || settings.GetVersion() != 0 || settings.GetFromAlias() != "" {
		t.Fatalf("settings change = %v, want a value edit", settings)
	}
	if !strings.Contains(*settings.Value, `"b": 1`) {
		t.Fatalf("settings value = %q, want the artifact's exact bytes", *settings.Value)
	}
	document := requireOneJSONDocument(t, c)
	skipped, _ := document["skipped_overrides"].([]any)
	if len(skipped) != 1 || skipped[0] != "greeting" {
		t.Fatalf("skipped_overrides = %v", document["skipped_overrides"])
	}
	if document["dry_run"] != true || document["executed"] != false || document["source_schema_version"] != float64(1) || document["schema_version"] != float64(2) {
		t.Fatalf("document = %v", document)
	}
	if document["release"] != nil || document["activation"] != nil {
		t.Fatalf("release/activation must be null before execute: %v", document)
	}

	// differs also sends the value.
	verify = &verifyReleaseStub{response: &kmsv1.VerifyReleaseDefaultsResponse{
		Entries: []*kmsv1.VerifyEntryVerdict{{Alias: "greeting", Verdict: "differs"}, {Alias: "settings", Verdict: "match"}},
	}}
	admin = &migrateAdminStub{responses: []*kmsv1.MigrateApplicationReleaseResponse{migratePreviewResponse()}}
	c = newMigrateCLI(t, migrateStubs{admin: admin, verify: verify})
	if code := c.Run(migrateArgs(t)); code != exitOK {
		t.Fatalf("exit = %d: %s", code, c.stderr())
	}
	req = admin.requests()[0]
	if greeting := changeByAlias(t, req, "greeting"); greeting == nil || greeting.Value == nil || *greeting.Value != "hello" {
		t.Fatalf("differs must send the artifact value: %v", req.GetChanges())
	}
	if changeByAlias(t, req, "settings") != nil {
		t.Fatalf("matching settings must stay carried: %v", req.GetChanges())
	}
	if !strings.Contains(c.stdout(), "Unchanged (artifact value already carried): settings") {
		t.Fatalf("stdout = %s", c.stdout())
	}

	// A secret verdict is a broken artifact, never a migration.
	verify = &verifyReleaseStub{response: &kmsv1.VerifyReleaseDefaultsResponse{
		Entries: []*kmsv1.VerifyEntryVerdict{{Alias: "greeting", Verdict: "secret_alias"}, {Alias: "settings", Verdict: "match"}},
	}}
	admin = &migrateAdminStub{}
	c = newMigrateCLI(t, migrateStubs{admin: admin, verify: verify})
	if code := c.Run(migrateArgs(t)); code != exitError || !strings.Contains(c.stderr(), "is a secret in the source release") {
		t.Fatalf("exit = %d, stderr = %s", code, c.stderr())
	}
	if len(admin.requests()) != 0 {
		t.Fatal("no preview after a secret verdict")
	}
}

func TestReleaseMigrateRenameAndPin(t *testing.T) {
	admin := &migrateAdminStub{responses: []*kmsv1.MigrateApplicationReleaseResponse{migratePreviewResponse()}}
	verify := &verifyReleaseStub{response: &kmsv1.VerifyReleaseDefaultsResponse{
		Entries: []*kmsv1.VerifyEntryVerdict{{Alias: "message", Verdict: "match"}},
	}}
	c := newMigrateCLI(t, migrateStubs{admin: admin, verify: verify})
	code := c.Run(migrateArgs(t, "--rename", "greeting=message", "--pin", "settings=9", "--pin", "db_password=4"))
	if code != exitOK {
		t.Fatalf("exit = %d: %s", code, c.stderr())
	}
	req := admin.requests()[0]
	// The artifact's greeting was verified under its source alias "message"
	// and matched, so the rename carries the pin without a value.
	if len(verify.calls) != 1 || len(verify.calls[0].GetEntries()) != 1 || verify.calls[0].GetEntries()[0].GetAlias() != "message" {
		t.Fatalf("verify entries = %v, want the renamed-from alias only", verify.calls)
	}
	greeting := changeByAlias(t, req, "greeting")
	if greeting == nil || greeting.GetFromAlias() != "message" || greeting.Value != nil || greeting.GetVersion() != 0 {
		t.Fatalf("greeting change = %v", greeting)
	}
	settings := changeByAlias(t, req, "settings")
	if settings == nil || settings.GetVersion() != 9 || settings.Value != nil || settings.GetKey() != "" {
		t.Fatalf("settings change = %v, want a bare version pin (the server resolves the key)", settings)
	}
	if password := changeByAlias(t, req, "db_password"); password == nil || password.GetVersion() != 4 {
		t.Fatalf("db_password change = %v", password)
	}
	if !strings.Contains(c.stderr(), "settings: --pin overrides the artifact value") {
		t.Fatalf("stderr = %s", c.stderr())
	}
	if len(req.GetChanges()) != 3 || req.GetChanges()[0].GetAlias() != "db_password" || req.GetChanges()[2].GetAlias() != "settings" {
		t.Fatalf("changes must be sorted by alias: %v", req.GetChanges())
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--rename", "nope=greeting"}, `alias "nope" is not in the target contract`},
		{[]string{"--pin", "greeting=zero"}, "--pin greeting=zero: version must be a positive integer"},
		{[]string{"--pin", "greeting"}, "expected NAME=VALUE"},
		{[]string{"--rename", "greeting=greeting"}, "renames an alias to itself"},
		{[]string{"--pin", "greeting=1", "--pin", "greeting=2"}, `--pin names "greeting" twice`},
	} {
		c := newMigrateCLI(t, migrateStubs{admin: &migrateAdminStub{}})
		if code := c.Run(migrateArgs(t, tc.args...)); code != exitUsage || !strings.Contains(c.stderr(), tc.want) {
			t.Fatalf("%v: exit = %d, stderr = %s", tc.args, code, c.stderr())
		}
	}
}

func TestReleaseMigrateInvalidPreviewExitsFailedPrecondition(t *testing.T) {
	preview := migratePreviewResponse()
	preview.Valid = false
	preview.Entries = append(preview.Entries, &kmsv1.ApplicationReleasePlanEntry{Alias: "new_flag", Kind: "parameter", Ref: &kmsv1.ResourceRef{Key: "config/new_flag"}, Source: "missing"})
	preview.Validation = []*kmsv1.ReleaseValidationError{
		{Alias: "settings", Code: "schema", SchemaPointer: "/properties/settings/properties/limit", InstancePointer: "/settings/limit", Message: "must be >= 1"},
		{Alias: "new_flag", Code: "not_found", Message: "target alias requires a parameter edit or exact existing resource reference"},
	}
	admin := &migrateAdminStub{responses: []*kmsv1.MigrateApplicationReleaseResponse{preview}}
	c := newMigrateCLI(t, migrateStubs{admin: admin})
	c.assumeYes = true
	code := c.Run(migrateArgs(t, "--execute"))
	if code != exitFailedPrecondition {
		t.Fatalf("exit = %d, want %d: %s", code, exitFailedPrecondition, c.stderr())
	}
	if len(admin.requests()) != 1 {
		t.Fatalf("an invalid plan must never be executed: %d calls", len(admin.requests()))
	}
	for _, want := range []string{"Validation problems:", "INSTANCE POINTER", "/settings/limit", "must be >= 1", "Missing: new_flag", "valid=false"} {
		if !strings.Contains(c.stdout(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, c.stdout())
		}
	}
	if !strings.Contains(c.stderr(), "migration plan is invalid") {
		t.Fatalf("stderr = %s", c.stderr())
	}

	admin = &migrateAdminStub{responses: []*kmsv1.MigrateApplicationReleaseResponse{preview}}
	c = newMigrateCLI(t, migrateStubs{admin: admin})
	if code := c.Run(migrateArgs(t, "--output", "json")); code != exitFailedPrecondition {
		t.Fatalf("json exit = %d: %s", code, c.stderr())
	}
	document := requireOneJSONDocument(t, c)
	validation, _ := document["validation"].([]any)
	if document["valid"] != false || len(validation) != 2 {
		t.Fatalf("document = %v", document)
	}
	if first, _ := validation[0].(map[string]any); first["instance_pointer"] != "/settings/limit" {
		t.Fatalf("validation[0] = %v", validation[0])
	}
}

func TestReleaseMigrateExecuteReplaysThePreviewedPlan(t *testing.T) {
	admin := &migrateAdminStub{responses: []*kmsv1.MigrateApplicationReleaseResponse{migratePreviewResponse(), migrateExecutedResponse()}}
	c := newMigrateCLI(t, migrateStubs{admin: admin})
	code := c.Run(migrateArgs(t, "--execute", "--yes", "--metadata-json", `{"ticket":"OPS-1"}`))
	if code != exitOK {
		t.Fatalf("exit = %d: %s", code, c.stderr())
	}
	calls := admin.requests()
	if len(calls) != 2 {
		t.Fatalf("migrate calls = %d, want preview then execute", len(calls))
	}
	preview, execute := calls[0], calls[1]
	if preview.GetExecute() || preview.GetPlanDigest() != "" {
		t.Fatalf("first call must be a preview: %v", preview)
	}
	if !execute.GetExecute() || execute.GetPlanDigest() != "abcdef0123456789abcdef0123456789" {
		t.Fatalf("execute call = %v", execute)
	}
	if execute.ExpectedSourceVersion == nil || *execute.ExpectedSourceVersion != 12 || execute.ExpectedSourceActivationRevision == nil || *execute.ExpectedSourceActivationRevision != 7 {
		t.Fatalf("execute must pin the previewed source: %v", execute)
	}
	if execute.GetMetadataJson() != `{"ticket":"OPS-1"}` || execute.GetSourceSchemaVersion() != 1 || execute.GetSchemaVersion() != 2 {
		t.Fatalf("execute request = %v", execute)
	}
	if !proto.Equal(preview.GetContract()[1], execute.GetContract()[1]) || len(preview.GetChanges()) != len(execute.GetChanges()) {
		t.Fatal("execute must resend the previewed contract and changes")
	}
	if !strings.Contains(c.stdout(), "Activated runtime@13 on schema v2 (revision 8)") {
		t.Fatalf("stdout = %s", c.stdout())
	}
	if strings.Contains(c.stderr(), "[y/N]") {
		t.Fatalf("--yes must skip the prompt: %s", c.stderr())
	}
}

func TestReleaseMigrateExecuteRefusesWithoutYesOnNonTTY(t *testing.T) {
	admin := &migrateAdminStub{responses: []*kmsv1.MigrateApplicationReleaseResponse{migratePreviewResponse()}}
	c := newMigrateCLI(t, migrateStubs{admin: admin})
	code := c.Run(migrateArgs(t, "--execute"))
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d: %s", code, exitUsage, c.stderr())
	}
	if len(admin.requests()) != 1 {
		t.Fatalf("refusal must stop after the preview: %d calls", len(admin.requests()))
	}
	if !strings.Contains(c.stderr(), "without --yes on a non-interactive stdin") || !strings.Contains(c.stderr(), "upgrade dev/gradethis runtime from schema v1 (version 12) to schema v2") {
		t.Fatalf("stderr = %s", c.stderr())
	}
	if !strings.Contains(c.stdout(), "Plan: abcdef012345") {
		t.Fatalf("the preview must be shown before refusing: %s", c.stdout())
	}
}

func TestReleaseMigrateExecutePromptsOnTTY(t *testing.T) {
	for _, tc := range []struct {
		answer string
		calls  int
		code   int
	}{
		{"y\n", 2, exitOK},
		{"n\n", 1, exitUsage},
	} {
		admin := &migrateAdminStub{responses: []*kmsv1.MigrateApplicationReleaseResponse{migratePreviewResponse(), migrateExecutedResponse()}}
		c := newMigrateCLI(t, migrateStubs{admin: admin})
		c.isTTY = func() bool { return true }
		stdin := filepath.Join(t.TempDir(), "stdin")
		if err := os.WriteFile(stdin, []byte(tc.answer), 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(stdin)
		if err != nil {
			t.Fatal(err)
		}
		c.Stdin = file
		code := c.Run(migrateArgs(t, "--execute"))
		_ = file.Close()
		if code != tc.code || len(admin.requests()) != tc.calls {
			t.Fatalf("answer %q: exit = %d (want %d), calls = %d (want %d): %s", tc.answer, code, tc.code, len(admin.requests()), tc.calls, c.stderr())
		}
		if !strings.Contains(c.stderr(), "[y/N]") {
			t.Fatalf("prompt missing: %s", c.stderr())
		}
	}
}

func TestReleaseMigrateProductionRequiresConfirmation(t *testing.T) {
	admin := &migrateAdminStub{responses: []*kmsv1.MigrateApplicationReleaseResponse{migratePreviewResponse(), migrateExecutedResponse()}}
	c := newMigrateCLI(t, migrateStubs{admin: admin})
	args := []string{"release", "migrate", "prod/gradethis", "--from-schema", "1", "--from", writeVerifyArtifact(t), "--execute", "--yes", "--insecure", "--token", "t"}
	if code := c.Run(args); code != exitUsage || !strings.Contains(c.stderr(), "requires --confirm-production prod") {
		t.Fatalf("exit = %d, stderr = %s", code, c.stderr())
	}
	if len(admin.requests()) != 0 {
		t.Fatal("no RPC before the production guard")
	}
	c = newMigrateCLI(t, migrateStubs{admin: admin})
	if code := c.Run(append(args, "--confirm-production", "staging")); code != exitUsage || !strings.Contains(c.stderr(), "must exactly match") {
		t.Fatalf("exit = %d, stderr = %s", code, c.stderr())
	}
	c = newMigrateCLI(t, migrateStubs{admin: admin})
	if code := c.Run(append(args, "--confirm-production", "prod")); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, c.stderr())
	}
	if len(admin.requests()) != 2 {
		t.Fatalf("calls = %d", len(admin.requests()))
	}
	// A dry run against production needs no confirmation.
	c = newMigrateCLI(t, migrateStubs{admin: &migrateAdminStub{responses: []*kmsv1.MigrateApplicationReleaseResponse{migratePreviewResponse()}}})
	if code := c.Run([]string{"release", "migrate", "prod/gradethis", "--from-schema", "1", "--from", writeVerifyArtifact(t), "--insecure", "--token", "t"}); code != exitOK {
		t.Fatalf("dry run exit = %d: %s", code, c.stderr())
	}
}

func TestReleaseMigrateStalePlanExitsConflict(t *testing.T) {
	admin := &migrateAdminStub{
		responses: []*kmsv1.MigrateApplicationReleaseResponse{migratePreviewResponse(), nil},
		errs:      []error{nil, status.Error(codes.Aborted, "migration plan is stale; preview again")},
	}
	c := newMigrateCLI(t, migrateStubs{admin: admin})
	code := c.Run(migrateArgs(t, "--execute", "--yes"))
	if code != exitConflict {
		t.Fatalf("exit = %d, want %d: %s", code, exitConflict, c.stderr())
	}
	if !strings.Contains(c.stderr(), "plan is stale; preview again") {
		t.Fatalf("stderr = %s", c.stderr())
	}
	admin = &migrateAdminStub{errs: []error{status.Error(codes.FailedPrecondition, "no active release on schema track 1")}}
	c = newMigrateCLI(t, migrateStubs{admin: admin})
	if code := c.Run(migrateArgs(t)); code != exitFailedPrecondition || !strings.Contains(c.stderr(), "no active release") {
		t.Fatalf("exit = %d, stderr = %s", code, c.stderr())
	}
}

func TestReleaseMigrateArtifactDigestMismatchFailsBeforeAdmin(t *testing.T) {
	admin := &migrateAdminStub{responses: []*kmsv1.MigrateApplicationReleaseResponse{migratePreviewResponse()}}
	verify := &verifyReleaseStub{}
	schemas := migrateTestSchemas()
	schemas[1].Digest = "feedfacefeedfacefeedfacefeedfacefeedfacefeedfacefeedfacefeedface"
	c := newMigrateCLI(t, migrateStubs{admin: admin, schema: &migrateSchemaStub{schemas: schemas}, verify: verify})
	code := c.Run(migrateArgs(t, "--to-schema", "2"))
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d: %s", code, exitUsage, c.stderr())
	}
	if !strings.Contains(c.stderr(), verifyTestSchemaSHA) || !strings.Contains(c.stderr(), "feedface") {
		t.Fatalf("stderr must name both digests: %s", c.stderr())
	}
	if len(admin.requests()) != 0 || len(verify.calls) != 0 {
		t.Fatal("no AdminService or verify call after a digest mismatch")
	}
	c = newMigrateCLI(t, migrateStubs{admin: admin, schema: &migrateSchemaStub{schemas: schemas}})
	if code := c.Run(migrateArgs(t, "--to-schema", "5")); code != exitNotFound || !strings.Contains(c.stderr(), "no registered schema version 5") {
		t.Fatalf("exit = %d, stderr = %s", code, c.stderr())
	}
}

func TestReleaseMigrateJSONExecutePrintsOneDocument(t *testing.T) {
	admin := &migrateAdminStub{responses: []*kmsv1.MigrateApplicationReleaseResponse{migratePreviewResponse(), migrateExecutedResponse()}}
	c := newMigrateCLI(t, migrateStubs{admin: admin})
	code := c.Run(migrateArgs(t, "--execute", "--yes", "--output", "json"))
	if code != exitOK {
		t.Fatalf("exit = %d: %s", code, c.stderr())
	}
	document := requireOneJSONDocument(t, c)
	if document["executed"] != true || document["dry_run"] != false || document["plan_digest"] != "abcdef0123456789abcdef0123456789" {
		t.Fatalf("document = %v", document)
	}
	release, _ := document["release"].(map[string]any)
	activation, _ := document["activation"].(map[string]any)
	if release["version"] != float64(13) || release["schema_version"] != float64(2) || activation["activation_revision"] != float64(8) || activation["changed"] != true {
		t.Fatalf("release = %v activation = %v", release, activation)
	}
	entries, _ := document["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("entries = %v", document["entries"])
	}
	if first, _ := entries[0].(map[string]any); first["alias"] != "db_password" || first["key"] != "db-password" || first["source"] != "preserved" || first["to_version"] != float64(2) {
		t.Fatalf("entries[0] = %v", entries[0])
	}
	affected, _ := document["affected_environments"].([]any)
	if len(affected) != 2 {
		t.Fatalf("affected_environments = %v", document["affected_environments"])
	}
	for _, key := range []string{"validation", "skipped_overrides", "release_name", "source_version", "source_activation_revision", "definition_changed", "valid"} {
		if _, ok := document[key]; !ok {
			t.Fatalf("document lacks %q: %v", key, document)
		}
	}
	// The preview still reaches the operator, on stderr.
	if !strings.Contains(c.stderr(), "Plan: abcdef012345") {
		t.Fatalf("stderr = %s", c.stderr())
	}
}

func TestReleaseMigrateUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"release", "migrate"}, "requires ENV/APP"},
		{[]string{"release", "migrate", "dev/app"}, "requires --from-schema"},
		{[]string{"release", "migrate", "dev/app", "--from-schema", "1", "--to-schema", "1"}, "must differ from --from-schema"},
		{[]string{"release", "migrate", "dev/app", "--from-schema", "1", "--to-schema", "0"}, "never targets the schema-free track"},
		{[]string{"release", "migrate", "dev:app", "--from-schema", "1"}, "invalid namespace"},
		{[]string{"release", "migrate", "dev/app", "extra", "--from-schema", "1"}, `unexpected argument "extra"`},
		{[]string{"release", "migrate", "dev/app", "--from-schema", "1", "--confirm-production", "dev"}, "only valid for production"},
	} {
		c := newTestCLI()
		if code := c.Run(tc.args); code != exitUsage || !strings.Contains(c.stderr(), tc.want) {
			t.Fatalf("%v: exit = %d, stderr = %s", tc.args, code, c.stderr())
		}
	}
	c := newTestCLI()
	if code := c.Run([]string{"release", "migrate", "-h"}); code != 0 {
		t.Fatalf("help exit = %d: %s", code, c.stderr())
	}
	for _, want := range []string{"--from-schema", "--to-schema", "--rename", "--pin", "--execute", "--confirm-production", "dry run"} {
		if !strings.Contains(c.stderr(), want) {
			t.Fatalf("help lacks %q:\n%s", want, c.stderr())
		}
	}
	c = newTestCLI()
	if code := c.Run([]string{"release", "--help"}); code != 0 || !strings.Contains(c.stderr(), "migrate ENV/APP") {
		t.Fatalf("release usage lacks migrate: %s", c.stderr())
	}
}
