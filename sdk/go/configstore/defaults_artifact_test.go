package configstore

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"reflect"
	"strings"
	"testing"
)

const testSchemaSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestDefaultsArtifactDeterministicRoundTrip(t *testing.T) {
	artifact := DefaultsArtifact{
		Format:       DefaultsArtifactFormat,
		Profile:      "dev",
		SchemaSHA256: testSchemaSHA256,
		Contract: []ContractEntry{
			{Alias: "token", Kind: ContractKindSecret},
			{Alias: "database", Kind: ContractKindParameter, ContentType: "json"},
		},
		Parameters: []DefaultsParameter{{
			Alias: "database", ContentType: "json", Value: "{\"html\":\"<safe>\",\"line\":\"a\\nb\"}",
		}},
	}
	want := "{\"format\":\"kms-config-defaults/v1\",\"profile\":\"dev\",\"schema_sha256\":\"" + testSchemaSHA256 + "\",\"contract\":[{\"alias\":\"database\",\"kind\":\"parameter\",\"content_type\":\"json\"},{\"alias\":\"token\",\"kind\":\"secret\",\"content_type\":\"\"}],\"parameters\":[{\"alias\":\"database\",\"content_type\":\"json\",\"value\":\"{\\\"html\\\":\\\"<safe>\\\",\\\"line\\\":\\\"a\\\\nb\\\"}\"}]}\n"

	first, err := EncodeDefaultsArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeDefaultsArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != want || !bytes.Equal(first, second) {
		t.Fatalf("encoded artifact differs:\n got: %s\nwant: %s", first, want)
	}
	if artifact.Contract[0].Alias != "token" {
		t.Fatal("encoder mutated the caller's contract order")
	}

	parsed, err := ParseDefaultsArtifact(first)
	if err != nil {
		t.Fatal(err)
	}
	wantArtifact := artifact
	wantArtifact.Contract = []ContractEntry{artifact.Contract[1], artifact.Contract[0]}
	if !reflect.DeepEqual(parsed, wantArtifact) {
		t.Fatalf("parsed artifact = %#v, want %#v", parsed, wantArtifact)
	}
	if parsed.Parameters[0].Value != artifact.Parameters[0].Value {
		t.Fatal("parser changed the exact encoded parameter value")
	}
}

