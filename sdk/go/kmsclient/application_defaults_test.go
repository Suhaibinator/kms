package kmsclient

import (
	"context"
	"errors"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestApplyApplicationDefaults(t *testing.T) {
	client, server := newTestClient(t, Config{Token: "admin-token"})
	server.QueueApplicationDefaultsResponse(&kmsv1.ApplyApplicationDefaultsResponse{
		Profile: "dev", SchemaSha256: "schema", ArtifactDigest: "artifact", PlanDigest: "plan",
		Entries: []*kmsv1.DefaultsApplyEntry{{
			Alias: "database", Key: "database", ContentType: "json", Status: "unchanged", CurrentVersion: 3,
		}},
		MissingSecrets:    []string{"db_password"},
		DefinitionChanged: true,
	}, nil)

	result, err := client.ApplyApplicationDefaults(context.Background(), ApplicationDefaultsApplyOptions{
		Namespace: "dev/gradethis", Artifact: []byte("artifact"), Overwrite: true, UpdateDefinition: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile != "dev" || result.PlanDigest != "plan" || result.Executed || !result.DefinitionChanged || result.DefinitionUpdated ||
		len(result.Entries) != 1 || result.Entries[0].Status != "unchanged" ||
		len(result.MissingSecrets) != 1 || result.MissingSecrets[0] != "db_password" {
		t.Fatalf("result = %#v", result)
	}
	calls := server.ApplicationDefaultsCalls()
	if len(calls) != 1 || calls[0].GetNamespace().GetEnv() != "dev" ||
		calls[0].GetNamespace().GetApp() != "gradethis" || !calls[0].GetOverwrite() ||
		!calls[0].GetUpdateDefinition() ||
		string(calls[0].GetArtifact()) != "artifact" {
		t.Fatalf("calls = %#v", calls)
	}
	if got := server.LastMetadata("ApplyApplicationDefaults").Get("authorization"); len(got) != 1 || got[0] != "Bearer admin-token" {
		t.Fatalf("authorization metadata = %v", got)
	}
}

func TestApplyApplicationDefaultsValidatesInputsAndResponses(t *testing.T) {
	client, server := newTestClient(t, Config{})
	if _, err := client.ApplyApplicationDefaults(context.Background(), ApplicationDefaultsApplyOptions{Namespace: "invalid", Artifact: []byte("x")}); err == nil {
		t.Fatal("invalid namespace succeeded")
	}
	if _, err := client.ApplyApplicationDefaults(context.Background(), ApplicationDefaultsApplyOptions{Namespace: "dev/app"}); err == nil {
		t.Fatal("empty artifact succeeded")
	}
	server.QueueApplicationDefaultsResponse(&kmsv1.ApplyApplicationDefaultsResponse{}, nil)
	if _, err := client.ApplyApplicationDefaults(context.Background(), ApplicationDefaultsApplyOptions{Namespace: "dev/app", Artifact: []byte("x")}); err == nil {
		t.Fatal("response without plan digest succeeded")
	}
	server.QueueApplicationDefaultsResponse(&kmsv1.ApplyApplicationDefaultsResponse{
		PlanDigest: "plan", Entries: []*kmsv1.DefaultsApplyEntry{{Alias: "a", Key: "a", Status: "unknown"}},
	}, nil)
	if _, err := client.ApplyApplicationDefaults(context.Background(), ApplicationDefaultsApplyOptions{Namespace: "dev/app", Artifact: []byte("x")}); err == nil {
		t.Fatal("response with unknown status succeeded")
	}
	server.QueueApplicationDefaultsResponse(&kmsv1.ApplyApplicationDefaultsResponse{
		PlanDigest: "plan", DefinitionUpdated: true,
	}, nil)
	if _, err := client.ApplyApplicationDefaults(context.Background(), ApplicationDefaultsApplyOptions{Namespace: "dev/app", Artifact: []byte("x")}); err == nil {
		t.Fatal("response with impossible definition state succeeded")
	}
}

func TestApplyApplicationDefaultsMapsAborted(t *testing.T) {
	client, server := newTestClient(t, Config{})
	server.QueueApplicationDefaultsResponse(nil, status.Error(codes.Aborted, "stale plan"))
	_, err := client.ApplyApplicationDefaults(context.Background(), ApplicationDefaultsApplyOptions{
		Namespace: "dev/app", Artifact: []byte("x"), Execute: true, PlanDigest: "old",
	})
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("error = %v, want ErrAborted", err)
	}
}
