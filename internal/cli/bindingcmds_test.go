package cli

import (
	"context"
	"encoding/base64"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

const (
	testOldBindingKey = "old-binding-key-material-32-bytes!"
	testNewBindingKey = "new-binding-key-material-32-bytes!"
)

type bindingSecretStub struct {
	kmsv1.UnimplementedSecretServiceServer
	metadata      *kmsv1.GetSecretMetadataResponse
	previewResp   *kmsv1.SecretBindingCohortResponse
	transition    *kmsv1.SecretVersionTransitionResponse
	cohort        *kmsv1.SecretBindingCohortResponse
	setPreview    *kmsv1.SecretVersionSetResponse
	setResult     *kmsv1.SecretVersionSetResponse
	bindReq       *kmsv1.BindSecretRequest
	unbindReq     *kmsv1.UnbindSecretRequest
	previewReq    *kmsv1.PreviewSecretBindingCohortRequest
	rotateReq     *kmsv1.RotateSecretBindingKeyRequest
	purgeReq      *kmsv1.PurgeSecretBindingCohortRequest
	setPurgeReq   *kmsv1.PurgeSecretUnboundVersionsRequest
	previewAt     time.Time
	mutationAt    time.Time
	previewEnd    <-chan struct{}
	mutationEnd   <-chan struct{}
	previewOff    bool
	bindErr       error
	unbindErr     error
	previewErr    error
	rotateErr     error
	purgeErr      error
	metadataErr   error
	setPreviewErr error
	setPurgeErr   error
}

func (s *bindingSecretStub) GetSecretMetadata(_ context.Context, _ *kmsv1.GetSecretMetadataRequest) (*kmsv1.GetSecretMetadataResponse, error) {
	return s.metadata, s.metadataErr
}

func (s *bindingSecretStub) BindSecret(_ context.Context, req *kmsv1.BindSecretRequest) (*kmsv1.SecretVersionTransitionResponse, error) {
	s.bindReq = req
	return s.transition, s.bindErr
}

func (s *bindingSecretStub) UnbindSecret(_ context.Context, req *kmsv1.UnbindSecretRequest) (*kmsv1.SecretVersionTransitionResponse, error) {
	s.unbindReq = req
	return s.transition, s.unbindErr
}

func (s *bindingSecretStub) PreviewSecretBindingCohort(ctx context.Context, req *kmsv1.PreviewSecretBindingCohortRequest) (*kmsv1.SecretBindingCohortResponse, error) {
	s.previewReq = req
	s.previewAt, _ = ctx.Deadline()
	s.previewEnd = ctx.Done()
	return s.previewResp, s.previewErr
}

func (s *bindingSecretStub) RotateSecretBindingKey(ctx context.Context, req *kmsv1.RotateSecretBindingKeyRequest) (*kmsv1.SecretVersionTransitionResponse, error) {
	s.rotateReq = req
	s.mutationAt, _ = ctx.Deadline()
	s.mutationEnd = ctx.Done()
	select {
	case <-s.previewEnd:
		s.previewOff = true
	default:
	}
	return s.transition, s.rotateErr
}

func (s *bindingSecretStub) PreviewSecretUnboundVersions(_ context.Context, _ *kmsv1.PreviewSecretUnboundVersionsRequest) (*kmsv1.SecretVersionSetResponse, error) {
	return s.setPreview, s.setPreviewErr
}

func (s *bindingSecretStub) PurgeSecretUnboundVersions(_ context.Context, req *kmsv1.PurgeSecretUnboundVersionsRequest) (*kmsv1.SecretVersionSetResponse, error) {
	s.setPurgeReq = req
	return s.setResult, s.setPurgeErr
}

func bindingMetadata(version uint64) *kmsv1.GetSecretMetadataResponse {
	return bindingMetadataFor("api-key", version)
}

func bindingMetadataFor(key string, version uint64) *kmsv1.GetSecretMetadataResponse {
	return &kmsv1.GetSecretMetadataResponse{Secret: &kmsv1.SecretMetadata{
		Ref:    &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: "prod", App: "app"}, Key: key},
		Labels: map[string]uint64{"current": version},
	}}
}

