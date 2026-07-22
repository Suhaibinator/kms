package configkms

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	fixtureconfig "github.com/Suhaibinator/kms/internal/configstorefixture/config"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient/kmsclienttest"
)

// This file is intentionally adversarial and black-box oriented. Its matrix
// crosses the checked-in schema, exact-version fake server, release loader,
// generated decoder, managed admission policy, acknowledgements, and LKG.
// Production code must change to satisfy these tests; assertions here must not
// be relaxed merely because an edge case is inconvenient to support.

const (
	adversarialEndpointDefault = `{"host":"db.internal","ports":[5432,5433],"labels":{"role":["primary","readonly"]},"zones":["us-west-1a","us-west-1b"]}`
	adversarialRuntimeDefault  = `{"features":["search","reports"],"payload":"Zml4dHVyZS1wYXlsb2Fk","thresholds":{"burst":100,"steady":25},"window":[0.25,0.75]}`
)

func adversarialDatabase(endpoint, maxOpen, timeout string) string {
	return fmt.Sprintf(`{"endpoint":%s,"max_open":%s,"timeout":%s}`, endpoint, maxOpen, timeout)
}

func adversarialRuntime(features, payload, thresholds, window string) string {
	return fmt.Sprintf(`{"features":%s,"payload":%s,"thresholds":%s,"window":%s}`, features, payload, thresholds, window)
}

func compileAdversarialFixtureSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile("../runtime.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse generated schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.RegisterFormat(&jsonschema.Format{
		Name: "go-duration",
		Validate: func(value any) error {
			text, ok := value.(string)
			if !ok {
				return nil
			}
			if _, parseErr := time.ParseDuration(text); parseErr != nil {
				return errors.New("not a Go duration")
			}
			return nil
		},
	})
	compiler.RegisterFormat(&jsonschema.Format{
		Name: "kms-base64",
		Validate: func(value any) error {
			text, ok := value.(string)
			if !ok {
				return nil
			}
			decoded, decodeErr := base64.StdEncoding.Strict().DecodeString(text)
			if decodeErr != nil || base64.StdEncoding.EncodeToString(decoded) != text {
				return errors.New("not canonical base64")
			}
			return nil
		},
	})
	compiler.AssertFormat()
	const schemaURL = "https://kms.local/adversarial-runtime.schema.json"
	if err := compiler.AddResource(schemaURL, document); err != nil {
		t.Fatalf("add generated schema: %v", err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		t.Fatalf("compile generated schema: %v", err)
	}
	return schema
}

func adversarialSchemaAccepts(schema *jsonschema.Schema, database, runtime string) bool {
	databaseValue, err := jsonschema.UnmarshalJSON(strings.NewReader(database))
	if err != nil {
		return false
	}
	runtimeValue, err := jsonschema.UnmarshalJSON(strings.NewReader(runtime))
	if err != nil {
		return false
	}
	return schema.Validate(map[string]any{"database": databaseValue, "runtime": runtimeValue}) == nil
}

type adversarialAcknowledgement struct {
	state      string
	category   string
	diagnostic string
}

func waitAdversarialFinalAcknowledgement(t *testing.T, fixture *runningFixture, version uint64) adversarialAcknowledgement {
	t.Helper()
	deadline := time.Now().Add(testOperationTimeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timed out waiting for final acknowledgement for release %d", version)
		}
		ack, err := fixture.sub.WaitAcknowledgement(remaining)
		if err != nil {
			t.Fatalf("wait for release %d acknowledgement: %v", version, err)
		}
		if ack.GetVersion() != version {
			continue
		}
		if ack.GetState() == kmsclient.ReleaseStateApplied || ack.GetState() == kmsclient.ReleaseStateRejected {
			return adversarialAcknowledgement{
				state:      ack.GetState(),
				category:   ack.GetRejectionCategory(),
				diagnostic: ack.GetDiagnostic(),
			}
		}
	}
}