func TestDefaultsArtifactCanonicalFixture(t *testing.T) {
	want, err := os.ReadFile("testdata/defaults_artifact_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := EncodeDefaultsArtifact(DefaultsArtifact{
		Format:       DefaultsArtifactFormat,
		Profile:      "development<>&\u2028inner",
		SchemaSHA256: testSchemaSHA256,
		Contract: []ContractEntry{
			{Alias: "secret-token", Kind: ContractKindSecret},
			{Alias: "runtime", Kind: ContractKindParameter, ContentType: "application/json"},
		},
		Parameters: []DefaultsParameter{{
			Alias: "runtime", ContentType: "application/json", Value: "{\"text\":\"<>&\u2028\u2029\"}",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("canonical fixture differs:\n got: %s\nwant: %s", got, want)
	}
	if _, err := ParseDefaultsArtifact(want); err != nil {
		t.Fatalf("parse canonical fixture: %v", err)
	}
}

func TestParseDefaultsArtifactRejectsMalformedContracts(t *testing.T) {
	valid := func() map[string]any {
		return map[string]any{
			"format":        DefaultsArtifactFormat,
			"profile":       "dev",
			"schema_sha256": testSchemaSHA256,
			"contract": []any{
				map[string]any{"alias": "database", "kind": "parameter", "content_type": "json"},
				map[string]any{"alias": "token", "kind": "secret", "content_type": ""},
			},
			"parameters": []any{
				map[string]any{"alias": "database", "content_type": "json", "value": "{}"},
			},
		}
	}
	tests := map[string]func(map[string]any){
		"unknown top-level field": func(value map[string]any) { value["secret_values"] = []any{} },
		"case mismatched field": func(value map[string]any) {
			value["Format"] = value["format"]
			delete(value, "format")
		},
		"unknown contract field": func(value map[string]any) {
			value["contract"].([]any)[1].(map[string]any)["value"] = "forbidden"
		},
		"missing field":    func(value map[string]any) { delete(value, "profile") },
		"uppercase digest": func(value map[string]any) { value["schema_sha256"] = strings.ToUpper(testSchemaSHA256) },
		"unsorted contract": func(value map[string]any) {
			entries := value["contract"].([]any)
			entries[0], entries[1] = entries[1], entries[0]
		},
		"duplicate contract alias": func(value map[string]any) {
			entries := value["contract"].([]any)
			entries[1].(map[string]any)["alias"] = "database"
		},
		"invalid kind": func(value map[string]any) {
			value["contract"].([]any)[0].(map[string]any)["kind"] = "schema"
		},
		"missing parameter": func(value map[string]any) { value["parameters"] = []any{} },
		"secret parameter": func(value map[string]any) {
			value["parameters"] = append(value["parameters"].([]any), map[string]any{"alias": "token", "content_type": "", "value": "forbidden"})
		},
		"content type mismatch": func(value map[string]any) {
			value["parameters"].([]any)[0].(map[string]any)["content_type"] = "text"
		},
		"missing secret content type": func(value map[string]any) {
			delete(value["contract"].([]any)[1].(map[string]any), "content_type")
		},
		"missing parameter value": func(value map[string]any) {
			delete(value["parameters"].([]any)[0].(map[string]any), "value")
		},
		"duplicate parameter alias": func(value map[string]any) {
			value["parameters"] = append(value["parameters"].([]any), map[string]any{"alias": "database", "content_type": "json", "value": "{}"})
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := valid()
			mutate(value)
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseDefaultsArtifact(data); err == nil {
				t.Fatal("malformed artifact was accepted")
			}
		})
	}

	data, err := json.Marshal(valid())
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte(" {}")...)
	if _, err := ParseDefaultsArtifact(data); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
	for _, duplicate := range []string{
		`{"format":"kms-config-defaults/v1","format":"kms-config-defaults/v1","profile":"dev","schema_sha256":"` + testSchemaSHA256 + `","contract":[],"parameters":[]}`,
		`{"format":"kms-config-defaults/v1","profile":"dev","schema_sha256":"` + testSchemaSHA256 + `","contract":[{"alias":"token","alias":"token","kind":"secret","content_type":""}],"parameters":[]}`,
	} {
		if _, err := ParseDefaultsArtifact([]byte(duplicate)); err == nil || !strings.Contains(err.Error(), "duplicate object member name") {
			t.Fatalf("duplicate JSON field error = %v", err)
		}
	}
}

func TestDefaultsArtifactLimits(t *testing.T) {
	artifact := DefaultsArtifact{
		Format:       DefaultsArtifactFormat,
		Profile:      "dev",
		SchemaSHA256: testSchemaSHA256,
		Contract: []ContractEntry{{
			Alias: "group", Kind: ContractKindParameter, ContentType: "json",
		}},
		Parameters: []DefaultsParameter{{
			Alias: "group", ContentType: "json", Value: strings.Repeat("x", MaxDefaultsParameterValueSize+1),
		}},
	}
	if _, err := EncodeDefaultsArtifact(artifact); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("oversized parameter error = %v", err)
	}
	oversizedWire := `{"format":"kms-config-defaults/v1","profile":"dev","schema_sha256":"` + testSchemaSHA256 + `","contract":[{"alias":"group","kind":"parameter","content_type":"json"}],"parameters":[{"alias":"group","content_type":"json","value":"` + strings.Repeat("x", MaxDefaultsParameterValueSize+1) + `"}]}`
	if _, err := ParseDefaultsArtifact([]byte(oversizedWire)); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("parsed oversized parameter error = %v", err)
	}
	if _, err := ParseDefaultsArtifact(bytes.Repeat([]byte{' '}, MaxDefaultsArtifactSize+1)); err == nil || !strings.Contains(err.Error(), "4 MiB") {
		t.Fatalf("oversized artifact error = %v", err)
	}

	artifact.Contract = nil
	artifact.Parameters = nil
	for _, alias := range []string{"a", "b", "c", "d"} {
		artifact.Contract = append(artifact.Contract, ContractEntry{Alias: alias, Kind: ContractKindParameter, ContentType: "text"})
		artifact.Parameters = append(artifact.Parameters, DefaultsParameter{Alias: alias, ContentType: "text", Value: strings.Repeat("x", MaxDefaultsParameterValueSize)})
	}
	if _, err := EncodeDefaultsArtifact(artifact); err == nil || !strings.Contains(err.Error(), "4 MiB") {
		t.Fatalf("total-size error = %v", err)
	}
}

