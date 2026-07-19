package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

func TestReadReleaseDefinitionIsStrictAndBuildsExactSelectors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "release.yaml")
	definition := `
namespace: prod/app
name: runtime
schema_id: app/runtime
schema_version: 2
entries:
  - alias: settings
    kind: parameter
    key: config/settings
    label: current
  - alias: password
    kind: secret
    key: /shared/data/db-password
    version: 7
`
	if err := os.WriteFile(path, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	cli := &CLI{}
	parsed, err := cli.readReleaseDefinition(path)
	if err != nil {
		t.Fatal(err)
	}
	req, err := releaseCreateRequest(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if req.GetNamespace().GetEnv() != "prod" || req.GetNamespace().GetApp() != "app" || req.GetSchemaVersion() != 2 {
		t.Fatalf("request identity = %#v", req)
	}
	if got := displayPath(req.GetEntries()[0].GetRef()); got != "/prod/app/config/settings" {
		t.Fatalf("relative entry path = %q", got)
	}
	if req.GetEntries()[0].GetLabel() != "current" || req.GetEntries()[0].GetVersion() != 0 {
		t.Fatalf("label selector = %#v", req.GetEntries()[0])
	}
	if got := displayPath(req.GetEntries()[1].GetRef()); got != "/shared/data/db-password" {
		t.Fatalf("absolute entry path = %q", got)
	}

	unknown := filepath.Join(dir, "unknown.yaml")
	if err := os.WriteFile(unknown, []byte("namespace: prod/app\nname: runtime\nunknown_field: true\nentries: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.readReleaseDefinition(unknown); err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("unknown field error = %v", err)
	}
}

func TestReleaseCreateRequestRejectsAmbiguousAndDuplicateEntries(t *testing.T) {
	_, err := releaseCreateRequest(releaseDefinition{
		Namespace: "prod/app", Name: "runtime",
		Entries: []releaseEntryDefinition{
			{Alias: "x", Kind: "parameter", Key: "a", Version: 1, Label: "current"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "version or label") {
		t.Fatalf("ambiguous selector error = %v", err)
	}
	_, err = releaseCreateRequest(releaseDefinition{
		Namespace: "prod/app", Name: "runtime",
		Entries: []releaseEntryDefinition{
			{Alias: "x", Kind: "parameter", Key: "a", Version: 1},
			{Alias: "x", Kind: "secret", Key: "b", Version: 2},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate alias") {
		t.Fatalf("duplicate alias error = %v", err)
	}
}

func TestPrintReleaseDiffNeverRendersSecretMaterial(t *testing.T) {
	secretRef := &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: "prod", App: "app"}, Key: "password"}
	parameterRef := &kmsv1.ResourceRef{Namespace: &kmsv1.NamespaceRef{Env: "prod", App: "app"}, Key: "settings"}
	from := &kmsv1.ConfigurationRelease{Entries: []*kmsv1.ConfigurationReleaseEntry{
		{Alias: "password", Kind: "secret", Ref: secretRef, Version: 1, MetadataJson: `{"do_not_print":"secret-plaintext"}`},
		{Alias: "settings", Kind: "parameter", Ref: parameterRef, Version: 1, ParameterDigest: "digest-one"},
	}}
	to := &kmsv1.ConfigurationRelease{Entries: []*kmsv1.ConfigurationReleaseEntry{
		{Alias: "password", Kind: "secret", Ref: secretRef, Version: 2, MetadataJson: `{"do_not_print":"new-secret-plaintext"}`},
		{Alias: "settings", Kind: "parameter", Ref: parameterRef, Version: 2, ParameterDigest: "digest-two"},
	}}
	var output bytes.Buffer
	printReleaseDiff(&output, from, to)
	text := output.String()
	if strings.Contains(text, "secret-plaintext") || strings.Contains(text, "do_not_print") {
		t.Fatalf("diff leaked secret metadata/value: %s", text)
	}
	for _, want := range []string{"password", "1 -> 2", "digest-one -> digest-two"} {
		if !strings.Contains(text, want) {
			t.Fatalf("diff missing %q: %s", want, text)
		}
	}
}

func TestOptionalExpectedCurrentVersionTracksPresenceOfZero(t *testing.T) {
	var value optionalUint64
	if value.set {
		t.Fatal("zero value should be absent")
	}
	if err := value.Set("0"); err != nil {
		t.Fatal(err)
	}
	if !value.set || value.value != 0 {
		t.Fatalf("optional value = %+v", value)
	}
}
