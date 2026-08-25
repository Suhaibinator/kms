package grpcserver

import (
	"bytes"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

func nearLimitDefaultsArtifact(t *testing.T) []byte {
	t.Helper()
	contract := make([]configstore.ContractEntry, 4)
	parameters := make([]configstore.DefaultsParameter, 4)
	for i := range contract {
		alias := "p" + string(rune('a'+i))
		contract[i] = configstore.ContractEntry{Alias: alias, Kind: configstore.ContractKindParameter, ContentType: "string"}
		parameters[i] = configstore.DefaultsParameter{Alias: alias, ContentType: "string"}
	}
	base, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev", SchemaSHA256: strings.Repeat("0", 64),
		Contract: contract, Parameters: parameters,
	})
	if err != nil {
		t.Fatal(err)
	}
	remaining := configstore.MaxDefaultsArtifactSize - len(base)
	for i := range parameters {
		size := min(remaining, configstore.MaxDefaultsParameterValueSize)
		parameters[i].Value = strings.Repeat("x", size)
		remaining -= size
	}
	if remaining != 0 {
		t.Fatalf("could not fill defaults artifact to transport boundary: %d bytes remain", remaining)
	}
	artifact, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev", SchemaSHA256: strings.Repeat("0", 64),
		Contract: contract, Parameters: parameters,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact) != configstore.MaxDefaultsArtifactSize {
		t.Fatalf("near-limit artifact size = %d, want %d", len(artifact), configstore.MaxDefaultsArtifactSize)
	}
	return artifact
}

func TestApplicationDefaultsGRPCTransportAndAuthorization(t *testing.T) {
	env := newTestEnv(t, true)
	artifact, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev", SchemaSHA256: strings.Repeat("0", 64),
		Contract:   []configstore.ContractEntry{{Alias: "runtime", Kind: configstore.ContractKindParameter, ContentType: "string"}},
		Parameters: []configstore.DefaultsParameter{{Alias: "runtime", ContentType: "string", Value: "default"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.admin().ApplyApplicationDefaults(clientCtx(), &kmsv1.ApplyApplicationDefaultsRequest{
		Namespace: pNS("dev", "worker"), Artifact: artifact,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("client defaults code = %v err=%v", status.Code(err), err)
	}
	_, err = env.admin().ApplyApplicationDefaults(adminCtx(), &kmsv1.ApplyApplicationDefaultsRequest{
		Namespace: pNS("dev", "worker"), Artifact: []byte(`{"unknown":"do-not-echo"}`),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("malformed defaults code = %v err=%v", status.Code(err), err)
	}
	if strings.Contains(err.Error(), "do-not-echo") {
		t.Fatalf("gRPC error leaked artifact bytes: %v", err)
	}
}

func TestApplicationDefaultsGRPCAcceptsArtifactAtParserLimit(t *testing.T) {
	env := newTestEnv(t, true)
	artifact := nearLimitDefaultsArtifact(t)

	_, err := env.admin().ApplyApplicationDefaults(adminCtx(), &kmsv1.ApplyApplicationDefaultsRequest{
		Namespace: pNS("dev", "worker"), Artifact: artifact,
	})
	// The memStore deliberately lacks application/defaults management. Reaching
	// FailedPrecondition proves transport decoding and the 4 MiB artifact parser
	// both completed; the former default would fail earlier as ResourceExhausted.
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("near-limit defaults code = %v err=%v", status.Code(err), err)
	}

	_, err = env.admin().ApplyApplicationDefaults(adminCtx(), &kmsv1.ApplyApplicationDefaultsRequest{
		Namespace: pNS("dev", "worker"),
		Artifact:  bytes.Repeat([]byte{'x'}, configstore.MaxDefaultsArtifactSize+1),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("oversize defaults code = %v err=%v", status.Code(err), err)
	}
}
