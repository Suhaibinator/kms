package kmsclient

import (
	"context"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc"
)

type lifecycleRPCStub struct {
	kmsv1.ConfigurationReleaseServiceClient
	validation      *kmsv1.ValidateReleaseResponse
	activation      *kmsv1.ActivateReleaseResponse
	active          *kmsv1.GetActiveReleaseResponse
	request         *kmsv1.ActivateReleaseRequest
	validateRequest *kmsv1.ValidateReleaseRequest
}

func (s *lifecycleRPCStub) ValidateRelease(_ context.Context, r *kmsv1.ValidateReleaseRequest, _ ...grpc.CallOption) (*kmsv1.ValidateReleaseResponse, error) {
	s.validateRequest = r
	return s.validation, nil
}
func (s *lifecycleRPCStub) ActivateRelease(_ context.Context, r *kmsv1.ActivateReleaseRequest, _ ...grpc.CallOption) (*kmsv1.ActivateReleaseResponse, error) {
	s.request = r
	return s.activation, nil
}
func (s *lifecycleRPCStub) GetActiveRelease(context.Context, *kmsv1.GetActiveReleaseRequest, ...grpc.CallOption) (*kmsv1.GetActiveReleaseResponse, error) {
	return s.active, nil
}

func TestManagedReleaseValidationStrict(t *testing.T) {
	for _, response := range []*kmsv1.ValidateReleaseResponse{nil, {Valid: true, Errors: []*kmsv1.ReleaseValidationError{{Code: "bad"}}}, {Valid: false}, {Errors: []*kmsv1.ReleaseValidationError{nil}}} {
		client := &Client{releases: &lifecycleRPCStub{validation: response}, timeout: time.Second}
		if _, err := client.ValidateManagedRelease(context.Background(), ManagedReleaseTarget{Namespace: "dev/app", Name: "runtime", Version: 1, SchemaVersion: 7}); err == nil {
			t.Fatal("accepted invalid response")
		}
	}
	stub := &lifecycleRPCStub{validation: &kmsv1.ValidateReleaseResponse{Valid: true}}
	client := &Client{releases: stub, timeout: time.Second}
	result, err := client.ValidateManagedRelease(context.Background(), ManagedReleaseTarget{Namespace: "dev/app", Name: "runtime", Version: 4, SchemaVersion: 7})
	if err != nil || !result.Valid || stub.validateRequest.GetVersion() != 4 || stub.validateRequest.SchemaVersion == nil || stub.validateRequest.GetSchemaVersion() != 7 {
		t.Fatalf("result=%v err=%v request=%v", result, err, stub.validateRequest)
	}
}
func TestManagedReleaseActivationGuardAndMalformedResponses(t *testing.T) {
	stub := &lifecycleRPCStub{}
	client := &Client{releases: stub, timeout: time.Second}
	target := ManagedReleaseTarget{Namespace: "dev/app", Name: "runtime", Version: 4, SchemaVersion: 7}
	if _, err := client.ActivateManagedRelease(context.Background(), target, 0); err == nil {
		t.Fatal("accepted nil response")
	}
	if stub.request.ExpectedCurrentVersion == nil || *stub.request.ExpectedCurrentVersion != 0 || stub.request.GetSchemaVersion() != 7 {
		t.Fatal("missing explicit guard/schema")
	}
	if _, err := client.GetManagedReleaseActivation(context.Background(), target); err == nil {
		t.Fatal("accepted nil active response")
	}
	target.Version = 0
	stub.request = nil
	if _, err := client.ActivateManagedRelease(context.Background(), target, 0); err == nil || stub.request != nil {
		t.Fatal("invalid target sent")
	}
}

func TestManagedReleaseActivationSuccess(t *testing.T) {
	release := &kmsv1.ConfigurationRelease{Namespace: &kmsv1.NamespaceRef{Env: "dev", App: "app"}, Name: "runtime", Version: 4, SchemaVersion: 7}
	digest, err := deterministicReleaseDigest(release)
	if err != nil {
		t.Fatal(err)
	}
	release.Digest = digest
	stub := &lifecycleRPCStub{activation: &kmsv1.ActivateReleaseResponse{Release: release, CurrentVersion: 4, PreviousVersion: 3, ActivationRevision: 26, Changed: true}, active: &kmsv1.GetActiveReleaseResponse{Release: release, PreviousVersion: 3, ActivationRevision: 26}}
	client := &Client{releases: stub, timeout: time.Second}
	target := ManagedReleaseTarget{Namespace: "dev/app", Name: "runtime", Version: 4, SchemaVersion: 7}
	result, err := client.ActivateManagedRelease(context.Background(), target, 3)
	if err != nil || result.Version != 4 || stub.request.GetExpectedCurrentVersion() != 3 {
		t.Fatalf("result=%v err=%v", result, err)
	}
	active, err := client.GetManagedReleaseActivation(context.Background(), target)
	if err != nil || active.Version != 4 || active.ActivationRevision != 26 {
		t.Fatalf("active=%v err=%v", active, err)
	}
	stub.activation.Changed = false
	if _, err := client.ActivateManagedRelease(context.Background(), target, 4); err != nil {
		t.Fatal(err)
	}
	release.Namespace.App = "other"
	if _, err := client.GetManagedReleaseActivation(context.Background(), target); err == nil {
		t.Fatal("accepted other namespace")
	}
}
