package grpcserver

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
)

// TestPutSecretRejectsV02ClientBoundWireRequest protects the intentional v0.3
// breaking boundary. In v0.2 PutSecretRequest field 5 was the bool
// client_bound; in v0.3 it became the string binding_key. A v0.3 protobuf
// decoder skips the stale varint field because its wire type does not match,
// so accepting the old RPC would silently create an unbound secret.
func TestPutSecretRejectsV02ClientBoundWireRequest(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"})

	stale := v02PutSecretRequest(t, "prod", "svc", "stale-client", []byte("value"), true)
	wire, err := proto.Marshal(stale)
	if err != nil {
		t.Fatalf("marshal v0.2 PutSecretRequest: %v", err)
	}
	decoded := new(kmsv1.PutSecretRequest)
	if err := proto.Unmarshal(wire, decoded); err != nil {
		t.Fatalf("decode v0.2 bytes as v0.3 PutSecretRequest: %v", err)
	}
	if decoded.GetBindingKey() != "" {
		t.Fatalf("v0.2 client_bound decoded as binding_key %q, want empty", decoded.GetBindingKey())
	}
	if !decoded.GetGenerateAccessToken() {
		t.Fatal("v0.2 generate_access_token did not survive decoding")
	}

	response := new(kmsv1.PutSecretResponse)
	err = env.conn.Invoke(adminCtx(), kmsv1.SecretService_PutSecret_FullMethodName, stale, response)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("v0.2 PutSecret = %v, want FailedPrecondition", err)
	}
	if got := status.Convert(err).Message(); got != "PutSecret is incompatible with KMS v0.3; use PutSecretV03" {
		t.Fatalf("v0.2 PutSecret message = %q", got)
	}

	env.store.mu.Lock()
	_, wrote := env.store.secrets["/prod/svc/stale-client"]
	env.store.mu.Unlock()
	if wrote {
		t.Fatal("v0.2 PutSecret wrote a secret")
	}
}

func v02PutSecretRequest(t *testing.T, env, app, key string, value []byte, clientBound bool) proto.Message {
	t.Helper()
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	message := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	stringType := descriptorpb.FieldDescriptorProto_TYPE_STRING
	bytesType := descriptorpb.FieldDescriptorProto_TYPE_BYTES
	boolType := descriptorpb.FieldDescriptorProto_TYPE_BOOL
	int64Type := descriptorpb.FieldDescriptorProto_TYPE_INT64
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Syntax:  proto.String("proto3"),
		Name:    proto.String("kms/v02/kms.proto"),
		Package: proto.String("kms.v1"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("NamespaceRef"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("env"), Number: proto.Int32(1), Label: &optional, Type: &stringType},
					{Name: proto.String("app"), Number: proto.Int32(2), Label: &optional, Type: &stringType},
				},
			},
			{
				Name: proto.String("ResourceRef"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("namespace"), Number: proto.Int32(1), Label: &optional, Type: &message, TypeName: proto.String(".kms.v1.NamespaceRef")},
					{Name: proto.String("key"), Number: proto.Int32(2), Label: &optional, Type: &stringType},
				},
			},
			{
				Name: proto.String("PutSecretRequest"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("ref"), Number: proto.Int32(1), Label: &optional, Type: &message, TypeName: proto.String(".kms.v1.ResourceRef")},
					{Name: proto.String("value"), Number: proto.Int32(2), Label: &optional, Type: &bytesType},
					{Name: proto.String("content_type"), Number: proto.Int32(3), Label: &optional, Type: &stringType},
					{Name: proto.String("metadata_json"), Number: proto.Int32(4), Label: &optional, Type: &stringType},
					{Name: proto.String("client_bound"), Number: proto.Int32(5), Label: &optional, Type: &boolType},
					{Name: proto.String("generate_access_token"), Number: proto.Int32(6), Label: &optional, Type: &boolType},
					{Name: proto.String("expires_at_unix_ms"), Number: proto.Int32(7), Label: &optional, Type: &int64Type},
				},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("build v0.2 descriptor: %v", err)
	}
	messages := file.Messages()
	namespace := dynamicpb.NewMessage(messages.ByName("NamespaceRef"))
	setV02Field(namespace, "env", protoreflect.ValueOfString(env))
	setV02Field(namespace, "app", protoreflect.ValueOfString(app))
	ref := dynamicpb.NewMessage(messages.ByName("ResourceRef"))
	setV02Field(ref, "namespace", protoreflect.ValueOfMessage(namespace))
	setV02Field(ref, "key", protoreflect.ValueOfString(key))
	request := dynamicpb.NewMessage(messages.ByName("PutSecretRequest"))
	setV02Field(request, "ref", protoreflect.ValueOfMessage(ref))
	setV02Field(request, "value", protoreflect.ValueOfBytes(value))
	setV02Field(request, "client_bound", protoreflect.ValueOfBool(clientBound))
	// v0.2 required token generation (or an existing token) when client_bound
	// was requested, so this is a semantically valid stale-client request.
	setV02Field(request, "generate_access_token", protoreflect.ValueOfBool(true))
	return request
}

func setV02Field(message *dynamicpb.Message, name protoreflect.Name, value protoreflect.Value) {
	message.Set(message.Descriptor().Fields().ByName(name), value)
}
