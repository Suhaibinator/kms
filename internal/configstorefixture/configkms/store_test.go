package configkms

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
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
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient/kmsclienttest"
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
	server *kmsclienttest.Server
	client *kmsclient.Client
	store  *Store
	sub    *kmsclienttest.ReleaseSubscription
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

func scriptResources(server *kmsclienttest.Server, data releaseData) {
	server.SetParameterVersion(fixtureNamespace, databasePath, data.databaseDocument, "json", data.databaseVersion)
	server.SetParameterVersion(fixtureNamespace, runtimePath, data.runtimeDocument, "json", data.runtimeVersion)
	server.SetSecretVersion(fixtureNamespace, data.passwordPath, data.passwordValue, "text/plain", data.passwordVersion)
	server.SetSecretVersion(fixtureNamespace, data.runtimeTokenPath, data.runtimeTokenValue, "text/plain", data.runtimeTokenVersion)
}

func releaseSpec(data releaseData) kmsclienttest.ReleaseSpec {
	entries := []kmsclienttest.ReleaseEntrySpec{
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
	return kmsclienttest.ReleaseSpec{
		Namespace:     fixtureNamespace,
		Name:          fixtureReleaseName,
		Version:       data.releaseVersion,
		SchemaID:      "managed-fixture",
		SchemaVersion: 1,
		Entries:       entries,
	}
}

func installInitial(t *testing.T, server *kmsclienttest.Server, data releaseData) {
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

func newFixtureClient(t *testing.T, server *kmsclienttest.Server) *kmsclient.Client {
	t.Helper()
	client, err := kmsclient.NewClient(kmsclient.Config{
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
	reporter func(configstore.DefaultMismatchReport),
) *runningFixture {
	t.Helper()
	return startFixtureWithCallbacks(t, initial, defaults, configstore.Callbacks{OnDefaultMismatch: reporter})
}

// startFixtureWithCallbacks enters through the generated public API. A
// divergent initial release is applied and reported, never refused, so the
// helper only fails on transport, contract, decode, or validation errors.
func startFixtureWithCallbacks(
	t *testing.T,
	initial releaseData,
	defaults func() *fixtureconfig.Config,
	callbacks configstore.Callbacks,
) *runningFixture {
	t.Helper()
	server, err := kmsclienttest.New()
	if err != nil {
		t.Fatal(err)
	}
	installInitial(t, server, initial)
	client := newFixtureClient(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	if callbacks.OnDefaultMismatch == nil {
		callbacks.OnDefaultMismatch = func(configstore.DefaultMismatchReport) {}
	}
	store, err := Start(ctx, client, Options{
		Release:           fixtureReleaseName,
		Defaults:          defaults,
		Callbacks:         callbacks,
		ReconcileInterval: time.Hour,
		InstanceID:        "managed-fixture-instance",
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
	waitAcknowledgement(t, sub, initial.releaseVersion, kmsclient.ReleaseStateApplied)
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

func waitAcknowledgement(t *testing.T, sub *kmsclienttest.ReleaseSubscription, version uint64, state string) *kmsv1.ReleaseAcknowledgement {
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
	fixture := startFixture(t, initial, func() *fixtureconfig.Config { return sourceDefaults }, nil)

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

func TestStartupDefaultMismatchIsAppliedAndReported(t *testing.T) {
	initial := matchingRelease(1, 101)
	const needle = "nonsecret-sensitive-looking-value"
	initial.runtimeDocument = runtimeDocument([]string{needle}, []byte("fixture-payload"), map[string]uint64{"burst": 100, "steady": 25}, [2]float64{0.25, 0.75})

	mismatches := make(chan configstore.DefaultMismatchReport, 4)
	applied := make(chan configstore.AppliedReport, 4)
	fixture := startFixtureWithCallbacks(t, initial, fixtureconfig.Defaults, configstore.Callbacks{
		OnDefaultMismatch: func(report configstore.DefaultMismatchReport) { mismatches <- report },
		OnApplied:         func(report configstore.AppliedReport) { applied <- report },
	})

	// A divergent active release is published as-is: a process must be able to
	// restart onto whatever is active, and the report is the reconciliation
	// signal.
	if got := fixture.store.Current().ApiHandler().Features(); !reflect.DeepEqual(got, []string{needle}) {
		t.Fatalf("divergent startup did not publish KMS values: %#v", got)
	}
	if !fixture.store.Status().DefaultDivergent || !fixture.store.Stats().DefaultDivergent {
		t.Fatal("divergent startup did not expose default divergence")
	}

	var report configstore.DefaultMismatchReport
	select {
	case report = <-mismatches:
	default:
		t.Fatal("divergent startup emitted no mismatch report before Start returned")
	}
	select {
	case extra := <-mismatches:
		t.Fatalf("divergent startup emitted more than one mismatch report: %v", extra)
	default:
	}
	if report.Phase() != configstore.PhaseStartup || report.Severity() != configstore.MismatchError {
		t.Fatalf("unexpected mismatch classification: phase=%s severity=%s", report.Phase(), report.Severity())
	}
	if report.Release().Version() != 1 || report.Release().ActivationRevision() != 101 {
		t.Fatalf("unexpected mismatch release: %s", report.Release())
	}
	fields := report.Fields()
	if len(fields) != 1 || fields[0].Path != "runtime.features" {
		t.Fatalf("unexpected mismatch fields: %#v", fields)
	}
	if expected, ok := fields[0].Expected.([]string); !ok || !reflect.DeepEqual(expected, []string{"search", "reports"}) {
		t.Fatalf("unexpected explicit expected value: %#v", fields[0].Expected)
	}
	actual, ok := fields[0].Actual.([]string)
	if !ok || len(actual) != 1 || actual[0] != needle {
		t.Fatalf("unexpected explicit actual value: %#v", fields[0].Actual)
	}
	actual[0] = "mutated"
	if got := report.Fields()[0].Actual.([]string)[0]; got != needle {
		t.Fatalf("report Fields did not return a defensive copy: %q", got)
	}
	for _, rendered := range []string{fmt.Sprint(report), fmt.Sprintf("%+v", report), fmt.Sprintf("%#v", report), fmt.Sprintf("%q", report)} {
		if strings.Contains(rendered, needle) {
			t.Fatalf("ordinary formatting exposed mismatch value: %q", rendered)
		}
	}

	var startup configstore.AppliedReport
	select {
	case startup = <-applied:
	default:
		t.Fatal("OnApplied did not fire for the initial generation before Start returned")
	}
	if startup.Phase() != configstore.PhaseStartup || !startup.DefaultDivergent() {
		t.Fatalf("unexpected startup applied report: phase=%s divergent=%t", startup.Phase(), startup.DefaultDivergent())
	}
	if startup.Release().Version() != 1 || startup.Release().ActivationRevision() != 101 {
		t.Fatalf("unexpected applied release: %s", startup.Release())
	}
	if changed := startup.Changed(); len(changed) != 0 {
		t.Fatalf("initial generation reported changes against no previous generation: %#v", changed)
	}
	groups, err := startup.Groups()
	if err != nil {
		t.Fatalf("startup Groups: %v", err)
	}
	wantAliases := make([]string, 0)
	for _, entry := range generatedContract {
		if entry.Kind == configstore.ContractKindParameter {
			wantAliases = append(wantAliases, entry.Alias)
		}
	}
	gotAliases := make([]string, 0, len(groups))
	for alias, document := range groups {
		gotAliases = append(gotAliases, alias)
		if !jsontext.Value(document).IsValid() {
			t.Fatalf("group %s is not a JSON document: %s", alias, document)
		}
		if strings.Contains(string(document), passwordPlaintext) || strings.Contains(string(document), defaultTokenPrefix) {
			t.Fatalf("group %s exposes secret material: %s", alias, document)
		}
	}
	sort.Strings(wantAliases)
	sort.Strings(gotAliases)
	if !reflect.DeepEqual(gotAliases, wantAliases) {
		t.Fatalf("startup Groups aliases = %v, want every contract parameter alias %v", gotAliases, wantAliases)
	}
	if !strings.Contains(string(groups["runtime"]), needle) {
		t.Fatalf("runtime group does not carry the applied (divergent) value: %s", groups["runtime"])
	}
	for _, rendered := range []string{fmt.Sprint(startup), fmt.Sprintf("%+v", startup), fmt.Sprintf("%#v", startup)} {
		if strings.Contains(rendered, needle) {
			t.Fatalf("ordinary applied-report formatting exposed a value: %q", rendered)
		}
	}

	// Restoring the source defaults clears divergence without a further
	// mismatch report; the reload is visible through OnApplied instead.
	restored := matchingRelease(2, 102)
	activate(t, fixture, restored)
	waitAcknowledgement(t, fixture.sub, 2, kmsclient.ReleaseStateApplied)
	waitAppliedVersion(t, fixture.store, 2)
	if fixture.store.Status().DefaultDivergent || fixture.store.Stats().DefaultDivergent {
		t.Fatal("restoring defaults did not clear divergence")
	}
	select {
	case extra := <-mismatches:
		t.Fatalf("restoration emitted an unexpected mismatch report: %v", extra)
	default:
	}
	reload := receiveApplied(t, applied)
	if reload.Phase() != configstore.PhaseRuntime || reload.DefaultDivergent() || reload.Release().Version() != 2 {
		t.Fatalf("unexpected restoration applied report: %s", reload)
	}
	if got := changePaths(reload); !reflect.DeepEqual(got, []string{"runtime.features", "runtime_token"}) {
		t.Fatalf("restoration change paths = %v, want runtime.features and the rotated runtime_token", got)
	}
}

func TestHotReloadReportsChangedFields(t *testing.T) {
	mismatches := make(chan configstore.DefaultMismatchReport, 8)
	applied := make(chan configstore.AppliedReport, 8)
	fixture := startFixtureWithCallbacks(t, matchingRelease(1, 101), fixtureconfig.Defaults, configstore.Callbacks{
		OnDefaultMismatch: func(report configstore.DefaultMismatchReport) { mismatches <- report },
		OnApplied:         func(report configstore.AppliedReport) { applied <- report },
	})
	startup := receiveApplied(t, applied)
	if startup.Phase() != configstore.PhaseStartup || startup.DefaultDivergent() || len(startup.Changed()) != 0 {
		t.Fatalf("unexpected startup applied report: %s", startup)
	}

	// Hot non-secret changes only: the same secret pins are carried forward so
	// the change list names exactly the two canonical parameter paths.
	hot := matchingRelease(2, 102)
	hot.databaseDocument = databaseDocument("db.internal", "750ms", 20)
	hot.runtimeDocument = runtimeDocument([]string{"hot-search"}, []byte("fixture-payload"), map[string]uint64{"burst": 100, "steady": 25}, [2]float64{0.25, 0.75})
	hot.runtimeTokenVersion = 1
	hot.runtimeTokenValue = []byte(defaultTokenPrefix + "1")
	activate(t, fixture, hot)
	waitAcknowledgement(t, fixture.sub, 2, kmsclient.ReleaseStateApplied)
	waitAppliedVersion(t, fixture.store, 2)
	hotReport := receiveApplied(t, applied)
	if hotReport.Phase() != configstore.PhaseRuntime || !hotReport.DefaultDivergent() || hotReport.Release().Version() != 2 {
		t.Fatalf("unexpected hot applied report: %s", hotReport)
	}
	hotChanges := changesByPath(hotReport)
	if got := changePaths(hotReport); !reflect.DeepEqual(got, []string{"database.timeout", "runtime.features"}) {
		t.Fatalf("hot change paths = %v, want database.timeout and runtime.features", got)
	}
	if change := hotChanges["database.timeout"]; change.Previous != "3s" || change.Current != "750ms" {
		t.Fatalf("database.timeout change = %#v, want 3s -> 750ms", change)
	}
	if change := hotChanges["runtime.features"]; !reflect.DeepEqual(change.Previous, []string{"search", "reports"}) || !reflect.DeepEqual(change.Current, []string{"hot-search"}) {
		t.Fatalf("runtime.features change = %#v, want defaults -> hot-search", change)
	}
	// Changed returns fresh copies; mutating one must not alter the report.
	hotReport.Changed()[1].Current.([]string)[0] = "mutated"
	if got := changesByPath(hotReport)["runtime.features"].Current.([]string)[0]; got != "hot-search" {
		t.Fatalf("Changed did not return a defensive copy: %q", got)
	}
	if mismatch := receiveMismatch(t, mismatches); mismatch.Phase() != configstore.PhaseRuntime || !reflect.DeepEqual(differencePaths(mismatch), []string{"database.timeout", "runtime.features"}) {
		t.Fatalf("hot mismatch report = %s", mismatch)
	}

	// Rotating a hot secret produces a path-only entry: never previous or
	// current plaintext, and no non-secret path because nothing else moved.
	rotated := hot
	rotated.releaseVersion, rotated.activationRevision = 3, 103
	rotated.databaseVersion, rotated.runtimeVersion = hot.databaseVersion, hot.runtimeVersion
	rotated.runtimeTokenVersion = 3
	rotated.runtimeTokenValue = []byte("rotated-token-v3")
	activate(t, fixture, rotated)
	waitAcknowledgement(t, fixture.sub, 3, kmsclient.ReleaseStateApplied)
	waitAppliedVersion(t, fixture.store, 3)
	rotatedReport := receiveApplied(t, applied)
	if rotatedReport.Phase() != configstore.PhaseRuntime || !rotatedReport.DefaultDivergent() || rotatedReport.Release().Version() != 3 {
		t.Fatalf("unexpected rotation applied report: %s", rotatedReport)
	}
	rotatedChanges := rotatedReport.Changed()
	if len(rotatedChanges) != 1 || rotatedChanges[0].Path != "runtime_token" || rotatedChanges[0].Previous != nil || rotatedChanges[0].Current != nil {
		t.Fatalf("secret rotation changes = %#v, want one path-only runtime_token entry", rotatedChanges)
	}
	for _, rendered := range []string{fmt.Sprint(rotatedReport), fmt.Sprintf("%+v", rotatedReport), fmt.Sprintf("%#v", rotatedReport)} {
		if strings.Contains(rendered, "rotated-token-v3") || strings.Contains(rendered, defaultTokenPrefix) {
			t.Fatalf("applied report formatting exposed secret plaintext: %q", rendered)
		}
	}
	if got := fixture.store.Current().ApiHandler().RuntimeToken(); got.StringValue() != "rotated-token-v3" || got.Version() != 3 {
		t.Fatalf("rotated secret did not publish: version=%d", got.Version())
	}
	// The rotation release is a distinct candidate carrying the same hot
	// override, so it is reported divergent once more.
	if mismatch := receiveMismatch(t, mismatches); mismatch.Release().Version() != 3 {
		t.Fatalf("rotation mismatch report = %s", mismatch)
	}

	// Restoring the defaults lists the reverted paths and clears divergence.
	restored := matchingRelease(4, 104)
	restored.runtimeTokenVersion = 3
	restored.runtimeTokenValue = []byte("rotated-token-v3")
	activate(t, fixture, restored)
	waitAcknowledgement(t, fixture.sub, 4, kmsclient.ReleaseStateApplied)
	waitAppliedVersion(t, fixture.store, 4)
	restoredReport := receiveApplied(t, applied)
	if restoredReport.Phase() != configstore.PhaseRuntime || restoredReport.DefaultDivergent() || restoredReport.Release().Version() != 4 {
		t.Fatalf("unexpected restoration applied report: %s", restoredReport)
	}
	if got := changePaths(restoredReport); !reflect.DeepEqual(got, []string{"database.timeout", "runtime.features"}) {
		t.Fatalf("restoration change paths = %v, want the two reverted paths", got)
	}
	restoredChanges := changesByPath(restoredReport)
	if change := restoredChanges["database.timeout"]; change.Previous != "750ms" || change.Current != "3s" {
		t.Fatalf("database.timeout restoration = %#v, want 750ms -> 3s", change)
	}
	if change := restoredChanges["runtime.features"]; !reflect.DeepEqual(change.Previous, []string{"hot-search"}) || !reflect.DeepEqual(change.Current, []string{"search", "reports"}) {
		t.Fatalf("runtime.features restoration = %#v, want hot-search -> defaults", change)
	}
	if fixture.store.Status().DefaultDivergent || fixture.store.Stats().DefaultDivergent {
		t.Fatal("default restoration did not clear divergence")
	}
	select {
	case extra := <-mismatches:
		t.Fatalf("matching restoration emitted a drift report: %v", extra)
	default:
	}
	select {
	case extra := <-applied:
		t.Fatalf("unexpected additional applied report: %s", extra)
	default:
	}
}

func receiveApplied(t *testing.T, reports <-chan configstore.AppliedReport) configstore.AppliedReport {
	t.Helper()
	select {
	case report := <-reports:
		return report
	case <-time.After(testOperationTimeout):
		t.Fatal("timed out waiting for an applied report")
		return nil
	}
}

func receiveMismatch(t *testing.T, reports <-chan configstore.DefaultMismatchReport) configstore.DefaultMismatchReport {
	t.Helper()
	select {
	case report := <-reports:
		return report
	case <-time.After(testOperationTimeout):
		t.Fatal("timed out waiting for a default mismatch report")
		return nil
	}
}

func changePaths(report configstore.AppliedReport) []string {
	changes := report.Changed()
	paths := make([]string, len(changes))
	for i := range changes {
		paths[i] = changes[i].Path
	}
	sort.Strings(paths)
	return paths
}

func changesByPath(report configstore.AppliedReport) map[string]configstore.FieldChange {
	changes := report.Changed()
	byPath := make(map[string]configstore.FieldChange, len(changes))
	for _, change := range changes {
		byPath[change.Path] = change
	}
	return byPath
}

func TestHotOverrideAndRestorationPublishWholeGenerations(t *testing.T) {
	reports := make(chan configstore.DefaultMismatchReport, 4)
	fixture := startFixture(t, matchingRelease(1, 101), fixtureconfig.Defaults, func(report configstore.DefaultMismatchReport) { reports <- report })
	oldSnapshot := fixture.store.Current()

	hot := matchingRelease(2, 102)
	hot.databaseDocument = databaseDocument("db.internal", "750ms", 20)
	hot.runtimeDocument = runtimeDocument([]string{"search-v2", "reports-v2"}, []byte("payload-v2"), map[string]uint64{"burst": 200, "steady": 50}, [2]float64{0.5, 1.5})
	hot.runtimeTokenValue = []byte("hot-token-v2")
	activate(t, fixture, hot)
	waitAcknowledgement(t, fixture.sub, 2, kmsclient.ReleaseStateApplied)
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
	waitAcknowledgement(t, fixture.sub, 3, kmsclient.ReleaseStateApplied)
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
	fixture := startFixture(t, matchingRelease(1, 101), fixtureconfig.Defaults, nil)

	restartParameter := matchingRelease(2, 102)
	restartParameter.databaseDocument = databaseDocument("db-restart.internal", "3s", 20)
	activate(t, fixture, restartParameter)
	ack := waitAcknowledgement(t, fixture.sub, 2, kmsclient.ReleaseStateRejected)
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
	ack = waitAcknowledgement(t, fixture.sub, 3, kmsclient.ReleaseStateRejected)
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
	waitAcknowledgement(t, fixture.sub, 4, kmsclient.ReleaseStateApplied)
	current := waitAppliedVersion(t, fixture.store, 4)
	if current.PersistenceHandler().Password().Version() != 1 || current.PersistenceHandler().Password().StringValue() != passwordPlaintext {
		t.Fatal("exact restart-secret pin was not preserved")
	}
	if current.ApiHandler().Features()[0] != "accepted-hot" || current.ApiHandler().RuntimeToken().StringValue() != "accepted-hot-token" {
		t.Fatal("accepted hot candidate was not fully published")
	}
}

func TestStrictDecodeContractAndValidationRejections(t *testing.T) {
	fixture := startFixture(t, matchingRelease(1, 101), fixtureconfig.Defaults, nil)

	var fetches atomic.Int64
	fixture.server.SetGetParameterHook(func(string) { fetches.Add(1) })
	badContract := matchingRelease(2, 102)
	badContract.databaseContentType = "text/plain"
	activate(t, fixture, badContract)
	ack := waitAcknowledgement(t, fixture.sub, 2, kmsclient.ReleaseStateRejected)
	if ack.GetRejectionCategory() != string(configstore.RejectConfigContractMismatch) || ack.GetDiagnostic() != "" {
		t.Fatalf("contract rejection ack = category %q diagnostic %q", ack.GetRejectionCategory(), ack.GetDiagnostic())
	}
	waitRejectedCount(t, fixture.store, configstore.RejectConfigContractMismatch, 1)
	if status := fixture.store.Status(); status.Observed.Version() != 2 || status.Observed.ActivationRevision() != 102 {
		t.Fatalf("contract rejection did not advance observed identity: %+v", status)
	}
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
			ack := waitAcknowledgement(t, fixture.sub, version, kmsclient.ReleaseStateRejected)
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
	ack = waitAcknowledgement(t, fixture.sub, validationVersion, kmsclient.ReleaseStateRejected)
	if ack.GetRejectionCategory() != string(configstore.RejectConfigValidationFailed) || ack.GetDiagnostic() != "" {
		t.Fatalf("validation rejection ack = category %q diagnostic %q", ack.GetRejectionCategory(), ack.GetDiagnostic())
	}
	waitRejectedCount(t, fixture.store, configstore.RejectConfigValidationFailed, 1)
	if got := fixture.store.Current().Release().Version(); got != 1 {
		t.Fatalf("validation failure displaced release 1 with %d", got)
	}
}

func TestConcurrentReadersSeeOnlyCompleteGenerations(t *testing.T) {
	fixture := startFixture(t, matchingRelease(1, 101), fixtureconfig.Defaults, nil)
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
		readers.Go(func() {
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
		})
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
		waitAcknowledgement(t, fixture.sub, version, kmsclient.ReleaseStateApplied)
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
