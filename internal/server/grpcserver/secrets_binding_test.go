package grpcserver

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

const (
	grpcBindingKeyA = "binding-key-a-0123456789-0123456789"
	grpcBindingKeyB = "binding-key-b-0123456789-0123456789"
	grpcBindingKeyC = "binding-key-c-0123456789-0123456789"
)

func TestSecretBindingTransportLifecycle(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"})
	client := env.secret()
	secretRef := pRef("prod", "svc", "credentials")
	expiresAt := time.Now().Add(time.Hour).UnixMilli()

	putV1, err := client.PutSecret(adminCtx(), &kmsv1.PutSecretRequest{
		Ref: secretRef, Value: []byte("v1"), ContentType: "text/plain",
		MetadataJson: `{"epoch":1}`, BindingKey: grpcBindingKeyA,
		GenerateAccessToken: true, ExpiresAtUnixMs: expiresAt,
	})
	if err != nil {
		t.Fatalf("put bound token-gated v1: %v", err)
	}
	if putV1.GetVersion() != 1 || putV1.GetRevision() == 0 || putV1.GetAccessToken() == "" {
		t.Fatalf("put v1 response = %+v", putV1)
	}
	accessToken := putV1.GetAccessToken()

	// GetSecret must transmit the independent access token and binding key.
	if _, err := client.GetSecret(adminCtx(), &kmsv1.GetSecretRequest{
		Ref: secretRef, Version: 1, BindingKey: grpcBindingKeyA,
	}); codeOf(err) != codes.PermissionDenied {
		t.Fatalf("missing access token code = %v, want PermissionDenied", codeOf(err))
	}
	if _, err := client.GetSecret(adminCtx(), &kmsv1.GetSecretRequest{
		Ref: secretRef, Version: 1, SecretToken: accessToken,
	}); codeOf(err) != codes.Internal {
		t.Fatalf("missing binding key code = %v, want Internal", codeOf(err))
	}
	gotV1, err := client.GetSecret(adminCtx(), &kmsv1.GetSecretRequest{
		Ref: secretRef, Version: 1, SecretToken: accessToken, BindingKey: grpcBindingKeyA,
	})
	if err != nil {
		t.Fatalf("get with both credentials: %v", err)
	}
	if string(gotV1.GetValue()) != "v1" || gotV1.GetContentType() != "text/plain" || gotV1.GetMetadataJson() != `{"epoch":1}` {
		t.Fatalf("get v1 response = %+v", gotV1)
	}

	put := func(value, bindingKey string) *kmsv1.PutSecretResponse {
		t.Helper()
		response, err := client.PutSecret(adminCtx(), &kmsv1.PutSecretRequest{
			Ref: secretRef, Value: []byte(value), ContentType: "text/plain", BindingKey: bindingKey,
		})
		if err != nil {
			t.Fatalf("put %s: %v", value, err)
		}
		return response
	}
	if got := put("v2", "").GetVersion(); got != 2 {
		t.Fatalf("v2 version = %d", got)
	}
	if got := put("v3", grpcBindingKeyB).GetVersion(); got != 3 {
		t.Fatalf("v3 version = %d", got)
	}

	assertMetadata := func(wantCurrentBound bool, wantBound []bool) {
		t.Helper()
		metadata, err := client.GetSecretMetadata(adminCtx(), &kmsv1.GetSecretMetadataRequest{Ref: secretRef})
		if err != nil {
			t.Fatalf("get metadata: %v", err)
		}
		secret := metadata.GetSecret()
		if secret.GetBound() != wantCurrentBound || !secret.GetHasAccessToken() {
			t.Fatalf("current metadata = %+v", secret)
		}
		if len(secret.GetVersions()) != len(wantBound) {
			t.Fatalf("versions = %+v", secret.GetVersions())
		}
		for i, version := range secret.GetVersions() {
			if version.GetVersion() != uint64(i+1) || version.GetBound() != wantBound[i] || !version.GetHasAccessToken() {
				t.Fatalf("version %d metadata = %+v", i+1, version)
			}
		}
		if got := secret.GetVersions()[0].GetExpiresAtUnixMs(); got != expiresAt {
			t.Fatalf("v1 expiry = %d, want %d", got, expiresAt)
		}
	}
	assertMetadata(true, []bool{true, false, true})

	if _, err := client.PromoteSecretVersion(adminCtx(), &kmsv1.PromoteSecretVersionRequest{Ref: secretRef, Version: 2}); err != nil {
		t.Fatalf("promote unbound v2: %v", err)
	}
	assertMetadata(false, []bool{true, false, true})

	bound, err := client.BindSecret(adminCtx(), &kmsv1.BindSecretRequest{
		Ref: secretRef, Version: 0, BindingKey: grpcBindingKeyA,
	})
	if err != nil {
		t.Fatalf("bind current: %v", err)
	}
	if bound.GetAnchorVersion() != 2 || !slices.Equal(bound.GetAffectedVersions(), []uint64{2}) || bound.GetRevision() == 0 {
		t.Fatalf("bind response = %+v", bound)
	}
	assertMetadata(true, []bool{true, true, true})

	unbound, err := client.UnbindSecret(adminCtx(), &kmsv1.UnbindSecretRequest{
		Ref: secretRef, Version: 2, BindingKey: grpcBindingKeyA,
	})
	if err != nil {
		t.Fatalf("unbind v2: %v", err)
	}
	if unbound.GetAnchorVersion() != 2 || !slices.Equal(unbound.GetAffectedVersions(), []uint64{2}) || unbound.GetRevision() <= bound.GetRevision() {
		t.Fatalf("unbind response = %+v", unbound)
	}
	assertMetadata(false, []bool{true, false, true})

	if got := put("v4", grpcBindingKeyB).GetVersion(); got != 4 {
		t.Fatalf("v4 version = %d", got)
	}
	preview, err := client.PreviewSecretBindingCohort(adminCtx(), &kmsv1.PreviewSecretBindingCohortRequest{
		Ref: secretRef, AnchorVersion: 3, BindingKey: grpcBindingKeyB,
	})
	if err != nil {
		t.Fatalf("preview B cohort: %v", err)
	}
	if preview.GetAnchorVersion() != 3 || !slices.Equal(preview.GetAffectedVersions(), []uint64{3, 4}) || preview.GetRevision() == 0 {
		t.Fatalf("preview response = %+v", preview)
	}

	// Optional scalar presence must survive protobuf decoding. With no revision,
	// versions-only is malformed; pointer-to-zero is a present but stale guard.
	_, err = client.RotateSecretBindingKey(adminCtx(), &kmsv1.RotateSecretBindingKeyRequest{
		Ref: secretRef, AnchorVersion: 3, BindingKey: grpcBindingKeyB, NewBindingKey: grpcBindingKeyC,
		ExpectedAffectedVersions: []uint64{3, 4},
	})
	if codeOf(err) != codes.InvalidArgument {
		t.Fatalf("versions-only guard code = %v, want InvalidArgument", codeOf(err))
	}
	zeroRevision := uint64(0)
	_, err = client.RotateSecretBindingKey(adminCtx(), &kmsv1.RotateSecretBindingKeyRequest{
		Ref: secretRef, AnchorVersion: 3, BindingKey: grpcBindingKeyB, NewBindingKey: grpcBindingKeyC,
		ExpectedRevision: &zeroRevision, ExpectedAffectedVersions: []uint64{3, 4},
	})
	if codeOf(err) != codes.Aborted {
		t.Fatalf("explicit-zero guard code = %v, want Aborted", codeOf(err))
	}

	rotated, err := client.RotateSecretBindingKey(adminCtx(), &kmsv1.RotateSecretBindingKeyRequest{
		Ref: secretRef, AnchorVersion: 3, BindingKey: grpcBindingKeyB, NewBindingKey: grpcBindingKeyC,
		ExpectedRevision: &preview.Revision, ExpectedAffectedVersions: preview.GetAffectedVersions(),
	})
	if err != nil {
		t.Fatalf("rotate B cohort: %v", err)
	}
	if rotated.GetAnchorVersion() != 3 || !slices.Equal(rotated.GetAffectedVersions(), []uint64{3, 4}) || rotated.GetRevision() <= preview.GetRevision() {
		t.Fatalf("rotate response = %+v", rotated)
	}

	preview, err = client.PreviewSecretBindingCohort(adminCtx(), &kmsv1.PreviewSecretBindingCohortRequest{
		Ref: secretRef, AnchorVersion: 3, BindingKey: grpcBindingKeyC,
	})
	if err != nil {
		t.Fatalf("preview rotated cohort: %v", err)
	}
	env.store.addPolicy(domain.Policy{
		Name: "delegated-destroy", Subject: "client",
		Allow: []domain.PolicyRule{{Operation: domain.OpSecretDestroy, Env: "prod", App: "svc"}},
	})
	_, err = client.PurgeSecretBindingCohort(clientCtx(), &kmsv1.PurgeSecretBindingCohortRequest{
		Ref: secretRef, AnchorVersion: 3, BindingKey: grpcBindingKeyC,
		ExpectedRevision: &preview.Revision, ExpectedAffectedVersions: preview.GetAffectedVersions(),
	})
	if codeOf(err) != codes.PermissionDenied {
		t.Fatalf("delegated non-admin purge code = %v, want PermissionDenied", codeOf(err))
	}

	purged, err := client.PurgeSecretBindingCohort(adminCtx(), &kmsv1.PurgeSecretBindingCohortRequest{
		Ref: secretRef, AnchorVersion: 3, BindingKey: grpcBindingKeyC,
		ExpectedRevision: &preview.Revision, ExpectedAffectedVersions: preview.GetAffectedVersions(),
	})
	if err != nil {
		t.Fatalf("purge rotated cohort: %v", err)
	}
	if purged.GetAnchorVersion() != 3 || !slices.Equal(purged.GetAffectedVersions(), []uint64{3, 4}) || purged.GetRevision() <= preview.GetRevision() {
		t.Fatalf("purge response = %+v", purged)
	}
	_, err = client.GetSecret(adminCtx(), &kmsv1.GetSecretRequest{
		Ref: secretRef, Version: 3, SecretToken: accessToken, BindingKey: grpcBindingKeyC,
	})
	if codeOf(err) != codes.FailedPrecondition {
		t.Fatalf("purged version read code = %v, want FailedPrecondition", codeOf(err))
	}
	metadata, err := client.GetSecretMetadata(adminCtx(), &kmsv1.GetSecretMetadataRequest{Ref: secretRef})
	if err != nil {
		t.Fatalf("metadata after purge: %v", err)
	}
	secretMetadata := metadata.GetSecret()
	if secretMetadata.GetContentType() != "" || secretMetadata.GetMetadataJson() != "" {
		t.Fatalf("purged current projection retained content: %+v", secretMetadata)
	}
	versions := secretMetadata.GetVersions()
	for _, index := range []int{2, 3} {
		if versions[index].GetState() != domain.StateDestroyed || versions[index].GetBound() || versions[index].GetHasAccessToken() {
			t.Fatalf("purged version metadata = %+v", versions[index])
		}
	}
}

