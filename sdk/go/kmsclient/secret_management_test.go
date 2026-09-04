package kmsclient

import (
	"context"
	"errors"
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

	bind, err := client.BindSecret(ctx, "managed", 7, "first-key")
	if err != nil || bind.AnchorVersion != 7 || len(bind.AffectedVersions) != 1 {
		t.Fatalf("BindSecret = %+v, %v", bind, err)
	}
	if calls := server.BindSecretCalls(); len(calls) != 1 || calls[0].GetVersion() != 7 || calls[0].GetBindingKey() != "first-key" {
		t.Fatalf("BindSecret calls = %+v", calls)
	}
	if _, err := client.UnbindSecret(ctx, "managed", 7, "first-key"); err != nil {
		t.Fatal(err)
	}
	if calls := server.UnbindSecretCalls(); len(calls) != 1 || calls[0].GetBindingKey() != "first-key" {
		t.Fatalf("UnbindSecret calls = %+v", calls)
	}
	preview, err := client.PreviewSecretBindingCohort(ctx, "managed", 7, "first-key")
	if err != nil {
		t.Fatal(err)
	}
	if calls := server.PreviewSecretBindingCohortCalls(); len(calls) != 1 || calls[0].GetAnchorVersion() != 7 {
		t.Fatalf("Preview calls = %+v", calls)
	}
	if _, err := client.RotateSecretBindingKeyIfUnchanged(ctx, "managed", 7, "first-key", "second-key", preview); err != nil {
		t.Fatal(err)
	}
	rotate := server.RotateSecretBindingKeyCalls()
	if len(rotate) != 1 || rotate[0].GetBindingKey() != "first-key" || rotate[0].GetNewBindingKey() != "second-key" ||
		rotate[0].ExpectedRevision == nil || rotate[0].GetExpectedRevision() != preview.Revision || len(rotate[0].GetExpectedAffectedVersions()) != 1 {
		t.Fatalf("Rotate calls = %+v", rotate)
	}
	if _, err := client.PurgeSecretBindingCohortIfUnchanged(ctx, "managed", 7, "second-key", preview); err != nil {
		t.Fatal(err)
	}
	purge := server.PurgeSecretBindingCohortCalls()
	if len(purge) != 1 || purge[0].GetBindingKey() != "second-key" || purge[0].ExpectedRevision == nil {
		t.Fatalf("Purge calls = %+v", purge)
	}
}