func TestAdversarialGeneratedSchemaAndRuntimeDecoderHaveIdenticalAcceptance(t *testing.T) {
	schema := compileAdversarialFixtureSchema(t)
	fixture := startFixture(t, matchingRelease(1, 1001), fixtureDefaults, false, nil)

	defaultDatabase := adversarialDatabase(adversarialEndpointDefault, "20", `"3s"`)
	tests := []struct {
		name         string
		database     string
		runtime      string
		wantSchema   bool
		wantState    string
		wantCategory string
	}{
		{name: "root unknown property", database: strings.TrimSuffix(defaultDatabase, "}") + `,"plaintext_canary":"must-not-be-accepted"}`, runtime: adversarialRuntimeDefault},
		{name: "nested unknown property", database: adversarialDatabase(strings.TrimSuffix(adversarialEndpointDefault, "}")+`,"unknown_nested":true}`, "20", `"3s"`), runtime: adversarialRuntimeDefault},
		{name: "missing nested property", database: adversarialDatabase(`{"ports":[5432,5433],"labels":{"role":["primary"]},"zones":["a","b"]}`, "20", `"3s"`), runtime: adversarialRuntimeDefault},
		{name: "negative uint16", database: adversarialDatabase(`{"host":"db.internal","ports":[-1,5433],"labels":{"role":["primary"]},"zones":["a","b"]}`, "20", `"3s"`), runtime: adversarialRuntimeDefault},
		{name: "uint16 overflow", database: adversarialDatabase(`{"host":"db.internal","ports":[65536,5433],"labels":{"role":["primary"]},"zones":["a","b"]}`, "20", `"3s"`), runtime: adversarialRuntimeDefault},
		{name: "portable int overflow", database: adversarialDatabase(adversarialEndpointDefault, "2147483648", `"3s"`), runtime: adversarialRuntimeDefault},
		{name: "fractional int", database: adversarialDatabase(adversarialEndpointDefault, "20.1", `"3s"`), runtime: adversarialRuntimeDefault},
		{name: "wrong fixed array length", database: adversarialDatabase(`{"host":"db.internal","ports":[5432,5433],"labels":{"role":["primary"]},"zones":["a"]}`, "20", `"3s"`), runtime: adversarialRuntimeDefault},
		{name: "invalid duration", database: adversarialDatabase(adversarialEndpointDefault, "20", `"three seconds"`), runtime: adversarialRuntimeDefault},
		{name: "null nested map", database: adversarialDatabase(`{"host":"db.internal","ports":[5432,5433],"labels":null,"zones":["us-west-1a","us-west-1b"]}`, "20", `"3s"`), runtime: adversarialRuntimeDefault, wantSchema: true, wantState: kmsclient.ReleaseStateRejected, wantCategory: string(configstore.RejectRestartRequired)},
		{name: "null slice", database: defaultDatabase, runtime: adversarialRuntime("null", `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":100,"steady":25}`, `[0.25,0.75]`), wantSchema: true, wantState: kmsclient.ReleaseStateApplied},
		{name: "null bytes", database: defaultDatabase, runtime: adversarialRuntime(`["search","reports"]`, "null", `{"burst":100,"steady":25}`, `[0.25,0.75]`), wantSchema: true, wantState: kmsclient.ReleaseStateApplied},
		{name: "null map", database: defaultDatabase, runtime: adversarialRuntime(`["search","reports"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, "null", `[0.25,0.75]`), wantSchema: true, wantState: kmsclient.ReleaseStateApplied},
		{name: "noncanonical base64 padding bits", database: defaultDatabase, runtime: adversarialRuntime(`["search","reports"]`, `"Zh=="`, `{"burst":100,"steady":25}`, `[0.25,0.75]`)},
		{name: "uint64 overflow", database: defaultDatabase, runtime: adversarialRuntime(`["search","reports"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":18446744073709551616,"steady":25}`, `[0.25,0.75]`)},
		{name: "negative uint64", database: defaultDatabase, runtime: adversarialRuntime(`["search","reports"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":-1,"steady":25}`, `[0.25,0.75]`)},
		{name: "float overflow", database: defaultDatabase, runtime: adversarialRuntime(`["search","reports"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":100,"steady":25}`, `[1e309,0.75]`)},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			version := uint64(index + 2)
			before := fixture.store.Current().Release().Version()
			candidate := matchingRelease(version, 1000+version)
			candidate.databaseDocument = test.database
			candidate.runtimeDocument = test.runtime
			schemaAccepts := adversarialSchemaAccepts(schema, test.database, test.runtime)
			if schemaAccepts != test.wantSchema {
				t.Fatalf("generated schema accepts=%t, want %t", schemaAccepts, test.wantSchema)
			}
			activate(t, fixture, candidate)
			ack := waitAdversarialFinalAcknowledgement(t, fixture, version)
			wantState := test.wantState
			wantCategory := test.wantCategory
			if wantState == "" {
				wantState = kmsclient.ReleaseStateRejected
				wantCategory = string(configstore.RejectConfigDecodeFailed)
			}
			if ack.state != wantState || ack.category != wantCategory || ack.diagnostic != "" {
				t.Fatalf("final acknowledgement = %+v, want state=%q category=%q", ack, wantState, wantCategory)
			}
			wantApplied := before
			if wantState == kmsclient.ReleaseStateApplied {
				wantApplied = version
			}
			if got := fixture.store.Current().Release().Version(); got != wantApplied {
				t.Fatalf("active release = %d, want %d", got, wantApplied)
			}
		})
	}
}

func TestAdversarialNilAndEmptyCollectionsRemainDistinctAcrossPublication(t *testing.T) {
	type nilShapeCase struct {
		name          string
		path          string
		setNilDefault func(*fixtureconfig.Config)
		nullDocument  string
		emptyDocument string
		isNil         func(Snapshot) bool
		isNonNilEmpty func(Snapshot) bool
	}
	cases := []nilShapeCase{
		{
			name: "slice",
			path: "runtime.features",
			setNilDefault: func(value *fixtureconfig.Config) {
				value.Features = nil
			},
			nullDocument:  adversarialRuntime("null", `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":100,"steady":25}`, `[0.25,0.75]`),
			emptyDocument: adversarialRuntime("[]", `"Zml4dHVyZS1wYXlsb2Fk"`, `{"burst":100,"steady":25}`, `[0.25,0.75]`),
			isNil: func(snapshot Snapshot) bool {
				return snapshot.ApiHandler().Features() == nil
			},
			isNonNilEmpty: func(snapshot Snapshot) bool {
				value := snapshot.ApiHandler().Features()
				return value != nil && len(value) == 0
			},
		},
		{
			name: "bytes",
			path: "runtime.payload",
			setNilDefault: func(value *fixtureconfig.Config) {
				value.Payload = nil
			},
			nullDocument:  adversarialRuntime(`["search","reports"]`, "null", `{"burst":100,"steady":25}`, `[0.25,0.75]`),
			emptyDocument: adversarialRuntime(`["search","reports"]`, `""`, `{"burst":100,"steady":25}`, `[0.25,0.75]`),
			isNil: func(snapshot Snapshot) bool {
				return snapshot.ApiHandler().Payload() == nil
			},
			isNonNilEmpty: func(snapshot Snapshot) bool {
				value := snapshot.ApiHandler().Payload()
				return value != nil && len(value) == 0
			},
		},
		{
			name: "map",
			path: "runtime.thresholds",
			setNilDefault: func(value *fixtureconfig.Config) {
				value.Thresholds = nil
			},
			nullDocument:  adversarialRuntime(`["search","reports"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, "null", `[0.25,0.75]`),
			emptyDocument: adversarialRuntime(`["search","reports"]`, `"Zml4dHVyZS1wYXlsb2Fk"`, "{}", `[0.25,0.75]`),
			isNil: func(snapshot Snapshot) bool {
				return snapshot.ApiHandler().Thresholds() == nil
			},
			isNonNilEmpty: func(snapshot Snapshot) bool {
				value := snapshot.ApiHandler().Thresholds()
				return value != nil && len(value) == 0
			},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			initial := matchingRelease(1, 1501)
			initial.runtimeDocument = test.nullDocument
			reports := make(chan configstore.DefaultMismatchReport, 2)
			fixture := startFixture(t, initial, func() *fixtureconfig.Config {
				defaults := fixtureDefaults()
				test.setNilDefault(defaults)
				return defaults
			}, false, func(report configstore.DefaultMismatchReport) { reports <- report })
			if !test.isNil(fixture.store.Current()) {
				t.Fatal("JSON null did not publish as the Go nil value")
			}

			empty := matchingRelease(2, 1502)
			empty.runtimeDocument = test.emptyDocument
			activate(t, fixture, empty)
			ack := waitAdversarialFinalAcknowledgement(t, fixture, 2)
			if ack.state != kmsclient.ReleaseStateApplied {
				t.Fatalf("empty hot override acknowledgement = %+v", ack)
			}
			emptySnapshot := waitAppliedVersion(t, fixture.store, 2)
			if !test.isNonNilEmpty(emptySnapshot) {
				t.Fatal("empty JSON collection collapsed into Go nil")
			}
			report := <-reports
			paths := differencePaths(report)
			if len(paths) != 1 || paths[0] != test.path || report.Phase() != configstore.MismatchRuntime || report.Severity() != configstore.MismatchError {
				t.Fatalf("nil-to-empty drift report = %s/%s paths=%v", report.Phase(), report.Severity(), paths)
			}
			if !fixture.store.Status().DefaultDivergent {
				t.Fatal("nil-to-empty override did not mark store divergent")
			}

			restored := matchingRelease(3, 1503)
			restored.runtimeDocument = test.nullDocument
			activate(t, fixture, restored)
			if ack := waitAdversarialFinalAcknowledgement(t, fixture, 3); ack.state != kmsclient.ReleaseStateApplied {
				t.Fatalf("null restoration acknowledgement = %+v", ack)
			}
			restoredSnapshot := waitAppliedVersion(t, fixture.store, 3)
			if !test.isNil(restoredSnapshot) || fixture.store.Status().DefaultDivergent {
				t.Fatal("restoration did not recover nil and clear divergence")
			}
			select {
			case extra := <-reports:
				t.Fatalf("matching null restoration emitted unexpected report: %v", extra)
			default:
			}
		})
	}
}

func TestAdversarialCanonicalJSONSpellingsApplyWithoutFalseDrift(t *testing.T) {
	schema := compileAdversarialFixtureSchema(t)
	fixture := startFixture(t, matchingRelease(1, 2001), fixtureDefaults, false, nil)

	tests := []struct {
		name     string
		database string
		runtime  string
	}{
		{
			name: "mathematically integral decimal and exponent numbers",
			database: adversarialDatabase(
				`{"host":"db.internal","ports":[5.432e3,54330e-1],"labels":{"role":["primary","readonly"]},"zones":["us-west-1a","us-west-1b"]}`,
				"2e1",
				`"3000ms"`,
			),
			runtime: adversarialRuntime(
				`["search","reports"]`,
				`"Zml4dHVyZS1wYXlsb2Fk"`,
				`{"burst":1e2,"steady":2.5e1}`,
				`[25e-2,75e-2]`,
			),
		},
		{
			name: "integer decimal points accepted by JSON Schema integer semantics",
			database: adversarialDatabase(
				adversarialEndpointDefault,
				"20.000",
				`"3s"`,
			),
			runtime: adversarialRuntime(
				`["search","reports"]`,
				`"Zml4dHVyZS1wYXlsb2Fk"`,
				`{"burst":100.0,"steady":25.000}`,
				`[0.2500,0.7500]`,
			),
		},
		{
			name: "escapes ordering and whitespace compare after typed decoding",
			database: `{
				"timeout":"3s",
				"max_open":20,
				"endpoint":{"zones":["us-west-1a","us-west-1b"],"labels":{"role":["pri\u006dary","readonly"]},"ports":[5432,5433],"host":"db.\u0069nternal"}
			}`,
			runtime: `{
				"window":[0.25,0.75],
				"thresholds":{"steady":25,"burst":100},
				"payload":"Zml4dHVyZS1wYXlsb2Fk",
				"features":["se\u0061rch","reports"]
			}`,
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !adversarialSchemaAccepts(schema, test.database, test.runtime) {
				t.Fatal("generated schema rejected a canonical spelling")
			}
			version := uint64(index + 2)
			candidate := matchingRelease(version, 2000+version)
			candidate.databaseDocument = test.database
			candidate.runtimeDocument = test.runtime
			activate(t, fixture, candidate)
			ack := waitAdversarialFinalAcknowledgement(t, fixture, version)
			if ack.state != kmsclient.ReleaseStateApplied || ack.category != "" || ack.diagnostic != "" {
				t.Fatalf("canonical spelling final acknowledgement = %+v", ack)
			}
			snapshot := fixture.store.Current()
			if snapshot.Release().Version() != version || snapshot.PersistenceHandler().MaxOpen() != 20 || snapshot.DatabaseHealth().Timeout() != 3*time.Second {
				t.Fatalf("canonical spelling did not publish typed defaults: release=%d max_open=%d timeout=%s", snapshot.Release().Version(), snapshot.PersistenceHandler().MaxOpen(), snapshot.DatabaseHealth().Timeout())
			}
			if fixture.store.Status().DefaultDivergent || fixture.store.Stats().DefaultDivergent {
				t.Fatal("equivalent canonical spelling caused false default drift")
			}
		})
	}
}

func TestAdversarialManifestContractMatrixRejectsBeforeResourceFetch(t *testing.T) {
	fixture := startFixture(t, matchingRelease(1, 3001), fixtureDefaults, false, nil)
	var fetches atomic.Int64
	fixture.server.SetGetParameterHook(func(string) { fetches.Add(1) })
	t.Cleanup(func() { fixture.server.SetGetParameterHook(nil) })

	type contractCase struct {
		name   string
		mutate func(*kmsclienttest.Server, *kmsclienttest.ReleaseSpec, releaseData)
	}
	cases := []contractCase{
		{name: "missing database parameter", mutate: omitContractAlias("database")},
		{name: "missing runtime parameter", mutate: omitContractAlias("runtime")},
		{name: "missing restart secret", mutate: omitContractAlias("database_password")},
		{name: "missing hot secret", mutate: omitContractAlias("runtime_token")},
		{
			name: "unknown extra alias",
			mutate: func(_ *kmsclienttest.Server, spec *kmsclienttest.ReleaseSpec, data releaseData) {
				spec.Entries = append(spec.Entries, kmsclienttest.ReleaseEntrySpec{
					Alias: "untrusted_alias_canary", Kind: "parameter", Path: runtimePath,
					Version: data.runtimeVersion, ContentType: "json",
				})
			},
		},
		{
			name: "case changed alias",
			mutate: func(_ *kmsclienttest.Server, spec *kmsclienttest.ReleaseSpec, _ releaseData) {
				contractEntry(spec, "runtime").Alias = "Runtime"
			},
		},
		{
			name: "parameter declared as secret",
			mutate: func(server *kmsclienttest.Server, spec *kmsclienttest.ReleaseSpec, data releaseData) {
				server.SetSecretVersion(fixtureNamespace, databasePath, []byte("wrong-kind-secret-canary"), "json", data.databaseVersion)
				contractEntry(spec, "database").Kind = "secret"
			},
		},
		{
			name: "secret declared as parameter",
			mutate: func(server *kmsclienttest.Server, spec *kmsclienttest.ReleaseSpec, data releaseData) {
				server.SetParameterVersion(fixtureNamespace, passwordPath, `{}`, "json", data.passwordVersion)
				entry := contractEntry(spec, "database_password")
				entry.Kind = "parameter"
				entry.ContentType = "json"
			},
		},
		{
			name: "incorrect exact parameter content type",
			mutate: func(_ *kmsclienttest.Server, spec *kmsclienttest.ReleaseSpec, _ releaseData) {
				contractEntry(spec, "runtime").ContentType = "application/json"
			},
		},
	}

	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			version := uint64(index + 2)
			candidate := matchingRelease(version, 3000+version)
			scriptResources(fixture.server, candidate)
			spec := releaseSpec(candidate)
			test.mutate(fixture.server, &spec, candidate)
			fetches.Store(0)
			if _, err := fixture.server.ActivateConfigurationRelease(spec, candidate.activationRevision); err != nil {
				t.Fatal(err)
			}
			ack := waitAdversarialFinalAcknowledgement(t, fixture, version)
			if ack.state != kmsclient.ReleaseStateRejected || ack.category != string(configstore.RejectConfigContractMismatch) || ack.diagnostic != "" {
				t.Fatalf("contract acknowledgement = %+v", ack)
			}
			if got := fetches.Load(); got != 0 {
				t.Fatalf("contract-invalid candidate fetched %d parameter resources", got)
			}
			status := fixture.store.Status()
			if status.Observed.Version() != version || status.Observed.ActivationRevision() != candidate.activationRevision {
				t.Fatalf("observed identity did not advance for prefetch rejection: %+v", status)
			}
			if status.Applied.Version() != 1 || fixture.store.Current().Release().Version() != 1 {
				t.Fatalf("contract-invalid candidate displaced LKG: %+v", status)
			}
		})
	}
}

func omitContractAlias(alias string) func(*kmsclienttest.Server, *kmsclienttest.ReleaseSpec, releaseData) {
	return func(_ *kmsclienttest.Server, spec *kmsclienttest.ReleaseSpec, _ releaseData) {
		entries := spec.Entries[:0]
		for _, entry := range spec.Entries {
			if entry.Alias != alias {
				entries = append(entries, entry)
			}
		}
		spec.Entries = entries
	}
}

func contractEntry(spec *kmsclienttest.ReleaseSpec, alias string) *kmsclienttest.ReleaseEntrySpec {
	for index := range spec.Entries {
		if spec.Entries[index].Alias == alias {
			return &spec.Entries[index]
		}
	}
	panic("adversarial test contract entry not found: " + alias)
}

func TestAdversarialSecretPinsPathsAndGetterCopies(t *testing.T) {
	fixture := startFixture(t, matchingRelease(1, 4001), fixtureDefaults, false, nil)
	initial := fixture.store.Current()

	// Parameter paths are operator-owned. Moving both groups without changing
	// their typed contents must not create restart drift or default drift.
	remapped := matchingRelease(2, 4002)
	scriptResources(fixture.server, remapped)
	const remappedDatabasePath = "operator-owned/renamed/database"
	const remappedRuntimePath = "operator-owned/renamed/runtime"
	fixture.server.SetParameterVersion(fixtureNamespace, remappedDatabasePath, remapped.databaseDocument, "json", remapped.databaseVersion)
	fixture.server.SetParameterVersion(fixtureNamespace, remappedRuntimePath, remapped.runtimeDocument, "json", remapped.runtimeVersion)
	remappedSpec := releaseSpec(remapped)
	contractEntry(&remappedSpec, "database").Path = remappedDatabasePath
	contractEntry(&remappedSpec, "runtime").Path = remappedRuntimePath
	if _, err := fixture.server.ActivateConfigurationRelease(remappedSpec, remapped.activationRevision); err != nil {
		t.Fatal(err)
	}
	if ack := waitAdversarialFinalAcknowledgement(t, fixture, 2); ack.state != kmsclient.ReleaseStateApplied {
		t.Fatalf("operator-owned parameter remap acknowledgement = %+v", ack)
	}
	remappedSnapshot := waitAppliedVersion(t, fixture.store, 2)
	if remappedSnapshot.PersistenceHandler().Endpoint().Host != "db.internal" || fixture.store.Status().DefaultDivergent {
		t.Fatal("physical parameter path remap changed typed/default semantics")
	}

	// A hot secret may move physical path and version. The generated getter must
	// preserve exact metadata while returning an independent plaintext buffer.
	const hotSecretPathCanary = "operator-owned/runtime-token-path-canary"
	const hotSecretValueCanary = "hot-secret-plaintext-canary"
	rotated := matchingRelease(3, 4003)
	rotated.runtimeTokenPath = hotSecretPathCanary
	rotated.runtimeTokenValue = []byte(hotSecretValueCanary)
	activate(t, fixture, rotated)
	if ack := waitAdversarialFinalAcknowledgement(t, fixture, 3); ack.state != kmsclient.ReleaseStateApplied {
		t.Fatalf("hot secret rotation acknowledgement = %+v", ack)
	}
	rotatedSnapshot := waitAppliedVersion(t, fixture.store, 3)
	secret := rotatedSnapshot.ApiHandler().RuntimeToken()
	wantSecretPath := "/" + fixtureNamespace + "/" + hotSecretPathCanary
	if secret.Path() != wantSecretPath || secret.Version() != rotated.runtimeTokenVersion || secret.ContentType() != "text/plain" || secret.StringValue() != hotSecretValueCanary {
		t.Fatalf("hot secret exact pin metadata mismatch: path=%q version=%d content_type=%q", secret.Path(), secret.Version(), secret.ContentType())
	}
	for _, rendered := range []string{fmt.Sprint(secret), fmt.Sprintf("%+v", secret), fmt.Sprintf("%#v", secret), fmt.Sprintf("%q", secret)} {
		if strings.Contains(rendered, hotSecretValueCanary) || strings.Contains(rendered, hotSecretPathCanary) {
			t.Fatalf("ordinary secret formatting exposed plaintext or metadata: %q", rendered)
		}
	}
	secret.Value()[0] = 'X'
	if got := rotatedSnapshot.ApiHandler().RuntimeToken().StringValue(); got != hotSecretValueCanary {
		t.Fatalf("mutating a returned secret changed the captured generation: %q", got)
	}
	if got := initial.ApiHandler().RuntimeToken().StringValue(); got != defaultTokenPrefix+"1" {
		t.Fatalf("old snapshot secret changed after rotation: %q", got)
	}

	// Restart-secret admission is identity-based, not plaintext-based. The same
	// bytes under a different immutable pin must reject the whole candidate.
	const restartSecretPathCanary = "operator-owned/password-path-canary"
	restart := matchingRelease(4, 4004)
	restart.passwordPath = restartSecretPathCanary
	restart.passwordVersion = 2
	restart.passwordValue = []byte(passwordPlaintext)
	activate(t, fixture, restart)
	ack := waitAdversarialFinalAcknowledgement(t, fixture, 4)
	if ack.state != kmsclient.ReleaseStateRejected || ack.category != string(configstore.RejectRestartRequired) || ack.diagnostic != "" {
		t.Fatalf("same-plaintext restart-secret acknowledgement = %+v", ack)
	}
	if fixture.store.Current().Release().Version() != 3 {
		t.Fatal("restart-secret pin change displaced LKG")
	}
	for _, rendered := range []string{fmt.Sprint(fixture.store.Status()), fmt.Sprint(fixture.store.Stats()), fmt.Sprintf("%+v", ack)} {
		if strings.Contains(rendered, restartSecretPathCanary) || strings.Contains(rendered, passwordPlaintext) {
			t.Fatalf("bounded status/ack exposed secret metadata or plaintext: %q", rendered)
		}
	}

	// Even with a newer version current at the physical path, a later release
	// must resolve the older exact version pinned in its manifest.
	exact := matchingRelease(5, 4005)
	scriptResources(fixture.server, exact)
	const unpinnedSecretCanary = "newer-unpinned-secret-plaintext-canary"
	fixture.server.SetSecretVersion(fixtureNamespace, passwordPath, []byte(unpinnedSecretCanary), "text/plain", 99)
	if _, err := fixture.server.ActivateConfigurationRelease(releaseSpec(exact), exact.activationRevision); err != nil {
		t.Fatal(err)
	}
	if ack := waitAdversarialFinalAcknowledgement(t, fixture, 5); ack.state != kmsclient.ReleaseStateApplied {
		t.Fatalf("exact secret pin acknowledgement = %+v", ack)
	}
	exactSnapshot := waitAppliedVersion(t, fixture.store, 5)
	password := exactSnapshot.PersistenceHandler().Password()
	if password.Version() != 1 || password.StringValue() != passwordPlaintext || password.StringValue() == unpinnedSecretCanary {
		t.Fatalf("release did not resolve exact secret version: version=%d value=%s", password.Version(), password)
	}
}

func TestAdversarialDefaultSecretsMustBeExactZeroIncludingMetadata(t *testing.T) {
	server, err := kmsclienttest.New()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.SetSecretVersion(fixtureNamespace, "empty-default-secret", nil, "text/plain", 77)
	client := newFixtureClient(t, server)
	defer func() { _ = client.Close() }()
	metadataOnly, err := client.GetSecret(context.Background(), "empty-default-secret", kmsclient.WithVersion(77))
	if err != nil {
		t.Fatal(err)
	}
	if !metadataOnly.IsZero() || metadataOnly.Path() == "" || metadataOnly.Version() != 77 || metadataOnly.ContentType() == "" {
		t.Fatalf("test precondition failed for metadata-only secret: path=%q version=%d content_type=%q", metadataOnly.Path(), metadataOnly.Version(), metadataOnly.ContentType())
	}

	for _, secretField := range []string{"Password", "RuntimeToken"} {
		t.Run(secretField, func(t *testing.T) {
			defaults := fixtureDefaults()
			if secretField == "Password" {
				defaults.Password = metadataOnly
			} else {
				defaults.RuntimeToken = metadataOnly
			}
			store, startErr := Start(context.Background(), client, Options{
				Release:           fixtureReleaseName,
				Defaults:          func() *fixtureconfig.Config { return defaults },
				OnDefaultMismatch: func(configstore.DefaultMismatchReport) {},
			})
			if store != nil || startErr == nil || !strings.Contains(startErr.Error(), "must be zero") {
				t.Fatalf("metadata-bearing default secret Start = (%v, %v), want exact-zero rejection", store, startErr)
			}
			for _, rendered := range []string{fmt.Sprint(startErr), fmt.Sprintf("%+v", startErr), fmt.Sprintf("%#v", startErr)} {
				if strings.Contains(rendered, metadataOnly.Path()) {
					t.Fatalf("default-secret rejection exposed secret path: %q", rendered)
				}
			}
		})
	}
}

func TestAdversarialConcurrentReadersAcrossRejectedAndAppliedCandidates(t *testing.T) {
	fixture := startFixture(t, matchingRelease(1, 5001), fixtureDefaults, false, nil)
	oldSnapshot := fixture.store.Current()
	var stop atomic.Bool
	var readers sync.WaitGroup
	failures := make(chan string, 1)
	recordFailure := func(message string) {
		select {
		case failures <- message:
		default:
		}
	}

	for range 24 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for !stop.Load() {
				snapshot := fixture.store.Current()
				if problem := adversarialSnapshotProblem(snapshot); problem != "" {
					recordFailure(problem)
					return
				}
			}
		}()
	}
	stopReadersAndWait := func() {
		stop.Store(true)
		readers.Wait()
	}
	t.Cleanup(stopReadersAndWait)

	for validVersion := uint64(3); validVersion <= 13; validVersion += 2 {
		invalidVersion := validVersion - 1
		invalid := matchingRelease(invalidVersion, 5000+invalidVersion)
		if invalidVersion%4 == 0 {
			invalid.runtimeDocument = adversarialRuntime(`["broken"]`, `"%%%"`, `{"generation":1}`, `[1,1]`)
		} else {
			invalid.databaseDocument = `{`
		}
		activate(t, fixture, invalid)
		invalidAck := waitAdversarialFinalAcknowledgement(t, fixture, invalidVersion)
		if invalidAck.state != kmsclient.ReleaseStateRejected || invalidAck.category != string(configstore.RejectConfigDecodeFailed) {
			t.Fatalf("invalid release %d acknowledgement = %+v", invalidVersion, invalidAck)
		}

		valid := matchingRelease(validVersion, 5000+validVersion)
		valid.databaseDocument = databaseDocument("db.internal", fmt.Sprintf("%dms", validVersion), 20)
		valid.runtimeDocument = runtimeDocument(
			[]string{fmt.Sprintf("generation-%d", validVersion)},
			[]byte(fmt.Sprintf("payload-%d", validVersion)),
			map[string]uint64{"generation": validVersion},
			[2]float64{float64(validVersion), float64(validVersion)},
		)
		valid.runtimeTokenValue = []byte(fmt.Sprintf("token-%d", validVersion))
		activate(t, fixture, valid)
		validAck := waitAdversarialFinalAcknowledgement(t, fixture, validVersion)
		if validAck.state != kmsclient.ReleaseStateApplied {
			t.Fatalf("valid release %d acknowledgement = %+v", validVersion, validAck)
		}
		waitAppliedVersion(t, fixture.store, validVersion)
	}

	stopReadersAndWait()
	select {
	case failure := <-failures:
		t.Fatal(failure)
	default:
	}
	if oldSnapshot.Release().Version() != 1 || oldSnapshot.ApiHandler().Features()[0] != "search" || oldSnapshot.ApiHandler().RuntimeToken().StringValue() != defaultTokenPrefix+"1" {
		t.Fatal("old snapshot mutated during concurrent activation stress")
	}
}

func adversarialSnapshotProblem(snapshot Snapshot) string {
	version := snapshot.Release().Version()
	api := snapshot.ApiHandler()
	jobs := snapshot.BackgroundJobs()
	health := snapshot.DatabaseHealth()
	if version == 1 {
		if api.Features()[0] != "search" || jobs.Features()[0] != "search" || health.Timeout() != 3*time.Second || api.RuntimeToken().StringValue() != defaultTokenPrefix+"1" {
			return "release 1 exposed a mixed generation"
		}
		return ""
	}
	if version%2 == 0 || version < 3 || version > 13 {
		return fmt.Sprintf("reader observed rejected/unexpected release %d", version)
	}
	wantFeature := fmt.Sprintf("generation-%d", version)
	wantPayload := fmt.Sprintf("payload-%d", version)
	wantToken := fmt.Sprintf("token-%d", version)
	features := api.Features()
	jobFeatures := jobs.Features()
	payload := api.Payload()
	thresholds := api.Thresholds()
	secret := api.RuntimeToken()
	if len(features) != 1 || features[0] != wantFeature || len(jobFeatures) != 1 || jobFeatures[0] != wantFeature ||
		string(payload) != wantPayload || thresholds["generation"] != version || jobs.Window() != [2]float64{float64(version), float64(version)} ||
		health.Timeout() != time.Duration(version)*time.Millisecond || secret.StringValue() != wantToken {
		return fmt.Sprintf("release %d exposed fields from different generations", version)
	}
	features[0] = "mutated"
	jobFeatures[0] = "mutated"
	payload[0] = 'X'
	thresholds["generation"] = 0
	secret.Value()[0] = 'X'
	if api.Features()[0] != wantFeature || jobs.Features()[0] != wantFeature || string(api.Payload()) != wantPayload ||
		api.Thresholds()["generation"] != version || api.RuntimeToken().StringValue() != wantToken {
		return fmt.Sprintf("release %d getter copy mutation reached active storage", version)
	}
	return ""
}

func fixtureDefaults() *fixtureconfig.Config {
	return fixtureconfig.Defaults()
}
