package kmsclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSecretCredentialsMapToIndependentRequestFields(t *testing.T) {
	client, server := newTestClient(t, Config{CacheTTL: 10})
	server.SetSecret(testNS, "credentials", []byte("value"))

	secret, err := client.GetSecret(context.Background(), "credentials",
		WithSecretToken("access-token"), WithBindingKey("binding-key"))
	if err != nil {
		t.Fatal(err)
	}
	if got := server.LastSecretToken("GetSecret"); got != "access-token" {
		t.Fatalf("secret token = %q", got)
	}
	if got := server.LastBindingKey("GetSecret"); got != "binding-key" {
		t.Fatalf("binding key = %q", got)
	}
	if secret.BindKey != "" {
		t.Fatal("fetched Secret retained the request binding key")
	}
}

func TestSecretRPCErrorCannotReflectBindingKey(t *testing.T) {
	client, server := newTestClient(t, Config{})
	const bindingKey = "binding-key-reflected-by-buggy-peer"
	server.SetSecretError(testNS, "error", status.Error(codes.PermissionDenied, "rejected "+bindingKey))
	_, err := client.GetSecret(context.Background(), "error", WithBindingKey(bindingKey))
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("error = %v, want ErrPermissionDenied", err)
	}
	if strings.Contains(err.Error(), bindingKey) {
		t.Fatalf("error reflected binding key: %v", err)
	}
}

func TestEverySecretBearingRPCDiscardsRemoteDiagnosticText(t *testing.T) {
	client, server := newTestClient(t, Config{})
	server.SetSecretVersion(testNS, "redaction", []byte("value"), "text/plain", 1)
	key := strings.Repeat("k", 32)
	newKey := strings.Repeat("n", 32)
	validCohortGuard := SecretBindingCohortResult{AnchorVersion: 1, Revision: 1, AffectedVersions: []uint64{1}}
	calls := []struct {
		method string
		call   func() error
	}{
		{method: "GetSecret", call: func() error {
			_, err := client.GetSecret(context.Background(), "redaction", WithBindingKey(key))
			return err
		}},
		{method: "PutSecret", call: func() error {
			_, err := client.PutSecret(context.Background(), "redaction", []byte("value"), WithPutBindingKey(key))
			return err
		}},
		{method: "BindSecret", call: func() error { _, err := client.BindSecret(context.Background(), "redaction", 1, key); return err }},
		{method: "UnbindSecret", call: func() error { _, err := client.UnbindSecret(context.Background(), "redaction", 1, key); return err }},
		{method: "PreviewSecretBindingCohort", call: func() error {
			_, err := client.PreviewSecretBindingCohort(context.Background(), "redaction", 1, key)
			return err
		}},
		{method: "RotateSecretBindingKey", call: func() error {
			_, err := client.RotateSecretBindingKey(context.Background(), "redaction", 1, key, newKey)
			return err
		}},
		{method: "PurgeSecretBindingCohort", call: func() error {
			_, err := client.PurgeSecretBindingCohortIfUnchanged(context.Background(), "redaction", 1, key, validCohortGuard)
			return err
		}},
		{method: "PreviewSecretUnboundVersions", call: func() error {
			_, err := client.PreviewSecretUnboundVersions(context.Background(), "redaction")
			return err
		}},
		{method: "PurgeSecretUnboundVersions", call: func() error {
			_, err := client.PurgeSecretUnboundVersions(context.Background(), "redaction", SecretVersionSetResult{Revision: 1, AffectedVersions: []uint64{1}})
			return err
		}},
	}
	for _, test := range calls {
		t.Run(test.method, func(t *testing.T) {
			canary := "remote-reflected-" + test.method + "-" + key
			server.SetSecretOperationError(test.method, testNS, "redaction", status.Error(codes.Internal, canary))
			err := test.call()
			if err == nil || strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), key) {
				t.Fatalf("mapped error leaked remote diagnostic: %v", err)
			}
		})
	}
}

func TestSecretErrorCanonicalizesWrappedContext(t *testing.T) {
	const canary = "binding-key-from-interceptor"
	for _, test := range []struct {
		name string
		base error
	}{
		{name: "canceled", base: context.Canceled},
		{name: "deadline", base: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := mapSecretError(fmt.Errorf("%s: %w", canary, test.base))
			if got != test.base || !errors.Is(got, test.base) {
				t.Fatalf("mapped error = %#v, want canonical %v", got, test.base)
			}
			if strings.Contains(got.Error(), canary) {
				t.Fatalf("mapped context error leaked credential: %v", got)
			}
		})
	}
}

