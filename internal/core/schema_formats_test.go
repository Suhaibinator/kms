package core

import (
	"testing"
)

func exactJSONNumber(t *testing.T, raw string) any {
	t.Helper()
	value, err := decodeStrictJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestCompileSchemaAssertsGeneratedFormats(t *testing.T) {
	schema, err := compileSchema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"timeout":{"type":"string","format":"go-duration"},
			"payload":{"type":"string","format":"kms-base64"}
		},
		"required":["timeout","payload"],
		"additionalProperties":false
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(map[string]any{"timeout": "30s", "payload": "aGVsbG8="}); err != nil {
		t.Fatalf("valid generated formats rejected: %v", err)
	}
	for name, value := range map[string]map[string]any{
		"duration":               {"timeout": "P1D", "payload": "aGVsbG8="},
		"base64":                 {"timeout": "30s", "payload": "%%%"},
		"base64 newline":         {"timeout": "30s", "payload": "aGVs\nbG8="},
		"base64 padding bits":    {"timeout": "30s", "payload": "Zh=="},
		"base64 missing padding": {"timeout": "30s", "payload": "aGVsbG8"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := schema.Validate(value); err == nil {
				t.Fatal("invalid generated format was accepted")
			}
		})
	}
}

func TestCompileSchemaLeavesUnrelatedFormatsAsAnnotations(t *testing.T) {
	schema, err := compileSchema(`{
		"type":"object",
		"properties":{
			"timeout":{"type":"string","format":"go-duration"},
			"contact":{"type":"string","format":"email"}
		},
		"required":["timeout","contact"]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(map[string]any{"timeout": "1s", "contact": "not-an-email"}); err != nil {
		t.Fatalf("unrelated format unexpectedly asserted: %v", err)
	}
}

func TestCompileSchemaDoesNotRewriteFormatPropertiesInsideConst(t *testing.T) {
	schema, err := compileSchema(`{
		"anyOf":[
			{"type":"string","format":"go-duration"},
			{"const":{"format":"email","x":1}}
		]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(map[string]any{"format": "email", "x": exactJSONNumber(t, "1")}); err != nil {
		t.Fatalf("const instance was changed while configuring formats: %v", err)
	}
	if err := schema.Validate(map[string]any{"x": exactJSONNumber(t, "1")}); err == nil {
		t.Fatal("schema accepted the const value only after its format property was removed")
	}
}

func TestCompileSchemaRejectsDuplicatePropertiesAndPreservesNumericBounds(t *testing.T) {
	if _, err := compileSchema(`{"type":"object","type":"array"}`); err == nil {
		t.Fatal("duplicate schema property was accepted")
	}
	schema, err := compileSchema(`{"type":"integer","maximum":9007199254740992}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(exactJSONNumber(t, "9007199254740993")); err == nil {
		t.Fatal("integer above the exact bound was accepted")
	}
}
