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
	preview, err := server.PreviewSecretBindingCohort(ctx, &kmsv1.PreviewSecretBindingCohortRequest{Ref: ref, AnchorVersion: 2, BindingKey: keyA})
	if err != nil || !slices.Equal(preview.GetAffectedVersions(), []uint64{1, 2}) {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	stale := preview.GetRevision() + 1
	if _, err := server.RotateSecretBindingKey(ctx, &kmsv1.RotateSecretBindingKeyRequest{
		Ref: ref, AnchorVersion: 2, BindingKey: keyA, NewBindingKey: keyC,
		ExpectedRevision: &stale, ExpectedAffectedVersions: preview.GetAffectedVersions(),
	}); status.Code(err) != codes.Aborted {
		t.Fatalf("stale rotation error = %v", err)
	}
	revision := preview.GetRevision()
	rotated, err := server.RotateSecretBindingKey(ctx, &kmsv1.RotateSecretBindingKeyRequest{
		Ref: ref, AnchorVersion: 2, BindingKey: keyA, NewBindingKey: keyC,
		ExpectedRevision: &revision, ExpectedAffectedVersions: preview.GetAffectedVersions(),
	})
	if err != nil || !slices.Equal(rotated.GetAffectedVersions(), []uint64{1, 2}) {
		t.Fatalf("rotation = %+v, %v", rotated, err)
	}
	if _, err := server.GetSecret(ctx, &kmsv1.GetSecretRequest{Ref: ref, Version: 1, BindingKey: keyA}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("retired key GetSecret error = %v", err)
	}
	if _, err := server.GetSecret(ctx, &kmsv1.GetSecretRequest{Ref: ref, Version: 1, BindingKey: keyC}); err != nil {
		t.Fatalf("rotated key GetSecret: %v", err)
	}

	current, err := server.PreviewSecretBindingCohort(ctx, &kmsv1.PreviewSecretBindingCohortRequest{Ref: ref, BindingKey: keyB})
	if err != nil || current.GetAnchorVersion() != 3 || !slices.Equal(current.GetAffectedVersions(), []uint64{3}) {
		t.Fatalf("current preview = %+v, %v", current, err)
	}
	revision = current.GetRevision()
	if _, err := server.PurgeSecretBindingCohort(ctx, &kmsv1.PurgeSecretBindingCohortRequest{
		Ref: ref, BindingKey: keyB, ExpectedRevision: &revision, ExpectedAffectedVersions: current.GetAffectedVersions(),
	}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	metadata, err := server.GetSecretMetadata(ctx, &kmsv1.GetSecretMetadataRequest{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.GetSecret().GetLabels()["current"] != 3 || metadata.GetSecret().GetContentType() != "" || metadata.GetSecret().GetBound() {
		t.Fatalf("purged current projection = %+v", metadata.GetSecret())
	}
	if got := metadata.GetSecret().GetVersions()[2]; got.GetState() != "destroyed" || got.GetBound() || got.GetDestroyedAtUnixMs() == 0 {
		t.Fatalf("purged tombstone = %+v", got)
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