func TestPurgeCleanupPendingMapsAcrossGRPC(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"})
	client := env.secret()
	secretRef := pRef("prod", "svc", "cleanup-pending")
	if _, err := client.PutSecret(adminCtx(), &kmsv1.PutSecretRequest{
		Ref: secretRef, Value: []byte("value"), BindingKey: grpcBindingKeyA,
	}); err != nil {
		t.Fatalf("put bound secret: %v", err)
	}
	env.store.setPurgeResultErr(storage.ErrPurgeCleanupPending)

	response, err := client.PurgeSecretBindingCohort(adminCtx(), &kmsv1.PurgeSecretBindingCohortRequest{
		Ref: secretRef, AnchorVersion: 1, BindingKey: grpcBindingKeyA,
	})
	if response != nil || codeOf(err) != codes.Unavailable || status.Convert(err).Message() != storage.ErrPurgeCleanupPending.Error() {
		t.Fatalf("cleanup-pending response = %+v, error = %v", response, err)
	}

	env.store.mu.Lock()
	version := env.store.secrets[ref("prod", "svc", "cleanup-pending").String()].versions[1]
	env.store.mu.Unlock()
	if version.State != domain.StateDestroyed || len(version.Ciphertext) != 0 {
		t.Fatalf("cleanup-pending purge did not logically commit: %+v", version)
	}
}

