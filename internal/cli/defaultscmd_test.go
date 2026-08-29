package cli

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type defaultsAdminStub struct {
	kmsv1.UnimplementedAdminServiceServer

	mu        sync.Mutex
	calls     []*kmsv1.ApplyApplicationDefaultsRequest
	auth      []string
	responses []*kmsv1.ApplyApplicationDefaultsResponse
	errs      []error
}

func (s *defaultsAdminStub) ApplyApplicationDefaults(ctx context.Context, req *kmsv1.ApplyApplicationDefaultsRequest) (*kmsv1.ApplyApplicationDefaultsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copyReq := &kmsv1.ApplyApplicationDefaultsRequest{
		Namespace: &kmsv1.NamespaceRef{Env: req.GetNamespace().GetEnv(), App: req.GetNamespace().GetApp()},
		Artifact:  append([]byte(nil), req.GetArtifact()...), Overwrite: req.GetOverwrite(),
		Execute: req.GetExecute(), PlanDigest: req.GetPlanDigest(), UpdateDefinition: req.GetUpdateDefinition(),
	}
	s.calls = append(s.calls, copyReq)
	md, _ := metadata.FromIncomingContext(ctx)
	s.auth = append(s.auth, strings.Join(md.Get("authorization"), ","))
	index := len(s.calls) - 1
	if index < len(s.errs) && s.errs[index] != nil {
		return nil, s.errs[index]
	}
	if index >= len(s.responses) {
		return nil, status.Error(codes.Internal, "unexpected call")
	}
	return s.responses[index], nil
}

func startDefaultsAdminServer(t *testing.T, stub *defaultsAdminStub) dialFunc {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	kmsv1.RegisterAdminServiceServer(server, stub)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return func(*connFlags) (*grpc.ClientConn, error) {
		return grpc.NewClient("passthrough:///defaults-test",
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return listener.Dial()
			}),
		)
	}
}

func writeDefaultsInput(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "defaults.json")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func defaultsPreviewResponse() *kmsv1.ApplyApplicationDefaultsResponse {
	return &kmsv1.ApplyApplicationDefaultsResponse{
		Profile: "dev", PlanDigest: "plan-123",
		Entries: []*kmsv1.DefaultsApplyEntry{
			{Alias: "zeta", Key: "config/zeta", ContentType: "application/json", Status: "unchanged", CurrentVersion: 3},
			{Alias: "alpha", Key: "config/alpha", ContentType: "text/plain", Status: "create"},
		},
		MissingSecrets: []string{"z-secret", "a-secret"},
	}
}

func TestDefaultsApplyPreviewFromFileIsValueFreeAndAuthenticated(t *testing.T) {
	const artifact = `{"not-even-parsed-by-the-cli":"parameter-value-must-never-render"}`
	stub := &defaultsAdminStub{responses: []*kmsv1.ApplyApplicationDefaultsResponse{defaultsPreviewResponse()}}
	dial := startDefaultsAdminServer(t, stub)
	c := newTestCLI()
	c.dialOverride = dial
	code := c.Run([]string{
		"defaults", "apply", "dev/my-app", "--from", writeDefaultsInput(t, artifact),
		"--overwrite", "--update-definition", "--insecure", "--token", "admin-token",
	})
	if code != 0 {
		t.Fatalf("preview exit = %d, stderr=%s", code, c.stderr())
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(stub.calls))
	}
	request := stub.calls[0]
	if request.GetExecute() || !request.GetOverwrite() || !request.GetUpdateDefinition() || request.GetPlanDigest() != "" {
		t.Fatalf("preview request = %#v", request)
	}
	if got := string(request.GetArtifact()); got != artifact {
		t.Fatalf("artifact changed by CLI: %q", got)
	}
	if request.GetNamespace().GetEnv() != "dev" || request.GetNamespace().GetApp() != "my-app" {
		t.Fatalf("namespace = %#v", request.GetNamespace())
	}
	if stub.auth[0] != "Bearer admin-token" {
		t.Fatalf("authorization metadata = %q", stub.auth[0])
	}
	output := c.stdout()
	if strings.Contains(output, "parameter-value-must-never-render") || strings.Contains(output, "not-even-parsed") {
		t.Fatalf("preview leaked artifact data: %s", output)
	}
	for _, want := range []string{
		"Preview defaults", "Profile: dev", "Plan digest: plan-123",
		"create", "unchanged", "alpha", "zeta",
		"Summary: create=1 unchanged=1 update=0 blocked=0",
		"Missing secrets: a-secret, z-secret",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("preview missing %q:\n%s", want, output)
		}
	}
	if strings.Index(output, "alpha") > strings.Index(output, "zeta") {
		t.Fatalf("entries are not deterministically sorted:\n%s", output)
	}
}