func TestParseDefaultsArtifactRequiresPresentArrays(t *testing.T) {
	for _, data := range []string{
		`{"format":"kms-config-defaults/v1","profile":"dev","schema_sha256":"` + testSchemaSHA256 + `","contract":null,"parameters":[]}`,
		`{"format":"kms-config-defaults/v1","profile":"dev","schema_sha256":"` + testSchemaSHA256 + `","contract":[],"parameters":null}`,
	} {
		if _, err := ParseDefaultsArtifact([]byte(data)); err == nil {
			t.Fatalf("missing array accepted: %s", data)
		}
	}
}

func TestDefaultsArtifactRejectsInvalidUTF8AndAcceptsEmptyValues(t *testing.T) {
	invalidUTF8 := []byte(`{"format":"kms-config-defaults/v1","profile":"dev","schema_sha256":"` + testSchemaSHA256 + `","contract":[],"parameters":[],"extra":"`)
	invalidUTF8 = append(invalidUTF8, 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}`)...)
	if _, err := ParseDefaultsArtifact(invalidUTF8); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}

	encoded, err := EncodeDefaultsArtifact(DefaultsArtifact{
		Format:       DefaultsArtifactFormat,
		Profile:      "dev",
		SchemaSHA256: testSchemaSHA256,
		Contract: []ContractEntry{{
			Alias: "empty", Kind: ContractKindParameter, ContentType: "text/plain",
		}},
		Parameters: []DefaultsParameter{{Alias: "empty", ContentType: "text/plain", Value: ""}},
	})
	if err != nil {
		t.Fatalf("empty parameter value: %v", err)
	}
	parsed, err := ParseDefaultsArtifact(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Parameters[0].Value != "" {
		t.Fatalf("empty parameter changed to %q", parsed.Parameters[0].Value)
	}
}

func TestDefaultsArtifactCanonicalWhitespace(t *testing.T) {
	for _, profile := range []string{" dev", "dev\t", "\u00a0dev"} {
		_, err := EncodeDefaultsArtifact(DefaultsArtifact{
			Format: DefaultsArtifactFormat, Profile: profile, SchemaSHA256: testSchemaSHA256,
			Contract: []ContractEntry{{Alias: "token", Kind: ContractKindSecret}},
		})
		if err == nil {
			t.Fatalf("noncanonical profile %q was accepted", profile)
		}
	}
	for _, contentType := range []string{" json", "json\n"} {
		_, err := EncodeDefaultsArtifact(DefaultsArtifact{
			Format: DefaultsArtifactFormat, Profile: "dev", SchemaSHA256: testSchemaSHA256,
			Contract:   []ContractEntry{{Alias: "group", Kind: ContractKindParameter, ContentType: contentType}},
			Parameters: []DefaultsParameter{{Alias: "group", ContentType: contentType}},
		})
		if err == nil {
			t.Fatalf("noncanonical content type %q was accepted", contentType)
		}
	}
}

func BenchmarkParseDefaultsArtifact(b *testing.B) {
	encoded, err := EncodeDefaultsArtifact(DefaultsArtifact{
		Format:       DefaultsArtifactFormat,
		Profile:      "production",
		SchemaSHA256: testSchemaSHA256,
		Contract: []ContractEntry{
			{Alias: "database", Kind: ContractKindParameter, ContentType: "json"},
			{Alias: "limits", Kind: ContractKindParameter, ContentType: "json"},
			{Alias: "token", Kind: ContractKindSecret},
		},
		Parameters: []DefaultsParameter{
			{Alias: "database", ContentType: "json", Value: `{"host":"db.internal","port":5432}`},
			{Alias: "limits", ContentType: "json", Value: `{"burst":20,"rate":10}`},
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := ParseDefaultsArtifact(encoded); err != nil {
			b.Fatal(err)
		}
	}
}
