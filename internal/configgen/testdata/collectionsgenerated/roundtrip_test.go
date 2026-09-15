package collectionsgenerated

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"

	rootconfig "github.com/Suhaibinator/kms/internal/configgen/testdata/collections"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient/kmsclienttest"
)

func TestCollectionDefaultsRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		root rootconfig.Config
		want string
	}{
		{"nil", rootconfig.Config{}, `{"inference_providers":null,"items":null,"labels":null,"payload":null}`},
		{"empty", rootconfig.Config{Providers: []rootconfig.ProviderConfig{}, Items: []string{}, Labels: map[string][]string{}, Payload: []byte{}}, `{"inference_providers":[],"items":[],"labels":{},"payload":""}`},
		{"nested nil", rootconfig.Config{Providers: []rootconfig.ProviderConfig{{}}, Labels: map[string][]string{"nil": nil, "empty": {}}}, `{"inference_providers":[{"http":{"headers":null}}],"items":null,"labels":{"empty":[],"nil":null},"payload":null}`},
		{"nested empty", rootconfig.Config{Providers: []rootconfig.ProviderConfig{{HTTP: rootconfig.HTTPProviderOptions{Headers: []rootconfig.Header{}}}}}, `{"inference_providers":[{"http":{"headers":[]}}],"items":null,"labels":null,"payload":null}`},
		{"populated", rootconfig.Config{Providers: []rootconfig.ProviderConfig{{HTTP: rootconfig.HTTPProviderOptions{Headers: []rootconfig.Header{{Name: "X-Test", Value: "yes"}}}}}, Items: []string{"one"}, Labels: map[string][]string{"key": {"value"}}, Payload: []byte("abc")}, `{"inference_providers":[{"http":{"headers":[{"name":"X-Test","value":"yes"}]}}],"items":["one"],"labels":{"key":["value"]},"payload":"YWJj"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groups, err := EncodeParameterGroups(&tc.root)
			if err != nil {
				t.Fatal(err)
			}
			if got := canonicalJSON(t, string(groups["genai"])); got != tc.want {
				t.Fatalf("encoded = %s, want %s", got, tc.want)
			}
			var decoded rootconfig.Config
			if err := configstore.DecodeGroup(string(groups["genai"]), &decoded, groupFields0); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(&tc.root, &decoded) {
				t.Fatalf("round trip changed values: got %#v, want %#v", decoded, tc.root)
			}
			testStoreDefaults(t, &tc.root, string(groups["genai"]), nil)
		})
	}
}

func TestCollectionDefaultDifferencesRemainVisible(t *testing.T) {
	defaults := &rootconfig.Config{Providers: []rootconfig.ProviderConfig{{}}}
	for _, tc := range []struct{ name, document, path string }{
		{"top-level slice", `{"inference_providers":[{"http":{"headers":null}}],"items":[],"labels":null,"payload":null}`, "genai.items"},
		{"nested slice", `{"inference_providers":[{"http":{"headers":[]}}],"items":null,"labels":null,"payload":null}`, "genai.inference_providers"},
		{"map", `{"inference_providers":[{"http":{"headers":null}}],"items":null,"labels":{},"payload":null}`, "genai.labels"},
		{"bytes", `{"inference_providers":[{"http":{"headers":null}}],"items":null,"labels":null,"payload":""}`, "genai.payload"},
		{"element", `{"inference_providers":[{"http":{"headers":[{"name":"X-Test","value":"changed"}]}}],"items":null,"labels":null,"payload":null}`, "genai.inference_providers"},
	} {
		t.Run(tc.name, func(t *testing.T) { testStoreDefaults(t, defaults, tc.document, []string{tc.path}) })
	}
}

func TestChangedHeaderValueRemainsDivergent(t *testing.T) {
	defaults := &rootconfig.Config{Providers: []rootconfig.ProviderConfig{{HTTP: rootconfig.HTTPProviderOptions{Headers: []rootconfig.Header{{Name: "X-Test", Value: "original"}}}}}}
	testStoreDefaults(t, defaults, `{"inference_providers":[{"http":{"headers":[{"name":"X-Test","value":"changed"}]}}],"items":null,"labels":null,"payload":null}`, []string{"genai.inference_providers"})
}

func testStoreDefaults(t *testing.T, defaults *rootconfig.Config, document string, wantPaths []string) {
	t.Helper()
	const namespace = "prod/collections"
	server, err := kmsclienttest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	server.SetParameterVersion(namespace, "groups/genai", document, "json", 1)
	if _, err := server.SetActiveRelease(kmsclienttest.ReleaseSpec{
		Namespace: namespace, Name: "runtime", Version: 1, SchemaVersion: 1,
		Entries: []kmsclienttest.ReleaseEntrySpec{{Alias: "genai", Kind: "parameter", Path: "groups/genai", Version: 1, ContentType: "json"}},
	}, 1); err != nil {
		t.Fatal(err)
	}
	client, err := kmsclient.NewClient(kmsclient.Config{Namespace: namespace, ClientName: "collection-test", DialOptions: server.DialOptions()})
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
	var calls atomic.Uint64
	reports := make(chan []string, 1)
	store, err := Start(ctx, client, Options{
		Release: "runtime", Defaults: func() *rootconfig.Config { return defaults },
		Callbacks: configstore.Callbacks{OnDefaultMismatch: func(report configstore.DefaultMismatchReport) {
			calls.Add(1)
			var paths []string
			for _, field := range report.Fields() {
				paths = append(paths, field.Path)
			}
			select {
			case reports <- paths:
			default:
			}
		}},
		InstanceID: "collection-test",
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
	wantDivergent := len(wantPaths) != 0
	if got := store.Stats().DefaultDivergent; got != wantDivergent {
		t.Fatalf("stats DefaultDivergent = %v, want %v", got, wantDivergent)
	}
	if got := store.Status().DefaultDivergent; got != wantDivergent {
		t.Fatalf("DefaultDivergent = %v, want %v", got, wantDivergent)
	}
	if wantDivergent {
		select {
		case paths := <-reports:
			if !reflect.DeepEqual(paths, wantPaths) {
				t.Fatalf("difference paths = %v, want %v", paths, wantPaths)
			}
		default:
			t.Fatal("missing mismatch report")
		}
	} else if got := calls.Load(); got != 0 {
		t.Fatalf("mismatch calls = %d, want 0", got)
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