func (s *bindingSecretStub) PurgeSecretBindingCohort(ctx context.Context, req *kmsv1.PurgeSecretBindingCohortRequest) (*kmsv1.SecretBindingCohortResponse, error) {
	s.purgeReq = req
	s.mutationAt, _ = ctx.Deadline()
	return s.cohort, s.purgeErr
}

func newBindingCLI(t *testing.T, stub *bindingSecretStub) *testCLI {
	t.Helper()
	c := newTestCLI()
	c.dialOverride = startStubGRPC(t, func(server *grpc.Server) {
		kmsv1.RegisterSecretServiceServer(server, stub)
	})
	return c
}

func TestBindingKeyGenerateWritesOnlyOneRawKey(t *testing.T) {
	c := newTestCLI()
	if code := c.Run([]string{"binding-key", "generate"}); code != exitOK {
		t.Fatalf("exit = %d, stderr=%s", code, c.stderr())
	}
	if c.stderr() != "" {
		t.Fatalf("stderr = %q, want empty", c.stderr())
	}
	if strings.Count(c.stdout(), "\n") != 1 || !strings.HasSuffix(c.stdout(), "\n") {
		t.Fatalf("stdout = %q, want exactly one newline-terminated key", c.stdout())
	}
	encoded := strings.TrimSuffix(c.stdout(), "\n")
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("generated key is not unpadded Base64URL: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("generated key decoded to %d bytes, want 32", len(decoded))
	}
}

func TestSecretTransitionRejectsMalformedServerResponse(t *testing.T) {
	for _, tc := range []struct {
		name string
		resp *kmsv1.SecretVersionTransitionResponse
	}{
		{name: "nil"},
		{name: "zero current", resp: &kmsv1.SecretVersionTransitionResponse{PreviousVersion: 1, Revision: 1}},
		{name: "zero previous", resp: &kmsv1.SecretVersionTransitionResponse{CurrentVersion: 2, Revision: 1}},
		{name: "zero revision", resp: &kmsv1.SecretVersionTransitionResponse{CurrentVersion: 2, PreviousVersion: 1}},
		{name: "current equals previous", resp: &kmsv1.SecretVersionTransitionResponse{CurrentVersion: 2, PreviousVersion: 2, Revision: 1}},
		{name: "current below previous", resp: &kmsv1.SecretVersionTransitionResponse{CurrentVersion: 1, PreviousVersion: 2, Revision: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCLI()
			if code := c.printSecretTransition("Bound", "/prod/app/key", tc.resp); code == exitOK {
				t.Fatalf("malformed response succeeded: %+v", tc.resp)
			}
			if !strings.Contains(c.stderr(), "invalid secret transition response") {
				t.Fatalf("stderr = %q", c.stderr())
			}
		})
	}
}

func TestSecretBindAndUnbindUseOpaqueEnvironmentKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		get  func(*bindingSecretStub) string
	}{
		{name: "bind", args: []string{"secret", "bind", "/prod/app/api-key"}, get: func(s *bindingSecretStub) string { return s.bindReq.GetBindingKey() }},
		{name: "unbind", args: []string{"secret", "unbind", "/prod/app/api-key"}, get: func(s *bindingSecretStub) string { return s.unbindReq.GetBindingKey() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &bindingSecretStub{
				metadata:   bindingMetadata(7),
				transition: &kmsv1.SecretVersionTransitionResponse{CurrentVersion: 8, PreviousVersion: 7, Revision: 19},
			}
			c := newBindingCLI(t, stub)
			c.lookupEnv = mapLookup(map[string]string{bindingKeyEnv: testOldBindingKey})
			c.readPassword = func(int) ([]byte, error) {
				t.Fatal("environment-supplied key unexpectedly prompted")
				return nil, nil
			}
			args := append(tc.args, "--insecure", "--token", "identity")
			if code := c.Run(args); code != exitOK {
				t.Fatalf("exit = %d, stderr=%s", code, c.stderr())
			}
			if got := tc.get(stub); got != testOldBindingKey {
				t.Fatalf("request binding key = %q", got)
			}
			if got := stub.bindReq; tc.name == "bind" && got.GetExpectedCurrentVersion() != 7 {
				t.Fatalf("expected current version = %d", got.GetExpectedCurrentVersion())
			}
			if got := stub.unbindReq; tc.name == "unbind" && got.GetExpectedCurrentVersion() != 7 {
				t.Fatalf("expected current version = %d", got.GetExpectedCurrentVersion())
			}
			if strings.Contains(c.stdout()+c.stderr(), testOldBindingKey) {
				t.Fatal("command output leaked the binding key")
			}
		})
	}
}

