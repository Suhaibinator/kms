package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

const (
	reflectedAccessToken = "kmss_reflected-access-token-canary"
	reflectedPlaintext   = "reflected-secret-plaintext-canary"
)

func hostileSecretRPCError(code codes.Code) error {
	return status.Error(code, strings.Join([]string{
		testOldBindingKey,
		testNewBindingKey,
		reflectedAccessToken,
		reflectedPlaintext,
	}, " | "))
}

func assertSecretCanariesRedacted(t *testing.T, output string) {
	t.Helper()
	for _, secret := range []string{testOldBindingKey, testNewBindingKey, reflectedAccessToken, reflectedPlaintext} {
		if strings.Contains(output, secret) {
			t.Fatalf("output reflected secret material %q: %s", secret, output)
		}
	}
	if !strings.Contains(output, secretRPCFailureMessage) {
		t.Fatalf("output = %q, want fixed secret-operation failure", output)
	}
}

func TestSecretRPCErrorRedactionPreservesClassification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		wantCode codes.Code
		wantExit int
	}{
		{name: "permission denied", err: hostileSecretRPCError(codes.PermissionDenied), wantCode: codes.PermissionDenied, wantExit: exitPermissionDenied},
		{name: "failed precondition", err: hostileSecretRPCError(codes.FailedPrecondition), wantCode: codes.FailedPrecondition, wantExit: exitFailedPrecondition},
		{name: "unavailable", err: hostileSecretRPCError(codes.Unavailable), wantCode: codes.Unavailable, wantExit: exitUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSecretRPCError(tc.err)
			if code := status.Code(got); code != tc.wantCode {
				t.Fatalf("status code = %s, want %s", code, tc.wantCode)
			}
			if code := exitCodeFor(got); code != tc.wantExit {
				t.Fatalf("exit code = %d, want %d", code, tc.wantExit)
			}
			assertSecretCanariesRedacted(t, got.Error())
		})
	}

	plain := errors.New(reflectedPlaintext)
	if got := redactSecretRPCError(plain); got.Error() != secretRPCFailureMessage {
		t.Fatalf("non-status error = %q, want fixed text", got)
	}
}

func TestPurgeCleanupPendingRequiresExactStatusAndPurgeCall(t *testing.T) {
	exact := status.Error(codes.Unavailable, purgeCleanupPendingWireMessage)
	got := redactPurgeSecretRPCError(exact)
	if status.Code(got) != codes.Unavailable || status.Convert(got).Message() != purgeCleanupPendingWireMessage {
		t.Fatalf("exact cleanup-pending error = %v", got)
	}
	if exitCodeFor(got) != exitUnavailable {
		t.Fatalf("cleanup-pending exit = %d, want %d", exitCodeFor(got), exitUnavailable)
	}

	for _, tc := range []struct {
		name string
		err  error
		mapf func(error) error
	}{
		{name: "same status on a non-purge call", err: exact, mapf: redactSecretRPCError},
		{name: "wrong code", err: status.Error(codes.Internal, purgeCleanupPendingWireMessage), mapf: redactPurgeSecretRPCError},
		{name: "message suffix", err: status.Error(codes.Unavailable, purgeCleanupPendingWireMessage+" "+reflectedPlaintext), mapf: redactPurgeSecretRPCError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mapped := tc.mapf(tc.err)
			if status.Convert(mapped).Message() != secretRPCFailureMessage {
				t.Fatalf("mapped message = %q, want fixed failure", status.Convert(mapped).Message())
			}
			if strings.Contains(mapped.Error(), reflectedPlaintext) {
				t.Fatalf("mapped error reflected plaintext: %v", mapped)
			}
		})
	}
}

func TestBindingLifecycleCommandsRedactHostileRemoteDetails(t *testing.T) {
	preview := &kmsv1.SecretBindingCohortResponse{AnchorVersion: 5, AffectedVersions: []uint64{4, 5}, Revision: 71}
	remoteErr := hostileSecretRPCError(codes.PermissionDenied)
	for _, tc := range []struct {
		name string
		args []string
		set  func(*bindingSecretStub)
		env  map[string]string
	}{
		{name: "bind", args: []string{"secret", "bind", "/prod/app/key", "--insecure"}, set: func(s *bindingSecretStub) { s.bindErr = remoteErr }, env: map[string]string{bindingKeyEnv: testOldBindingKey}},
		{name: "unbind", args: []string{"secret", "unbind", "/prod/app/key", "--insecure"}, set: func(s *bindingSecretStub) { s.unbindErr = remoteErr }, env: map[string]string{bindingKeyEnv: testOldBindingKey}},
		{name: "rotate preview", args: []string{"binding-key", "rotate", "/prod/app/key", "--yes", "--insecure"}, set: func(s *bindingSecretStub) { s.previewErr = remoteErr }, env: map[string]string{bindingKeyEnv: testOldBindingKey}},
		{name: "rotate", args: []string{"binding-key", "rotate", "/prod/app/key", "--yes", "--insecure"}, set: func(s *bindingSecretStub) { s.rotateErr = remoteErr }, env: map[string]string{bindingKeyEnv: testOldBindingKey, newBindingKeyEnv: testNewBindingKey}},
		{name: "purge preview", args: []string{"secret", "purge-binding-cohort", "/prod/app/key", "--yes", "--insecure"}, set: func(s *bindingSecretStub) { s.previewErr = remoteErr }, env: map[string]string{bindingKeyEnv: testOldBindingKey}},
		{name: "purge", args: []string{"secret", "purge-binding-cohort", "/prod/app/key", "--yes", "--insecure"}, set: func(s *bindingSecretStub) { s.purgeErr = remoteErr }, env: map[string]string{bindingKeyEnv: testOldBindingKey}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &bindingSecretStub{previewResp: preview}
			tc.set(stub)
			c := newBindingCLI(t, stub)
			c.lookupEnv = mapLookup(tc.env)
			if code := c.Run(tc.args); code != exitPermissionDenied {
				t.Fatalf("exit = %d, want %d; stderr=%s", code, exitPermissionDenied, c.stderr())
			}
			assertSecretCanariesRedacted(t, c.stdout()+c.stderr())
		})
	}
}

