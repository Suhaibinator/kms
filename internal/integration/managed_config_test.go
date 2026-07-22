package integration

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	fixtureconfig "github.com/Suhaibinator/kms/internal/configstorefixture/config"
	fixturekms "github.com/Suhaibinator/kms/internal/configstorefixture/configkms"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/paramstore"
)

const (
	managedNamespace = "prod/managed-config"
	managedRelease   = "runtime"
	managedSchemaID  = "managed-config/runtime"

	managedDatabaseDefault = `{"endpoint":{"host":"db.internal","ports":[5432,5433],"labels":{"role":["primary","readonly"]},"zones":["us-west-1a","us-west-1b"]},"max_open":20,"timeout":"3s"}`
	managedDatabaseRestart = `{"endpoint":{"host":"db.failover.internal","ports":[5432,5433],"labels":{"role":["primary","readonly"]},"zones":["us-west-1a","us-west-1b"]},"max_open":20,"timeout":"3s"}`
	managedRuntimeDefault  = `{"features":["search","reports"],"payload":"Zml4dHVyZS1wYXlsb2Fk","thresholds":{"burst":100,"steady":25},"window":[0.25,0.75]}`
	managedRuntimeHot      = `{"features":["emergency"],"payload":"Zml4dHVyZS1wYXlsb2Fk","thresholds":{"burst":200,"steady":25},"window":[0.5,0.5]}`
	managedRuntimeInvalid  = `{"features":["broken"],"thresholds":{"burst":999},"window":[0.1,0.9]}`
)

type managedPins struct {
	database uint64
	runtime  uint64
	password uint64
	token    uint64
}

type managedReporter struct {
	mu      sync.Mutex
	reports []configstore.DefaultMismatchReport
}

func (r *managedReporter) report(value configstore.DefaultMismatchReport) {
	r.mu.Lock()
	r.reports = append(r.reports, value)
	r.mu.Unlock()
}

func (r *managedReporter) snapshot() []configstore.DefaultMismatchReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]configstore.DefaultMismatchReport(nil), r.reports...)
}

