package kmsclient

import (
	"context"
	"errors"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testSchemaDigest = "a2c799262a3ce3c19ef5cdd983bf3d12b43ab3c426227091b909dcb7054738c0"

func TestCreateApplicationSchema(t *testing.T) {
	client, server := newTestClient(t, Config{Token: "admin-token"})
	server.QueueCreateSchemaResponse(&kmsv1.CreateSchemaResponse{Schema: &kmsv1.ConfigurationSchema{
		Application: "gradethis", ReleaseName: "runtime", Version: 2, Digest: testSchemaDigest, MetadataJson: `{"owner":"config"}`,
	}}, nil)

	created, err := client.CreateApplicationSchema(context.Background(), CreateApplicationSchemaOptions{
		Application: "gradethis", Schema: []byte(`{"type":"object"}`), MetadataJSON: `{"owner":"config"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Application != "gradethis" || created.ReleaseName != "runtime" || created.Version != 2 || created.Digest != testSchemaDigest {
		t.Fatalf("created = %#v", created)
	}
	calls := server.CreateSchemaCalls()
	if len(calls) != 1 || calls[0].GetApplication() != "gradethis" || calls[0].GetSchemaJson() != `{"type":"object"}` || calls[0].GetMetadataJson() != `{"owner":"config"}` {
		t.Fatalf("calls = %#v", calls)
	}
	if got := server.LastMetadata("CreateSchema").Get("authorization"); len(got) != 1 || got[0] != "Bearer admin-token" {
		t.Fatalf("authorization metadata = %v", got)
	}
}

func TestCreateApplicationSchemaRejectsDuplicateAndBadResponses(t *testing.T) {
	client, server := newTestClient(t, Config{})
	server.QueueCreateSchemaResponse(nil, status.Error(codes.AlreadyExists, "schema already registered"))
	_, err := client.CreateApplicationSchema(context.Background(), CreateApplicationSchemaOptions{Application: "app", Schema: []byte(`{}`)})
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("error = %v, want ErrAlreadyExists", err)
	}

	server.QueueCreateSchemaResponse(&kmsv1.CreateSchemaResponse{Schema: &kmsv1.ConfigurationSchema{
		Application: "other", ReleaseName: "runtime", Version: 1, Digest: testSchemaDigest,
	}}, nil)
	if _, err := client.CreateApplicationSchema(context.Background(), CreateApplicationSchemaOptions{Application: "app", Schema: []byte(`{}`)}); err == nil {
		t.Fatal("mismatched application response succeeded")
	}
	if _, err := client.CreateApplicationSchema(context.Background(), CreateApplicationSchemaOptions{Schema: []byte(`{}`)}); err == nil {
		t.Fatal("empty application succeeded")
	}
	if _, err := client.CreateApplicationSchema(context.Background(), CreateApplicationSchemaOptions{Application: "app"}); err == nil {
		t.Fatal("empty schema succeeded")
	}
}
