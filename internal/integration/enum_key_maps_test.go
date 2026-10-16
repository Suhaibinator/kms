package integration

import (
	"context"
	"encoding/json/v2"
	"reflect"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	rootconfig "github.com/Suhaibinator/kms/internal/configgen/testdata/enums"
	fixturekms "github.com/Suhaibinator/kms/internal/configgen/testdata/enumsgenerated"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Exercise the generated propertyNames constraint through production schema
// validation and TLS/gRPC, then load accepted documents with the generated SDK.
func TestEnumKeyMapsOverRealKMS(t *testing.T) {
	env := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	auth := networkAuthContext(ctx, env.adminToken)
	principal := core.Principal{Identity: domain.Identity{Name: "network-root", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken}
	const application = "enum-key-maps"
	const releaseName = "runtime"
	_, schema, err := env.svc.CreateApplicationWithSchema(ctx, principal,
		domain.Application{Name: application, ReleaseName: releaseName}, string(fixturekms.GeneratedSchema()), "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.CreateNamespace(ctx, principal, domain.NamespaceRef{Env: "prod", App: application}, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatal(err)
	}
	groups, err := fixturekms.EncodeParameterGroups(rootconfig.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	parameters := kmsv1.NewParameterServiceClient(env.adminConn)
	releases := kmsv1.NewConfigurationReleaseServiceClient(env.adminConn)
	put := func(alias, document string) uint64 {
		t.Helper()
		result, err := parameters.PutParameter(auth, &kmsv1.PutParameterRequest{
			Ref: networkRef("prod", application, "groups/"+alias), Value: document, ContentType: "json",
		})
		if err != nil {
			t.Fatal(err)
		}
		return result.GetVersion()
	}
	planVersion := put("plan", string(groups["plan"]))
	client, err := kmsclient.NewClient(kmsclient.Config{
		Endpoint: env.endpoint(), Namespace: "prod/" + application, Token: env.adminToken,
		TLS: env.clientTLS(nil), Timeout: 3 * time.Second, ClientName: "enum-key-integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	route := map[string]any{"adapter": "http", "tier": nil}
	var activeVersion uint64
	for _, tc := range []struct {
		name    string
		entries any
		valid   bool
	}{
		{"canonical zero and sparse members", map[string]any{"TIER_UNSPECIFIED": route, "TIER_PRO": route}, true},
		{"empty map", map[string]any{}, true},
		{"null map", nil, true},
		{"unknown member", map[string]any{"TIER_PRIVATE_UNKNOWN": route}, false},
		{"alias member", map[string]any{"TIER_PROFESSIONAL": route}, false},
		{"numeric spelling", map[string]any{"0": route}, false},
		{"old lowercase spelling", map[string]any{"tier_pro": route}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var routing map[string]any
			if err := json.Unmarshal(groups["routing"], &routing); err != nil {
				t.Fatal(err)
			}
			routing["tier_routes"] = tc.entries
			document, err := json.Marshal(routing, json.Deterministic(true))
			if err != nil {
				t.Fatal(err)
			}
			routingVersion := put("routing", string(document))
			created, err := releases.CreateRelease(auth, &kmsv1.CreateReleaseRequest{
				Namespace: networkNS("prod", application), Name: releaseName, SchemaVersion: &schema.Version,
				Entries: []*kmsv1.ReleaseEntrySelector{
					{Alias: "plan", Kind: "parameter", Ref: networkRef("prod", application, "groups/plan"), Version: planVersion},
					{Alias: "routing", Kind: "parameter", Ref: networkRef("prod", application, "groups/routing"), Version: routingVersion},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			version := created.GetRelease().GetVersion()
			validation, err := releases.ValidateRelease(auth, &kmsv1.ValidateReleaseRequest{
				Namespace: networkNS("prod", application), Name: releaseName, SchemaVersion: &schema.Version, Version: version,
			})
			if err != nil || validation.GetValid() != tc.valid {
				t.Fatalf("schema validity = %v, %v; want %t", validation, err, tc.valid)
			}
			_, err = releases.ActivateRelease(auth, &kmsv1.ActivateReleaseRequest{
				Namespace: networkNS("prod", application), Name: releaseName, SchemaVersion: &schema.Version,
				Version: version, ExpectedCurrentVersion: &activeVersion,
			})
			if !tc.valid {
				if status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("invalid-key activation error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			activeVersion = version
			storeCtx, stop := context.WithCancel(ctx)
			store, err := fixturekms.Start(storeCtx, client, fixturekms.Options{
				Release: releaseName, Defaults: rootconfig.Defaults,
				Callbacks: configstore.Callbacks{OnDefaultMismatch: func(configstore.DefaultMismatchReport) {}},
			})
			if err != nil {
				stop()
				t.Fatalf("generated runtime rejected schema-accepted map: %v", err)
			}
			defer func() {
				stop()
				if err := store.Wait(); err != nil {
					t.Error(err)
				}
			}()
			decoded := store.Current().Config()
			encoded, err := fixturekms.EncodeParameterGroups(decoded)
			if err != nil {
				t.Fatal(err)
			}
			var roundTrip map[string]any
			if err := json.Unmarshal(encoded["routing"], &roundTrip); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(roundTrip["tier_routes"], tc.entries) {
				t.Fatalf("typed SDK round trip changed enum-key map: %s", encoded["routing"])
			}
		})
	}
}