// TestManagedConfigStoreOverRealKMS crosses the production storage, crypto,
// schema, TLS, gRPC, release-watch, SDK loader, generated decoder, and atomic
// publication paths. The paramstoretest suite covers the larger error matrix;
// this test proves the same policy against a temporary real KMS instance.
func TestManagedConfigStoreOverRealKMS(t *testing.T) {
	env := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	authCtx := networkAuthContext(ctx, env.adminToken)
	namespace := networkNS("prod", "managed-config")

	admin := kmsv1.NewAdminServiceClient(env.adminConn)
	if _, err := admin.CreateNamespace(authCtx, &kmsv1.CreateNamespaceRequest{
		Ref: namespace, AllowedAuthMethods: []string{string(domain.AuthMethodToken)},
	}); err != nil {
		t.Fatalf("create managed namespace: %v", err)
	}

	schemaJSON, err := os.ReadFile("../configstorefixture/runtime.schema.json")
	if err != nil {
		t.Fatalf("read generated schema: %v", err)
	}
	schemas := kmsv1.NewConfigurationSchemaServiceClient(env.adminConn)
	schemaResponse, err := schemas.CreateSchema(authCtx, &kmsv1.CreateSchemaRequest{
		Id: managedSchemaID, SchemaJson: string(schemaJSON), MetadataJson: `{"owner":"integration"}`,
	})
	if err != nil {
		t.Fatalf("register generated schema: %v", err)
	}
	schemaVersion := schemaResponse.GetSchema().GetVersion()

	parameters := kmsv1.NewParameterServiceClient(env.adminConn)
	putParameter := func(key, value string) uint64 {
		t.Helper()
		response, putErr := parameters.PutParameter(authCtx, &kmsv1.PutParameterRequest{
			Ref: networkRef("prod", "managed-config", key), Value: value, ContentType: "json",
		})
		if putErr != nil {
			t.Fatalf("put managed parameter %s: %v", key, putErr)
		}
		return response.GetVersion()
	}

	secrets := kmsv1.NewSecretServiceClient(env.adminConn)
	putSecret := func(key, plaintext string) (uint64, string) {
		t.Helper()
		response, putErr := secrets.PutSecret(authCtx, &kmsv1.PutSecretRequest{
			Ref: networkRef("prod", "managed-config", key), Value: []byte(plaintext),
			ContentType: "text/plain", GenerateAccessToken: true,
		})
		if putErr != nil {
			t.Fatalf("put managed secret %s: %v", key, putErr)
		}
		if response.GetAccessToken() == "" {
			t.Fatalf("managed secret %s returned no access token", key)
		}
		return response.GetVersion(), response.GetAccessToken()
	}

	pins := managedPins{
		database: putParameter("groups/database", managedDatabaseDefault),
		runtime:  putParameter("groups/runtime", managedRuntimeDefault),
	}
	passwordVersion, passwordToken := putSecret("secrets/database_password", "integration-password-canary")
	runtimeTokenVersion, runtimeToken := putSecret("secrets/runtime_token", "integration-runtime-token-canary")
	pins.password, pins.token = passwordVersion, runtimeTokenVersion

	releases := kmsv1.NewConfigurationReleaseServiceClient(env.adminConn)
	createRelease := func(candidate managedPins, wantValid bool) *kmsv1.ConfigurationRelease {
		t.Helper()
		response, createErr := releases.CreateRelease(authCtx, &kmsv1.CreateReleaseRequest{
			Namespace: namespace, Name: managedRelease, SchemaId: managedSchemaID, SchemaVersion: schemaVersion,
			Entries: []*kmsv1.ReleaseEntrySelector{
				{Alias: "database", Kind: "parameter", Ref: networkRef("prod", "managed-config", "groups/database"), Version: candidate.database},
				{Alias: "runtime", Kind: "parameter", Ref: networkRef("prod", "managed-config", "groups/runtime"), Version: candidate.runtime},
				{Alias: "database_password", Kind: "secret", Ref: networkRef("prod", "managed-config", "secrets/database_password"), Version: candidate.password},
				{Alias: "runtime_token", Kind: "secret", Ref: networkRef("prod", "managed-config", "secrets/runtime_token"), Version: candidate.token},
			},
		})
		if createErr != nil {
			t.Fatalf("create managed release: %v", createErr)
		}
		release := response.GetRelease()
		validation, validateErr := releases.ValidateRelease(authCtx, &kmsv1.ValidateReleaseRequest{
			Namespace: namespace, Name: managedRelease, Version: release.GetVersion(),
		})
		if validateErr != nil {
			t.Fatalf("validate managed release %d: %v", release.GetVersion(), validateErr)
		}
		if validation.GetValid() != wantValid {
			t.Fatalf("release %d validity = %t errors=%v, want %t", release.GetVersion(), validation.GetValid(), validation.GetErrors(), wantValid)
		}
		return release
	}
	activate := func(release *kmsv1.ConfigurationRelease, expected uint64) uint64 {
		t.Helper()
		response, activateErr := releases.ActivateRelease(authCtx, &kmsv1.ActivateReleaseRequest{
			Namespace: namespace, Name: managedRelease, Version: release.GetVersion(), ExpectedCurrentVersion: &expected,
		})
		if activateErr != nil {
			t.Fatalf("activate managed release %d: %v", release.GetVersion(), activateErr)
		}
		if !response.GetChanged() {
			t.Fatalf("release %d activation reported no change", release.GetVersion())
		}
		return response.GetCurrentVersion()
	}

	initialRelease := createRelease(pins, true)
	activate(initialRelease, 0)

	client, err := paramstore.NewClient(paramstore.Config{
		Endpoint: env.endpoint(), Namespace: managedNamespace, Token: env.adminToken,
		TLS: env.clientTLS(nil), Timeout: 3 * time.Second, ClientName: "managed-config-integration",
	})
	if err != nil {
		t.Fatalf("create managed SDK client: %v", err)
	}
	defer func() { _ = client.Close() }()
	secretTokens := map[string]string{"database_password": passwordToken, "runtime_token": runtimeToken}
	provider := func(alias, _ string) (string, bool) {
		token, ok := secretTokens[alias]
		return token, ok
	}

	storeCtx, stopStore := context.WithCancel(ctx)
	defer stopStore()
	reporter := &managedReporter{}
	store, err := fixturekms.Start(storeCtx, client, fixturekms.Options{
		Release: managedRelease, Defaults: fixtureconfig.Defaults, OnDefaultMismatch: reporter.report,
		SecretTokenProvider: provider, ReconcileInterval: 25 * time.Millisecond, InstanceID: "managed-primary",
	})
	if err != nil {
		t.Fatalf("start matching managed store: %v", err)
	}
	initial := store.Current()
	if initial.Release().Version() != initialRelease.GetVersion() {
		t.Fatalf("initial snapshot release = %d, want %d", initial.Release().Version(), initialRelease.GetVersion())
	}
	persistence, health := initial.PersistenceHandler(), initial.DatabaseHealth()
	if persistence.Timeout() != health.Timeout() || persistence.Endpoint().Host != health.Endpoint().Host {
		t.Fatal("views from one snapshot did not expose the same canonical fields")
	}
	if persistence.Password().StringValue() != "integration-password-canary" || initial.ApiHandler().RuntimeToken().StringValue() != "integration-runtime-token-canary" {
		t.Fatal("generated store did not inject exact pinned secrets")
	}

	hotPins := pins
	hotPins.runtime = putParameter("groups/runtime", managedRuntimeHot)
	hotRelease := createRelease(hotPins, true)

	var stopReaders atomic.Bool
	readerErr := make(chan string, 1)
	var readers sync.WaitGroup
	for range 12 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for !stopReaders.Load() {
				snapshot := store.Current()
				api, jobs := snapshot.ApiHandler(), snapshot.BackgroundJobs()
				features := api.Features()
				thresholds := jobs.Thresholds()
				window := jobs.Window()
				old := len(features) == 2 && features[0] == "search" && thresholds["burst"] == 100 && window == [2]float64{0.25, 0.75}
				hot := len(features) == 1 && features[0] == "emergency" && thresholds["burst"] == 200 && window == [2]float64{0.5, 0.5}
				if !old && !hot {
					select {
					case readerErr <- "reader observed a mixed managed generation":
					default:
					}
					return
				}
			}
		}()
	}
	activate(hotRelease, initialRelease.GetVersion())
	waitForManagedState(t, func() bool { return store.Current().Release().Version() == hotRelease.GetVersion() }, "hot release publication")
	stopReaders.Store(true)
	readers.Wait()
	select {
	case message := <-readerErr:
		t.Fatal(message)
	default:
	}
	if !store.Status().DefaultDivergent || store.Stats().Applied < 2 {
		t.Fatalf("hot override status=%+v stats=%+v", store.Status(), store.Stats())
	}
	reports := reporter.snapshot()
	if len(reports) != 1 || reports[0].Phase() != configstore.MismatchRuntime || reports[0].Severity() != configstore.MismatchError {
		t.Fatalf("runtime mismatch reports = %#v", reports)
	}

	// The same active divergent release must fail a fresh process by default.
	strictCtx, stopStrict := context.WithCancel(ctx)
	_, strictErr := fixturekms.Start(strictCtx, client, fixturekms.Options{
		Release: managedRelease, Defaults: fixtureconfig.Defaults, OnDefaultMismatch: func(configstore.DefaultMismatchReport) {},
		SecretTokenProvider: provider, ReconcileInterval: 25 * time.Millisecond, InstanceID: "managed-strict-restart",
	})
	stopStrict()
	var mismatch *configstore.DefaultMismatchError
	if !errors.As(strictErr, &mismatch) || mismatch.Phase() != configstore.MismatchStartup || mismatch.Severity() != configstore.MismatchFatal {
		t.Fatalf("strict divergent restart error = %T %v, want fatal *DefaultMismatchError", strictErr, strictErr)
	}

	bypassCtx, stopBypass := context.WithCancel(ctx)
	defer stopBypass()
	bypassReporter := &managedReporter{}
	bypassStore, err := fixturekms.Start(bypassCtx, client, fixturekms.Options{
		Release: managedRelease, Defaults: fixtureconfig.Defaults, AllowDefaultMismatch: true,
		OnDefaultMismatch: bypassReporter.report, SecretTokenProvider: provider,
		ReconcileInterval: 25 * time.Millisecond, InstanceID: "managed-bypass-restart",
	})
	if err != nil {
		t.Fatalf("start divergent store with bypass: %v", err)
	}
	bypassReports := bypassReporter.snapshot()
	if len(bypassReports) != 1 || bypassReports[0].Phase() != configstore.MismatchStartup || bypassReports[0].Severity() != configstore.MismatchError || !bypassStore.Status().DefaultDivergent {
		t.Fatalf("bypassed startup reports=%#v status=%+v", bypassReports, bypassStore.Status())
	}

	invalidPins := hotPins
	invalidPins.runtime = putParameter("groups/runtime", managedRuntimeInvalid)
	invalidRelease := createRelease(invalidPins, false)
	activate(invalidRelease, hotRelease.GetVersion())
	waitForManagedState(t, func() bool {
		status := store.Status()
		return status.Observed.Version() == invalidRelease.GetVersion() && status.LastRejectionCategory == configstore.RejectConfigDecodeFailed
	}, "invalid release rejection")
	if store.Current().Release().Version() != hotRelease.GetVersion() {
		t.Fatal("invalid release displaced last-known-good")
	}

	restartPins := hotPins
	restartPins.database = putParameter("groups/database", managedDatabaseRestart)
	restartRelease := createRelease(restartPins, true)
	activate(restartRelease, invalidRelease.GetVersion())
	waitForManagedState(t, func() bool {
		status := store.Status()
		return status.Observed.Version() == restartRelease.GetVersion() && status.LastRejectionCategory == configstore.RejectRestartRequired
	}, "restart-required release rejection")
	if store.Current().Release().Version() != hotRelease.GetVersion() {
		t.Fatal("restart-required release displaced last-known-good")
	}

	restoreRelease := createRelease(pins, true)
	activate(restoreRelease, restartRelease.GetVersion())
	waitForManagedState(t, func() bool {
		return store.Current().Release().Version() == restoreRelease.GetVersion() && !store.Status().DefaultDivergent
	}, "default restoration")
	waitForManagedState(t, func() bool {
		return bypassStore.Current().Release().Version() == restoreRelease.GetVersion() && !bypassStore.Status().DefaultDivergent
	}, "bypassed store restoration")

	stopBypass()
	if err := bypassStore.Wait(); err != nil {
		t.Fatalf("bypassed store Wait after cancellation: %v", err)
	}
	stopStore()
	if err := store.Wait(); err != nil {
		t.Fatalf("primary store Wait after cancellation: %v", err)
	}
}

func waitForManagedState(t *testing.T, predicate func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}
