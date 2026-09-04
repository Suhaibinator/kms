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
	previewResp *kmsv1.SecretBindingCohortResponse
	mutation    *kmsv1.SecretVersionMutationResponse
	cohort      *kmsv1.SecretBindingCohortResponse
	bindReq     *kmsv1.BindSecretRequest
	unbindReq   *kmsv1.UnbindSecretRequest
	previewReq  *kmsv1.PreviewSecretBindingCohortRequest
	rotateReq   *kmsv1.RotateSecretBindingKeyRequest
	purgeReq    *kmsv1.PurgeSecretBindingCohortRequest
	previewAt   time.Time
	mutationAt  time.Time
	bindErr     error
	unbindErr   error
	previewErr  error
	rotateErr   error
	purgeErr    error
}

func (s *bindingSecretStub) BindSecret(_ context.Context, req *kmsv1.BindSecretRequest) (*kmsv1.SecretVersionMutationResponse, error) {
	s.bindReq = req
	return s.mutation, s.bindErr
}

func (s *bindingSecretStub) UnbindSecret(_ context.Context, req *kmsv1.UnbindSecretRequest) (*kmsv1.SecretVersionMutationResponse, error) {
	s.unbindReq = req
	return s.mutation, s.unbindErr
}

func (s *bindingSecretStub) PreviewSecretBindingCohort(ctx context.Context, req *kmsv1.PreviewSecretBindingCohortRequest) (*kmsv1.SecretBindingCohortResponse, error) {
	s.previewReq = req
	s.previewAt, _ = ctx.Deadline()
	return s.previewResp, s.previewErr
}

func (s *bindingSecretStub) RotateSecretBindingKey(ctx context.Context, req *kmsv1.RotateSecretBindingKeyRequest) (*kmsv1.SecretBindingCohortResponse, error) {
	s.rotateReq = req
	s.mutationAt, _ = ctx.Deadline()
	return s.cohort, s.rotateErr
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

func TestSecretBindAndUnbindUseOpaqueEnvironmentKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		get  func(*bindingSecretStub) string
	}{
		{name: "bind", args: []string{"secret", "bind", "/prod/app/api-key", "--version", "7"}, get: func(s *bindingSecretStub) string { return s.bindReq.GetBindingKey() }},
		{name: "unbind", args: []string{"secret", "unbind", "/prod/app/api-key", "--version", "7"}, get: func(s *bindingSecretStub) string { return s.unbindReq.GetBindingKey() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &bindingSecretStub{mutation: &kmsv1.SecretVersionMutationResponse{AnchorVersion: 7, AffectedVersions: []uint64{7}, Revision: 19}}
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
			if strings.Contains(c.stdout()+c.stderr(), testOldBindingKey) {
				t.Fatal("command output leaked the binding key")
			}
		})
	}
}