func TestBindingKeyRotateRejectsRemovedVersionFlag(t *testing.T) {
	stub := &bindingSecretStub{metadata: bindingMetadata(5)}
	c := newBindingCLI(t, stub)
	c.lookupEnv = mapLookup(map[string]string{bindingKeyEnv: testOldBindingKey, newBindingKeyEnv: testNewBindingKey})
	code := c.Run([]string{"binding-key", "rotate", "/prod/app/api-key", "--version", "5", "--insecure"})
	if code != exitUsage {
		t.Fatalf("exit = %d, want removed flag rejected; stderr=%s", code, c.stderr())
	}
	if stub.rotateReq != nil {
		t.Fatal("invalid rotation still mutated")
	}
}

func TestNoBindingKeyFlagsOrFileConventionExist(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "put flag", args: []string{"put-secret", "/prod/app/key", "--binding-key", testOldBindingKey}},
		{name: "get file", args: []string{"get-secret", "/prod/app/key", "--binding-key-file", "key.txt"}},
		{name: "bind file", args: []string{"secret", "bind", "/prod/app/key", "--binding-key-file", "key.txt"}},
		{name: "rotate flag", args: []string{"binding-key", "rotate", "/prod/app/key", "--new-binding-key", testNewBindingKey}},
		{name: "purge file", args: []string{"secret", "purge-binding-cohort", "/prod/app/key", "--binding-key-file", "key.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCLI()
			if code := c.Run(tc.args); code != exitUsage {
				t.Fatalf("exit = %d, want removed flag rejected; stderr=%s", code, c.stderr())
			}
		})
	}
}

func TestSecretBindPromptsWithoutEchoAndConfirmsNewKey(t *testing.T) {
	stub := &bindingSecretStub{metadata: bindingMetadata(3), transition: &kmsv1.SecretVersionTransitionResponse{CurrentVersion: 4, PreviousVersion: 3, Revision: 4}}
	c := newBindingCLI(t, stub)
	c.isTTY = func() bool { return true }
	c.Stdin = openPromptInput(t, "")
	reads := [][]byte{[]byte(testNewBindingKey), []byte(testNewBindingKey)}
	c.readPassword = func(int) ([]byte, error) {
		if len(reads) == 0 {
			t.Fatal("unexpected password read")
		}
		value := slices.Clone(reads[0])
		reads = reads[1:]
		return value, nil
	}
	if code := c.Run([]string{"secret", "bind", "/prod/app/api-key", "--insecure"}); code != exitOK {
		t.Fatalf("exit = %d, stderr=%s", code, c.stderr())
	}
	if len(reads) != 0 {
		t.Fatalf("password reads remaining = %d, want both entry and confirmation consumed", len(reads))
	}
	if !strings.Contains(c.stderr(), "New binding key for /prod/app/api-key:") || !strings.Contains(c.stderr(), "Confirm new binding key for /prod/app/api-key:") {
		t.Fatalf("stderr did not carry separate non-echoing prompts: %q", c.stderr())
	}
	if strings.Contains(c.stdout()+c.stderr(), testNewBindingKey) {
		t.Fatal("prompted binding key was echoed")
	}
}

