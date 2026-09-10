package cli

import (
	"context"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc"
)

type releasePinCLIStub struct {
	kmsv1.UnimplementedConfigurationReleaseServiceServer
	calls       chan *kmsv1.SetReleasePinRequest
	validations chan *kmsv1.ValidateReleaseRequest
}

func (s *releasePinCLIStub) ValidateRelease(_ context.Context, r *kmsv1.ValidateReleaseRequest) (*kmsv1.ValidateReleaseResponse, error) {
	s.validations <- r
	return &kmsv1.ValidateReleaseResponse{Valid: true}, nil
}
func (s *releasePinCLIStub) SetReleasePin(_ context.Context, r *kmsv1.SetReleasePinRequest) (*kmsv1.InstanceReleaseTarget, error) {
	s.calls <- r
	return &kmsv1.InstanceReleaseTarget{Release: &kmsv1.ConfigurationRelease{Version: r.Version}, TargetRevision: 99, PinRevision: 99, Pinned: r.Version > 0}, nil
}
func TestReleasePinCLIUsesExactSessionAndGuard(t *testing.T) {
	for _, unpin := range []bool{false, true} {
		t.Run(map[bool]string{false: "pin", true: "unpin"}[unpin], func(t *testing.T) {
			stub := &releasePinCLIStub{calls: make(chan *kmsv1.SetReleasePinRequest, 1), validations: make(chan *kmsv1.ValidateReleaseRequest, 1)}
			c := newTestCLI()
			c.dialOverride = startStubGRPC(t, func(s *grpc.Server) { kmsv1.RegisterConfigurationReleaseServiceServer(s, stub) })
			args := []string{"release", "pin", "prod/app", "runtime", "7"}
			if unpin {
				args = []string{"release", "unpin", "prod/app", "runtime"}
			}
			args = append(args, "--schema-version", "2", "--expected-pin-revision", "42", "--identity", "workload", "--client", "api", "--instance", "stable", "--session", "process-uuid", "--yes", "--insecure", "--token", "test-token")
			if code := c.Run(args); code != 0 {
				t.Fatalf("code=%d stderr=%s", code, c.stderr())
			}
			r := <-stub.calls
			if r.Session.SessionId != "process-uuid" || r.Session.Identity != "workload" || r.Session.ClientName != "api" || r.Session.InstanceId != "stable" || r.Session.GetSchemaVersion() != 2 || r.GetExpectedPinRevision() != 42 || r.Session.Namespace.Env != "prod" {
				t.Fatalf("incorrect guard/scope: %v", r)
			}
			if unpin {
				if r.Version != 0 || len(stub.validations) != 0 {
					t.Fatal("unpin requested a release validation")
				}
			} else {
				v := <-stub.validations
				if v.Version != 7 || v.GetSchemaVersion() != 2 || r.Version != 7 {
					t.Fatal("pin failed to validate exact selected release")
				}
			}
		})
	}
}
