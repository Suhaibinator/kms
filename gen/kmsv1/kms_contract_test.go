package kmsv1

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type wireField struct {
	name   protoreflect.Name
	number protoreflect.FieldNumber
}

func TestV03WireFieldLayouts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		message string
		fields  []wireField
	}{
		{"SecretMetadata", fields("ref", "content_type", "bound", "", "metadata_json", "created_at_unix_ms", "updated_at_unix_ms", "labels", "versions")},
		{"SecretVersionInfo", fields("version", "state", "created_by", "created_at_unix_ms", "destroyed_at_unix_ms", "expires_at_unix_ms", "metadata_json", "bound", "")},
		{"GetSecretRequest", fields("ref", "version", "label", "", "binding_key")},
		{"PutSecretRequest", fields("ref", "value", "content_type", "metadata_json", "binding_key", "", "expires_at_unix_ms")},
		{"BindSecretRequest", fields("ref", "expected_current_version", "binding_key")},
		{"UnbindSecretRequest", fields("ref", "expected_current_version", "binding_key")},
		{"SecretVersionTransitionResponse", fields("current_version", "previous_version", "revision")},
		{"PreviewSecretBindingCohortRequest", fields("ref", "anchor_version", "binding_key")},
		{"RotateSecretBindingKeyRequest", fields("ref", "expected_current_version", "binding_key", "new_binding_key")},
		{"PurgeSecretBindingCohortRequest", fields("ref", "anchor_version", "binding_key", "expected_revision", "expected_affected_versions")},
		{"SecretBindingCohortResponse", fields("anchor_version", "affected_versions", "revision")},
		{"PreviewSecretUnboundVersionsRequest", fields("ref")},
		{"PurgeSecretUnboundVersionsRequest", fields("ref", "expected_revision", "expected_affected_versions")},
		{"SecretVersionSetResponse", fields("affected_versions", "revision")},
		{"ConfigurationReleaseEntry", fields("alias", "kind", "ref", "version", "content_type", "metadata_json", "parameter_digest")},
		{"ConfigurationRelease", fields("namespace", "name", "version", "schema_version", "entries", "digest", "metadata_json", "created_by", "created_at_unix_ms")},
		{"CreateReleaseRequest", fields("namespace", "name", "schema_version", "entries", "metadata_json")},
		{"ReleaseAcknowledgement", fields("namespace", "name", "version", "activation_revision", "client_name", "instance_id", "state", "rejection_category", "diagnostic", "timestamp_unix_ms", "applied_divergent", "divergent_field_count", "schema_version", "sequence")},
		{"ReleaseAcknowledgementRejectedEvent", fields("namespace", "name", "schema_version", "version", "activation_revision", "client_name", "instance_id", "state", "sequence", "reason")},
		{"ConfigurationSchema", fields("version", "schema_json", "digest", "metadata_json", "created_by", "created_at_unix_ms", "application", "release_name", "contract", "contract_established")},
		{"CreateSchemaRequest", fields("schema_json", "metadata_json", "application")},
		{"GetSchemaRequest", fields("version", "application", "release_name")},
		{"ListSchemasRequest", fields("page_size", "page_token", "application", "release_name")},
	}

	messages := File_kms_v1_kms_proto.Messages()
	for _, test := range tests {
		t.Run(test.message, func(t *testing.T) {
			descriptor := messages.ByName(protoreflect.Name(test.message))
			if descriptor == nil {
				t.Fatalf("message %q is missing", test.message)
			}
			if got, want := descriptor.Fields().Len(), len(test.fields); got != want {
				t.Fatalf("field count = %d, want %d", got, want)
			}
			for index, want := range test.fields {
				got := descriptor.Fields().Get(index)
				if got.Name() != want.name || got.Number() != want.number {
					t.Errorf("field %d = %s:%d, want %s:%d", index, got.Name(), got.Number(), want.name, want.number)
				}
			}
		})
	}

	for _, name := range []protoreflect.Name{"PurgeSecretBindingCohortRequest", "PurgeSecretUnboundVersionsRequest"} {
		descriptor := messages.ByName(name)
		if field := descriptor.Fields().ByName("expected_revision"); field == nil || field.Kind() != protoreflect.Uint64Kind || field.Cardinality() != protoreflect.Optional || field.HasPresence() {
			t.Errorf("%s.expected_revision must be a singular uint64 without presence", descriptor.Name())
		}
		if field := descriptor.Fields().ByName("expected_affected_versions"); field == nil || field.Cardinality() != protoreflect.Repeated {
			t.Errorf("%s.expected_affected_versions must be repeated", descriptor.Name())
		}
	}
}

func TestWireNumbersRemainDenseIncludingReservedSlots(t *testing.T) {
	t.Parallel()

	assertDenseUnreservedMessages(t, File_kms_v1_kms_proto.Messages())
}