func TestSecretBindingUnlockFailuresAreIndistinguishableOverGRPC(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"})
	client := env.secret()
	secretRef := pRef("prod", "svc", "credential-errors")
	if _, err := client.PutSecret(adminCtx(), &kmsv1.PutSecretRequest{
		Ref: secretRef, Value: []byte("value"), BindingKey: grpcBindingKeyA,
	}); err != nil {
		t.Fatalf("put bound secret: %v", err)
	}

	operations := []struct {
		name string
		call func(string) error
	}{
		{name: "unbind", call: func(key string) error {
			_, err := client.UnbindSecret(adminCtx(), &kmsv1.UnbindSecretRequest{Ref: secretRef, Version: 1, BindingKey: key})
			return err
		}},
		{name: "preview", call: func(key string) error {
			_, err := client.PreviewSecretBindingCohort(adminCtx(), &kmsv1.PreviewSecretBindingCohortRequest{Ref: secretRef, AnchorVersion: 1, BindingKey: key})
			return err
		}},
		{name: "rotate", call: func(key string) error {
			_, err := client.RotateSecretBindingKey(adminCtx(), &kmsv1.RotateSecretBindingKeyRequest{Ref: secretRef, AnchorVersion: 1, BindingKey: key, NewBindingKey: grpcBindingKeyC})
			return err
		}},
		{name: "purge", call: func(key string) error {
			_, err := client.PurgeSecretBindingCohort(adminCtx(), &kmsv1.PurgeSecretBindingCohortRequest{Ref: secretRef, AnchorVersion: 1, BindingKey: key})
			return err
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			var wantMessage string
			for _, credential := range []string{"", "short", grpcBindingKeyB} {
				err := operation.call(credential)
				if codeOf(err) != codes.Internal {
					t.Fatalf("credential %q code = %v, want Internal", credential, codeOf(err))
				}
				message := status.Convert(err).Message()
				if message != "internal error" || strings.Contains(message, credential) && credential != "" {
					t.Fatalf("credential %q leaked in %q", credential, message)
				}
				if wantMessage == "" {
					wantMessage = message
				} else if message != wantMessage {
					t.Fatalf("credential failures differ: %q != %q", message, wantMessage)
				}
			}
		})
	}
}

