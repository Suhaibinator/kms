package kmsclient

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type applicationReleaseAdminStub struct {
	kmsv1.AdminServiceClient
	response *kmsv1.CreateApplicationReleaseResponse
	err      error
	calls    []*kmsv1.CreateApplicationReleaseRequest
	metadata metadata.MD
}

func (stub *applicationReleaseAdminStub) CreateApplicationRelease(
	ctx context.Context,
	request *kmsv1.CreateApplicationReleaseRequest,
	_ ...grpc.CallOption,
) (*kmsv1.CreateApplicationReleaseResponse, error) {
	stub.calls = append(stub.calls, proto.Clone(request).(*kmsv1.CreateApplicationReleaseRequest))
	stub.metadata, _ = metadata.FromOutgoingContext(ctx)
	return stub.response, stub.err
}

func newApplicationReleaseTestClient(stub kmsv1.AdminServiceClient) *Client {
	return &Client{admin: stub, cfg: Config{Token: "admin-token"}, timeout: time.Second}
}

func TestCreateApplicationReleasePreview(t *testing.T) {
	planDigest := strings.Repeat("a", 64)
	stub := &applicationReleaseAdminStub{response: &kmsv1.CreateApplicationReleaseResponse{
		Profile: "dev", PlanDigest: planDigest, Valid: true, ReleaseName: "runtime",
		SchemaVersion: 3, BaseReleaseVersion: 7,
		Entries: []*kmsv1.ApplicationReleasePlanEntry{
			{Alias: "runtime", Kind: "parameter", Ref: applicationReleaseRef("runtime"), FromVersion: 2, ToVersion: 3, Source: ApplicationReleaseSourceGeneratedDefault},
			{Alias: "database_password", Kind: "secret", Ref: &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: "prod", App: "shared-secrets"}, Key: "database-password"}, FromVersion: 5, ToVersion: 5, Source: ApplicationReleaseSourceCarriedActiveSecret},
		},
	}}
	client := newApplicationReleaseTestClient(stub)

	result, err := client.CreateApplicationRelease(context.Background(), CreateApplicationReleaseOptions{
		Namespace: "dev/gradethis", Artifact: []byte(`{"format":"kms-config-defaults/v1"}`), MetadataJSON: `{"commit":"abc"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile != "dev" || result.PlanDigest != planDigest || !result.Valid || result.Executed || result.Created ||
		result.ReleaseName != "runtime" || result.SchemaVersion != 3 || result.BaseReleaseVersion != 7 || result.Release != nil {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Entries) != 2 || result.Entries[0].Path != "/dev/gradethis/runtime" || result.Entries[1].Path != "/prod/shared-secrets/database-password" || result.Entries[1].Source != ApplicationReleaseSourceCarriedActiveSecret {
		t.Fatalf("entries = %#v", result.Entries)
	}
	if len(stub.calls) != 1 || stub.calls[0].GetNamespace().GetEnv() != "dev" || stub.calls[0].GetNamespace().GetApp() != "gradethis" ||
		stub.calls[0].GetExecute() || stub.calls[0].GetPlanDigest() != "" || stub.calls[0].GetMetadataJson() != `{"commit":"abc"}` {
		t.Fatalf("calls = %#v", stub.calls)
	}
	if got := stub.metadata.Get("authorization"); len(got) != 1 || got[0] != "Bearer admin-token" {
		t.Fatalf("authorization metadata = %v", got)
	}
}

func TestCreateApplicationReleaseExecuteReturnsIdempotentRelease(t *testing.T) {
	planDigest := strings.Repeat("b", 64)
	release := &kmsv1.ConfigurationRelease{
		Namespace: &kmsv1.NamespaceRef{Env: "dev", App: "gradethis"},
		Name:      "runtime", Version: 9, SchemaVersion: 3, MetadataJson: `{"commit":"abc"}`,
	}
	digest, err := deterministicReleaseDigest(release)
	if err != nil {
		t.Fatal(err)
	}
	release.Digest = digest
	stub := &applicationReleaseAdminStub{response: &kmsv1.CreateApplicationReleaseResponse{
		Profile: "dev", PlanDigest: planDigest, Valid: true, Executed: true, Created: false,
		ReleaseName: "runtime", SchemaVersion: 3, BaseReleaseVersion: 8, Release: release,
	}}
	client := newApplicationReleaseTestClient(stub)

	result, err := client.CreateApplicationRelease(context.Background(), CreateApplicationReleaseOptions{
		Namespace: "dev/gradethis", Artifact: []byte(`{}`), Execute: true, PlanDigest: planDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Executed || result.Created || result.Release == nil || result.Release.Name() != "runtime" || result.Release.Version() != 9 || result.Release.Digest() != digest {
		t.Fatalf("result = %#v", result)
	}
	if len(stub.calls) != 1 || !stub.calls[0].GetExecute() || stub.calls[0].GetPlanDigest() != planDigest {
		t.Fatalf("calls = %#v", stub.calls)
	}
}

func TestCreateApplicationReleaseRejectsInvalidInputsAndMapsErrors(t *testing.T) {
	stub := &applicationReleaseAdminStub{}
	client := newApplicationReleaseTestClient(stub)
	for name, options := range map[string]CreateApplicationReleaseOptions{
		"namespace":      {Namespace: "dev", Artifact: []byte(`{}`)},
		"artifact":       {Namespace: "dev/gradethis"},
		"preview digest": {Namespace: "dev/gradethis", Artifact: []byte(`{}`), PlanDigest: strings.Repeat("a", 64)},
		"execute digest": {Namespace: "dev/gradethis", Artifact: []byte(`{}`), Execute: true, PlanDigest: strings.Repeat("A", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.CreateApplicationRelease(context.Background(), options); err == nil {
				t.Fatal("invalid input succeeded")
			}
		})
	}
	if len(stub.calls) != 0 {
		t.Fatalf("invalid inputs made RPCs: %#v", stub.calls)
	}

	stub.err = status.Error(codes.PermissionDenied, "denied")
	_, err := client.CreateApplicationRelease(context.Background(), CreateApplicationReleaseOptions{
		Namespace: "dev/gradethis", Artifact: []byte(`{}`),
	})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("error = %v, want ErrPermissionDenied", err)
	}
}

func TestCreateApplicationReleaseRejectsMalformedResponses(t *testing.T) {
	planDigest := strings.Repeat("c", 64)
	validPreview := func() *kmsv1.CreateApplicationReleaseResponse {
		return &kmsv1.CreateApplicationReleaseResponse{
			Profile: "dev", PlanDigest: planDigest, Valid: true, ReleaseName: "runtime",
			Entries: []*kmsv1.ApplicationReleasePlanEntry{{
				Alias: "runtime", Kind: "parameter", Ref: applicationReleaseRef("runtime"), ToVersion: 1,
				Source: ApplicationReleaseSourceGeneratedDefault,
			}},
		}
	}
	tests := map[string]func(*kmsv1.CreateApplicationReleaseResponse){
		"uppercase digest":   func(response *kmsv1.CreateApplicationReleaseResponse) { response.PlanDigest = strings.Repeat("C", 64) },
		"execution mismatch": func(response *kmsv1.CreateApplicationReleaseResponse) { response.Executed = true },
		"preview release": func(response *kmsv1.CreateApplicationReleaseResponse) {
			response.Release = &kmsv1.ConfigurationRelease{}
		},
		"unknown source": func(response *kmsv1.CreateApplicationReleaseResponse) { response.Entries[0].Source = "active" },
		"source kind mismatch": func(response *kmsv1.CreateApplicationReleaseResponse) {
			response.Entries[0].Source = ApplicationReleaseSourceCarriedActiveSecret
		},
		"duplicate alias": func(response *kmsv1.CreateApplicationReleaseResponse) {
			response.Entries = append(response.Entries, proto.Clone(response.Entries[0]).(*kmsv1.ApplicationReleasePlanEntry))
		},
		"validation without code": func(response *kmsv1.CreateApplicationReleaseResponse) {
			response.Validation = []*kmsv1.ReleaseValidationError{{Alias: "runtime"}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			response := validPreview()
			mutate(response)
			client := newApplicationReleaseTestClient(&applicationReleaseAdminStub{response: response})
			if _, err := client.CreateApplicationRelease(context.Background(), CreateApplicationReleaseOptions{Namespace: "dev/gradethis", Artifact: []byte(`{}`)}); err == nil {
				t.Fatal("malformed response succeeded")
			}
		})
	}
}

func TestCreateApplicationReleaseExecuteRequiresValidRelease(t *testing.T) {
	planDigest := strings.Repeat("d", 64)
	for name, release := range map[string]*kmsv1.ConfigurationRelease{
		"missing": nil,
		"invalid": {Namespace: &kmsv1.NamespaceRef{Env: "dev", App: "gradethis"}, Name: "runtime", Version: 1, SchemaVersion: 3, Digest: strings.Repeat("e", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			stub := &applicationReleaseAdminStub{response: &kmsv1.CreateApplicationReleaseResponse{
				Profile: "dev", PlanDigest: planDigest, Valid: true, Executed: true,
				ReleaseName: "runtime", SchemaVersion: 3, Release: release,
			}}
			client := newApplicationReleaseTestClient(stub)
			if _, err := client.CreateApplicationRelease(context.Background(), CreateApplicationReleaseOptions{
				Namespace: "dev/gradethis", Artifact: []byte(`{}`), Execute: true, PlanDigest: planDigest,
			}); err == nil {
				t.Fatal("execute with invalid release succeeded")
			}
		})
	}
}

func applicationReleaseRef(key string) *kmsv1.ResourceRef {
	return &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: "dev", App: "gradethis"}, Key: key}
}
