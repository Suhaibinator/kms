package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	fixtureconfig "github.com/Suhaibinator/kms/internal/configstorefixture/config"
	fixturekms "github.com/Suhaibinator/kms/internal/configstorefixture/configkms"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"github.com/Suhaibinator/kms/sdk/go/kmsverify"
)

const (
	kmsverifyEnv       = "prod"
	kmsverifyApp       = "kmsverify-app"
	kmsverifyNamespace = kmsverifyEnv + "/" + kmsverifyApp
	kmsverifyRelease   = "runtime"
	kmsverifySchemaID  = "kmsverify/runtime"
	// kmsverifyProfile deliberately differs from the environment name so the
	// audit assertions can tell the informational profile label apart from the
	// namespace that legitimately appears on every audit row.
	kmsverifyProfile    = "ci-profile-label"
	kmsverifyAuditEvent = "configuration_release.verify_defaults"
	// kmsverifyCanary is a value that exists only in the drifted release; no
	// report, error, or audit row may ever carry it.
	kmsverifyCanary = "kmsverify-drift-canary-feature-7c1e"
)

var sha256HexPattern = regexp.MustCompile(`[0-9a-f]{64}`)

// verifyRecorder is a testing.TB that records what kmsverify.Run does to it
// instead of ending the goroutine, so the outer test can assert on a Run that
// fails or skips. Fatal and Skip unwind through a panic that runKMSVerify
// recovers.
type verifyRecorder struct {
	testing.TB
	logs    []string
	fatal   string
	skipped string
}

type verifyRecorderStop struct{}

func (r *verifyRecorder) Helper() {}

func (r *verifyRecorder) Logf(format string, args ...any) {
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

func (r *verifyRecorder) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	panic(verifyRecorderStop{})
}

func (r *verifyRecorder) Skipf(format string, args ...any) {
	r.skipped = fmt.Sprintf(format, args...)
	panic(verifyRecorderStop{})
}

// runKMSVerify runs kmsverify.Run exactly as an application test would (it
// reads the process environment) against a recorder.
func runKMSVerify(t *testing.T, spec kmsverify.Spec[fixtureconfig.Config]) *verifyRecorder {
	t.Helper()
	recorder := &verifyRecorder{TB: t}
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				if _, ok := recovered.(verifyRecorderStop); !ok {
					panic(recovered)
				}
			}
		}()
		kmsverify.Run(recorder, spec)
	}()
	return recorder
}

