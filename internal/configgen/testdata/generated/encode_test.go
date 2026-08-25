package generated

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	rootconfig "github.com/Suhaibinator/kms/internal/configgen/testdata/valid"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

func TestEncodeParameterGroupsCanonicalRoundTrip(t *testing.T) {
	ratio := float32(0.25)
	root := &rootconfig.Config{
		Endpoint: rootconfig.Endpoint{
			Host:  "db.internal",
			Ports: []uint16{},
			Labels: map[string][]string{
				"empty": {},
				"nil":   nil,
			},
			Zones: [2]string{"west-a", "west-b"},
		},
		Timeout: 1500 * time.Millisecond,
		Limit:   rootconfig.RetryLimit(7),
		Ratio:   &ratio,
		Payload: []byte{},
		Password: kmsclient.NewSecret(
			[]byte("must-never-appear-in-parameter-groups"),
		),
	}

	groups, err := EncodeParameterGroups(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("group count = %d, want 2", len(groups))
	}
	if got, want := string(groups["database"]), `{"endpoint":{"host":"db.internal","labels":{"empty":[],"nil":null},"ports":[],"zones":["west-a","west-b"]},"timeout":"1.5s"}`; got != want {
		t.Fatalf("database group = %s, want %s", got, want)
	}
	if got, want := string(groups["rate_limits"]), `{"limit":7,"payload":"","ratio":0.25}`; got != want {
		t.Fatalf("rate_limits group = %s, want %s", got, want)
	}
	for alias, document := range groups {
		if string(document) == "" || json.Valid(document) == false {
			t.Fatalf("group %q is not valid JSON: %q", alias, document)
		}
		if contents := string(document); strings.Contains(contents, "database_password") || strings.Contains(contents, "must-never-appear-in-parameter-groups") {
			t.Fatalf("group %q exposed a secret: %s", alias, document)
		}
	}

	decoded := &rootconfig.Config{}
	if err := configstore.DecodeGroup(string(groups["database"]), decoded, groupFields0); err != nil {
		t.Fatal(err)
	}
	if err := configstore.DecodeGroup(string(groups["rate_limits"]), decoded, groupFields1); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Endpoint, root.Endpoint) || decoded.Timeout != root.Timeout || decoded.Limit != root.Limit || !reflect.DeepEqual(decoded.Ratio, root.Ratio) || !reflect.DeepEqual(decoded.Payload, root.Payload) {
		t.Fatalf("round trip changed parameter values:\n got: %#v\nwant: %#v", decoded, root)
	}
	if !decoded.Password.IsZero() {
		t.Fatal("parameter group round trip populated a secret")
	}
}

func TestEncodeParameterGroupsPreservesNilCollections(t *testing.T) {
	root := &rootconfig.Config{Timeout: time.Second}
	groups, err := EncodeParameterGroups(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(groups["database"]), `{"endpoint":{"host":"","labels":null,"ports":null,"zones":["",""]},"timeout":"1s"}`; got != want {
		t.Fatalf("database group = %s, want %s", got, want)
	}
	if got, want := string(groups["rate_limits"]), `{"limit":0,"payload":null,"ratio":null}`; got != want {
		t.Fatalf("rate_limits group = %s, want %s", got, want)
	}
}

func TestEncodeParameterGroupsReturnsJSONError(t *testing.T) {
	notANumber := float32(math.NaN())
	_, err := EncodeParameterGroups(&rootconfig.Config{Ratio: &notANumber})
	if err == nil {
		t.Fatal("NaN was encoded")
	}
	var unsupported *json.UnsupportedValueError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %T: %v, want wrapped *json.UnsupportedValueError", err, err)
	}
}

func TestEncodeDefaultsArtifactIncludesCompleteContractWithoutSecretValues(t *testing.T) {
	root := &rootconfig.Config{
		Timeout: time.Second,
		Password: kmsclient.NewSecret(
			[]byte("must-never-appear-in-defaults-artifact"),
		),
	}
	data, err := EncodeDefaultsArtifact("dev", root)
	if err != nil {
		t.Fatal(err)
	}
	if data[len(data)-1] != '\n' {
		t.Fatal("defaults artifact is not newline terminated")
	}
	if strings.Contains(string(data), "must-never-appear-in-defaults-artifact") {
		t.Fatal("defaults artifact exposed a secret value")
	}
	artifact, err := configstore.ParseDefaultsArtifact(data)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Profile != "dev" || artifact.SchemaSHA256 != generatedSchemaSHA256 {
		t.Fatalf("artifact identity = profile:%q schema:%q", artifact.Profile, artifact.SchemaSHA256)
	}
	if len(artifact.Contract) != 3 || artifact.Contract[0].Alias != "database" || artifact.Contract[1].Alias != "database_password" || artifact.Contract[1].Kind != configstore.ContractKindSecret || artifact.Contract[2].Alias != "rate_limits" {
		t.Fatalf("artifact contract = %#v", artifact.Contract)
	}
	groups, err := EncodeParameterGroups(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Parameters) != 2 || artifact.Parameters[0].Alias != "database" || artifact.Parameters[0].Value != string(groups["database"]) || artifact.Parameters[1].Alias != "rate_limits" || artifact.Parameters[1].Value != string(groups["rate_limits"]) {
		t.Fatalf("artifact parameters = %#v", artifact.Parameters)
	}
}
