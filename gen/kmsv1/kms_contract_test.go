package kmsv1

import (
	"testing"

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
		{"SecretMetadata", fields("ref", "content_type", "bound", "has_access_token", "metadata_json", "created_at_unix_ms", "updated_at_unix_ms", "labels", "versions")},
		{"SecretVersionInfo", fields("version", "state", "created_by", "created_at_unix_ms", "destroyed_at_unix_ms", "expires_at_unix_ms", "metadata_json", "bound", "has_access_token")},
		{"GetSecretRequest", fields("ref", "version", "label", "secret_token", "binding_key")},
		{"PutSecretRequest", fields("ref", "value", "content_type", "metadata_json", "binding_key", "generate_access_token", "expires_at_unix_ms")},
		{"BindSecretRequest", fields("ref", "version", "binding_key")},
		{"UnbindSecretRequest", fields("ref", "version", "binding_key")},
		{"SecretVersionMutationResponse", fields("anchor_version", "affected_versions", "revision")},
		{"PreviewSecretBindingCohortRequest", fields("ref", "anchor_version", "binding_key")},
		{"RotateSecretBindingKeyRequest", fields("ref", "anchor_version", "binding_key", "new_binding_key", "expected_revision", "expected_affected_versions")},
		{"PurgeSecretBindingCohortRequest", fields("ref", "anchor_version", "binding_key", "expected_revision", "expected_affected_versions")},
		{"SecretBindingCohortResponse", fields("anchor_version", "affected_versions", "revision")},
		{"ConfigurationReleaseEntry", fields("alias", "kind", "ref", "version", "content_type", "metadata_json", "parameter_digest")},
		{"ConfigurationRelease", fields("namespace", "name", "version", "schema_version", "entries", "digest", "metadata_json", "created_by", "created_at_unix_ms")},
		{"CreateReleaseRequest", fields("namespace", "name", "schema_version", "entries", "metadata_json")},
		{"ConfigurationSchema", fields("version", "schema_json", "digest", "metadata_json", "created_by", "created_at_unix_ms", "application", "release_name")},
		{"CreateSchemaRequest", fields("schema_json", "metadata_json", "application")},
		{"GetSchemaRequest", fields("version", "application", "release_name")},
		{"ListSchemasRequest", fields("page_size", "page_token", "application", "release_name")},
	}

	messages := File_kms_v1_kms_proto.Messages()
	for _, test := range tests {
		test := test
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
			if descriptor.ReservedNames().Len() != 0 || descriptor.ReservedRanges().Len() != 0 {
				t.Error("clean 0.3 contract unexpectedly retains reserved names or numbers")
			}
		})
	}

	rotate := messages.ByName("RotateSecretBindingKeyRequest")
	purge := messages.ByName("PurgeSecretBindingCohortRequest")
	for _, descriptor := range []protoreflect.MessageDescriptor{rotate, purge} {
		if field := descriptor.Fields().ByName("expected_revision"); field == nil || !field.HasPresence() {
			t.Errorf("%s.expected_revision must have explicit presence", descriptor.Name())
		}
		if field := descriptor.Fields().ByName("expected_affected_versions"); field == nil || field.Cardinality() != protoreflect.Repeated {
			t.Errorf("%s.expected_affected_versions must be repeated", descriptor.Name())
		}
	}
}

func TestV03SecretBindingRPCLayouts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method string
		input  protoreflect.FullName
		output protoreflect.FullName
	}{
		{"BindSecret", "kms.v1.BindSecretRequest", "kms.v1.SecretVersionMutationResponse"},
		{"UnbindSecret", "kms.v1.UnbindSecretRequest", "kms.v1.SecretVersionMutationResponse"},
		{"PreviewSecretBindingCohort", "kms.v1.PreviewSecretBindingCohortRequest", "kms.v1.SecretBindingCohortResponse"},
		{"RotateSecretBindingKey", "kms.v1.RotateSecretBindingKeyRequest", "kms.v1.SecretBindingCohortResponse"},
		{"PurgeSecretBindingCohort", "kms.v1.PurgeSecretBindingCohortRequest", "kms.v1.SecretBindingCohortResponse"},
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
	result := make([]wireField, len(names))
	for index, name := range names {
		result[index] = wireField{name: name, number: protoreflect.FieldNumber(index + 1)}
	}
	return result
}