// loadFixtureContract derives the application contract from the generated
// contract manifest so the application definition is exactly what
// kms-config-gen emitted, not a hand-copied approximation.
func loadFixtureContract(t *testing.T) (schemaSHA256 string, contract []domain.ApplicationContractField, sdkContract []configstore.ContractEntry) {
	t.Helper()
	raw, err := os.ReadFile("../configstorefixture/runtime.contract.json")
	if err != nil {
		t.Fatalf("read generated contract: %v", err)
	}
	var manifest struct {
		SchemaSHA256 string `json:"schema_sha256"`
		Groups       []struct {
			Alias       string `json:"alias"`
			Kind        string `json:"kind"`
			ContentType string `json:"content_type"`
		} `json:"groups"`
		Secrets []struct {
			Alias string `json:"alias"`
			Kind  string `json:"kind"`
		} `json:"secrets"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse generated contract: %v", err)
	}
	for _, group := range manifest.Groups {
		contract = append(contract, domain.ApplicationContractField{Alias: group.Alias, Kind: group.Kind, ContentType: group.ContentType})
		sdkContract = append(sdkContract, configstore.ContractEntry{Alias: group.Alias, Kind: configstore.ContractKind(group.Kind), ContentType: group.ContentType})
	}
	for _, secret := range manifest.Secrets {
		contract = append(contract, domain.ApplicationContractField{Alias: secret.Alias, Kind: secret.Kind})
		sdkContract = append(sdkContract, configstore.ContractEntry{Alias: secret.Alias, Kind: configstore.ContractKind(secret.Kind)})
	}
	return manifest.SchemaSHA256, contract, sdkContract
}

// reportHasRow reports whether the tabwriter-aligned report contains a
// verdict row with exactly these columns.
func reportHasRow(report, verdict, alias, contentType string) bool {
	for _, line := range strings.Split(report, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == verdict && fields[1] == alias && fields[2] == contentType {
			return true
		}
	}
	return false
}

// kmsverifyFixture is the application, namespace, release, and identities the
// subtests share against one real loopback server.
type kmsverifyFixture struct {
	env   *loopbackTLSEnv
	ctx   context.Context
	admin core.Principal
	ns    domain.NamespaceRef
	// otherNS is a second real namespace the verify policy does not cover.
	otherNS     domain.NamespaceRef
	sdkContract []configstore.ContractEntry
	spec        kmsverify.Spec[fixtureconfig.Config]

	// verifyToken belongs to the unbound verify-only identity.
	verifyName  string
	verifyToken string
}

// setupKMSVerifyFixture registers the generated schema, defines the
// application from the generated contract, applies the source defaults as
// parameters through the defaults artifact path, and ships the first release.
func setupKMSVerifyFixture(t *testing.T) *kmsverifyFixture {
	t.Helper()
	env := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	admin := core.Principal{Identity: domain.Identity{Name: "network-root", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken}
	ns := domain.NamespaceRef{Env: kmsverifyEnv, App: kmsverifyApp}

	schemaJSON, err := os.ReadFile("../configstorefixture/runtime.schema.json")
	if err != nil {
		t.Fatalf("read generated schema: %v", err)
	}
	schema, err := env.svc.CreateConfigurationSchema(ctx, admin, kmsverifySchemaID, string(schemaJSON), `{"owner":"kmsverify-integration"}`)
	if err != nil {
		t.Fatalf("register generated schema: %v", err)
	}
	schemaSHA256, contract, sdkContract := loadFixtureContract(t)
	if schema.Digest != schemaSHA256 {
		t.Fatalf("registered schema digest %s != generated contract schema_sha256 %s", schema.Digest, schemaSHA256)
	}
	if _, err := env.svc.CreateApplication(ctx, admin, domain.Application{
		Name: kmsverifyApp, ReleaseName: kmsverifyRelease, SchemaID: schema.ID, SchemaVersion: schema.Version, Contract: contract,
	}); err != nil {
		t.Fatalf("create application: %v", err)
	}
	if _, err := env.svc.CreateNamespace(ctx, admin, ns, "kmsverify integration", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	otherNS := domain.NamespaceRef{Env: kmsverifyEnv, App: "kmsverify-other"}
	if _, err := env.svc.CreateNamespace(ctx, admin, otherNS, "kmsverify integration (ungranted)", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatalf("create other namespace: %v", err)
	}
	for key, value := range map[string]string{"database_password": "kmsverify-password-canary", "runtime_token": "kmsverify-runtime-token-canary"} {
		if _, err := env.svc.PutSecret(ctx, admin, core.PutSecretInput{
			Ref: domain.Ref{NS: ns, Key: key}, Value: []byte(value), ContentType: "text/plain", Metadata: "{}",
		}); err != nil {
			t.Fatalf("put secret %s: %v", key, err)
		}
	}

	// Source defaults reach the server only through the parameter-only
	// defaults artifact: preview, then execute with the previewed plan digest.
	artifact, err := fixturekms.EncodeDefaultsArtifact(kmsverifyProfile, fixtureconfig.Defaults())
	if err != nil {
		t.Fatalf("encode defaults artifact: %v", err)
	}
	adminClient := kmsv1.NewAdminServiceClient(env.adminConn)
	authCtx := networkAuthContext(ctx, env.adminToken)
	preview, err := adminClient.ApplyApplicationDefaults(authCtx, &kmsv1.ApplyApplicationDefaultsRequest{
		Namespace: networkNS(ns.Env, ns.App), Artifact: artifact,
	})
	if err != nil {
		t.Fatalf("preview defaults: %v", err)
	}
	if preview.GetExecuted() || preview.GetPlanDigest() == "" || len(preview.GetEntries()) != 2 || len(preview.GetMissingSecrets()) != 0 {
		t.Fatalf("defaults preview = %+v", preview)
	}
	applied, err := adminClient.ApplyApplicationDefaults(authCtx, &kmsv1.ApplyApplicationDefaultsRequest{
		Namespace: networkNS(ns.Env, ns.App), Artifact: artifact, Execute: true, PlanDigest: preview.GetPlanDigest(),
	})
	if err != nil {
		t.Fatalf("apply defaults: %v", err)
	}
	if !applied.GetExecuted() {
		t.Fatalf("defaults apply = %+v", applied)
	}
	for _, entry := range applied.GetEntries() {
		if entry.GetAppliedVersion() != 1 {
			t.Fatalf("applied defaults entry %s version = %d, want 1", entry.GetAlias(), entry.GetAppliedVersion())
		}
	}

	shipped, err := env.svc.ShipApplicationChange(ctx, admin, domain.ShipInput{Application: kmsverifyApp, Environment: kmsverifyEnv})
	if err != nil {
		t.Fatalf("ship first release: %v", err)
	}
	if shipped.Status != domain.ShipStatusActivated || shipped.Release == nil || shipped.Release.Version != 1 || len(shipped.Release.Entries) != len(contract) {
		t.Fatalf("first release = %+v", shipped)
	}

	f := &kmsverifyFixture{
		env: env, ctx: ctx, admin: admin, ns: ns, otherNS: otherNS, sdkContract: sdkContract,
		spec: kmsverify.Spec[fixtureconfig.Config]{
			Defaults: func(profile string) (*fixtureconfig.Config, error) {
				if profile != kmsverifyProfile {
					return nil, fmt.Errorf("unexpected profile %q", profile)
				}
				return fixtureconfig.Defaults(), nil
			},
			Verify: fixturekms.VerifyReleaseDefaults,
		},
		verifyName: "kmsverify-ci",
	}
	f.verifyToken = f.createIdentity(t, f.verifyName, nil, true)
	return f
}

// createIdentity mints a token client, optionally bound to the fixture
// namespace, and optionally grants it configuration-release:verify-defaults
// on the fixture namespace and nothing else.
func (f *kmsverifyFixture) createIdentity(t *testing.T, name string, home *domain.NamespaceRef, grantVerify bool) string {
	t.Helper()
	created, err := f.env.svc.CreateIdentity(f.ctx, f.admin, core.CreateIdentityInput{
		Name: name, Kind: domain.IdentityKindClient, Namespace: home, AuthMethods: []domain.AuthMethod{domain.AuthMethodToken},
	})
	if err != nil {
		t.Fatalf("create identity %s: %v", name, err)
	}
	if grantVerify {
		if _, err := f.env.svc.CreatePolicy(f.ctx, f.admin, domain.Policy{
			Name: name + "-verify", Subject: name,
			Allow: []domain.PolicyRule{{Operation: domain.OpConfigurationReleaseVerifyDefaults, Env: f.ns.Env, App: f.ns.App}},
		}); err != nil {
			t.Fatalf("grant verify to %s: %v", name, err)
		}
	}
	return created.Token
}

// client builds an SDK client for token over the harness TLS trust.
func (f *kmsverifyFixture) client(t *testing.T, token string) *kmsclient.Client {
	t.Helper()
	client, err := kmsclient.NewClient(kmsclient.Config{
		Endpoint: f.env.endpoint(), Token: token, TLS: f.env.clientTLS(nil), Timeout: 3 * time.Second, ClientName: "kmsverify-integration",
	})
	if err != nil {
		t.Fatalf("create SDK client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// setVerifyEnv points kmsverify.Run at the loopback server as the verify-only
// identity, trusting the harness CA through KMS_VERIFY_CA_FILE.
func (f *kmsverifyFixture) setVerifyEnv(t *testing.T) {
	t.Helper()
	t.Setenv(kmsverify.EnvEndpoint, f.env.endpoint())
	t.Setenv(kmsverify.EnvToken, f.verifyToken)
	t.Setenv(kmsverify.EnvCAFile, f.env.caFile(t))
	t.Setenv(kmsverify.EnvCAPEM, "")
	t.Setenv(kmsverify.EnvInsecure, "")
	t.Setenv(kmsverify.EnvProfile, kmsverifyProfile)
	t.Setenv(kmsverify.EnvNamespace, kmsverifyNamespace)
	t.Setenv(kmsverify.EnvRelease, "")
	t.Setenv(kmsverify.EnvRequired, "1")
}

func (f *kmsverifyFixture) verifyEnv(t *testing.T) kmsverify.Env {
	t.Helper()
	return kmsverify.Env{
		Endpoint: f.env.endpoint(), Token: f.verifyToken, CAFile: f.env.caFile(t),
		Profile: kmsverifyProfile, Namespace: kmsverifyNamespace, Required: true,
	}
}

// shipRuntime writes a new runtime group value and activates a release pinning
// it, returning the activated version.
func (f *kmsverifyFixture) shipRuntime(t *testing.T, value string, expectedActive uint64) uint64 {
	t.Helper()
	shipped, err := f.env.svc.ShipApplicationChange(f.ctx, f.admin, domain.ShipInput{
		Application: kmsverifyApp, Environment: kmsverifyEnv, ExpectedActiveVersion: &expectedActive,
		Changes: []domain.ShipChange{{Alias: "runtime", Value: &value}},
	})
	if err != nil {
		t.Fatalf("ship runtime change: %v", err)
	}
	if shipped.Status != domain.ShipStatusActivated || shipped.Release == nil || shipped.Release.Version != expectedActive+1 {
		t.Fatalf("ship runtime change = %+v", shipped)
	}
	return shipped.Release.Version
}

func (f *kmsverifyFixture) auditRows(t *testing.T) []domain.AuditEvent {
	t.Helper()
	events, _, err := f.env.svc.ListAuditEvents(f.ctx, f.admin, domain.AuditFilter{EventType: kmsverifyAuditEvent}, storage.ListPage{Limit: 1000})
	if err != nil {
		t.Fatalf("list verify audit rows: %v", err)
	}
	return events
}

// runtimeDriftRoot returns the fixture defaults with the runtime group
// changed; it is what the drifted release pins, so against that release it
// matches everywhere while the source defaults differ on runtime.
func runtimeDriftRoot() *fixtureconfig.Config {
	root := fixtureconfig.Defaults()
	root.Features = []string{kmsverifyCanary}
	return root
}

// databaseDriftRoot returns the source defaults with the database group
// changed; against the drifted release both groups differ.
func databaseDriftRoot() *fixtureconfig.Config {
	root := fixtureconfig.Defaults()
	root.Endpoint.Host = "db.drifted.internal"
	return root
}

// verifyWithSchema verifies root's groups against the active release with an
// explicit schema digest, bypassing the generated binding's pinned digest.
func (f *kmsverifyFixture) verifyWithSchema(client *kmsclient.Client, root *fixtureconfig.Config, schemaSHA256 string) (configstore.VerifyResult, error) {
	groups, err := fixturekms.EncodeParameterGroups(root)
	if err != nil {
		return configstore.VerifyResult{}, err
	}
	return configstore.VerifyDefaults(f.ctx, client, configstore.VerifyInput{
		SchemaSHA256: schemaSHA256, Contract: f.sdkContract, Groups: groups,
	}, configstore.VerifyOptions{Namespace: kmsverifyNamespace, Profile: kmsverifyProfile})
}

// TestKMSVerifyOverRealKMS is the end-to-end proof for defaults verification:
// a CI-style kmsverify.Run against a real server, the value-free failure
// report on drift, the least-privilege identity shape, the per-identity
// budgets, and the counts-only audit trail.
func TestKMSVerifyOverRealKMS(t *testing.T) {
	f := setupKMSVerifyFixture(t)
	// Budgets are process-wide on the service; every subtest that changes them
	// restores the defaults, and the fixture restores them once more at the end.
	t.Cleanup(func() { f.env.svc.SetVerifyDefaultsLimits(core.DefaultVerifyDefaultsLimits()) })

	var driftedVersion uint64

	t.Run("run passes against the source defaults release", func(t *testing.T) {
		f.setVerifyEnv(t)
		recorder := runKMSVerify(t, f.spec)
		if recorder.fatal != "" || recorder.skipped != "" {
			t.Fatalf("kmsverify.Run fatal=%q skipped=%q", recorder.fatal, recorder.skipped)
		}
		if len(recorder.logs) != 1 || !strings.Contains(recorder.logs[0], "result: active release matches source defaults") ||
			!strings.Contains(recorder.logs[0], kmsverifyNamespace+" "+kmsverifyRelease+"@1#") || !strings.Contains(recorder.logs[0], "schema: match") {
			t.Fatalf("passing run logged %q", recorder.logs)
		}
		if !strings.Contains(recorder.logs[0], "summary: match=2 differs=0") {
			t.Fatalf("passing run summary = %q", recorder.logs[0])
		}
		// Required + unset endpoint must fail rather than skip, so a CI job
		// cannot silently pass when the server variables are missing.
		t.Setenv(kmsverify.EnvEndpoint, "")
		recorder = runKMSVerify(t, f.spec)
		if recorder.fatal == "" || !strings.Contains(recorder.fatal, kmsverify.EnvRequired) || recorder.skipped != "" {
			t.Fatalf("required run without endpoint fatal=%q skipped=%q", recorder.fatal, recorder.skipped)
		}
	})

	t.Run("drifted release fails value-free", func(t *testing.T) {
		groups, err := fixturekms.EncodeParameterGroups(runtimeDriftRoot())
		if err != nil {
			t.Fatal(err)
		}
		driftedRuntime := string(groups["runtime"])
		if !strings.Contains(driftedRuntime, kmsverifyCanary) {
			t.Fatalf("drifted runtime document %s does not carry the canary", driftedRuntime)
		}
		driftedVersion = f.shipRuntime(t, driftedRuntime, 1)

		result, err := kmsverify.Verify(f.ctx, f.spec, f.verifyEnv(t))
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if result.Passed() || !result.SchemaMatches || result.ReleaseVersion != driftedVersion || len(result.Entries) != 2 || result.Unverified != 0 {
			t.Fatalf("drifted result = %+v", result)
		}
		failures := result.Failures()
		if len(failures) != 1 || failures[0].Alias != "runtime" || failures[0].Verdict != kmsclient.VerifyVerdictDiffers || failures[0].ContentType != "json" {
			t.Fatalf("failures = %+v, want exactly runtime/differs", failures)
		}
		report := result.Report()
		if !reportHasRow(report, kmsclient.VerifyVerdictDiffers, "runtime", "json") || !reportHasRow(report, kmsclient.VerifyVerdictMatch, "database", "json") ||
			!strings.Contains(report, "summary: match=1 differs=1") || !strings.Contains(report, "result: active release differs from source defaults") {
			t.Fatalf("report = %q", report)
		}
		for _, leak := range []string{kmsverifyCanary, driftedRuntime, `"features"`, "db.internal", "search"} {
			if strings.Contains(report, leak) {
				t.Fatalf("report leaked %q: %q", leak, report)
			}
		}
		if sha256HexPattern.MatchString(report) {
			t.Fatalf("report leaked a hash: %q", report)
		}

		// The test entry point fails the test with that same report.
		f.setVerifyEnv(t)
		recorder := runKMSVerify(t, f.spec)
		if recorder.fatal == "" || recorder.skipped != "" || len(recorder.logs) != 0 {
			t.Fatalf("drifted run fatal=%q skipped=%q logs=%q", recorder.fatal, recorder.skipped, recorder.logs)
		}
		if !reportHasRow(recorder.fatal, kmsclient.VerifyVerdictDiffers, "runtime", "json") || strings.Contains(recorder.fatal, kmsverifyCanary) {
			t.Fatalf("drifted run fatal = %q", recorder.fatal)
		}
	})

	t.Run("verify-only identity has no read access", func(t *testing.T) {
		client := f.client(t, f.verifyToken)
		if _, err := client.GetParameter(f.ctx, "/"+kmsverifyNamespace+"/runtime"); !errors.Is(err, kmsclient.ErrPermissionDenied) {
			t.Fatalf("GetParameter as verify-only = %v, want ErrPermissionDenied", err)
		}
		if _, err := client.GetParameter(f.ctx, "/"+kmsverifyNamespace+"/database"); !errors.Is(err, kmsclient.ErrPermissionDenied) {
			t.Fatalf("GetParameter database as verify-only = %v, want ErrPermissionDenied", err)
		}
		if _, err := client.GetSecret(f.ctx, "/"+kmsverifyNamespace+"/database_password"); !errors.Is(err, kmsclient.ErrPermissionDenied) {
			t.Fatalf("GetSecret as verify-only = %v, want ErrPermissionDenied", err)
		}
		releases := kmsv1.NewConfigurationReleaseServiceClient(f.env.dial(t, nil))
		verifyCtx := networkAuthContext(f.ctx, f.verifyToken)
		if _, err := releases.GetActiveRelease(verifyCtx, &kmsv1.GetActiveReleaseRequest{Namespace: networkNS(f.ns.Env, f.ns.App), Name: kmsverifyRelease}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("GetActiveRelease as verify-only = %v, want PermissionDenied", err)
		}
		if _, err := releases.GetRelease(verifyCtx, &kmsv1.GetReleaseRequest{Namespace: networkNS(f.ns.Env, f.ns.App), Name: kmsverifyRelease, Version: driftedVersion}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("GetRelease as verify-only = %v, want PermissionDenied", err)
		}
		if _, err := releases.ListReleases(verifyCtx, &kmsv1.ListReleasesRequest{Namespace: networkNS(f.ns.Env, f.ns.App)}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("ListReleases as verify-only = %v, want PermissionDenied", err)
		}
		// The generated store, which needs release reads, cannot start as the
		// verify-only identity either (addressed at the fixture namespace, since
		// the identity has no home of its own).
		addressed, err := kmsclient.NewClient(kmsclient.Config{
			Endpoint: f.env.endpoint(), Namespace: kmsverifyNamespace, Token: f.verifyToken, TLS: f.env.clientTLS(nil), Timeout: 3 * time.Second, ClientName: "kmsverify-integration",
		})
		if err != nil {
			t.Fatalf("create addressed SDK client: %v", err)
		}
		defer func() { _ = addressed.Close() }()
		if _, err := fixturekms.Start(f.ctx, addressed, fixturekms.Options{
			Release: kmsverifyRelease, Defaults: fixtureconfig.Defaults, InstanceID: "verify-only",
			Callbacks: configstore.Callbacks{OnDefaultMismatch: func(configstore.DefaultMismatchReport) {}},
		}); !errors.Is(err, kmsclient.ErrPermissionDenied) {
			t.Fatalf("generated store Start as verify-only = %v, want ErrPermissionDenied", err)
		}
		// Yet the same identity, unbound and without any read grant, still verifies.
		result, err := fixturekms.VerifyReleaseDefaults(f.ctx, client, fixtureconfig.Defaults(), configstore.VerifyOptions{Namespace: kmsverifyNamespace, Profile: kmsverifyProfile})
		if err != nil || len(result.Entries) != 2 {
			t.Fatalf("verify as verify-only = (%+v, %v)", result, err)
		}
	})

	t.Run("bound identity without the policy is denied verify", func(t *testing.T) {
		ns := f.ns
		boundToken := f.createIdentity(t, "kmsverify-bound-reader", &ns, false)
		bound := f.client(t, boundToken)
		// The implicit home grant lets it read its own namespace...
		if _, err := bound.GetParameter(f.ctx, "database"); err != nil {
			t.Fatalf("bound identity could not read its home namespace: %v", err)
		}
		// ...but verify-defaults is never implicit, even at home.
		result, err := fixturekms.VerifyReleaseDefaults(f.ctx, bound, fixtureconfig.Defaults(), configstore.VerifyOptions{Namespace: kmsverifyNamespace})
		if !errors.Is(err, kmsclient.ErrPermissionDenied) || len(result.Entries) != 0 {
			t.Fatalf("verify as bound reader = (%+v, %v), want ErrPermissionDenied", result, err)
		}
		// A different unbound identity with only the policy verifies fine.
		otherToken := f.createIdentity(t, "kmsverify-unbound-second", nil, true)
		other := f.client(t, otherToken)
		result, err = fixturekms.VerifyReleaseDefaults(f.ctx, other, fixtureconfig.Defaults(), configstore.VerifyOptions{Namespace: kmsverifyNamespace})
		if err != nil || len(result.Entries) != 2 || result.ReleaseVersion != driftedVersion {
			t.Fatalf("verify as second unbound identity = (%+v, %v)", result, err)
		}
		// The policy is namespace-scoped: another namespace is denied.
		result, err = fixturekms.VerifyReleaseDefaults(f.ctx, other, fixtureconfig.Defaults(), configstore.VerifyOptions{Namespace: f.otherNS.Env + "/" + f.otherNS.App})
		if !errors.Is(err, kmsclient.ErrPermissionDenied) || len(result.Entries) != 0 {
			t.Fatalf("verify outside the granted namespace = (%+v, %v), want ErrPermissionDenied", result, err)
		}
	})

	t.Run("request budget", func(t *testing.T) {
		const burst = 3
		f.env.svc.SetVerifyDefaultsLimits(core.VerifyDefaultsLimits{RequestsPerHour: 60, Burst: burst, MismatchBudgetPerHour: 500})
		t.Cleanup(func() { f.env.svc.SetVerifyDefaultsLimits(core.DefaultVerifyDefaultsLimits()) })
		token := f.createIdentity(t, "kmsverify-request-budget", nil, true)
		client := f.client(t, token)
		opts := configstore.VerifyOptions{Namespace: kmsverifyNamespace, Profile: kmsverifyProfile}
		for i := 1; i <= burst; i++ {
			if result, err := fixturekms.VerifyReleaseDefaults(f.ctx, client, fixtureconfig.Defaults(), opts); err != nil || len(result.Entries) != 2 {
				t.Fatalf("call %d within the request budget = (%+v, %v)", i, result, err)
			}
		}
		result, err := fixturekms.VerifyReleaseDefaults(f.ctx, client, fixtureconfig.Defaults(), opts)
		if !errors.Is(err, kmsclient.ErrRateLimited) || !strings.Contains(err.Error(), "request budget") || len(result.Entries) != 0 {
			t.Fatalf("call %d over the request budget = (%+v, %v), want ErrRateLimited", burst+1, result, err)
		}
		// The budget is per identity: the verify-only identity is unaffected.
		if result, err := fixturekms.VerifyReleaseDefaults(f.ctx, f.client(t, f.verifyToken), fixtureconfig.Defaults(), opts); err != nil || len(result.Entries) != 2 {
			t.Fatalf("other identity after exhaustion = (%+v, %v)", result, err)
		}
	})

	t.Run("mismatch budget", func(t *testing.T) {
		tight := core.VerifyDefaultsLimits{RequestsPerHour: 60, Burst: 100, MismatchBudgetPerHour: 1}
		f.env.svc.SetVerifyDefaultsLimits(tight)
		t.Cleanup(func() { f.env.svc.SetVerifyDefaultsLimits(core.DefaultVerifyDefaultsLimits()) })
		token := f.createIdentity(t, "kmsverify-mismatch-budget", nil, true)
		client := f.client(t, token)
		opts := configstore.VerifyOptions{Namespace: kmsverifyNamespace, Profile: kmsverifyProfile}

		// Two non-matching aliases against a budget of one: refused outright,
		// with no verdicts at all, not even for the alias it could afford.
		result, err := fixturekms.VerifyReleaseDefaults(f.ctx, client, databaseDriftRoot(), opts)
		if !errors.Is(err, kmsclient.ErrRateLimited) || !strings.Contains(err.Error(), "mismatch budget") {
			t.Fatalf("two mismatches over a budget of one = (%+v, %v), want ErrRateLimited", result, err)
		}
		if len(result.Entries) != 0 || result.ReleaseVersion != 0 {
			t.Fatalf("budget-refused call leaked verdicts: %+v", result)
		}
		// A refusal is itself an answer, so it drains the request budget too:
		// even a call that would match everywhere is refused until refill.
		result, err = fixturekms.VerifyReleaseDefaults(f.ctx, client, runtimeDriftRoot(), opts)
		if !errors.Is(err, kmsclient.ErrRateLimited) || !strings.Contains(err.Error(), "request budget") || len(result.Entries) != 0 {
			t.Fatalf("all-match call after a refusal = (%+v, %v), want ErrRateLimited", result, err)
		}

		// Fresh buckets: exactly one non-match is affordable, then the same
		// single non-match is refused.
		f.env.svc.SetVerifyDefaultsLimits(tight)
		result, err = fixturekms.VerifyReleaseDefaults(f.ctx, client, fixtureconfig.Defaults(), opts)
		if err != nil || len(result.Failures()) != 1 || result.Failures()[0].Alias != "runtime" {
			t.Fatalf("one mismatch within budget = (%+v, %v)", result, err)
		}
		result, err = fixturekms.VerifyReleaseDefaults(f.ctx, client, fixtureconfig.Defaults(), opts)
		if !errors.Is(err, kmsclient.ErrRateLimited) || len(result.Entries) != 0 {
			t.Fatalf("one mismatch after budget exhaustion = (%+v, %v), want ErrRateLimited", result, err)
		}

		// A non-matching schema digest is one bit about the pinned schema and
		// is charged like an alias mismatch: the first all-match call with a
		// wrong digest is answered (schema: differs), the second is refused.
		f.env.svc.SetVerifyDefaultsLimits(tight)
		result, err = f.verifyWithSchema(client, runtimeDriftRoot(), strings.Repeat("0", 64))
		if err != nil || result.SchemaMatches || len(result.Failures()) != 0 || result.Passed() {
			t.Fatalf("wrong schema digest within budget = (%+v, %v)", result, err)
		}
		result, err = f.verifyWithSchema(client, runtimeDriftRoot(), strings.Repeat("0", 64))
		if !errors.Is(err, kmsclient.ErrRateLimited) || len(result.Entries) != 0 {
			t.Fatalf("wrong schema digest after budget exhaustion = (%+v, %v), want ErrRateLimited", result, err)
		}
	})

	t.Run("audit rows carry counts only", func(t *testing.T) {
		rows := f.auditRows(t)
		if len(rows) == 0 {
			t.Fatal("no verify audit rows")
		}
		wantKeys := []string{
			"entry_count", "match_count", "differs_count", "missing_count", "unknown_alias_count",
			"secret_alias_count", "unsupported_count", "unverified_count", "schema_matches", "limited",
		}
		decisions := map[string]int{}
		var sawPass, sawDrift, sawRequestLimited, sawMismatchLimited bool
		for _, row := range rows {
			decisions[row.Decision]++
			// A request-budget refusal is audited before the release name is
			// resolved, so its key is whatever the caller sent (empty when the
			// binding relies on the application's release name).
			if row.ResourceEnv != f.ns.Env || row.ResourceApp != f.ns.App || (row.ResourceKey != kmsverifyRelease && row.ResourceKey != "") {
				t.Fatalf("verify audit row addressed %s/%s/%s, want the fixture release", row.ResourceEnv, row.ResourceApp, row.ResourceKey)
			}
			if !strings.HasPrefix(row.ActorIdentity, "kmsverify-") {
				t.Fatalf("verify audit row actor = %q", row.ActorIdentity)
			}
			var meta map[string]string
			if err := json.Unmarshal([]byte(row.Metadata), &meta); err != nil {
				t.Fatalf("verify audit metadata %q: %v", row.Metadata, err)
			}
			if len(meta) != len(wantKeys) {
				t.Fatalf("verify audit metadata keys = %v, want exactly %v", meta, wantKeys)
			}
			for _, key := range wantKeys {
				value, ok := meta[key]
				if !ok {
					t.Fatalf("verify audit metadata missing %s: %v", key, meta)
				}
				if key == "schema_matches" || key == "limited" {
					if _, err := strconv.ParseBool(value); err != nil {
						t.Fatalf("verify audit metadata %s = %q is not a bool", key, value)
					}
					continue
				}
				if _, err := strconv.Atoi(value); err != nil {
					t.Fatalf("verify audit metadata %s = %q is not a count", key, value)
				}
			}
			for _, leak := range []string{"database", "runtime_token", "database_password", kmsverifyProfile, kmsverifyCanary} {
				if strings.Contains(row.Metadata, leak) {
					t.Fatalf("verify audit metadata carried %q: %s", leak, row.Metadata)
				}
			}
			if sha256HexPattern.MatchString(row.Metadata) {
				t.Fatalf("verify audit metadata carried a hash: %s", row.Metadata)
			}
			switch {
			case row.Decision == "allow" && meta["match_count"] == "2" && meta["schema_matches"] == "true" && meta["limited"] == "false":
				sawPass = true
			case row.Decision == "allow" && meta["match_count"] == "1" && meta["differs_count"] == "1":
				sawDrift = true
			case row.Decision == "deny" && meta["limited"] == "true" && meta["entry_count"] == "2" && meta["match_count"] == "0" && meta["differs_count"] == "0":
				// The request budget refuses before any comparison, so the
				// counts are all zero.
				sawRequestLimited = true
			case row.Decision == "deny" && meta["limited"] == "true" && meta["differs_count"] == "2":
				// The mismatch budget refuses after comparing, so the row
				// records what would have been disclosed but the caller got nothing.
				sawMismatchLimited = true
			}
		}
		if !sawPass || !sawDrift || !sawRequestLimited || !sawMismatchLimited {
			t.Fatalf("verify audit coverage pass=%t drift=%t request_limited=%t mismatch_limited=%t decisions=%v", sawPass, sawDrift, sawRequestLimited, sawMismatchLimited, decisions)
		}
		if decisions["deny"] < 5 || decisions["allow"] < 8 {
			t.Fatalf("verify audit decisions = %v", decisions)
		}
		// Authorization denials of the oracle are audited as authz denials,
		// not as verification rows, so the verification trail never contains
		// rows for identities that could not call it.
		for _, row := range rows {
			if row.ActorIdentity == "kmsverify-bound-reader" {
				t.Fatalf("denied identity produced a verification audit row: %+v", row)
			}
		}
	})
}