func TestBindingKeyRotateReadsCurrentAndSendsGuard(t *testing.T) {
	stub := &bindingSecretStub{
		metadata:   bindingMetadata(5),
		transition: &kmsv1.SecretVersionTransitionResponse{CurrentVersion: 6, PreviousVersion: 5, Revision: 72},
	}
	c := newBindingCLI(t, stub)
	c.lookupEnv = mapLookup(map[string]string{bindingKeyEnv: testOldBindingKey, newBindingKeyEnv: testNewBindingKey})
	code := c.Run([]string{"binding-key", "rotate", "/prod/app/api-key", "--insecure", "--token", "identity"})
	if code != exitOK {
		t.Fatalf("exit = %d, stderr=%s", code, c.stderr())
	}
	if stub.rotateReq.GetBindingKey() != testOldBindingKey || stub.rotateReq.GetNewBindingKey() != testNewBindingKey {
		t.Fatalf("rotate request carried the wrong credentials")
	}
	if stub.rotateReq.GetExpectedCurrentVersion() != 5 {
		t.Fatalf("rotate current guard = %d", stub.rotateReq.GetExpectedCurrentVersion())
	}
	if !strings.Contains(c.stdout(), "new version 6") || !strings.Contains(c.stdout(), "previous version 5 is unchanged") {
		t.Fatalf("transition output = %q", c.stdout())
	}
	if strings.Contains(c.stdout()+c.stderr(), testOldBindingKey) || strings.Contains(c.stdout()+c.stderr(), testNewBindingKey) {
		t.Fatal("rotate output leaked a binding key")
	}
}

func TestBindingKeyRotateSendsUnchangedReplacementToServer(t *testing.T) {
	stub := &bindingSecretStub{
		metadata:   bindingMetadata(5),
		transition: &kmsv1.SecretVersionTransitionResponse{CurrentVersion: 6, PreviousVersion: 5, Revision: 73},
	}
	c := newBindingCLI(t, stub)
	c.lookupEnv = mapLookup(map[string]string{
		bindingKeyEnv:    testOldBindingKey,
		newBindingKeyEnv: testOldBindingKey,
	})
	code := c.Run([]string{"binding-key", "rotate", "/prod/app/api-key", "--insecure"})
	if code != exitOK {
		t.Fatalf("exit = %d, want success; stderr=%s", code, c.stderr())
	}
	if stub.rotateReq == nil {
		t.Fatal("unchanged replacement did not reach the server")
	}
	if stub.rotateReq.GetBindingKey() != testOldBindingKey || stub.rotateReq.GetNewBindingKey() != testOldBindingKey {
		t.Fatal("unchanged replacement was not forwarded verbatim")
	}
	if strings.Contains(c.stdout()+c.stderr(), testOldBindingKey) {
		t.Fatal("rotate output leaked the binding key")
	}
}

func TestBindingKeyRotatePromptsOldThenConfirmsNewTwice(t *testing.T) {
	stub := &bindingSecretStub{
		metadata:   bindingMetadata(2),
		transition: &kmsv1.SecretVersionTransitionResponse{CurrentVersion: 3, PreviousVersion: 2, Revision: 11},
	}
	c := newBindingCLI(t, stub)
	c.isTTY = func() bool { return true }
	c.Stdin = openPromptInput(t, "")
	reads := [][]byte{[]byte(testOldBindingKey), []byte(testNewBindingKey), []byte(testNewBindingKey)}
	c.readPassword = func(int) ([]byte, error) {
		if len(reads) == 0 {
			t.Fatal("unexpected password read")
		}
		value := slices.Clone(reads[0])
		reads = reads[1:]
		return value, nil
	}
	if code := c.Run([]string{"binding-key", "rotate", "/prod/app/api-key", "--insecure"}); code != exitOK {
		t.Fatalf("exit = %d, stderr=%s", code, c.stderr())
	}
	if len(reads) != 0 {
		t.Fatalf("password reads remaining = %d", len(reads))
	}
	for _, prompt := range []string{
		"Current binding key for /prod/app/api-key:",
		"New binding key for /prod/app/api-key:",
		"Confirm new binding key for /prod/app/api-key:",
	} {
		if !strings.Contains(c.stderr(), prompt) {
			t.Fatalf("stderr missing %q: %s", prompt, c.stderr())
		}
	}
	if stub.rotateReq.GetBindingKey() != testOldBindingKey || stub.rotateReq.GetNewBindingKey() != testNewBindingKey {
		t.Fatal("prompted old/new keys were not mapped separately")
	}
	if strings.Contains(c.stdout()+c.stderr(), testOldBindingKey) || strings.Contains(c.stdout()+c.stderr(), testNewBindingKey) {
		t.Fatal("prompted rotation keys were echoed")
	}
}

