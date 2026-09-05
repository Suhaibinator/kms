package kmsclienttest

import (
	"context"
	"slices"
	"strings"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSecretBindingLifecycleModelsCredentialsCASAndTombstones(t *testing.T) {
	server, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx := context.Background()
	keyA, keyB, keyC := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	for version := uint64(1); version <= 3; version++ {
		server.SetSecretVersion("prod/app", "password", []byte("value"), "text/plain", version)
	}
	server.SetSecretVersionCredentials("prod/app", "password", 1, "", keyA)
	server.SetSecretVersionCredentials("prod/app", "password", 2, "", keyA)
	server.SetSecretVersionCredentials("prod/app", "password", 3, "", keyB)
	ref := resourceProto("prod/app", "password")

	if _, err := server.GetSecret(ctx, &kmsv1.GetSecretRequest{Ref: ref, Version: 2, BindingKey: keyB}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("wrong key GetSecret error = %v", err)
	}
	stale := uint64(2)
	if _, err := server.RotateSecretBindingKey(ctx, &kmsv1.RotateSecretBindingKeyRequest{
		Ref: ref, ExpectedCurrentVersion: stale, BindingKey: keyA, NewBindingKey: keyC,
	}); status.Code(err) != codes.Aborted {
		t.Fatalf("stale rotation error = %v", err)
	}
	rotated, err := server.RotateSecretBindingKey(ctx, &kmsv1.RotateSecretBindingKeyRequest{
		Ref: ref, ExpectedCurrentVersion: 3, BindingKey: keyB, NewBindingKey: keyC,
	})
	if err != nil || rotated.GetCurrentVersion() != 4 || rotated.GetPreviousVersion() != 3 {
		t.Fatalf("rotation = %+v, %v", rotated, err)
	}
	if _, err := server.GetSecret(ctx, &kmsv1.GetSecretRequest{Ref: ref, Version: 3, BindingKey: keyB}); err != nil {
		t.Fatalf("historical key GetSecret: %v", err)
	}
	if _, err := server.GetSecret(ctx, &kmsv1.GetSecretRequest{Ref: ref, Version: 4, BindingKey: keyC}); err != nil {
		t.Fatalf("rotated key GetSecret: %v", err)
	}

	current, err := server.PreviewSecretBindingCohort(ctx, &kmsv1.PreviewSecretBindingCohortRequest{Ref: ref, AnchorVersion: 3, BindingKey: keyB})
	if err != nil || current.GetAnchorVersion() != 3 || !slices.Equal(current.GetAffectedVersions(), []uint64{3}) {
		t.Fatalf("current preview = %+v, %v", current, err)
	}
	if _, err := server.PurgeSecretBindingCohort(ctx, &kmsv1.PurgeSecretBindingCohortRequest{
		Ref: ref, AnchorVersion: 3, BindingKey: keyB, ExpectedRevision: current.GetRevision(), ExpectedAffectedVersions: current.GetAffectedVersions(),
	}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	metadata, err := server.GetSecretMetadata(ctx, &kmsv1.GetSecretMetadataRequest{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.GetSecret().GetLabels()["current"] != 4 || metadata.GetSecret().GetContentType() == "" || !metadata.GetSecret().GetBound() {
		t.Fatalf("purged current projection = %+v", metadata.GetSecret())
	}
	if got := metadata.GetSecret().GetVersions()[2]; got.GetState() != "destroyed" || got.GetBound() || got.GetDestroyedAtUnixMs() == 0 {
		t.Fatalf("purged tombstone = %+v", got)
	}
}

func TestPutSecretKeepsHistoricalTokenRequirementImmutable(t *testing.T) {
	server, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx := context.Background()
	ref := resourceProto("prod/app", "token-history")

	if _, err := server.PutSecretV03(ctx, &kmsv1.PutSecretRequest{Ref: ref, Value: []byte("v1")}); err != nil {
		t.Fatal(err)
	}
	firstToken, err := server.PutSecretV03(ctx, &kmsv1.PutSecretRequest{
		Ref: ref, Value: []byte("v2"), GenerateAccessToken: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.PutSecretV03(ctx, &kmsv1.PutSecretRequest{Ref: ref, Value: []byte("v3")}); err != nil {
		t.Fatal(err)
	}
	secondToken, err := server.PutSecretV03(ctx, &kmsv1.PutSecretRequest{
		Ref: ref, Value: []byte("v4"), GenerateAccessToken: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	metadata, err := server.GetSecretMetadata(ctx, &kmsv1.GetSecretMetadataRequest{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	want := []bool{false, true, true, true}
	for i, version := range metadata.GetSecret().GetVersions() {
		if version.GetHasAccessToken() != want[i] {
			t.Fatalf("v%d HasAccessToken = %v, want %v", i+1, version.GetHasAccessToken(), want[i])
		}
	}
	if _, err := server.GetSecret(ctx, &kmsv1.GetSecretRequest{Ref: ref, Version: 1}); err != nil {
		t.Fatalf("ungated v1 changed after token mint: %v", err)
	}
	if _, err := server.GetSecret(ctx, &kmsv1.GetSecretRequest{Ref: ref, Version: 2, SecretToken: firstToken.GetAccessToken()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("retired token v2 error = %v, want permission denied", err)
	}
	if _, err := server.GetSecret(ctx, &kmsv1.GetSecretRequest{Ref: ref, Version: 2, SecretToken: secondToken.GetAccessToken()}); err != nil {
		t.Fatalf("rotated token v2 read: %v", err)
	}
}

func TestPutSecretInheritsAccessTokenAfterEveryGatedVersionIsPurged(t *testing.T) {
	for _, test := range []struct {
		name       string
		bindingKey string
		purge      func(context.Context, *Server, *kmsv1.ResourceRef, string) error
	}{
		{
			name:       "bound cohort",
			bindingKey: strings.Repeat("k", 32),
			purge: func(ctx context.Context, server *Server, ref *kmsv1.ResourceRef, bindingKey string) error {
				preview, err := server.PreviewSecretBindingCohort(ctx, &kmsv1.PreviewSecretBindingCohortRequest{
					Ref: ref, AnchorVersion: 1, BindingKey: bindingKey,
				})
				if err != nil {
					return err
				}
				_, err = server.PurgeSecretBindingCohort(ctx, &kmsv1.PurgeSecretBindingCohortRequest{
					Ref: ref, AnchorVersion: 1, BindingKey: bindingKey,
					ExpectedRevision: preview.GetRevision(), ExpectedAffectedVersions: preview.GetAffectedVersions(),
				})
				return err
			},
		},
		{
			name: "unbound versions",
			purge: func(ctx context.Context, server *Server, ref *kmsv1.ResourceRef, _ string) error {
				preview, err := server.PreviewSecretUnboundVersions(ctx, &kmsv1.PreviewSecretUnboundVersionsRequest{Ref: ref})
				if err != nil {
					return err
				}
				_, err = server.PurgeSecretUnboundVersions(ctx, &kmsv1.PurgeSecretUnboundVersionsRequest{
					Ref: ref, ExpectedRevision: preview.GetRevision(), ExpectedAffectedVersions: preview.GetAffectedVersions(),
				})
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, err := New()
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			ctx := context.Background()
			ref := resourceProto("prod/app", "token-after-purge")

			created, err := server.PutSecretV03(ctx, &kmsv1.PutSecretRequest{
				Ref: ref, Value: []byte("v1"), BindingKey: test.bindingKey, GenerateAccessToken: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := test.purge(ctx, server, ref, test.bindingKey); err != nil {
				t.Fatal(err)
			}

			purged, err := server.GetSecretMetadata(ctx, &kmsv1.GetSecretMetadataRequest{Ref: ref})
			if err != nil {
				t.Fatal(err)
			}
			if !purged.GetSecret().GetHasAccessToken() || purged.GetSecret().GetVersions()[0].GetHasAccessToken() {
				t.Fatalf("metadata after purge = %+v, want persistent secret token and ungated tombstone", purged.GetSecret())
			}

			next, err := server.PutSecretV03(ctx, &kmsv1.PutSecretRequest{Ref: ref, Value: []byte("v2")})
			if err != nil {
				t.Fatal(err)
			}
			if next.GetVersion() != 2 || next.GetAccessToken() != "" {
				t.Fatalf("ordinary PutSecret = %+v, want version 2 without newly minted token", next)
			}
			metadata, err := server.GetSecretMetadata(ctx, &kmsv1.GetSecretMetadataRequest{Ref: ref})
			if err != nil {
				t.Fatal(err)
			}
			if !metadata.GetSecret().GetHasAccessToken() || !metadata.GetSecret().GetVersions()[1].GetHasAccessToken() {
				t.Fatalf("metadata after PutSecret = %+v, want inherited token requirement", metadata.GetSecret())
			}
			if _, err := server.GetSecret(ctx, &kmsv1.GetSecretRequest{Ref: ref, Version: 2}); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("uncredentialed v2 read error = %v, want permission denied", err)
			}
			secret, err := server.GetSecret(ctx, &kmsv1.GetSecretRequest{
				Ref: ref, Version: 2, SecretToken: created.GetAccessToken(),
			})
			if err != nil || string(secret.GetValue()) != "v2" {
				t.Fatalf("credentialed v2 read = %+v, %v", secret, err)
			}
		})
	}
}

func TestActivateConfigurationReleaseNotifiesOnlyMatchingStreams(t *testing.T) {
	server, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.SetParameterVersion("prod/app", "settings", `{"enabled":true}`, "json", 1)

	matching := &ReleaseSubscription{
		Registration: &kmsv1.ReleaseWatchRegistration{Namespace: nsProto("prod/app"), Name: "runtime"},
		send:         make(chan *kmsv1.WatchReleaseEvent, 1),
	}
	otherName := &ReleaseSubscription{
		Registration: &kmsv1.ReleaseWatchRegistration{Namespace: nsProto("prod/app"), Name: "other"},
		send:         make(chan *kmsv1.WatchReleaseEvent, 1),
	}
	otherNamespace := &ReleaseSubscription{
		Registration: &kmsv1.ReleaseWatchRegistration{Namespace: nsProto("prod/other"), Name: "runtime"},
		send:         make(chan *kmsv1.WatchReleaseEvent, 1),
	}
	server.releaseSubs = []*ReleaseSubscription{matching, otherName, otherNamespace}

	_, err = server.ActivateConfigurationRelease(ReleaseSpec{
		Namespace: "prod/app",
		Name:      "runtime",
		Version:   1,
		Entries: []ReleaseEntrySpec{
			{Alias: "settings", Kind: "parameter", Path: "settings", Version: 1},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-matching.send:
	default:
		t.Fatal("matching release stream was not notified")
	}
	for name, sub := range map[string]*ReleaseSubscription{"other name": otherName, "other namespace": otherNamespace} {
		select {
		case <-sub.send:
			t.Errorf("%s stream received an unrelated activation", name)
		default:
		}
	}
}