func TestPurgeCleanupPendingHasDistinctSanitizedSentinel(t *testing.T) {
	client, server := newTestClient(t, Config{})
	server.SetSecretVersion(testNS, "cleanup-pending", []byte("value"), "text/plain", 1)
	server.SetSecretVersionCredentials(testNS, "cleanup-pending", 1, "", strings.Repeat("k", 32))
	server.SetSecretOperationError("PurgeSecretBindingCohort", testNS, "cleanup-pending",
		status.Error(codes.Unavailable, purgeCleanupPendingWireMessage))

	guard := SecretBindingCohortResult{AnchorVersion: 1, Revision: 1, AffectedVersions: []uint64{1}}
	result, err := client.PurgeSecretBindingCohortIfUnchanged(context.Background(), "cleanup-pending", 1, strings.Repeat("k", 32), guard)
	if result.AnchorVersion != 0 || len(result.AffectedVersions) != 0 || result.Revision != 0 || !errors.Is(err, ErrPurgeCleanupPending) {
		t.Fatalf("purge result=%+v error=%v, want zero result and ErrPurgeCleanupPending", result, err)
	}
	if err.Error() != ErrPurgeCleanupPending.Error() {
		t.Fatalf("purge error = %q, want fixed sentinel text", err)
	}

	server.SetSecretOperationError("PurgeSecretBindingCohort", testNS, "cleanup-pending",
		status.Error(codes.Unavailable, purgeCleanupPendingWireMessage+" reflected-key"))
	_, err = client.PurgeSecretBindingCohortIfUnchanged(context.Background(), "cleanup-pending", 1, "reflected-key", guard)
	if errors.Is(err, ErrPurgeCleanupPending) || strings.Contains(err.Error(), "reflected-key") {
		t.Fatalf("non-canonical unavailable error was trusted or leaked: %v", err)
	}
}