func TestPurgeBindingCohortIsExplicitAndCASGuarded(t *testing.T) {
	stub := &bindingSecretStub{
		previewResp: &kmsv1.SecretBindingCohortResponse{AnchorVersion: 4, AffectedVersions: []uint64{4, 5}, Revision: 80},
		cohort:      &kmsv1.SecretBindingCohortResponse{AnchorVersion: 4, AffectedVersions: []uint64{4, 5}, Revision: 81},
	}
	c := newBindingCLI(t, stub)
	c.lookupEnv = mapLookup(map[string]string{bindingKeyEnv: testOldBindingKey})
	code := c.Run([]string{"secret", "purge-binding-cohort", "/prod/app/api-key", "--version", "4", "--yes", "--insecure", "--token", "admin"})
	if code != exitOK {
		t.Fatalf("exit = %d, stderr=%s", code, c.stderr())
	}
	if stub.purgeReq.GetExpectedRevision() != 80 || !slices.Equal(stub.purgeReq.GetExpectedAffectedVersions(), []uint64{4, 5}) {
		t.Fatalf("purge CAS guards = revision %d, versions %v", stub.purgeReq.GetExpectedRevision(), stub.purgeReq.GetExpectedAffectedVersions())
	}
	for _, text := range []string{"IRREVERSIBLE ADMIN OPERATION", "permanently destroyed", "versions 4, 5"} {
		if !strings.Contains(c.stderr(), text) {
			t.Fatalf("purge warning missing %q: %s", text, c.stderr())
		}
	}
	if strings.Contains(c.stdout()+c.stderr(), testOldBindingKey) {
		t.Fatal("purge output leaked the binding key")
	}
}

func TestPurgeUnboundVersionsPreviewsWarnsAndSendsExactGuard(t *testing.T) {
	stub := &bindingSecretStub{
		setPreview: &kmsv1.SecretVersionSetResponse{AffectedVersions: []uint64{1, 3, 7}, Revision: 80},
		setResult:  &kmsv1.SecretVersionSetResponse{AffectedVersions: []uint64{1, 3, 7}, Revision: 81},
	}
	c := newBindingCLI(t, stub)
	code := c.Run([]string{"secret", "purge-unbound-versions", "/prod/app/api-key", "--yes", "--insecure", "--token", "admin"})
	if code != exitOK {
		t.Fatalf("exit = %d, stderr=%s", code, c.stderr())
	}
	if stub.setPurgeReq.GetExpectedRevision() != 80 || !slices.Equal(stub.setPurgeReq.GetExpectedAffectedVersions(), []uint64{1, 3, 7}) {
		t.Fatalf("purge guard = revision %d, versions %v", stub.setPurgeReq.GetExpectedRevision(), stub.setPurgeReq.GetExpectedAffectedVersions())
	}
	for _, text := range []string{"IRREVERSIBLE ADMIN OPERATION", "unbound versions 1, 3, 7", "immutable releases"} {
		if !strings.Contains(c.stderr(), text) {
			t.Fatalf("purge warning missing %q: %s", text, c.stderr())
		}
	}
}

