package grpcserver

import (
	"bytes"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
	"google.golang.org/protobuf/proto"
)

func TestSchemaContractPresenceSurvivesProtobuf(t *testing.T) {
	unestablished, err := proto.Marshal(toProtoConfigurationSchema(domain.ConfigurationSchema{}))
	if err != nil {
		t.Fatal(err)
	}
	establishedEmpty, err := proto.Marshal(toProtoConfigurationSchema(domain.ConfigurationSchema{
		Contract: []domain.ApplicationContractField{},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(unestablished, establishedEmpty) {
		t.Fatalf("gRPC schema contract states have identical wire data: unestablished=%x established-empty=%x", unestablished, establishedEmpty)
	}
	// This established-empty wire fixture is also decoded by the Python and
	// TypeScript SDK tests, where a repeated empty contract has no presence.
	if !bytes.Equal(establishedEmpty, []byte{0x50, 0x01}) {
		t.Fatalf("established-empty wire data = %x, want 5001", establishedEmpty)
	}
	for _, test := range []struct {
		name        string
		contract    []domain.ApplicationContractField
		established bool
	}{
		{name: "unestablished"},
		{name: "established empty", contract: []domain.ApplicationContractField{}, established: true},
		{name: "established nonempty", contract: []domain.ApplicationContractField{
			{Alias: "settings", Kind: domain.ReleaseEntryParameter, ContentType: "json"},
			{Alias: "token", Kind: domain.ReleaseEntrySecret},
		}, established: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := proto.Marshal(toProtoConfigurationSchema(domain.ConfigurationSchema{Contract: test.contract}))
			if err != nil {
				t.Fatal(err)
			}
			var decoded kmsv1.ConfigurationSchema
			if err := proto.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.GetContractEstablished() != test.established || len(decoded.GetContract()) != len(test.contract) {
				t.Fatalf("decoded contract = %+v, want established=%v fields=%d", &decoded, test.established, len(test.contract))
			}
			for i, want := range test.contract {
				got := decoded.GetContract()[i]
				if got.GetAlias() != want.Alias || got.GetKind() != want.Kind || got.GetContentType() != want.ContentType {
					t.Fatalf("contract field %d = %+v, want %+v", i, got, want)
				}
			}
		})
	}
}