func TestSecretBindingExpectedRevisionGuardShapeOverGRPC(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"})
	client := env.secret()
	secretRef := pRef("prod", "svc", "guard-shape")
	if _, err := client.PutSecret(adminCtx(), &kmsv1.PutSecretRequest{Ref: secretRef, Value: []byte("value"), BindingKey: grpcBindingKeyA}); err != nil {
		t.Fatalf("put bound secret: %v", err)
	}
	preview, err := client.PreviewSecretBindingCohort(context.Background(), &kmsv1.PreviewSecretBindingCohortRequest{})
	if codeOf(err) != codes.Unauthenticated || preview != nil {
		t.Fatalf("unauthenticated preview = %+v, %v", preview, err)
	}

	current, err := client.PreviewSecretBindingCohort(adminCtx(), &kmsv1.PreviewSecretBindingCohortRequest{Ref: secretRef, AnchorVersion: 1, BindingKey: grpcBindingKeyA})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	_, err = client.PurgeSecretBindingCohort(adminCtx(), &kmsv1.PurgeSecretBindingCohortRequest{
		Ref: secretRef, AnchorVersion: 1, BindingKey: grpcBindingKeyA,
		ExpectedRevision: &current.Revision,
	})
	if codeOf(err) != codes.InvalidArgument {
		t.Fatalf("revision-only purge code = %v, want InvalidArgument", codeOf(err))
	}
}