func TestDefaultsApplyExecuteReadsStdinAndUsesFreshPlan(t *testing.T) {
	preview := defaultsPreviewResponse()
	applied := defaultsPreviewResponse()
	applied.Executed = true
	applied.Entries[0].AppliedVersion = 3
	applied.Entries[1].AppliedVersion = 1
	stub := &defaultsAdminStub{responses: []*kmsv1.ApplyApplicationDefaultsResponse{preview, applied}}
	dial := startDefaultsAdminServer(t, stub)

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	const artifact = "raw-defaults-artifact"
	if _, err := write.WriteString(artifact); err != nil {
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	c := newTestCLI()
	c.Stdin = read
	c.dialOverride = dial
	code := c.Run([]string{
		"defaults", "apply", "dev/app", "--from", "-", "--execute", "--overwrite",
		"--insecure",
	})
	if code != 0 {
		t.Fatalf("execute exit = %d, stderr=%s", code, c.stderr())
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.calls) != 2 {
		t.Fatalf("calls = %d, want preview plus execute", len(stub.calls))
	}
	first, second := stub.calls[0], stub.calls[1]
	if first.GetExecute() || !second.GetExecute() {
		t.Fatalf("execute flags = %v, %v", first.GetExecute(), second.GetExecute())
	}
	if second.GetPlanDigest() != preview.GetPlanDigest() {
		t.Fatalf("execute digest = %q, want %q", second.GetPlanDigest(), preview.GetPlanDigest())
	}
	if !first.GetOverwrite() || !second.GetOverwrite() {
		t.Fatal("overwrite was not preserved across preview and execute")
	}
	if string(first.GetArtifact()) != artifact || string(second.GetArtifact()) != artifact {
		t.Fatal("artifact was not preserved across preview and execute")
	}
	if strings.Count(c.stdout(), "Plan digest: plan-123") != 2 || !strings.Contains(c.stdout(), "Applied defaults") {
		t.Fatalf("execute output = %s", c.stdout())
	}
}

func TestDefaultsApplyExecuteNoOpSucceeds(t *testing.T) {
	preview := &kmsv1.ApplyApplicationDefaultsResponse{
		Profile: "dev", PlanDigest: "no-op-plan",
		Entries: []*kmsv1.DefaultsApplyEntry{{Alias: "settings", Key: "config/settings", Status: "unchanged", CurrentVersion: 2}},
	}
	applied := &kmsv1.ApplyApplicationDefaultsResponse{
		Profile: "dev", PlanDigest: "no-op-plan", Executed: true,
		Entries: []*kmsv1.DefaultsApplyEntry{{Alias: "settings", Key: "config/settings", Status: "unchanged", CurrentVersion: 2}},
	}
	stub := &defaultsAdminStub{responses: []*kmsv1.ApplyApplicationDefaultsResponse{preview, applied}}
	dial := startDefaultsAdminServer(t, stub)
	c := newTestCLI()
	c.dialOverride = dial
	if code := c.Run([]string{"defaults", "apply", "dev/app", "--from", writeDefaultsInput(t, "{}"), "--execute", "--insecure"}); code != 0 {
		t.Fatalf("no-op execute exit = %d, stderr=%s", code, c.stderr())
	}
}

func TestDefaultsApplyBlockedPreviewDoesNotExecute(t *testing.T) {
	preview := defaultsPreviewResponse()
	preview.Entries[0].Status = "blocked"
	stub := &defaultsAdminStub{responses: []*kmsv1.ApplyApplicationDefaultsResponse{preview}}
	dial := startDefaultsAdminServer(t, stub)
	c := newTestCLI()
	c.dialOverride = dial
	code := c.Run([]string{"defaults", "apply", "dev/app", "--from", writeDefaultsInput(t, "{}"), "--execute", "--insecure"})
	if code != 1 || !strings.Contains(c.stderr(), "blocked") {
		t.Fatalf("blocked exit=%d stderr=%s", code, c.stderr())
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.calls) != 1 {
		t.Fatalf("blocked preview made %d calls, want 1", len(stub.calls))
	}
}

func TestDefaultsApplyProductionConfirmation(t *testing.T) {
	artifact := writeDefaultsInput(t, "{}")
	tests := []struct {
		name string
		env  string
		args []string
		want int
	}{
		{name: "production preview needs no confirmation", env: "prod", want: 0},
		{name: "production execute requires confirmation", env: "prod", args: []string{"--execute"}, want: 2},
		{name: "production mismatch", env: "prod", args: []string{"--execute", "--confirm-production", "production"}, want: 2},
		{name: "non-production confirmation rejected", env: "dev", args: []string{"--confirm-production", "dev"}, want: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &defaultsAdminStub{responses: []*kmsv1.ApplyApplicationDefaultsResponse{defaultsPreviewResponse()}}
			dial := startDefaultsAdminServer(t, stub)
			c := newTestCLI()
			c.dialOverride = dial
			args := []string{"defaults", "apply", test.env + "/app", "--from", artifact, "--insecure"}
			args = append(args, test.args...)
			if code := c.Run(args); code != test.want {
				t.Fatalf("exit=%d want=%d stderr=%s", code, test.want, c.stderr())
			}
		})
	}

	preview := defaultsPreviewResponse()
	applied := defaultsPreviewResponse()
	applied.Executed = true
	stub := &defaultsAdminStub{responses: []*kmsv1.ApplyApplicationDefaultsResponse{preview, applied}}
	dial := startDefaultsAdminServer(t, stub)
	c := newTestCLI()
	c.dialOverride = dial
	if code := c.Run([]string{
		"defaults", "apply", "prod/app", "--from", artifact, "--execute", "--confirm-production", "prod",
		"--insecure",
	}); code != 0 {
		t.Fatalf("confirmed production execute exit=%d stderr=%s", code, c.stderr())
	}
}

