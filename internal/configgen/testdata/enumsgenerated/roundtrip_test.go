package enumsgenerated

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"reflect"
	"strings"
	"testing"

	rootconfig "github.com/Suhaibinator/kms/internal/configgen/testdata/enums"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient/kmsclienttest"
)

const (
	defaultPlan    = `{"interval":"INTERVAL_MONTHLY","label":"main","min_tier":"TIER_PRO","priority":"Normal","tier":"TIER_FREE","weight":9}`
	defaultRouting = `{"adapter_routes":{"http":{"adapter":"grpc","tier":null}},"adapters":{"primary":"grpc"},"nested_maps":null,"nested_routes":[{"TIER_FREE":{"adapter":"http","tier":null}}],"primary":"http","routes":[{"adapter":"http","tier":null}],"sink_levels":{"File":"Low","None":"Normal"},"sinks":["Stdout","File"],"tier_routes":{"TIER_PRO":{"adapter":"grpc","tier":null},"TIER_UNSPECIFIED":{"adapter":"http","tier":null}}}`
)

func TestEnumDefaultsRoundTripByName(t *testing.T) {
	defaults := rootconfig.Defaults()
	groups, err := EncodeParameterGroups(defaults)
	if err != nil {
		t.Fatal(err)
	}
	for alias, want := range map[string]string{"plan": defaultPlan, "routing": defaultRouting} {
		if got := canonicalJSON(t, string(groups[alias])); got != want {
			t.Fatalf("%s encoded = %s, want %s", alias, got, want)
		}
	}
	var decoded rootconfig.Config
	if err := configstore.DecodeGroup(string(groups["plan"]), &decoded, groupFields0); err != nil {
		t.Fatal(err)
	}
	if err := configstore.DecodeGroup(string(groups["routing"]), &decoded, groupFields1); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(defaults, &decoded) {
		t.Fatalf("round trip changed values: got %#v, want %#v", decoded, *defaults)
	}
}