func TestPurgePreviewValidationRejectsMalformedPeerResponses(t *testing.T) {
	validCohort := &kmsv1.SecretBindingCohortResponse{
		AnchorVersion: 2, AffectedVersions: []uint64{1, 2, 3}, Revision: 9,
	}
	validSet := &kmsv1.SecretVersionSetResponse{
		AffectedVersions: []uint64{1, 3, 7}, Revision: 9,
	}
	if err := validateCohortPreview(validCohort); err != nil {
		t.Fatalf("valid cohort rejected: %v", err)
	}
	if err := validateVersionSet(validSet); err != nil {
		t.Fatalf("valid version set rejected: %v", err)
	}

	cohorts := []struct {
		name string
		resp *kmsv1.SecretBindingCohortResponse
	}{
		{name: "nil", resp: nil},
		{name: "zero anchor", resp: &kmsv1.SecretBindingCohortResponse{AffectedVersions: []uint64{1}, Revision: 1}},
		{name: "zero revision", resp: &kmsv1.SecretBindingCohortResponse{AnchorVersion: 1, AffectedVersions: []uint64{1}}},
		{name: "empty set", resp: &kmsv1.SecretBindingCohortResponse{AnchorVersion: 1, Revision: 1}},
		{name: "zero version", resp: &kmsv1.SecretBindingCohortResponse{AnchorVersion: 1, AffectedVersions: []uint64{0, 1}, Revision: 1}},
		{name: "unsorted", resp: &kmsv1.SecretBindingCohortResponse{AnchorVersion: 1, AffectedVersions: []uint64{2, 1}, Revision: 1}},
		{name: "duplicate", resp: &kmsv1.SecretBindingCohortResponse{AnchorVersion: 1, AffectedVersions: []uint64{1, 1}, Revision: 1}},
		{name: "anchor omitted", resp: &kmsv1.SecretBindingCohortResponse{AnchorVersion: 2, AffectedVersions: []uint64{1, 3}, Revision: 1}},
	}
	for _, tc := range cohorts {
		t.Run("cohort/"+tc.name, func(t *testing.T) {
			if err := validateCohortPreview(tc.resp); err == nil {
				t.Fatal("malformed cohort preview was accepted")
			}
		})
	}

	sets := []struct {
		name string
		resp *kmsv1.SecretVersionSetResponse
	}{
		{name: "nil", resp: nil},
		{name: "zero revision", resp: &kmsv1.SecretVersionSetResponse{AffectedVersions: []uint64{1}}},
		{name: "empty set", resp: &kmsv1.SecretVersionSetResponse{Revision: 1}},
		{name: "zero version", resp: &kmsv1.SecretVersionSetResponse{AffectedVersions: []uint64{0, 1}, Revision: 1}},
		{name: "unsorted", resp: &kmsv1.SecretVersionSetResponse{AffectedVersions: []uint64{2, 1}, Revision: 1}},
		{name: "duplicate", resp: &kmsv1.SecretVersionSetResponse{AffectedVersions: []uint64{1, 1}, Revision: 1}},
	}
	for _, tc := range sets {
		t.Run("set/"+tc.name, func(t *testing.T) {
			if err := validateVersionSet(tc.resp); err == nil {
				t.Fatal("malformed version-set preview was accepted")
			}
		})
	}
}

func TestBindingCommandsRequireEnvironmentOnNonInteractiveInput(t *testing.T) {
	stub := &bindingSecretStub{metadata: bindingMetadata(1)}
	c := newBindingCLI(t, stub)
	code := c.Run([]string{"secret", "unbind", "/prod/app/api-key", "--insecure"})
	if code != exitUsage {
		t.Fatalf("exit = %d, want usage; stderr=%s", code, c.stderr())
	}
	if !strings.Contains(c.stderr(), bindingKeyEnv+" is required") {
		t.Fatalf("stderr = %q", c.stderr())
	}
	if stub.unbindReq != nil {
		t.Fatal("RPC ran without a binding key")
	}
}

func openPromptInput(t *testing.T, contents string) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "prompt-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(contents); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}