func TestPurgeCommandSurfacesOnlyCanonicalCleanupPendingMessage(t *testing.T) {
	stub := &bindingSecretStub{
		previewResp: &kmsv1.SecretBindingCohortResponse{AnchorVersion: 5, AffectedVersions: []uint64{4, 5}, Revision: 71},
		purgeErr:    status.Error(codes.Unavailable, purgeCleanupPendingWireMessage),
	}
	c := newBindingCLI(t, stub)
	c.lookupEnv = mapLookup(map[string]string{bindingKeyEnv: testOldBindingKey})
	if code := c.Run([]string{"secret", "purge-binding-cohort", "/prod/app/key", "--yes", "--insecure"}); code != exitUnavailable {
		t.Fatalf("exit = %d, want %d; stderr=%s", code, exitUnavailable, c.stderr())
	}
	if !strings.Contains(c.stderr(), purgeCleanupPendingWireMessage) {
		t.Fatalf("stderr = %q, want canonical cleanup-pending message", c.stderr())
	}
	if strings.Contains(c.stdout()+c.stderr(), testOldBindingKey) {
		t.Fatalf("cleanup-pending output reflected binding key: %s", c.stderr())
	}
}

func TestPutAndGetSecretRedactHostileRemoteDetails(t *testing.T) {
	remoteErr := hostileSecretRPCError(codes.PermissionDenied)
	t.Run("put", func(t *testing.T) {
		secrets := &secretStub{err: remoteErr}
		c := newConvenienceCLI(t, &parameterStub{}, secrets)
		c.lookupEnv = mapLookup(map[string]string{bindingKeyEnv: testNewBindingKey})
		valueFile := filepath.Join(t.TempDir(), "value")
		if err := os.WriteFile(valueFile, []byte(reflectedPlaintext), 0o600); err != nil {
			t.Fatal(err)
		}
		if code := c.Run([]string{"put-secret", "/prod/app/key", "--value-file", valueFile, "--insecure"}); code != exitPermissionDenied {
			t.Fatalf("exit = %d, want %d; stderr=%s", code, exitPermissionDenied, c.stderr())
		}
		assertSecretCanariesRedacted(t, c.stdout()+c.stderr())
	})

	t.Run("get", func(t *testing.T) {
		secrets := &secretStub{
			metadataResp: &kmsv1.GetSecretMetadataResponse{Secret: &kmsv1.SecretMetadata{
				Ref: ref("prod", "app", "key"), Labels: map[string]uint64{"current": 7},
				Versions: []*kmsv1.SecretVersionInfo{{Version: 7, State: "enabled", Bound: true, HasAccessToken: true}},
			}},
			readErr: remoteErr,
		}
		c := newConvenienceCLI(t, &parameterStub{}, secrets)
		c.lookupEnv = mapLookup(map[string]string{bindingKeyEnv: testOldBindingKey})
		if code := c.Run([]string{"get-secret", "/prod/app/key", "--secret-token", reflectedAccessToken, "--insecure"}); code != exitPermissionDenied {
			t.Fatalf("exit = %d, want %d; stderr=%s", code, exitPermissionDenied, c.stderr())
		}
		assertSecretCanariesRedacted(t, c.stdout()+c.stderr())
	})
}

func TestBulkSecretReadsRedactHostileRemoteDetails(t *testing.T) {
	for _, command := range []string{"env", "exec"} {
		t.Run(command, func(t *testing.T) {
			f := newExecFixture(t, 0, nil)
			f.secrets.getErr["/prod/app/stripe-key"] = hostileSecretRPCError(codes.PermissionDenied)
			var code int
			if command == "env" {
				code = f.run("--secret-token", "stripe-key="+reflectedAccessToken)
			} else {
				code = f.runExec([]string{"--secret-token", "stripe-key=" + reflectedAccessToken}, "must-not-launch")
				if f.launched.called {
					t.Fatal("exec launched after a secret resolution failure")
				}
			}
			if code != exitPermissionDenied {
				t.Fatalf("exit = %d, want %d; stderr=%s", code, exitPermissionDenied, f.stderr())
			}
			if !strings.Contains(f.stderr(), "/prod/app/stripe-key") {
				t.Fatalf("stderr = %q, want safe secret path", f.stderr())
			}
			assertSecretCanariesRedacted(t, f.stdout()+f.stderr())
		})
	}
}