func TestDefaultsApplyRejectsUsageErrorsWithExitTwo(t *testing.T) {
	tests := [][]string{
		{"defaults"},
		{"defaults", "unknown"},
		{"defaults", "apply"},
		{"defaults", "apply", "dev/app"},
		{"defaults", "apply", "bad-target", "--from", "x"},
		{"defaults", "apply", "dev/app", "--from", "x", "--unknown"},
	}
	for _, args := range tests {
		c := newTestCLI()
		if code := c.Run(args); code != 2 {
			t.Errorf("Run(%v) exit=%d want=2 stderr=%s", args, code, c.stderr())
		}
	}
	c := newTestCLI()
	if code := c.Run([]string{"defaults", "apply", "--help"}); code != 0 {
		t.Fatalf("help exit=%d", code)
	}
}

func TestDefaultsApplyHandlesServerAndMalformedResponses(t *testing.T) {
	tests := []struct {
		name     string
		response *kmsv1.ApplyApplicationDefaultsResponse
		err      error
		want     string
	}{
		{name: "malformed artifact rejected by server", err: status.Error(codes.InvalidArgument, "invalid defaults artifact"), want: "invalid defaults artifact"},
		{name: "authentication failure", err: status.Error(codes.Unauthenticated, "missing credentials"), want: "missing credentials"},
		{name: "empty plan digest", response: &kmsv1.ApplyApplicationDefaultsResponse{Profile: "dev"}, want: "missing plan digest"},
		{name: "unknown status", response: &kmsv1.ApplyApplicationDefaultsResponse{PlanDigest: "plan", Entries: []*kmsv1.DefaultsApplyEntry{{Alias: "a", Key: "a", Status: "mystery"}}}, want: "unknown status"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &defaultsAdminStub{responses: []*kmsv1.ApplyApplicationDefaultsResponse{test.response}, errs: []error{test.err}}
			dial := startDefaultsAdminServer(t, stub)
			c := newTestCLI()
			c.dialOverride = dial
			code := c.Run([]string{"defaults", "apply", "dev/app", "--from", writeDefaultsInput(t, "malformed artifact"), "--insecure"})
			if code != 1 || !strings.Contains(c.stderr(), test.want) {
				t.Fatalf("exit=%d stderr=%s, want %q", code, c.stderr(), test.want)
			}
		})
	}
}

func TestReadDefaultsArtifactIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "too-large.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(make([]byte, configstore.MaxDefaultsArtifactSize+1)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	c := newTestCLI()
	if _, err := c.readDefaultsArtifact(path); err == nil || !strings.Contains(err.Error(), "4 MiB") {
		t.Fatalf("oversized artifact error = %v", err)
	}
	if _, err := c.readDefaultsArtifact(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing artifact error = %v", err)
	}
}