func TestEnumSchemaDefaultsMatchEncodedDefaults(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Default jsontext.Value `json:"default"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(GeneratedSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	for alias, want := range map[string]string{"plan": defaultPlan, "routing": defaultRouting} {
		if got := canonicalJSON(t, string(schema.Properties[alias].Default)); got != want {
			t.Fatalf("%s schema default = %s, want %s", alias, got, want)
		}
	}
}

func TestEnumDecodeRejectsNumbersAndUnknownNames(t *testing.T) {
	for _, tc := range []struct{ name, document, want string }{
		{"number for name", strings.Replace(defaultPlan, `"tier":"TIER_FREE"`, `"tier":1`, 1), "expected enum name at $.tier"},
		{"unknown name", strings.Replace(defaultPlan, `"tier":"TIER_FREE"`, `"tier":"TIER_GOLD"`, 1), `allowed values are "TIER_UNSPECIFIED", "TIER_FREE", "TIER_PRO"`},
		{"alias name", strings.Replace(defaultPlan, `"tier":"TIER_FREE"`, `"tier":"TIER_PROFESSIONAL"`, 1), "unknown enum value at $.tier"},
		{"go constant name", strings.Replace(defaultPlan, `"priority":"Normal"`, `"priority":"PriorityNormal"`, 1), "unknown enum value at $.priority"},
		{"null scalar", strings.Replace(defaultPlan, `"tier":"TIER_FREE"`, `"tier":null`, 1), "expected enum at $.tier"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var decoded rootconfig.Config
			err := configstore.DecodeGroup(tc.document, &decoded, groupFields0)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("decode error = %v, want substring %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "TIER_GOLD") {
				t.Fatalf("decode error leaks the rejected value: %v", err)
			}
		})
	}
	for _, tc := range []struct{ name, document, want string }{
		{"unknown map key", strings.Replace(defaultRouting, `"TIER_UNSPECIFIED":`, `"PRIVATE_UNKNOWN_KEY":`, 1), "unknown enum value at $.tier_routes[*]"},
		{"alias map key", strings.Replace(defaultRouting, `"TIER_PRO":`, `"TIER_PROFESSIONAL":`, 1), "unknown enum value at $.tier_routes[*]"},
		{"number map key", strings.Replace(defaultRouting, `"TIER_UNSPECIFIED":`, `"0":`, 1), "unknown enum value at $.tier_routes[*]"},
		{"unknown string map key", strings.Replace(defaultRouting, `"adapter_routes":{"http":`, `"adapter_routes":{"ftp":`, 1), "unknown enum value at $.adapter_routes[*]"},
		{"unknown string enum value", strings.Replace(defaultRouting, `"primary":"http"`, `"primary":"ftp"`, 1), `unknown enum value at $.primary; allowed values are "http", "grpc"`},
		{"unknown list element", strings.Replace(defaultRouting, `"Stdout"`, `"Syslog"`, 1), "unknown enum value at $.sinks[]"},
		{"number list element", strings.Replace(defaultRouting, `"Stdout"`, `1`, 1), "expected enum name at $.sinks[]"},
		{"unknown nested pointer", strings.Replace(defaultRouting, `"routes":[{"adapter":"http","tier":null}]`, `"routes":[{"adapter":"http","tier":"TIER_GOLD"}]`, 1), "unknown enum value at $.routes[].tier"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var decoded rootconfig.Config
			err := configstore.DecodeGroup(tc.document, &decoded, groupFields1)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("decode error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestEnumEncodeRejectsUndeclaredValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*rootconfig.Config)
		want   string
	}{
		{"int map key", func(c *rootconfig.Config) {
			c.TierRoutes = map[rootconfig.Tier]rootconfig.Route{7: {Adapter: rootconfig.AdapterHTTP}}
		}, "invalid enum map key at $.tier_routes[*]"},
		{"string map key", func(c *rootconfig.Config) {
			c.AdapterRoutes = map[rootconfig.Adapter]rootconfig.Route{"ftp": {Adapter: rootconfig.AdapterHTTP}}
		}, "invalid enum map key at $.adapter_routes[*]"},
		{"int enum", func(c *rootconfig.Config) { c.Tier = 7 }, "value 7 at $.tier is not a declared enum number"},
		{"uint enum element", func(c *rootconfig.Config) { c.Sinks = []rootconfig.Sink{9} }, "value 9 at $.sinks[] is not a declared enum number"},
		{"string enum", func(c *rootconfig.Config) { c.Primary = "ftp" }, "value at $.primary is not a declared enum value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := rootconfig.Defaults()
			tc.mutate(config)
			_, err := EncodeParameterGroups(config)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("encode error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestEnumDefaultDifferencesReportNames(t *testing.T) {
	const namespace = "prod/enums"
	server, err := kmsclienttest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	plan := strings.Replace(defaultPlan, `"tier":"TIER_FREE"`, `"tier":"TIER_PRO"`, 1)
	routing := strings.Replace(defaultRouting, `["Stdout","File"]`, `["None"]`, 1)
	routing = strings.Replace(routing, `"nested_maps":null`, `"nested_maps":{}`, 1)
	routing = strings.Replace(routing, `"nested_routes":[{"TIER_FREE":{"adapter":"http","tier":null}}]`, `"nested_routes":[]`, 1)
	routing = strings.Replace(routing, `"TIER_PRO":{"adapter":"grpc","tier":null}`, `"TIER_PRO":{"adapter":"http","tier":null}`, 1)
	server.SetParameterVersion(namespace, "groups/plan", plan, "json", 1)
	server.SetParameterVersion(namespace, "groups/routing", routing, "json", 1)
	if _, err := server.SetActiveRelease(kmsclienttest.ReleaseSpec{
		Namespace: namespace, Name: "runtime", Version: 1, SchemaVersion: 1,
		Entries: []kmsclienttest.ReleaseEntrySpec{
			{Alias: "plan", Kind: "parameter", Path: "groups/plan", Version: 1, ContentType: "json"},
			{Alias: "routing", Kind: "parameter", Path: "groups/routing", Version: 1, ContentType: "json"},
		},
	}, 1); err != nil {
		t.Fatal(err)
	}
	client, err := kmsclient.NewClient(kmsclient.Config{Namespace: namespace, ClientName: "enum-test", DialOptions: server.DialOptions()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reports := make(chan []configstore.FieldDifference, 1)
	store, err := Start(ctx, client, Options{
		Release: "runtime", Defaults: func() *rootconfig.Config { c := rootconfig.Defaults(); c.NestedRoutes = nil; return c },
		Callbacks: configstore.Callbacks{OnDefaultMismatch: func(report configstore.DefaultMismatchReport) {
			select {
			case reports <- report.Fields():
			default:
			}
		}},
		InstanceID: "enum-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		if err := store.Wait(); err != nil {
			t.Error(err)
		}
	})
	if got := store.Current().Api().Tier(); got != rootconfig.Tier_TIER_PRO {
		t.Fatalf("decoded tier = %v, want TIER_PRO", got)
	}
	view := store.Current().Api()
	routes := view.TierRoutes()
	delete(routes, rootconfig.Tier_TIER_UNSPECIFIED)
	if _, ok := view.TierRoutes()[rootconfig.Tier_TIER_UNSPECIFIED]; !ok {
		t.Fatal("mutating a returned map changed the snapshot")
	}
	want := []configstore.FieldDifference{
		{Path: "plan.tier", Expected: "TIER_FREE", Actual: "TIER_PRO"},
		{Path: "routing.nested_maps", Expected: nil, Actual: map[string]any{}},
		{Path: "routing.nested_routes", Expected: nil, Actual: []any{}},
		{Path: "routing.sinks", Expected: []any{"Stdout", "File"}, Actual: []any{"None"}},
		{Path: "routing.tier_routes", Expected: map[string]any{"TIER_UNSPECIFIED": map[string]any{"adapter": rootconfig.AdapterHTTP, "tier": nil}, "TIER_PRO": map[string]any{"adapter": rootconfig.AdapterGRPC, "tier": nil}}, Actual: map[string]any{"TIER_UNSPECIFIED": map[string]any{"adapter": rootconfig.AdapterHTTP, "tier": nil}, "TIER_PRO": map[string]any{"adapter": rootconfig.AdapterHTTP, "tier": nil}}},
	}
	select {
	case got := <-reports:
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("difference report = %#v, want %#v", got, want)
		}
	default:
		t.Fatal("missing mismatch report")
	}
}

func canonicalJSON(t *testing.T, value string) string {
	t.Helper()
	canonical, err := configstore.CanonicalParameterValue("json", []byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return string(canonical)
}

func TestEnumMapSchema(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(GeneratedSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	routing := schema["properties"].(map[string]any)["routing"].(map[string]any)["properties"].(map[string]any)
	for field, names := range map[string][]any{"tier_routes": {"TIER_UNSPECIFIED", "TIER_FREE", "TIER_PRO"}, "sink_levels": {"None", "Stdout", "File"}, "adapter_routes": {"http", "grpc"}} {
		object := routing[field].(map[string]any)["anyOf"].([]any)[0].(map[string]any)
		keys := object["propertyNames"].(map[string]any)
		if !reflect.DeepEqual(keys["enum"], names) {
			t.Fatalf("%s key names = %#v, want %#v", field, keys["enum"], names)
		}
		if field != "adapter_routes" && keys["x-kms-enum-numbers"] == nil {
			t.Fatalf("%s lacks enum numbers", field)
		}
		if object["additionalProperties"] == nil {
			t.Fatalf("%s lacks value schema", field)
		}
	}
}

func TestEnumMapPresence(t *testing.T) {
	for _, empty := range []bool{false, true} {
		root := rootconfig.Defaults()
		root.TierRoutes = nil
		root.AdapterRoutes = nil
		if empty {
			root.TierRoutes = map[rootconfig.Tier]rootconfig.Route{}
			root.AdapterRoutes = map[rootconfig.Adapter]rootconfig.Route{}
		}
		groups, err := EncodeParameterGroups(root)
		if err != nil {
			t.Fatal(err)
		}
		var decoded rootconfig.Config
		if err := configstore.DecodeGroup(string(groups["routing"]), &decoded, groupFields1); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(root.TierRoutes, decoded.TierRoutes) || !reflect.DeepEqual(root.AdapterRoutes, decoded.AdapterRoutes) {
			t.Fatal("map presence changed")
		}
	}
}

func TestEnumMapDefaultsArtifact(t *testing.T) {
	data, err := EncodeDefaultsArtifact("test", rootconfig.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := configstore.ParseDefaultsArtifact(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, parameter := range artifact.Parameters {
		if parameter.Alias == "routing" {
			if got := canonicalJSON(t, parameter.Value); got != defaultRouting {
				t.Fatalf("routing defaults = %s, want %s", got, defaultRouting)
			}
			return
		}
	}
	t.Fatal("routing defaults missing")
}
