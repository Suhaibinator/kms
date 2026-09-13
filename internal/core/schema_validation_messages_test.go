package core

import (
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"math/big"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

func TestSchemaValidationMessagesDescribeMissingFieldsWithoutValues(t *testing.T) {
	schema, err := compileSchema(`{"type":"object","properties":{"oauth":{"type":"object","required":["client_id","redirect_uri"],"properties":{"token":{"type":"integer"}}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	err = schema.Validate(map[string]any{"oauth": map[string]any{"token": "private-token-do-not-show"}})
	if err == nil {
		t.Fatal("expected validation failure")
	}
	problems := sanitizeSchemaErrors(err)
	if len(problems) != 2 {
		t.Fatalf("problems=%+v", problems)
	}
	var missing bool
	for _, problem := range problems {
		if problem.Alias != "oauth" {
			t.Fatalf("wrong alias: %+v", problem)
		}
		if strings.Contains(problem.Message, "private-token") {
			t.Fatal("configuration value leaked")
		}
		switch problem.SchemaPointer {
		case "/required":
			if problem.InstancePointer != "/oauth" {
				t.Fatalf("required error must point at the object missing the fields: %+v", problem)
			}
			missing = strings.Contains(problem.Message, `"client_id"`) && strings.Contains(problem.Message, `"redirect_uri"`)
		case "/type":
			if problem.InstancePointer != "/oauth/token" {
				t.Fatalf("type error must point at the nested value: %+v", problem)
			}
		default:
			t.Fatalf("unexpected problem: %+v", problem)
		}
	}
	if !missing {
		t.Fatalf("missing actionable required fields: %+v", problems)
	}
}

func TestSchemaValidationMessagesDoNotEchoValidatorValues(t *testing.T) {
	const sensitive = "private-token-do-not-show"
	for _, rule := range []jsonschema.ErrorKind{&kind.Enum{Got: sensitive, Want: []any{sensitive}}, &kind.Pattern{Got: sensitive, Want: sensitive}} {
		message := actionableSchemaMessage(rule)
		if strings.Contains(message, sensitive) {
			t.Fatal("configuration value leaked")
		}
	}
}

func TestSchemaValidationMessagesDescribeBounds(t *testing.T) {
	got := actionableSchemaMessage(&kind.Minimum{Want: big.NewRat(5, 1), Got: big.NewRat(1, 1)})
	if got != "Use a value greater than or equal to 5." {
		t.Fatal(got)
	}
}

func TestSchemaValidationInstancePointerNamesNestedValues(t *testing.T) {
	schema, err := compileSchema(`{"type":"object","properties":{"settings":{"type":"object","properties":{"pool":{"type":"object","properties":{"min":{"type":"integer","minimum":1}}},"hosts":{"type":"array","items":{"type":"string","minLength":1}},"a/b":{"type":"object","properties":{"c~d":{"type":"boolean"}}}}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	err = schema.Validate(map[string]any{"settings": map[string]any{
		"pool":  map[string]any{"min": float64(0)},
		"hosts": []any{"ok", ""},
		"a/b":   map[string]any{"c~d": "private-token-do-not-show"},
	}})
	if err == nil {
		t.Fatal("expected validation failure")
	}
	problems := sanitizeSchemaErrors(err)
	want := map[string]string{
		"/minimum":   "/settings/pool/min",
		"/minLength": "/settings/hosts/1",
		"/type":      "/settings/a~1b/c~0d",
	}
	if len(problems) != len(want) {
		t.Fatalf("problems=%+v", problems)
	}
	for _, problem := range problems {
		if problem.Alias != "settings" {
			t.Fatalf("wrong alias: %+v", problem)
		}
		if strings.Contains(problem.InstancePointer, "private-token") || strings.Contains(problem.Message, "private-token") {
			t.Fatalf("configuration value leaked: %+v", problem)
		}
		if got, ok := want[problem.SchemaPointer]; !ok || problem.InstancePointer != got {
			t.Fatalf("instance pointer for %s = %q, want %q", problem.SchemaPointer, problem.InstancePointer, got)
		}
	}
}

func TestSchemaValidationInstancePointerIsEmptyForReleaseLevelErrors(t *testing.T) {
	schema, err := compileSchema(`{"type":"object","required":["settings"]}`)
	if err != nil {
		t.Fatal(err)
	}
	err = schema.Validate(map[string]any{})
	if err == nil {
		t.Fatal("expected validation failure")
	}
	problems := sanitizeSchemaErrors(err)
	if len(problems) != 1 || problems[0].Alias != "" || problems[0].InstancePointer != "" || problems[0].SchemaPointer != "/required" {
		t.Fatalf("problems=%+v", problems)
	}
	if got := instancePointer(nil); got != "" {
		t.Fatalf("instancePointer(nil) = %q", got)
	}
}
