package configkms

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	fixtureconfig "github.com/Suhaibinator/kms/internal/configstorefixture/config"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/paramstore"
	"github.com/Suhaibinator/kms/sdk/go/paramstore/paramstoretest"
)

const (
	fixtureNamespace     = "prod/managed-fixture"
	fixtureReleaseName   = "runtime"
	databasePath         = "groups/database"
	runtimePath          = "groups/runtime"
	passwordPath         = "secrets/database-password"
	runtimeTokenPath     = "secrets/runtime-token"
	passwordPlaintext    = "database-password-value"
	defaultTokenPrefix   = "runtime-token-v"
	testOperationTimeout = 5 * time.Second
)

type releaseData struct {
	releaseVersion      uint64
	activationRevision  uint64
	databaseVersion     uint64
	runtimeVersion      uint64
	passwordVersion     uint64
	runtimeTokenVersion uint64
	databaseDocument    string
	runtimeDocument     string
	passwordValue       []byte
	runtimeTokenValue   []byte
	passwordPath        string
	runtimeTokenPath    string
	databaseContentType string
	runtimeContentType  string
	omitAlias           string
}

type runningFixture struct {
	server *paramstoretest.Server
	client *paramstore.Client
	store  *Store
	sub    *paramstoretest.ReleaseSubscription
	cancel context.CancelFunc
}

func matchingRelease(version, revision uint64) releaseData {
	return releaseData{
		releaseVersion:      version,
		activationRevision:  revision,
		databaseVersion:     version*10 + 1,
		runtimeVersion:      version*10 + 2,
		passwordVersion:     1,
		runtimeTokenVersion: version,
		databaseDocument:    databaseDocument("db.internal", "3s", 20),
		runtimeDocument:     runtimeDocument([]string{"search", "reports"}, []byte("fixture-payload"), map[string]uint64{"burst": 100, "steady": 25}, [2]float64{0.25, 0.75}),
		passwordValue:       []byte(passwordPlaintext),
		runtimeTokenValue:   []byte(fmt.Sprintf("%s%d", defaultTokenPrefix, version)),
		passwordPath:        passwordPath,
		runtimeTokenPath:    runtimeTokenPath,
		databaseContentType: "json",
		runtimeContentType:  "json",
	}
}

func databaseDocument(host, timeout string, maxOpen any) string {
	return mustJSON(map[string]any{
		"endpoint": map[string]any{
			"host":  host,
			"ports": []uint16{5432, 5433},
			"labels": map[string][]string{
				"role": {"primary", "readonly"},
			},
			"zones": []string{"us-west-1a", "us-west-1b"},
		},
		"max_open": maxOpen,
		"timeout":  timeout,
	})
}

func runtimeDocument(features []string, payload []byte, thresholds map[string]uint64, window [2]float64) string {
	return mustJSON(map[string]any{
		"features":   features,
		"payload":    base64.StdEncoding.EncodeToString(payload),
		"thresholds": thresholds,
		"window":     window,
	})
}

func mustJSON(value any) string {
	document, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(document)
}

func scriptResources(server *paramstoretest.Server, data releaseData) {
	server.SetParameterVersion(fixtureNamespace, databasePath, data.databaseDocument, "json", data.databaseVersion)
	server.SetParameterVersion(fixtureNamespace, runtimePath, data.runtimeDocument, "json", data.runtimeVersion)
	server.SetSecretVersion(fixtureNamespace, data.passwordPath, data.passwordValue, "text/plain", data.passwordVersion)
	server.SetSecretVersion(fixtureNamespace, data.runtimeTokenPath, data.runtimeTokenValue, "text/plain", data.runtimeTokenVersion)
}