func TestSecretValueSendsBothCredentialsAndEnvSkipsKMS(t *testing.T) {
	client, server := newTestClient(t, Config{})
	server.SetSecret(testNS, "declarative", []byte("value"))
	value := SecretValue{Key: "declarative", Token: "access-token", BindKey: "binding-key"}
	if err := value.InitContext(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	if server.LastSecretToken("GetSecret") != "access-token" || server.LastBindingKey("GetSecret") != "binding-key" {
		t.Fatal("SecretValue did not send independent credentials")
	}

	client2, server2 := newTestClient(t, Config{})
	t.Setenv("KMSCLIENT_BINDING_TEST_OVERRIDE", "from-env")
	override := SecretValue{Key: "declarative", Token: "token-must-not-be-used", BindKey: "key-must-not-be-used", EnvVar: "KMSCLIENT_BINDING_TEST_OVERRIDE"}
	if err := override.InitContext(context.Background(), client2); err != nil {
		t.Fatal(err)
	}
	if server2.LastMetadata("GetSecret") != nil {
		t.Fatal("environment override contacted KMS")
	}
}

func TestSecretMetadataPreservesExactVersionProtection(t *testing.T) {
	client, server := newTestClient(t, Config{})
	server.SetSecretVersion(testNS, "metadata", []byte("value"), "text/plain", 7)
	server.SetSecretVersionMetadata(testNS, "metadata", 7, "disabled", true, true, 1234)

	metadata, err := client.GetSecretMetadata(context.Background(), "metadata")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Path != "/"+testNS+"/metadata" || !metadata.Bound || !metadata.HasAccessToken || len(metadata.Versions) != 1 {
		t.Fatalf("metadata = %+v", metadata)
	}
	version := metadata.Versions[0]
	if version.Version != 7 || version.State != "disabled" || !version.Bound || !version.HasAccessToken || version.ExpiresAtUnixMS != 1234 {
		t.Fatalf("version metadata = %+v", version)
	}
	metadata.Labels["current"] = 99
	again, err := client.GetSecretMetadata(context.Background(), "metadata")
	if err != nil {
		t.Fatal(err)
	}
	if again.Labels["current"] != 7 {
		t.Fatal("metadata labels alias server state")
	}
}

func TestSecretBindingManagementRequestMapping(t *testing.T) {
	client, server := newTestClient(t, Config{})
	server.SetSecretVersion(testNS, "managed", []byte("value"), "text/plain", 7)
	ctx := context.Background()
	firstKey := strings.Repeat("a", 32)
	secondKey := strings.Repeat("b", 32)

	bind, err := client.BindSecret(ctx, "managed", 7, firstKey)
	if err != nil || bind.CurrentVersion != 8 || bind.PreviousVersion != 7 {
		t.Fatalf("BindSecret = %+v, %v", bind, err)
	}
	if calls := server.BindSecretCalls(); len(calls) != 1 || calls[0].GetExpectedCurrentVersion() != 7 || calls[0].GetBindingKey() != firstKey {
		t.Fatalf("BindSecret calls = %+v", calls)
	}
	unbound, err := client.UnbindSecret(ctx, "managed", 8, firstKey)
	if err != nil || unbound.CurrentVersion != 9 || unbound.PreviousVersion != 8 {
		t.Fatal(err)
	}
	if calls := server.UnbindSecretCalls(); len(calls) != 1 || calls[0].GetBindingKey() != firstKey {
		t.Fatalf("UnbindSecret calls = %+v", calls)
	}
	if _, err := client.BindSecret(ctx, "managed", 9, firstKey); err != nil {
		t.Fatal(err)
	}
	preview, err := client.PreviewSecretBindingCohort(ctx, "managed", 10, firstKey)
	if err != nil || preview.AnchorVersion != 10 || len(preview.AffectedVersions) != 1 {
		t.Fatalf("PreviewSecretBindingCohort = %+v, %v", preview, err)
	}
	if calls := server.PreviewSecretBindingCohortCalls(); len(calls) != 1 || calls[0].GetAnchorVersion() != 10 {
		t.Fatalf("Preview calls = %+v", calls)
	}
	rotation, err := client.RotateSecretBindingKey(ctx, "managed", 10, firstKey, secondKey)
	if err != nil || rotation.CurrentVersion != 11 || rotation.PreviousVersion != 10 {
		t.Fatal(err)
	}
	rotate := server.RotateSecretBindingKeyCalls()
	if len(rotate) != 1 || rotate[0].GetExpectedCurrentVersion() != 10 || rotate[0].GetBindingKey() != firstKey || rotate[0].GetNewBindingKey() != secondKey {
		t.Fatalf("Rotate calls = %+v", rotate)
	}
	rotated, err := client.PreviewSecretBindingCohort(ctx, "managed", 11, secondKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.PurgeSecretBindingCohortIfUnchanged(ctx, "managed", 11, secondKey, rotated); err != nil {
		t.Fatal(err)
	}
	purge := server.PurgeSecretBindingCohortCalls()
	if len(purge) != 1 || purge[0].GetBindingKey() != secondKey || purge[0].GetExpectedRevision() != rotated.Revision {
		t.Fatalf("Purge calls = %+v", purge)
	}
	unboundPreview, err := client.PreviewSecretUnboundVersions(ctx, "managed")
	if err != nil || len(unboundPreview.AffectedVersions) != 2 || unboundPreview.AffectedVersions[0] != 7 || unboundPreview.AffectedVersions[1] != 9 {
		t.Fatalf("PreviewSecretUnboundVersions = %+v, %v", unboundPreview, err)
	}
	if _, err := client.PurgeSecretUnboundVersions(ctx, "managed", unboundPreview); err != nil {
		t.Fatal(err)
	}
	if calls := server.PurgeSecretUnboundVersionsCalls(); len(calls) != 1 || calls[0].GetExpectedRevision() != unboundPreview.Revision {
		t.Fatalf("PurgeSecretUnboundVersions calls = %+v", calls)
	}
}

func TestRotateSecretBindingKeySendsIdenticalKeysForServerValidation(t *testing.T) {
	client, server := newTestClient(t, Config{})
	server.SetSecretVersion(testNS, "managed", []byte("value"), "text/plain", 1)
	server.SetSecretVersionCredentials(testNS, "managed", 1, "", strings.Repeat("k", 32))
	key := strings.Repeat("k", 32)
	if _, err := client.RotateSecretBindingKey(context.Background(), "managed", 1, key, key); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("RotateSecretBindingKey(no-op) = %v", err)
	}
	if got := server.RotateSecretBindingKeyCalls(); len(got) != 1 || got[0].GetBindingKey() != key || got[0].GetNewBindingKey() != key {
		t.Fatalf("no-op rotation calls = %+v", got)
	}
}

func TestGuardedBindingMutationsRejectMalformedPreviewLocally(t *testing.T) {
	client, server := newTestClient(t, Config{})
	key := strings.Repeat("k", 32)
	tests := []struct {
		name     string
		expected SecretBindingCohortResult
	}{
		{name: "zero revision", expected: SecretBindingCohortResult{AffectedVersions: []uint64{1}}},
		{name: "empty versions", expected: SecretBindingCohortResult{Revision: 1}},
		{name: "zero version", expected: SecretBindingCohortResult{Revision: 1, AffectedVersions: []uint64{0}}},
		{name: "unsorted", expected: SecretBindingCohortResult{Revision: 1, AffectedVersions: []uint64{2, 1}}},
		{name: "duplicate", expected: SecretBindingCohortResult{Revision: 1, AffectedVersions: []uint64{1, 1}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.PurgeSecretBindingCohortIfUnchanged(context.Background(), "missing", 1, key, test.expected); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("malformed purge guard error = %v, want ErrInvalidArgument", err)
			}
		})
	}
	if got := len(server.PurgeSecretBindingCohortCalls()); got != 0 {
		t.Fatalf("invalid purge guards made %d RPCs", got)
	}
}

func TestPurgeSecretUnboundVersionsRejectsMalformedPreviewLocally(t *testing.T) {
	client, server := newTestClient(t, Config{})
	for _, expected := range []SecretVersionSetResult{
		{AffectedVersions: []uint64{1}},
		{Revision: 1},
		{Revision: 1, AffectedVersions: []uint64{0}},
		{Revision: 1, AffectedVersions: []uint64{2, 1}},
		{Revision: 1, AffectedVersions: []uint64{1, 1}},
	} {
		if _, err := client.PurgeSecretUnboundVersions(context.Background(), "missing", expected); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("malformed unbound purge guard error = %v, want ErrInvalidArgument", err)
		}
	}
	if got := len(server.PurgeSecretUnboundVersionsCalls()); got != 0 {
		t.Fatalf("invalid guards made %d RPCs", got)
	}
}