func assertDenseUnreservedMessages(t *testing.T, messages protoreflect.MessageDescriptors) {
	t.Helper()
	for index := 0; index < messages.Len(); index++ {
		descriptor := messages.Get(index)
		t.Run(string(descriptor.FullName()), func(t *testing.T) {

			limit := descriptor.Fields().Len() + descriptor.ReservedRanges().Len()
			seen := make([]bool, limit+1)
			for i := 0; i < descriptor.ReservedRanges().Len(); i++ {
				r := descriptor.ReservedRanges().Get(i)
				if r[1] != r[0]+1 || r[0] < 1 || int(r[0]) > limit {
					t.Fatal("unexpected reserved range")
				}
				seen[int(r[0])] = true
			}
			for fieldIndex := 0; fieldIndex < descriptor.Fields().Len(); fieldIndex++ {
				field := descriptor.Fields().Get(fieldIndex)
				number := int(field.Number())
				if number < 1 || number > limit {
					t.Errorf("field %s has number %d outside dense range 1..%d", field.Name(), number, descriptor.Fields().Len())
					continue
				}
				seen[number] = true
			}
			for number := 1; number < len(seen); number++ {
				if !seen[number] {
					t.Errorf("field number %d is missing from dense range 1..%d", number, descriptor.Fields().Len())
				}
			}

			assertDenseUnreservedMessages(t, descriptor.Messages())
		})
	}
}

func TestV03SecretBindingRPCLayouts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method string
		input  protoreflect.FullName
		output protoreflect.FullName
	}{
		{"BindSecret", "kms.v1.BindSecretRequest", "kms.v1.SecretVersionTransitionResponse"},
		{"UnbindSecret", "kms.v1.UnbindSecretRequest", "kms.v1.SecretVersionTransitionResponse"},
		{"PreviewSecretBindingCohort", "kms.v1.PreviewSecretBindingCohortRequest", "kms.v1.SecretBindingCohortResponse"},
		{"RotateSecretBindingKey", "kms.v1.RotateSecretBindingKeyRequest", "kms.v1.SecretVersionTransitionResponse"},
		{"PurgeSecretBindingCohort", "kms.v1.PurgeSecretBindingCohortRequest", "kms.v1.SecretBindingCohortResponse"},
		{"PreviewSecretUnboundVersions", "kms.v1.PreviewSecretUnboundVersionsRequest", "kms.v1.SecretVersionSetResponse"},
		{"PurgeSecretUnboundVersions", "kms.v1.PurgeSecretUnboundVersionsRequest", "kms.v1.SecretVersionSetResponse"},
	}

	service := File_kms_v1_kms_proto.Services().ByName("SecretService")
	if service == nil {
		t.Fatal("SecretService is missing")
	}
	for _, test := range tests {
		method := service.Methods().ByName(protoreflect.Name(test.method))
		if method == nil {
			t.Errorf("RPC %s is missing", test.method)
			continue
		}
		if method.Input().FullName() != test.input || method.Output().FullName() != test.output {
			t.Errorf("RPC %s = %s -> %s, want %s -> %s", test.method, method.Input().FullName(), method.Output().FullName(), test.input, test.output)
		}
		if method.IsStreamingClient() || method.IsStreamingServer() {
			t.Errorf("RPC %s must be unary", test.method)
		}
	}
}

func fields(names ...protoreflect.Name) []wireField {
	out := make([]wireField, 0, len(names))
	for index, name := range names {
		if name == "" {
			continue
		}
		out = append(out, wireField{name: name, number: protoreflect.FieldNumber(index + 1)})
	}
	return out
}

func TestRemovedSecretTokenFieldsAreReserved(t *testing.T) {
	for _, retired := range []struct {
		message, name protoreflect.Name
		number        protoreflect.FieldNumber
	}{
		{"SecretMetadata", "has_access_token", 4},
		{"SecretVersionInfo", "has_access_token", 9},
		{"GetSecretRequest", "secret_token", 4},
		{"PutSecretRequest", "generate_access_token", 6},
		{"PutSecretResponse", "access_token", 3},
	} {
		descriptor := File_kms_v1_kms_proto.Messages().ByName(retired.message)
		if descriptor.Fields().ByName(retired.name) != nil || descriptor.Fields().ByNumber(retired.number) != nil || !descriptor.ReservedNames().Has(retired.name) || !descriptor.ReservedRanges().Has(retired.number) {
			t.Errorf("%s.%s (%d) must remain removed and reserved", retired.message, retired.name, retired.number)
		}
	}
}

// Missing schema selection and explicitly selecting schema zero must remain
// distinguishable across a wire round trip, so legacy clients cannot silently
// attach to a different schema track.
func TestReleaseTrackSchemaSelectionPresence(t *testing.T) {
	tests := []proto.Message{
		&GetActiveReleaseRequest{SchemaVersion: proto.Uint64(0)},
		&GetReleaseRequest{SchemaVersion: proto.Uint64(0)},
		&ValidateReleaseRequest{SchemaVersion: proto.Uint64(0)},
		&ActivateReleaseRequest{SchemaVersion: proto.Uint64(0)},
		&ReleaseWatchRegistration{SchemaVersion: proto.Uint64(0)},
	}
	for _, message := range tests {
		t.Run(string(message.ProtoReflect().Descriptor().Name()), func(t *testing.T) {
			wire, err := proto.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			decoded := message.ProtoReflect().New().Interface()
			if err := proto.Unmarshal(wire, decoded); err != nil {
				t.Fatal(err)
			}
			field := decoded.ProtoReflect().Descriptor().Fields().ByName("schema_version")
			if !decoded.ProtoReflect().Has(field) || decoded.ProtoReflect().Get(field).Uint() != 0 {
				t.Fatal("explicit schema zero lost its presence in transport")
			}
			proto.Reset(decoded)
			if decoded.ProtoReflect().Has(field) {
				t.Fatal("absent selection has presence")
			}
		})
	}
}