func releaseSpec(data releaseData) paramstoretest.ReleaseSpec {
	entries := []paramstoretest.ReleaseEntrySpec{
		{Alias: "database", Kind: "parameter", Path: databasePath, Version: data.databaseVersion, ContentType: data.databaseContentType},
		{Alias: "runtime", Kind: "parameter", Path: runtimePath, Version: data.runtimeVersion, ContentType: data.runtimeContentType},
		{Alias: "database_password", Kind: "secret", Path: data.passwordPath, Version: data.passwordVersion},
		{Alias: "runtime_token", Kind: "secret", Path: data.runtimeTokenPath, Version: data.runtimeTokenVersion},
	}
	if data.omitAlias != "" {
		filtered := entries[:0]
		for _, entry := range entries {
			if entry.Alias != data.omitAlias {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
	}
	return paramstoretest.ReleaseSpec{
		Namespace:     fixtureNamespace,
		Name:          fixtureReleaseName,
		Version:       data.releaseVersion,
		SchemaID:      "managed-fixture",
		SchemaVersion: 1,
		Entries:       entries,
	}
}

func installInitial(t *testing.T, server *paramstoretest.Server, data releaseData) {
	t.Helper()
	scriptResources(server, data)
	if _, err := server.SetActiveRelease(releaseSpec(data), data.activationRevision); err != nil {
		t.Fatal(err)
	}
}

func activate(t *testing.T, fixture *runningFixture, data releaseData) {
	t.Helper()
	scriptResources(fixture.server, data)
	if _, err := fixture.server.ActivateConfigurationRelease(releaseSpec(data), data.activationRevision); err != nil {
		t.Fatal(err)
	}
}

func newFixtureClient(t *testing.T, server *paramstoretest.Server) *paramstore.Client {
	t.Helper()
	client, err := paramstore.NewClient(paramstore.Config{
		Namespace:   fixtureNamespace,
		ClientName:  "managed-fixture-test",
		DialOptions: server.DialOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func startFixture(
	t *testing.T,
	initial releaseData,
	defaults func() *fixtureconfig.Config,
	allowDefaultMismatch bool,
	reporter func(configstore.DefaultMismatchReport),
) *runningFixture {
	t.Helper()
	server, err := paramstoretest.New()
	if err != nil {
		t.Fatal(err)
	}
	installInitial(t, server, initial)
	client := newFixtureClient(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	if reporter == nil {
		reporter = func(configstore.DefaultMismatchReport) {}
	}
	store, err := Start(ctx, client, Options{
		Release:              fixtureReleaseName,
		Defaults:             defaults,
		AllowDefaultMismatch: allowDefaultMismatch,
		OnDefaultMismatch:    reporter,
		ReconcileInterval:    time.Hour,
		InstanceID:           "managed-fixture-instance",
	})
	if err != nil {
		cancel()
		_ = client.Close()
		server.Close()
		t.Fatal(err)
	}
	sub, err := server.WaitForReleaseSubscribe(testOperationTimeout)
	if err != nil {
		cancel()
		_ = store.Wait()
		_ = client.Close()
		server.Close()
		t.Fatal(err)
	}
	fixture := &runningFixture{server: server, client: client, store: store, sub: sub, cancel: cancel}
	waitAcknowledgement(t, sub, initial.releaseVersion, paramstore.ReleaseStateApplied)
	t.Cleanup(func() {
		cancel()
		if err := store.Wait(); err != nil {
			t.Errorf("managed store shutdown: %v", err)
		}
		if err := client.Close(); err != nil {
			t.Errorf("client close: %v", err)
		}
		server.Close()
	})
	return fixture
}

func waitAcknowledgement(t *testing.T, sub *paramstoretest.ReleaseSubscription, version uint64, state string) *kmsv1.ReleaseAcknowledgement {
	t.Helper()
	deadline := time.Now().Add(testOperationTimeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timed out waiting for release %d state %s", version, state)
		}
		ack, err := sub.WaitAcknowledgement(remaining)
		if err != nil {
			t.Fatalf("wait for release %d state %s: %v", version, state, err)
		}
		if ack.GetVersion() == version && ack.GetState() == state {
			return ack
		}
	}
}

func waitAppliedVersion(t *testing.T, store *Store, version uint64) Snapshot {
	t.Helper()
	deadline := time.Now().Add(testOperationTimeout)
	for time.Now().Before(deadline) {
		snapshot := store.Current()
		if snapshot.Release().Version() == version {
			return snapshot
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for applied release %d; status=%+v", version, store.Status())
	return Snapshot{}
}

func waitRejectedCount(t *testing.T, store *Store, category configstore.RejectionCategory, want uint64) {
	t.Helper()
	deadline := time.Now().Add(testOperationTimeout)
	for time.Now().Before(deadline) {
		if store.Stats().Rejected[category] >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s rejection count %d; stats=%+v", category, want, store.Stats())
}

func TestGeneratedStoreStartupViewsAndDefensiveGetters(t *testing.T) {
	initial := matchingRelease(1, 101)
	sourceDefaults := fixtureconfig.Defaults()
	fixture := startFixture(t, initial, func() *fixtureconfig.Config { return sourceDefaults }, false, nil)

	// The generated store must own defaults, including excluded composites.
	sourceDefaults.Local["owners"][0] = "mutated-by-caller"
	if got := fixture.store.defaults.Local["owners"][0]; got != "platform" {
		t.Fatalf("excluded defaults alias caller memory: %q", got)
	}

	snapshot := fixture.store.Current()
	if got := snapshot.Release().Version(); got != 1 {
		t.Fatalf("release version = %d, want 1", got)
	}
	if got := snapshot.Release().ActivationRevision(); got != 101 {
		t.Fatalf("activation revision = %d, want 101", got)
	}

	persistence := snapshot.PersistenceHandler()
	health := snapshot.DatabaseHealth()
	api := snapshot.ApiHandler()
	jobs := snapshot.BackgroundJobs()
	if persistence.Timeout() != 3*time.Second || health.Timeout() != persistence.Timeout() {
		t.Fatalf("shared timeout differs: persistence=%s health=%s", persistence.Timeout(), health.Timeout())
	}
	if persistence.MaxOpen() != 20 || health.MaxOpen() != 20 {
		t.Fatalf("pointer scalar getter mismatch: persistence=%d health=%d", persistence.MaxOpen(), health.MaxOpen())
	}
	if !reflect.DeepEqual(api.Features(), jobs.Features()) || !reflect.DeepEqual(api.Thresholds(), jobs.Thresholds()) {
		t.Fatal("shared runtime fields differ between views")
	}
	if got := persistence.Password(); got.StringValue() != passwordPlaintext || got.Version() != 1 || got.Path() != "/prod/managed-fixture/"+passwordPath {
		t.Fatalf("unexpected injected password metadata: value=%s version=%d path=%q", got, got.Version(), got.Path())
	}
	if got := api.RuntimeToken(); got.StringValue() != defaultTokenPrefix+"1" || got.Version() != 1 {
		t.Fatalf("unexpected injected runtime token: value=%s version=%d", got, got.Version())
	}

	endpoint := persistence.Endpoint()
	endpoint.Ports[0] = 1
	endpoint.Labels["role"][0] = "mutated"
	if current := health.Endpoint(); current.Ports[0] != 5432 || current.Labels["role"][0] != "primary" {
		t.Fatalf("endpoint getter exposed active memory: %#v", current)
	}
	features := api.Features()
	features[0] = "mutated"
	payload := api.Payload()
	payload[0] = 'X'
	thresholds := api.Thresholds()
	thresholds["burst"] = 1
	secret := api.RuntimeToken()
	secret.Value()[0] = 'X'
	if got := fixture.store.Current().ApiHandler(); got.Features()[0] != "search" || string(got.Payload()) != "fixture-payload" || got.Thresholds()["burst"] != 100 || got.RuntimeToken().StringValue() != defaultTokenPrefix+"1" {
		t.Fatal("a defensive getter mutation changed the active generation")
	}
}

func TestStartupDefaultMismatchFatalAndBypass(t *testing.T) {
	t.Run("fatal typed error and immutable report", func(t *testing.T) {
		initial := matchingRelease(1, 101)
		const needle = "nonsecret-sensitive-looking-value"
		initial.runtimeDocument = runtimeDocument([]string{needle}, []byte("fixture-payload"), map[string]uint64{"burst": 100, "steady": 25}, [2]float64{0.25, 0.75})

		server, err := paramstoretest.New()
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		installInitial(t, server, initial)
		client := newFixtureClient(t, server)
		defer client.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		reports := make(chan configstore.DefaultMismatchReport, 1)
		store, startErr := Start(ctx, client, Options{
			Release:           fixtureReleaseName,
			Defaults:          fixtureconfig.Defaults,
			OnDefaultMismatch: func(report configstore.DefaultMismatchReport) { reports <- report },
			ReconcileInterval: time.Hour,
		})
		if store != nil || startErr == nil {
			t.Fatalf("Start = (%v, %v), want nil store and mismatch error", store, startErr)
		}
		var mismatch *configstore.DefaultMismatchError
		if !errors.As(startErr, &mismatch) {
			t.Fatalf("Start error type = %T (%v), want *configstore.DefaultMismatchError", startErr, startErr)
		}
		if mismatch.Phase() != configstore.MismatchStartup || mismatch.Severity() != configstore.MismatchFatal {
			t.Fatalf("unexpected mismatch classification: phase=%s severity=%s", mismatch.Phase(), mismatch.Severity())
		}
		if mismatch.Release().Version() != 1 || mismatch.Release().ActivationRevision() != 101 {
			t.Fatalf("unexpected mismatch release: %s", mismatch.Release())
		}
		report := <-reports
		if report != mismatch.Report() {
			t.Fatal("callback and returned error did not share the same immutable report")
		}
		fields := report.Fields()
		if len(fields) != 1 || fields[0].Path != "runtime.features" {
			t.Fatalf("unexpected mismatch fields: %#v", fields)
		}
		actual, ok := fields[0].Actual.([]string)
		if !ok || len(actual) != 1 || actual[0] != needle {
			t.Fatalf("unexpected explicit actual value: %#v", fields[0].Actual)
		}
		actual[0] = "mutated"
		if got := report.Fields()[0].Actual.([]string)[0]; got != needle {
			t.Fatalf("report Fields did not return a defensive copy: %q", got)
		}
		for _, rendered := range []string{fmt.Sprint(report), fmt.Sprintf("%+v", report), fmt.Sprintf("%#v", report), fmt.Sprint(startErr), fmt.Sprintf("%+v", startErr), fmt.Sprintf("%#v", startErr)} {
			if strings.Contains(rendered, needle) {
				t.Fatalf("ordinary formatting exposed mismatch value: %q", rendered)
			}
		}
	})

	t.Run("explicit bypass publishes divergent startup", func(t *testing.T) {
		initial := matchingRelease(1, 101)
		initial.runtimeDocument = runtimeDocument([]string{"kms-override"}, []byte("fixture-payload"), map[string]uint64{"burst": 100, "steady": 25}, [2]float64{0.25, 0.75})
		reports := make(chan configstore.DefaultMismatchReport, 2)
		fixture := startFixture(t, initial, fixtureconfig.Defaults, true, func(report configstore.DefaultMismatchReport) { reports <- report })
		if got := fixture.store.Current().ApiHandler().Features(); !reflect.DeepEqual(got, []string{"kms-override"}) {
			t.Fatalf("bypassed startup did not publish KMS values: %#v", got)
		}
		report := <-reports
		if report.Phase() != configstore.MismatchStartup || report.Severity() != configstore.MismatchError {
			t.Fatalf("unexpected bypass report: %s/%s", report.Phase(), report.Severity())
		}
		if !fixture.store.Status().DefaultDivergent || !fixture.store.Stats().DefaultDivergent {
			t.Fatal("bypassed startup did not expose default divergence")
		}

		restored := matchingRelease(2, 102)
		activate(t, fixture, restored)
		waitAcknowledgement(t, fixture.sub, 2, paramstore.ReleaseStateApplied)
		waitAppliedVersion(t, fixture.store, 2)
		if fixture.store.Status().DefaultDivergent || fixture.store.Stats().DefaultDivergent {
			t.Fatal("restoring defaults did not clear divergence")
		}
		select {
		case extra := <-reports:
			t.Fatalf("restoration emitted an unexpected mismatch report: %v", extra)
		default:
		}
	})
}

func TestHotOverrideAndRestorationPublishWholeGenerations(t *testing.T) {
	reports := make(chan configstore.DefaultMismatchReport, 4)
	fixture := startFixture(t, matchingRelease(1, 101), fixtureconfig.Defaults, false, func(report configstore.DefaultMismatchReport) { reports <- report })
	oldSnapshot := fixture.store.Current()

	hot := matchingRelease(2, 102)
	hot.databaseDocument = databaseDocument("db.internal", "750ms", 20)
	hot.runtimeDocument = runtimeDocument([]string{"search-v2", "reports-v2"}, []byte("payload-v2"), map[string]uint64{"burst": 200, "steady": 50}, [2]float64{0.5, 1.5})
	hot.runtimeTokenValue = []byte("hot-token-v2")
	activate(t, fixture, hot)
	waitAcknowledgement(t, fixture.sub, 2, paramstore.ReleaseStateApplied)
	current := waitAppliedVersion(t, fixture.store, 2)

	if oldSnapshot.Release().Version() != 1 || oldSnapshot.DatabaseHealth().Timeout() != 3*time.Second || oldSnapshot.ApiHandler().Features()[0] != "search" || oldSnapshot.ApiHandler().RuntimeToken().StringValue() != defaultTokenPrefix+"1" {
		t.Fatal("previously captured snapshot changed after hot publication")
	}
	if current.DatabaseHealth().Timeout() != 750*time.Millisecond || current.PersistenceHandler().Timeout() != 750*time.Millisecond {
		t.Fatal("hot database field was not shared by both views")
	}
	if !reflect.DeepEqual(current.ApiHandler().Features(), []string{"search-v2", "reports-v2"}) || !reflect.DeepEqual(current.BackgroundJobs().Features(), []string{"search-v2", "reports-v2"}) {
		t.Fatal("hot runtime fields were not published as one generation")
	}
	if got := current.ApiHandler().RuntimeToken(); got.StringValue() != "hot-token-v2" || got.Version() != 2 {
		t.Fatalf("hot secret did not publish: value=%s version=%d", got, got.Version())
	}
	if got := current.PersistenceHandler().Password(); got.Version() != 1 || got.StringValue() != passwordPlaintext {
		t.Fatalf("restart secret changed during hot publication: value=%s version=%d", got, got.Version())
	}
	report := <-reports
	if report.Phase() != configstore.MismatchRuntime || report.Severity() != configstore.MismatchError {
		t.Fatalf("unexpected runtime drift classification: %s/%s", report.Phase(), report.Severity())
	}
	paths := differencePaths(report)
	wantPaths := []string{"database.timeout", "runtime.features", "runtime.payload", "runtime.thresholds", "runtime.window"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("drift paths = %#v, want %#v", paths, wantPaths)
	}
	if fixture.store.Status().DefaultDivergent != true {
		t.Fatal("runtime override did not expose divergence")
	}

	restored := matchingRelease(3, 103)
	activate(t, fixture, restored)
	waitAcknowledgement(t, fixture.sub, 3, paramstore.ReleaseStateApplied)
	restoredSnapshot := waitAppliedVersion(t, fixture.store, 3)
	if restoredSnapshot.DatabaseHealth().Timeout() != 3*time.Second || restoredSnapshot.ApiHandler().Features()[0] != "search" {
		t.Fatal("default restoration did not publish matching values")
	}
	if fixture.store.Status().DefaultDivergent || fixture.store.Stats().DefaultDivergent {
		t.Fatal("default restoration did not clear divergence")
	}
	select {
	case extra := <-reports:
		t.Fatalf("matching restoration emitted a drift report: %v", extra)
	default:
	}
}

func differencePaths(report configstore.DefaultMismatchReport) []string {
	fields := report.Fields()
	paths := make([]string, len(fields))
	for i := range fields {
		paths[i] = fields[i].Path
	}
	sort.Strings(paths)
	return paths
}

func TestRestartAndMixedCandidatesPreserveLastKnownGood(t *testing.T) {
	fixture := startFixture(t, matchingRelease(1, 101), fixtureconfig.Defaults, false, nil)

	restartParameter := matchingRelease(2, 102)
	restartParameter.databaseDocument = databaseDocument("db-restart.internal", "3s", 20)
	activate(t, fixture, restartParameter)
	ack := waitAcknowledgement(t, fixture.sub, 2, paramstore.ReleaseStateRejected)
	if ack.GetRejectionCategory() != string(configstore.RejectRestartRequired) || ack.GetDiagnostic() != "" {
		t.Fatalf("restart rejection ack = category %q diagnostic %q", ack.GetRejectionCategory(), ack.GetDiagnostic())
	}
	waitRejectedCount(t, fixture.store, configstore.RejectRestartRequired, 1)
	if got := fixture.store.Current(); got.Release().Version() != 1 || got.PersistenceHandler().Endpoint().Host != "db.internal" {
		t.Fatal("restart-required parameter displaced last-known-good")
	}

	mixed := matchingRelease(3, 103)
	mixed.runtimeDocument = runtimeDocument([]string{"must-not-leak"}, []byte("must-not-leak"), map[string]uint64{"burst": 999}, [2]float64{9, 9})
	mixed.passwordVersion = 2
	mixed.passwordValue = []byte(passwordPlaintext) // Same plaintext, different immutable pin.
	activate(t, fixture, mixed)
	ack = waitAcknowledgement(t, fixture.sub, 3, paramstore.ReleaseStateRejected)
	if ack.GetRejectionCategory() != string(configstore.RejectRestartRequired) || ack.GetDiagnostic() != "" {
		t.Fatalf("mixed rejection ack = category %q diagnostic %q", ack.GetRejectionCategory(), ack.GetDiagnostic())
	}
	waitRejectedCount(t, fixture.store, configstore.RejectRestartRequired, 2)
	if got := fixture.store.Current(); got.Release().Version() != 1 || got.ApiHandler().Features()[0] != "search" || string(got.ApiHandler().Payload()) != "fixture-payload" || got.PersistenceHandler().Password().Version() != 1 {
		t.Fatal("mixed restart/hot candidate partially changed last-known-good")
	}

	// Re-pin the restart secret to the applied path/version while changing only
	// hot values. Exact-version resolution must recover version 1 even though a
	// newer secret version is current in the fake.
	hot := matchingRelease(4, 104)
	hot.runtimeDocument = runtimeDocument([]string{"accepted-hot"}, []byte("accepted-hot"), map[string]uint64{"burst": 400}, [2]float64{4, 4})
	hot.runtimeTokenValue = []byte("accepted-hot-token")
	activate(t, fixture, hot)
	waitAcknowledgement(t, fixture.sub, 4, paramstore.ReleaseStateApplied)
	current := waitAppliedVersion(t, fixture.store, 4)
	if current.PersistenceHandler().Password().Version() != 1 || current.PersistenceHandler().Password().StringValue() != passwordPlaintext {
		t.Fatal("exact restart-secret pin was not preserved")
	}
	if current.ApiHandler().Features()[0] != "accepted-hot" || current.ApiHandler().RuntimeToken().StringValue() != "accepted-hot-token" {
		t.Fatal("accepted hot candidate was not fully published")
	}
}

func TestStrictDecodeContractAndValidationRejections(t *testing.T) {
	fixture := startFixture(t, matchingRelease(1, 101), fixtureconfig.Defaults, false, nil)

	var fetches atomic.Int64
	fixture.server.SetGetParameterHook(func(string) { fetches.Add(1) })
	badContract := matchingRelease(2, 102)
	badContract.databaseContentType = "text/plain"
	activate(t, fixture, badContract)
	ack := waitAcknowledgement(t, fixture.sub, 2, paramstore.ReleaseStateRejected)
	if ack.GetRejectionCategory() != string(configstore.RejectConfigContractMismatch) || ack.GetDiagnostic() != "" {
		t.Fatalf("contract rejection ack = category %q diagnostic %q", ack.GetRejectionCategory(), ack.GetDiagnostic())
	}
	waitRejectedCount(t, fixture.store, configstore.RejectConfigContractMismatch, 1)
	fixture.server.SetGetParameterHook(nil)
	if got := fetches.Load(); got != 0 {
		t.Fatalf("contract mismatch fetched %d parameter resources before rejection", got)
	}

	defaultDB := matchingRelease(1, 101).databaseDocument
	defaultRuntime := matchingRelease(1, 101).runtimeDocument
	decodeCases := []struct {
		name     string
		database string
		runtime  string
	}{
		{name: "malformed", database: `{`, runtime: defaultRuntime},
		{name: "missing", database: `{"max_open":20,"timeout":"3s"}`, runtime: defaultRuntime},
		{name: "unknown", database: strings.TrimSuffix(defaultDB, "}") + `,"unknown":true}`, runtime: defaultRuntime},
		{name: "duplicate", database: strings.TrimSuffix(defaultDB, "}") + `,"timeout":"4s"}`, runtime: defaultRuntime},
		{name: "wrong type", database: databaseDocument("db.internal", "3s", 20), runtime: `{"features":["search"],"payload":7,"thresholds":{"burst":100},"window":[0.25,0.75]}`},
		{name: "invalid base64", database: defaultDB, runtime: `{"features":["search"],"payload":"***","thresholds":{"burst":100},"window":[0.25,0.75]}`},
	}
	for i, test := range decodeCases {
		t.Run(test.name, func(t *testing.T) {
			version := uint64(i + 3)
			candidate := matchingRelease(version, 100+version)
			candidate.databaseDocument = test.database
			candidate.runtimeDocument = test.runtime
			activate(t, fixture, candidate)
			ack := waitAcknowledgement(t, fixture.sub, version, paramstore.ReleaseStateRejected)
			if ack.GetRejectionCategory() != string(configstore.RejectConfigDecodeFailed) || ack.GetDiagnostic() != "" {
				t.Fatalf("decode rejection ack = category %q diagnostic %q", ack.GetRejectionCategory(), ack.GetDiagnostic())
			}
			waitRejectedCount(t, fixture.store, configstore.RejectConfigDecodeFailed, uint64(i+1))
			if got := fixture.store.Current().Release().Version(); got != 1 {
				t.Fatalf("invalid candidate displaced release 1 with %d", got)
			}
		})
	}

	validationVersion := uint64(len(decodeCases) + 3)
	invalid := matchingRelease(validationVersion, 100+validationVersion)
	invalid.databaseDocument = databaseDocument("db.internal", "0s", 20)
	activate(t, fixture, invalid)
	ack = waitAcknowledgement(t, fixture.sub, validationVersion, paramstore.ReleaseStateRejected)
	if ack.GetRejectionCategory() != string(configstore.RejectConfigValidationFailed) || ack.GetDiagnostic() != "" {
		t.Fatalf("validation rejection ack = category %q diagnostic %q", ack.GetRejectionCategory(), ack.GetDiagnostic())
	}
	waitRejectedCount(t, fixture.store, configstore.RejectConfigValidationFailed, 1)
	if got := fixture.store.Current().Release().Version(); got != 1 {
		t.Fatalf("validation failure displaced release 1 with %d", got)
	}
}

func TestConcurrentReadersSeeOnlyCompleteGenerations(t *testing.T) {
	fixture := startFixture(t, matchingRelease(1, 101), fixtureconfig.Defaults, false, nil)
	const (
		readerCount = 12
		lastVersion = 25
	)
	stop := make(chan struct{})
	errorsSeen := make(chan string, 1)
	var readers sync.WaitGroup
	recordFailure := func(format string, args ...any) {
		select {
		case errorsSeen <- fmt.Sprintf(format, args...):
		default:
		}
	}
	for range readerCount {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snapshot := fixture.store.Current()
				version := snapshot.Release().Version()
				api := snapshot.ApiHandler()
				jobs := snapshot.BackgroundJobs()
				health := snapshot.DatabaseHealth()
				if version == 1 {
					if api.Features()[0] != "search" || jobs.Features()[0] != "search" || health.Timeout() != 3*time.Second || api.RuntimeToken().StringValue() != defaultTokenPrefix+"1" {
						recordFailure("release 1 exposed a mixed generation")
						return
					}
					continue
				}
				wantFeature := fmt.Sprintf("generation-%d", version)
				wantPayload := fmt.Sprintf("payload-%d", version)
				wantToken := fmt.Sprintf("token-%d", version)
				if got := api.Features(); len(got) != 1 || got[0] != wantFeature {
					recordFailure("release %d api features = %#v", version, got)
					return
				}
				if got := jobs.Features(); len(got) != 1 || got[0] != wantFeature {
					recordFailure("release %d jobs features = %#v", version, got)
					return
				}
				if got := string(api.Payload()); got != wantPayload {
					recordFailure("release %d payload = %q", version, got)
					return
				}
				if got := api.Thresholds()["generation"]; got != version {
					recordFailure("release %d threshold = %d", version, got)
					return
				}
				if got := api.RuntimeToken().StringValue(); got != wantToken {
					recordFailure("release %d token mismatch", version)
					return
				}
				if got := health.Timeout(); got != time.Duration(version)*time.Millisecond {
					recordFailure("release %d timeout = %s", version, got)
					return
				}
			}
		}()
	}

	for version := uint64(2); version <= lastVersion; version++ {
		candidate := matchingRelease(version, 100+version)
		candidate.databaseDocument = databaseDocument("db.internal", fmt.Sprintf("%dms", version), 20)
		candidate.runtimeDocument = runtimeDocument(
			[]string{fmt.Sprintf("generation-%d", version)},
			[]byte(fmt.Sprintf("payload-%d", version)),
			map[string]uint64{"generation": version},
			[2]float64{float64(version), float64(version)},
		)
		candidate.runtimeTokenValue = []byte(fmt.Sprintf("token-%d", version))
		activate(t, fixture, candidate)
		waitAcknowledgement(t, fixture.sub, version, paramstore.ReleaseStateApplied)
		waitAppliedVersion(t, fixture.store, version)
	}
	close(stop)
	readers.Wait()
	select {
	case failure := <-errorsSeen:
		t.Fatal(failure)
	default:
	}
	if got := fixture.store.Current().Release().Version(); got != lastVersion {
		t.Fatalf("final release = %d, want %d", got, lastVersion)
	}
}