func TestBindingKeyRotateDeclinedPreviewDoesNotMutate(t *testing.T) {
	stub := &bindingSecretStub{previewResp: &kmsv1.SecretBindingCohortResponse{
		AnchorVersion: 5, AffectedVersions: []uint64{4, 5}, Revision: 71,
	}}
	c := newBindingCLI(t, stub)
	c.lookupEnv = mapLookup(map[string]string{bindingKeyEnv: testOldBindingKey, newBindingKeyEnv: testNewBindingKey})
	c.isTTY = func() bool { return true }
	c.Stdin = openPromptInput(t, "n\n")
	code := c.Run([]string{"binding-key", "rotate", "/prod/app/api-key", "--version", "5", "--insecure"})
	if code != exitUsage {
		t.Fatalf("exit = %d, want declined confirmation usage exit; stderr=%s", code, c.stderr())
	}
	if stub.previewReq == nil {
		t.Fatal("rotation was not previewed")
	}
	if stub.rotateReq != nil {
		t.Fatal("declined rotation still mutated")
	}
	if !strings.Contains(c.stderr(), "affected versions: 4, 5") || !strings.Contains(c.stderr(), "aborted") {
		t.Fatalf("stderr = %q", c.stderr())
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
	stub := &bindingSecretStub{mutation: &kmsv1.SecretVersionMutationResponse{AnchorVersion: 3, AffectedVersions: []uint64{3}, Revision: 4}}
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
	if code := c.Run([]string{"secret", "bind", "/prod/app/api-key", "--version", "3", "--insecure"}); code != exitOK {
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

func TestBindingKeyRotatePreviewsConfirmsAndSendsCASGuards(t *testing.T) {
	stub := &bindingSecretStub{
		previewResp: &kmsv1.SecretBindingCohortResponse{AnchorVersion: 5, AffectedVersions: []uint64{4, 5}, Revision: 71},
		cohort:      &kmsv1.SecretBindingCohortResponse{AnchorVersion: 5, AffectedVersions: []uint64{4, 5}, Revision: 72},
	}
	c := newBindingCLI(t, stub)
	c.lookupEnv = mapLookup(map[string]string{bindingKeyEnv: testOldBindingKey, newBindingKeyEnv: testNewBindingKey})
	code := c.Run([]string{"binding-key", "rotate", "/prod/app/api-key", "--version", "5", "--yes", "--insecure", "--token", "identity"})
	if code != exitOK {
		t.Fatalf("exit = %d, stderr=%s", code, c.stderr())
	}
	if stub.previewReq.GetAnchorVersion() != 5 || stub.previewReq.GetBindingKey() != testOldBindingKey {
		t.Fatalf("preview request = %+v", stub.previewReq)
	}
	if stub.rotateReq.GetBindingKey() != testOldBindingKey || stub.rotateReq.GetNewBindingKey() != testNewBindingKey {
		t.Fatalf("rotate request carried the wrong credentials")
	}
	if stub.rotateReq.ExpectedRevision == nil || stub.rotateReq.GetExpectedRevision() != 71 || !slices.Equal(stub.rotateReq.GetExpectedAffectedVersions(), []uint64{4, 5}) {
		t.Fatalf("rotate CAS guards = revision %v, versions %v", stub.rotateReq.ExpectedRevision, stub.rotateReq.GetExpectedAffectedVersions())
	}
	if stub.previewAt.IsZero() || stub.mutationAt.IsZero() || !stub.mutationAt.After(stub.previewAt) {
		t.Fatalf("preview deadline = %v, mutation deadline = %v; want a fresh post-confirmation deadline", stub.previewAt, stub.mutationAt)
	}
	if !strings.Contains(c.stderr(), "affected versions: 4, 5") {
		t.Fatalf("preview omitted exact versions: %q", c.stderr())
	}
	if strings.Contains(c.stdout()+c.stderr(), testOldBindingKey) || strings.Contains(c.stdout()+c.stderr(), testNewBindingKey) {
		t.Fatal("rotate output leaked a binding key")
	}
}

func TestBindingKeyRotateRejectsUnchangedReplacementBeforeMutation(t *testing.T) {
	stub := &bindingSecretStub{previewResp: &kmsv1.SecretBindingCohortResponse{
		AnchorVersion: 5, AffectedVersions: []uint64{4, 5}, Revision: 71,
	}}
	c := newBindingCLI(t, stub)
	c.lookupEnv = mapLookup(map[string]string{
		bindingKeyEnv:    testOldBindingKey,
		newBindingKeyEnv: testOldBindingKey,
	})
	code := c.Run([]string{"binding-key", "rotate", "/prod/app/api-key", "--version", "5", "--yes", "--insecure"})
	if code != exitUsage {
		t.Fatalf("exit = %d, want usage; stderr=%s", code, c.stderr())
	}
	if stub.previewReq == nil {
		t.Fatal("rotation did not validate the old key through preview")
	}
	if stub.rotateReq != nil {
		t.Fatal("no-op rotation made a mutation RPC")
	}
	if !strings.Contains(c.stderr(), "new binding key must differ from current binding key") || strings.Contains(c.stdout()+c.stderr(), testOldBindingKey) {
		t.Fatalf("unsafe or missing no-op error: %q", c.stderr())
	}
}

func TestBindingKeyRotatePromptsOldThenConfirmsNewTwice(t *testing.T) {
	stub := &bindingSecretStub{
		previewResp: &kmsv1.SecretBindingCohortResponse{AnchorVersion: 2, AffectedVersions: []uint64{2}, Revision: 10},
		cohort:      &kmsv1.SecretBindingCohortResponse{AnchorVersion: 2, AffectedVersions: []uint64{2}, Revision: 11},
	}
	c := newBindingCLI(t, stub)
	c.isTTY = func() bool { return true }
	c.Stdin = openPromptInput(t, "yes\n")
	reads := [][]byte{[]byte(testOldBindingKey), []byte(testNewBindingKey), []byte(testNewBindingKey)}
	c.readPassword = func(int) ([]byte, error) {
		if len(reads) == 0 {
			t.Fatal("unexpected password read")
		}
		value := slices.Clone(reads[0])
		reads = reads[1:]
		return value, nil
	}
	if code := c.Run([]string{"binding-key", "rotate", "/prod/app/api-key", "--version", "2", "--insecure"}); code != exitOK {
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
	if stub.purgeReq.ExpectedRevision == nil || stub.purgeReq.GetExpectedRevision() != 80 || !slices.Equal(stub.purgeReq.GetExpectedAffectedVersions(), []uint64{4, 5}) {
		t.Fatalf("purge CAS guards = revision %v, versions %v", stub.purgeReq.ExpectedRevision, stub.purgeReq.GetExpectedAffectedVersions())
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

func TestBindingCommandsRequireEnvironmentOnNonInteractiveInput(t *testing.T) {
	stub := &bindingSecretStub{}
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
