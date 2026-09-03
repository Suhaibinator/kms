package grpcserver

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

func TestCreateApplicationReleaseGRPCTransportAuthorizationAndSanitizedErrors(t *testing.T) {
	env := newTestEnv(t, true)
	request := &kmsv1.CreateApplicationReleaseRequest{
		Namespace: pNS("dev", "worker"),
		Artifact:  []byte(`{"secret":"do-not-echo"}`),
	}

	if _, err := env.admin().CreateApplicationRelease(clientCtx(), request); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("client create application release code = %v err=%v", status.Code(err), err)
	}
	_, err := env.admin().CreateApplicationRelease(adminCtx(), request)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("malformed artifact code = %v err=%v", status.Code(err), err)
	}
	if strings.Contains(err.Error(), "do-not-echo") {
		t.Fatalf("gRPC error leaked artifact bytes: %v", err)
	}
}

func TestCreateApplicationReleaseGRPCAcceptsArtifactAtParserLimit(t *testing.T) {
	env := newTestEnv(t, true)
	artifact := nearLimitDefaultsArtifact(t)

	_, err := env.admin().CreateApplicationRelease(adminCtx(), &kmsv1.CreateApplicationReleaseRequest{
		Namespace: pNS("dev", "worker"), Artifact: artifact,
	})
	// The memStore deliberately lacks application/release management. Reaching
	// FailedPrecondition proves transport decoding and the 4 MiB artifact parser
	// both completed rather than failing at gRPC's message-size boundary.
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("near-limit artifact code = %v err=%v", status.Code(err), err)
	}

	_, err = env.admin().CreateApplicationRelease(adminCtx(), &kmsv1.CreateApplicationReleaseRequest{
		Namespace: pNS("dev", "worker"),
		Artifact:  bytes.Repeat([]byte{'x'}, configstore.MaxDefaultsArtifactSize+1),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("oversize artifact code = %v err=%v", status.Code(err), err)
	}
}

func TestToProtoApplicationReleaseCreateResult(t *testing.T) {
	release := domain.ConfigurationRelease{
		Namespace: domain.NamespaceRef{Env: "dev", App: "worker"},
		Name:      "runtime", Version: 1, SchemaVersion: 2,
		Entries: []domain.ConfigurationReleaseEntry{
			{Alias: "runtime", Kind: "parameter", Ref: domain.Ref{NS: domain.NamespaceRef{Env: "dev", App: "worker"}, Key: "runtime"}, Version: 3, ContentType: "json", ParameterDigest: strings.Repeat("a", 64)},
			{Alias: "db", Kind: "secret", Ref: domain.Ref{NS: domain.NamespaceRef{Env: "dev", App: "worker"}, Key: "db"}, Version: 4, ClientBound: true, HasAccessToken: true},
		},
		Digest: strings.Repeat("b", 64), Metadata: `{"ticket":"OPS-1"}`,
		CreatedBy: "admin", CreatedAt: time.Unix(1_700_000_000, 0),
	}
	result := domain.ApplicationReleaseCreateResult{
		Profile: "dev", PlanDigest: strings.Repeat("c", 64), Valid: true,
		Executed: true, Created: true, ReleaseName: "runtime", SchemaVersion: 2,
		BaseReleaseVersion: 0,
		Entries: []domain.ApplicationReleasePlanEntry{
			{Alias: "runtime", Kind: "parameter", Ref: domain.Ref{NS: release.Namespace, Key: "runtime"}, ToVersion: 3, Source: domain.ApplicationReleaseSourceGeneratedDefault},
			{Alias: "db", Kind: "secret", Ref: domain.Ref{NS: release.Namespace, Key: "db"}, FromVersion: 4, ToVersion: 4, Source: domain.ApplicationReleaseSourceCarriedActiveSecret},
		},
		MissingSecrets: []string{},
		Validation: []domain.ReleaseValidationError{
			{Alias: "runtime", Code: "schema_violation", SchemaPointer: "/properties/runtime", Message: "value does not match schema"},
		},
		Release: &release,
	}

	got := toProtoApplicationReleaseCreateResult(result)
	if got.GetProfile() != result.Profile || got.GetPlanDigest() != result.PlanDigest || !got.GetValid() || !got.GetExecuted() || !got.GetCreated() {
		t.Fatalf("response state = %+v", got)
	}
	if got.GetReleaseName() != "runtime" || got.GetSchemaVersion() != 2 || got.GetBaseReleaseVersion() != 0 {
		t.Fatalf("response coordinates = %+v", got)
	}
	if len(got.GetEntries()) != 2 || got.GetEntries()[0].GetSource() != domain.ApplicationReleaseSourceGeneratedDefault || got.GetEntries()[1].GetSource() != domain.ApplicationReleaseSourceCarriedActiveSecret {
		t.Fatalf("response entries = %+v", got.GetEntries())
	}
	if got.GetEntries()[1].GetFromVersion() != 4 || got.GetEntries()[1].GetToVersion() != 4 || got.GetEntries()[1].GetRef().GetKey() != "db" {
		t.Fatalf("secret plan entry = %+v", got.GetEntries()[1])
	}
	if len(got.GetValidation()) != 1 || got.GetValidation()[0].GetCode() != "schema_violation" {
		t.Fatalf("response validation = %+v", got.GetValidation())
	}
	if got.GetRelease() == nil || got.GetRelease().GetVersion() != 1 || got.GetRelease().GetEntries()[1].GetVersion() != 4 {
		t.Fatalf("response release = %+v", got.GetRelease())
	}

	preview := toProtoApplicationReleaseCreateResult(domain.ApplicationReleaseCreateResult{
		Profile: "dev", PlanDigest: strings.Repeat("d", 64), ReleaseName: "runtime",
	})
	if preview.GetRelease() != nil {
		t.Fatalf("preview unexpectedly included a release: %+v", preview.GetRelease())
	}
}

func TestApplicationReleasePlanEntryWireShapeIsValueFree(t *testing.T) {
	descriptor := (&kmsv1.ApplicationReleasePlanEntry{}).ProtoReflect().Descriptor()
	got := make([]string, 0, descriptor.Fields().Len())
	for index := 0; index < descriptor.Fields().Len(); index++ {
		field := descriptor.Fields().Get(index)
		if field.Kind() == protoreflect.BytesKind {
			t.Fatalf("plan entry unexpectedly contains bytes field %q", field.Name())
		}
		got = append(got, string(field.Name()))
	}
	want := []string{"alias", "kind", "ref", "from_version", "to_version", "source"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan entry fields = %v, want %v", got, want)
	}
}
